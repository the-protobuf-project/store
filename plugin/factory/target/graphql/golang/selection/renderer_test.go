package selection

import (
	"strings"
	"testing"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/typemap"
)

// A JSON scalar column decodes to an opaque json.RawMessage. The model struct
// field must carry a `scalar:"true"` tag so hasura/go-graphql-client's jsonutil
// decoder copies the raw JSON into the field instead of recursing into the object
// to match its keys against struct fields — which fails for a nullable
// (*json.RawMessage) field, producing:
//
//	struct field for "<key>" doesn't exist in any of 1 places to unmarshal
func TestModelBodyTagsJSONScalarFields(t *testing.T) {
	schema := &ir.Schema{
		Objects: map[string]*ir.Object{},
		Enums:   map[string]*ir.Enum{},
		Inputs:  map[string]*ir.Input{},
		Scalars: map[string]bool{"Json": true, "String1": true},
	}
	obj := &ir.Object{
		Name: "PropertyProperties",
		Fields: []ir.Field{
			// nullable jsonb -> *json.RawMessage
			{Name: "attributes", Type: ir.FieldType{Base: "Json"}},
			{Name: "displayName", Type: ir.FieldType{Base: "String1", NonNull: true}},
		},
	}
	schema.Objects[obj.Name] = obj

	r := New(schema, typemap.New(schema, nil, dialect.Default()), 5, typemap.Qualifier{})
	body := r.ModelBody(obj)

	wantAttr := "Attributes *json.RawMessage `graphql:\"attributes\" scalar:\"true\"`"
	if !strings.Contains(body, wantAttr) {
		t.Fatalf("JSON scalar field must be tagged scalar:\"true\"; got:\n%s", body)
	}

	wantName := "DisplayName string `graphql:\"displayName\"`"
	if !strings.Contains(body, wantName) {
		t.Fatalf("non-JSON field tag unexpected; got:\n%s", body)
	}
	if strings.Contains(body, "graphql:\"displayName\" scalar:\"true\"") {
		t.Fatalf("non-JSON field must not be tagged scalar; got:\n%s", body)
	}
}
