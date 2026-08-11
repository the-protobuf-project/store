package wire

// graphqlsource.go is the only wire file that imports the GraphQL *source*
// package. Keeping it apart from graphqltarget.go (which imports the same-named
// GraphQL *target* package) lets both use the bare identifier `graphql` with no
// import alias.

import (
	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
	"github.com/the-protobuf-project/store/plugin/factory/source/graphql"
)

// NewGraphQLSource builds the GraphQL source from an endpoint (or cached SDL/JSON
// schema file), the auth secret + headers, and the dialect. Returned as the
// factory.Source[*coreir.Model] interface so callers need not import the graphql source package.
func NewGraphQLSource(endpoint, schemaFile, adminSecret string, headers []string, d dialect.Dialect) factory.Source[*coreir.Model] {
	return graphql.New(graphql.Config{
		Endpoint:    endpoint,
		SchemaFile:  schemaFile,
		AdminSecret: adminSecret,
		Headers:     headers,
		Dialect:     d,
	})
}

// addGraphQLSource registers a zero-config graphql source (for validation/listing).
func addGraphQLSource(reg *factory.Registry[*coreir.Model]) {
	reg.AddSource(graphql.New(graphql.Config{}))
}
