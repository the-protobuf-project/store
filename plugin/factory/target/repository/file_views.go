// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// file_views.go assembles the per-file template data for names.go, mask.go,
// and gorm.go from the planned gormResourceViews. Each view carries its own
// import list so every emitted file imports exactly what its fragments use.

import (
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
)

// fileHeader renders the banner for one generated file of schema s.
func fileHeader(db *schema.Database, s *schema.Schema, note string) string {
	return provenance.Render("//", header.Info{
		PluginVersion: db.PluginVersion,
		ProtocVersion: db.ProtocVersion,
		Source:        strings.Join(s.SourceProtos(), ", "),
		Database:      db.Name,
		Schema:        s.Name,
		Notes:         []string{note},
	})
}

// namesView prepares names.go: the per-resource resource-name codecs.
func namesView(db *schema.Database, s *schema.Schema, pkg string, rs []gormResourceView) map[string]any {
	return map[string]any{
		"Header":      fileHeader(db, s, "AIP resource-name codecs for this schema's repositories."),
		"Package":     pkg,
		"RepoxImport": dbGoModule(db) + "/" + repoxPkg,
		"Resources":   rs,
	}
}

// maskView prepares mask.go: the per-resource field-mask merge functions.
func maskView(pb *pbIndex, db *schema.Database, s *schema.Schema, pkg string, rs []gormResourceView) map[string]any {
	imports := map[string]string{
		dbGoModule(db) + "/" + repoxPkg: "",
	}
	addPBImports(pb, s, imports)
	return map[string]any{
		"Header":    fileHeader(db, s, "Field-mask merge functions shared by every adapter of this schema."),
		"Package":   pkg,
		"Imports":   renderImports(imports),
		"Resources": rs,
	}
}

// gormFileView prepares gorm.go: the GORM adapters.
func gormFileView(pb *pbIndex, db *schema.Database, s *schema.Schema, pkg string, rs []gormResourceView) map[string]any {
	imports := map[string]string{
		"context":                          "",
		"gorm.io/gorm":                     "",
		"google.golang.org/protobuf/proto": "",
		dbGoModule(db) + "/" + repoxPkg:    "",
		dbGormModule(db) + "/filterx":      "",
	}
	for _, r := range rs {
		if r.Parented {
			imports["fmt"] = ""
		}
		for _, cp := range r.CrossVOPkgs {
			imports[dbGormModule(db)+"/"+db.Name+"/"+cp] = ""
		}
	}
	addPBImports(pb, s, imports)
	gormPkg := addGormModelsImport(pb, db, s, pkg, imports)
	return map[string]any{
		"Header":    fileHeader(db, s, "GORM adapters composing the generated models, stores, converters, and filterx specs."),
		"Package":   pkg,
		"GormPkg":   gormPkg,
		"Imports":   renderImports(imports),
		"Resources": rs,
	}
}

// addPBEnumImports adds the generated proto packages of every enum a
// repository resource's columns reference — enums may live in a different
// proto package than the resource message (shared enums).
func addPBEnumImports(pb *pbIndex, s *schema.Schema, imports map[string]string) {
	for _, t := range s.Tables {
		if t.Source == nil || t.ValueObject || resourcePattern(t.Source) == "" {
			continue
		}
		for _, c := range t.Columns {
			if c.Enum == nil || c.Source == nil {
				continue
			}
			m, ok := pb.msgs[c.Source.ContainingMessage().FullName()]
			if !ok {
				continue
			}
			for _, f := range m.Fields {
				if f.Desc.FullName() == c.Source.FullName() && f.Enum != nil {
					path := string(f.Enum.GoIdent.GoImportPath)
					imports[path] = pb.names.Of(path)
				}
			}
		}
	}
}

// addPBImports adds the generated proto packages of s's repository resources.
func addPBImports(pb *pbIndex, s *schema.Schema, imports map[string]string) {
	for _, t := range s.Tables {
		if t.Source == nil {
			continue
		}
		if msg, ok := pb.msgs[t.Source.FullName()]; ok && resourcePattern(t.Source) != "" && !t.ValueObject {
			path := string(msg.GoIdent.GoImportPath)
			imports[path] = pb.names.Of(path)
		}
	}
}

// gormModelsQual is the identifier the adapters use for schema s's generated
// gorm models package, and the alias its import carries.
//
// That package is named after the schema, and a schema derived from a versioned
// proto package carries the same name its protos do — schema "resource_v1"
// yields "resourcev1", exactly what `option go_package = ".../resource/v1;resourcev1"`
// names the proto package the same file imports. One of the two must step
// aside, and it cannot be the proto package: that name is protoc-gen-go's, and
// the .pb.go types are spelled with it. So the models package takes the suffix.
//
// Every view that qualifies the models package calls this — the adapter files
// for their import alias, gormResourceViews for the VO fragments it bakes — so
// the alias and the fragments cannot disagree.
func gormModelsQual(pb *pbIndex, s *schema.Schema, pkg string) string {
	protoPkgs := map[string]string{}
	addPBImports(pb, s, protoPkgs)
	addPBEnumImports(pb, s, protoPkgs)
	for _, name := range protoPkgs {
		if name == pkg {
			return pkg + "gorm"
		}
	}
	return pkg
}

// addGormModelsImport registers s's generated gorm models package under the
// qualifier gormModelsQual picked, and returns it. renderImports drops the
// alias again when it matches the path's last segment, so the usual
// no-collision case still emits a bare import.
func addGormModelsImport(pb *pbIndex, db *schema.Database, s *schema.Schema, pkg string, imports map[string]string) string {
	qual := gormModelsQual(pb, s, pkg)
	imports[dbGormModule(db)+"/"+db.Name+"/"+pkg] = qual
	return qual
}
