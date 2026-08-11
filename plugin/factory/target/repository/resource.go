package repository

// resource.go plans the repository surface for one database: which tables are
// repository resources, their AIP resource patterns (name codecs), parent
// scoping, reference columns, and the mutable-field set that drives both the
// gorm and graphql adapters' mask/update handling. Views and templates are
// presentation; every decision is made here.

import (
	"fmt"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/target/gorm"
)

// resource is one repository-managed table: a proto message carrying
// google.api.resource, materialized as a table with a ULID surrogate key.
type resource struct {
	Table   *schema.Table
	Schema  *schema.Schema
	Pattern string // first resource pattern, e.g. "organisations/{organisation}/members/{member}"

	// Segments decomposes the pattern: collection/var pairs in order.
	Segments []patternSegment

	// Parent is the enclosing resource (nil for root resources). Parent scoping
	// on Create/List uses ParentFK, the synthesized "<parentvar>_id" column.
	Parent   *resource
	ParentFK string

	// AncestorFKs are every ancestor's "<var>_id" columns present on the
	// table, in pattern order — a nested resource often denormalizes its
	// grandparents (e.g. an exception carries property_id AND unit_id), and
	// Create must wire them all from the split parent name.
	AncestorFKs []ancestorFK

	// Cols classifies every column once for all emitters.
	Cols columnPlan

	// VOs are the single-level belongs-to value objects the adapters
	// materialize (see planVOFields); deep graphs stay Tier-2.
	VOs []voField
}

// voField is one generated belongs-to value object: a message field
// relationalized into a child row referenced by a synthesized FK column. The
// adapters create the row (fresh ULID) inside the resource write, preload the
// association on reads, and replace the row wholesale on masked updates.
type voField struct {
	Col        *schema.Column // FK column, e.g. "window_id"
	FieldName  string         // proto field name, e.g. "window"
	PBGoName   string         // protogen accessor base, e.g. "Window"
	AssocField string         // gorm association field, e.g. "Window"
	FKField    string         // gorm FK field, e.g. "WindowID"
	Target     *schema.Table  // the value-object table
	CrossPkg   string         // gorm package of the target when cross-schema; "" same-schema
	Case       *voCase        // oneof discriminator, nil for plain fields
}

// voCase is the synthesized discriminator column of a oneof-member value
// object, resolved to the gorm model's enum spelling.
type voCase struct {
	Column      *schema.Column // e.g. "span_case"
	Field       string         // gorm field, e.g. "SpanCase"
	EnumLocal   string         // model enum type, e.g. "AvailabilityExceptionSpanCase"
	Const       string         // this member's constant, e.g. "AvailabilityExceptionSpanCaseWindow"
	OneofGoName string         // proto oneof Go name, e.g. "Span"
}

// patternSegment is one collection/{var} pair of a resource pattern.
type patternSegment struct {
	Collection string // "organisations"
	Var        string // "organisation"
}

// ancestorFK is one ancestor id column and the parentIDs index that fills it.
type ancestorFK struct {
	Column string // "property_id"
	Index  int    // position in the split parent name
}

// columnPlan buckets a resource's columns by the role each plays in the
// generated CRUD bodies.
type columnPlan struct {
	PK      *schema.Column   // synthesized ULID id
	Name    *schema.Column   // the AIP name column (IDENTIFIER)
	Etag    *schema.Column   // optimistic-concurrency column, when present
	Refs    []*schema.Column // resource-reference columns (FKModel set): name↔bare-id mapped
	Mutable []*schema.Column // caller-settable data columns (the mask universe)
	Autos   []*schema.Column // AutoCreate/AutoUpdate audit columns
	Skipped []*schema.Column // value-object FKs, parent FKs, and other non-generated columns
}

