// Package validate is the single projection table for orm.v1's validation
// presets. One preset resolves to three renderings, and every target reads them
// from here so the application check, the AutoMigrate tag, and the DDL constraint
// can never drift apart:
//
//   - Func — the validatex predicate the generated store calls on the write path.
//   - Tag  — a go-playground/validator tag fragment, emitted on the model struct
//     purely for interop. Nothing generated depends on it: enforcement is the
//     generated call to Func, so no consumer takes a validator dependency.
//   - SQL  — a Postgres CHECK body, so the constraint survives writes that never
//     go through a generated store.
//
// Presets are read off the column's Source descriptor at render time rather than
// stored on the IR — the same pattern types.SQLForColumn uses for the type
// override, keeping protokit free of orm-specific fields.
package validate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Rule is one preset's full projection. An empty Tag or SQL means that rendering
// has no faithful equivalent, not that it was overlooked: VALIDATE_PAST has no
// immutable SQL form (a CHECK cannot reference wall-clock time), and
// VALIDATE_SLUG has no go-playground tag (its regex has no builtin).
type Rule struct {
	// Preset is the orm.v1.Validate value this rule renders.
	Preset ormpbv1.Validate

	// Func is the validatex predicate name the generated store calls. Always set.
	Func string

	// Tag is the go-playground/validator tag fragment, or "" when that library
	// has no builtin for this preset.
	Tag string

	// SQL is the Postgres CHECK body with "{}" standing in for the quoted column
	// reference, or "" when the preset cannot be expressed as an immutable CHECK.
	SQL string

	// Message is the failure text the generated check returns, phrased to complete
	// the sentence "<field> must ...".
	Message string

	// Kinds are the neutral field types this preset accepts. A preset applied to
	// any other type is a schema author error, reported by Verify.
	Kinds []schema.FieldType

	// List reports whether the preset applies to the repeated cardinality itself
	// rather than to each element, in which case Kinds is not consulted.
	List bool
}

// text is every neutral type the string-shaped presets accept.
var text = []schema.FieldType{schema.TypeString, schema.TypeText}

// numeric is every neutral type the sign presets accept.
var numeric = []schema.FieldType{
	schema.TypeInt32, schema.TypeInt64, schema.TypeUint32, schema.TypeUint64,
	schema.TypeFloat, schema.TypeDouble, schema.TypeDecimal,
}

// integral is the subset of numeric types with no fractional part, where a
// fractional bound could not be rendered as a Go constant.
var integral = []schema.FieldType{
	schema.TypeInt32, schema.TypeInt64, schema.TypeUint32, schema.TypeUint64,
}

// float is the subset of numeric types carrying NaN and infinity.
var float = []schema.FieldType{schema.TypeFloat, schema.TypeDouble}

// jsonKinds is every neutral type VALIDATE_JSON accepts.
var jsonKinds = []schema.FieldType{schema.TypeString, schema.TypeText, schema.TypeJSON}

// temporal is every neutral type the PAST/FUTURE presets accept.
var temporal = []schema.FieldType{schema.TypeTimestamp, schema.TypeDate}

