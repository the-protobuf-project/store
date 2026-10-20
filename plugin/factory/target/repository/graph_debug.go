package repository

// TEMPORARY: STORE_GRAPH_DEBUG dumps planned graphs to stderr. Remove before commit.

import (
	"fmt"
	"os"
	"strings"
)

func debugGraph(r *resource) {
	if os.Getenv("STORE_GRAPH_DEBUG") == "" || len(r.Graph) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "%s.%s\n", r.Schema.Name, r.Table.LocalName)
	var walk func(es []*gEdge, depth int)
	walk = func(es []*gEdge, depth int) {
		for _, e := range es {
			kind := "one"
			if e.Kind == edgeMany {
				kind = "many"
			}
			extra := ""
			if e.Natural {
				extra += " natural"
			}
			if e.Case != nil {
				extra += " case=" + e.Case.Field
			}
			var owners []string
			for _, o := range e.Child.Owners {
				owners = append(owners, fmt.Sprintf("%s->d%d", o.Field, o.Depth))
			}
			fmt.Fprintf(os.Stderr, "%s%s %s -> %s.%s%s owners=[%s]\n", strings.Repeat("  ", depth+1), kind, e.FieldName, e.Child.Schema.Name, e.Child.Table.LocalName, extra, strings.Join(owners, ","))
			walk(e.Child.Edges, depth+1)
		}
	}
	walk(r.Graph, 0)
}