// planResources returns the repository resources of db keyed by table, in
// schema order. Tables without a google.api.resource pattern (value objects,
// join tables, embedded children) are not repository resources, and neither
// are patterns the flat CRUD shape cannot express — AIP-156 singletons
// ("channels/{channel}/syncStatus") and multi-var segments — which stay
// hand-written (Tier-2), like the custom logic that usually accompanies them.
func planResources(pb *pbIndex, db *schema.Database) (map[*schema.Table]*resource, error) {
	byTable := map[*schema.Table]*resource{}
	byLeaf := map[string]*resource{} // leaf collection var -> resource, for parent linking
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			if t.Source == nil || t.ValueObject {
				continue
			}
			pattern := resourcePattern(t.Source)
			if pattern == "" {
				continue
			}
			segs, err := parsePattern(pattern)
			if err != nil {
				continue // unsupported shape: no generated repository
			}
			r := &resource{Table: t, Schema: s, Pattern: pattern, Segments: segs}
			r.Cols = planColumns(t)
			if r.Cols.PK == nil {
				continue // no generated surrogate key: the adapters need one to mint ids
			}
			byTable[t] = r
			byLeaf[segs[len(segs)-1].Var] = r
		}
	}
	// Second pass: link parents by pattern prefix and locate the parent FK.
	for _, r := range byTable {
		if len(r.Segments) < 2 {
			continue
		}
		parentVar := r.Segments[len(r.Segments)-2].Var
		r.Parent = byLeaf[parentVar]
		fk := parentVar + "_id"
		for _, c := range r.Table.Columns {
			if c.Name == fk {
				r.ParentFK = fk
				break
			}
		}
		if r.Parent != nil && r.ParentFK == "" {
			return nil, fmt.Errorf("repository: %s: parent pattern %q but no %q column (unsupported layout)", r.Table.ProtoMessage, r.Pattern, fk)
		}
		for i, seg := range r.Segments[:len(r.Segments)-1] {
			if columnByName(r.Table, seg.Var+"_id") != nil {
				r.AncestorFKs = append(r.AncestorFKs, ancestorFK{Column: seg.Var + "_id", Index: i})
			}
		}
	}
	// Third pass: plan the generated value objects (needs ParentFK resolved so
	// the parent FK is never mistaken for a value-object FK).
	for _, r := range byTable {
		r.VOs = planVOFields(pb, db, r)
	}
	return byTable, nil
}

// planVOFields selects the belongs-to value objects the adapters generate for
// r: single-level only — the target is a keyed value-object table whose own
// columns hold no further references (no nested rows, no resource references),
// so a fresh row is buildable from the message field alone via the generated
// converters. Everything deeper stays with Tier-2 overrides. Field naming
// mirrors the gorm target's association plan, so Preload names can never
// drift from the emitted models.
func planVOFields(pb *pbIndex, db *schema.Database, r *resource) []voField {
	msg := pb.msgs[r.Table.Source.FullName()]
	if msg == nil {
		return nil
	}
	fieldsByName := map[string]*protogen.Field{}
	for _, f := range msg.Fields {
		fieldsByName[string(f.Desc.Name())] = f
	}
	var out []voField
	bts, _ := gorm.AssocPlan(db, r.Schema, r.Table)
	for _, bt := range bts {
		c := bt.Col
		tgt := bt.Target
		if c.Source != nil || c.Name == r.ParentFK || tgt == nil || !tgt.ValueObject || tgt.Source == nil {
			continue // resource reference, parent scoping, or not a value object
		}
		if voDeep(tgt) {
			continue // nested graph: Tier-2
		}
		pkOK := false
		for _, tc := range tgt.Columns {
			if tc.Generated != "" {
				pkOK = true
			}
		}
		if !pkOK {
			continue // no mintable surrogate key
		}
		fieldName := naming.StripIDSuffix(c.Name)
		f := fieldsByName[fieldName]
		if f == nil || f.Message == nil || string(f.Message.Desc.FullName()) != string(tgt.Source.FullName()) {
			continue
		}
		v := voField{
			Col:        c,
			FieldName:  fieldName,
			PBGoName:   f.GoName,
			AssocField: bt.Field,
			FKField:    gormFieldName(c.Name, true),
			Target:     tgt,
			CrossPkg:   bt.CrossPkg,
		}
		if f.Oneof != nil && !f.Oneof.Desc.IsSynthetic() {
			cc := columnByName(r.Table, string(f.Oneof.Desc.Name())+"_case")
			if cc == nil || cc.Enum == nil {
				continue // no discriminator to keep consistent: Tier-2
			}
			v.Case = &voCase{
				Column:      cc,
				Field:       gormFieldName(cc.Name, false),
				EnumLocal:   cc.Enum.LocalName,
				Const:       cc.Enum.LocalName + naming.PascalGo(fieldName),
				OneofGoName: f.Oneof.GoName,
			}
		}
		out = append(out, v)
	}
	return out
}

