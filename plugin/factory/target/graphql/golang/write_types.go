// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// write_types.go writes the shared model (schema) and enum packages and resolves each body's non-schema imports.

import (
	"path"
	"sort"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/naming"
)

// enumsDir is the shared enums package, referenced as "enums.". Model types live in the
// resource package whose operations return them (see writeResourceModels) — there is no
// separate schema package, so consumers never juggle same-named model packages across
// domains.
const enumsDir = "enumsql"

// modelGroup is the set of model objects a resource OWNS: every object its operations
// return (the row type, its aggregate, the mutation responses) that no earlier resource of
// the domain claimed. The owner defines the types in its own package; later resources that
// also return one of them alias the owner's definition.
type modelGroup struct {
	owner   *resGen
	objects []*ir.Object
}

// domainObjects returns, per domain, the model objects the domain's operations return
// (rows, responses, aggregates), grouped by owning resource. Because every object inlines
// its relations, this reachable set is self-contained. An object returned by several
// resources is claimed by the first (resources are already sorted), so each type is
// defined exactly once; ownerOf records the claim for the alias emission.
func (g *generator) domainObjects() map[string][]modelGroup {
	g.ownerOf = map[string]*resGen{}
	out := map[string][]modelGroup{}
	for _, dg := range g.domains {
		for _, rg := range dg.reses {
			seen := map[string]*ir.Object{}
			for _, set := range [][]op{rg.queries, rg.mutations, rg.subs} {
				for _, o := range set {
					if obj, ok := g.opts.Schema.Objects[o.Op.Return.Base]; ok && g.ownerOf[obj.Name] == nil {
						seen[obj.Name] = obj
					}
				}
			}
			if len(seen) == 0 {
				continue
			}
			grp := modelGroup{owner: rg}
			for _, name := range sortedKeys(seen) {
				g.ownerOf[name] = rg
				grp.objects = append(grp.objects, seen[name])
			}
			out[dg.name] = append(out[dg.name], grp)
		}
		sort.Slice(out[dg.name], func(i, j int) bool { return out[dg.name][i].owner.pkg < out[dg.name][j].owner.pkg })
	}
	return out
}

// writeTypes writes the shared enums package. Model types are emitted with their owning
// resource package (writeResourceModels), not here.
func (g *generator) writeTypes() error {
	enumUsed := map[string]bool{}
	for _, name := range sortedKeys(g.opts.Schema.Enums) {
		body := g.r.enum(g.opts.Schema.Enums[name])
		if err := g.writeFile(enumsDir, naming.GoFileName(name, "enum", enumUsed), "enumsql", g.typeImports(body), body); err != nil {
			return err
		}
	}
	return nil
}

// typeImports returns the non-schema imports a body needs (json, the runtime graphql
// helpers, and the shared enums package). The per-domain schema import is resolved by the
// caller, which knows the domain.
func (g *generator) typeImports(body string) []string {
	var im []string
	if strings.Contains(body, "json.RawMessage") {
		im = append(im, "encoding/json")
	}
	if strings.Contains(body, "graphql.") {
		im = append(im, g.graphqlModule())
	}
	if strings.Contains(body, "enumsql.") {
		im = append(im, g.opts.GoModule+"/"+enumsDir)
	}
	return im
}

// graphqlModule is the import path of the runtime graphql helper package (scalar types,
// predicate DSL, and column helpers), a sibling of the runtime facade module.
func (g *generator) graphqlModule() string {
	return path.Dir(g.opts.RuntimeModule) + "/graphql"
}
