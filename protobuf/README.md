# store annotations

Protobuf custom options that let [**store**](https://github.com/the-protobuf-project/store)
turn your service definitions into production database schemas. Annotate your
messages with the [Google AIP](https://google.aip.dev/) standards you already use
(`google.api.resource`, `field_behavior`, `resource_reference`); reach for these
options only for the ~20% AIP can't express (explicit types, indexes, id
strategy, referential actions, …).

This module ships **only the option definitions**. Code generation is done by the
`protoc-gen-store` plugin — see the [main repo](https://github.com/the-protobuf-project/store)
for installing the plugin and generating Prisma / GORM / SQL output.

## Two modules, and why

Annotations are split across two proto modules, and which one owns an option is
not an accident of history:

- **`entity.v1`** — *structure*: what a thing is **named**, and whether it
  exists at all. It ships from this repository as
  [`buf.build/the-protobuf-project/entity`](https://buf.build/the-protobuf-project/entity),
  alongside the Go reader every generator imports
  (`github.com/the-protobuf-project/store/entity`). A store generator, a cache
  generator, and a docs generator all derive the *same* databases, schemas,
  tables, and columns from the same protos, because they all run that same
  reader. That agreement is what lets them compose.
- **`store.v1`** — *storage*: how a column is physically stored and queried. That
  is this plugin's business, and no other generator needs to agree with it.

The practical rule: if two generators would have to agree on it, it belongs in
`entity.v1`.

> `entity.v1` shipped inside protokit as `protokit.v1` through protokit v1.3.0.
> It moved because the vocabulary is persistence-shaped — datasource, table,
> column — and protokit is not a persistence engine; a chain generator has no
> tables. protokit now reads AIP and nothing else. Migrating is the import line
> and the option prefix: field numbers and semantics are unchanged.

---

## Install

Add both modules to your `buf.yaml` `deps` (with [buf](https://buf.build)):

```yaml
# buf.yaml
version: v2
deps:
  - buf.build/the-protobuf-project/entity
  - buf.build/the-protobuf-project/store
```

Run `buf dep update`, then import the two entrypoints in your protos:

```proto
import "entity/v1/annotations.proto";  // structure
import "store/v1/annotations.proto";     // storage
```

---

## Quick start

```proto
syntax = "proto3";
package bookstore.v1;

import "google/api/field_behavior.proto";
import "google/api/resource.proto";
import "entity/v1/annotations.proto";
import "store/v1/annotations.proto";

// File-level: name the database all messages in this file map to.
option (entity.v1.datasource) = {
  database: "bookstore_db"
  provider: "postgres"
};

message Author {
  option (google.api.resource) = {
    type: "bookstore.v1/Author"
    plural: "authors"
  };
  // Message-level: synthesize a ULID primary key + created_at/updated_at.
  option (entity.v1.table) = {id: ID_STRATEGY_ULID, timestamps: true};

  // IDENTIFIER → the AIP resource name; becomes a UNIQUE lookup column.
  string name = 1 [(google.api.field_behavior) = IDENTIFIER];

  // REQUIRED → NOT NULL.
  string display_name = 2 [(google.api.field_behavior) = REQUIRED];

  // Field-level: override the default VARCHAR(255) with unbounded TEXT.
  string bio = 3 [(store.v1.column) = {type: "TEXT"}];
}
```

---

## Options reference

### `(entity.v1.datasource)` — file level

Configures the database every message in the file maps to. Files that declare the
same `database` merge into one schema tree.

| Field | Description |
| --- | --- |
| `database` | Database name. Defaults to the last proto package segment. |
| `schema` | Override the schema namespace for every table in the file. |
| `url` | Connection URL, documented in generated config/DDL. |
| `provider` | `postgres` (default) or `mongodb`. |

### `(entity.v1.table)` — message level

Overrides table-level structure for a `google.api.resource` message.

| Field | Description |
| --- | --- |
| `table` | Explicit table name. Defaults to the snake_case plural of the resource. |
| `skip` | Exclude the message from all output. |
| `indexes` | Composite indexes: `{ columns: [...], unique: bool, index: "..." }`. |
| `id` | `ID_STRATEGY_ULID` / `ID_STRATEGY_UUID` — synthesize a generated `id` PK and demote the `IDENTIFIER` field to `UNIQUE`. |
| `timestamps` | Add `created_at` / `updated_at` columns. |

### `(entity.v1.column)` — field level

| Field | Description |
| --- | --- |
| `column` | Explicit column name (defaults to the proto field name). |
| `skip` | Field exists in the proto contract but not the database. |

### `(store.v1.column)` — field level

| Field | Description |
| --- | --- |
| `type` | Explicit SQL type — escape hatch; prefer the sizing options below. |
| `max_length` | `VARCHAR(n)` instead of the `VARCHAR(255)` default. |
| `precision` / `scale` | `NUMERIC(p, s)` for numeric fields. |
| `default_value` | SQL default expression, written verbatim. |
| `unique` / `index` | Single-column constraint / index. |
| `on_delete` / `on_update` | FK referential action (`REFERENTIAL_ACTION_CASCADE`, `…_SET_NULL`, …) for a `resource_reference` field. |

### `(store.v1.table)` — message level

| Field | Description |
| --- | --- |
| `outbox` | Emit a companion `<table>_outbox` table — `id`, `aggregate_id`, `event_type`, `payload`, `created_at`, `published_at` — so a change event can be written in the same transaction as the row it describes. The table only; draining it belongs to a streams generator. Off by default. |

### `(store.v1.query)` — field level

Tunes the field's generated AIP-160 filter / AIP-132 `order_by` surface,
separately from the physical column options.

| Field | Description |
| --- | --- |
| `filterable` | Override the type-derived default in the filter spec. Presence matters: `filterable: false` removes the field; unset keeps the default. |
| `sortable` | Override the type-derived default in the `order_by` allowlist. Presence matters, as with `filterable`. |
| `search` | Include the column in bareword free-text search. Off by default. |

> Nested message fields are always relationalized into their own child table with
> a primary key + foreign key — there is no JSON-inlining option. `map` fields and
> `google.*` well-known types map to `JSONB`.

---

## Versioning

The packages are `entity.v1` and `store.v1`, both shipped from this repository but
versioned as separate BSR modules — `entity.v1` changes when the neutral
vocabulary does, which is far less often than this plugin changes. Option field
numbers live in the `50000`–`99999` range reserved for non-Google custom options:
`store.v1` uses `50010`–`50012`, clear of `entity.v1`'s `51000`–`51002`, so a
file may carry both. `entity.v1` kept the numbers `entity.v1` used, so a proto's
option *values* survive that migration untouched.

**`orm.v1` was removed in v2.** The pre-split vocabulary said everything at once —
what a table was called *and* how its columns were stored — which is why no other
generator could read it. See the migration table in the
[main repository](https://github.com/the-protobuf-project/store) for the
option-by-option mapping; field semantics are unchanged, so migrating is a rename.
