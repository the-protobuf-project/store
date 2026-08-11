package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/store/plugin/factory/config"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
	"github.com/the-protobuf-project/store/plugin/factory/wire"
)

func testRegistry() *factory.Registry[*coreir.Model] {
	// Validation only inspects the registry's shape, so no readers or layout.
	return wire.Registry(protokit.Options{}, nil, nil)
}

// loadFrom writes yaml to a temp file and loads it (exercising strict decode).
func loadFrom(t *testing.T, yaml string) (*config.Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "store.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return config.Load(p)
}

func TestLoad_StrictRejectsUnknownKey(t *testing.T) {
	_, err := loadFrom(t, "graphql:\n  endpoint: http://x/graphql\nbogus_key: 1\n")
	if err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Fatalf("expected unknown-key error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	reg := testRegistry()
	cases := []struct {
		name    string
		yaml    string
		wantErr string // substring; "" means valid
	}{
		{
			name: "valid full",
			yaml: `graphql:
  endpoint: http://x/graphql
  dialect: hasura
generate:
  - {target: graphql, source: graphql, out: gen, go_module: example.com/gen}
  - {target: gorm, source: proto, out: gen/gorm, go_module: example.com/gen, stores: true}
`,
		},
		{
			name:    "graphql neither endpoint nor schema",
			yaml:    "graphql:\n  dialect: hasura\n",
			wantErr: "exactly one of `endpoint` or `schema`",
		},
		{
			name:    "graphql both endpoint and schema",
			yaml:    "graphql:\n  endpoint: http://x\n  schema: s.json\n",
			wantErr: "mutually exclusive",
		},
		{
			name:    "unknown dialect",
			yaml:    "graphql:\n  endpoint: http://x\n  dialect: nope\n",
			wantErr: "unknown graphql dialect",
		},
		{
			name:    "unknown target",
			yaml:    "generate:\n  - {target: mongo, out: gen}\n",
			wantErr: "unknown target",
		},
		{
			name:    "graphql without graphql block",
			yaml:    "generate:\n  - {target: graphql, source: graphql, go_module: m}\n",
			wantErr: "requires a top-level `graphql:` block",
		},
		{
			name:    "graphql wrong source",
			yaml:    "graphql:\n  endpoint: http://x\ngenerate:\n  - {target: graphql, source: proto}\n",
			wantErr: "requires source `graphql`",
		},
		{
			name: "telemetry entry",
			yaml: "generate:\n  - {target: gorm, source: proto, go_module: m, telemetry: true}\n",
		},
		{
			name:    "missing target",
			yaml:    "generate:\n  - {source: proto, out: gen}\n",
			wantErr: "target: required",
		},
		{
			name:    "bad scalar form",
			yaml:    "graphql:\n  endpoint: http://x\n  scalars: [\"UUID\"]\n",
			wantErr: "Name=GoType form",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadFrom(t, tc.yaml)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			verr := cfg.Validate(reg)
			if tc.wantErr == "" {
				if verr != nil {
					t.Fatalf("expected valid, got %v", verr)
				}
				return
			}
			if verr == nil || !strings.Contains(verr.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, verr)
			}
		})
	}
}

func TestResolveSecret(t *testing.T) {
	t.Setenv("MY_SECRET", "s3cr3t")
	if v, err := config.ResolveSecret("env:MY_SECRET"); err != nil || v != "s3cr3t" {
		t.Fatalf("env resolve: %q %v", v, err)
	}
	if v, _ := config.ResolveSecret("literal"); v != "literal" {
		t.Fatalf("literal: %q", v)
	}
	if _, err := config.ResolveSecret("env:UNSET_VAR_XYZ"); err == nil {
		t.Fatal("expected error for unset env var")
	}
}
