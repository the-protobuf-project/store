// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// graphql_file_views.go assembles the template data for graphql.go and
// graphql_convert.go (emitted only when the graphql_module opt is set).

import (
	"github.com/the-protobuf-project/protokit/schema"
	"slices"
	"sort"
	"strings"
)

// graphqlDSLModule is the predicate/scalar DSL package of the generated
// client's runtime — the graphql target's default runtime sibling.
const graphqlDSLModule = "github.com/the-protobuf-project/runtime-go/network/graphql"

// identLower keeps [a-z0-9] of the lowercased name (the graphql target's
// identifier() rule); the client's per-domain package is identLower(schema)+"ql".
func identLower(name string) string {
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		case r >= 'A' && r <= 'Z':
			out = append(out, byte(r-'A'+'a'))
		}
	}
	return string(out)
}

// graphqlFileView prepares graphql.go: the GraphQL adapters.
func graphqlFileView(pb *pbIndex, db *schema.Database, s *schema.Schema, pkg string, rs []gqlResourceView) map[string]any {
	client := dbGraphQLModule(db)
	domainPkg := identLower(s.Name) + "ql"
	imports := map[string]string{
		"context":                          "",
		"errors":                           "",
		"fmt":                              "",
		"strings":                          "",
		"google.golang.org/protobuf/proto": "",
		graphqlDSLModule:                   "graphql",
		dbGoModule(db) + "/" + repoxPkg:    "",
		dbGormModule(db) + "/filterx":      "",
		client:                             clientPkgName(client),
	}
	needTime := false
	for _, r := range rs {
		imports[client+"/"+domainPkg+"/"+r.ResPkg] = r.ResPkg
		if r.Parented {
			imports["fmt"] = ""
		}
		if r.SoftDelete {
			imports["fmt"] = ""
			imports["time"] = ""
		}
		if r.CreateTimeField != "" || r.UpdateTimeField != "" {
			needTime = true
		}
		for _, p := range r.DeepPkgs {
			parts := strings.Split(p, "/")
			imports[client+"/"+p] = parts[len(parts)-1]
		}
		for _, p := range r.DeepPBImports {
			imports[p] = pb.names.Of(p)
		}
	}
	if needTime {
		imports["time"] = ""
		imports["google.golang.org/protobuf/types/known/timestamppb"] = ""
	}
	addPBImports(pb, s, imports)
	gormPkg := addGormModelsImport(pb, db, s, pkg, imports)
	return map[string]any{
		"Header":      fileHeader(db, s, "GraphQL adapters over the generated client — same repository surface as the gorm adapters."),
		"Package":     pkg,
		"GormPkg":     gormPkg,
		"ClientPkg":   clientPkgName(client),
		"Imports":     renderImports(imports),
		"Resources":   rs,
		"HasBatch":    len(rs) > 0,
		"HasExpiring": slices.ContainsFunc(rs, func(r gqlResourceView) bool { return r.Expiring }),
	}
}

// graphqlParentViews prepares graphql_parent_<schema>.go for each schema whose
// rows the resources of s look up as nested ancestors (gqlParentLookup), keyed
// by file name.
func graphqlParentViews(db *schema.Database, s *schema.Schema, pkg string, rs []gqlResourceView) map[string]map[string]any {
	client := dbGraphQLModule(db)
	bySchema := map[string][]gqlParentLookup{}
	seen := map[string]bool{}
	for _, r := range rs {
		for _, lk := range r.ParentLookups {
			if seen[lk.Func] {
				continue
			}
			seen[lk.Func] = true
			bySchema[lk.DomPkg] = append(bySchema[lk.DomPkg], lk)
		}
	}
	out := map[string]map[string]any{}
	for dom, lks := range bySchema {
		sort.Slice(lks, func(i, j int) bool { return lks[i].Func < lks[j].Func })
		imports := map[string]string{
			"context":                       "",
			dbGoModule(db) + "/" + repoxPkg: "",
			client:                          clientPkgName(client),
		}
		for _, lk := range lks {
			imports[client+"/"+lk.DomPkg+"/"+lk.ResPkg] = lk.ResPkg
		}
		out["graphql_parent_"+strings.TrimSuffix(dom, "ql")+".go"] = map[string]any{
			"Header":    fileHeader(db, s, "Lookups of nested ancestors' rows in schema "+strings.TrimSuffix(dom, "ql")+", by name."),
			"Package":   pkg,
			"ClientPkg": clientPkgName(client),
			"Imports":   renderImports(imports),
			"Lookups":   lks,
		}
	}
	return out
}

