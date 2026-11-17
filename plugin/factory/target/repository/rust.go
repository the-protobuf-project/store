// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// rust.go renders lang=rust: per proto package, a Rust module binding each
// repository resource to tpp-database's typed repository contract
// (database::repository::RecordsRepository) over any records provider.
//
// The plan is the Go target's own (planResources), so the two languages agree
// on which messages are repositories. What differs is storage: Go composes
// the gorm output, Rust describes each table to the runtime with a
// database::records::Resource built from the same IR the sql and gorm targets
// render, so a Go and a Rust service share one database.
//
// Output layout, beside influxdb's:
//
//	Cargo.toml, lib.rs              one crate (the crate opt), mountable as a module
//	<proto package path>/mod.rs     one module per proto package: org.v1 -> org/v1/mod.rs
//
// The prost messages are named through the rust_proto_module opt (default
// crate::pb), as protoc-gen-service's Rust target names them, so the module
// lives in, or is mounted into, the crate holding those messages.

import (
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/gorm"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

//go:embed templates/rust/*.tpl
var rustTemplateFS embed.FS

var rustTmpl = templates.MustParse(rustTemplateFS, "templates/rust/*.tpl")

// Rust runtime coordinates, as the influxdb target pins them: the crate the
// generated code depends on, where Cargo fetches it, and the branch it tracks.
const (
	rustRuntimeCrate  = "tpp-database"
	rustRuntimeGit    = "https://github.com/the-protobuf-project/runtime-rs"
	rustRuntimeBranch = "main"

	defaultRustCrate       = "generated"
	defaultRustProtoModule = "crate::pb"

	// rustfmt's defaults the renderer mirrors: max_width, and the
	// fn_call_width / array_width a call's arguments or a vec!'s items must
	// fit in to stay on one line.
	rustMaxWidth  = 100
	rustListWidth = 60
)

var (
	rustCrateName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
	rustModulePath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(::[A-Za-z_][A-Za-z0-9_]*)*$`)
)

// rustTreeView is the template data of Cargo.toml and lib.rs.
type rustTreeView struct {
	Header       string
	TOMLHeader   string
	Crate        string
	Lib          string
	ProtoModule  string
	RuntimeCrate string
	RuntimeGit   string
	RuntimeRef   string
	Mods         []string
	Packages     []string
}

// rustModView is one module file: an intermediate one holding only child
// modules, or a proto package's.
type rustModView struct {
	Header string
	Doc    string
	Mods   []string
	Pkg    *rustPackageView
}

// rustPackageView is one proto package's repositories.
type rustPackageView struct {
	Package   string // proto package, "org.v1"
	ProtoPath string // its prost module, "crate::pb::org::v1"
	Imports   []string
	Resources []*rustResourceView
	Bind      []string // repositories()'s struct fields, rendered
	HasFKs    bool
	HasRefs   bool
	// Cache is set when a resource carries (cache.v1.cache): protoc-gen-cache
	// writes the package's cache.rs beside its mod.rs.
	Cache bool
}

// rustResourceView is one resource: its descriptor and rules functions.
type rustResourceView struct {
	FullName string // "org.v1.User"
	Type     string // "pb::User"
	Snake    string // "user"
	Field    string // "users"
	Table    string // "users"
	Schema   string
	PK       string
	Columns  []string // rendered column expressions, each ending in ","
	FKs      string   // rendered fks vec!, "" without foreign keys
	Search   string   // rendered search vec!, "" without search columns
	Base     string   // the rendered ..Resource::new(...) line
	Rules    []string // rendered Rules fields
	HasRefs  bool
}

// rustNode is one module of the package tree.
type rustNode struct {
	children  map[string]*rustNode
	pkg       *rustPackageView
	sources   []string
	databases []string
}

func (n *rustNode) child(name string) *rustNode {
	if n.children == nil {
		n.children = map[string]*rustNode{}
	}
	c := n.children[name]
	if c == nil {
		c = &rustNode{}
		n.children[name] = c
	}
	return c
}

func (n *rustNode) mods() []string {
	var out []string
	for name := range n.children {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// generateRust writes the crate: Cargo.toml and lib.rs, then a module per
// proto package holding repositories, plus the modules leading to it.
func (g *Generator) generateRust(p *protogen.Plugin, ir *protokit.IR) error {
	dbs := ir.Databases
	if len(dbs) == 0 {
		return nil
	}
	fx := facets.New(ir)
	pb := newPbIndex(p)
	crate, protoModule := defaultRustCrate, defaultRustProtoModule
	for _, db := range dbs {
		if c := db.Opt("rust_crate"); c != "" {
			crate = c
		}
		if m := db.Opt("rust_proto_module"); m != "" {
			protoModule = m
		}
	}
	if !rustCrateName.MatchString(crate) {
		return fmt.Errorf("repository: crate %q is not a valid Cargo package name (a letter, then letters, digits, - and _)", crate)
	}
	if !rustModulePath.MatchString(protoModule) {
		return fmt.Errorf("repository: rust_proto_module %q is not a Rust module path (e.g. crate::pb)", protoModule)
	}

	tables := tableIndex(dbs)
	byPkg := map[string][]*resource{}
	sources := map[string][]string{}
	databases := map[string][]string{}
	search := map[*schema.Table][]string{}
	for _, db := range dbs {
		resources, err := planResources(pb, db)
		if err != nil {
			return err
		}
		for _, s := range db.Schemas {
			for _, r := range sortedResources(s, resources) {
				pkg := string(r.Table.Source.ParentFile().Package())
				if pkg == "" {
					return fmt.Errorf("repository: lang=rust: %s needs a proto package, which names its Rust module", r.Table.ProtoMessage)
				}
				byPkg[pkg] = append(byPkg[pkg], r)
				search[r.Table] = gorm.SearchColumns(db, s, r.Table, fx)
				if !contains(databases[pkg], db.Name) {
					databases[pkg] = append(databases[pkg], db.Name)
				}
				if src := r.Table.SourceProto; src != "" && !contains(sources[pkg], src) {
					sources[pkg] = append(sources[pkg], src)
				}
			}
		}
	}
	if len(byPkg) == 0 {
		return nil
	}

	root := &rustNode{}
	var pkgNames []string
	for pkg := range byPkg {
		pkgNames = append(pkgNames, pkg)
	}
	sort.Strings(pkgNames)
	for _, pkg := range pkgNames {
		view, err := rustPackageOf(pkg, protoModule, byPkg[pkg], fx, tables, search)
		if err != nil {
			return err
		}
		n := root
		for _, part := range strings.Split(pkg, ".") {
			n = n.child(rustIdent(naming.SnakeCase(part)))
		}
		n.pkg = view
		sort.Strings(sources[pkg])
		n.sources = sources[pkg]
		n.databases = databases[pkg]
	}

	plugin, protoc := dbs[0].PluginVersion, dbs[0].ProtocVersion
	banner := func(prefix string, in header.Info, note string) string {
		in.PluginVersion, in.ProtocVersion, in.Notes = plugin, protoc, []string{note}
		return provenance.Render(prefix, in, rustRuntimeCrate)
	}
	tv := rustTreeView{
		Crate:        crate,
		Lib:          strings.ReplaceAll(crate, "-", "_"),
		ProtoModule:  protoModule,
		RuntimeCrate: rustRuntimeCrate,
		RuntimeGit:   rustRuntimeGit,
		RuntimeRef:   rustRuntimeBranch,
		Mods:         root.mods(),
		Packages:     pkgNames,
	}
	tv.TOMLHeader = banner("#", header.Info{}, "The crate of every generated Rust repository.")
	if err := renderRust(p, "Cargo.toml", "Cargo.toml.tpl", tv); err != nil {
		return err
	}
	tv.Header = banner("//", header.Info{}, "The crate root: one module per proto package.")
	if err := renderRust(p, "lib.rs", "lib.rs.tpl", tv); err != nil {
		return err
	}

	var walk func(n *rustNode, path []string) error
	walk = func(n *rustNode, path []string) error {
		for _, name := range n.mods() {
			c := n.children[name]
			sub := append(append([]string(nil), path...), name)
			mv := rustModView{Mods: c.mods(), Pkg: c.pkg}
			if c.pkg != nil && c.pkg.Cache {
				if c.children["cache"] != nil {
					return fmt.Errorf("repository: lang=rust: %s's cache module (protoc-gen-cache's cache.rs) conflicts with its proto package %s.cache",
						c.pkg.Package, c.pkg.Package)
				}
				mv.Mods = append(mv.Mods, "cache")
				sort.Strings(mv.Mods)
			}
			var dir []string
			for _, s := range sub {
				dir = append(dir, strings.TrimPrefix(s, "r#"))
			}
			if c.pkg != nil {
				mv.Header = banner("//", header.Info{Source: strings.Join(c.sources, ", "), Database: strings.Join(c.databases, ", "), SchemaLabel: "package", Schema: c.pkg.Package},
					"Repositories of "+c.pkg.Package+" over the tpp-database records runtime.")
			} else {
				mv.Header = banner("//", header.Info{}, "Leads to the proto packages below it.")
				mv.Doc = "`" + strings.Join(dir, "::") + "`: the proto packages below it."
			}
			if err := renderRust(p, strings.Join(dir, "/")+"/mod.rs", "mod.rs.tpl", mv); err != nil {
				return err
			}
			if err := walk(c, sub); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, nil)
}

// rustPackageOf builds one proto package's view, refusing a resource whose
// storage the records runtime cannot write yet.
func rustPackageOf(pkg, protoModule string, resources []*resource, fx facets.Set, tables map[string]*schema.Table, search map[*schema.Table][]string) (*rustPackageView, error) {
	pv := &rustPackageView{Package: pkg, ProtoPath: protoModule}
	for _, part := range strings.Split(pkg, ".") {
		pv.ProtoPath += "::" + rustIdent(naming.SnakeCase(part))
	}
	sort.SliceStable(resources, func(i, j int) bool {
		return resources[i].Table.Source.Name() < resources[j].Table.Source.Name()
	})
	usesGenerated, usesFK := false, false
	fields := map[string]string{}
	for _, r := range resources {
		rv, gen, err := rustResourceOf(r, fx, tables, search[r.Table])
		if err != nil {
			return nil, err
		}
		if prev, ok := fields[rv.Field]; ok {
			return nil, fmt.Errorf("repository: lang=rust: %s and %s both become Repositories.%s; give one a distinct google.api.resource plural",
				prev, rv.FullName, rv.Field)
		}
		fields[rv.Field] = rv.FullName
		usesGenerated = usesGenerated || gen
		usesFK = usesFK || rv.FKs != ""
		pv.HasRefs = pv.HasRefs || rv.HasRefs
		pv.Cache = pv.Cache || cached(r.Table.Source)
		pv.Resources = append(pv.Resources, rv)
		args := []string{"db", "pool", quoteRust(rv.FullName), rv.Snake + "_resource", rv.Snake + "_rules()"}
		pv.Bind = append(pv.Bind, rustCall(8, rv.Field+": ", "bind", args, "?,"))
	}
	records := []string{"Column", "DB"}
	if usesFK {
		records = append(records, "ForeignKey")
	}
	if usesGenerated {
		records = append(records, "Generated")
	}
	records = append(records, "Kind", "Resource")
	pv.HasFKs = usesFK
	repoImports := "RecordsRepository, Repository, Rules"
	if pv.HasRefs {
		repoImports = "RecordsRepository, Reference, Repository, Rules"
	}
	pv.Imports = []string{
		"use " + pv.ProtoPath + " as pb;",
		"use database::records::{" + strings.Join(records, ", ") + "};",
		"use database::repository::{" + repoImports + "};",
		"use prost::Message;",
		"use prost_reflect::{DescriptorPool, MessageDescriptor, ReflectMessage};",
		"use std::sync::Arc;",
	}
	return pv, nil
}

// rustResourceOf renders one resource's descriptor and rules, and reports
// whether a column is generated.
func rustResourceOf(r *resource, fx facets.Set, tables map[string]*schema.Table, search []string) (*rustResourceView, bool, error) {
	t := r.Table
	msg := t.Source
	rv := &rustResourceView{
		FullName: string(msg.FullName()),
		Type:     "pb::" + rustMessagePath(msg),
		Snake:    naming.SnakeCase(string(msg.Name())),
		Field:    rustIdent(rustPlural(r)),
		Table:    t.Name,
		Schema:   r.Schema.Name,
	}
	if err := refuseChildStorage(r); err != nil {
		return nil, false, err
	}
	pk := r.Cols.NameKey
	if pk == nil {
		pk = r.Cols.Name
	}
	if pk == nil {
		return nil, false, fmt.Errorf("repository: lang=rust: %s has no stored resource name column to key it by", rv.FullName)
	}
	rv.PK = pk.Name

	uniqueIdx := map[string]bool{}
	for _, ix := range t.Indexes {
		if ix.Unique && len(ix.Columns) == 1 {
			uniqueIdx[ix.Columns[0]] = true
		}
	}
	parentFKs := map[string]bool{}
	if r.ParentFK != "" {
		parentFKs[r.ParentFK] = true
	}
	for _, a := range r.AncestorFKs {
		parentFKs[a.Column] = true
	}

	generated := false
	for _, c := range t.Columns {
		kind, err := rustKind(r, c, parentFKs)
		if err != nil {
			return nil, false, err
		}
		field := ""
		if c.Source != nil {
			field = string(c.Source.Name())
		}
		var flags []string
		sqlType := types.SQLForColumn(c, fx.Column(c))
		if c.Enum != nil {
			// Stored by label, as gorm stores it, in the Postgres enum type
			// the sql target's DDL declares.
			sqlType = `"` + r.Schema.Name + `"."` + c.Enum.LocalSQLName + `"`
			labels := rustEnumLabels(c)
			if len(labels) > 0 {
				flags = append(flags, strings.TrimLeft(rustList(16, "labels: ", labels, ","), " "))
			}
		}
		if c.PrimaryKey {
			flags = append(flags, "primary_key: true,")
		}
		if c.NotNull || c.PrimaryKey {
			flags = append(flags, "not_null: true,")
		}
		if (c.Unique || uniqueIdx[c.Name]) && !c.PrimaryKey {
			flags = append(flags, "unique: true,")
		}
		switch c.Generated {
		case "ulid":
			flags = append(flags, "generated: Some(Generated::Ulid),")
			generated = true
		case "uuid":
			flags = append(flags, "generated: Some(Generated::Uuid),")
			generated = true
		}
		autoCreate, autoUpdate := c.AutoCreate, c.AutoUpdate
		if c.Source != nil && isTimestamp(c) && outputOnly(c) {
			switch field {
			case "create_time":
				autoCreate = true
			case "update_time":
				autoUpdate = true
			}
		}
		if autoCreate {
			flags = append(flags, "auto_create: true,")
		}
		if autoUpdate {
			flags = append(flags, "auto_update: true,")
		}
		// labels, when any, come first in flags; rustfmt keeps field order.
		if c.Enum != nil && len(flags) > 0 && strings.Contains(flags[0], "labels: ") {
			flags = append(flags[1:], flags[0])
		}
		args := []string{quoteRust(c.Name), quoteRust(field), "Kind::" + kind, quoteRust(sqlType)}
		if len(flags) == 0 {
			rv.Columns = append(rv.Columns, rustCall(12, "", "column", args, ","))
			continue
		}
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", 12) + "Column {\n")
		for _, f := range flags {
			b.WriteString(strings.Repeat(" ", 16) + f + "\n")
		}
		b.WriteString(rustCall(16, "..", "column", args, "") + "\n")
		b.WriteString(strings.Repeat(" ", 12) + "},")
		rv.Columns = append(rv.Columns, b.String())
	}

	var fks []string
	for _, fk := range append(append([]*schema.ForeignKey(nil), t.ForeignKeys...), t.ExternalForeignKeys...) {
		name := fk.ReferencedModel
		if target := tables[fk.ReferencedSchema+"."+fk.ReferencedTable]; target != nil && target.Source != nil {
			name = string(target.Source.Name())
		}
		fks = append(fks, rustCall(0, "", "foreign_key", []string{quoteRust(fk.Column), quoteRust(name), quoteRust(fk.ReferencedColumn)}, ""))
	}
	if len(fks) > 0 {
		rv.FKs = rustList(8, "fks: ", fks, ",")
	}
	// The columns a free-text filter term searches: the gorm filter spec's
	// Search, from the one derivation both share.
	if len(search) > 0 {
		var cols []string
		for _, c := range search {
			cols = append(cols, quoteRust(c)+".into()")
		}
		rv.Search = rustList(8, "search: ", cols, ",")
	}
	rv.Base = rustCall(8, "..", "Resource::new", []string{"message", quoteRust(rv.Table), quoteRust(rv.PK), "Vec::new()"}, "")

	opt := func(c *schema.Column) string {
		if c == nil || c.Source == nil {
			return "None"
		}
		return "Some(" + quoteRust(string(c.Source.Name())) + ".into())"
	}
	rv.Rules = append(rv.Rules,
		"        etag: "+opt(r.Cols.Etag)+",",
		"        delete_time: "+opt(r.Cols.DeleteTime)+",",
		"        expire_time: "+opt(r.Cols.ExpireTime)+",",
	)
	var outs []string
	for _, c := range t.Columns {
		if c.Source != nil && outputOnly(c) {
			outs = append(outs, quoteRust(string(c.Source.Name()))+".into()")
		}
	}
	if len(outs) > 0 {
		rv.Rules = append(rv.Rules, rustList(8, "output_only: ", outs, ","))
	}
	rv.Rules = append(rv.Rules, "        pattern: "+quoteRust(r.Pattern)+".into(),")
	// The repository fills the parent's id only under a two-collection
	// pattern; a deeper parent is keyed by a surrogate Go looks up.
	parent := "None"
	if len(r.Segments) == 2 && r.ParentFK != "" {
		parent = "Some(" + quoteRust(r.ParentFK) + ".into())"
	}
	rv.Rules = append(rv.Rules, "        parent_column: "+parent+",")
	var refs []string
	for _, c := range r.Cols.Refs {
		if c.Source == nil || c.List {
			continue
		}
		if p := topLevelTarget(t, c, tables); p != "" {
			refs = append(refs, rustCall(0, "", "reference", []string{quoteRust(string(c.Source.Name())), quoteRust(p)}, ""))
		}
	}
	if len(refs) > 0 {
		rv.HasRefs = true
		rv.Rules = append(rv.Rules, rustList(8, "references: ", refs, ","))
	}
	return rv, generated, nil
}

// topLevelTarget is the pattern of the resource reference c names when Go
// stores it as the target's bare id: a relation to a top-level resource's
// primary key. "" for anything stored whole.
func topLevelTarget(t *schema.Table, c *schema.Column, tables map[string]*schema.Table) string {
	for _, fk := range t.ForeignKeys {
		if fk.Column != c.Name {
			continue
		}
		target := tables[fk.ReferencedSchema+"."+fk.ReferencedTable]
		if target == nil || target.Source == nil || fk.ReferencedColumn != target.PKColumn {
			return ""
		}
		pattern := resourcePattern(target.Source)
		if segs, err := parsePattern(pattern); err != nil || len(segs) != 1 {
			return ""
		}
		return pattern
	}
	return ""
}

// rustEnumLabels pairs each stored enum value's number with its label, spelled
// as gorm stores it (naming.EnumLabel), rendered as (n, "LABEL".into()).
func rustEnumLabels(c *schema.Column) []string {
	stored := map[string]bool{}
	for _, v := range c.Enum.Values {
		stored[v.MapName] = true
	}
	ed := c.Source.Enum()
	var out []string
	for i := 0; i < ed.Values().Len(); i++ {
		v := ed.Values().Get(i)
		label := naming.EnumLabel(string(ed.Name()), string(v.Name()))
		if stored[label] {
			out = append(out, fmt.Sprintf("(%d, %s.into())", v.Number(), quoteRust(label)))
		}
	}
	return out
}

// rustKind is the runtime Kind of c: what the records bridge moves between
// the proto field and the column. A column the bridge cannot fill -- a
// repeated field, a message other than Timestamp, a synthesized column other
// than a key, an audit time or a parent's id -- is refused by field name, so
// no data is dropped silently.
func rustKind(r *resource, c *schema.Column, parentFKs map[string]bool) (string, error) {
	where := r.Table.ProtoMessage
	if c.Source == nil {
		switch {
		case c.Generated != "", parentFKs[c.Name]:
			return "String", nil
		case c.AutoCreate || c.AutoUpdate:
			return "Timestamp", nil
		case isValueObjectFK(r.Table, c):
			return "", fmt.Errorf("repository: lang=rust: %s.%s: stored in a child table (column %q of %s); "+
				"the Rust repository writes one row per resource and cannot store it yet",
				where, naming.StripIDSuffix(c.Name), c.Name, r.Table.Name)
		}
		if name, ok := strings.CutSuffix(c.Name, "_case"); ok {
			if o := r.Table.Source.Oneofs().ByName(protoreflect.Name(name)); o != nil {
				return "", fmt.Errorf("repository: lang=rust: %s.%s: the oneof's case is stored in column %q of %s, "+
					"which no field carries; the Rust repository cannot fill it yet", where, name, c.Name, r.Table.Name)
			}
		}
		return "", fmt.Errorf("repository: lang=rust: %s: column %q of %s is synthesized from no field (a companion column); "+
			"the Rust repository cannot fill it yet", where, c.Name, r.Table.Name)
	}
	field := where + "." + string(c.Source.Name())
	switch {
	case c.List:
		return "", fmt.Errorf("repository: lang=rust: %s: a repeated field (column %q) cannot be stored by the Rust repository yet", field, c.Name)
	case c.Source.Message() != nil:
		if c.Source.Message().FullName() == "google.protobuf.Timestamp" {
			return "Timestamp", nil
		}
		return "", fmt.Errorf("repository: lang=rust: %s: a %s field (column %q) cannot be stored by the Rust repository yet; "+
			"only scalars, enums and google.protobuf.Timestamp can", field, c.Source.Message().FullName(), c.Name)
	}
	switch c.Type {
	case schema.TypeString, schema.TypeULID, schema.TypeUUID:
		return "String", nil
	case schema.TypeBool:
		return "Bool", nil
	case schema.TypeInt32, schema.TypeInt64:
		return "Int", nil
	case schema.TypeUint32, schema.TypeUint64:
		return "Uint", nil
	case schema.TypeFloat, schema.TypeDouble:
		return "Float", nil
	case schema.TypeBytes:
		return "Bytes", nil
	case schema.TypeEnum:
		return "String", nil // stored by label (Column.labels)
	}
	return "", fmt.Errorf("repository: lang=rust: %s: column %q has no tpp-database kind", field, c.Name)
}

// refuseChildStorage refuses a resource whose fields the Go adapters write
// into rows of other tables: value objects, deeper graphs and join tables.
// The column checks catch the belongs-to side; this names the fields stored
// with no column on the resource's own table.
func refuseChildStorage(r *resource) error {
	where := r.Table.ProtoMessage
	if len(r.Joins) > 0 {
		j := r.Joins[0]
		return fmt.Errorf("repository: lang=rust: %s.%s: stored in the join table %q; "+
			"the Rust repository writes one row per resource and cannot store it yet", where, j.Join.Field, j.Table.Name)
	}
	stored := map[protoreflect.FullName]bool{}
	for _, c := range r.Table.Columns {
		if c.Source != nil {
			stored[c.Source.FullName()] = true
		}
		if c.Source == nil && isValueObjectFK(r.Table, c) {
			stored[r.Table.Source.FullName()+"."+protoreflect.FullName(naming.StripIDSuffix(c.Name))] = true
		}
	}
	fields := r.Table.Source.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if stored[f.FullName()] {
			continue
		}
		// A scalar with no column was left out on purpose ((entity.v1.column).skip),
		// as the Go target leaves it out; a message or repeated field with no
		// column lives in rows of another table.
		if f.Message() != nil || f.IsList() || f.IsMap() {
			return fmt.Errorf("repository: lang=rust: %s.%s: stored in rows of a child table; "+
				"the Rust repository writes one row per resource and cannot store it yet", where, f.Name())
		}
	}
	return nil
}

// The (cache.v1.cache) extension and the fields read here, by number, as
// protoc-gen-service reads them: no dependency on protoc-gen-cache's types.
const (
	cacheExtension   = 52001 // (cache.v1.cache) on MessageOptions
	cacheEnabled     = 1     // CacheOptions.enabled
	cacheListField   = 7     // CacheOptions.list
	cacheListEnabled = 1     // ListCacheOptions.enabled
)

// cached reports whether m caches its records or its lists, which is when
// protoc-gen-cache writes a cache.rs for its package.
func cached(m protoreflect.MessageDescriptor) bool {
	opts := m.Options()
	if opts == nil {
		return false
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(opts)
	if err != nil {
		return false
	}
	for _, ext := range wireBytes(raw, cacheExtension) {
		if wireBool(ext, cacheEnabled) {
			return true
		}
		for _, list := range wireBytes(ext, cacheListField) {
			if wireBool(list, cacheListEnabled) {
				return true
			}
		}
	}
	return false
}

// wireBytes returns every length-delimited value of field num in b.
func wireBytes(b []byte, num protowire.Number) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return out
		}
		b = b[l:]
		if n == num && typ == protowire.BytesType {
			v, l := protowire.ConsumeBytes(b)
			if l < 0 {
				return out
			}
			out = append(out, v)
			b = b[l:]
			continue
		}
		if l = protowire.ConsumeFieldValue(n, typ, b); l < 0 {
			return out
		}
		b = b[l:]
	}
	return out
}

// wireBool reports the last value of bool field num in b.
func wireBool(b []byte, num protowire.Number) bool {
	v := false
	for len(b) > 0 {
		n, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			return v
		}
		b = b[l:]
		if n == num && typ == protowire.VarintType {
			x, l := protowire.ConsumeVarint(b)
			if l < 0 {
				return v
			}
			v = x != 0
			b = b[l:]
			continue
		}
		if l = protowire.ConsumeFieldValue(n, typ, b); l < 0 {
			return v
		}
		b = b[l:]
	}
	return v
}

// tableIndex maps "schema.table" to its table across every database.
func tableIndex(dbs []*schema.Database) map[string]*schema.Table {
	out := map[string]*schema.Table{}
	for _, db := range dbs {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				out[s.Name+"."+t.Name] = t
			}
		}
	}
	return out
}

// rustPlural is the Repositories field of r: its google.api.resource plural,
// else its pattern's last collection, in snake_case.
func rustPlural(r *resource) string {
	if p := resourcePlural(r.Table.Source); p != "" {
		return naming.SnakeCase(p)
	}
	return naming.SnakeCase(r.Segments[len(r.Segments)-1].Collection)
}

// resourcePlural reads the google.api.resource plural off the message.
func resourcePlural(d interface{ Options() proto.Message }) string {
	opts := d.Options()
	if opts == nil || !proto.HasExtension(opts, annotations.E_Resource) {
		return ""
	}
	rd, _ := proto.GetExtension(opts, annotations.E_Resource).(*annotations.ResourceDescriptor)
	return rd.GetPlural()
}

// rustMessagePath is a message's path inside its package's prost module:
// prost nests a message in a snake_case module of its parent.
func rustMessagePath(m protoreflect.MessageDescriptor) string {
	name := rustTypeName(string(m.Name()))
	for p, ok := m.Parent().(protoreflect.MessageDescriptor); ok; p, ok = p.Parent().(protoreflect.MessageDescriptor) {
		name = rustIdent(naming.SnakeCase(string(p.Name()))) + "::" + name
	}
	return name
}

// rustTypeName is a proto message name as prost spells its struct: heck's
// UpperCamelCase, so "ISBNRecord" is IsbnRecord and "book_v2" BookV2.
func rustTypeName(s string) string {
	var words []string
	var cur []rune
	runes := []rune(s)
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	for i, c := range runes {
		switch {
		case c == '_':
			flush()
			continue
		case unicode.IsUpper(c) && i > 0:
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				flush()
			}
		}
		cur = append(cur, c)
	}
	flush()
	var b strings.Builder
	for _, w := range words {
		r := []rune(strings.ToLower(w))
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}

// rustIdent escapes a snake_case name that is a Rust keyword, as prost does.
func rustIdent(s string) string {
	switch s {
	case "self", "super", "crate", "Self":
		return s + "_"
	case "as", "async", "await", "break", "const", "continue", "dyn", "else", "enum", "extern", "false", "fn", "for",
		"if", "impl", "in", "let", "loop", "match", "mod", "move", "mut", "pub", "ref", "return", "static", "struct",
		"trait", "true", "type", "unsafe", "use", "where", "while", "abstract", "become", "box", "do", "final", "macro",
		"override", "priv", "typeof", "unsized", "virtual", "yield", "try", "gen":
		return "r#" + s
	}
	return s
}

// quoteRust is a Rust string literal.
func quoteRust(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// rustCall renders prefix+name(args)+suffix at indent as rustfmt does: on one
// line when the arguments fit fn_call_width and the line max_width, else one
// argument per line.
func rustCall(indent int, prefix, name string, args []string, suffix string) string {
	pad := strings.Repeat(" ", indent)
	inline := strings.Join(args, ", ")
	line := pad + prefix + name + "(" + inline + ")" + suffix
	if len(inline) <= rustListWidth && len(line) <= rustMaxWidth {
		return line
	}
	var b strings.Builder
	b.WriteString(pad + prefix + name + "(\n")
	for _, a := range args {
		b.WriteString(pad + "    " + a + ",\n")
	}
	b.WriteString(pad + ")" + suffix)
	return b.String()
}

// rustList renders prefix+vec![items]+suffix at indent as rustfmt does: on
// one line when the items fit array_width and the line max_width, else one
// item per line.
func rustList(indent int, prefix string, items []string, suffix string) string {
	pad := strings.Repeat(" ", indent)
	inline := strings.Join(items, ", ")
	line := pad + prefix + "vec![" + inline + "]" + suffix
	if len(inline) <= rustListWidth && len(line) <= rustMaxWidth {
		return line
	}
	var b strings.Builder
	b.WriteString(pad + prefix + "vec![\n")
	for _, it := range items {
		b.WriteString(pad + "    " + it + ",\n")
	}
	b.WriteString(pad + "]" + suffix)
	return b.String()
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func renderRust(p *protogen.Plugin, path, tpl string, data any) error {
	if err := templates.Render(rustTmpl, p.NewGeneratedFile(path, ""), tpl, data); err != nil {
		return fmt.Errorf("repository: %s: %w", path, err)
	}
	return nil
}
