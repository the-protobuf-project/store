# Examples

Everything generated lives under [`generated/`](generated), organized as
`<language>/<target>/<example>`. Protos, configs and the code that runs the
examples are hand-written and live beside it. Regenerate everything with
`just examples`; never edit `generated/` by hand.

| Language | Target | Example | Source | Run |
| --- | --- | --- | --- | --- |
| Go | [gorm](generated/go/gorm) | bookstore, search, references | [`proto/bookstore`](proto/bookstore), [`search/proto`](search/proto), [`references/proto`](references/proto) | `just build-examples` |
| Go | [graphql](generated/go/graphql) | bookstore | [`graphql/`](graphql) | `just build-examples` |
| Go | [repository](generated/go/repository) | search, references | as above | `just test-search`, `just test-references` |
| Go | [opensearch](generated/go/opensearch) | search | [`search/proto`](search/proto) | `just test-search` |
| Go | [protobuf](generated/go/protobuf) | search, references | as above | stubs the above import |
| Rust | [influxdb](generated/rust/influxdb) | sensors (InfluxDB 3, Arrow) | [`proto/sensors`](proto/sensors) | `just test-sensors` |
| SQL | [sql](generated/sql) | bookstore, search, references | as above | `psql -f <db>/migrate.sql` |
| TypeScript | [prisma](generated/typescript/prisma) | bookstore | [`proto/bookstore`](proto/bookstore) | `npx prisma validate` |

A new target takes the next folder: an ORM for Rust lands at
`generated/rust/<orm>/<example>`, beside `influxdb`, and moves nothing else.

`just test-examples` runs them all. Without these, the search and references
tests skip; they need throwaway instances, since they create and drop schemas:

```bash
export STORE_TEST_POSTGRES_DSN="host=localhost port=55432 user=postgres dbname=postgres sslmode=disable"
export STORE_TEST_OPENSEARCH_URL=http://localhost:19201
```

Set `INFLUX_HOST`, `INFLUX_TOKEN` and `INFLUX_DATABASE` to run the sensors
example against InfluxDB 3 as well as Arrow captures.
