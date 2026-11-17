// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

//! The generated repositories of library.v1, checked for what the generator is
//! responsible for: each descriptor names the table, columns, types,
//! constraints and foreign keys the sql target's DDL declares (so Go and Rust
//! share a database); the rules follow the field behaviors; `repositories()`
//! binds every resource; and the descriptors create their tables and
//! round-trip records on SQLite.

use std::collections::BTreeMap;

use database::orm::OrmProvider;
use database::orm::sqlx::Row;
use database::orm::sqlx::sqlite::SqlitePool;
use database::orm::sqlx::sqlite::SqlitePoolOptions;
use database::records::{DB, Generated, Kind, Provider, Resource};
use database::repository::{ListInput, Reference};
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
            assert_eq!(g, d, "{}.{}", res.table, d.name);
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
    assert_eq!(rules.pattern, "publishers/{publisher}/books/{book}");
    assert_eq!(rules.parent_column.as_deref(), Some("publisher_id"));
    assert_eq!(
        rules.references,
        [Reference {
            field: "author".into(),
            pattern: "authors/{author}".into()
        }]
    );
    let rules = repo::author_rules();
    assert_eq!(rules.pattern, "authors/{author}");
    assert_eq!(rules.parent_column, None);
    assert!(rules.references.is_empty() && rules.etag.is_none());
}

#[test]
fn search_lists_the_text_columns_opted_in() {
    // As Go's filterx.Spec.Search: (store.v1.query).search on a text column.
    // Book.pages opts in too but is no text, so it is left out.
    let res = resources();
    assert_eq!(res[0].search, ["bio"]);
    assert_eq!(res[1].search, ["title"]);
    assert_eq!(res[2].search, ["display_name"]);
}

