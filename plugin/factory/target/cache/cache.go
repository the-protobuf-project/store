// Package cache reads orm.v1's caching policy off a table's Source descriptor
// and checks it for coherence. The policy is metadata: nothing is emitted from
// it beyond generated documentation, so this package resolves and validates it
// rather than rendering it.
//
// As with the validation presets, the policy is read at render time off the
// descriptor instead of being stored on the IR, so protokit carries no
// orm-specific fields.
package cache

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/the-protobuf-project/orm/plugin/pb/ormpbv1"
	"github.com/the-protobuf-project/protokit/schema"
	"google.golang.org/protobuf/proto"
)

// Policy is one table's resolved caching policy, with defaults applied.
type Policy struct {
	// TTL is how long an entry stays fresh; zero when unset.
	TTL time.Duration

	// Store is where entries live, defaulted to memory.
	Store string

	// Strategy is how reads and writes move through the cache.
	Strategy string

	// Invalidation is what makes an entry stale.
	Invalidation string

	// Keys are the cached lookups, each already named — defaulted to the table's
	// primary key when the annotation declares none.
	Keys []Key

	// Stream is the broker subject carrying change events; zero when unset.
	Stream Stream
}

// Key is one cached lookup: a named column set.
type Key struct {
	Name    string
	Columns []string
}

// Stream is the broker coordinates for change-event driven invalidation.
type Stream struct {
	Subject    string
	Durable    string
	QueueGroup string
}

// Of returns the table's caching policy, reporting false when the table carries
// no annotation or has caching switched off. A table with a policy present but
// disabled reads as absent here, so callers need not check twice.
func Of(t *schema.Table) (Policy, bool) {
	o := opts(t)
	if !o.GetEnabled() {
		return Policy{}, false
	}
	p := Policy{
		Store:        storeName(o.GetStore()),
		Strategy:     strategyName(o.GetStrategy()),
		Invalidation: invalidationName(o.GetInvalidation()),
		Stream: Stream{
			Subject:    o.GetStream().GetSubject(),
			Durable:    o.GetStream().GetDurable(),
			QueueGroup: o.GetStream().GetQueueGroup(),
		},
	}
	if d := o.GetTtl(); d != nil {
		p.TTL = d.AsDuration()
	}
	for _, k := range o.GetKeys() {
		p.Keys = append(p.Keys, Key{Name: keyName(t, k), Columns: k.GetColumns()})
	}
	// No declared key set means the primary key alone — the lookup every store
	// already performs.
	if len(p.Keys) == 0 && t.PKColumn != "" {
		p.Keys = []Key{{Name: "cache_" + t.Name + "_" + t.PKColumn, Columns: []string{t.PKColumn}}}
	}
	return p, true
}

// Verify reports the ways a table's cache policy contradicts itself or the
// table. Like a misapplied validation preset, each of these would otherwise be
// silently inert: a key naming a column that does not exist can never be built,
// and a stream invalidation with no subject can never fire.
func Verify(t *schema.Table) []string {
	o := opts(t)
	if !o.GetEnabled() {
		return nil
	}
	var out []string

	cols := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		cols = append(cols, c.Name)
	}
	for _, k := range o.GetKeys() {
		if len(k.GetColumns()) == 0 {
			out = append(out, "cache key declares no columns")
			continue
		}
		for _, name := range k.GetColumns() {
			if !slices.Contains(cols, name) {
				out = append(out, fmt.Sprintf("cache key names unknown column %q", name))
			}
		}
	}
	if o.GetInvalidation() == ormpbv1.Invalidation_INVALIDATION_STREAM && o.GetStream().GetSubject() == "" {
		out = append(out, "invalidation is INVALIDATION_STREAM but no stream subject is set")
	}
	if o.GetInvalidation() == ormpbv1.Invalidation_INVALIDATION_TTL_ONLY && o.GetTtl() == nil {
		out = append(out, "invalidation is INVALIDATION_TTL_ONLY but no ttl is set, so entries would never become stale")
	}
	return out
}

// Doc renders the database's declared cache policies as a markdown section,
// appended to a generated README. Empty when no table declares one, so a tree
// without caching gains no section. This is the whole visible output of the
// annotation today: the policy is pinned in the schema and documented in the
// generated tree, but nothing reads it at runtime yet.
func Doc(db *schema.Database) string {
	var rows []string
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			p, ok := Of(t)
			if !ok {
				continue
			}
			ttl := "—"
			if p.TTL > 0 {
				ttl = p.TTL.String()
			}
			keys := make([]string, 0, len(p.Keys))
			for _, k := range p.Keys {
				keys = append(keys, fmt.Sprintf("`%s` (%s)", k.Name, strings.Join(k.Columns, ", ")))
			}
			stream := "—"
			if p.Stream.Subject != "" {
				stream = "`" + p.Stream.Subject + "`"
				if p.Stream.Durable != "" {
					stream += " (durable `" + p.Stream.Durable + "`)"
				}
			}
			rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |",
				t.LocalName, p.Store, ttl, p.Strategy, p.Invalidation, strings.Join(keys, "<br>"), stream))
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return "\n## Cache policy\n\n" +
		"Declared with `(orm.v1.cache)`. This is policy metadata: the schema states\n" +
		"which lookups are worth caching and what makes them stale. No cache client,\n" +
		"key builder, or stream consumer is generated from it — the contract is pinned\n" +
		"here so a runtime can implement it without re-deciding it.\n\n" +
		"| Model | Store | TTL | Strategy | Invalidation | Keys | Stream |\n" +
		"| --- | --- | --- | --- | --- | --- | --- |\n" +
		strings.Join(rows, "\n") + "\n"
}

// keyName is the key set's explicit name, or cache_<table>_<cols> when unnamed —
// the same shape protokit gives an unnamed index.
func keyName(t *schema.Table, k *ormpbv1.CacheKey) string {
	if n := k.GetKey(); n != "" {
		return n
	}
	return "cache_" + t.Name + "_" + strings.Join(k.GetColumns(), "_")
}

func storeName(s ormpbv1.CacheStore) string {
	switch s {
	case ormpbv1.CacheStore_CACHE_STORE_REDIS:
		return "redis"
	case ormpbv1.CacheStore_CACHE_STORE_VALKEY:
		return "valkey"
	default:
		return "memory"
	}
}

func strategyName(s ormpbv1.CacheStrategy) string {
	switch s {
	case ormpbv1.CacheStrategy_CACHE_STRATEGY_WRITE_THROUGH:
		return "write-through"
	case ormpbv1.CacheStrategy_CACHE_STRATEGY_WRITE_BEHIND:
		return "write-behind"
	case ormpbv1.CacheStrategy_CACHE_STRATEGY_REFRESH_AHEAD:
		return "refresh-ahead"
	default:
		return "read-through"
	}
}

func invalidationName(i ormpbv1.Invalidation) string {
	switch i {
	case ormpbv1.Invalidation_INVALIDATION_TTL_ONLY:
		return "ttl-only"
	case ormpbv1.Invalidation_INVALIDATION_STREAM:
		return "stream"
	default:
		return "on-write"
	}
}

// opts returns the table's cache annotation, empty when absent or synthesized.
func opts(t *schema.Table) *ormpbv1.CacheOptions {
	if t == nil || t.Source == nil || !proto.HasExtension(t.Source.Options(), ormpbv1.E_Cache) {
		return &ormpbv1.CacheOptions{}
	}
	return proto.GetExtension(t.Source.Options(), ormpbv1.E_Cache).(*ormpbv1.CacheOptions)
}
