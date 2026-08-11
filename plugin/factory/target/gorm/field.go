package gorm

// field.go holds the column-level rendering helpers used by packageView in
// view.go: Go type selection, the gorm/json/validate struct tag, the FK
// constraint fragment, and name deduplication for association fields.

import (
	"strconv"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// gormFieldName is the Go struct field name for a column. Foreign-key columns
// gain an "ID" suffix (Resource → ResourceID) so the bare name is free for the
// association field; the DB column stays col.Name via the gorm:"column:..." tag.
func gormFieldName(col *schema.Column) string {
	return naming.PascalGo(naming.FKFieldBase(col.Name, col.FKModel != ""))
}

// goType returns the Go type for a column: enums use their generated Go type,
// nullable scalars become pointers. Slice-backed types ([]byte, json.RawMessage,
// and the pq.*Array types repeated scalars map to) are not re-wrapped — their nil
// zero value already encodes SQL NULL.
func goType(col *schema.Column, typeOf types.TypeOf) string {
	var base string
	if col.Enum != nil {
		base = col.Enum.LocalName // package-namespaced: bare enum type name
	} else {
		base = types.GormGoType(typeOf(col))
	}
	if col.Optional && !strings.HasPrefix(base, "[]") && base != "json.RawMessage" && !strings.HasPrefix(base, "pq.") {
		return "*" + base
	}
	return base
}

// structTag builds the combined gorm + json + validate + telemetry struct tag
// for a column. extra are additional gorm fragments computed table-side: the
// index fragments this column participates in (composite and synthesized FK
// indexes) and, for an enum column, a CHECK constraint — so GORM AutoMigrate
// reproduces the same indexes and enum value integrity the SQL target emits.
// telemetryTag, when non-empty, is the pre-rendered opentelementry tag the SDK
// reflects over (span attributes and value metrics).
func structTag(col *schema.Column, extra []string, telemetryTag string, typeOf types.TypeOf) string {
	gormParts := []string{"column:" + col.Name}
	// Pin the DB type when GORM's Go-type default disagrees with the canonical
	// column type (timestamptz, jsonb, native arrays) so AutoMigrate produces the
	// same column the Prisma/SQL targets do.
	if ct := types.GormColumnType(typeOf(col)); ct != "" {
		gormParts = append(gormParts, "type:"+ct)
	}
	if col.PrimaryKey {
		gormParts = append(gormParts, "primaryKey")
	}
	if col.NotNull {
		gormParts = append(gormParts, "not null")
	}
	if col.Unique {
		gormParts = append(gormParts, "uniqueIndex")
	}
	switch {
	case col.AutoCreate:
		gormParts = append(gormParts, "autoCreateTime")
	case col.AutoUpdate:
		gormParts = append(gormParts, "autoUpdateTime")
	case col.Generated == "uuid":
		gormParts = append(gormParts, "default:gen_random_uuid()")
	case col.Enum != nil && col.Default != "":
		gormParts = append(gormParts, "default:'"+col.Default+"'") // enum label literal
	case col.Default != "":
		gormParts = append(gormParts, "default:"+col.Default)
	}
	if col.Index {
		gormParts = append(gormParts, "index")
	}
	gormParts = append(gormParts, extra...)
	tag := `gorm:"` + strings.Join(gormParts, ";") + `"`

	if col.Optional {
		tag += ` json:"` + col.Name + `,omitempty"`
	} else {
		tag += ` json:"` + col.Name + `"`
	}

	// Required validation for non-PK NOT NULL fields the application supplies —
	// DB-managed columns (generated ids, timestamps) are excluded.
	if col.NotNull && !col.PrimaryKey && !col.AutoCreate && !col.AutoUpdate && col.Generated == "" {
		tag += ` validate:"required"`
	}
	if telemetryTag != "" {
		tag += " " + telemetryTag
	}
	return tag
}

// indexTagsByColumn maps each column to the GORM index struct-tag fragments for
// the table-level indexes it participates in (composite indexes from
// orm.v1.table.indexes and the synthesized single-column FK indexes). A
// multi-column index carries a per-column priority so GORM preserves column
// order; a single-column index needs none. The index name matches the SQL
// target's (assigned in the build's nameIndexes pass), so both backends create
// the same physical index.
func indexTagsByColumn(t *schema.Table) map[string][]string {
	out := map[string][]string{}
	for _, idx := range t.Indexes {
		kind := "index"
		if idx.Unique {
			kind = "uniqueIndex"
		}
		for pos, col := range idx.Columns {
			frag := kind + ":" + idx.Name
			if len(idx.Columns) > 1 {
				frag += ",priority:" + strconv.Itoa(pos+1)
			}
			out[col] = append(out[col], frag)
		}
	}
	return out
}

// enumCheck renders a GORM CHECK-constraint tag fragment restricting an enum
// column to its declared values, so AutoMigrate enforces the same value
// integrity the SQL/Prisma targets get from a native enum type — GORM models an
// enum as a plain string column, which otherwise accepts any text. The explicit
// constraint name (before the first comma) keeps GORM from mis-parsing the
// commas inside the IN list as the name/expression separator. Returns "" for an
// enum with no values (e.g. one declaring only the dropped *_UNSPECIFIED
// sentinel), which would otherwise produce an invalid `IN ()`.
func enumCheck(tableName string, col *schema.Column) string {
	if len(col.Enum.Values) == 0 {
		return ""
	}
	vals := make([]string, len(col.Enum.Values))
	for i, v := range col.Enum.Values {
		vals[i] = "'" + v.MapName + "'"
	}
	return "check:chk_" + tableName + "_" + col.Name + "," + col.Name + " IN (" + strings.Join(vals, ",") + ")"
}

// constraintTag renders the GORM constraint fragment for the FK on column
// colName, e.g. ";constraint:OnDelete:CASCADE,OnUpdate:SET NULL". Empty when
// the FK declares no referential actions.
func constraintTag(t *schema.Table, colName string) string {
	for _, fk := range t.ForeignKeys {
		if fk.Column != colName {
			continue
		}
		var parts []string
		if fk.OnDelete != "" {
			parts = append(parts, "OnDelete:"+fk.OnDelete)
		}
		if fk.OnUpdate != "" {
			parts = append(parts, "OnUpdate:"+fk.OnUpdate)
		}
		if len(parts) > 0 {
			return ";constraint:" + strings.Join(parts, ",")
		}
	}
	return ""
}

// commentOr returns comment when non-empty, otherwise the fallback.
func commentOr(comment, fallback string) string {
	if comment != "" {
		return comment
	}
	return fallback
}
