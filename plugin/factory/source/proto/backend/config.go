package backend

// config.go loads the optional store.yaml layout config (passed via the
// `config=<path>` plugin option) and resolves a proto package to its target
// database and postgres schema. This is what lets a multi-service monorepo split
// into several databases with clean schema names without annotating every file.
// Precedence: a per-file (orm.v1.datasource) annotation wins over the config,
// which in turn wins over the package-path defaults. The config is orm's alone —
// protokit owns no configuration; the Backend resolves grouping from it before
// handing protokit a fully-resolved Datasource.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the parsed store.yaml.
type Config struct {
	Datasources []matchRule `yaml:"datasources"`
	// StripVersion, when true, flattens the API version out of every derived
	// schema name ("bookstore.v1" → schema "bookstore" instead of "bookstore_v1").
	// It applies to resource-type-derived and config-derived schema names, but
	// never to an explicit (orm.v1.datasource).schema annotation. A per-rule
	// strip_version overrides this default for that rule.
	StripVersion bool `yaml:"strip_version"`

	// DedupeSchemaTable, when true, renames a table whose name would stutter with
	// its schema in a schema-qualified identifier ("booking" schema + "bookings"
	// table → "bookingBookings" in tools that join schema+table, e.g. Hasura).
	// The redundant leading schema word is stripped from the table name, or — for
	// the schema's eponymous/primary table, where stripping leaves nothing — the
	// table is renamed to a generic word ("resource", then "entity"/…). Only the
	// generated table name changes; proto/model names are untouched.
	DedupeSchemaTable bool `yaml:"dedupe_schema_table"`

	// Telemetry tunes the gorm target's first-party opentelementry
	// instrumentation (instrumented stores, the telemetry package, the
	// filterx observer, Registry.Instrument). Nil leaves the telemetry plugin
	// opt in charge. Replaces the removed `otel:` block.
	Telemetry *telemetryConfig `yaml:"telemetry"`
}

// telemetryConfig is the store.yaml `telemetry:` block. Every field is a pointer
// so an unset key inherits the plugin-opt default rather than the Go zero value.
type telemetryConfig struct {
	// Enabled overrides the telemetry plugin opt: true forces instrumentation on
	// for the tree, false strips it even when the opt enabled it.
	Enabled *bool `yaml:"enabled"`
	// Metrics, when explicitly false, drops the per-operation ops counter +
	// duration histogram tree-wide (spans and logs are unaffected). Defaults to
	// true. Per-table (telemetry.v1.telemetry).metrics narrows it further.
	Metrics *bool `yaml:"metrics"`
	// Logs, when explicitly false, drops the trace-correlated error logging the
	// telemetry adapter performs on failed operations. Defaults to true.
	Logs *bool `yaml:"logs"`
}

// matchRule assigns every proto package matching Match to a database and schema.
// Match is a dotted glob over the package: a trailing "**" matches any remaining
// segments ("fleet.tracking.**"); "*" matches exactly one segment; other
// segments match literally.
type matchRule struct {
	Match    string `yaml:"match"`
	Database string `yaml:"database"`
	// Schema is a literal or a template using {leaf} (the last package segment
	// with a trailing API version dropped). Empty falls back to SchemaDepth.
	Schema string `yaml:"schema"`
	// SchemaDepth, when >0 and Schema is empty, joins the first N package
	// segments with "_" to form the schema name.
	SchemaDepth int `yaml:"schema_depth"`
	// StripVersion overrides the top-level strip_version for packages this rule
	// matches. Nil (the default) inherits the global setting; set it explicitly
	// (true/false) to force version stripping on or off for this datasource.
	StripVersion *bool `yaml:"strip_version"`
}

// LoadConfig reads store.yaml from path. A blank path yields nil (no config;
// defaults apply).
//
// The file was called orm.yaml before the plugin was renamed. A path that names
// the old file still loads, with a deprecation warning on stderr — and so does a
// store.yaml path whose directory only has the old file, which is the case that
// actually matters: a buf.gen.yaml pinning `config=…/store.yaml` is what a user
// writes *after* reading the release notes, before they have moved the file.
// Warnings go to stderr because this runs before protokit's diagnostics exist.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	path, err := resolveConfigPath(path, os.Stderr)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // reject unknown keys instead of silently ignoring them
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	return &c, nil
}

