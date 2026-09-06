// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// write_root.go writes the per-domain aggregator packages and the root Service and constructors.

import (
	"fmt"
	"strings"
)

// writeDomains writes one package per domain aggregating its resources' handlers.
func (g *generator) writeDomains() error {
	for _, dg := range g.domains {
		if err := g.writeDomain(dg); err != nil {
			return err
		}
	}
	return nil
}

// writeDomain writes a domain package aggregating its resources' handlers and aliasing the
// domain's model types (so callers read results via <domain>.<Model> with one import).
func (g *generator) writeDomain(dg *domainGen) error {
	var b strings.Builder
	imports := map[string]bool{g.opts.RuntimeModule: true}
	if groups := g.domSchema[dg.name]; len(groups) > 0 {
		b.WriteString("// Model type aliases for this domain, re-exported from the owning resource packages.\n")
		for _, grp := range groups {
			for _, obj := range grp.objects {
				fmt.Fprintf(&b, "type %s = %s.%s\n", obj.Name, grp.owner.pkg, obj.Name)
				imports[grp.owner.importPath] = true
			}
		}
		b.WriteByte('\n')
	}
	for _, spec := range kindSpecs() {
		members := domainMembers(dg, spec.pick)
		if len(members) == 0 {
			continue
		}
		fmt.Fprintf(&b, "// %s aggregates %s handlers for the %s domain.\ntype %s struct {\n", spec.iface, spec.verb, dg.name, spec.iface)
		for _, m := range members {
			fmt.Fprintf(&b, "\t%s %s.%s\n", m.field, m.pkg, spec.iface)
			imports[m.importPath] = true
		}
		b.WriteString("}\n\n")
		fmt.Fprintf(&b, "// %s wires every %s handler in the domain.\nfunc %s(gql *runtime.GraphQLClient) %s {\n\treturn %s{\n", spec.ctor, spec.verb, spec.ctor, spec.iface, spec.iface)
		for _, m := range members {
			fmt.Fprintf(&b, "\t\t%s: %s.%s(gql),\n", m.field, m.pkg, spec.ctor)
		}
		b.WriteString("\t}\n}\n\n")
	}
	return g.writeFile(dg.name, dg.name+".go", dg.name, sortedKeys(imports), b.String())
}

// writeRoot writes the root Service, its domain aggregators, and the New/NewFromURL
// constructors.
func (g *generator) writeRoot() error {
	var b strings.Builder
	imports := map[string]bool{g.opts.RuntimeModule: true, "net/url": true, "context": true, "fmt": true}

	b.WriteString("// Service is a typed GraphQL client. Access operations via\n")
	b.WriteString("// s.Query.<Domain>.<Resource>, s.Mutation..., and s.Subscription....\n")
	b.WriteString("type Service struct {\n\tQuery QueryHandler\n\tMutation MutationHandler\n\tSubscription SubscriptionHandler\n}\n\n")

	for _, spec := range kindSpecs() {
		fmt.Fprintf(&b, "// %s groups every domain's %s handlers.\ntype %s struct {\n", spec.iface, spec.verb, spec.iface)
		if spec.rawMethod != "" {
			b.WriteString("\tgql *runtime.GraphQLClient\n")
		}
		for _, dg := range g.domains {
			if len(domainMembers(dg, spec.pick)) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\t%s %s.%s\n", dg.field, dg.name, spec.iface)
			imports[dg.importPath] = true
		}
		b.WriteString("}\n\n")
		b.WriteString(rawMethod(spec))
		if spec.field == "Mutation" {
			fmt.Fprintf(&b, "%s", txMethod(spec.iface))
		}
	}

	b.WriteString("// New connects to the endpoint described by opts and returns a Service.\n")
	b.WriteString("func New(opts runtime.ConnectionOptions) (*Service, error) {\n")
	b.WriteString("\tconn, err := runtime.NewConnection(runtime.GraphQLConnClient)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\tif _, err := conn.WithOpts(opts); err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\tgql, err := conn.AsGraphQLConnectionType()\n\tif err != nil {\n\t\treturn nil, err\n\t}\n")
	b.WriteString("\treturn &Service{\n")
	for _, spec := range kindSpecs() {
		fmt.Fprintf(&b, "\t\t%s: %s{\n", spec.field, spec.iface)
		if spec.rawMethod != "" {
			b.WriteString("\t\t\tgql: gql,\n")
		}
		for _, dg := range g.domains {
			if len(domainMembers(dg, spec.pick)) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\t\t\t%s: %s.%s(gql),\n", dg.field, dg.name, spec.ctor)
		}
		b.WriteString("\t\t},\n")
	}
	b.WriteString("\t}, nil\n}\n\n")
	b.WriteString(serviceConnect)

	return g.writeFile("", "service.go", g.opts.Package, sortedKeys(imports), b.String())
}

