{{define "package"}}

//! Repositories of the `{{.Package}}` resources over the tpp-database records
//! runtime: per resource its storage descriptor (`<resource>_resource`), its
//! rules (`<resource>_rules`), and its repository in [`Repositories`].
//!
//! The descriptors name the tables and columns the sql and gorm targets
//! render, so a Go and a Rust service share one database.
{{- if .Cache}}
//!
//! Some of these resources carry `(cache.v1.cache)`: protoc-gen-cache
//! (`lang=rust`) writes their caches into `cache.rs` beside this file, the
//! `cache` module below.
{{- end}}
//!
//! | Resource | Table | Key | Repository |
//! | --- | --- | --- | --- |
{{- range .Resources}}
//! | `{{.FullName}}` | `{{.Schema}}.{{.Table}}` | `{{.PK}}` | [`Repositories::{{.Field}}`] |
{{- end}}

{{range .Imports}}{{.}}
{{end}}
/// Every repository of `{{.Package}}`, one per resource.
pub struct Repositories {
{{- range .Resources}}
    /// `{{.FullName}}`, stored in `{{.Schema}}.{{.Table}}`.
    pub {{.Field}}: Arc<dyn Repository<{{.Type}}>>,
{{- end}}
}

/// Binds every resource of `{{.Package}}` over `db`, each described with its
/// message from `pool`, which must hold the package's descriptors. Retention
/// is zero: set it per resource with `<resource>_rules` and `RecordsRepository`
/// directly when soft-deleted records should expire.
pub fn repositories(db: &DB, pool: &DescriptorPool) -> database::Result<Repositories> {
    Ok(Repositories {
{{- range .Bind}}
{{.}}
{{- end}}
    })
}
{{range .Resources}}
/// The storage descriptor of `{{.FullName}}`: table `{{.Schema}}.{{.Table}}`,
/// keyed by its resource name, `{{.PK}}`. `message` is its descriptor.
pub fn {{.Snake}}_resource(message: MessageDescriptor) -> Resource {
    Resource {
        schema: {{printf "%q" .Schema}}.into(),
        columns: vec![
{{- range .Columns}}
{{.}}
{{- end}}
        ],
{{- if .FKs}}
{{.FKs}}
{{- end}}
{{.Base}}
    }
}

/// How `{{.FullName}}` behaves beyond plain storage, from its field behaviors.
/// Retention is zero; the caller sets it.
pub fn {{.Snake}}_rules() -> Rules {
    Rules {
{{- range .Rules}}
{{.}}
{{- end}}
        ..Rules::default()
    }
}
{{end}}
/// A column `name` of `kind`, carried by proto field `field`; an empty field is
/// a column the driver fills, or one the repository does.
fn column(name: &str, field: &str, kind: Kind, sql_type: &str) -> Column {
    Column {
        name: name.into(),
        field: field.into(),
        kind,
        sql_type: sql_type.into(),
        ..Column::default()
    }
}
{{- if .HasFKs}}

/// A foreign key from `column` to `referenced_field` of resource `referenced_name`.
fn foreign_key(column: &str, referenced_name: &str, referenced_field: &str) -> ForeignKey {
    ForeignKey {
        column: column.into(),
        referenced_name: referenced_name.into(),
        referenced_field: referenced_field.into(),
    }
}
{{- end}}
{{- if .HasRefs}}

/// A reference field stored as the bare id of a resource of `pattern`.
fn reference(field: &str, pattern: &str) -> Reference {
    Reference {
        field: field.into(),
        pattern: pattern.into(),
    }
}
{{- end}}

/// `M`'s repository over `db`, described by `resource` from message `name` of
/// `pool`.
fn bind<M>(
    db: &DB,
    pool: &DescriptorPool,
    name: &str,
    resource: fn(MessageDescriptor) -> Resource,
    rules: Rules,
) -> database::Result<Arc<dyn Repository<M>>>
where
    M: ReflectMessage + Message + Default + Send + Sync + 'static,
{
    let message = pool.get_message_by_name(name).ok_or_else(|| {
        database::Error::Invalid(format!("the descriptor pool has no message {name}"))
    })?;
    let repository = RecordsRepository::<M>::new(db, Arc::new(resource(message)), rules)?;
    Ok(Arc::new(repository))
}
{{- end}}
