Rename the orm plugin to store and split its annotations.

RENAMES
  repo:    the-protobuf-project/orm    → the-protobuf-project/store
  binary:  protoc-gen-orm              → protoc-gen-store
  config:  orm.yaml                    → store.yaml (accept orm.yaml with a warn)
  cask:    protoc-gen-orm              → protoc-gen-store (keep old as alias 1 major)
Targets keep their names: gorm | sql | prisma | graphql.

ANNOTATION SPLIT
  Move to schema.v1 (now read by protokit): datasource{database,schema,url,provider},
  table{table,skip,id,timestamps,indexes}, column{column,skip}.
  Keep in store.v1: column{type,max_length,precision,scale,default_value,unique,
  index,on_delete,on_update}, query{filterable,sortable,search}.
  Ship a compat FacetReader that reads deprecated orm.v1 fields, maps them onto
  their schema.v1 equivalents, and emits a deprecation diagnostic naming the
  replacement. Remove after one major.

STRUCTURAL CHANGES

1. Become a FacetReader.
   Replace the schema.Backend implementation with a store.v1 FacetReader.
   Targets read core IR for naming and the store.v1 facet for physical types.

2. Emit store interfaces (this unblocks every future decorator).
   For each resource the gorm target currently emits `AuthorStore` (struct).
   Also emit `AuthorStoreIface` with the full method set — Create, GetByID, List,
   Count, Update, DeleteByID, every GetBy<Col>, every ListBy<FK>. The concrete
   struct satisfies it. Constructors keep returning the concrete type.
   Cache and any other plugin will generate decorators against the interface,
   in their own package, and must never need to edit files in this tree.

3. Telemetry: staged split.
   Move the telemetry.v1 reading + rendering into its own Go package
   (telemetry/ in a separate repo) exposing a FacetReader and a Target.
   protoc-gen-store's main.go imports and registers it for now. This makes the
   eventual protoc-gen-telemetry split a main.go change, not a refactor.

4. Manifest.
   Add plugin.yaml:
     provides: [store]
     annotations: { schema.v1: "^1", store.v1: "^1" }
     runtime: { go: "runtime-go/database ^0.x" }
     facets: { reads: [schema.v1, store.v1] }
     outputs: ["<out>/**"]

5. Banner provenance.
   The generated header records the full triple: plugin@version,
   annotation modules@version, runtime module@version.

6. Outbox (needed by streams later — store owns it because it's a table).
   Add schema.v1 table option `outbox: true`... actually put this in store.v1 as
   `store.v1.table.outbox`. When set, emit the outbox table DDL/model alongside
   the resource. Off by default. Emit nothing else — streams generates the
   publisher against it.

CONSTRAINTS

- Golden output for the bookstore example must be identical except for the
  banner and renamed packages. Any other diff is a bug.
- Existing protos using only orm.v1 must still generate, with deprecation warnings.
