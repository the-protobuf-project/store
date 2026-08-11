// Package types is orm's projection of the neutral schema.FieldType onto the
// canonical PostgreSQL type the gorm/sql/prisma targets render from. It is the
// db-specific half of the type system that used to live in protokit; protokit now
// carries only the neutral FieldType, and orm's own type override
// (orm.v1.column.type/max_length/precision) arrives from the column's orm.v1
// facet rather than being stored back on the shared IR.
package types

import (
	"fmt"

	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit/schema"
)

// sqlForType maps a neutral FieldType to its canonical PostgreSQL type, matching
// what protokit's PostgresType produced before the split. Unsigned 32/64 widen a
// step so the full range fits. TypeEnum has no SQL type (enum columns carry
// schema.Column.Enum instead).
var sqlForType = map[schema.FieldType]string{
	schema.TypeString:    "VARCHAR(255)",
	schema.TypeBool:      "BOOLEAN",
	schema.TypeInt32:     "INTEGER",
	schema.TypeUint32:    "BIGINT",
	schema.TypeInt64:     "BIGINT",
	schema.TypeUint64:    "NUMERIC(20,0)",
	schema.TypeFloat:     "REAL",
	schema.TypeDouble:    "DOUBLE PRECISION",
	schema.TypeBytes:     "BYTEA",
	schema.TypeTimestamp: "TIMESTAMPTZ",
	schema.TypeDuration:  "INTERVAL",
	schema.TypeDate:      "DATE",
	schema.TypeTimeOfDay: "TIME",
	schema.TypeDecimal:   "NUMERIC",
	schema.TypeLatLng:    "POINT",
	schema.TypeInterval:  "TSTZRANGE",
	schema.TypeText:      "TEXT",
	schema.TypeJSON:      "JSONB",
	schema.TypeULID:      "CHAR(26)",
	schema.TypeUUID:      "UUID",
}

// SQL returns the canonical PostgreSQL type for a neutral FieldType, appending the
// array suffix for a repeated field. JSONB stays a single document (one JSON value
// already represents the whole collection).
func SQL(t schema.FieldType, list bool) string {
	base := sqlForType[t]
	if list && base != "" && t != schema.TypeJSON {
		return base + "[]"
	}
	return base
}

// TypeOf resolves a column's effective PostgreSQL type. Targets build one from
// their facet set and thread it into the view builders, which is why the deep
// renderers never need the IR itself.
type TypeOf func(*schema.Column) string

// SQLForColumn returns the effective PostgreSQL type of a column: an explicit
// orm.v1.column type/max_length/precision override wins; otherwise the neutral
// FieldType — proto-classified, or set by protokit's synthesis and foreign-key
// alignment — projects to a PostgreSQL type.
//
// o comes from the column's orm.v1 facet rather than its Source descriptor, so a
// synthesized column (a surrogate key, an embedded child's foreign key) resolves
// correctly instead of silently falling back to the neutral type. Callers pass
// the never-nil value facets.Set.Column returns.
func SQLForColumn(col *schema.Column, o *ormpbv1.ColumnOptions) string {
	if t := overrideType(o); t != "" {
		return t
	}
	return SQL(col.Type, col.List)
}

// overrideType returns the SQL type an orm.v1.column type/max_length/precision
// override pins on a column, or "" when it carries no such override.
func overrideType(o *ormpbv1.ColumnOptions) string {
	switch {
	case o.GetType() != "":
		return o.GetType()
	case o.GetMaxLength() > 0:
		return fmt.Sprintf("VARCHAR(%d)", o.GetMaxLength())
	case o.GetPrecision() > 0:
		return fmt.Sprintf("NUMERIC(%d,%d)", o.GetPrecision(), o.GetScale())
	}
	return ""
}
