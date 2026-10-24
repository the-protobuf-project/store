// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package migrate keeps a directory of versioned SQL migrations -- the files
// that change a production database -- in step with the GORM registries
// protoc-gen-store generates, and applies them.
//
// Nothing in the directory is written by hand. The desired schema is what the
// generated registries build (Build) on a scratch database, and atlas
// (https://atlasgo.io, the community build) diffs it against the directory's
// history to write the next file. Atlas's community build manages neither
// extensions nor functions and triggers, so this package writes those itself,
// read off the scratch database: a migration ahead of atlas's that creates
// the extensions the schema has started to use, and one after it that
// (re)creates the functions and triggers it has gained or changed -- the
// no_overlap lock triggers among them. Nor does atlas know NOT VALID: a
// foreign key the scratch database holds unvalidated -- one a registry adds
// with MigrateExternal, into another registry's tables -- is marked NOT VALID
// in the file atlas writes, so applying it never fails on rows already there.
// Validating those keys is a step of its own, taken once the data is clean:
// validate writes a migration running VALIDATE CONSTRAINT, guarded, for every
// foreign key the scratch database holds unvalidated that no migration has
// validated yet.
//
// The registries are the consumer's generated Go code, so this package cannot
// load them itself: the consumer compiles a small command that hands them over.
//
//	func main() {
//		migrate.Main(migrate.Config{}, migrate.Of(mydb.Default), migrate.Of(otherdb.Default))
//	}
//
// The command it makes:
//
//	[flags] diff <name>      write the next migration, if the models changed
//	[flags] validate         write a migration validating the NOT VALID foreign keys
//	[flags] check            fail if the models changed without a migration
//	[flags] apply [atlas flags]
//	                         apply pending migrations to the configured database;
//	                         flags after apply pass through to atlas (e.g. --baseline <version>)
//
// It needs the atlas CLI, and diff, validate and check need a Postgres server
// where it may create and drop two scratch databases.
package migrate

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// AtlasVersion is the atlas community release this package is tested with.
// Config.AtlasVersion set to it makes the command refuse any other.
const AtlasVersion = "v1.3.0"

// Config is what the command needs besides the registries. Main fills the
// fields left empty from a config file (-config), the environment and flags,
// in that order, each overriding the one before.
type Config struct {
	// URL is the postgres:// URL of the database apply migrates
	// (STORE_MIGRATE_URL, -url).
	URL string `yaml:"url"`
	// ScratchURL is the postgres:// URL of a database on the server where
	// diff, validate and check create their two scratch databases, named
	// after it with the suffixes _atlas_desired and _atlas_dev. Empty means
	// URL (STORE_MIGRATE_SCRATCH_URL, -scratch-url).
	ScratchURL string `yaml:"scratch_url"`
	// Dir is the migration directory. Empty means "migrations" (-dir).
	Dir string `yaml:"dir"`
	// Header, when set, is prepended to every file the command writes -- a
	// license notice, say -- before the directory is rehashed.
	Header string `yaml:"header"`
	// Atlas is the atlas binary. Empty means "atlas" on PATH (-atlas).
	Atlas string `yaml:"atlas"`
	// AtlasVersion, when set, is the atlas version the command insists on
	// ("v1.3.0"), so every machine and CI writes the same files.
	AtlasVersion string `yaml:"atlas_version"`
}

// LoadConfig reads a YAML config file, expanding ${NAME} from the
// environment.
func LoadConfig(path string) (Config, error) {
	var c Config
	src, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(src))))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// merge is c with every field over sets replaces.
func (c Config) merge(over Config) Config {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&c.URL, over.URL)
	set(&c.ScratchURL, over.ScratchURL)
	set(&c.Dir, over.Dir)
	set(&c.Header, over.Header)
	set(&c.Atlas, over.Atlas)
	set(&c.AtlasVersion, over.AtlasVersion)
	return c
}

// withDefaults fills what is still empty.
func (c Config) withDefaults() Config {
	if c.Dir == "" {
		c.Dir = "migrations"
	}
	if c.Atlas == "" {
		c.Atlas = "atlas"
	}
	if c.ScratchURL == "" {
		c.ScratchURL = c.URL
	}
	return c
}

// Registry is one generated factory registry, as Build drives it. Of makes
// one.
type Registry struct {
	ensureSchemas, migrate, migrateExternal func(*gorm.DB) error
}

// generated is the method set every registry protoc-gen-store emits: each
// Migrate takes its own package's Migrator interface, which *gorm.DB
// satisfies, so M is inferred per registry.
type generated[M any] interface {
	EnsureSchemas(db *gorm.DB) error
	Migrate(db M) error
	MigrateExternal(db *gorm.DB) error
}

