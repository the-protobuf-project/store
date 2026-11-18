// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package gorm

// filter_view.go prepares the template views for the filters feature: the
// per-schema filters.go spec file and the once-per-tree filterx package
// (backend-neutral core plus the chainable Gorm and Hasura engines, and — with
// the telemetry opt — the telemetry Observer adapter). The planning logic
// lives in filterspec.go; the templates are presentation only.

import (
	"strings"

	"github.com/the-protobuf-project/protokit/header"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
	"github.com/the-protobuf-project/store/plugin/factory/provenance"
	"github.com/the-protobuf-project/store/telemetry"
)

// filterxImport is the import path of the shared AIP-160/132 engines the
// generated specs are data for. It lives in runtime-go, so it is no longer
// generated.
const filterxImport = "github.com/the-protobuf-project/runtime-go/database/filterx"

// filtersView assembles the data for one schema's filters.go: one Spec var per
// table. Returns nil when no table in the schema yields a spec.
func filtersView(db *schema.Database, s *schema.Schema, pkg string, fx facets.Set) map[string]any {
	var tables []filterTableView
	nested := nestedRefs(db)
	for _, t := range s.Tables {
		if v, ok := buildFilterTable(db, s, t, fx, nested); ok {
			tables = append(tables, v)
		}
	}
	if len(tables) == 0 {
		return nil
	}
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Source:        strings.Join(s.SourceProtos(), ", "),
			Database:      db.Name,
			Schema:        s.Name,
			Notes:         []string{"AIP-160 filter / AIP-132 order_by specs — data for the shared filterx engines."},
		}),
		"Package":       pkg,
		"FilterxImport": filterxImport,
		"Tables":        tables,
	}
}

// filterxObserverView is the view of the filterx observer the telemetry adapter
// package carries: it adapts the SDK handle to runtime-go filterx's Observer.
func filterxObserverView(db *schema.Database) map[string]any {
	return map[string]any{
		"Header": provenance.Render("//", header.Info{
			PluginVersion: db.PluginVersion,
			ProtocVersion: db.ProtocVersion,
			Database:      db.Name,
			SchemaLabel:   "package",
			Schema:        telemetry.Package,
			Notes:         []string{"The telemetry SDK as the Observer of runtime-go's filterx list engines."},
		}, telemetry.Module),
		"Package":            telemetry.Package,
		"FilterxImport":      filterxImport,
		"TelemetrySDKImport": telemetryModule,
	}
}
