package prisma

// view_field.go holds the column-level rendering helpers used by modelViewOf in
// view.go: one field declaration, referential-action formatting, doc fallbacks,
// and the name-deduplication used to keep relation fields from colliding.

import (
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/target/types"
)

// scalarFieldName is the Prisma field identifier for a column. Foreign-key
// columns gain an "Id" suffix (resource → resourceId) so the bare name is free
// for the relation field; the DB column stays col.Name via @map.
func scalarFieldName(col *schema.Column) string {
	return naming.Camel(naming.FKFieldBase(col.Name, col.FKModel != ""))
}

// fieldDecl renders one column declaration: name, type, and attributes. dsName is
// the Prisma datasource block name, used to prefix native-type attributes
// (@<dsName>.Timestamptz(6)).
func fieldDecl(col *schema.Column, provider types.Provider, dsName string, typeOf types.TypeOf) string {
	var b strings.Builder
	b.WriteString(scalarFieldName(col))
	b.WriteByte(' ')
	var typeName string
	if col.Enum != nil {
		typeName = col.Enum.Name
	} else {
		typeName = types.PrismaTypeFor(provider, typeOf(col))
	}
	b.WriteString(typeName)
	// A Prisma list is implicitly empty-not-null: an optional list (`Type[]?`)
	// is a schema error, so only scalar columns take the optional marker.
	if col.Optional && !strings.HasSuffix(typeName, "[]") {
		b.WriteByte('?')
	}
	if col.PrimaryKey {
		b.WriteString(" @id")
	}
	if col.Unique {
		b.WriteString(" @unique")
	}
	switch {
	case col.Generated != "":
		b.WriteString(" @default(")
		b.WriteString(col.Generated)
		b.WriteString("())") // ulid() / uuid()
	case col.AutoUpdate:
		b.WriteString(" @updatedAt") // Prisma maintains the value; no @default
	case col.Default != "":
		b.WriteString(" @default(")
		b.WriteString(col.Default)
		b.WriteString(")")
	}
	// Pin the Postgres native type when Prisma's default would drift from the
	// GORM/SQL column (DateTime → timestamp(3) without zone). The attribute is
	// namespaced by the datasource block name. Mongo has no native-type attributes.
	if provider == types.Postgres {
		if nt := types.PrismaNativeType(typeOf(col)); nt != "" {
			b.WriteString(" @" + dsName + "." + nt)
		}
	}
	mapName := col.Name
	if provider == types.MongoDB && col.PrimaryKey {
		mapName = "_id" // Mongo documents key on _id; Prisma requires the mapping.
	}
	b.WriteString(` @map("`)
	b.WriteString(mapName)
	b.WriteString(`")`)
	return b.String()
}

// prismaAction converts a SQL referential action to Prisma's identifier form:
// "SET NULL" → "SetNull", "CASCADE" → "Cascade". Empty stays empty.
func prismaAction(sqlAction string) string {
	if sqlAction == "" {
		return ""
	}
	var b strings.Builder
	for _, word := range strings.Fields(sqlAction) {
		b.WriteString(strings.ToUpper(word[:1]))
		b.WriteString(strings.ToLower(word[1:]))
	}
	return b.String()
}

// fieldDoc returns the /// documentation for a column: the proto comment when
// present, otherwise a generated description.
func fieldDoc(col *schema.Column) string {
	if col.Comment != "" {
		return col.Comment
	}
	switch {
	case col.PrimaryKey:
		return `Unique identifier for the record. Primary key mapped to "` + col.Name + `".`
	case col.Optional:
		return `Optional column mapped to "` + col.Name + `".`
	default:
		return `Required column mapped to "` + col.Name + `".`
	}
}

// withFallbackComments fills empty enum/value comments so every line still
// carries /// documentation, matching the hand-written schema convention.
func withFallbackComments(e *schema.Enum) *schema.Enum {
	if e.Comment == "" {
		e.Comment = "Enum representing " + e.Name + " values."
	}
	for _, v := range e.Values {
		if v.Comment == "" {
			v.Comment = "Represents the " + v.MapName + " value."
		}
	}
	return e
}

// commentOr returns comment when non-empty, otherwise the fallback.
func commentOr(comment, fallback string) string {
	if comment != "" {
		return comment
	}
	return fallback
}
