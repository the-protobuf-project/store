{{.Header}}
{{- $m := .Model}}

//! The store of the `{{$m.Table}}` table.

use super::models::{{$m.Name}};
use crate::seriesx::{{if $m.Numbers}}{self, Column, Query}{{else if $m.Tags}}{Column, Query}{{else}}Query{{end}};
use database::store::{DB, For, ListOptions, {{if $m.Numbers}}Reducer, Reduction, {{end}}Result, WriteResult};

/// Typed access to the `{{$m.Table}}` table: writes [`{{$m.Name}}`] points
/// and queries them by time and tag. Cheap to clone; the database is shared.
#[derive(Clone)]
pub struct {{$m.Store}} {
    rows: For<{{$m.Name}}>,
}

impl {{$m.Store}} {
    /// The table the points are stored in: the InfluxDB table, and the Arrow
    /// capture directory under the database.
    pub const TABLE: &'static str = "{{$m.Table}}";

    /// Binds the table on `db`, as the database module's `connect_influx`,
    /// `open_arrow` or `mirror` selects it.
    pub fn new(db: &DB) -> Self {
        Self { rows: For::new(db) }
    }

    /// The database the store writes to.
    pub fn db(&self) -> &DB {
        self.rows.db()
    }

    /// Writes one point. A point with the same tags and time replaces it.
    pub async fn create(&self, point: &{{$m.Name}}) -> Result<WriteResult> {
        self.rows.create(point).await
    }

    /// Writes every point in one request.
    pub async fn create_many(&self, points: &[{{$m.Name}}]) -> Result<Vec<WriteResult>> {
        self.rows.create_many(points).await
    }

    /// Starts a query over every point; narrow it with `between`, the tag
    /// methods and `filter`, then `fetch`, `all`, `latest`, `count` or
    /// `aggregate`.
    pub fn query(&self) -> Query<{{$m.Name}}> {
        Query::new(self.rows.clone())
    }

    /// One page by `opts`, its next token and the total.
    pub async fn list(&self, opts: &ListOptions) -> Result<(Vec<{{$m.Name}}>, String, i64)> {
        self.rows.list(opts).await
    }

    /// How many points match `opts.filter`.
    pub async fn count(&self, opts: &ListOptions) -> Result<i64> {
        self.rows.count(opts).await
    }

    /// Removes the table and every point in it.
    pub async fn drop_table(&self) -> Result<()> {
        self.db().schema.drop_schema(self.rows.resource()).await
    }
}
{{- if $m.Tags}}

/// The tag filters of a `{{$m.Table}}` query, one per tag.
impl Query<{{$m.Name}}> {
{{- range $i, $t := $m.Tags}}
{{- if $i}}
{{end}}
    /// Points whose `{{$t.Column}}` tag is `value`.
    pub fn {{$t.Method}}(self, value: &str) -> Self {
        self.eq({{$m.TagEnum}}::{{$t.Variant}}, value)
    }
{{- end}}
}

/// The tags of `{{$m.Table}}`: its series identity, filtered by
/// [`Query::eq`] and grouped by [`Query::group_by`].
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum {{$m.TagEnum}} {
{{- range $m.Tags}}
{{- if .Comment}}
    /// {{.Comment}}
{{- end}}
    {{.Variant}},
{{- end}}
}

impl Column for {{$m.TagEnum}} {
    fn column(self) -> &'static str {
        match self {
{{- range $m.Tags}}
            Self::{{.Variant}} => "{{.Column}}",
{{- end}}
        }
    }
}
{{- end}}
{{- if $m.Numbers}}

/// The numeric fields of `{{$m.Table}}`, reduced by [`Query::aggregate`].
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum {{$m.FieldEnum}} {
{{- range $m.Numbers}}
{{- if .Comment}}
    /// {{.Comment}}
{{- end}}
    {{.Variant}},
{{- end}}
}

impl Column for {{$m.FieldEnum}} {
    fn column(self) -> &'static str {
        match self {
{{- range $m.Numbers}}
            Self::{{.Variant}} => "{{.Column}}",
{{- end}}
        }
    }
}

impl {{$m.FieldEnum}} {
    /// The mean over each bucket, as `avg_<field>`.
    pub fn avg(self) -> Reduction {
        seriesx::reduce(Reducer::Avg, self)
    }

    /// The minimum, as `min_<field>`.
    pub fn min(self) -> Reduction {
        seriesx::reduce(Reducer::Min, self)
    }

    /// The maximum, as `max_<field>`.
    pub fn max(self) -> Reduction {
        seriesx::reduce(Reducer::Max, self)
    }

    /// The sum, as `sum_<field>`.
    pub fn sum(self) -> Reduction {
        seriesx::reduce(Reducer::Sum, self)
    }

    /// The number of values, as `count_<field>`.
    pub fn count(self) -> Reduction {
        seriesx::reduce(Reducer::Count, self)
    }
}
{{- end}}
