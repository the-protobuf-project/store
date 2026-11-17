// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

//! The generated repositories of library.v1, checked for what the generator is
//! responsible for: each descriptor names the table, columns, types,
//! constraints and foreign keys the sql target's DDL declares (so Go and Rust
//! share a database); the rules follow the field behaviors; `repositories()`
//! binds every resource; and the descriptors create their tables and
//! round-trip records on SQLite.

use std::collections::BTreeMap;
use std::sync::Arc;

use database::orm::OrmProvider;
use database::orm::sqlx::sqlite::SqlitePoolOptions;
use database::records::{DB, Generated, Provider, Resource, Typed};
use library_example::DESCRIPTOR_POOL;
use library_example::pb::library::v1 as pb;
use library_example::repository::library::v1 as repo;

const DDL: &str = include_str!("../../generated/sql/library/library_db/library_v1.postgres.sql");

/// One column as the DDL declares it.
#[derive(Clone, Debug, PartialEq)]
struct DdlColumn {
    name: String,
    sql_type: String,
    not_null: bool,
    primary_key: bool,
    unique: bool,
}

/// One table as the DDL declares it: its columns in order and its foreign keys
/// as (column, referenced table, referenced column).
#[derive(Debug, Default)]
struct DdlTable {
    columns: Vec<DdlColumn>,
    fks: Vec<(String, String, String)>,
}

fn unquote(s: &str) -> String {
    s.trim().trim_matches('"').to_owned()
}

/// The CREATE TABLE statements of the sql target's DDL, by schema.table.
fn ddl_tables() -> BTreeMap<String, DdlTable> {
    let mut out = BTreeMap::new();
    let mut current: Option<(String, DdlTable)> = None;
    for line in DDL.lines() {
        let line = line.trim();
        if let Some(rest) = line.strip_prefix("CREATE TABLE ") {
            let name = rest.trim_end_matches(" (").replace('"', "");
            current = Some((name, DdlTable::default()));
            continue;
        }
        let Some((_, table)) = current.as_mut() else {
            continue;
        };
        if line == ");" {
            let (name, table) = current.take().unwrap();
            out.insert(name, table);
            continue;
        }
        if line.starts_with("--") || line.is_empty() {
            continue;
        }
        if let Some(rest) = line.strip_prefix("CONSTRAINT ") {
            // CONSTRAINT "fk" FOREIGN KEY ("col") REFERENCES "schema"."table"("col") ...
            let col = rest
                .split("FOREIGN KEY (")
                .nth(1)
                .unwrap()
                .split(')')
                .next()
                .unwrap();
            let refs = rest.split("REFERENCES ").nth(1).unwrap();
            let (target, rest) = refs.split_once('(').unwrap();
            let target_col = rest.split(')').next().unwrap();
            let target = target.rsplit('.').next().unwrap();
            table
                .fks
                .push((unquote(col), unquote(target), unquote(target_col)));
            continue;
        }
        // "name"  TYPE  NOT NULL  PRIMARY KEY  UNIQUE  DEFAULT ...,
        let def = line.trim_end_matches(',');
        let parts: Vec<&str> = def.split("  ").collect();
        table.columns.push(DdlColumn {
            name: unquote(parts[0]),
            sql_type: parts[1].to_owned(),
            not_null: parts.contains(&"NOT NULL"),
            primary_key: parts.contains(&"PRIMARY KEY"),
            unique: parts.contains(&"UNIQUE"),
        });
    }
    out
}

fn resources() -> Vec<Resource> {
    let md = |name: &str| DESCRIPTOR_POOL.get_message_by_name(name).unwrap();
    vec![
        repo::author_resource(md("library.v1.Author")),
        repo::book_resource(md("library.v1.Book")),
        repo::publisher_resource(md("library.v1.Publisher")),
    ]
}

#[test]
fn descriptors_match_the_sql_ddl() {
    let tables = ddl_tables();
    let resources = resources();
    assert_eq!(tables.len(), resources.len(), "one table per resource");
    let names: BTreeMap<String, String> = resources
        .iter()
        .map(|r| (r.table.clone(), r.name.clone()))
        .collect();
    for res in &resources {
        let ddl = tables
            .get(&res.qualified_table())
            .unwrap_or_else(|| panic!("no DDL table {}", res.qualified_table()));
        let got: Vec<DdlColumn> = res
            .columns
            .iter()
            .map(|c| DdlColumn {
                name: c.name.clone(),
                sql_type: c.sql_type.clone(),
                not_null: c.not_null,
                primary_key: c.primary_key,
                unique: c.unique,
            })
            .collect();
        assert_eq!(got.len(), ddl.columns.len(), "{}: column count", res.table);
        for (g, d) in got.iter().zip(&ddl.columns) {
            let mut want = d.clone();
            // An enum is a Postgres enum type in the DDL; the IR has no
            // portable SQL type for it, so the descriptor leaves the dialect's.
            if want.sql_type.starts_with('"') {
                want.sql_type = String::new();
            }
            assert_eq!(g, &want, "{}.{}", res.table, d.name);
        }
        let fks: Vec<(String, String, String)> = res
            .fks
            .iter()
            .map(|fk| {
                let table = names
                    .iter()
                    .find(|(_, name)| **name == fk.referenced_name)
                    .map(|(table, _)| table.clone())
                    .unwrap_or_default();
                (fk.column.clone(), table, fk.referenced_field.clone())
            })
            .collect();
        assert_eq!(fks, ddl.fks, "{}: foreign keys", res.table);
        // The key the repository addresses records by is the resource name.
        assert_eq!(res.pk_column, "name");
        assert!(res.pk().unwrap().unique);
    }
}