// voDeep reports whether a value-object table carries references of its own —
// nested value objects or resource references — putting it past the
// single-level contract the adapters generate.
func voDeep(t *schema.Table) bool {
	for _, fk := range t.ForeignKeys {
		if fk.ReferencedProto != "" {
			return true
		}
	}
	for _, c := range t.Columns {
		if c.FKModel != "" {
			return true
		}
	}
	return false
}

// columnByName finds a column by its snake_case name.
func columnByName(t *schema.Table, name string) *schema.Column {
	for _, c := range t.Columns {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// resourcePattern reads the first google.api.resource pattern off the message.
func resourcePattern(d interface{ Options() proto.Message }) string {
	opts := d.Options()
	if opts == nil || !proto.HasExtension(opts, annotations.E_Resource) {
		return ""
	}
	rd, _ := proto.GetExtension(opts, annotations.E_Resource).(*annotations.ResourceDescriptor)
	if rd == nil || len(rd.GetPattern()) == 0 {
		return ""
	}
	return rd.GetPattern()[0]
}

// parsePattern splits "a/{x}/b/{y}" into collection/var segments, rejecting
// shapes the codec cannot round-trip (singleton tails, multi-var segments).
func parsePattern(pattern string) ([]patternSegment, error) {
	parts := strings.Split(pattern, "/")
	if len(parts)%2 != 0 {
		return nil, fmt.Errorf("unsupported resource pattern %q (singleton or malformed)", pattern)
	}
	segs := make([]patternSegment, 0, len(parts)/2)
	for i := 0; i < len(parts); i += 2 {
		v := parts[i+1]
		if !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") {
			return nil, fmt.Errorf("unsupported resource pattern %q (want collection/{var} pairs)", pattern)
		}
		segs = append(segs, patternSegment{Collection: parts[i], Var: v[1 : len(v)-1]})
	}
	return segs, nil
}

// planColumns classifies t's columns for the generated CRUD bodies.
func planColumns(t *schema.Table) columnPlan {
	var p columnPlan
	for _, c := range t.Columns {
		switch {
		case c.Generated != "": // synthesized surrogate key
			p.PK = c
		case c.AutoCreate || c.AutoUpdate:
			p.Autos = append(p.Autos, c)
		case c.Source == nil: // synthesized (parent FK, value-object FK)
			p.Skipped = append(p.Skipped, c)
		case c.Name == "name" && c.PrimaryKey: // the AIP IDENTIFIER
			p.Name = c
		case c.Name == "etag":
			p.Etag = c
		case c.FKModel != "":
			p.Refs = append(p.Refs, c)
			p.Mutable = append(p.Mutable, c)
		case isValueObjectFK(t, c):
			p.Skipped = append(p.Skipped, c)
		default:
			if !outputOnly(c) {
				p.Mutable = append(p.Mutable, c)
			} else {
				p.Skipped = append(p.Skipped, c)
			}
		}
	}
	return p
}

// isValueObjectFK reports whether c is the FK side of a relationalized
// embedded message (value object) — graph wiring the flat adapters leave to
// Tier-2 overrides.
func isValueObjectFK(t *schema.Table, c *schema.Column) bool {
	for _, fk := range t.ForeignKeys {
		if fk.Column == c.Name && fk.ReferencedProto != "" {
			return true
		}
	}
	return false
}

// outputOnly reports whether the column's proto field is OUTPUT_ONLY — server
// derived, so not part of the mutable/mask universe.
func outputOnly(c *schema.Column) bool {
	if c.Source == nil {
		return true
	}
	fb, _ := proto.GetExtension(c.Source.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	for _, b := range fb {
		if b == annotations.FieldBehavior_OUTPUT_ONLY {
			return true
		}
	}
	return false
}
