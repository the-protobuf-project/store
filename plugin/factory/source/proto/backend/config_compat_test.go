package backend

// config_compat_test.go covers the orm.yaml → store.yaml rename window: which
// file a given config path actually resolves to, and whether the user is told.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigPath(t *testing.T) {
	// present names the files to create in the case's temp dir.
	cases := []struct {
		name     string
		present  []string
		ask      string
		want     string
		warns    bool
		wantsErr bool
	}{
		{
			name:    "store.yaml present is used silently",
			present: []string{"store.yaml"},
			ask:     "store.yaml",
			want:    "store.yaml",
		},
		{
			name:    "orm.yaml named outright still loads, with a warning",
			present: []string{"orm.yaml"},
			ask:     "orm.yaml",
			want:    "orm.yaml",
			warns:   true,
		},
		{
			// The migration window: buf.gen.yaml already says store.yaml, the file
			// on disk has not been renamed yet.
			name:    "missing store.yaml falls back to orm.yaml beside it",
			present: []string{"orm.yaml"},
			ask:     "store.yaml",
			want:    "orm.yaml",
			warns:   true,
		},
		{
			// A repo that has both has finished migrating; the old file is stale
			// and must not silently win.
			name:    "both present prefers store.yaml",
			present: []string{"store.yaml", "orm.yaml"},
			ask:     "store.yaml",
			want:    "store.yaml",
		},
		{
			// The error the user expects names the file they asked for.
			name: "neither present returns the requested path unchanged",
			ask:  "store.yaml",
			want: "store.yaml",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.present {
				writeFile(t, filepath.Join(dir, f), "strip_version: true\n")
			}

			var w strings.Builder
			got, err := resolveConfigPath(filepath.Join(dir, c.ask), &w)
			if (err != nil) != c.wantsErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantsErr)
			}
			if base := filepath.Base(got); base != c.want {
				t.Errorf("resolved %q, want %q", base, c.want)
			}
			if warned := strings.Contains(w.String(), "deprecated"); warned != c.warns {
				t.Errorf("warned = %v, want %v (output: %q)", warned, c.warns, w.String())
			}
		})
	}
}

// TestLoadConfigViaLegacyName proves the fallback is wired into LoadConfig, not
// just into the path helper — the config has to actually parse.
func TestLoadConfigViaLegacyName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "orm.yaml"), "strip_version: true\ndedupe_schema_table: true\n")

	cfg, err := LoadConfig(filepath.Join(dir, "store.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}
	if !cfg.StripVersion || !cfg.DedupeSchemaTable {
		t.Errorf("legacy config did not parse: %+v", cfg)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
