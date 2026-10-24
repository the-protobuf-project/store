// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package migrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// now is the clock migration files are named by.
var now = time.Now

// migrator runs the subcommands for one config and set of registries.
type migrator struct {
	cfg         Config
	registries  []Registry
	out, errOut io.Writer
}

// withDesired builds the schema the registries describe on a scratch
// database (the scratch URL's database + "_atlas_desired"), creates an empty
// one beside it for atlas (+ "_atlas_dev"), runs f with their URLs, and drops
// both.
func (m *migrator) withDesired(ctx context.Context, f func(db *gorm.DB, desired, dev string) error) error {
	if m.cfg.ScratchURL == "" {
		return fmt.Errorf("no scratch server: set url or scratch_url (-url, STORE_MIGRATE_URL or the config)")
	}
	base, err := databaseOf(m.cfg.ScratchURL)
	if err != nil {
		return err
	}
	desiredName, devName := base+"_atlas_desired", base+"_atlas_dev"
	desired, err := withDatabase(m.cfg.ScratchURL, desiredName)
	if err != nil {
		return err
	}
	dev, err := withDatabase(m.cfg.ScratchURL, devName)
	if err != nil {
		return err
	}
	admin, err := Open(m.cfg.ScratchURL)
	if err != nil {
		return err
	}
	admin = admin.WithContext(ctx)
	defer closeDB(admin)
	for _, name := range []string{desiredName, devName} {
		if err := recreate(admin, name); err != nil {
			return err
		}
		defer func() { _ = admin.Exec(`DROP DATABASE IF EXISTS "` + name + `"`).Error }()
	}

	db, err := Open(desired)
	if err != nil {
		return err
	}
	db = db.WithContext(ctx)
	defer closeDB(db)
	if err := Build(db, m.registries...); err != nil {
		return fmt.Errorf("build the desired schema: %w", err)
	}
	return f(db, desired, dev)
}

// closeDB closes db's connection pool.
func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// recreate drops and creates an empty database.
func recreate(admin *gorm.DB, name string) error {
	if err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `"`).Error; err != nil {
		return err
	}
	return admin.Exec(`CREATE DATABASE "` + name + `"`).Error
}

// diff writes the migrations that bring into's history up to the models.
func (m *migrator) diff(ctx context.Context, into, name string) error {
	return m.withDesired(ctx, func(db *gorm.DB, desired, dev string) error {
		return m.diffFrom(ctx, db, into, name, desired, dev)
	})
}

// diffFrom is diff over the desired schema db.
func (m *migrator) diffFrom(ctx context.Context, db *gorm.DB, into, name, desired, dev string) error {
	existing, err := sqlFiles(into)
	if err != nil {
		return err
	}
	if err := m.extensions(ctx, db, into); err != nil {
		return err
	}
	before, err := sqlFiles(into)
	if err != nil {
		return err
	}
	if err := m.atlas(ctx, "migrate", "diff", name, "--dir", "file://"+into,
		"--to", desired, "--dev-url", dev); err != nil {
		return err
	}
	if err := m.notValid(ctx, db, into, before); err != nil {
		return err
	}
	if err := m.routines(ctx, db, into, name); err != nil {
		return err
	}
	return m.header(ctx, into, existing)
}

// check fails when diff would write anything: a model changed and nobody ran
// diff.
func (m *migrator) check(ctx context.Context) error {
	tmp, err := os.MkdirTemp("", "migrations-check")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.CopyFS(tmp, os.DirFS(m.cfg.Dir)); err != nil {
		return err
	}
	before, _ := os.ReadDir(tmp)
	if err := m.diff(ctx, tmp, "check"); err != nil {
		return err
	}
	after, _ := os.ReadDir(tmp)
	if len(after) != len(before) {
		var added []string
		for _, e := range after {
			if !slices.ContainsFunc(before, func(b os.DirEntry) bool { return b.Name() == e.Name() }) {
				added = append(added, e.Name())
			}
		}
		return fmt.Errorf("the generated models changed and %s/ has no migration for it (diff would write %s): run diff <name> and commit the file it writes", m.cfg.Dir, strings.Join(added, ", "))
	}
	return nil
}

// fkConstraint is one foreign key, by schema, table and name.
type fkConstraint struct{ Schema, Table, Name string }

// unvalidated lists the foreign keys db holds NOT VALID.
func unvalidated(db *gorm.DB) ([]fkConstraint, error) {
	var out []fkConstraint
	err := db.Raw(`SELECT n.nspname AS schema, t.relname AS "table", c.conname AS name
		FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE c.contype = 'f' AND NOT c.convalidated ORDER BY 1, 2, 3`).Scan(&out).Error
	return out, err
}