// serviceConnect is the source of the Connect convenience constructor.
const serviceConnect = `// Connect dials the GraphQL endpoint at u (e.g. url.Parse("http://localhost:3280/graphql")).
// Request headers are optional; pass one map only if the endpoint needs them.
func Connect(u *url.URL, headers ...map[string]string) (*Service, error) {
	var h map[string]string
	if len(headers) > 0 {
		h = headers[0]
	}
	return New(runtime.ConnectionOptions{URL: runtime.URLFromStd(u), Headers: h})
}
`

// rawMethod renders the raw escape-hatch method for an aggregator (e.g. QueryRaw on
// QueryHandler), or "" when the kind has none. It runs an arbitrary operation string with
// optional variables and returns the decoded JSON response map.
func rawMethod(spec kindSpec) string {
	if spec.rawMethod == "" {
		return ""
	}
	return fmt.Sprintf(`// %s runs an arbitrary GraphQL %s string with optional variables and returns the decoded
// JSON response — an escape hatch for %s the typed API does not cover.
func (h %s) %s(ctx context.Context, %s string, variables map[string]any) (map[string]any, error) {
	res := <-h.gql.%s(ctx, %s, variables)
	if res.Error != nil {
		return nil, res.Error
	}
	out, ok := res.Response.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected raw response type %%T", res.Response)
	}
	return out, nil
}

`, spec.rawMethod, spec.rawNoun, spec.rawPlural, spec.iface, spec.rawMethod, spec.rawNoun, spec.rawCall, spec.rawNoun)
}

// txMethod renders the Tx starter on the root mutation aggregator (iface), which holds the gql
// client. Callers queue deferred mutations built with a resource's <Method>Op and Commit them as
// one atomic GraphQL document.
func txMethod(iface string) string {
	return fmt.Sprintf(`// Tx starts a transactional batch: add deferred mutations built with a resource's <Method>Op
// (e.g. svc.Mutation.Booking.Money.CreateOp(input, &out)), then Commit to run them as one atomic
// GraphQL document — all commit together or none do.
func (h %s) Tx() *runtime.Tx { return runtime.NewTx(h.gql) }

`, iface)
}

// kindSpec describes one operation kind's aggregator naming. rawMethod/rawCall, when set,
// add a raw escape-hatch method (e.g. QueryRaw) to the top-level aggregator, backed by the
// named runtime GraphQLClient call, so callers can run arbitrary operations the typed API
// does not cover.
type kindSpec struct {
	field     string // Service field: "Query"
	iface     string // aggregator/interface type: "QueryHandler"
	verb      string // "query"
	ctor      string // "NewQuery"
	pick      func(*resGen) []op
	rawMethod string // escape-hatch method name, e.g. "QueryRaw" ("" = none)
	rawCall   string // runtime GraphQLClient method, e.g. "ExecuteRawQuery"
	rawNoun   string // singular wording in the doc comment, e.g. "query"
	rawPlural string // plural wording in the doc comment, e.g. "queries"
}

func kindSpecs() []kindSpec {
	return []kindSpec{
		{"Query", "QueryHandler", "query", "NewQuery", func(r *resGen) []op { return r.queries }, "QueryRaw", "ExecuteRawQuery", "query", "queries"},
		{"Mutation", "MutationHandler", "mutation", "NewMutation", func(r *resGen) []op { return r.mutations }, "ExecuteRaw", "ExecRawMutation", "mutation", "mutations"},
		{"Subscription", "SubscriptionHandler", "subscription", "NewSubscription", func(r *resGen) []op { return r.subs }, "", "", "", ""},
	}
}

// member is a resource field within a domain aggregator.
type member struct {
	field      string
	pkg        string
	importPath string
}

// domainMembers returns the resources in dg that have ops for the picked kind.
func domainMembers(dg *domainGen, pick func(*resGen) []op) []member {
	var out []member
	for _, rg := range dg.reses {
		if len(pick(rg)) > 0 {
			out = append(out, member{field: rg.field, pkg: rg.pkg, importPath: rg.importPath})
		}
	}
	return out
}
