// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package types

import "testing"

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
		"VARCHAR(255)[]":  "text[]", // matches Prisma's String[] → text[]
		"TEXT[]":          "text[]",
		"INTEGER[]":       "integer[]",
		"BIGINT[]":        "bigint[]",
		"NUMERIC(20,0)[]": "text[]", // unmapped element → text[] fallback
		"VARCHAR(255)":    "",       // scalar string: GORM default is fine
		"INTEGER":         "",       // scalar int: GORM default is fine
		"CHAR(26)":        "",       // FK/ULID column: no override
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