// Of adapts a generated registry (a package's Default) for Build and Run.
func Of[M any](r generated[M]) Registry {
	if _, ok := any((*gorm.DB)(nil)).(M); !ok {
		panic(fmt.Sprintf("migrate.Of: %T's Migrate takes %T, which *gorm.DB does not satisfy", r, *new(M)))
	}
	return Registry{
		ensureSchemas:   r.EnsureSchemas,
		migrate:         func(db *gorm.DB) error { return r.Migrate(any(db).(M)) },
		migrateExternal: r.MigrateExternal,
	}
}

// Open connects to a postgres:// URL (or key=value DSN) the way building the
// schema needs: AutoMigrate creates no foreign keys, so cross-schema
// references impose no table order; Build adds the cross-database ones
// itself, after every table exists.
func Open(dsn string) (*gorm.DB, error) {
	return gorm.Open(postgres.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
}

// Build creates or updates every table of every registry on db: the schemas,
// extensions and tables of each registry in turn, then each one's foreign
// keys into another registry's tables, once every table exists. Those are
// added NOT VALID, so existing rows are not checked; a later migration
// validates them once the data is known to be clean.
func Build(db *gorm.DB, registries ...Registry) error {
	for _, r := range registries {
		if err := r.ensureSchemas(db); err != nil {
			return err
		}
		if err := r.migrate(db); err != nil {
			return err
		}
	}
	for _, r := range registries {
		if err := r.migrateExternal(db); err != nil {
			return err
		}
	}
	return nil
}

// Main runs the command on os.Args and exits non-zero on failure.
func Main(cfg Config, registries ...Registry) {
	err := Run(context.Background(), cfg, os.Args[1:], registries...)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// errUsage is what a malformed command line returns.
var errUsage = errors.New("usage: [-config file] [-url url] [-scratch-url url] [-dir dir] [-atlas path] diff <name> | validate | check | apply [atlas flags]")

// Run runs one subcommand: args are the command line after the program name.
func Run(ctx context.Context, cfg Config, args []string, registries ...Registry) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	file := fs.String("config", "", "YAML config file (url, scratch_url, dir, header, atlas, atlas_version)")
	var flags Config
	fs.StringVar(&flags.URL, "url", "", "postgres:// URL of the database apply migrates")
	fs.StringVar(&flags.ScratchURL, "scratch-url", "", "postgres:// URL on the server for the scratch databases (default: -url)")
	fs.StringVar(&flags.Dir, "dir", "", "migration directory (default: migrations)")
	fs.StringVar(&flags.Atlas, "atlas", "", "atlas binary (default: atlas on PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file != "" {
		fc, err := LoadConfig(*file)
		if err != nil {
			return err
		}
		cfg = cfg.merge(fc)
	}
	cfg = cfg.merge(Config{URL: os.Getenv("STORE_MIGRATE_URL"), ScratchURL: os.Getenv("STORE_MIGRATE_SCRATCH_URL")})
	cfg = cfg.merge(flags).withDefaults()

	rest := fs.Args()
	if len(rest) == 0 {
		return errUsage
	}
	m := &migrator{cfg: cfg, registries: registries, out: os.Stdout, errOut: os.Stderr}
	switch rest[0] {
	case "diff":
		if len(rest) != 2 {
			return errUsage
		}
		return m.diff(ctx, cfg.Dir, rest[1])
	case "check":
		if len(rest) != 1 {
			return errUsage
		}
		return m.check(ctx)
	case "validate":
		if len(rest) != 1 {
			return errUsage
		}
		return m.validate(ctx, cfg.Dir)
	case "apply":
		if cfg.URL == "" {
			return errors.New("no database to apply to: set url (-url, STORE_MIGRATE_URL or the config)")
		}
		return m.atlas(ctx, append([]string{"migrate", "apply", "--dir", "file://" + cfg.Dir, "--url", cfg.URL}, rest[1:]...)...)
	default:
		return errUsage
	}
}

// withDatabase is the postgres:// URL u naming database name instead.
func withDatabase(u, name string) (string, error) {
	p, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	if p.Scheme != "postgres" && p.Scheme != "postgresql" {
		return "", fmt.Errorf("%s: want a postgres:// URL", p.Redacted())
	}
	p.Path = "/" + name
	p.RawPath = ""
	return p.String(), nil
}

// databaseOf is the database a postgres:// URL names.
func databaseOf(u string) (string, error) {
	p, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(p.Path, "/")
	if name == "" {
		return "", fmt.Errorf("%s names no database", p.Redacted())
	}
	return name, nil
}
