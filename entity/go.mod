// entity is a nested module on purpose.
//
// It holds the neutral schema vocabulary (entity.v1) and the reader over it —
// what every protokit plugin must share to agree on database, schema, table, and
// column names. A cache generator or a streams generator needs exactly this and
// nothing else, so it must be possible to depend on it without pulling in gorm,
// prisma, graphql, or any other part of the store plugin.
//
// That is the whole constraint, and it is checked rather than remembered:
//
//	cd entity && go build ./...
//
// resolves with no dependency on github.com/the-protobuf-project/store. If a
// require line for the parent module ever appears below, something in this
// directory imported back into the plugin and the isolation is gone.
module github.com/the-protobuf-project/store/entity

go 1.26.4

require (
	github.com/the-protobuf-project/protokit v1.3.0
	google.golang.org/protobuf v1.36.11
)

require google.golang.org/genproto/googleapis/api v0.0.0-20260630182238-925bb5da69e7 // indirect

// protokit's removal of protokit.v1 — the change that created this module — is
// unreleased. Drop this once it is tagged and the require above resolves.
replace github.com/the-protobuf-project/protokit => ../../protokit