// rules is the projection table, keyed by preset. Regexes are POSIX so the same
// pattern reaches Go's RE2 and Postgres' ~ operator with the same meaning.
//
// No SQL expression may contain a backslash, and TestRuleSQLHasNoBackslash
// enforces it. These expressions are embedded in GORM struct tags, whose values
// reflect.StructTag.Get unquotes as Go string literals: a stray `\.` is an
// invalid escape, so the unquote fails and the ENTIRE gorm tag for that field is
// silently discarded — losing `not null` and `column:` along with the check.
// Write [.] rather than \. and the problem cannot arise.
var rules = map[ormpbv1.Validate]Rule{
	ormpbv1.Validate_VALIDATE_EMAIL: {
		Func: "Email", Tag: "email", Kinds: text,
		SQL:     `{} ~ '^[^@[:space:]]+@[^@[:space:]]+[.][^@[:space:]]+$'`,
		Message: "be a valid email address",
	},
	ormpbv1.Validate_VALIDATE_URL: {
		Func: "URL", Tag: "url", Kinds: text,
		SQL:     `{} ~ '^[a-zA-Z][a-zA-Z0-9+.-]*://[^[:space:]]+$'`,
		Message: "be an absolute URL",
	},
	ormpbv1.Validate_VALIDATE_UUID: {
		Func: "UUID", Tag: "uuid", Kinds: text,
		SQL:     `{} ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'`,
		Message: "be a canonical UUID",
	},
	ormpbv1.Validate_VALIDATE_ULID: {
		Func: "ULID", Tag: "ulid", Kinds: text,
		SQL:     `{} ~ '^[0-9A-HJKMNP-TV-Z]{26}$'`,
		Message: "be a Crockford base32 ULID",
	},
	ormpbv1.Validate_VALIDATE_HOSTNAME: {
		Func: "Hostname", Tag: "hostname", Kinds: text,
		SQL:     `{} ~ '^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?([.][a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$'`,
		Message: "be a valid hostname",
	},
	ormpbv1.Validate_VALIDATE_IP: {
		Func: "IP", Tag: "ip", Kinds: text,
		Message: "be a valid IP address",
	},
	ormpbv1.Validate_VALIDATE_IPV4: {
		Func: "IPv4", Tag: "ipv4", Kinds: text,
		SQL:     `{} ~ '^([0-9]{1,3}[.]){3}[0-9]{1,3}$'`,
		Message: "be a valid IPv4 address",
	},
	ormpbv1.Validate_VALIDATE_IPV6: {
		Func: "IPv6", Tag: "ipv6", Kinds: text,
		Message: "be a valid IPv6 address",
	},
	ormpbv1.Validate_VALIDATE_E164: {
		Func: "E164", Tag: "e164", Kinds: text,
		SQL:     `{} ~ '^[+][1-9][0-9]{1,14}$'`,
		Message: "be an E.164 phone number",
	},
	ormpbv1.Validate_VALIDATE_SLUG: {
		Func: "Slug", Kinds: text,
		SQL:     `{} ~ '^[a-z0-9]+(-[a-z0-9]+)*$'`,
		Message: "be a URL-safe slug",
	},
	ormpbv1.Validate_VALIDATE_SEMVER: {
		Func: "Semver", Tag: "semver", Kinds: text,
		SQL:     `{} ~ '^[0-9]+[.][0-9]+[.][0-9]+([-+].*)?$'`,
		Message: "be a semantic version",
	},
	ormpbv1.Validate_VALIDATE_HEX_COLOR: {
		Func: "HexColor", Tag: "hexcolor", Kinds: text,
		SQL:     `{} ~ '^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$'`,
		Message: "be a hex color",
	},
	ormpbv1.Validate_VALIDATE_BASE64: {
		// Text only: a bytes column stores BYTEA, where "is this base64" is a
		// question about a wire encoding the column no longer carries.
		Func: "Base64", Tag: "base64", Kinds: text,
		SQL:     `{} ~ '^[A-Za-z0-9+/]*={0,2}$'`,
		Message: "be base64",
	},
	ormpbv1.Validate_VALIDATE_JSON: {
		Func: "JSON", Tag: "json", Kinds: jsonKinds,
		Message: "be a JSON document",
	},
	ormpbv1.Validate_VALIDATE_NON_EMPTY: {
		Func: "NonEmpty", Kinds: text,
		SQL:     `length(btrim({})) > 0`,
		Message: "not be blank",
	},
	ormpbv1.Validate_VALIDATE_TRIMMED: {
		Func: "Trimmed", Kinds: text,
		SQL:     `{} = btrim({})`,
		Message: "not have leading or trailing whitespace",
	},
	ormpbv1.Validate_VALIDATE_LOWERCASE: {
		Func: "Lowercase", Tag: "lowercase", Kinds: text,
		SQL:     `{} = lower({})`,
		Message: "be lowercase",
	},
	ormpbv1.Validate_VALIDATE_UPPERCASE: {
		Func: "Uppercase", Tag: "uppercase", Kinds: text,
		SQL:     `{} = upper({})`,
		Message: "be uppercase",
	},
	ormpbv1.Validate_VALIDATE_ASCII: {
		Func: "ASCII", Tag: "ascii", Kinds: text,
		SQL:     `{} ~ '^[[:print:]]*$'`,
		Message: "be printable ASCII",
	},
	ormpbv1.Validate_VALIDATE_ALPHANUMERIC: {
		Func: "Alphanumeric", Tag: "alphanum", Kinds: text,
		SQL:     `{} ~ '^[[:alnum:]]+$'`,
		Message: "be alphanumeric",
	},
	ormpbv1.Validate_VALIDATE_COUNTRY_CODE: {
		Func: "CountryCode", Tag: "iso3166_1_alpha2", Kinds: text,
		SQL:     `{} ~ '^[A-Z]{2}$'`,
		Message: "be an ISO 3166-1 alpha-2 country code",
	},
	ormpbv1.Validate_VALIDATE_CURRENCY_CODE: {
		Func: "CurrencyCode", Tag: "iso4217", Kinds: text,
		SQL:     `{} ~ '^[A-Z]{3}$'`,
		Message: "be an ISO 4217 currency code",
	},
	ormpbv1.Validate_VALIDATE_LANGUAGE_TAG: {
		Func: "LanguageTag", Tag: "bcp47_language_tag", Kinds: text,
		SQL:     `{} ~ '^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$'`,
		Message: "be a BCP 47 language tag",
	},
	ormpbv1.Validate_VALIDATE_TIMEZONE: {
		// No CHECK: resolving an IANA name needs pg_timezone_names, and a
		// subquery is not allowed in a check constraint.
		Func: "Timezone", Tag: "timezone", Kinds: text,
		Message: "be an IANA time zone",
	},
	ormpbv1.Validate_VALIDATE_POSITIVE: {
		Func: "Positive", Tag: "gt=0", Kinds: numeric,
		SQL:     `{} > 0`,
		Message: "be greater than zero",
	},
	ormpbv1.Validate_VALIDATE_NON_NEGATIVE: {
		Func: "NonNegative", Tag: "gte=0", Kinds: numeric,
		SQL:     `{} >= 0`,
		Message: "not be negative",
	},
	ormpbv1.Validate_VALIDATE_NEGATIVE: {
		Func: "Negative", Tag: "lt=0", Kinds: numeric,
		SQL:     `{} < 0`,
		Message: "be less than zero",
	},
	ormpbv1.Validate_VALIDATE_FINITE: {
		// Postgres orders NaN as equal to itself, so `{} = {}` does NOT catch it
		// the way IEEE comparison would — the sentinel has to be named directly.
		Func: "Finite", Kinds: float,
		SQL:     `{} <> 'NaN'::float8 AND {} <> 'Infinity'::float8 AND {} <> '-Infinity'::float8`,
		Message: "be finite",
	},
	ormpbv1.Validate_VALIDATE_PAST: {
		// No CHECK: a constraint referencing now() is not immutable, so the same
		// row would validate differently on a later table rewrite.
		Func: "Past", Kinds: temporal,
		Message: "be in the past",
	},
	ormpbv1.Validate_VALIDATE_FUTURE: {
		Func: "Future", Kinds: temporal,
		Message: "be in the future",
	},
	ormpbv1.Validate_VALIDATE_UNIQUE_ITEMS: {
		// No CHECK: de-duplicating needs unnest in a subquery, disallowed here.
		Func: "UniqueItems", Tag: "unique", List: true,
		Message: "not contain duplicates",
	},
	ormpbv1.Validate_VALIDATE_NON_EMPTY_LIST: {
		Func: "NonEmptyList", Tag: "min=1", List: true,
		SQL:     `cardinality({}) > 0`,
		Message: "not be empty",
	},
}

