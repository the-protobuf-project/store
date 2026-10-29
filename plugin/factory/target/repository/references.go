// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// references.go plans the two halves of reference integrity the repositories
// enforce above the database:
//
//   - External references (extRef): a google.api.resource_reference field
//     whose target no foreign key reaches -- protokit left it a soft FK
//     (schema.ReferenceUnresolved) because the target lives in another
//     physical database or another service. Writes prove the target exists
//     with its AIP Get (repox.References.Resolve); deletes of a target ask the
//     referring side's store.v1.ReferenceService, which these fields feed.
//   - Referrers by foreign key (referrerFK): rows of this physical database
//     that hold a real foreign key to a resource's table, in any database of
//     the run -- a join table's row, keeping one element of a repeated
//     reference, among them. A purge leaves a row they still name in place
//     instead of failing on the constraint (or cascading through it).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/ddn/ddnrefs"
	"github.com/the-protobuf-project/store/plugin/pb/storepbv1"
)

// extRef is one external reference field of a resource.
type extRef struct {
	Field    string // proto field name, e.g. "customer"
	Column   string // its column, e.g. "customer"
	Getter   string // proto getter, e.g. "GetCustomer"
	Repeated bool   // a repeated string of names
	Type     string // the target's AIP resource type
	OnDelete string // repox constant: OnDeleteAllow, OnDeleteRestrict, OnDeleteNotify
	Policy   string // the same as the store.v1 enum value name, "ON_DELETE_ALLOW"
}

// referrerFK is a foreign key into a resource's table from another table of
// the same physical database.
type referrerFK struct {
	Schema, Table, Column string // referencing side
	RefColumn             string // the column of the resource's table it holds
	// DB and From are the referencing database and table: a GraphQL purge
	// asks the ones of its own database (one client) through them.
	DB   *schema.Database
	From *schema.Table
	// Join marks a join table's key to its target (joinTargetFK): one element
	// of a repeated reference, which a delete without a purge (no soft
	// delete) refuses to go under, as a singular reference's constraint does.
	Join bool
}

// tableKey identifies a table across the run's databases.
type tableKey struct{ schema, table string }

// referrerIndex maps every table to the foreign keys into it that a purge must
// respect: relations and parent FKs from resource tables (not a value object's
// own rows, which are owned and go with it), and every constraint-only FK,
// across all of dbs. Two databases of a run that hold foreign keys into each
// other share a physical database (protokit emits no constraint otherwise),
// so the schema-qualified names are unambiguous.
func referrerIndex(dbs []*schema.Database) map[tableKey][]referrerFK {
	idx := map[tableKey][]referrerFK{}
	for _, db := range dbs {
		for _, s := range db.Schemas {
			for _, t := range s.Tables {
				if t.ValueObject {
					continue
				}
				add := func(fk *schema.ForeignKey) {
					if fk.ReferencedTable == "" || fk.ReferencedColumn == "" {
						return
					}
					k := tableKey{fk.ReferencedSchema, fk.ReferencedTable}
					idx[k] = append(idx[k], referrerFK{
						Schema: s.Name, Table: t.Name, Column: fk.Column, RefColumn: fk.ReferencedColumn,
						DB: db, From: t, Join: joinTargetFK(t, fk),
					})
				}
				for _, fk := range t.ForeignKeys {
					// Embed FKs are owned rows, not referrers, and so is a join
					// table's key to its owner (its rows are the owner's field).
					// Its key to the target is a reference, as a singular
					// resource_reference's column is (protokit sets
					// ReferencedProto on both join keys).
					if (fk.ReferencedProto == "" && !joinOwnerFK(t, fk)) || joinTargetFK(t, fk) {
						add(fk)
					}
				}
				for _, fk := range t.ExternalForeignKeys {
					add(fk)
				}
			}
		}
	}
	for k := range idx {
		sort.SliceStable(idx[k], func(i, j int) bool {
			a, b := idx[k][i], idx[k][j]
			return a.Schema+"."+a.Table+"."+a.Column < b.Schema+"."+b.Table+"."+b.Column
		})
	}
	return idx
}

// planReferences fills each resource's external references, AIP type and
// referrers by foreign key.
func planReferences(pb *pbIndex, resources map[*schema.Table]*resource, rev map[tableKey][]referrerFK) {
	for t, r := range resources {
		r.Type = resourceType(t.Source)
		r.ReferencedBy = rev[tableKey{r.Schema.Name, t.Name}]
		r.ExtRefs = planExtRefs(pb, t)
	}
}

