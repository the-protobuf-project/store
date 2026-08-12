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
//	cd plugin/entity && go build ./...
//
// resolves with no dependency on github.com/the-protobuf-project/store. If a
// require line for the parent module ever appears below, something in this
// directory imported back into the plugin and the isolation is gone.
module github.com/the-protobuf-project/store/plugin/entity

go 1.26.4

require (
	github.com/the-protobuf-project/protokit v1.2.1
	google.golang.org/protobuf v1.36.11
)

require google.golang.org/genproto/googleapis/api v0.0.0-20260720211330-0afa2a65878a // indirect
