package gorm

// filterspec.go plans the per-table AIP-160 filter / AIP-132 order_by specs the
// filters.go.tpl emits. Specs are pure data (field → column/kind/enum-prefix
// maps plus the free-text search and sort allowlists); the behavior lives in the
// once-per-tree filterx engine package, so the same spec drives both the gorm
// and the hasura engine. Field selection is type-driven with per-field
// store.v1.query overrides (filterable / sortable / search), read from the
// column's facet.

import (
	"fmt"
	"sort"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// filterFieldView is one filterable field in a table spec.
type filterFieldView struct {
	Field      string // API filter field name (the proto field name)
	Column     string // physical snake_case column
	Kind       string // filterx kind const name, e.g. "KindText"
	EnumPrefix string // proto enum value prefix ("UNIT_TYPE_"), enum kinds only
}

// filterSortView is one sortable field in a table spec. Kind travels with the
// column because the keyset cursor round-trips each sort value through the page
// token, and a column can be sortable without being filterable — so its kind is
// not always recoverable from the spec's Fields.
type filterSortView struct {
	Field   string
	Column  string
	Kind    string // filterx kind const name, e.g. "KindText"
	NotNull bool   // drops the NULL half of this column's keyset comparison
}

// filterPKView is one primary-key column, appended as the order_by tiebreaker
// and therefore also a cursor column.
type filterPKView struct {
	Column  string
	Kind    string
	NotNull bool
}

// filterTableView is the spec data for one table.
type filterTableView struct {
	SpecVar string // "UnitFilterSpec"
	Model   string // bare model name the spec belongs to
	Table   string // quoted, schema-qualified physical table: "property"."units"
	Fields  []filterFieldView
	Search  []string // columns matched by a bareword free-text term
	Sort    []filterSortView
	PK      []filterPKView // primary-key columns, appended as the order_by tiebreaker
}

// buildFilterTable plans one table's spec. Returns ok=false when nothing on the
// table is filterable or sortable (the spec would be empty).
func buildFilterTable(s *schema.Schema, t *schema.Table, fx facets.Set) (filterTableView, bool) {
	v := filterTableView{
		SpecVar: t.LocalName + "FilterSpec",
		Model:   t.LocalName,
		Table:   fmt.Sprintf("%q.%q", s.Name, t.Name),
	}
	// Collected before the filter pass, which skips synthesized columns: a
	// surrogate primary key is synthesized, and it is exactly the column the
	// order_by tiebreaker needs.
	for _, c := range t.Columns {
		if !c.PrimaryKey {
			continue
		}
		// A key type classifyFilterColumn excludes from filtering is still a
		// perfectly good tiebreaker; it just travels through the cursor as text.
		kind, _, ok := classifyFilterColumn(c)
		if !ok {
			kind = "KindText"
		}
		// A primary key is never NULL whether or not the column carries the flag.
		v.PK = append(v.PK, filterPKView{Column: c.Name, Kind: kind, NotNull: true})
	}
	for _, c := range t.Columns {
		if c.Source == nil {
			continue // synthesized (surrogate keys, parent FKs): scoped by the caller, not filters
		}
		kind, sortable, ok := classifyFilterColumn(c)
		if !ok {
			continue
		}
		opts := fx.Query(c)
		field := string(c.Source.Name())

		filterable := true
		if opts.Filterable != nil {
			filterable = *opts.Filterable
		}
		if filterable {
			ff := filterFieldView{Field: field, Column: c.Name, Kind: kind}
			if kind == "KindEnum" && c.Enum != nil {
				ff.EnumPrefix = naming.ScreamingSnake(c.Enum.LocalName) + "_"
			}
			v.Fields = append(v.Fields, ff)
			if opts.GetSearch() && kind == "KindText" {
				v.Search = append(v.Search, c.Name)
			}
		}

		if sortable {
			on := true
			if opts.Sortable != nil {
				on = *opts.Sortable
			}
			if on {
				v.Sort = append(v.Sort, filterSortView{Field: field, Column: c.Name, Kind: kind, NotNull: c.NotNull})
			}
		}
	}
	sort.Slice(v.Fields, func(i, j int) bool { return v.Fields[i].Field < v.Fields[j].Field })
	sort.Slice(v.Sort, func(i, j int) bool { return v.Sort[i].Field < v.Sort[j].Field })
	sort.Strings(v.Search)
	return v, len(v.Fields) > 0 || len(v.Sort) > 0
}

// classifyFilterColumn maps a column's neutral type onto a filterx kind and the
// type-default sortability. ok=false excludes the column entirely (blobs, JSON,
// intervals, and other shapes with no meaningful filter semantics).
func classifyFilterColumn(c *schema.Column) (kind string, sortable, ok bool) {
	// An explicit resource reference is an identity match on the stored id — it
	// scopes rows to a parent/peer, so only equality applies and sorting by it
	// is meaningless.
	if c.FKModel != "" {
		return "KindRef", false, true
	}
	if c.List {
		if c.Type == schema.TypeString {
			return "KindTags", false, true
		}
		return "", false, false
	}
	switch c.Type {
	case schema.TypeString:
		return "KindText", true, true
	case schema.TypeEnum:
		return "KindEnum", true, true
	case schema.TypeDate:
		return "KindDate", true, true
	case schema.TypeTimestamp:
		return "KindTimestamp", true, true
	case schema.TypeInt32, schema.TypeUint32, schema.TypeInt64, schema.TypeUint64:
		return "KindInt", true, true
	case schema.TypeFloat, schema.TypeDouble:
		return "KindFloat", true, true
	case schema.TypeBool:
		return "KindBool", true, true
	default:
		return "", false, false
	}
}