// planExtRefs lists t's external reference fields, in column order.
func planExtRefs(pb *pbIndex, t *schema.Table) []extRef {
	soft := map[string]*schema.ForeignKey{}
	for _, fk := range t.UnresolvedForeignKeys {
		if fk.ReferencedType != "" && fk.ReferencedType != "*" {
			soft[fk.Column] = fk
		}
	}
	msg := pb.msgs[t.Source.FullName()]
	var out []extRef
	for _, c := range t.Columns {
		fk, ok := soft[c.Name]
		if !ok || c.Source == nil || c.Source.Kind() != protoreflect.StringKind || msg == nil {
			continue
		}
		getter := ""
		for _, f := range msg.Fields {
			if f.Desc.FullName() == c.Source.FullName() {
				getter = "Get" + f.GoName
			}
		}
		if getter == "" {
			continue
		}
		out = append(out, extRef{
			Field:    string(c.Source.Name()),
			Column:   c.Name,
			Getter:   getter,
			Repeated: c.Source.IsList(),
			Type:     fk.ReferencedType,
			OnDelete: onDeleteConst(referenceOptions(c.Source).GetOnDelete()),
			Policy:   policyName(referenceOptions(c.Source).GetOnDelete()),
		})
	}
	return out
}

// referenceOptions reads (store.v1.reference) off a field, nil when unset.
func referenceOptions(fd protoreflect.FieldDescriptor) *storepbv1.ReferenceOptions {
	if fd == nil || !proto.HasExtension(fd.Options(), storepbv1.E_Reference) {
		return nil
	}
	o, _ := proto.GetExtension(fd.Options(), storepbv1.E_Reference).(*storepbv1.ReferenceOptions)
	return o
}

// onDeleteConst names the repox constant of a policy; unspecified is allow.
func onDeleteConst(p storepbv1.ReferenceOptions_OnDelete) string {
	switch p {
	case storepbv1.ReferenceOptions_ON_DELETE_RESTRICT:
		return "repox.OnDeleteRestrict"
	case storepbv1.ReferenceOptions_ON_DELETE_NOTIFY:
		return "repox.OnDeleteNotify"
	}
	return "repox.OnDeleteAllow"
}

// policyName is a policy's enum value name; unspecified is allow.
func policyName(p storepbv1.ReferenceOptions_OnDelete) string {
	if p == storepbv1.ReferenceOptions_ON_DELETE_UNSPECIFIED {
		p = storepbv1.ReferenceOptions_ON_DELETE_ALLOW
	}
	return p.String()
}

// referencedSQL is the query a GORM purge asks before removing a row: does any
// row, deleted or not, still hold a foreign key to it? One placeholder, what
// the row is found by (keyColumn). Empty when nothing can refer to the table.
func referencedSQL(r *resource) string {
	return referencedSQLOf(r, r.ReferencedBy)
}

// joinReferencedSQL is the query a GORM delete of a resource without soft
// delete asks first: does a join table's row still name it (referrerFK.Join)?
// Empty when none can, or when the resource is soft-deleted (its purge asks
// referencedSQL).
func joinReferencedSQL(r *resource) string {
	if r.Cols.DeleteTime != nil {
		return ""
	}
	var joins []referrerFK
	for _, f := range r.ReferencedBy {
		if f.Join {
			joins = append(joins, f)
		}
	}
	return referencedSQLOf(r, joins)
}

// referencedSQLOf is referencedSQL over the referrers fs.
func referencedSQLOf(r *resource, fs []referrerFK) string {
	if len(fs) == 0 || r.Cols.PK == nil {
		return ""
	}
	var terms []string
	for i, f := range fs {
		alias := fmt.Sprintf("r%d", i)
		terms = append(terms, fmt.Sprintf("EXISTS (SELECT 1 FROM %s %s WHERE %s.%s = t.%s)",
			qualifiedIdent(f.Schema, f.Table), alias, alias, quoteIdent(f.Column), quoteIdent(f.RefColumn)))
	}
	return fmt.Sprintf("SELECT %s FROM %s t WHERE t.%s = ?",
		strings.Join(terms, " OR "), qualifiedIdent(r.Schema.Name, r.Table.Name), quoteIdent(keyColumn(r)))
}

