package types

// sqlproject.go projects a canonical PostgreSQL type string (produced by
// SQLForColumn) onto the Go, GORM, and Prisma type systems the database targets
// render. These used to live in protokit/types, but they are orm-specific — the
// generic IR carries only the neutral schema.FieldType — so they live here with
// the rest of orm's type projection.

import "strings"

// BaseType splits a SQL type into its leading keyword and whether it is an
// array, discarding any "(length)"/"(precision,scale)" modifier.
// "VARCHAR(255)[]" → ("VARCHAR", true); "DOUBLE PRECISION" → (same, false).
func BaseType(sqlType string) (base string, isArray bool) {
	base = strings.ToUpper(strings.TrimSpace(sqlType))
	if strings.HasSuffix(base, "[]") {
		base, isArray = strings.TrimSpace(strings.TrimSuffix(base, "[]")), true
	}
	if i := strings.IndexByte(base, '('); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	return base, isArray
}

// goScalar maps a bare PostgreSQL keyword (no modifiers, no array suffix) to a
// Go type. Types without a lossless Go primitive (NUMERIC, MONEY, UUID, INET,
// POINT, INTERVAL, ranges, …) map to string to stay driver-agnostic.
var goScalar = map[string]string{
	"BOOLEAN": "bool", "BOOL": "bool",
	"SMALLINT": "int32", "INT2": "int32", "INTEGER": "int32", "INT": "int32", "INT4": "int32", "SERIAL": "int32",
	"BIGINT": "int64", "INT8": "int64", "BIGSERIAL": "int64",
	"REAL": "float32", "FLOAT4": "float32",
	"DOUBLE PRECISION": "float64", "FLOAT8": "float64",
	"BYTEA": "[]byte",
	"JSON":  "json.RawMessage", "JSONB": "json.RawMessage",
	"TIMESTAMPTZ": "time.Time", "TIMESTAMP": "time.Time",
	"DATE": "time.Time", "TIME": "time.Time", "TIMETZ": "time.Time",
}

// GoType projects a canonical PostgreSQL type onto Go.
// Arrays become slices; unknown keywords default to string.
func GoType(pgType string) string {
	base, isArray := BaseType(pgType)
	t, ok := goScalar[base]
	if !ok {
		t = "string"
	}
	if isArray {
		return "[]" + t
	}
	return t
}

// pqArrayType maps an array's element keyword to the github.com/lib/pq array
// type GORM needs: a bare Go slice (`[]string`) fails AutoMigrate with
// "unsupported data type", so a repeated scalar is rendered as the pq.*Array
// type that implements sql.Scanner/driver.Valuer. Element kinds without a
// dedicated pq type fall back to pq.StringArray (see GormGoType).
var pqArrayType = map[string]string{
	"BOOLEAN": "pq.BoolArray", "BOOL": "pq.BoolArray",
	"SMALLINT": "pq.Int32Array", "INT2": "pq.Int32Array", "INTEGER": "pq.Int32Array", "INT": "pq.Int32Array", "INT4": "pq.Int32Array", "SERIAL": "pq.Int32Array",
	"BIGINT": "pq.Int64Array", "INT8": "pq.Int64Array", "BIGSERIAL": "pq.Int64Array",
	"REAL": "pq.Float32Array", "FLOAT4": "pq.Float32Array",
	"DOUBLE PRECISION": "pq.Float64Array", "FLOAT8": "pq.Float64Array",
	"BYTEA": "pq.ByteaArray",
}

// GormGoType is GoType specialized for the GORM target: a repeated scalar maps
// to its github.com/lib/pq array type instead of a bare Go slice, because GORM's
// AutoMigrate cannot map a bare `[]string`/`[]int32`. Non-array types are
// identical to GoType. Callers detect the resulting "pq." prefix to add the
// lib/pq import.
func GormGoType(pgType string) string {
	if base, isArray := BaseType(pgType); isArray {
		if t, ok := pqArrayType[base]; ok {
			return t
		}
		return "pq.StringArray" // text[] and any unmapped element kind
	}
	return GoType(pgType)
}

