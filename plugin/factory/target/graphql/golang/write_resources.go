// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// write_resources.go emits each resource package: models, predicates, inputs, requests, and handlers.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/the-protobuf-project/protokit/graphql/ir"
)

// writeResources writes one package per resource: the predicate DSL, the natural
// CreateInput/UpdateInput structs, the chained request builders, and the handler
// interfaces plus their query/mutation/subscription implementations.
func (g *generator) writeResources() error {
	for _, dg := range g.domains {
		for _, rg := range dg.reses {
			if err := g.writeResource(rg); err != nil {
				return err
			}
		}
	}
	return nil
}

func (g *generator) writeResource(rg *resGen) error {
	subdir := filepath.Join(rg.domain, rg.pkg)
	if err := g.writeResourceModels(subdir, rg); err != nil {
		return err
	}
	if err := g.writePredicates(subdir, rg); err != nil {
		return err
	}
	if err := g.writeResourceInputs(subdir, rg); err != nil {
		return err
	}
	if err := g.writeRequests(subdir, rg); err != nil {
		return err
	}
	if err := g.writeHandlers(subdir, rg); err != nil {
		return err
	}
	if err := g.writeHandlerImpl(subdir, rg.domain, rg.pkg, "queries.go", "queryHandler", rg.queries); err != nil {
		return err
	}
	if err := g.writeHandlerImpl(subdir, rg.domain, rg.pkg, "mutations.go", "mutationHandler", rg.mutations); err != nil {
		return err
	}
	return g.writeHandlerImpl(subdir, rg.domain, rg.pkg, "subscriptions.go", "subscriptionHandler", rg.subs)
}

// writeResourceModels writes the models the resource's operations return into the
// resource package itself: full definitions for the objects this resource owns, and
// aliases into the owning sibling for the rare object another resource of the domain
// claimed first — so a table's types live with its query builders and callers import one
// package per resource, never a shared schema package.
func (g *generator) writeResourceModels(subdir string, rg *resGen) error {
	seen := map[string]*ir.Object{}
	for _, set := range [][]op{rg.queries, rg.mutations, rg.subs} {
		for _, o := range set {
			if obj, ok := g.opts.Schema.Objects[o.Op.Return.Base]; ok {
				seen[obj.Name] = obj
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	var defs, aliases strings.Builder
	imports := map[string]bool{}
	for _, name := range sortedKeys(seen) {
		if owner := g.ownerOf[name]; owner != nil && owner != rg {
			fmt.Fprintf(&aliases, "type %s = %s.%s\n", name, owner.pkg, name)
			imports[owner.importPath] = true
			continue
		}
		if defs.Len() > 0 {
			defs.WriteString("\n\n")
		}
		defs.WriteString(g.r.model(seen[name]))
	}
	var b strings.Builder
	b.WriteString(defs.String())
	if aliases.Len() > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("// Aliases for shared models defined by a sibling resource of this domain.\n")
		b.WriteString(aliases.String())
	}
	im := append(g.typeImports(defs.String()), sortedKeys(imports)...)
	return g.writeFile(subdir, "models.go", rg.pkg, im, b.String())
}

// writePredicates writes the resource's filter field handles and And/Or/Not combinators.
func (g *generator) writePredicates(subdir string, rg *resGen) error {
	body, _ := g.r.renderPredicates(rg)
	if body == "" {
		return nil
	}
	return g.writeFile(subdir, "predicates.go", rg.pkg, g.typeImports(body), body)
}

// writeResourceInputs writes the resource-local CreateInput and UpdateInput structs.
func (g *generator) writeResourceInputs(subdir string, rg *resGen) error {
	var b strings.Builder
	if base := g.r.insertObjectType(rg); base != "" {
		doc := "CreateInput holds the settable fields for creating one " + rg.res.Name + " row."
		b.WriteString(g.r.renderInputStruct("CreateInput", doc, g.opts.Schema.Inputs[base].Fields, false))
		b.WriteByte('\n')
	}
	if base := g.r.updateColumnsType(rg); base != "" {
		doc := "UpdateInput holds the fields to change on a " + rg.res.Name + " row; each set field becomes a column update."
		b.WriteString(g.r.renderInputStruct("UpdateInput", doc, g.opts.Schema.Inputs[base].Fields, true))
	}
	if b.Len() == 0 {
		return nil
	}
	return g.writeFile(subdir, "inputs.go", rg.pkg, g.typeImports(b.String()), b.String())
}

// writeRequests writes the chained <Method>Request builders for optional arguments.
func (g *generator) writeRequests(subdir string, rg *resGen) error {
	var b strings.Builder
	for _, set := range [][]op{rg.queries, rg.mutations, rg.subs} {
		for _, o := range set {
			if rt := g.r.requestType(o); rt != "" {
				b.WriteString(rt)
			}
		}
	}
	if b.Len() == 0 {
		return nil
	}
	return g.writeFile(subdir, "requests.go", rg.pkg, g.typeImports(b.String()), b.String())
}

// writeHandlers writes the query/mutation/subscription interfaces and constructors.
func (g *generator) writeHandlers(subdir string, rg *resGen) error {
	var b strings.Builder
	g.ifaceBlock(&b, "QueryHandler", "Query", rg.res.Name, "NewQuery", "queryHandler", rg.queries)
	g.ifaceBlock(&b, "MutationHandler", "Mutation", rg.res.Name, "NewMutation", "mutationHandler", rg.mutations)
	g.ifaceBlock(&b, "SubscriptionHandler", "Subscription", rg.res.Name, "NewSubscription", "subscriptionHandler", rg.subs)
	b.WriteString(g.r.genericAsserts(rg))
	return g.writeFile(subdir, "handlers.go", rg.pkg, g.resImports(rg.domain, b.String()), b.String())
}

// ifaceBlock appends an interface plus its constructor when ops is non-empty.
func (g *generator) ifaceBlock(b *strings.Builder, iface, verb, resource, ctor, recv string, ops []op) {
	if len(ops) == 0 {
		return
	}
	b.WriteString(g.r.iface(iface, fmt.Sprintf("%s runs %s %s operations.", iface, resource, strings.ToLower(verb)), ops))
	fmt.Fprintf(b, "\n// %s returns a %s bound to gql.\nfunc %s(gql *runtime.GraphQLClient) %s { return &%s{gql: gql} }\n\n", ctor, iface, ctor, iface, recv)
}

func (g *generator) writeHandlerImpl(subdir, domain, pkg, file, recv string, ops []op) error {
	if len(ops) == 0 {
		return nil
	}
	body := g.r.handler(recv, ops)
	return g.writeFile(subdir, file, pkg, g.resImports(domain, body), body)
}

// resImports returns the imports for a handler file: context and the runtime facade plus
// the non-schema type packages (enums/graphql). Model types are package-local (defined or
// aliased by models.go), so no schema import exists.
func (g *generator) resImports(domain, body string) []string {
	return append([]string{"context", g.opts.RuntimeModule}, g.typeImports(body)...)
}
