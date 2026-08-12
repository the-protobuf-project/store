package resources

// kind.go projects protokit's neutral FieldType onto runtime-go's store.Kind.

import "github.com/the-protobuf-project/protokit/schema"

// storeKind names the store.Kind constant a column's neutral FieldType maps to.
// It returns the identifier as written in the generated file ("KindString"), not
// a value, because this plugin does not import runtime-go — the descriptor is
// rendered as source.
//
// store.Kind is deliberately coarser than FieldType: it exists so the runtime
// bridge knows how to move a value between a proto field and a backend column,
// not to describe the column's type. Anything the bridge handles as a string
// (a decimal, a duration, a JSON blob) is KindString regardless of how a
// relational target would render it — SQLType carries that detail separately.
func storeKind(t schema.FieldType) string {
	switch t {
	case schema.TypeBool:
		return "KindBool"

	case schema.TypeInt32, schema.TypeInt64:
		return "KindInt"

	case schema.TypeUint32, schema.TypeUint64:
		return "KindUint"

	case schema.TypeFloat, schema.TypeDouble:
		return "KindFloat"

	case schema.TypeBytes:
		return "KindBytes"

	case schema.TypeEnum:
		// The bridge moves enums as their int32 number, not their name.
		return "KindEnum"

	case schema.TypeTimestamp:
		return "KindTimestamp"

	case schema.TypeString, schema.TypeText, schema.TypeULID, schema.TypeUUID:
		return "KindString"

	// Everything below has no distinct runtime representation: the bridge reads
	// and writes it as a string, and SQLType tells a relational driver what the
	// column really is. Listing them explicitly rather than falling through
	// keeps a newly added FieldType from being silently absorbed here.
	case schema.TypeDuration, schema.TypeDate, schema.TypeTimeOfDay,
		schema.TypeDecimal, schema.TypeLatLng, schema.TypeInterval,
		schema.TypeJSON:
		return "KindString"

	default:
		// KindUnknown is documented as "treat as KindString", so an unclassified
		// column degrades to the same behaviour rather than failing generation.
		return "KindUnknown"
	}
}
