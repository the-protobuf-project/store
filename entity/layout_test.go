package entity

// layout_test.go covers the naming policy directly, which it did not used to be
// worth doing.
//
// While this logic lived in the store plugin it was exercised only through that
// plugin's golden cases — reasonable, because a wrong answer showed up as a wrong
// schema name in a committed file. That is no longer the whole story: this package
// is the shared implementation every protokit plugin resolves layouts through, so
// a regression here is a regression in a plugin that has no golden case in this
// repository and no way to notice. The cases below are the edges where two
// independent implementations would have diverged.

import "testing"

func ptr(b bool) *bool { return &b }

func TestResolve(t *testing.T) {
	cfg := &LayoutConfig{
		StripVersion: true,
		Datasources: []DatasourceRule{
			// Exact match, explicit schema.
			{Match: "shop.v1", Database: "shop_db", Schema: "shop"},
			// Single-segment wildcard.
			{Match: "fleet.*.v1", Database: "fleet_db", Schema: "{leaf}"},
			// Depth-derived schema, with stripping forced off for this rule.
			{Match: "billing.**", Database: "billing_db", SchemaDepth: 2, StripVersion: ptr(false)},
		},
	}

	for _, tc := range []struct {
		name    string
		pkg     string
		db      string
		schema  string
		strip   bool
		comment string
	}{
		{
			name: "exact match wins", pkg: "shop.v1",
			db: "shop_db", schema: "shop", strip: true,
		},
		{
			name: "single wildcard matches one segment", pkg: "fleet.tracking.v1",
			db: "fleet_db", schema: "tracking", strip: true,
			comment: "{leaf} drops the trailing version, so it is tracking rather than v1",
		},
		{
			name: "single wildcard does not span segments", pkg: "fleet.a.b.v1",
			db: "", schema: "", strip: true,
			comment: "no rule matches, but the global strip_version still applies",
		},
		{
			name: "trailing ** matches any remainder", pkg: "billing.invoices.v2",
			db: "billing_db", schema: "billing_invoices", strip: false,
			comment: "per-rule strip_version:false overrides the global true",
		},
		{
			name: "unmatched package keeps the global default", pkg: "unrelated.v1",
			db: "", schema: "", strip: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, schemaName, strip := cfg.resolve(tc.pkg)
			if db != tc.db || schemaName != tc.schema || strip != tc.strip {
				t.Errorf("resolve(%q) = (%q, %q, %v), want (%q, %q, %v)%s",
					tc.pkg, db, schemaName, strip, tc.db, tc.schema, tc.strip, note(tc.comment))
			}
		})
	}
}

// TestResolveSchemaDepthOverrun pins the case a naive implementation gets wrong by
// panicking: a schema_depth longer than the package has segments. Falling back to
// "no opinion" is the right answer — the rule is misconfigured, and protokit's
// package-path default is a better outcome than a truncated name or a crash.
func TestResolveSchemaDepthOverrun(t *testing.T) {
	cfg := &LayoutConfig{Datasources: []DatasourceRule{
		{Match: "a.**", Database: "db", SchemaDepth: 9},
	}}
	if _, schemaName, _ := cfg.resolve("a.b"); schemaName != "" {
		t.Errorf("schema = %q, want empty for a schema_depth past the package length", schemaName)
	}
}

// TestLayoutNilConfig pins the contract protokit relies on: a plugin invoked with
// no config file supplies a resolver, not nil, and that resolver reports no
// opinion. The distinction is load bearing in protokit — ok=false means "fall back
// to package-path defaults", while ok=true with empty strings means "a config was
// loaded and had nothing to say about this package", which still carries the
// global strip_version.
func TestLayoutNilConfig(t *testing.T) {
	l := Layout(nil)
	if _, _, _, ok := l.ResolveDatasource("anything.v1"); ok {
		t.Error("ResolveDatasource ok = true for a nil config, want false")
	}
	if l.DedupeSchemaTable() {
		t.Error("DedupeSchemaTable = true for a nil config, want false")
	}
}

// TestLayoutLoadedConfigAlwaysHasAnOpinion is the other half of that contract: a
// loaded config answers ok=true even for a package no rule names, because its
// global strip_version still applies to the name protokit derives.
func TestLayoutLoadedConfigAlwaysHasAnOpinion(t *testing.T) {
	l := Layout(&LayoutConfig{StripVersion: true})
	db, schemaName, strip, ok := l.ResolveDatasource("unmatched.v1")
	if !ok {
		t.Fatal("ResolveDatasource ok = false for a loaded config, want true")
	}
	if db != "" || schemaName != "" {
		t.Errorf("got (%q, %q), want both empty — no rule matched", db, schemaName)
	}
	if !strip {
		t.Error("stripVersion = false, want the global strip_version to apply")
	}
}

func note(s string) string {
	if s == "" {
		return ""
	}
	return " — " + s
}
