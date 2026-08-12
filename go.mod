module github.com/the-protobuf-project/store

go 1.26.4

require (
	github.com/the-protobuf-project/protokit v1.3.0
	github.com/the-protobuf-project/store/entity v0.0.0
	google.golang.org/genproto/googleapis/api v0.0.0-20260720211330-0afa2a65878a
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

require (
	github.com/agnivade/levenshtein v1.2.1 // indirect
	github.com/bufbuild/protocompile v0.14.1 // indirect
	github.com/the-protobuf-project/opentelementry/opentelementry-go v0.0.0-20260722091843-d33763c88e10
	github.com/vektah/gqlparser/v2 v2.5.36 // indirect
	golang.org/x/sync v0.21.0 // indirect
)

// entity/ is a nested module in this repository: the neutral entity.v1 vocabulary
// and its reader, kept separate so a plugin that wants only the shared names does
// not inherit this module's dependency graph. The replace points at the directory
// until it is tagged on its own.
replace github.com/the-protobuf-project/store/entity => ./entity

// protokit's removal of protokit.v1 — which is what moved entity.v1 here — is
// unreleased. Drop this once it is tagged.
replace github.com/the-protobuf-project/protokit => ../protokit