// gormDefaultType is the column type GORM's PostgreSQL driver creates for a Go
// type when the struct tag pins none. Anything the canonical schema declares
// that is not exactly this has to be pinned explicitly, or AutoMigrate and the
// emitted DDL describe different columns.
//
// This is the inverse of goScalar, and deliberately expressed as "what does GORM
// do" rather than "which keywords need an override": an override list only knows
// about the cases someone thought to add, and every keyword it missed —
// VARCHAR(n), CHAR(n), REAL, DOUBLE PRECISION, INTERVAL, and every other type
// that falls back to a Go string — silently migrated as text or numeric.
var gormDefaultType = map[string]string{
	"bool":            "BOOLEAN",
	"int32":           "INTEGER",
	"int64":           "BIGINT",
	"float32":         "NUMERIC", // GORM maps both float kinds to decimal
	"float64":         "NUMERIC",
	"[]byte":          "BYTEA",
	"json.RawMessage": "BYTEA", // a []byte alias, so jsonb must be pinned
	// time.Time is deliberately absent: it never matches, so every temporal
	// column is pinned. GORM's default for a time.Time is timestamptz, which
	// would silently flatten a TIMESTAMP, DATE, TIME, or TIMETZ into it.
	"string": "TEXT",
}

// gormArrayColumnType maps an array element keyword to the GORM `type:` array
// value. String-ish elements default to text[] so GORM matches Prisma's
// String[] → text[] mapping.
var gormArrayColumnType = map[string]string{
	"BOOLEAN": "boolean[]", "BOOL": "boolean[]",
	"SMALLINT": "smallint[]", "INT2": "smallint[]", "INTEGER": "integer[]", "INT": "integer[]", "INT4": "integer[]", "SERIAL": "integer[]",
	"BIGINT": "bigint[]", "INT8": "bigint[]", "BIGSERIAL": "bigint[]",
	"REAL": "real[]", "FLOAT4": "real[]",
	"DOUBLE PRECISION": "double precision[]", "FLOAT8": "double precision[]",
	"BYTEA": "bytea[]",
}

// character are the PostgreSQL string keywords carrying a length modifier, which
// BaseType strips — the length is exactly what needs pinning.
var character = map[string]bool{
	"VARCHAR": true, "CHARACTER VARYING": true,
	"CHAR": true, "CHARACTER": true, "BPCHAR": true,
}

// GormColumnType returns the explicit value for a GORM `type:` struct-tag
// fragment, or "" when GORM's Go-type default already matches the canonical
// column type. It keeps the three backends agreeing — on timestamptz, jsonb,
// native arrays, sized character types, and the float and interval kinds — so
// AutoMigrate doesn't fight a Prisma-created column.
func GormColumnType(pgType string) string {
	base, isArray := BaseType(pgType)
	if isArray {
		if t, ok := gormArrayColumnType[base]; ok {
			return t
		}
		// A sized character element keeps its modifier, which BaseType strips,
		// so a VARCHAR(255)[] column does not migrate as text[]. Other unmapped
		// element kinds keep the deliberate text[] fallback that matches
		// Prisma's String[] mapping.
		if character[base] {
			return strings.ToLower(strings.TrimSpace(pgType))
		}
		return "text[]"
	}
	// Pin whenever the canonical type is not exactly what GORM would produce for
	// the Go type this column maps to. Comparing against GORM's own behavior
	// catches every keyword, including the ones with no goScalar entry (VARCHAR,
	// CHAR, INTERVAL, UUID, NUMERIC, …) that fall back to a Go string.
	canonical := strings.ToUpper(strings.TrimSpace(pgType))
	if gormDefaultType[GoType(pgType)] == canonical {
		return ""
	}
	return strings.ToLower(canonical)
}

