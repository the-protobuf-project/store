package cache

// plan.go turns the IR plus the (orm.v1.cache) policy into a Plan: what to
// cache, under which keys, for how long. The Plan mentions no programming
// language and no cache product — no Go types, no Go identifiers, no Redis
// commands — only resources, columns, key strings, and durations.
//
// That is the whole point of the split. A provider target (cache-redis,
// cache-valkey, …) renders this Plan into a client for its product, and a second
// language is a second template set over the same Plan rather than a second
// derivation of what the cache should contain. Anything a renderer needs that is
// language-specific (a Go type, a Python class name) it derives itself from the
// neutral names here.

import (
	"strings"

	"github.com/the-protobuf-project/protokit/schema"
)

// Plan is every cached resource in one database.
type Plan struct {
	// Database is the datasource name the plan belongs to.
	Database string

	// Resources are the cached resources, in schema then table order so output is
	// deterministic.
	Resources []Resource
}

// Resource is one cached resource: its identity, the lookups worth caching, and
// how entries expire.
type Resource struct {
	// Schema and Table are the neutral storage names.
	Schema, Table string

	// Singular is the bare resource name ("Booking"). A renderer builds its own
	// language's identifiers from this rather than being handed one.
	Singular string

	// Collection is the key prefix every entry for this resource sits under,
	// "hotel.bookings/". Dropping it invalidates the resource entirely.
	Collection string

	// ListPrefix is where cached list results live, "hotel.bookings/$list/".
	ListPrefix string

	// PKColumn is the primary key column, empty when the table has none.
	PKColumn string

	// TTLSeconds is the default entry lifetime; 0 means no expiry.
	TTLSeconds int64

	// Strategy and Invalidation are the resolved policy words.
	Strategy, Invalidation string

	// Keys are the single-entry lookups: fetch one resource by these columns.
	Keys []KeySpec

	// Lists are the declared multi-entry lookups.
	Lists []ListSpec

	// Stream is the broker coordinates for change-event invalidation, zero when
	// the policy does not use one.
	Stream Stream
}

// KeySpec is one cached lookup of a single resource.
type KeySpec struct {
	// Name is the key prefix, "hotel.bookings/id".
	Name string

	// Columns are the columns the lookup is keyed by, in order.
	Columns []string

	// Suffix is the trailing segment of Name, "id" — the part a renderer turns
	// into a method or function name in its own language's conventions.
	Suffix string
}

// ListSpec is one declared cached list query.
type ListSpec struct {
	// Name is the declared list name, "by_hotel_dates".
	Name string

	// Prefix is the key prefix its results sit under.
	Prefix string

	// Columns are the columns the query filters on.
	Columns []string

	// TTLSeconds is this list's lifetime, which may be shorter than the
	// resource's — an availability lookup goes stale faster than the rows behind
	// it.
	TTLSeconds int64

	// Strategy is the resolved strategy word for this list.
	Strategy string
}

// Build assembles the Plan for one database, skipping every table that declares
// no cache policy. An empty Plan means nothing in this database is cached, which
// a target uses to emit nothing at all.
func Build(db *schema.Database) Plan {
	p := Plan{Database: db.Name}
	for _, s := range db.Schemas {
		for _, t := range s.Tables {
			pol, ok := Of(s, t)
			if !ok {
				continue
			}
			r := Resource{
				Schema:       s.Name,
				Table:        t.Name,
				Singular:     t.LocalName,
				Collection:   Prefix(s, t),
				ListPrefix:   ListPrefix(s, t),
				PKColumn:     t.PKColumn,
				TTLSeconds:   int64(pol.TTL.Seconds()),
				Strategy:     pol.Strategy,
				Invalidation: pol.Invalidation,
				Stream:       pol.Stream,
			}
			for _, k := range pol.Keys {
				r.Keys = append(r.Keys, KeySpec{
					Name:    k.Name,
					Columns: k.Columns,
					Suffix:  strings.TrimPrefix(k.Name, r.Collection),
				})
			}
			for _, l := range pol.Lists {
				r.Lists = append(r.Lists, ListSpec{
					Name:       l.Name,
					Prefix:     l.Prefix,
					Columns:    l.Columns,
					TTLSeconds: int64(l.TTL.Seconds()),
					Strategy:   l.Strategy,
				})
			}
			p.Resources = append(p.Resources, r)
		}
	}
	return p
}

// Empty reports whether nothing in the database is cached.
func (p Plan) Empty() bool { return len(p.Resources) == 0 }
