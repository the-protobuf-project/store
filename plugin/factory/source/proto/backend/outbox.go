package backend

// outbox.go synthesizes the companion table that (store.v1.table).outbox asks
// for: the durable hand-off point for change events, written in the same
// transaction as the row it describes.
//
// It is built as an ordinary schema.Table and appended to the schema during
// enrichment, which is the whole trick. Every target already knows how to render
// a table — so the gorm model, the SQL DDL, and the Prisma model all appear with
// no per-target code, and stay correct as those renderers evolve. An outbox
// emitted by three hand-written branches would drift from the tables around it
// the first time anything changed.
//
// The table and nothing else. No publisher, no store, no dispatch loop: owning
// the table is this plugin's job because an outbox *is* a table, and draining it
// is a different plugin's. A streams generator reads this shape and emits the
// publisher against it.

import (
	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/store/plugin/factory/facets"
)

// outboxSuffix is appended to the owning table's name. "books" → "books_outbox".
const outboxSuffix = "_outbox"

// appendOutboxTables adds an outbox companion for every table whose message
// carries (store.v1.table).outbox.
//
// Each schema is scanned before anything is appended, so a synthesized outbox is
// never itself scanned for the annotation. (It carries no Node, so a facet lookup
// on it would miss anyway — but relying on that would make the loop's correctness
// depend on a detail of the table it just created.)
func appendOutboxTables(ir *schema.IR, fx facets.Set) {
	for _, db := range ir.Databases {
		for _, s := range db.Schemas {
			var add []*schema.Table
			for _, t := range s.Tables {
				if !fx.Table(t).GetOutbox() {
					continue
				}
				add = append(add, outboxTableFor(t, pkTypeOf(t)))
			}
			s.Tables = append(s.Tables, add...)
		}
	}
}

// pkTypeOf returns the neutral type of owner's primary-key column, so
// aggregate_id is declared to match whatever key strategy the table uses — a
// ULID CHAR(26), a native UUID, or the AIP identifier's VARCHAR.
//
// A table with no resolvable primary key falls back to a string, which is what
// the AIP identifier would have been.
func pkTypeOf(owner *schema.Table) schema.FieldType {
	for _, c := range owner.Columns {
		if c.Name == owner.PKColumn {
			return c.Type
		}
	}
	return schema.TypeString
}

// outboxTableFor builds the outbox companion for owner.
//
// The columns are the minimum a transactional outbox needs and no more, because
// every extra column is one a downstream publisher has to agree with:
//
//	id            surrogate ULID key, so rows sort by insertion
//	aggregate_id  the owning row's primary key, typed to match it
//	event_type    what happened, e.g. "book.created"
//	payload       the event body as JSONB
//	created_at    when it was written
//	published_at  when a publisher claimed it; NULL means outstanding
//
// The index on (published_at, created_at) is the one that matters: the publisher's
// hot query is "oldest unpublished rows", and without it that becomes a full scan
// of a table which by design only ever grows between drains.
func outboxTableFor(owner *schema.Table, pkType schema.FieldType) *schema.Table {
	name := owner.Name + outboxSuffix
	t := &schema.Table{
		Name:      name,
		LocalName: owner.LocalName + "Outbox",
		ModelName: owner.ModelName + "Outbox",
		Comment: "Transactional outbox for " + owner.LocalName +
			": change events written in the same transaction as the row they describe.",
		PKColumn:     "id",
		PgSchema:     owner.PgSchema,
		SourceFile:   owner.SourceFile,
		SourceProto:  owner.SourceProto,
		SourceDir:    owner.SourceDir,
		ProtoMessage: owner.ProtoMessage + "Outbox",
		// Source and Node stay empty: this table maps to no proto message, so it
		// carries no annotation and nothing should try to read one off it.
	}

	t.Columns = []*schema.Column{
		{
			Name:       "id",
			Comment:    "Surrogate key; ULID so rows sort by insertion order.",
			Type:       schema.TypeULID,
			NotNull:    true,
			PrimaryKey: true,
			Generated:  "ulid",
		},
		{
			Name:    "aggregate_id",
			Comment: "Primary key of the " + owner.LocalName + " row this event describes.",
			Type:    pkType,
			NotNull: true,
			// Indexed via t.Indexes below, not Column.Index: the latter reaches the
			// GORM struct tag and protokit's redundant-FK-index suppression, but no
			// target turns it into DDL, so an index declared that way would exist
			// for AutoMigrate users and silently not for anyone applying the SQL.
		},
		{
			Name:    "event_type",
			Comment: "What happened, e.g. \"" + owner.LocalName + ".created\".",
			Type:    schema.TypeString,
			NotNull: true,
		},
		{
			Name:    "payload",
			Comment: "The event body.",
			Type:    schema.TypeJSON,
			NotNull: true,
		},
		{
			Name:       "created_at",
			Comment:    "When the event was written.",
			Type:       schema.TypeTimestamp,
			NotNull:    true,
			Default:    "now()",
			AutoCreate: true,
		},
		{
			Name:     "published_at",
			Comment:  "When a publisher claimed the event; NULL while outstanding.",
			Type:     schema.TypeTimestamp,
			Optional: true,
		},
	}

	t.Indexes = []*schema.Index{
		// The publisher's hot query is "oldest unpublished rows". Without this the
		// drain is a full scan of a table which by design only grows between drains.
		{
			Name:    "idx_" + name + "_unpublished",
			Columns: []string{"published_at", "created_at"},
		},
		// Replaying or auditing the events for one row is the other query anyone
		// runs against an outbox.
		{
			Name:    "idx_" + name + "_aggregate",
			Columns: []string{"aggregate_id"},
		},
	}

	return t
}
