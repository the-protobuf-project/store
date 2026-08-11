package golang

import (
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/typemap"
)

// TestIsOrderByNestedRelations verifies that an order-by input whose fields include a nested
// relation's order-by input (e.g. ResourceOrderByExp.campaign: CampaignOrderByExp) is still
// recognized as an order-by — not misclassified as an insert-object list, which previously
// made the generated list query demand a CreateInput as its order_by argument.
func TestIsOrderByNestedRelations(t *testing.T) {
	dir := ir.FieldType{Base: "OrderBy", NonNull: false}
	schema := &ir.Schema{
		Objects: map[string]*ir.Object{},
		Enums:   map[string]*ir.Enum{},
		Scalars: map[string]bool{"OrderBy": true, "String": true},
		Inputs: map[string]*ir.Input{
			"ResourceOrderByExp": {Name: "ResourceOrderByExp", Fields: []ir.Field{
				{Name: "id", Type: dir},
				{Name: "campaign", Type: ir.FieldType{Base: "CampaignOrderByExp"}},
			}},
			"CampaignOrderByExp": {Name: "CampaignOrderByExp", Fields: []ir.Field{
				{Name: "name", Type: dir},
			}},
			// An insert object: scalar columns, never an order-by.
			"ResourceInsertInput": {Name: "ResourceInsertInput", Fields: []ir.Field{
				{Name: "name", Type: ir.FieldType{Base: "String", NonNull: true}},
			}},
		},
	}
	r := &renderer{schema: schema, mapper: typemap.New(schema, nil, dialect.Default())}

	if !r.isOrderBy("ResourceOrderByExp") {
		t.Error("ResourceOrderByExp with a nested relation order-by should be an order-by input")
	}
	if !r.isOrderBy("CampaignOrderByExp") {
		t.Error("CampaignOrderByExp should be an order-by input")
	}
	if r.isOrderBy("ResourceInsertInput") {
		t.Error("an insert input with scalar columns must not be classified as order-by")
	}
}

// TestIdentifierKeywordSafe verifies that names reducing to Go keywords are made into
// valid package identifiers (a table named "Type" must not yield `package type`).
func TestIdentifierKeywordSafe(t *testing.T) {
	cases := map[string]string{
		"Type":           "type_",
		"Map":            "map_",
		"Func":           "func_",
		"Range":          "range_",
		"Select":         "select_",
		"Interface":      "interface_",
		"BufferSettings": "buffersettings",
		"123":            "res123",
		"":               "res",
	}
	for in, want := range cases {
		if got := identifier(in); got != want {
			t.Errorf("identifier(%q) = %q, want %q", in, got, want)
		}
	}
}
