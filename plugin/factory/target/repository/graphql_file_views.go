// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// graphql_file_views.go assembles the template data for graphql.go and
// graphql_convert.go (emitted only when the graphql_module opt is set).

import (
	"github.com/the-protobuf-project/protokit/schema"
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
		if r.CreateTimeField != "" || r.UpdateTimeField != "" {
			needTime = true
		}
	}
	if needTime {
		imports["time"] = ""
		imports["google.golang.org/protobuf/types/known/timestamppb"] = ""
	}
	addPBImports(pb, s, imports)
	gormPkg := addGormModelsImport(pb, db, s, pkg, imports)
	return map[string]any{
		"Header":    fileHeader(db, s, "GraphQL adapters over the generated client — same repository surface as the gorm adapters."),
		"Package":   pkg,
		"GormPkg":   gormPkg,
		"ClientPkg": clientPkgName(client),
		"Imports":   renderImports(imports),
		"Resources": rs,
	}
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
	addPBImports(pb, s, imports)
	addPBEnumImports(pb, s, imports)
	return map[string]any{
		"Header":    fileHeader(db, s, "Proto↔row/input converters for the GraphQL adapters, with the shared scalar codecs."),
		"Package":   pkg,
		"Imports":   renderImports(imports),
		"Resources": rs,
		"VOConvs":   voConvs,
		"Needs":     needs,
	}
}