#[test]
fn enums_are_stored_by_label_in_the_ddl_enum_type() {
    let book = &resources()[1];
    let format = book.lookup_column("format").unwrap();
    assert_eq!(format.kind, Kind::String);
    assert_eq!(format.sql_type, r#""library_v1"."format""#);
    // The labels the DDL's CREATE TYPE lists, in number order.
    let create_type = DDL
        .lines()
        .find(|l| l.starts_with(r#"CREATE TYPE "library_v1"."format""#))
        .unwrap();
    let labels: Vec<String> = format
        .labels
        .iter()
        .map(|(_, l)| format!("'{l}'"))
        .collect();
    assert!(create_type.ends_with(&format!("({});", labels.join(", "))));
    assert_eq!(
        format.labels,
        [
            (pb::Format::Hardcover as i32, "HARDCOVER".to_owned()),
            (pb::Format::Paperback as i32, "PAPERBACK".to_owned()),
            (pb::Format::Ebook as i32, "EBOOK".to_owned()),
        ]
    );
}

/// An in-memory SQLite database with the `library_v1` schema attached, as the
/// descriptors qualify their tables with it, and every table created. The pool
/// is returned too, to read rows as stored.
async fn sqlite() -> (DB, SqlitePool) {
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
    let db = OrmProvider::new(pool.clone())
        .set_database("")
        .await
        .unwrap();
    for mut res in resources() {
        // An enum's sql_type names its Postgres type, which SQLite's CREATE
        // TABLE cannot parse: the table is created with the dialect's type.
        // The repositories below use the generated descriptors unchanged.
        for c in &mut res.columns {
            if !c.labels.is_empty() {
                c.sql_type.clear();
            }
        }
        db.schema.ensure_schema(&res).await.unwrap();
    }
    (db, pool)
}

/// One stored row's text columns, as Go's GORM would read them.
async fn row(pool: &SqlitePool, table: &str, name: &str, cols: &[&str]) -> Vec<Option<String>> {
    let sql = format!(
        "SELECT {} FROM library_v1.{table} WHERE name = ?",
        cols.join(", ")
    );
    let r = database::orm::sqlx::query(database::orm::sqlx::AssertSqlSafe(sql))
        .bind(name)
        .fetch_one(pool)
        .await
        .unwrap();
    cols.iter().map(|c| r.try_get(*c).unwrap()).collect()
}

fn last_segment(name: &str) -> &str {
    name.rsplit('/').next().unwrap()
}

#[tokio::test]
async fn repositories_round_trip_as_go_stores() {
    let (db, pool) = sqlite().await;
    let repos = repo::repositories(&db, &DESCRIPTOR_POOL).unwrap();

    // A root resource created without a name gets publishers/<ULID>, and its
    // row's id is that id, as Go's Create sets it.
    let acme = repos
        .publishers
        .create(&pb::Publisher {
            display_name: "Acme".into(),
            slug: "acme".into(),
            ..Default::default()
        })
        .await
        .unwrap();
    assert!(acme.name.starts_with("publishers/"));
    assert_eq!(last_segment(&acme.name).len(), 26, "a ULID id");
    assert!(!acme.etag.is_empty() && acme.create_time.is_some());
    let stored = row(&pool, "publishers", &acme.name, &["id", "etag"]).await;
    assert_eq!(stored[0].as_deref(), Some(last_segment(&acme.name)));
    assert_eq!(stored[1].as_deref(), Some(acme.etag.as_str()));
    let other = repos
        .publishers
        .create(&pb::Publisher {
            name: "publishers/other".into(),
            display_name: "Other".into(),
            ..Default::default()
        })
        .await
        .unwrap();
    assert_eq!(other.name, "publishers/other");

    let author = repos
        .authors
        .create(&pb::Author {
            name: "authors/le-guin".into(),
            display_name: "Ursula K. Le Guin".into(),
            bio: "Earthsea".into(),
        })
        .await
        .unwrap();

    // A nested resource named <parent>/books/ gets a minted id.
    let draft = pb::Book {
        name: format!("{}/books/", acme.name),
        title: "A Wizard of Earthsea".into(),
        author: author.name.clone(),
        format: pb::Format::Ebook as i32,
        pages: 183,
        copies_sold: 1_000_000,
        price: 9.99,
        in_print: true,
        cover_digest: vec![1, 2, 3],
        publish_time: Some(prost_types::Timestamp {
            seconds: -63_158_400,
            nanos: 0,
        }),
        ..Default::default()
    };
    let book = repos.books.create(&draft).await.unwrap();
    assert!(book.name.starts_with(&format!("{}/books/", acme.name)));
    assert_eq!(last_segment(&book.name).len(), 26);
    assert_eq!(book.author, "authors/le-guin", "read back as the name");
    assert_eq!(book.format, pb::Format::Ebook as i32);
    assert_eq!(
        (book.pages, book.copies_sold, book.in_print),
        (183, 1_000_000, true)
    );
    assert_eq!(book.cover_digest, [1, 2, 3]);
    assert_eq!(book.publish_time, draft.publish_time);
    assert!(!book.etag.is_empty() && book.create_time.is_some());
    assert_eq!(repos.books.get(&book.name).await.unwrap(), book);

    // The row as Go writes it: a fresh surrogate id (a nested id is unique only
    // under its parent), the full name, the parent's id, the reference as the
    // author's bare id, and the enum as its label.
    let stored = row(
        &pool,
        "books",
        &book.name,
        &["id", "name", "publisher_id", "author", "format", "etag"],
    )
    .await;
    let id = stored[0].clone().unwrap();
    assert_eq!(id.len(), 26);
    assert_ne!(
        id,
        last_segment(&book.name),
        "a nested row keys by a surrogate"
    );
    assert_eq!(stored[1].as_deref(), Some(book.name.as_str()));
    assert_eq!(stored[2].as_deref(), Some(last_segment(&acme.name)));
    assert_eq!(stored[3].as_deref(), Some("le-guin"));
    assert_eq!(stored[4].as_deref(), Some("EBOOK"));
    assert_eq!(stored[5].as_deref(), Some(book.etag.as_str()));

    // Update with the etag: the masked field changes and a new etag is minted;
    // the stale etag then conflicts.
    let edit = pb::Book {
        title: "A Wizard of Earthsea (50th anniversary)".into(),
        ..book.clone()
    };
    let updated = repos.books.update(&edit, &["title".into()]).await.unwrap();
    assert_eq!(updated.title, edit.title);
    assert_ne!(updated.etag, book.etag);
    assert_eq!(updated.format, book.format);
    let stale = repos.books.update(&edit, &["title".into()]).await;
    assert!(matches!(stale, Err(database::Error::Conflict)), "{stale:?}");

    // Parent-scoped lists: two books under acme, one under other.
    for (parent, title) in [
        (&acme.name, "The Tombs of Atuan"),
        (&other.name, "Other Book"),
    ] {
        repos
            .books
            .create(&pb::Book {
                name: format!("{parent}/books/"),
                title: title.into(),
                format: pb::Format::Paperback as i32,
                ..Default::default()
            })
            .await
            .unwrap();
    }
    let list = |parent: &str| ListInput {
        parent: parent.into(),
        ..Default::default()
    };
    let acme_books = repos.books.list(&list(&acme.name)).await.unwrap();
    assert_eq!(acme_books.items.len(), 2);
    assert!(
        acme_books
            .items
            .iter()
            .all(|b| b.name.starts_with(&acme.name))
    );
    assert_eq!(
        repos
            .books
            .list(&list(&other.name))
            .await
            .unwrap()
            .items
            .len(),
        1
    );
    assert_eq!(repos.books.count(&list("publishers/-")).await.unwrap(), 3);
    assert_eq!(repos.books.count(&list("")).await.unwrap(), 3);

    // A bareword filter term searches the search columns (Book.title).
    let found = repos
        .books
        .list(&ListInput {
            filter: "Tombs".into(),
            ..Default::default()
        })
        .await
        .unwrap();
    let titles: Vec<&str> = found.items.iter().map(|b| b.title.as_str()).collect();
    assert_eq!(titles, ["The Tombs of Atuan"]);

    // Soft delete (Publisher has delete_time): marked and restored by
    // undelete; soft_deleted_records_leave_lists covers lists.
    repos
        .publishers
        .delete(&other.name, Some(&other.etag))
        .await
        .unwrap();
    let deleted = repos.publishers.get(&other.name).await.unwrap();
    assert!(deleted.delete_time.is_some());
    let stored = row(&pool, "publishers", &other.name, &["delete_time"]).await;
    assert!(stored[0].is_some(), "delete_time stored");
    let restored = repos.publishers.undelete(&other.name, None).await.unwrap();
    assert!(restored.delete_time.is_none());
    let stored = row(&pool, "publishers", &other.name, &["delete_time"]).await;
    assert!(stored[0].is_none(), "delete_time cleared");

    // Book has no delete_time: delete removes it.
    repos.books.delete(&book.name, None).await.unwrap();
    assert!(matches!(
        repos.books.get(&book.name).await,
        Err(database::Error::NotFound)
    ));
}

/// Lists and counts leave soft-deleted records out unless `show_deleted`.
#[tokio::test]
#[ignore = "runtime-rs feat/repository-impl ee40397: the soft-delete clause \
            `delete_time = null` is refused by the AIP-160 filter reader \
            (\"null\" is not a timestamp)"]
async fn soft_deleted_records_leave_lists() {
    let (db, _) = sqlite().await;
    let repos = repo::repositories(&db, &DESCRIPTOR_POOL).unwrap();
    let mut created = Vec::new();
    for name in ["publishers/a", "publishers/b"] {
        let p = pb::Publisher {
            name: name.into(),
            display_name: name.into(),
            slug: name.into(),
            ..Default::default()
        };
        created.push(repos.publishers.create(&p).await.unwrap());
    }
    repos
        .publishers
        .delete(&created[1].name, None)
        .await
        .unwrap();
    let live = ListInput::default();
    let all = ListInput {
        show_deleted: true,
        ..Default::default()
    };
    assert_eq!(repos.publishers.count(&live).await.unwrap(), 1);
    assert_eq!(repos.publishers.count(&all).await.unwrap(), 2);
    assert_eq!(repos.publishers.list(&live).await.unwrap().items.len(), 1);
    repos
        .publishers
        .undelete(&created[1].name, None)
        .await
        .unwrap();
    assert_eq!(repos.publishers.count(&live).await.unwrap(), 2);
}
