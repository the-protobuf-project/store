// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package golang

// plan.go groups resources into domains and packages and assigns each operation a
// friendly CRUD method name (List/Get/Find/Create/Update/Delete/On…).

import (
	"sort"
	"strings"
	"unicode"

	"github.com/the-protobuf-project/protokit/graphql/dialect"
	"github.com/the-protobuf-project/protokit/graphql/ir"
	"github.com/the-protobuf-project/protokit/naming"
)

// resGen is a resource within a domain, with its per-kind operations.
type resGen struct {
	res        *ir.Resource
	domain     string // e.g. "schedule"
	field      string // e.g. "AvailabilityExceptions"
	pkg        string // e.g. "availabilityexceptions"
	importPath string
	queries    []op
	mutations  []op
	subs       []op
}

// domainGen groups resources sharing a top-level name (e.g. all "schedule*").
type domainGen struct {
	name       string // package name, e.g. "schedule"
	field      string // aggregator field, e.g. "Schedule"
	importPath string
	reses      []*resGen
	usedPkg    map[string]bool // dedup for resource package names within the domain
	usedField  map[string]bool // dedup for resource field names within the domain
}

// plan groups resources by domain and assigns packages and method names.
func (g *generator) plan() {
	byDomain := map[string]*domainGen{}
	for _, res := range g.opts.Schema.Resources {
		rawDomain, rest := splitDomain(res.Name)
		if rest == "" {
			rest = res.Name
		}
		// Generated packages carry a "ql" suffix (protobuf-style: foldername == package name
		// == import segment, e.g. organisationql, resourceql), keeping them visually distinct
		// from hand-written packages.
		domain := identifier(rawDomain) + "ql"
		dg, ok := byDomain[domain]
		if !ok {
			dg = &domainGen{
				name:       domain,
				field:      export(rawDomain),
				importPath: g.opts.GoModule + "/" + domain,
				usedPkg:    map[string]bool{},
				usedField:  map[string]bool{},
			}
			byDomain[domain] = dg
		}
		// Dedup within the domain so distinct resource names never collide on a package
		// directory or aggregator field (which would overwrite files or emit a duplicate
		// struct field).
		pkg := naming.Unique(identifier(rest), dg.usedPkg) + "ql"
		dg.reses = append(dg.reses, &resGen{
			res:        res,
			domain:     domain,
			field:      naming.Unique(export(rest), dg.usedField),
			pkg:        pkg,
			importPath: g.opts.GoModule + "/" + domain + "/" + pkg,
			queries:    pairOps(res.Queries, res.Name, g.opts.Dialect),
			mutations:  pairOps(res.Mutations, res.Name, g.opts.Dialect),
			subs:       pairOps(res.Subscriptions, res.Name, g.opts.Dialect),
		})
	}
	for _, name := range sortedKeys(byDomain) {
		dg := byDomain[name]
		sort.Slice(dg.reses, func(i, j int) bool { return dg.reses[i].field < dg.reses[j].field })
		g.domains = append(g.domains, dg)
	}
}

// listMethod is the method name for a resource's plural list query (where/order/limit).
const listMethod = "List"

// pairOps assigns each operation a short, unique method name within its kind. The list
// query additionally gets a Find companion that returns the first match (see op.FindOne).
func pairOps(ops []*ir.Operation, resource string, d dialect.Dialect) []op {
	used := map[string]bool{}
	out := make([]op, 0, len(ops))
	for _, o := range ops {
		short := opShortName(o, resource, d)
		name := naming.Unique(short, used)
		out = append(out, op{Op: o, Name: name})
		if o.Kind == "query" && short == listMethod {
			out = append(out, op{Op: o, Name: naming.Unique("Find", used), FindOne: true, ListName: name})
		}
	}
	return out
}

// splitDomain splits a PascalCase resource name into a lowercase domain (its first
// word) and the remainder (e.g. "ScheduleBufferSettings" -> "schedule", "BufferSettings").
func splitDomain(name string) (domain, rest string) {
	runes := []rune(name)
	i := 1
	for i < len(runes) && !unicode.IsUpper(runes[i]) {
		i++
	}
	return strings.ToLower(string(runes[:i])), string(runes[i:])
}

// opShortName maps a root field name to a friendly CRUD method within its resource
// package: list -> "Find", byId -> "Get", insertX -> "Create", updateXById -> "Update",
// deleteXById -> "Delete". Subscriptions get an "On" prefix (OnFind/OnGet).
func opShortName(operation *ir.Operation, resource string, d dialect.Dialect) string {
	switch operation.Kind {
	case "mutation":
		return mutationShort(operation.Name, resource, d)
	case "subscription":
		return "On" + queryShort(operation.Name, lowerFirst(resource), d)
	default:
		return queryShort(operation.Name, lowerFirst(resource), d)
	}
}

func queryShort(name, resCamel string, d dialect.Dialect) string {
	if strings.HasPrefix(name, resCamel) {
		switch rest := name[len(resCamel):]; rest {
		case "":
			return listMethod
		case d.ByIdSuffix():
			return "Get"
		default:
			return export(rest)
		}
	}
	return export(name)
}

// mutationShort maps insert/update/delete root fields to CRUD verbs, dropping the trailing
// key suffix (e.g. "updateXById" -> "Update", "insertX" -> "Create").
func mutationShort(name, resource string, d dialect.Dialect) string {
	for _, v := range d.MutationVerbs() {
		if strings.HasPrefix(name, v.OpPrefix) {
			rest := strings.TrimPrefix(name[len(v.OpPrefix):], resource)
			rest = strings.TrimPrefix(rest, d.ByIdSuffix())
			if rest == "" {
				return v.Friendly
			}
			return v.Friendly + export(rest)
		}
	}
	return export(name)
}

// identifier lowercases a name and keeps only [a-z0-9] for use as a package name,
// avoiding empty/digit-leading names and Go keywords (invalid as packages).
func identifier(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "res" + s
	}
	if naming.GoKeyword(s) {
		s += "_"
	}
	return s
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
