// Command protoc-gen-store is a protoc plugin that reads proto descriptors
// annotated with google.api.* and orm.v1.* options, then generates database
// schema artifacts for the requested backend: gorm, sql, or prisma. The generic
// IR engine lives in the protokit module; the on-chain (solidity/subgraph)
// targets ship as the separate protoc-gen-web3 plugin.
//
// # Install
//
//	go install github.com/the-protobuf-project/store/plugin/cmd/protoc-gen-store@latest
//
// # Usage via buf.gen.yaml
//
//	plugins:
//	  - local: protoc-gen-store
//	    out: generated/
//	    opt:
//	      - target=prisma   # prisma | gorm | sql
//
// # Inference priority
//
//  1. google.api.* annotations — table, column, FK inference (~80%)
//  2. orm.v1.* options         — overrides: type, name, skip, unique, index
//  3. buf.gen.yaml opt:         — global defaults (target backend)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/store/plugin/factory/config"
	"github.com/the-protobuf-project/store/plugin/factory/source/proto/backend"
	"github.com/the-protobuf-project/store/plugin/factory/wire"
	telemetrygen "github.com/the-protobuf-project/store/telemetry"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/pluginpb"
)

// version is the build version, injected at release time via
// -ldflags "-X main.version=...".
var version = "dev"

// resolveVersion returns the build version to stamp into generated files.
// A release sets `version` via ldflags and wins outright. Otherwise we recover
// it from the build info the Go toolchain embeds: `go install …@v0.1.2` records
// the tag as the main module version, and when orm is consumed as a
// dependency its module entry carries the version. Only genuine local builds
// (`go build`/`go run`, which report "(devel)") fall back to "dev".
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, dep := range bi.Deps {
		if dep.Path == "github.com/the-protobuf-project/store" && dep.Version != "" {
			return dep.Version
		}
	}
	return version
}

func main() {
	v := resolveVersion()

	// When invoked directly with -version (not by protoc), print and exit before
	// protogen tries to read a CodeGeneratorRequest from stdin.
	if len(os.Args) == 2 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Printf("protoc-gen-store %s\n", v)
		return
	}

	// Every generated file's banner names the tool that produced it.
	header.SetTool("protoc-gen-store")

	// flags are populated by protogen (ParamFunc maps each buf.gen.yaml opt:
	// "key=value" to flags.Set) before the Run closure reads them.
	var flags flag.FlagSet
	target := flags.String("target", "", "output backend: gorm | sql | prisma")
	strict := flags.String("strict", "",
		"per-rule severity for schema problems: \"\"=all warn, \"true\"=all error, "+
			"or \"ref:error,collision:warn,index:error,lint:warn\"")
	config := flags.String("config", "",
		"path to an store.yaml mapping proto packages to databases/schemas")
	goModule := flags.String("go_module", "",
		"Go import path of the output directory (e.g. github.com/me/gen); the gorm "+
			"target needs it to generate the migration aggregator that imports each schema package")
	stores := flags.Bool("stores", false,
		"gorm target only: also generate a typed CRUD store per resource "+
			"(introduces a gorm.io/gorm dependency in each models package)")
	converters := flags.Bool("converters", false,
		"gorm target only: also generate proto↔model converters per schema "+
			"(<Model>ToProto / <Model>FromProto plus per-enum value mappers; introduces "+
			"a dependency on the generated proto packages)")
	telemetry := flags.Bool("telemetry", false,
		"gorm target only: fold first-party opentelementry instrumentation into the "+
			"generated output — instrumented stores (with stores), a telemetry "+
			"adapter/plugin package, a filterx observer (with filters), and "+
			"Registry.Instrument. Requires go_module; generated consumers gain a "+
			"github.com/the-protobuf-project/opentelementry/opentelementry-go dependency. "+
			"store.yaml telemetry: and telemetry.v1's (telemetry.v1.telemetry) annotations tune it further")
	filters := flags.Bool("filters", false,
		"gorm target only: also generate AIP-160 filter / AIP-132 order_by specs per "+
			"schema plus the shared filterx engine packages (a backend-neutral core and "+
			"gorm + hasura engines); requires go_module")
	gormModule := flags.String("gorm_module", "",
		"repository target only: Go import path of the generated gorm output the "+
			"repository adapters compose (models, stores, filterx)")
	graphqlModule := flags.String("graphql_module", "",
		"repository target only: Go import path of the generated GraphQL client the "+
			"graphql repository adapters compose; empty emits gorm-only repositories")

	protogen.Options{ParamFunc: flags.Set}.Run(func(p *protogen.Plugin) error {
		// Proto3 `optional` is fully supported (presence is read via field_behavior,
		// not synthetic oneofs); declare it so buf/protoc don't warn.
		p.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)

		// The graphql target reads a GraphQL endpoint from store.yaml rather than the
		// proto descriptors, so it takes its own path — but still runs as part of a
		// normal plugin invocation and returns files through the protoc response.
		if *target == "graphql" {
			return runGraphQL(p, *config, *goModule, v)
		}

		// orm owns its layout config (protokit has none): load it here and split it
		// across the two seams it feeds — the reader (which needs the telemetry
		// block and the render knobs) and the layout resolver (which owns the
		// database/schema naming policy).
		cfg, err := backend.LoadConfig(*config)
		if err != nil {
			return err
		}

		// Drive the proto build + render through the factory registry. In plugin mode
		// buf selects exactly one target via opt: [target=...] and owns the output dir.
		reader := backend.New(cfg, *goModule, *stores, *telemetry, *converters, *filters).
			WithRepositoryModules(*gormModule, *graphqlModule)
		// The compat reader keeps schemas written against orm.v1 generating for one
		// major; protokit warns once per deprecated option per build.
		reg := wire.Registry(
			protokit.Options{Target: *target, Strict: *strict, Version: v},
			[]protokit.FacetReader{reader, backend.NewCompat(), telemetrygen.NewReader()},
			backend.NewLayout(cfg))

		tgt, ok := reg.Targets[*target]
		if !ok {
			if *target == "" {
				return fmt.Errorf("required option \"target\" is missing — add opt: [target=%s] to your buf.gen.yaml plugin entry", reg.TargetNames())
			}
			return fmt.Errorf("unknown target %q — valid targets: %s", *target, reg.TargetNames())
		}

		ctx := factory.Ctx{Plugin: p}
		model, err := reg.Sources["proto"].Build(ctx)
		if err != nil {
			return err
		}
		return tgt.Generate(ctx, model, "go")
	})
}