// keyColumn is the column the GORM adapter finds r's rows by: the name for a
// nested resource (see nameKeyed), the surrogate key otherwise.
func keyColumn(r *resource) string {
	if r.nameKeyed() {
		return r.Cols.NameKey.Name
	}
	return r.Cols.PK.Name
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func qualifiedIdent(schemaName, name string) string {
	return quoteIdent(schemaName) + "." + quoteIdent(name)
}

// referenceService is one store.yaml reference_services: entry, as the
// backend stamps it onto db.Opts (see backend.ReferenceService).
type referenceService struct {
	Type      string   `json:"type"`
	Get       string   `json:"get"`
	Target    string   `json:"target"`
	Referrers []string `json:"referrers"`
	Database  string   `json:"database"`
}

// referenceServices reads the reference_services: entries off db.
func referenceServices(db *schema.Database) ([]referenceService, error) {
	raw := db.Opt("reference_services")
	if raw == "" {
		return nil, nil
	}
	var out []referenceService
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("repository: reference_services: %w", err)
	}
	return out, nil
}

// ddnMutations are the DDN mutation root fields of schema s's resources the
// DDN reference plugin (ddn/ddnrefs) checks: inserts and updates that may set
// an external reference, and every delete of a typed resource (the plugin
// asks referrers only about types a reference_services: entry lists).
//
// The names are wire names: root fields carry the subgraph's
// graphql_root_field_prefix, as the requests the plugin sees and sends do.
func ddnMutations(s *schema.Schema, rs []*resource, rootPrefix string) []ddnrefs.Mutation {
	var out []ddnrefs.Mutation
	for _, r := range rs {
		model := clientModelName(s, r.Table)
		if len(r.ExtRefs) > 0 {
			var refs []ddnrefs.Reference
			for _, e := range r.ExtRefs {
				refs = append(refs, ddnrefs.Reference{
					Field: e.Field, Column: camel(e.Column), Type: e.Type,
					OnDelete: e.Policy, Repeated: e.Repeated,
				})
			}
			out = append(out,
				ddnrefs.Mutation{Field: rootPrefix + "insert" + model, Kind: "insert", ResourceType: r.Type, References: refs},
				ddnrefs.Mutation{Field: rootPrefix + "update" + model + "ById", Kind: "update", ResourceType: r.Type, References: refs})
		}
		if r.Type != "" {
			out = append(out, ddnrefs.Mutation{
				Field: rootPrefix + "delete" + model + "ById", Kind: "delete", ResourceType: r.Type,
				Pattern: r.Pattern, KeyArgument: "keyId", ByIDQuery: rootPrefix + naming.CamelFirst(model) + "ById",
			})
		}
	}
	return out
}

// ddnEndpoints converts the reference_services: entries for the plugin.
func ddnEndpoints(eps []referenceService) []ddnrefs.Endpoint {
	var out []ddnrefs.Endpoint
	for _, e := range eps {
		out = append(out, ddnrefs.Endpoint{Type: e.Type, Get: e.Get, Target: e.Target, Referrers: e.Referrers})
	}
	return out
}

// ddnHookMetadata is the DDN metadata registering the plugin's two hooks.
const ddnHookMetadata = `# Generated by protoc-gen-store. DO NOT EDIT.
#
# Registers the store reference plugin (ddn/cmd/store-ddn-references, run with
# -config pointing at references.json beside this file) with the DDN engine,
# so mutations sent straight to GraphQL keep the store.v1 reference contract.
# Copy into your supergraph's globals subgraph metadata (or include it), set
# STORE_REFERENCES_PLUGIN_URL and STORE_REFERENCES_PLUGIN_SECRET, and build.
kind: LifecyclePluginHook
version: v1
definition:
  name: store-references
  url:
    valueFromEnv: STORE_REFERENCES_PLUGIN_URL
  pre: parse
  config:
    request:
      headers:
        additional:
          hasura-m-auth:
            valueFromEnv: STORE_REFERENCES_PLUGIN_SECRET
      session: {}
      rawRequest:
        query: {}
        variables: {}
---
kind: LifecyclePluginHook
version: v1
definition:
  name: store-references-notify
  url:
    valueFromEnv: STORE_REFERENCES_PLUGIN_RESPONSE_URL
  pre: response
  config:
    mode:
      type: asynchronous
    request:
      headers:
        additional:
          hasura-m-auth:
            valueFromEnv: STORE_REFERENCES_PLUGIN_SECRET
      session: {}
      rawRequest:
        query: {}
        variables: {}
`
