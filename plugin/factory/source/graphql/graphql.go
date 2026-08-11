// Package graphql is the factory Source that reads a GraphQL server: it
// introspects a live endpoint (or reads a cached GraphQL SDL schema, .graphql),
// then builds the GraphQL IR under the configured dialect. Unlike the proto Source it runs
// outside protoc — its input is an endpoint or a file, supplied via Config — so
// it drives the CLI (`protoc-gen-store graphql …`) rather than plugin mode.
package graphql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/introspect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
)

// Config selects the introspection input and conventions for a GraphQL build.
type Config struct {
	// Endpoint is a live GraphQL URL to introspect (used when SchemaFile is empty).
	Endpoint string
	// SchemaFile is a cached schema file that skips the live fetch: a GraphQL SDL
	// document (.graphql/.gql), or a .json introspection dump for back-compat.
	SchemaFile string
	// AdminSecret, when set, is sent under the dialect's auth header.
	AdminSecret string
	// Headers are extra "Key: Value" request headers.
	Headers []string
	// Dialect supplies engine conventions; defaults to dialect.Default().
	Dialect dialect.Dialect
}

// Source builds the GraphQL IR from an endpoint or cached schema.
type Source struct{ cfg Config }

// New returns a GraphQL source. A nil Dialect defaults to dialect.Default().
func New(cfg Config) *Source {
	if cfg.Dialect == nil {
		cfg.Dialect = dialect.Default()
	}
	return &Source{cfg: cfg}
}

// Name identifies this source in the registry and config.
func (s *Source) Name() string { return "graphql" }

// Build loads the introspection schema and normalizes it to the IR.
func (s *Source) Build(_ factory.Ctx) (*coreir.Model, error) {
	schema, err := s.loadSchema()
	if err != nil {
		return nil, err
	}
	return &coreir.Model{
		GraphQL:    ir.Build(schema, s.cfg.Dialect),
		RawGraphQL: schema,
	}, nil
}

// loadSchema reads the introspection schema from SchemaFile or fetches it live.
func (s *Source) loadSchema() (*introspect.Schema, error) {
	if s.cfg.SchemaFile != "" {
		raw, err := os.ReadFile(s.cfg.SchemaFile)
		if err != nil {
			return nil, fmt.Errorf("read schema file: %w", err)
		}
		// A cached schema is authored as GraphQL SDL (.graphql/.gql); a .json file is
		// treated as a raw introspection dump for backward compatibility.
		if strings.EqualFold(filepath.Ext(s.cfg.SchemaFile), ".json") {
			return introspect.Decode(raw)
		}
		return introspect.ParseSDL(s.cfg.SchemaFile, string(raw))
	}
	if s.cfg.Endpoint == "" {
		return nil, fmt.Errorf("graphql source needs an endpoint or a schema file")
	}
	headers := map[string]string{}
	if s.cfg.AdminSecret != "" {
		if h := s.cfg.Dialect.AuthHeader(); h != "" {
			headers[h] = s.cfg.AdminSecret
		}
	}
	for _, h := range s.cfg.Headers {
		if key, value, ok := strings.Cut(h, ":"); ok {
			headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return introspect.Fetch(context.Background(), s.cfg.Endpoint, headers)
}