#[test]
fn managed_columns_follow_the_ir() {
    let res = resources();
    let author = &res[0];
    let id = author.lookup_column("id").unwrap();
    assert_eq!(
        (id.field.as_str(), id.generated),
        ("", Some(Generated::Ulid))
    );
    assert!(author.lookup_column("created_at").unwrap().auto_create);
    assert!(author.lookup_column("updated_at").unwrap().auto_update);
    let publisher = &res[2];
    assert!(publisher.lookup_column("create_time").unwrap().auto_create);
    assert!(publisher.lookup_column("update_time").unwrap().auto_update);
    let book = &res[1];
    let parent = book.lookup_column("publisher_id").unwrap();
    assert!(parent.field.is_empty() && parent.not_null);
}

#[test]
fn rules_follow_field_behaviors() {
    let rules = repo::publisher_rules();
    assert_eq!(rules.etag.as_deref(), Some("etag"));
    assert_eq!(rules.delete_time.as_deref(), Some("delete_time"));
    assert_eq!(rules.expire_time.as_deref(), Some("expire_time"));
    assert_eq!(rules.retention, std::time::Duration::ZERO);
    assert_eq!(
        rules.output_only,
        [
            "etag",
            "create_time",
            "update_time",
            "delete_time",
            "expire_time"
        ]
    );
    let rules = repo::book_rules();
    assert_eq!(rules.etag.as_deref(), Some("etag"));
    assert_eq!(rules.delete_time, None);
    assert_eq!(rules.output_only, ["etag", "create_time"]);
    assert_eq!(repo::author_rules(), Default::default());
}

/// An in-memory SQLite database with the `library_v1` schema attached, as the
/// descriptors qualify their tables with it.
async fn sqlite() -> DB {
    let pool = SqlitePoolOptions::new()
        .max_connections(1)
        .idle_timeout(None)
        .max_lifetime(None)
        .connect("sqlite::memory:")
        .await
        .unwrap();
    database::orm::sqlx::query("ATTACH DATABASE ':memory:' AS library_v1")
        .execute(&pool)
        .await
        .unwrap();
    OrmProvider::new(pool).set_database("").await.unwrap()
}

#[tokio::test]
async fn repositories_bind_every_resource() {
    let db = sqlite().await;
    let repos = repo::repositories(&db, &DESCRIPTOR_POOL).unwrap();
    // Each is a dyn Repository of its prost message; binding validated the
    // descriptor and the rules against the message.
    let _: &Arc<dyn database::repository::Repository<pb::Publisher>> = &repos.publishers;
    let _: &Arc<dyn database::repository::Repository<pb::Author>> = &repos.authors;
    let _: &Arc<dyn database::repository::Repository<pb::Book>> = &repos.books;
}

#[tokio::test]
async fn descriptors_create_tables_and_round_trip() {
    let db = sqlite().await;
    for res in resources() {
        db.schema.ensure_schema(&res).await.unwrap();
    }
    let res = resources();

    let publishers = Typed::<pb::Publisher>::new(&db, Arc::new(res[2].clone())).unwrap();
    let publisher = pb::Publisher {
        name: "publishers/acme".into(),
        display_name: "Acme".into(),
        slug: "acme".into(),
        etag: "e1".into(),
        ..Default::default()
    };
    let stored = publishers.create(&publisher).await.unwrap();
    let back = publishers.get("publishers/acme").await.unwrap();
    assert_eq!(back.display_name, "Acme");
    assert_eq!(back.slug, "acme");
    assert_eq!(back.etag, "e1");
    assert!(
        back.create_time.is_some() && back.update_time.is_some(),
        "audit times filled"
    );
    assert!(back.delete_time.is_none());
    assert!(stored.message.get_field_by_name("name").is_some());
    // The unique slug refuses a second publisher with it.
    let dup = pb::Publisher {
        name: "publishers/other".into(),
        ..publisher.clone()
    };
    assert!(matches!(
        publishers.create(&dup).await,
        Err(database::Error::AlreadyExists)
    ));

    let authors = Typed::<pb::Author>::new(&db, Arc::new(res[0].clone())).unwrap();
    let author = pb::Author {
        name: "authors/le-guin".into(),
        display_name: "Ursula K. Le Guin".into(),
        bio: "Earthsea".into(),
    };
    authors.create(&author).await.unwrap();
    assert_eq!(authors.get("authors/le-guin").await.unwrap(), author);
}
