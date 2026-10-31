{{.Header}}

//! Models of the `{{.Schema}}` schema: one struct per table, with its descriptor
//! and the conversions to and from stored records.

use crate::seriesx::decode;
use database::store::{Column, Kind, Model, Record, Resource, Role};
use std::sync::LazyLock;
{{range .Enums}}
{{- if .Comment}}
/// {{.Comment}}
///
{{- end}}
/// The `{{.Proto}}` enum. Fields store its number; convert with `TryFrom`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
#[repr(i32)]
pub enum {{.Name}} {
{{- range $i, $v := .Variants}}
{{- if $v.Comment}}
    /// {{$v.Comment}}
{{- end}}
{{- if eq $i 0}}
    #[default]
{{- end}}
    {{$v.Name}} = {{$v.Number}},
{{- end}}
}

impl TryFrom<i32> for {{.Name}} {
    type Error = i32;

    fn try_from(number: i32) -> Result<Self, i32> {
        match number {
{{- range .Variants}}
            {{.Number}} => Ok(Self::{{.Name}}),
{{- end}}
            other => Err(other),
        }
    }
}

impl From<{{.Name}}> for i32 {
    fn from(value: {{.Name}}) -> i32 {
        value as i32
    }
}
{{end}}
{{- range .Models}}
{{- if .Comment}}
/// {{.Comment}}
///
{{- end}}
/// A point of the `{{.Table}}` table, from `{{.Proto}}`; stored through
/// [`{{.Store}}`](super::{{.Store}}).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct {{.Name}} {
{{- range .Fields}}
{{- if .Comment}}
    /// {{.Comment}}
{{- end}}
    pub {{.Ident}}: {{.Type}},
{{- end}}
}

static {{.Static}}: LazyLock<Resource> = LazyLock::new(|| {
    Resource::new(
        "{{.Table}}",
        vec![
{{- range .Fields}}
            Column::new("{{.Column}}", Kind::{{.Kind}}, Role::{{.Role}}),
{{- end}}
        ],
    )
    .expect("generated descriptor is valid")
    .with_name("{{.Name}}")
});

impl Model for {{.Name}} {
    fn resource() -> &'static Resource {
        &{{.Static}}
    }

    fn to_record(&self) -> database::Result<Record> {
        Ok(Record::from([
{{- range .Fields}}
            ("{{.Column}}".into(), {{.Encode}}),
{{- end}}
        ]))
    }

    fn from_record(mut record: Record) -> database::Result<Self> {
        Ok(Self {
{{- range .Fields}}
            {{.Ident}}: {{.Decode}},
{{- end}}
        })
    }
}
{{end -}}
