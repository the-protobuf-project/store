{{.Header}}

//! Repositories generated from protobuf for the tpp-database records runtime.
//!
//! One module per proto package{{range $i, $p := .Packages}}{{if $i}},{{else}}:{{end}} `{{$p}}`{{end}}. Each binds its
//! resources' `database::repository::Repository` over any records provider
//! (SQL, MongoDB, Redis, a Tauri store) with `repositories(db, pool)`, naming
//! the prost messages through `{{.ProtoModule}}`.

{{range .Mods}}pub mod {{.}};
{{end -}}
