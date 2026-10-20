// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package repository

// graph_gql.go renders the GraphQL adapter fragments for the deep value-object
// graphs planned in graph.go, over the generated client.
//
// Writes go through one client transaction (Mutation.Tx) so a graph lands
// whole or not at all: singular value objects, then the resource row, then
// has-many children, every key minted up front so owner columns can name rows
// queued earlier in the same transaction. A naturally keyed row is shared and
// is ensured (read, insert if absent) outside the transaction.
//
// Updates are ordered so a lost etag race never duplicates a list: stale ids
// are captured first; new singular value objects are committed before the
// guarded row update (a lost race leaves them unreferenced); new children and
// the removal of stale subtrees commit only after the update succeeded.
//
// Reads hydrate the graph with one Get per singular value object and one
// ordered List per has-many level.

import (
	"fmt"
	"strings"

	"github.com/the-protobuf-project/protokit/naming"
	"github.com/the-protobuf-project/protokit/schema"
)

// gqlNames is how the generated client spells one table.
type gqlNames struct {
	Domain     string // e.g. "Availability"
	Resource   string // e.g. "AvailablePeriods"
	ResPkg     string // e.g. "availableperiodsql"
	DomPkg     string // e.g. "availabilityql"
	Row        string // e.g. "availableperiodsql.AvailabilityAvailablePeriods"
	InsertResp string // e.g. "availableperiodsql.InsertAvailabilityAvailablePeriodsResponse"
	DeleteResp string // e.g. "availableperiodsql.DeleteAvailabilityAvailablePeriodsByIdResponse"
	ConvName   string // converter base, e.g. "availabilityAvailablePeriod"
}

func gqlNamesOf(t *schema.Table) gqlNames {
	domain := naming.PascalGo(t.PgSchema)
	model := domain + naming.PascalGo(t.Name)
	rest := strings.TrimPrefix(model, domain)
	pkg := clientPkgIdent(rest)
	return gqlNames{
		Domain:     domain,
		Resource:   rest,
		ResPkg:     pkg,
		DomPkg:     identLower(t.PgSchema) + "ql",
		Row:        pkg + "." + model,
		InsertResp: pkg + ".Insert" + model + "Response",
		DeleteResp: pkg + ".Delete" + model + "ByIdResponse",
		ConvName:   naming.CamelFirst(domain + t.LocalName),
	}
}

// graphGQL is the per-resource bundle of rendered GraphQL deep-graph fragments.
type graphGQL struct {
	Creates      []string        // ops before the resource insert (into gtx)
	AfterCreates []string        // has-many ops after the resource insert (into gtx)
	Hydrates     []string        // reads into out, inside toProto
	Stale        []string        // captures at the top of Update
	Updates      []string        // replacement ops: singular (utx, committed now) and has-many (atx)
	Deletes      []string        // subtree removal after the resource delete (into dtx)
	Nodes        []*gNode        // every node table, for converters and imports
	PBImports    map[string]bool // proto packages of oneof wrappers the hydrates box into
}

type gqlGraphRenderer struct {
	pb  *pbIndex
	n   int
	out *graphGQL
}

