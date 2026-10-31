// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// projection.go prepares projection_<name>.go: a (store.v1.projection)
// table's spec -- its columns, key, range and filter surface -- as a
// repox.Projection value, plus, with the GraphQL client, the typed handlers
// that reach the table through it. Everything the projection does is the
// repox runtime's (repox/projection.go); this file only describes the table.

import (
	"fmt"
	"github.com/the-protobuf-project/store/plugin/factory/target/gopkg"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// projectionKeyColumn and projectionVersionColumn mirror
// backend.ProjectionKeyColumn and backend.ProjectionVersionColumn: the
// synthesized primary key holding the key fields as a JSON array, and the
// row version every upsert raises.
const (
	projectionKeyColumn     = "id"
	projectionVersionColumn = "row_version"
)

// projectionColumnView is one column of the generated spec.
type projectionColumnView struct {
	Field, Column, Kind, SQLType, EnumPrefix string
}

// projectionView is the template data for one projection file.
type projectionView struct {
	Header     string
	Package    string
	Imports    []string
	Var        string // the exported Projection value, e.g. "ResourceDays"
	Model      string // message name
	PB         string // qualified message type
	Name       string // message full name
	Table      string // quoted, schema-qualified table
	Key        []string
	Range      string
	IndexLeads []string // columns leading a B-tree index: the key's and each declared one's
	Sources    []string
	FilterSpec string // qualified filterx spec of the table
	Columns    []projectionColumnView
	Manual     bool

	HasGraphQL bool
	ClientPkg  string
	Domain     string
	Resource   string
	ResPkg     string
	Row        string
	InsertResp string
	DeleteResp string
}

// projectionOptions returns t's projection options when t is a projection
// table (synthesized from a message carrying (store.v1.projection)).
func projectionOptions(pb *pbIndex, t *schema.Table) (*storepbv1.ProjectionOptions, bool) {
	if t.Source != nil || t.Node == "" {
		return nil, false
	}
	msg, ok := pb.msgs[protoreflect.FullName(t.ProtoMessage)]
	if !ok || !proto.HasExtension(msg.Desc.Options(), storepbv1.E_Projection) {
		return nil, false
	}
	return proto.GetExtension(msg.Desc.Options(), storepbv1.E_Projection).(*storepbv1.ProjectionOptions), true
}

// projectionIndexLeads are the columns that lead a B-tree index of a
// projection table: the key's unique index and each declared B-tree fields
// index (a projection column is named by its field). A search with equality
// on one of them is served by that index, so the runtime writes its range
// test as a filter the planner does not estimate (see repox rangeClause).
func projectionIndexLeads(o *storepbv1.ProjectionOptions) []string {
	var out []string
	add := func(c string) {
		if c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(o.GetKey()) > 0 {
		add(o.GetKey()[0])
	}
	for _, idx := range o.GetIndexes() {
		if idx.GetExpression() != "" || len(idx.GetFields()) == 0 {
			continue
		}
		switch idx.GetMethod() {
		case storepbv1.IndexMethod_INDEX_METHOD_UNSPECIFIED, storepbv1.IndexMethod_INDEX_METHOD_BTREE:
			add(idx.GetFields()[0])
		}
	}
	return out
}

// projectionViews builds the views of s's projection tables.
func projectionViews(pb *pbIndex, db *schema.Database, s *schema.Schema, pkg string) ([]projectionView, error) {
	var out []projectionView
	for _, t := range s.Tables {
		o, ok := projectionOptions(pb, t)
		if !ok {
			continue
		}
		msg := pb.msgs[protoreflect.FullName(t.ProtoMessage)]
		imports := map[string]string{dbGoModule(db) + "/" + repoxPkg: ""}
		pbPath := string(msg.GoIdent.GoImportPath)
		pbName := pb.names.Of(pbPath)
		imports[pbPath] = pbName
		gormQual := pkg
		if gormQual == pbName {
			gormQual = pkg + "gorm"
		}
		imports[dbGormModule(db)+"/"+gopkg.Dir(db.Name)+"/"+pkg] = gormQual
		v := projectionView{
			Header:     fileHeader(db, s, "Projection "+t.LocalName+": the table spec the repox projection runtime maintains and searches."),
			Package:    pkg,
			Var:        ProjectionVar(t),
			Model:      t.LocalName,
			PB:         pbName + "." + msg.GoIdent.GoName,
			Name:       t.ProtoMessage,
			Table:      fmt.Sprintf("%q.%q", s.Name, t.Name),
			Key:        o.GetKey(),
			Sources:    o.GetSources(),
			FilterSpec: gormQual + "." + t.LocalName + "FilterSpec",
			Manual:     o.GetMaintain() == storepbv1.ProjectionMaintenance_PROJECTION_MAINTENANCE_MANUAL,
		}
		v.IndexLeads = projectionIndexLeads(o)
		for _, c := range t.Columns {
			if c.Source == nil || c.Name == projectionKeyColumn || c.Name == projectionVersionColumn {
				continue
			}
			cv, err := projectionColumn(c, string(c.Source.Name()) == o.GetRangeField())
			if err != nil {
				return nil, fmt.Errorf("repository: projection %s: %w", t.ProtoMessage, err)
			}
			if cv.Kind == "ColRange" || cv.Kind == "ColMultirange" {
				v.Range = c.Name
			}
			v.Columns = append(v.Columns, cv)
		}
		if client := dbGraphQLModule(db); client != "" {
			n := gqlNamesOf(t)
			v.HasGraphQL = true
			v.ClientPkg = clientPkgName(client)
			v.Domain, v.Resource, v.ResPkg, v.Row = n.Domain, n.Resource, n.ResPkg, n.Row
			v.InsertResp, v.DeleteResp = n.InsertResp, n.DeleteResp
			imports["context"] = ""
			imports[graphqlDSLModule] = "graphql"
			imports[client+"/"+identLower(s.Name)+"ql/"+n.ResPkg] = n.ResPkg
		}
		v.Imports = renderImports(imports)
		out = append(out, v)
	}
	return out, nil
}

// projectionColumn plans one column's runtime spec from its field.
func projectionColumn(c *schema.Column, isRange bool) (projectionColumnView, error) {
	fd := c.Source
	v := projectionColumnView{Field: string(fd.Name()), Column: c.Name}
	colOpts, _ := proto.GetExtension(fd.Options(), storepbv1.E_Column).(*storepbv1.ColumnOptions)
	v.SQLType = strings.ToLower(types.SQLForColumn(c, colOpts))
	switch {
	case isRange && fd.IsList():
		v.Kind, v.SQLType = "ColMultirange", "tstzmultirange"
		return v, nil
	case isRange:
		v.Kind, v.SQLType = "ColRange", "tstzrange"
		return v, nil
	case fd.IsMap():
		v.Kind = "ColJSON"
		return v, nil
	case fd.IsList() && fd.Kind() == protoreflect.StringKind:
		v.Kind = "ColStrings"
		return v, nil
	case fd.IsList() && fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.EnumKind && fd.Kind() != protoreflect.BytesKind:
		v.Kind = "ColNumbers"
		return v, nil
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		v.Kind = "ColText"
	case protoreflect.EnumKind:
		v.Kind = "ColEnum"
		v.EnumPrefix = naming.ScreamingSnake(string(fd.Enum().Name())) + "_"
	case protoreflect.BoolKind:
		v.Kind = "ColBool"
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		v.Kind = "ColInt"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		v.Kind = "ColUint64"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		v.Kind = "ColFloat"
	case protoreflect.MessageKind:
		switch fd.Message().FullName() {
		case "google.protobuf.Timestamp":
			v.Kind = "ColTimestamp"
		case "google.type.Date":
			v.Kind = "ColDate"
		default:
			v.Kind = "ColJSON"
		}
	default:
		return v, fmt.Errorf("field %q: unsupported kind %s", fd.Name(), fd.Kind())
	}
	return v, nil
}

// ProjectionColumnKind is the repox.ColumnKind constant (its name, e.g.
// "ColMultirange") a projection column stores its field as, for targets that
// copy a projection elsewhere (target=opensearch).
func ProjectionColumnKind(c *schema.Column, isRange bool) (string, error) {
	v, err := projectionColumn(c, isRange)
	return v.Kind, err
}

// ProjectionVar is the name of a projection's generated repox.Projection
// value ("RoomDays" for RoomDay).
func ProjectionVar(t *schema.Table) string {
	return naming.PascalGo(naming.Pluralize(naming.SnakeCase(t.LocalName)))
}