// graphqlReferrerViews prepares graphql_referrers_<schema>.go for each schema
// holding foreign keys into the resources of s that a purge asks about
// (gqlReferrer), keyed by file name.
func graphqlReferrerViews(db *schema.Database, s *schema.Schema, pkg string, rs []gqlResourceView) map[string]map[string]any {
	client := dbGraphQLModule(db)
	bySchema := map[string][]gqlReferrer{}
	seen := map[string]bool{}
	for _, r := range rs {
		for _, f := range r.GQLReferrers {
			if seen[f.Func] {
				continue
			}
			seen[f.Func] = true
			bySchema[f.DomPkg] = append(bySchema[f.DomPkg], f)
		}
	}
	out := map[string]map[string]any{}
	for dom, fs := range bySchema {
		sort.Slice(fs, func(i, j int) bool { return fs[i].Func < fs[j].Func })
		imports := map[string]string{
			"context": "",
			client:    clientPkgName(client),
		}
		for _, f := range fs {
			imports[client+"/"+f.DomPkg+"/"+f.ResPkg] = f.ResPkg
		}
		out["graphql_referrers_"+strings.TrimSuffix(dom, "ql")+".go"] = map[string]any{
			"Header":    fileHeader(db, s, "Referrers in schema "+strings.TrimSuffix(dom, "ql")+" a purge asks about, by key."),
			"Package":   pkg,
			"ClientPkg": clientPkgName(client),
			"Imports":   renderImports(imports),
			"Referrers": fs,
		}
	}
	return out
}

// graphqlConvertView prepares graphql_convert.go: converters + scalar helpers.
func graphqlConvertView(pb *pbIndex, db *schema.Database, s *schema.Schema, pkg string, rs []gqlResourceView, voConvs []gqlVOConv, needs helperNeeds) map[string]any {
	client := dbGraphQLModule(db)
	domainPkg := identLower(s.Name) + "ql"
	imports := map[string]string{
		dbGoModule(db) + "/" + repoxPkg: "",
	}
	for _, r := range rs {
		imports[client+"/"+domainPkg+"/"+r.ResPkg] = r.ResPkg
	}
	for _, c := range voConvs {
		imports[c.ResPkgPath] = c.ResPkgName
		imports[c.PBPath] = c.PBName
		for _, p := range c.EnumPaths {
			imports[p] = pb.names.Of(p)
		}
	}
	if needs.Wrappers {
		imports["google.golang.org/protobuf/types/known/wrapperspb"] = "wrapperspb"
	}
	// The patch builders always speak graphql.Value/Null.
	imports[graphqlDSLModule] = "graphql"
	if needs.Ts {
		imports["time"] = ""
		imports["google.golang.org/protobuf/types/known/timestamppb"] = ""
	}
	if needs.Interval {
		imports["strings"] = ""
		imports["time"] = ""
		imports["google.golang.org/genproto/googleapis/type/interval"] = ""
		imports["google.golang.org/protobuf/types/known/timestamppb"] = ""
	}
	if needs.DT {
		imports["fmt"] = ""
		imports["strings"] = ""
		imports["time"] = ""
		imports["google.golang.org/genproto/googleapis/type/datetime"] = ""
		imports["google.golang.org/protobuf/types/known/durationpb"] = ""
	}
	if needs.Date {
		imports["fmt"] = ""
		imports["time"] = ""
		imports["google.golang.org/genproto/googleapis/type/date"] = ""
	}
	if needs.Struct {
		imports["encoding/json"] = ""
		imports["google.golang.org/protobuf/types/known/structpb"] = ""
	}
	if needs.Bytes {
		imports["encoding/base64"] = ""
		imports["encoding/hex"] = ""
		imports["encoding/json"] = ""
		imports["strings"] = ""
	}
	if needs.Duration {
		imports["time"] = ""
		imports["google.golang.org/protobuf/types/known/durationpb"] = ""
	}
	if needs.Decimal {
		imports["strconv"] = ""
	}
	if needs.Enum {
		imports["strings"] = ""
	}
	if needs.GormEnums {
		addGormModelsImport(pb, db, s, pkg, imports)
	}
	if needs.DecimalMsg {
		imports["google.golang.org/genproto/googleapis/type/decimal"] = ""
	}
	if needs.Doc {
		imports["encoding/json"] = ""
		imports["google.golang.org/protobuf/encoding/protojson"] = ""
		imports["google.golang.org/protobuf/proto"] = ""
		for _, p := range needs.DocPaths {
			imports[p] = pb.names.Of(p)
		}
	}
	addPBImports(pb, s, imports)
	if needs.Enum {
		// The proto enum types are named only by conversions outside the
		// gorm mappers (an enum of another schema).
		addPBEnumImports(pb, s, imports)
	}
	return map[string]any{
		"Header":    fileHeader(db, s, "Proto↔row/input converters for the GraphQL adapters, with the shared scalar codecs."),
		"Package":   pkg,
		"Imports":   renderImports(imports),
		"Resources": rs,
		"VOConvs":   voConvs,
		"Needs":     needs,
	}
}

// graphqlRuntimeModule holds runtime.Tx, the mutation request a repox.Unit
// commits.
const graphqlRuntimeModule = "github.com/the-protobuf-project/runtime-go/network/runtime"
