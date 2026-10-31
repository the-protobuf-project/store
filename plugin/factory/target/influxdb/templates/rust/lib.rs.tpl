{{.Header}}

//! Time-series stores generated from protobuf for the tpp-database runtime.
//!
//! One module per database, each selecting its InfluxDB 3 database or Arrow
//! capture directory, with a typed store per table; [`seriesx`] is the runtime
//! they share.

{{range .Mods}}pub mod {{.}};
{{end -}}