func (g *gqlGraphRenderer) fresh(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

// graphGQLFragments renders every GraphQL deep-graph fragment for r.
func graphGQLFragments(pb *pbIndex, r *resource) graphGQL {
	out := graphGQL{PBImports: map[string]bool{}}
	if len(r.Graph) == 0 {
		return out
	}
	g := &gqlGraphRenderer{pb: pb, out: &out}
	seen := map[*schema.Table]bool{}
	var collect func(es []*gEdge)
	collect = func(es []*gEdge) {
		for _, e := range es {
			if !seen[e.Child.Table] {
				seen[e.Child.Table] = true
				out.Nodes = append(out.Nodes, e.Child)
			}
			collect(e.Child.Edges)
		}
	}
	collect(r.Graph)

	for _, e := range r.Graph {
		if e.Kind == edgeOne {
			out.Creates = append(out.Creates, g.createOne(e, "in", "ci", false, []string{"id"}, "gtx"))
			out.Hydrates = append(out.Hydrates, g.hydrateOne(e, rowFK("row", e.FKCol), "out"))
			out.Deletes = append(out.Deletes, fmt.Sprintf("for i := range resp.Returning {\n%s\n}", g.deleteNode(e.Child, rowFK("resp.Returning[i]", e.FKCol), "dtx", "return ")))
		} else {
			out.AfterCreates = append(out.AfterCreates, g.createMany(e, "in", []string{"id"}, "gtx"))
			out.Hydrates = append(out.Hydrates, g.hydrateMany(e, "row.Id", "out"))
			out.Deletes = append(out.Deletes, g.deleteMany(e, "id", "dtx", "return "))
		}
	}

	for _, grp := range graphGroups(r.Graph) {
		var conds []string
		for _, e := range grp {
			conds = append(conds, fmt.Sprintf("repox.GroupTouched(paths, %q)", e.FieldName))
		}
		touched := strings.Join(conds, " || ")
		var b strings.Builder
		fmt.Fprintf(&b, "if %s {\n", touched)
		for _, e := range grp {
			stale := "stale" + e.PBGoName
			if e.Kind == edgeOne {
				if !e.Natural {
					out.Stale = append(out.Stale, fmt.Sprintf("%s := %s", stale, rowFK("row", e.FKCol)))
					b.WriteString(g.deleteNode(e.Child, stale, "atx", "return nil, ") + "\n")
				}
				fmt.Fprintf(&b, "patch.%s = graphql.Null[string]()\n", export(camel(e.FKCol.Name)))
			} else {
				// Captured before anything new is written: these are the rows
				// the update replaces, not the ones it adds.
				ids := g.fresh("old")
				out.Stale = append(out.Stale, g.listIDs(e, "id", ids))
				out.Stale = append(out.Stale, fmt.Sprintf("%s := %s", stale, ids))
				fmt.Fprintf(&b, "for _, oid := range %s {\n%s\n}\n", stale, g.deleteNode(e.Child, "oid", "atx", "return nil, "))
			}
		}
		if c := grp[0].Case; c != nil {
			fmt.Fprintf(&b, "patch.%s = graphql.Null[string]()\n", caseInputField(voField{Case: c}))
		}
		for _, e := range grp {
			if e.Kind == edgeOne {
				b.WriteString(g.createOne(e, "merged", "patch", true, []string{"id"}, "utx") + "\n")
			} else {
				b.WriteString(g.createMany(e, "merged", []string{"id"}, "atx") + "\n")
			}
		}
		b.WriteString("}")
		out.Updates = append(out.Updates, b.String())

	}
	return out
}

// rowFK reads an FK column off a client row, as a string.
func rowFK(row string, c *schema.Column) string {
	f := row + "." + export(camel(c.Name))
	if c.Optional {
		return "repox.Deref(" + f + ")"
	}
	return f
}

// wrapperOf is the qualified oneof wrapper of an edge's proto field.
func (g *gqlGraphRenderer) wrapperOf(e *gEdge) string {
	path := string(e.Field.GoIdent.GoImportPath)
	g.out.PBImports[path] = true
	return g.pb.names.Of(path) + "." + e.Field.GoIdent.GoName
}

// assignFK renders owner.<fk> = value on a CreateInput, or on an update patch.
func assignFK(owner, field, value string, patch bool) string {
	if patch {
		return fmt.Sprintf("%s.%s = graphql.Value(%s)\n", owner, field, value)
	}
	return fmt.Sprintf("%s.%s = %s\n", owner, field, value)
}

// createOne renders "when the singular field is set, queue its subtree and
// point the owner at it". owner is a CreateInput, or the update patch.
func (g *gqlGraphRenderer) createOne(e *gEdge, src, owner string, patch bool, ancestors []string, tx string) string {
	nm := gqlNamesOf(e.Child.Table)
	v := g.fresh("v")
	fk := export(camel(e.FKCol.Name))
	var b strings.Builder
	fmt.Fprintf(&b, "if %s := %s.Get%s(); %s != nil {\n", v, src, e.PBGoName, v)
	if e.Natural {
		key := v + ".Get" + e.Child.NaturalPB + "()"
		got := g.fresh("got")
		fmt.Fprintf(&b, "if key := %s; key != \"\" {\n", key)
		fmt.Fprintf(&b, "%s, err := r.Svc.Query.%s.%s.Get(ctx, key)\nif err != nil {\nreturn nil, mapGraphQLErr(err)\n}\n", got, nm.Domain, nm.Resource)
		fmt.Fprintf(&b, "if %s == nil {\n", got)
		fmt.Fprintf(&b, "if _, err := r.Svc.Mutation.%s.%s.Create(ctx, %sToCreateInput(%s)); err != nil {\n", nm.Domain, nm.Resource, nm.ConvName, v)
		fmt.Fprintf(&b, "// Lost a race to insert the shared row: fine if it now exists.\nif again, gerr := r.Svc.Query.%s.%s.Get(ctx, key); gerr != nil || again == nil {\nreturn nil, mapGraphQLErr(err)\n}\n}\n}\n", nm.Domain, nm.Resource)
		b.WriteString(assignFK(owner, fk, "key", patch))
		b.WriteString("}\n}")
		return b.String()
	}
	row := g.fresh("n")
	fmt.Fprintf(&b, "%s := %sToCreateInput(%s)\n%s.Id = repox.NewULID()\n", row, nm.ConvName, v, row)
	for _, o := range e.Child.Owners {
		fmt.Fprintf(&b, "%s.%s = %s\n", row, export(camel(o.Column.Name)), ancestors[o.Depth])
	}
	next := append(ancestors[:len(ancestors):len(ancestors)], row+".Id")
	for _, c := range e.Child.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.createOne(c, v, row, false, next, tx) + "\n")
		}
	}
	fmt.Fprintf(&b, "%s.Add(r.Svc.Mutation.%s.%s.CreateOp(%s, new(%s)))\n", tx, nm.Domain, nm.Resource, row, nm.InsertResp)
	for _, c := range e.Child.Edges {
		if c.Kind == edgeMany {
			b.WriteString(g.createMany(c, v, next, tx) + "\n")
		}
	}
	b.WriteString(assignFK(owner, fk, row+".Id", patch))
	if e.Case != nil {
		b.WriteString(assignFK(owner, caseInputField(voField{Case: e.Case}), fmt.Sprintf("%q", naming.ScreamingSnake(e.FieldName)), patch))
	}
	b.WriteString("}")
	return b.String()
}

