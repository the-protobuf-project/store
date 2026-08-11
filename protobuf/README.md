# orm annotations

Protobuf custom options that let [**orm**](https://github.com/the-protobuf-project/store)
turn your service definitions into production database schemas. Annotate your
messages with the [Google AIP](https://google.aip.dev/) standards you already use
(`google.api.resource`, `field_behavior`, `resource_reference`); reach for these
`orm.v1.*` options only for the ~20% AIP can't express (explicit types,
indexes, id strategy, referential actions, …).

This module ships **only the option definitions**. Code generation is done by the
`protoc-gen-store` plugin — see the [main repo](https://github.com/the-protobuf-project/store)
for installing the plugin and generating Prisma / GORM / SQL output.

---

## Install

Add the module to your `buf.yaml` `deps` (with [buf](https://buf.build)):

```yaml
# buf.yaml
version: v2
deps:
  - buf.build/the-protobuf-project/store
```

Run `buf dep update`, then import the single entrypoint in your protos:

```proto
import "orm/v1/annotations.proto";
```

---

## Quick start

```proto
syntax = "proto3";
package bookstore.v1;

import "google/api/field_behavior.proto";
import "google/api/resource.proto";
import "orm/v1/annotations.proto";

// File-level: name the database all messages in this file map to.
option (orm.v1.datasource) = {
  database: "bookstore_db"
  provider: "postgres"
};

message Author {
  option (google.api.resource) = {
    type: "bookstore.v1/Author"
    plural: "authors"
  };
  // Message-level: synthesize a ULID primary key + created_at/updated_at.
  option (orm.v1.table) = {id: ID_STRATEGY_ULID, timestamps: true};

  // IDENTIFIER → the AIP resource name; becomes a UNIQUE lookup column.
  string name = 1 [(google.api.field_behavior) = IDENTIFIER];

  // REQUIRED → NOT NULL.
  string display_name = 2 [(google.api.field_behavior) = REQUIRED];

  // Field-level: override the default VARCHAR(255) with unbounded TEXT.
  string bio = 3 [(orm.v1.column) = {type: "TEXT"}];
}
```

---

## Options reference

### `(orm.v1.datasource)` — file level

Configures the database every message in the file maps to. Files that declare the
same `database` merge into one schema tree.

| Field | Description |
| --- | --- |
| `database` | Database name. Defaults to the last proto package segment. |
| `schema` | Override the schema namespace for every table in the file. |
| `url` | Connection URL, documented in generated config/DDL. |
| `provider` | `postgres` (default) or `mongodb`. |

### `(orm.v1.table)` — message level

Overrides table-level generation for a `google.api.resource` message.

| Field | Description |
| --- | --- |
| `table` | Explicit table name. Defaults to the snake_case plural of the resource. |
| `skip` | Exclude the message from all output. |
| `indexes` | Composite indexes: `{ columns: [...], unique: bool, index: "..." }`. |
| `id` | `ID_STRATEGY_ULID` / `ID_STRATEGY_UUID` — synthesize a generated `id` PK and demote the `IDENTIFIER` field to `UNIQUE`. |
| `timestamps` | Add `created_at` / `updated_at` columns. |

### `(orm.v1.column)` — field level

Overrides column-level generation for a single field.

| Field | Description |
| --- | --- |
| `column` | Explicit column name (defaults to the proto field name). |
| `type` | Explicit SQL type — escape hatch; prefer the sizing options below. |
| `max_length` | `VARCHAR(n)` instead of the `VARCHAR(255)` default. |
| `precision` / `scale` | `NUMERIC(p, s)` for numeric fields. |
| `default_value` | SQL default expression, written verbatim. |
| `unique` / `index` | Single-column constraint / index. |
| `skip` | Field exists in the proto contract but not the database. |
| `on_delete` / `on_update` | FK referential action (`REFERENTIAL_ACTION_CASCADE`, `…_SET_NULL`, …) for a `resource_reference` field. |

> Nested message fields are always relationalized into their own child table with
> a primary key + foreign key — there is no JSON-inlining option. `map` fields and
> `google.*` well-known types map to `JSONB`.

---

## Versioning

The package is `orm.v1`. Option field numbers live in the `50000`–`99999`
range reserved for non-Google custom options. See the
[main repository](https://github.com/the-protobuf-project/store) for the full
option surface, defaults applied automatically, and the type-mapping table.

## License

[Apache License 2.0](https://github.com/the-protobuf-project/store/blob/main/LICENSE).
