// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

// Package opensearch generates the OpenSearch copy of every
// (store.v1.projection): a document mapping derived from the projection's
// columns, an index template, versioned indices behind an alias, a search
// layer (Get, Search, Count, Facet, Iterate) that translates the same AIP-160
// filters and AIP-132 order_by the Postgres search takes through the filterx
// OpenSearch engine, a change-feed indexer fed by the repositories'
// repox.ProjectionFeed (bulk, version_type=external), and a full reindex with
// an alias swap. The client is github.com/oh-tarnished/opensearch-go-sdk.
//
// The Postgres projection table stays the source of truth: OpenSearch holds a
// copy, versioned by the table's row_version, that the feed keeps current and
// Reindex rebuilds.
package opensearch

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"go/format"
	"io"
	"sort"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit"
	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/plugin/factory/target/gopkg"
	"github.com/the-protobuf-project/store/plugin/factory/target/repository"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

//go:embed templates/*.tpl
var templateFS embed.FS

var tmpl = templates.MustParse(templateFS, "templates/*.tpl")

// osxPkg is the shared runtime package name and directory.
const osxPkg = "osx"

// SDKModule is the OpenSearch client the generated code imports.
const SDKModule = "github.com/oh-tarnished/opensearch-go-sdk"

// Generator implements schema.IRTarget for the OpenSearch search layer.
type Generator struct{}

var _ schema.IRTarget = (*Generator)(nil)

// Name returns the target identifier used in buf.gen.yaml opt: [target=opensearch].
func (g *Generator) Name() string { return "opensearch" }

// Generate renders from the databases alone; the JSON key types then come
// from nowhere, so prefer GenerateIR.
func (g *Generator) Generate(p *protogen.Plugin, dbs []*schema.Database) error {
	return g.GenerateIR(p, &schema.IR{Databases: dbs})
}

// indexView is the template data of one projection's index file.
type indexView struct {
	Header     string
	Package    string
	Imports    []string
	Model      string
	Var        string // e.g. RoomDayIndex
	Projection string // qualified repox.Projection value, e.g. search.RoomDays
	Table      string
	Alias      string
	MappingVar string
	Mapping    string
}

// GenerateIR writes osx/osx.go once and <db>/<schema>/<projection>_index.go
// per projection.
func (g *Generator) GenerateIR(p *protogen.Plugin, ir *protokit.IR) error {
	fx := facets.New(ir)
	emitted := false
	for _, db := range ir.Databases {
		var views []indexView
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				proj := fx.Projection(t)
				if proj == nil {
					continue
				}
				if db.Opt("go_module") == "" || db.Opt("gorm_module") == "" || db.Opt("repository_module") == "" {
					return fmt.Errorf("opensearch: database %q needs the go_module, gorm_module and repository_module opts "+
						"(import paths of this output dir, the generated gorm output and the generated repository output)", db.Name)
				}
				v, err := viewOf(db, s, t, proj, fx)
				if err != nil {
					return err
				}
				views = append(views, v)
			}
		}
		if len(views) == 0 {
			continue
		}
		if !emitted {
			f := p.NewGeneratedFile(osxPkg+"/"+osxPkg+".go", "")
			if err := renderGo(f, "osx.go.tpl", map[string]any{
				"Header": provenance.Render("//", header.Info{
					PluginVersion: db.PluginVersion,
					ProtocVersion: db.ProtocVersion,
					Database:      db.Name,
					SchemaLabel:   "package",
					Schema:        osxPkg,
					Notes:         []string{"Shared runtime for the generated OpenSearch indices."},
				}),
				"FilterxImport": db.Opt("gorm_module") + "/filterx",
				"RepoxImport":   db.Opt("repository_module") + "/repox",
				"SDKImport":     SDKModule + "/opensearch",
			}); err != nil {
				return fmt.Errorf("opensearch: osx: %w", err)
			}
			emitted = true
		}
		for _, v := range views {
			name := fmt.Sprintf("%s/%s/%s_index.go", gopkg.Dir(db.Name), v.Package, naming.SnakeCase(v.Model))
			if err := renderGo(p.NewGeneratedFile(name, ""), "index.go.tpl", v); err != nil {
				return fmt.Errorf("opensearch: %s: %w", name, err)
			}
		}
	}
	return nil
}

func viewOf(db *schema.Database, s *schema.Schema, t *schema.Table, proj *facets.Projection, fx facets.Set) (indexView, error) {
	pkg := naming.GoPackage(s.Name)
	repoPath := db.Opt("repository_module") + "/" + gopkg.Dir(db.Name) + "/" + pkg
	alias := proj.Options.GetSearchIndex()
	if alias == "" {
		alias = s.Name + "_" + t.Name
	}
	mapping, err := Mapping(t, proj.Options, fx)
	if err != nil {
		return indexView{}, fmt.Errorf("opensearch: projection %s: %w", t.ProtoMessage, err)
	}
	imports := []string{`"` + db.Opt("go_module") + "/" + osxPkg + `"`, `"` + repoPath + `"`}
	sort.Strings(imports)
	return indexView{
		Header: provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        t.SourceProto,
			Database:      db.Name,
			Schema:        s.Name,
			Notes:         []string{"Projection " + t.LocalName + " in OpenSearch: its mapping and index."},
		}),
		Package:    pkg,
		Imports:    imports,
		Model:      t.LocalName,
		Var:        t.LocalName + "Index",
		Projection: lastSegment(repoPath) + "." + repository.ProjectionVar(t),
		Table:      fmt.Sprintf("%q.%q", s.Name, t.Name),
		Alias:      alias,
		MappingVar: naming.CamelFirst(t.LocalName) + "Mapping",
		Mapping:    mapping,
	}, nil
}