// createMany renders "queue one child subtree per element, in order".
func (g *gqlGraphRenderer) createMany(e *gEdge, src string, ancestors []string, tx string) string {
	nm := gqlNamesOf(e.Child.Table)
	v, row := g.fresh("v"), g.fresh("n")
	var b strings.Builder
	fmt.Fprintf(&b, "for _, %s := range %s.Get%s() {\n", v, src, e.PBGoName)
	fmt.Fprintf(&b, "%s := %sToCreateInput(%s)\n%s.Id = repox.NewULID()\n", row, nm.ConvName, v, row)
	for _, o := range e.Child.Owners {
		fmt.Fprintf(&b, "%s.%s = %s\n", row, export(camel(o.Column.Name)), ancestors[o.Depth])
	}
	next := append(ancestors[:len(ancestors):len(ancestors)], row+".Id")
	for _, c := range e.Child.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.createOne(c, v, row, false, next, tx) + "\n")
		}
	}
	fmt.Fprintf(&b, "%s.Add(r.Svc.Mutation.%s.%s.CreateOp(%s, new(%s)))\n", tx, nm.Domain, nm.Resource, row, nm.InsertResp)
	for _, c := range e.Child.Edges {
		if c.Kind == edgeMany {
			b.WriteString(g.createMany(c, v, next, tx) + "\n")
		}
	}
	b.WriteString("}")
	return b.String()
}

// hydrateOne renders the read of a singular value object by its stored key,
// and of its own subtree, into target.
func (g *gqlGraphRenderer) hydrateOne(e *gEdge, idExpr, target string) string {
	nm := gqlNamesOf(e.Child.Table)
	got, pbv := g.fresh("got"), g.fresh("p")
	var b strings.Builder
	fmt.Fprintf(&b, "if id := %s; id != \"\" {\n", idExpr)
	fmt.Fprintf(&b, "%s, err := r.Svc.Query.%s.%s.Get(ctx, id)\nif err != nil {\nreturn nil, mapGraphQLErr(err)\n}\n", got, nm.Domain, nm.Resource)
	fmt.Fprintf(&b, "if %s != nil {\n%s := %sFromRow(%s)\n", got, pbv, nm.ConvName, got)
	b.WriteString(g.hydrateChildren(e.Child, got, pbv))
	if e.Case != nil {
		fmt.Fprintf(&b, "%s.%s = &%s{%s: %s}\n", target, e.Case.OneofGoName, g.wrapperOf(e), e.PBGoName, pbv)
	} else {
		fmt.Fprintf(&b, "%s.%s = %s\n", target, e.PBGoName, pbv)
	}
	b.WriteString("}\n}")
	return b.String()
}