// runGraphQL generates the typed GraphQL client during a plugin run. It reads the
// endpoint (or cached schema) and conventions from store.yaml's graphql block,
// introspects, and returns the generated files through the protoc response so buf
// writes them to the plugin entry's out: directory.
func runGraphQL(p *protogen.Plugin, configPath, goModuleOpt, version string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if cfg == nil || cfg.GraphQL == nil {
		return fmt.Errorf("target=graphql needs a `graphql:` block in store.yaml (set the config=<path> opt)")
	}
	// Validation only needs the registry's shape (which sources and targets exist),
	// so it is built with no readers and no layout policy.
	validationReg := wire.Registry(protokit.Options{}, nil, nil)
	if err := cfg.Validate(validationReg); err != nil {
		return err
	}

	g := cfg.GraphQL
	dl := dialect.Default()
	if g.Dialect != "" {
		if dl, err = dialect.Get(g.Dialect); err != nil {
			return err
		}
	}
	secret := g.AdminSecret
	if secret != "" {
		if secret, err = config.ResolveSecret(secret); err != nil {
			return err
		}
	}

	entry := cfg.GraphQLEntry()
	goModule := goModuleOpt
	if goModule == "" {
		goModule = entry.GoModule
	}
	if goModule == "" {
		return fmt.Errorf("the graphql target needs go_module (the go_module opt or a generate[].go_module in store.yaml)")
	}
	maxDepth := 1
	if g.MaxDepth != nil {
		maxDepth = *g.MaxDepth
	}

	sink := func(rel string, content []byte) error {
		_, err := p.NewGeneratedFile(rel, "").Write(content)
		return err
	}

	// Construct the graphql source and target through wire so main imports neither
	// same-named graphql package (see wire/graphqlsource.go, graphqltarget.go).
	source := wire.NewGraphQLSource(g.Endpoint, g.Schema, secret, g.Headers, dl)
	target := wire.NewGraphQLTarget(entry.Package, goModule, entry.RuntimeModule, version, maxDepth, parseScalars(g.Scalars), dl, sink)

	model, err := source.Build(factory.Ctx{})
	if err != nil {
		return err
	}
	if err := target.Generate(factory.Ctx{Plugin: p}, model, "go"); err != nil {
		return err
	}
	if entry.DumpSchema && model.RawGraphQL != nil {
		data, err := json.MarshalIndent(model.RawGraphQL, "", "  ")
		if err != nil {
			return err
		}
		return sink(filepath.Join(target.PackageName(), "schema.json"), data)
	}
	return nil
}

// parseScalars converts "Name=GoType" entries into a map.
func parseScalars(entries []string) map[string]string {
	out := map[string]string{}
	for _, e := range entries {
		if k, val, ok := strings.Cut(e, "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return out
}
