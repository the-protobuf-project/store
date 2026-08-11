// Package proto is the factory Source that reads proto descriptors. It wraps
// protokit's proto→IR build so the rest of the factory never depends on protoc
// directly: Build runs protokit.Build with this plugin's facet readers and layout
// policy, and carries the resulting IR as the Model's DB facet.
package proto

import (
	"fmt"

	"github.com/the-protobuf-project/protokit"

	"github.com/the-protobuf-project/protokit/factory"
	"github.com/the-protobuf-project/store/plugin/factory/coreir"
)

// Source builds the proto/DB IR. opts, readers, and layout are fixed at
// construction (the protoc plugin context arrives per-run via factory.Ctx).
type Source struct {
	opts    protokit.Options
	readers []protokit.FacetReader
	layout  protokit.LayoutResolver
}

// New returns a proto Source driven by protokit opts, the plugin's facet readers,
// and the naming policy it resolved from its own config. Both readers and layout
// may be empty/nil, which yields a pure AIP + protokit.v1 build.
func New(opts protokit.Options, readers []protokit.FacetReader, layout protokit.LayoutResolver) *Source {
	return &Source{opts: opts, readers: readers, layout: layout}
}

// Name identifies this source in the registry and config.
func (s *Source) Name() string { return "proto" }

// Build runs protokit's IR build against the plugin's CodeGeneratorRequest.
func (s *Source) Build(ctx factory.Ctx) (*coreir.Model, error) {
	if ctx.Plugin == nil {
		return nil, fmt.Errorf("proto source requires a protoc plugin context (only available in a buf/protoc run)")
	}
	ir, err := protokit.Build(ctx.Plugin, s.opts, s.readers, s.layout)
	if err != nil {
		return nil, err
	}
	return &coreir.Model{DB: ir}, nil
}