func lastSegment(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}

// TypedSuffix mirrors filterx.OpenSearchTypedSuffix: the object holding the
// typed copies of a JSON column's declared keys.
const TypedSuffix = "_typed"

// Date formats: a date field keeps YYYY-MM-DD first, so terms aggregations
// print it the way PostgreSQL does.
const (
	dateFormat    = "strict_date||strict_date_optional_time||epoch_millis"
	instantFormat = "strict_date_optional_time||epoch_millis"
)

// Mapping is the document mapping of projection table t, as indented JSON:
// keyword for text, enums and tags, the numeric and boolean types, date for
// dates and timestamps, date_range for the range column, flat_object for a
// JSON column, and a <column>_typed object holding the declared JSON keys
// (store.v1.query json_keys) typed so they compare and sort as their kind.
func Mapping(t *schema.Table, o *storepbv1.ProjectionOptions, fx facets.Set) (string, error) {
	props := map[string]any{"id": map[string]any{"type": "keyword"}}
	for _, c := range t.Columns {
		if c.Source == nil || c.Name == "id" || c.Name == "row_version" {
			continue
		}
		kind, err := repository.ProjectionColumnKind(c, string(c.Source.Name()) == o.GetRangeField())
		if err != nil {
			return "", err
		}
		var m map[string]any
		switch kind {
		case "ColText", "ColEnum", "ColStrings":
			m = map[string]any{"type": "keyword"}
		case "ColBool":
			m = map[string]any{"type": "boolean"}
		case "ColInt":
			m = map[string]any{"type": "long"}
		case "ColUint64":
			m = map[string]any{"type": "unsigned_long"}
		case "ColFloat":
			m = map[string]any{"type": "double"}
		case "ColTimestamp":
			m = map[string]any{"type": "date", "format": instantFormat}
		case "ColDate":
			m = map[string]any{"type": "date", "format": dateFormat}
		case "ColRange", "ColMultirange":
			m = map[string]any{"type": "date_range", "format": instantFormat}
		case "ColNumbers":
			switch c.Source.Kind() {
			case protoreflect.BoolKind:
				m = map[string]any{"type": "boolean"}
			case protoreflect.FloatKind, protoreflect.DoubleKind:
				m = map[string]any{"type": "double"}
			default:
				m = map[string]any{"type": "long"}
			}
		case "ColJSON":
			m = map[string]any{"type": "flat_object"}
			if keys := fx.Query(c).GetJsonKeys(); len(keys) > 0 {
				typed := map[string]any{}
				for _, k := range keys {
					setPath(typed, splitPath(k.GetPath()), jsonKeyMapping(k.GetType()))
				}
				props[c.Name+TypedSuffix] = map[string]any{"properties": typed}
			}
		default:
			return "", fmt.Errorf("column %q: no mapping for %s", c.Name, kind)
		}
		props[c.Name] = m
	}
	b, err := json.MarshalIndent(map[string]any{"dynamic": false, "properties": props}, "", "  ")
	return string(b), err
}

func jsonKeyMapping(t storepbv1.JsonKeyType) map[string]any {
	switch t {
	case storepbv1.JsonKeyType_JSON_KEY_TYPE_INT64:
		return map[string]any{"type": "long"}
	case storepbv1.JsonKeyType_JSON_KEY_TYPE_DOUBLE:
		return map[string]any{"type": "double"}
	case storepbv1.JsonKeyType_JSON_KEY_TYPE_BOOL:
		return map[string]any{"type": "boolean"}
	case storepbv1.JsonKeyType_JSON_KEY_TYPE_DATE:
		return map[string]any{"type": "date", "format": dateFormat}
	case storepbv1.JsonKeyType_JSON_KEY_TYPE_TIMESTAMP:
		return map[string]any{"type": "date", "format": instantFormat}
	default:
		return map[string]any{"type": "keyword"}
	}
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return out
}

// setPath puts leaf at path in props, nesting objects as properties.
func setPath(props map[string]any, path []string, leaf map[string]any) {
	if len(path) == 1 {
		props[path[0]] = leaf
		return
	}
	obj, _ := props[path[0]].(map[string]any)
	if obj == nil {
		obj = map[string]any{"properties": map[string]any{}}
		props[path[0]] = obj
	}
	setPath(obj["properties"].(map[string]any), path[1:], leaf)
}

func renderGo(w io.Writer, name string, data any) error {
	var buf bytes.Buffer
	if err := templates.Render(tmpl, &buf, name, data); err != nil {
		return err
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("gofmt %s: %w\nrendered source:\n%s", name, err, buf.String())
	}
	_, err = w.Write(formatted)
	return err
}