// FuncNames returns every validatex predicate the table refers to, sorted. The
// generated runtime must declare all of them — see the gorm target's parity test.
func FuncNames() []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Func)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Each rule's Preset is its own map key. Filling it here rather than repeating
// it in every literal keeps the two from disagreeing — a mismatch would name a
// constraint after the wrong preset.
func init() {
	for p, r := range rules {
		r.Preset = p
		rules[p] = r
	}
}

// Presets returns the column's declared presets in annotation order, dropping
// the unspecified sentinel and any repeat. Nil when the column carries none or
// was synthesized by protokit (no Source descriptor).
func Presets(col *schema.Column) []ormpbv1.Validate {
	if col == nil || col.Source == nil {
		return nil
	}
	return presets(col.Source)
}

func presets(d protoreflect.FieldDescriptor) []ormpbv1.Validate {
	if !proto.HasExtension(d.Options(), ormpbv1.E_Validate) {
		return nil
	}
	declared, _ := proto.GetExtension(d.Options(), ormpbv1.E_Validate).([]ormpbv1.Validate)
	out := make([]ormpbv1.Validate, 0, len(declared))
	seen := map[ormpbv1.Validate]bool{}
	for _, p := range declared {
		if p == ormpbv1.Validate_VALIDATE_UNSPECIFIED || seen[p] {
			continue
		}
		if _, ok := rules[p]; !ok {
			continue // a preset this build of the plugin predates
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Rules returns the projection of every preset declared on the column, in
// annotation order.
func Rules(col *schema.Column) []Rule {
	ps := Presets(col)
	out := make([]Rule, 0, len(ps))
	for _, p := range ps {
		out = append(out, rules[p])
	}
	return out
}

// Tag returns the go-playground/validator fragment for the column's presets —
// "email,lowercase" — omitting presets with no builtin. Empty when nothing maps.
// The caller joins this with the required rule it already emits.
func Tag(col *schema.Column) string {
	var parts []string
	for _, r := range Rules(col) {
		if r.Tag != "" {
			parts = append(parts, r.Tag)
		}
	}
	for _, c := range Constraints(col) {
		if c.Tag != "" {
			parts = append(parts, c.Tag)
		}
	}
	return strings.Join(parts, ",")
}

// Check is one named CHECK constraint projected from a preset. Both the SQL
// target's DDL and the gorm target's AutoMigrate tag are built from this, so the
// two backends create the same constraint under the same name.
type Check struct {
	// Name is the constraint identifier, chk_<table>_<column>_<preset>.
	Name string

	// Expr is the constraint body with the column reference substituted in.
	Expr string
}

// ColumnCheck returns the single CHECK constraint covering every DB-expressible
// preset on the column, its clauses AND-ed together. ref is the column reference
// to substitute — the bare column name for a GORM tag, the quoted identifier for
// DDL. Reports false when no preset on the column has a SQL form.
//
// One constraint per column rather than one per preset, because GORM parses a
// field's tag into a map keyed by setting name: a second `check:` overwrites the
// first, so a column with two presets would keep only the last constraint while
// the SQL target emitted both. Combining makes the two backends agree by
// construction instead of by convention.
func ColumnCheck(table string, col *schema.Column, ref string) (Check, bool) {
	var clauses []string
	for _, r := range Rules(col) {
		if r.SQL == "" || !r.Accepts(col) {
			continue
		}
		clauses = append(clauses, strings.ReplaceAll(r.SQL, "{}", ref))
	}
	for _, c := range Constraints(col) {
		if c.SQL == "" {
			continue
		}
		clauses = append(clauses, strings.ReplaceAll(c.SQL, "{}", ref))
	}
	if len(clauses) == 0 {
		return Check{}, false
	}
	return Check{
		Name: constraintName(table, col.Name),
		Expr: strings.Join(clauses, " AND "),
	}, true
}

// TagValue escapes a CHECK expression for embedding in a Go struct tag.
// reflect.StructTag.Get unquotes a tag value as a Go string literal, so a
// backslash or a double quote reaching it verbatim makes the unquote fail and
// silently voids the ENTIRE gorm tag for that field — the column then migrates
// without its NOT NULL or its type.
//
// The preset expressions avoid backslashes by construction (see the rules table)
// and TestRuleSQLHasNoBackslash enforces that, but a user-supplied
// (orm.v1.constraint).pattern can legitimately contain `\d`, so anything bound
// for a tag goes through here. A backtick cannot be escaped at all — it would
// close the raw string the tag is written in — and is rejected during Verify.
func TagValue(expr string) string {
	expr = strings.ReplaceAll(expr, `\`, `\\`)
	return strings.ReplaceAll(expr, `"`, `\"`)
}

// Slug is the preset's bare name, e.g. VALIDATE_NON_NEGATIVE → "non_negative".
// It distinguishes the constraints of a column carrying several presets.
func (r Rule) Slug() string {
	return strings.ToLower(strings.TrimPrefix(r.Preset.String(), "VALIDATE_"))
}

// constraintName builds the shared chk_<table>_<column> identifier, matching the
// shape enumCheck already uses, truncated to Postgres' 63-byte identifier limit —
// which the server would otherwise apply itself, silently leaving the two
// backends naming the same constraint differently.
func constraintName(table, column string) string {
	name := "chk_" + table + "_" + column
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// Verify returns a message for every preset on the column that cannot apply to
// its type or cardinality — VALIDATE_EMAIL on an int32, say. These are hard
// errors rather than warnings because there is no sensible fallback: the
// annotation would simply be dropped, leaving the author believing a column is
// validated when nothing checks it. Nil when every preset fits.
func Verify(col *schema.Column) []string {
	var out []string
	for _, r := range Rules(col) {
		if r.Accepts(col) {
			continue
		}
		if col.List && !r.List {
			out = append(out, fmt.Sprintf("%s judges a single value, not a repeated column", r.Preset))
			continue
		}
		out = append(out, fmt.Sprintf("%s applies to %s", r.Preset, r.applies()))
	}
	return append(out, VerifyConstraints(col)...)
}

// applies names the column kinds a preset accepts, for Verify's message.
func (r Rule) applies() string {
	switch {
	case r.List:
		return "repeated columns"
	case slices.Equal(r.Kinds, text):
		return "string columns"
	case slices.Equal(r.Kinds, numeric):
		return "numeric columns"
	case slices.Equal(r.Kinds, float):
		return "float or double columns"
	case slices.Equal(r.Kinds, temporal):
		return "timestamp or date columns"
	case slices.Equal(r.Kinds, jsonKinds):
		return "string or JSON columns"
	default:
		return "other column types"
	}
}

// Accepts reports whether the preset applies to the column's cardinality and
// neutral type.
func (r Rule) Accepts(col *schema.Column) bool {
	if r.List {
		return col.List
	}
	if col.List {
		return false // an element preset on a repeated column is ambiguous
	}
	return slices.Contains(r.Kinds, col.Type)
}