// prismaScalar maps a bare PostgreSQL keyword to a Prisma scalar type.
// Types Prisma cannot model natively map to String.
var prismaScalar = map[string]string{
	"BOOLEAN": "Boolean", "BOOL": "Boolean",
	"SMALLINT": "Int", "INT2": "Int", "INTEGER": "Int", "INT": "Int", "INT4": "Int", "SERIAL": "Int",
	"BIGINT": "BigInt", "INT8": "BigInt", "BIGSERIAL": "BigInt",
	"REAL": "Float", "FLOAT4": "Float", "DOUBLE PRECISION": "Float", "FLOAT8": "Float",
	"NUMERIC": "Decimal", "DECIMAL": "Decimal", "MONEY": "Decimal",
	"BYTEA": "Bytes",
	"JSON":  "Json", "JSONB": "Json",
	"TIMESTAMPTZ": "DateTime", "TIMESTAMP": "DateTime",
	"DATE": "DateTime", "TIME": "DateTime", "TIMETZ": "DateTime",
}

// PrismaType projects a canonical PostgreSQL type onto a Prisma scalar.
// Arrays become Prisma lists; unknown keywords default to String.
func PrismaType(pgType string) string {
	base, isArray := BaseType(pgType)
	t, ok := prismaScalar[base]
	if !ok {
		t = "String"
	}
	if isArray {
		return t + "[]"
	}
	return t
}

// prismaNativeType maps a date/time keyword to the bare Prisma native-type
// (without the datasource prefix). Prisma's DateTime defaults to `timestamp(3)`
// (no time zone), so a TIMESTAMPTZ field silently loses its zone — a UTC write
// reads back as a local-wall-clock value. Pinning Timestamptz(6) keeps Prisma
// agreeing with the GORM/SQL columns. Only the date/time family needs an
// override: Json already maps to jsonb and String[] to text[] by default.
var prismaNativeType = map[string]string{
	"TIMESTAMPTZ": "Timestamptz(6)", "TIMESTAMP": "Timestamp(6)",
	"DATE": "Date", "TIME": "Time(6)", "TIMETZ": "Timetz(6)",
}

// PrismaNativeType returns the bare Prisma native-type name (e.g.
// "Timestamptz(6)") for a canonical PostgreSQL type, or "" when Prisma's default
// mapping already matches. The caller prefixes it with the datasource block name
// to form the attribute (@<datasource>.Timestamptz(6)). Postgres-only; Mongo has
// no native-type attributes.
func PrismaNativeType(pgType string) string {
	base, _ := BaseType(pgType)
	return prismaNativeType[base]
}

// mongoScalar maps a bare canonical-PostgreSQL keyword to the Prisma scalar
// used under the mongodb provider. Mongo has no VARCHAR/NUMERIC distinctions:
// strings collapse to String, arbitrary precision to Float, ranges/geo to Json.
var mongoScalar = map[string]string{
	"BOOLEAN": "Boolean", "BOOL": "Boolean",
	"SMALLINT": "Int", "INT2": "Int", "INTEGER": "Int", "INT": "Int", "INT4": "Int", "SERIAL": "Int",
	"BIGINT": "BigInt", "INT8": "BigInt", "BIGSERIAL": "BigInt",
	"REAL": "Float", "FLOAT4": "Float", "DOUBLE PRECISION": "Float", "FLOAT8": "Float",
	"NUMERIC": "Float", "DECIMAL": "Float", "MONEY": "Float",
	"BYTEA": "Bytes",
	"JSON":  "Json", "JSONB": "Json",
	"TIMESTAMPTZ": "DateTime", "TIMESTAMP": "DateTime",
	"DATE": "DateTime", "TIME": "DateTime", "TIMETZ": "DateTime",
	"POINT": "Json", "TSTZRANGE": "Json", "INTERVAL": "String",
}

// MongoPrismaType projects a canonical PostgreSQL type onto a Prisma scalar
// for the mongodb provider. Arrays become Prisma lists; unknown → String.
func MongoPrismaType(pgType string) string {
	base, isArray := BaseType(pgType)
	t, ok := mongoScalar[base]
	if !ok {
		t = "String"
	}
	if isArray {
		return t + "[]"
	}
	return t
}

// PrismaTypeFor projects a canonical PostgreSQL type for the given provider.
func PrismaTypeFor(p Provider, pgType string) string {
	if p == MongoDB {
		return MongoPrismaType(pgType)
	}
	return PrismaType(pgType)
}