// hydrateMany renders the ordered read of a has-many level into target.
func (g *gqlGraphRenderer) hydrateMany(e *gEdge, parentID, target string) string {
	nm := gqlNamesOf(e.Child.Table)
	rows, pbv := g.fresh("rows"), g.fresh("p")
	viaFK := g.viaFK(e)
	var b strings.Builder
	fmt.Fprintf(&b, "{\n%s, err := r.Svc.Query.%s.%s.List(ctx, %s.List().Where(%s.%s.Eq(%s)).OrderBy(%s.Id.Asc()))\nif err != nil {\nreturn nil, mapGraphQLErr(err)\n}\n",
		rows, nm.Domain, nm.Resource, nm.ResPkg, nm.ResPkg, viaFK, parentID, nm.ResPkg)
	fmt.Fprintf(&b, "for i := range %s {\n%s := %sFromRow(&%s[i])\n", rows, pbv, nm.ConvName, rows)
	b.WriteString(g.hydrateChildren(e.Child, "(&"+rows+"[i])", pbv))
	fmt.Fprintf(&b, "%s.%s = append(%s.%s, %s)\n}\n}", target, e.PBGoName, target, e.PBGoName, pbv)
	return b.String()
}

func (g *gqlGraphRenderer) hydrateChildren(n *gNode, row, target string) string {
	var b strings.Builder
	for _, c := range n.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.hydrateOne(c, rowFK(row, c.FKCol), target) + "\n")
		} else {
			b.WriteString(g.hydrateMany(c, row+".Id", target) + "\n")
		}
	}
	return b.String()
}

// viaFK is the child's predicate field pointing at the has-many parent: the
// owner column resolved to the immediate parent (the deepest ancestor).
func (g *gqlGraphRenderer) viaFK(e *gEdge) string {
	best := -1
	field := ""
	for _, o := range e.Child.Owners {
		if o.Depth > best {
			best, field = o.Depth, export(camel(o.Column.Name))
		}
	}
	return field
}

// listIDs renders the capture of a has-many level's current row keys.
func (g *gqlGraphRenderer) listIDs(e *gEdge, parentID, into string) string {
	nm := gqlNamesOf(e.Child.Table)
	rows := g.fresh("rows")
	return fmt.Sprintf("var %s []string\n{\n%s, err := r.Svc.Query.%s.%s.List(ctx, %s.List().Where(%s.%s.Eq(%s)))\nif err != nil {\nreturn nil, mapGraphQLErr(err)\n}\nfor i := range %s {\n%s = append(%s, %s[i].Id)\n}\n}",
		into, rows, nm.Domain, nm.Resource, nm.ResPkg, nm.ResPkg, g.viaFK(e), parentID, rows, into, into, rows)
}

// deleteNode renders the queued removal of the node row keyed by idExpr and
// its subtree: has-many children first, then the row, then its singular value
// objects. Natural-key rows are shared and stay. ret prefixes an early return
// in the enclosing function ("return " or "return nil, ").
func (g *gqlGraphRenderer) deleteNode(n *gNode, idExpr, tx, ret string) string {
	if n.NaturalPB != "" {
		return "_ = " + idExpr
	}
	nm := gqlNamesOf(n.Table)
	got := g.fresh("got")
	var b strings.Builder
	fmt.Fprintf(&b, "if id := %s; id != \"\" {\n", idExpr)
	fmt.Fprintf(&b, "%s, err := r.Svc.Query.%s.%s.Get(ctx, id)\nif err != nil {\n%smapGraphQLErr(err)\n}\n", got, nm.Domain, nm.Resource, ret)
	fmt.Fprintf(&b, "if %s != nil {\n", got)
	for _, c := range n.Edges {
		if c.Kind == edgeMany {
			b.WriteString(g.deleteMany(c, got+".Id", tx, ret) + "\n")
		}
	}
	fmt.Fprintf(&b, "%s.Add(r.Svc.Mutation.%s.%s.DeleteOp(%s.Id, new(%s)))\n", tx, nm.Domain, nm.Resource, got, nm.DeleteResp)
	for _, c := range n.Edges {
		if c.Kind == edgeOne {
			b.WriteString(g.deleteNode(c.Child, rowFK(got, c.FKCol), tx, ret) + "\n")
		}
	}
	b.WriteString("}\n}")
	return b.String()
}

// deleteMany renders the queued removal of every child subtree under parentID.
func (g *gqlGraphRenderer) deleteMany(e *gEdge, parentID, tx, ret string) string {
	nm := gqlNamesOf(e.Child.Table)
	rows := g.fresh("rows")
	var b strings.Builder
	fmt.Fprintf(&b, "{\n%s, err := r.Svc.Query.%s.%s.List(ctx, %s.List().Where(%s.%s.Eq(%s)))\nif err != nil {\n%smapGraphQLErr(err)\n}\n",
		rows, nm.Domain, nm.Resource, nm.ResPkg, nm.ResPkg, g.viaFK(e), parentID, ret)
	fmt.Fprintf(&b, "for i := range %s {\n%s\n}\n}", rows, g.deleteNode(e.Child, rows+"[i].Id", tx, ret))
	return b.String()
}
