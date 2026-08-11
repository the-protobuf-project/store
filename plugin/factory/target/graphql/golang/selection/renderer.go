// Package selection renders Go model struct bodies from IR objects.
//
// Selection sets are expressed as the nested Go struct shape: scalar/enum fields
// become typed fields, and relation (object) fields become inline anonymous structs
// expanded up to a configured depth. A per-branch visited set prevents infinite
// recursion on cyclic relationships. The rendered body feeds the model templates and
// is the single source of truth for both the struct shape and the GraphQL selection.
package selection

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/store/plugin/factory/target/graphql/golang/typemap"
)

// Renderer builds model bodies for a schema.
type Renderer struct {
	schema   *ir.Schema
	mapper   *typemap.Mapper
	maxDepth int
	qual     typemap.Qualifier
}

// New returns a Renderer. maxDepth controls how many levels of relations are inlined
// into a model (0 = scalars only, 1 = direct relations, etc.). qual qualifies leaf
// references (e.g. enum fields) for the package the models are written into.
func New(schema *ir.Schema, mapper *typemap.Mapper, maxDepth int, qual typemap.Qualifier) *Renderer {
	return &Renderer{schema: schema, mapper: mapper, maxDepth: maxDepth, qual: qual}
}

// ModelBody renders the field declarations for an object's model struct, expanding
// relations inline up to the renderer's maxDepth.
func (r *Renderer) ModelBody(obj *ir.Object) string {
	visited := map[string]bool{obj.Name: true}
	return r.body(obj, r.maxDepth, visited)
}

// body renders one object's fields at the given remaining depth.
func (r *Renderer) body(obj *ir.Object, depth int, visited map[string]bool) string {
	var b strings.Builder
	for _, f := range obj.Fields {
		line, ok := r.field(f, depth, visited)
		if ok {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// field renders a single field declaration. The bool result is false when the field
// is skipped (a relation beyond max depth or on a cyclic path).
func (r *Renderer) field(f ir.Field, depth int, visited map[string]bool) (string, bool) {
	tag := fmt.Sprintf("`graphql:%q`", f.Name)
	// A JSON scalar column decodes to an opaque json.RawMessage. Tag it `scalar`
	// so the graphql client copies the raw JSON into the field instead of
	// recursing into the object to match sub-keys against struct fields — which
	// fails for a nullable (*json.RawMessage) field, since only a non-pointer
	// json.RawMessage is auto-detected (hasura/go-graphql-client jsonutil).
	if r.mapper.UsesJSON(f.Type.Base) {
		tag = fmt.Sprintf("`graphql:%q scalar:\"true\"`", f.Name)
	}
	goName := export(f.Name)
	doc := naming.Doc(f.Description)
	if doc == "" {
		doc = naming.Doc(fieldDoc(goName, f))
	}

	if r.schema.IsScalarOrEnum(f.Type.Base) || r.isUnknownLeaf(f.Type.Base) {
		return doc + fmt.Sprintf("\t%s %s %s", goName, r.mapper.GoType(f.Type, r.qual), tag), true
	}

	// Relation: inline the target object's body one level deeper.
	target, ok := r.schema.Objects[f.Type.Base]
	if !ok || depth <= 0 || visited[f.Type.Base] {
		return "", false
	}
	next := cloneVisited(visited)
	next[f.Type.Base] = true
	inner := r.body(target, depth-1, next)

	prefix := "struct {"
	if f.Type.List {
		prefix = "[]struct {"
	}
	return doc + fmt.Sprintf("\t%s %s\n%s\t} %s", goName, prefix, inner, tag), true
}

// fieldDoc synthesizes a field's doc when the schema carries no description:
// the well-known mutation-response fields get precise wording, everything else
// names the GraphQL field it selects (with a nullability note).
func fieldDoc(goName string, f ir.Field) string {
	switch f.Name {
	case "affectedRows":
		return "AffectedRows counts the rows the mutation touched."
	case "returning":
		return "Returning carries the affected rows as stored after the mutation."
	}
	null := ""
	if !f.Type.NonNull && !f.Type.List {
		null = "; nil when the column is NULL"
	}
	return goName + " selects the " + strconv.Quote(f.Name) + " field" + null + "."
}

// isUnknownLeaf reports whether base is neither a known object nor input — treated as
// a leaf so generation degrades gracefully on unusual schemas.
func (r *Renderer) isUnknownLeaf(base string) bool {
	_, isObj := r.schema.Objects[base]
	_, isInput := r.schema.Inputs[base]
	return !isObj && !isInput
}

func cloneVisited(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}