// validate writes a migration validating every foreign key the registries
// add NOT VALID (MigrateExternal's keys into another registry's tables) that
// no migration in into validates yet, and rehashes the directory. Each
// statement is guarded: it runs only while the key exists and is still
// unvalidated, so the file applies to a database that validated some by hand.
// VALIDATE CONSTRAINT takes a SHARE UPDATE EXCLUSIVE lock, which leaves reads
// and writes running, and fails on the first row that breaks the key: clean
// those rows first.
func (m *migrator) validate(ctx context.Context, into string) error {
	return m.withDesired(ctx, func(db *gorm.DB, _, _ string) error {
		pending, err := unvalidated(db)
		if err != nil {
			return err
		}
		history, err := historyOf(into)
		if err != nil {
			return err
		}
		stmts := validateStatements(pending, history)
		if len(stmts) == 0 {
			fmt.Fprintln(m.out, "every foreign key is validated already: nothing to write")
			return nil
		}
		file := now().UTC().Format("20060102150405") + "_validate_foreign_keys.sql"
		if err := m.write(filepath.Join(into, file), strings.Join(stmts, "\n")); err != nil {
			return err
		}
		fmt.Fprintf(m.out, "wrote %s (%d foreign keys)\n", file, len(stmts))
		return m.hash(ctx, into)
	})
}

// validateStatements is a guarded VALIDATE CONSTRAINT for each key in
// pending that history does not validate yet.
func validateStatements(pending []fkConstraint, history string) []string {
	var stmts []string
	for _, c := range pending {
		table := fmt.Sprintf("%q.%q", c.Schema, c.Table)
		alter := fmt.Sprintf("ALTER TABLE %s VALIDATE CONSTRAINT %q", table, c.Name)
		if strings.Contains(history, alter) {
			continue
		}
		stmts = append(stmts, fmt.Sprintf(`-- Validate %q on %s
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '%s' AND conrelid = to_regclass('%s') AND NOT convalidated) THEN
    %s;
  END IF;
END $$;
`, c.Name, table, c.Name, table, alter))
	}
	return stmts
}

var createExtension = regexp.MustCompile(`(?i)CREATE EXTENSION IF NOT EXISTS "([^"]+)"`)

// extensions writes a migration creating the extensions db uses that no
// migration in into creates yet, and rehashes the directory.
func (m *migrator) extensions(ctx context.Context, db *gorm.DB, into string) error {
	var used []string
	if err := db.Raw(`SELECT extname FROM pg_extension WHERE extname <> 'plpgsql' ORDER BY extname`).Scan(&used).Error; err != nil {
		return err
	}
	history, err := historyOf(into)
	if err != nil {
		return err
	}
	stmts := extensionStatements(used, history)
	if len(stmts) == 0 {
		return nil
	}
	// One second before atlas names its own file, so the extensions come
	// first in the history.
	name := now().UTC().Add(-time.Second).Format("20060102150405") + "_extensions.sql"
	if err := m.write(filepath.Join(into, name), strings.Join(stmts, "")); err != nil {
		return err
	}
	return m.hash(ctx, into)
}

// extensionStatements creates each extension in used that history does not.
func extensionStatements(used []string, history string) []string {
	var created []string
	for _, match := range createExtension.FindAllStringSubmatch(history, -1) {
		created = append(created, match[1])
	}
	var stmts []string
	for _, ext := range used {
		if !slices.Contains(created, ext) {
			stmts = append(stmts, fmt.Sprintf("-- Create extension %q\nCREATE EXTENSION IF NOT EXISTS %q;\n", ext, ext))
		}
	}
	return stmts
}

// trigger is one trigger: its name, table and definition.
type trigger struct{ Name, Table, Def string }

// routines writes a migration (re)creating the functions and triggers db has
// that into's history does not -- or has with another definition -- and
// rehashes the directory. A function is CREATE OR REPLACE; a trigger is
// dropped first, so a changed one is replaced.
func (m *migrator) routines(ctx context.Context, db *gorm.DB, into, name string) error {
	var funcs []string
	// Functions of the schema's own, not of an extension.
	if err := db.Raw(`SELECT pg_get_functiondef(p.oid) FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND p.prokind = 'f'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')
		ORDER BY n.nspname, p.proname`).Scan(&funcs).Error; err != nil {
		return err
	}
	var triggers []trigger
	if err := db.Raw(`SELECT t.tgname AS name, t.tgrelid::regclass::text AS "table", pg_get_triggerdef(t.oid) AS def
		FROM pg_trigger t WHERE NOT t.tgisinternal ORDER BY 2, 1`).Scan(&triggers).Error; err != nil {
		return err
	}
	history, err := historyOf(into)
	if err != nil {
		return err
	}
	stmts := routineStatements(funcs, triggers, history)
	if len(stmts) == 0 {
		return nil
	}
	// One second after atlas names its own file, so these follow the tables
	// they are on.
	file := now().UTC().Add(time.Second).Format("20060102150405") + "_" + name + "_routines.sql"
	if err := m.write(filepath.Join(into, file), strings.Join(stmts, "\n")); err != nil {
		return err
	}
	return m.hash(ctx, into)
}