// legacyConfigName is what store.yaml was called before the plugin was renamed
// from protoc-gen-orm to protoc-gen-store. Accepted for one major.
const legacyConfigName = "orm.yaml"

// resolveConfigPath returns the config file to actually read, warning to w when
// that turns out to be the deprecated orm.yaml.
//
// Two shapes are tolerated. Naming orm.yaml outright is the obvious one. The
// other is a store.yaml path that does not exist next to an orm.yaml that does:
// falling back there is what keeps a repo generating in the window between
// updating buf.gen.yaml and renaming the file, which is otherwise a confusing
// "no such file" for a config the user can plainly see.
//
// A path that exists is never second-guessed, and a missing store.yaml with no
// legacy neighbour is returned unchanged so the caller reports the error the
// user expects — naming the file they asked for, not one they have never heard of.
func resolveConfigPath(path string, w io.Writer) (string, error) {
	if filepath.Base(path) == legacyConfigName {
		warnLegacyConfig(path, w)
		return path, nil
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read config %q: %w", path, err)
	}
	legacy := filepath.Join(filepath.Dir(path), legacyConfigName)
	if _, err := os.Stat(legacy); err != nil {
		return path, nil // no fallback; let the caller report the original miss
	}
	warnLegacyConfig(legacy, w)
	return legacy, nil
}

// warnLegacyConfig emits the one-line rename notice.
func warnLegacyConfig(path string, w io.Writer) {
	// Best effort: a warning that cannot be written is not worth failing over.
	_, _ = fmt.Fprintf(w, "protoc-gen-store: warning: [lint] reading deprecated %s; rename it to store.yaml — %s is removed in v2\n",
		path, legacyConfigName)
}

// resolve returns the database, schema, and version-stripping decision for a
// proto package under the first matching rule, or the global default when no
// rule matches (nil-safe). stripVer reflects the per-rule strip_version when
// set, otherwise the top-level strip_version.
func (c *Config) resolve(pkg string) (database, schema string, stripVer bool) {
	if c == nil {
		return "", "", false
	}
	segs := strings.Split(pkg, ".")
	for _, r := range c.Datasources {
		if matchPackage(r.Match, segs) {
			sv := c.StripVersion
			if r.StripVersion != nil {
				sv = *r.StripVersion
			}
			return r.Database, ruleSchema(r, segs), sv
		}
	}
	return "", "", c.StripVersion
}

// ruleSchema computes the schema name a matched rule assigns to a package.
func ruleSchema(r matchRule, segs []string) string {
	switch {
	case r.Schema != "":
		return strings.ReplaceAll(r.Schema, "{leaf}", leafSegment(segs))
	case r.SchemaDepth > 0 && r.SchemaDepth <= len(segs):
		return strings.Join(segs[:r.SchemaDepth], "_")
	default:
		return ""
	}
}

// leafSegment returns the last package segment, dropping a trailing API version
// ("store.apps.calendar.v1" → "calendar").
func leafSegment(segs []string) string {
	i := len(segs) - 1
	if i > 0 && isVersionSegment(segs[i]) {
		i--
	}
	if i < 0 {
		return ""
	}
	return segs[i]
}

// isVersionSegment reports whether seg is an API version segment like "v1",
// "v2", "v1alpha1", or "v1beta1": a 'v' followed by a digit.
func isVersionSegment(seg string) bool {
	return len(seg) >= 2 && seg[0] == 'v' && seg[1] >= '0' && seg[1] <= '9'
}

// matchPackage reports whether the dotted glob pattern matches package segments.
func matchPackage(pattern string, segs []string) bool {
	pats := strings.Split(pattern, ".")
	for i, p := range pats {
		if p == "**" {
			return true // trailing wildcard: the rest matches
		}
		if i >= len(segs) {
			return false
		}
		if p != "*" && p != segs[i] {
			return false
		}
	}
	return len(pats) == len(segs)
}
