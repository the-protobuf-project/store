// Package entity is the reader for entity.v1, the neutral schema vocabulary that
// decides what things are *named* — databases, schemas, tables, columns — as
// distinct from how any one generator stores them.
//
// # Import this package; do not reimplement it
//
// A plugin that writes its own entity.v1 reader is doing it wrong, and the
// failure is quiet. protokit does not read entity.v1 — it reads AIP and nothing
// else, and every vocabulary reaches it through a registered reader. So there is
// no engine-level arbiter that two plugins agree on a table name. What makes them
// agree is that they run *the same code* over the same annotations. Two readers
// over one proto module drift the moment either one handles an edge case
// differently: a missing google.api.resource.plural, an empty schema override, a
// version suffix stripped in one and not the other.
//
// golden.IRAgreement catches the divergence after the fact. Importing this
// package prevents it.
//
// # Why it lives in store
//
// The obvious home for a vocabulary two plugins share is the engine they both
// depend on, and this vocabulary was there — it shipped as protokit.v1 through
// protokit v1.3.0. It moved because of what it says. datasource, table, column,
// and id strategy are persistence-shaped, and web3 has no datasources and no
// tables; holding them in protokit made the neutral engine a persistence engine
// with a generic name, and handed every future non-relational plugin a vocabulary
// it could not use. protokit's import-boundary test had to carry an allowlist
// entry for the one annotation module protokit imported.
//
// So it lives in the repository that owns what an entity means, and is shared the
// way every other vocabulary is shared: as a module on the BSR
// (buf.build/the-protobuf-project/entity) plus a reader over it. See
// protokit's docs/ownership.md, rule 4.
//
// # What it costs to depend on
//
// Nothing but protokit. This is a nested module —
// github.com/the-protobuf-project/store/plugin/entity — that imports protokit and its own
// generated stubs and nothing else from store. A cache generator, a streams
// generator, or a documentation generator can consume the neutral names without
// pulling in gorm, prisma, graphql, or a line of SQL. That constraint is the whole
// reason the module is nested rather than living beside the plugin, and it is
// worth keeping: the moment this package imports store proper, every consumer
// inherits a database generator.
//
// # Using it
//
// A plugin composes the readers it needs and passes them to protokit.Build:
//
//	protokit.RunPlugin(p, opts, protokit.Plugin{
//	    Registry: targets,
//	    Readers: []protokit.FacetReader{
//	        entity.Reader(),       // the neutral names
//	        entity.CompatReader(), // deprecated orm.v1 / store.v1 structure
//	        myReader{},            // this generator's own vocabulary
//	    },
//	    Layout: entity.Layout(cfg),
//	})
//
// Readers run in sorted Key order, so entity.v1 is resolved before
// entity.v1-compat and before any generator vocabulary — an explicit entity.v1
// annotation wins over a legacy one, and protokit reports the loser as a lint
// diagnostic rather than silently dropping it.
package entity