// routineStatements (re)creates each function and trigger whose definition
// history lacks.
func routineStatements(funcs []string, triggers []trigger, history string) []string {
	var stmts []string
	for _, f := range funcs {
		if f = strings.TrimSpace(f); !strings.Contains(history, f) {
			stmts = append(stmts, f+";\n")
		}
	}
	for _, t := range triggers {
		if !strings.Contains(history, t.Def) {
			stmts = append(stmts, fmt.Sprintf("DROP TRIGGER IF EXISTS %q ON %s;\n%s;\n", t.Name, t.Table, t.Def))
		}
	}
	return stmts
}

// notValid marks NOT VALID, in the files atlas just wrote into (those not in
// before), every foreign key db holds unvalidated, and rehashes the
// directory when it changed one. Atlas writes such a key as a plain ADD
// CONSTRAINT, which would check every existing row and fail on the first
// dangling one.
func (m *migrator) notValid(ctx context.Context, db *gorm.DB, into string, before []string) error {
	pending, err := unvalidated(db)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	files, err := sqlFiles(into)
	if err != nil {
		return err
	}
	changed := false
	for _, f := range files {
		if slices.Contains(before, f) {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if out := markNotValid(string(src), pending); out != string(src) {
			if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
				return err
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.hash(ctx, into)
}

// markNotValid appends NOT VALID to each ADD CONSTRAINT of a key in pending
// on the ALTER TABLE lines of src.
func markNotValid(src string, pending []fkConstraint) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		for _, c := range pending {
			if !strings.HasPrefix(line, fmt.Sprintf(`ALTER TABLE %q.%q `, c.Schema, c.Table)) {
				continue
			}
			clause := regexp.MustCompile(`(ADD CONSTRAINT ` + regexp.QuoteMeta(fmt.Sprintf("%q", c.Name)) + ` FOREIGN KEY [^,;]*?(?:\([^)]*\)[^,;]*?)*)([,;])`)
			lines[i] = clause.ReplaceAllString(lines[i], "$1 NOT VALID$2")
		}
	}
	return strings.Join(lines, "\n")
}

// header prepends Config.Header to every file in into that is not in
// existing and does not start with it yet, and rehashes the directory when
// it changed one.
func (m *migrator) header(ctx context.Context, into string, existing []string) error {
	if m.cfg.Header == "" {
		return nil
	}
	files, err := sqlFiles(into)
	if err != nil {
		return err
	}
	changed := false
	for _, f := range files {
		if slices.Contains(existing, f) {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if out := withHeader(string(src), m.cfg.Header); out != string(src) {
			if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
				return err
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.hash(ctx, into)
}

// withHeader is src led by header and a blank line, unless it is already.
func withHeader(src, header string) string {
	header = strings.TrimRight(header, "\n") + "\n"
	if strings.HasPrefix(src, header) {
		return src
	}
	return header + "\n" + src
}

// write writes a migration file the command made itself, led by the header.
func (m *migrator) write(path, src string) error {
	if m.cfg.Header != "" {
		src = withHeader(src, m.cfg.Header)
	}
	return os.WriteFile(path, []byte(src), 0o644)
}

// sqlFiles lists dir's migrations in order.
func sqlFiles(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "*.sql"))
}

// historyOf is every migration in dir, one after another.
func historyOf(dir string) (string, error) {
	files, err := sqlFiles(dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		b.Write(src)
	}
	return b.String(), nil
}

// hash rewrites dir's atlas.sum.
func (m *migrator) hash(ctx context.Context, dir string) error {
	return m.atlas(ctx, "migrate", "hash", "--dir", "file://"+dir)
}

// atlas runs the atlas CLI, its output passed through, after checking it is
// there and, when the config pins one, its version.
func (m *migrator) atlas(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath(m.cfg.Atlas)
	if err != nil {
		return fmt.Errorf("atlas (%s) is not on PATH: install the community build %s (brew install atlas, or https://atlasgo.io/docs#installation)", m.cfg.Atlas, AtlasVersion)
	}
	if v := m.cfg.AtlasVersion; v != "" {
		out, err := exec.CommandContext(ctx, bin, "version").Output()
		if err != nil {
			return fmt.Errorf("atlas version: %w", err)
		}
		if !atlasVersionMatches(string(out), v) {
			return fmt.Errorf("atlas is %q, want %s", firstLine(string(out)), v)
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = m.out, m.errOut
	return cmd.Run()
}

// atlasVersionMatches reports whether `atlas version` printed want.
func atlasVersionMatches(out, want string) bool {
	want = "v" + strings.TrimPrefix(want, "v")
	for _, f := range strings.Fields(firstLine(out)) {
		if f == want {
			return true
		}
	}
	return false
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
