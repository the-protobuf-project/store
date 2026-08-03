package types

import (
	"strings"
	"testing"
)

func TestBaseType(t *testing.T) {
	cases := []struct {
		in    string
		base  string
		array bool
	}{
		{"VARCHAR(255)", "VARCHAR", false},
		{"VARCHAR(255)[]", "VARCHAR", true},
		{"DOUBLE PRECISION", "DOUBLE PRECISION", false},
		{"NUMERIC(20,0)", "NUMERIC", false},
		{"text", "TEXT", false},
		{" INTEGER[] ", "INTEGER", true},
	}
	for _, c := range cases {
		base, array := BaseType(c.in)
		if base != c.base || array != c.array {
			t.Errorf("BaseType(%q) = (%q, %v), want (%q, %v)", c.in, base, array, c.base, c.array)
		}
	}
}

func TestGoType(t *testing.T) {
	cases := map[string]string{
		"VARCHAR(255)":     "string",
		"VARCHAR(255)[]":   "[]string",
		"INTEGER":          "int32",
		"BIGINT":           "int64",
		"NUMERIC(20,0)":    "string", // precision-safe: no lossless Go primitive
		"DOUBLE PRECISION": "float64",
		"BOOLEAN":          "bool",
		"BYTEA":            "[]byte",
		"JSONB":            "json.RawMessage",
		"TIMESTAMPTZ":      "time.Time",
		"DATE":             "time.Time",
		"TSTZRANGE":        "string",
		"INTERVAL":         "string", // no lossless Go primitive; stays driver-agnostic
	}
	for in, want := range cases {
		if got := GoType(in); got != want {
			t.Errorf("GoType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGormGoType(t *testing.T) {
	cases := map[string]string{
		"VARCHAR(255)":       "string",         // scalar: same as GoType
		"VARCHAR(255)[]":     "pq.StringArray", // repeated scalar → pq array, not a bare slice
		"TEXT[]":             "pq.StringArray",
		"INTEGER[]":          "pq.Int32Array",
		"BIGINT[]":           "pq.Int64Array",
		"DOUBLE PRECISION[]": "pq.Float64Array",
		"REAL[]":             "pq.Float32Array",
		"BOOLEAN[]":          "pq.BoolArray",
		"NUMERIC(20,0)[]":    "pq.StringArray", // unmapped element → StringArray fallback
		"TIMESTAMPTZ":        "time.Time",
		"JSONB":              "json.RawMessage",
	}
	for in, want := range cases {
		if got := GormGoType(in); got != want {
			t.Errorf("GormGoType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGormColumnType(t *testing.T) {
	cases := map[string]string{
		"TIMESTAMPTZ":     "timestamptz", // GORM's time.Time default loses the kind
		"TIMESTAMP":       "timestamp",
		"DATE":            "date",
		"JSONB":           "jsonb", // GORM's []byte default would be bytea
		"JSON":            "json",
		"TEXT[]":          "text[]",
		"INTEGER[]":       "integer[]",
		"BIGINT[]":        "bigint[]",
		"NUMERIC(20,0)[]": "text[]", // unmapped element → text[] fallback
		"INTEGER":         "",       // scalar int: GORM default is fine
		"TEXT":            "",       // unsized string: GORM's default IS text

		// Sized character types keep their modifier. GORM's Postgres driver maps
		// a Go string to text, so leaving these unpinned migrated every VARCHAR
		// column — and every CHAR(26) ULID surrogate key — as text, while the sql
		// and prisma targets emitted the sized type. Verified against a live
		// Postgres: before this, AutoMigrate and migrate.sql produced different
		// columns for every string in the schema.
		"VARCHAR(255)":   "varchar(255)",
		"VARCHAR(64)":    "varchar(64)",
		"CHAR(26)":       "char(26)",
		"VARCHAR(255)[]": "varchar(255)[]",
	}
	for in, want := range cases {
		if got := GormColumnType(in); got != want {
			t.Errorf("GormColumnType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrismaNativeType(t *testing.T) {
	cases := map[string]string{
		"TIMESTAMPTZ":   "Timestamptz(6)", // bare; caller adds the @<datasource> prefix
		"TIMESTAMP":     "Timestamp(6)",
		"DATE":          "Date",
		"TIME":          "Time(6)",
		"TIMESTAMPTZ[]": "Timestamptz(6)", // element keyword drives it
		"VARCHAR(255)":  "",               // String needs no native type
		"JSONB":         "",               // Json already maps to jsonb
		"INTEGER":       "",
	}
	for in, want := range cases {
		if got := PrismaNativeType(in); got != want {
			t.Errorf("PrismaNativeType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrismaType(t *testing.T) {
	cases := map[string]string{
		"VARCHAR(255)":     "String",
		"TEXT[]":           "String[]",
		"INTEGER":          "Int",
		"BIGINT":           "BigInt",
		"NUMERIC(20,0)":    "Decimal",
		"DOUBLE PRECISION": "Float",
		"JSONB":            "Json",
		"TIMESTAMPTZ":      "DateTime",
		"POINT":            "String", // no native Prisma scalar
		"INTERVAL":         "String", // Prisma has no Interval scalar; DB type stays INTERVAL
	}
	for in, want := range cases {
		if got := PrismaType(in); got != want {
			t.Errorf("PrismaType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMongoPrismaType(t *testing.T) {
	cases := map[string]string{
		"VARCHAR(255)":  "String",
		"NUMERIC(20,0)": "Float", // Mongo has no Decimal: collapses to Float
		"POINT":         "Json",
		"TSTZRANGE":     "Json",
		"TIMESTAMPTZ":   "DateTime",
	}
	for in, want := range cases {
		if got := MongoPrismaType(in); got != want {
			t.Errorf("MongoPrismaType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGormColumnTypeCoversEveryCanonicalType is the guard against the failure
// this function exists to prevent, and which an override list could not: a
// canonical type that GORM would migrate as something else, left unpinned.
//
// The original implementation listed the keywords needing an override, so every
// keyword nobody thought of — VARCHAR(n), CHAR(n), REAL, DOUBLE PRECISION,
// INTERVAL — silently became text or numeric under AutoMigrate while the sql and
// prisma targets emitted the declared type. Verified against a live Postgres:
// before the fix, every string column in every schema diverged, including the
// CHAR(26) ULID surrogate key on every table.
func TestGormColumnTypeCoversEveryCanonicalType(t *testing.T) {
	// Every type the sql target can emit, paired with what GORM's driver would
	// create for the Go type it maps to when nothing is pinned.
	cases := []struct{ canonical, gormDefault string }{
		{"VARCHAR(255)", "text"},
		{"CHAR(26)", "text"},
		{"TEXT", "text"},
		{"INTEGER", "integer"},
		{"BIGINT", "bigint"},
		{"BOOLEAN", "boolean"},
		{"REAL", "numeric"},
		{"DOUBLE PRECISION", "numeric"},
		{"NUMERIC(20,0)", "text"},
		{"INTERVAL", "text"},
		{"UUID", "text"},
		{"BYTEA", "bytea"},
		{"JSONB", "bytea"},
		{"TIMESTAMPTZ", "timestamptz"},
		{"DATE", "timestamptz"},
	}
	for _, c := range cases {
		got := GormColumnType(c.canonical)
		want := strings.ToLower(c.canonical)
		// A type GORM already produces needs no tag; anything else must be
		// pinned to exactly the canonical type.
		if want == c.gormDefault && c.canonical != "TIMESTAMPTZ" && c.canonical != "DATE" {
			if got != "" {
				t.Errorf("GormColumnType(%q) = %q, want \"\" (GORM's default already matches)", c.canonical, got)
			}
			continue
		}
		if got != want {
			t.Errorf("GormColumnType(%q) = %q, want %q — GORM would otherwise create %s",
				c.canonical, got, want, c.gormDefault)
		}
	}
}
