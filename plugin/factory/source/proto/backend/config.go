package backend

// config.go loads the optional store.yaml layout config (passed via the
// `config=<path>` plugin option). This is what lets a multi-service monorepo split
// into several databases with clean schema names without annotating every file.
// Precedence: a per-file (entity.v1.datasource) annotation wins over the config,
// which in turn wins over the package-path defaults. The config is this plugin's
// alone — protokit owns no configuration.
//
// The naming-policy half of the file — `datasources:`, `strip_version:`,
// `dedupe_schema_table:` — is declared and resolved by entity.LayoutConfig, not
// here. It is embedded inline so store.yaml keeps its exact shape while the
// matching, templating, and version-stripping rules stay shared with every other
// plugin reading entity.v1. Two plugins that each reimplemented "match a package
// glob, template a schema name" would agree until the first edge case.

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/the-protobuf-project/store/plugin/entity"
)

// Config is the parsed store.yaml.
type Config struct {
	// LayoutConfig carries the naming policy: datasources, strip_version, and
	// dedupe_schema_table. Inlined so those keys sit at the top level of
	// store.yaml exactly as before, while the rules behind them are entity's.
	entity.LayoutConfig `yaml:",inline"`

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

// LoadConfig reads store.yaml from path. A blank path yields nil (no config;
// defaults apply).
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, nil
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
