// Package config loads and validates store.yaml — the factory's single source of
// truth. One file configures both the proto/DB side (datasources, telemetry,
// schema naming, inherited from the backend package) and the GraphQL side (the
// graphql source block and the generate list). Decoding is strict (unknown keys error)
// and Validate runs before any generation, aggregating every problem with a keyed
// path so misconfiguration fails fast and legibly instead of producing bad output.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
	"github.com/the-protobuf-project/store/plugin/factory/source/proto/backend"
)

// Config is the whole store.yaml. The proto/DB keys (datasources, strip_version,
// dedupe_schema_table, telemetry) are inlined from backend.Config so the proto
// plugin path keeps reading them unchanged; GraphQL and Generate are the factory
// additions.
type Config struct {
	backend.Config `yaml:",inline"`

	// Version is an optional schema-version marker for forward compatibility.
	Version int `yaml:"version"`

	// GraphQL configures the GraphQL source (introspection input + conventions).
	GraphQL *GraphQL `yaml:"graphql"`

	// Generate is the daisy chain: which targets to emit and how.
	Generate []GenerateEntry `yaml:"generate"`
}

// GraphQL is the store.yaml `graphql:` block.
type GraphQL struct {
	Endpoint    string   `yaml:"endpoint"`
	Schema      string   `yaml:"schema"`
	AdminSecret string   `yaml:"admin_secret"`
	Headers     []string `yaml:"headers"`
	Dialect     string   `yaml:"dialect"`
	MaxDepth    *int     `yaml:"max_depth"`
	Scalars     []string `yaml:"scalars"`
}

// GenerateEntry is one target invocation in the `generate:` list.
type GenerateEntry struct {
	Target        string `yaml:"target"`
	Source        string `yaml:"source"`
	Lang          string `yaml:"lang"`
	Out           string `yaml:"out"`
	GoModule      string `yaml:"go_module"`
	Package       string `yaml:"package"`
	RuntimeModule string `yaml:"runtime_module"`
	DumpSchema    bool   `yaml:"dump_schema"`

	// gorm-target knobs (ignored by other targets).
	Stores     bool  `yaml:"stores"`
	Converters bool  `yaml:"converters"`
	Filters    bool  `yaml:"filters"`
	Telemetry  *bool `yaml:"telemetry"`
}

// GraphQLEntry returns the first `generate:` entry targeting the graphql client,
// or a zero entry when none is configured (the caller then falls back to plugin
// opts / defaults for go_module and package).
func (c *Config) GraphQLEntry() GenerateEntry {
	for _, e := range c.Generate {
		if e.Target == "graphql" {
			return e
		}
	}
	return GenerateEntry{}
}

// Load reads and strictly decodes store.yaml from path. Unknown keys are rejected.
// A blank path yields a nil Config (no file; defaults apply).
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return &c, nil
}

// langOK reports whether lang is in the target's supported set (empty lang means
// the target default, always allowed).
func langOK(t factory.Target[*coreir.Model], lang string) bool {
	if lang == "" {
		return true
	}
	for _, l := range t.Languages() {
		if l == lang {
			return true
		}
	}
	return false
}

// Validate checks the config against the registered sources/targets and the
// dialect registry, returning all problems joined into one error (nil if valid).
func (c *Config) Validate(reg *factory.Registry[*coreir.Model]) error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.GraphQL != nil {
		g := c.GraphQL
		switch {
		case g.Endpoint == "" && g.Schema == "":
			add("graphql: exactly one of `endpoint` or `schema` is required (got neither)")
		case g.Endpoint != "" && g.Schema != "":
			add("graphql: `endpoint` and `schema` are mutually exclusive (set only one)")
		}
		if g.Dialect != "" {
			if _, err := dialect.Get(g.Dialect); err != nil {
				add("graphql.dialect: %v", err)
			}
		}
		for i, sc := range g.Scalars {
			if !strings.Contains(sc, "=") {
				add("graphql.scalars[%d]: %q must be in Name=GoType form", i, sc)
			}
		}
		for i, h := range g.Headers {
			if !strings.Contains(h, ":") {
				add("graphql.headers[%d]: %q must be in 'Key: Value' form", i, h)
			}
		}
	}

	// `generate:` entries carry per-target settings. In the plugin-hosted model buf
	// owns the output dir and selects the target (opt: [target=…]), so `out` is not
	// required here and `go_module` may come from a plugin opt — those are enforced
	// at run time. Validation covers the structural choices instead.
	for i, e := range c.Generate {
		at := fmt.Sprintf("generate[%d]", i)
		if e.Target == "" {
			add("%s.target: required", at)
			continue
		}
		tgt, ok := reg.Targets[e.Target]
		if !ok {
			add("%s.target: unknown target %q — registered: %s", at, e.Target, reg.TargetNames())
		}
		if e.Source != "" {
			if _, ok := reg.Sources[e.Source]; !ok {
				add("%s.source: unknown source %q", at, e.Source)
			}
		}
		if ok && !langOK(tgt, e.Lang) {
			add("%s.lang: target %q does not support %q (supports: %s)", at, e.Target, e.Lang, strings.Join(tgt.Languages(), ", "))
		}
		// the graphql target must be fed by a configured graphql source.
		if e.Target == "graphql" {
			if e.Source != "" && e.Source != "graphql" {
				add("%s.source: the graphql target requires source `graphql` (got %q)", at, e.Source)
			}
			if c.GraphQL == nil {
				add("%s: the graphql target requires a top-level `graphql:` block", at)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid store.yaml:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// ResolveSecret expands an "env:VAR" reference to the environment variable's
// value (erroring if unset), or returns a literal unchanged.
func ResolveSecret(spec string) (string, error) {
	if rest, ok := strings.CutPrefix(spec, "env:"); ok {
		v := os.Getenv(rest)
		if v == "" {
			return "", fmt.Errorf("environment variable %q is not set", rest)
		}
		return v, nil
	}
	return spec, nil
}
