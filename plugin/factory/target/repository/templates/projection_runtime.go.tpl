{{.Header}}

package repox

// projection.go is the runtime of (store.v1.projection): tables derived from
// source resources, kept current inside the writes to those sources and
// searched by range and AIP-160 filter. A generated projection is a
// Projection[T] value holding its column spec; everything it does -- encoding
// a message as a row, the upsert by key, the delete, the prune, the search,
// the count, the facets, the rebuild -- is here, once, for every projection.
//
// The application supplies what a row holds: a ProjectionWriter, told the
// source records a write changed, returns the rows to store and the rows to
// remove. Wired as a Projector on Conn.Projections, it runs:
//
//   - on GORM, inside the transaction of every generated repository write and
//     every Batch/Unit commit that changes a source -- the projection never
//     lags its sources, and a writer failure rolls the write back;
//   - on GraphQL, inside the one mutation request of a Batch/Unit commit (its
//     reads happen before the request, as a Batch's do), and right after a
//     repository write, as a request of its own: see ProjectGraphQLAfter.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"{{.FilterxImport}}"
{{- if .GraphQLModule}}
	"{{.GraphQLDSLImport}}"
{{- end}}
)

// ChangeOp is what a write did to a record.
type ChangeOp int

const (
	// ChangeCreate is a create.
	ChangeCreate ChangeOp = iota
	// ChangeUpdate is an update (any of the Update methods).
	ChangeUpdate
	// ChangeDelete is a delete: a soft delete for an AIP-164 resource, a
	// removal otherwise.
	ChangeDelete
	// ChangeUndelete is an undelete.
	ChangeUndelete
	// ChangePurge is a removal for good.
	ChangePurge
)

// Change is one record a write changed, as a projection hears of it.
type Change struct {
	// Type is the record's AIP resource type ("calendar.example.com/Event").
	Type string
	// Name is its resource name.
	Name string
	// Op is what the write did.
	Op ChangeOp
	// Record is the record as written, when the write had it: the stored
	// record after a repository Create/Update/Undelete, the queued input in
	// a Batch (for an update, only the fields its mask names mean anything:
	// the merge happens at commit). nil after a delete or purge. On GORM,
	// read the record through the projection's read connection instead,
	// which sees the write.
	Record proto.Message
	// Prior is the record as it was before the write, read inside the
	// write's transaction (on GraphQL, just before its request), for an
	// update, delete or purge: what a projection must un-project when a
	// write moves or removes a record. nil for a create, and when there was
	// none.
	Prior proto.Message
	// LoadPrior, set by the generated Batch writes, reads Prior when the
	// Unit commits, before its writes run.
	LoadPrior func(ctx context.Context, read Conn) (proto.Message, error)
}

// Prior adapts a repository Get to Change.Prior: the record, or nil when
// there is none.
func Prior[T proto.Message](m T, err error) (proto.Message, error) {
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m, nil
}

// PriorOf is a Change.LoadPrior reading the record name through get.
func PriorOf[T proto.Message](name string, get func(ctx context.Context, read Conn, name string) (T, error)) func(ctx context.Context, read Conn) (proto.Message, error) {
	return func(ctx context.Context, read Conn) (proto.Message, error) {
		return Prior(get(ctx, read, name))
	}
}

// WithPrior sets Prior on every change of cs.
func WithPrior(cs []Change, prior proto.Message) []Change {
	for i := range cs {
		cs[i].Prior = prior
	}
	return cs
}

// loadPriors fills the Prior of every change that can load one, reading
// through read.
func loadPriors(ctx context.Context, read Conn, changes []Change) error {
	for i := range changes {
		c := &changes[i]
		if c.Prior != nil || c.LoadPrior == nil {
			continue
		}
		prior, err := c.LoadPrior(ctx, read)
		if err != nil {
			return fmt.Errorf("reading %s before the write: %w", c.Name, err)
		}
		c.Prior = prior
	}
	return nil
}

// Projector keeps one projection current. The generated Projection[T]
// returns one from Projector(w); the repositories and Units call it.
type Projector interface {
	// ProjectionName is the projection message's full name.
	ProjectionName() string
	// Sources are the resource types whose writes it projects.
	Sources() []string
	// ProjectGorm runs the projection for changes inside tx and reports
	// what it stored and removed.
	ProjectGorm(ctx context.Context, tx *gorm.DB, changes []Change) (Committed, error)
{{- if .GraphQLModule}}
	// ProjectGraphQL runs the projection for changes, adding its mutations
	// to ops, which are sent in the same request as the writes.
	ProjectGraphQL(ctx context.Context, conn Conn, ops *Ops, changes []Change) (Committed, error)
{{- end}}
}

// Committed is what one committed write did to one projection: the rows it
// stored, each with its new version, and the keys it removed.
type Committed struct {
	// Projection is the projection message's full name.
	Projection string
	Stored     []StoredRow
	Removed    []RemovedRow
}

// StoredRow is a projection row a write stored.
type StoredRow struct {
	// Key is the row's KeyColumn value.
	Key string
	// Version is the row's version after the write (its row_version
	// column): higher than any version the row had before.
	Version int64
	// Row is the row as stored.
	Row proto.Message
}

// RemovedRow is a projection row a write removed.
type RemovedRow struct {
	Key string
	// Version is higher than the removed row's last version, so a copy that
	// applies versions in order applies the removal after every store.
	Version int64
}

// ProjectionFeed hears what each committed write did to the projections, in
// commit order per connection: the change feed a copy of a projection
// elsewhere follows (target=opensearch's indexer is one). It runs after the
// commit, so it cannot fail or undo the write; a copy that misses a change is
// repaired by rebuilding it (the OpenSearch indexer's Reindex).
type ProjectionFeed interface {
	ProjectionsCommitted(ctx context.Context, changes []Committed)
}

// ProjectionFeedFunc adapts a function to ProjectionFeed.
type ProjectionFeedFunc func(ctx context.Context, changes []Committed)

// ProjectionsCommitted calls f.
func (f ProjectionFeedFunc) ProjectionsCommitted(ctx context.Context, changes []Committed) {
	f(ctx, changes)
}

// Feeds is one ProjectionFeed handing every commit to each of fs in turn:
// an OpenSearch indexer per projection, say.
func Feeds(fs ...ProjectionFeed) ProjectionFeed {
	return ProjectionFeedFunc(func(ctx context.Context, changes []Committed) {
		for _, f := range fs {
			f.ProjectionsCommitted(ctx, changes)
		}
	})
}

// merge folds what a later step of the same commit did into c: a key stored
// after it was removed is a store, a key removed after it was stored a
// removal.
func (c *Committed) merge(o Committed) {
	if c.Projection == "" {
		c.Projection = o.Projection
	}
	stored := map[string]bool{}
	for _, r := range o.Stored {
		stored[r.Key] = true
	}
	removed := map[string]bool{}
	for _, r := range o.Removed {
		removed[r.Key] = true
	}
	c.Stored = slices.DeleteFunc(c.Stored, func(r StoredRow) bool { return stored[r.Key] || removed[r.Key] })
	c.Removed = slices.DeleteFunc(c.Removed, func(r RemovedRow) bool { return stored[r.Key] || removed[r.Key] })
	c.Stored = append(c.Stored, o.Stored...)
	c.Removed = append(c.Removed, o.Removed...)
}

// ErrProjectionStale reports that a write landed but a projection could not
// be updated with it (GraphQL repository writes only, which cannot share the
// write's request; see ProjectGraphQLAfter). Rebuild repairs it.
var ErrProjectionStale = errors.New("projection not updated")

// Projections are the projectors a Conn maintains. Build with NewProjections.
type Projections struct {
	list []Projector
	// OnError, when set, hears of a projection a GraphQL repository write
	// could not update (the write itself succeeded), and the write returns
	// no error. Without it the write returns ErrProjectionStale with its
	// result.
	OnError func(ctx context.Context, projection string, err error)
	// Feed, when set, hears what every committed write -- repository write,
	// Batch/Unit commit, Apply, Rebuild batch -- did to the projections.
	Feed ProjectionFeed
}

// Publish hands committed to the Feed, leaving out projections a write did
// not touch. The generated writes call it after their commit.
func (p *Projections) Publish(ctx context.Context, committed []Committed) {
	if p == nil || p.Feed == nil {
		return
	}
	committed = slices.DeleteFunc(slices.Clone(committed), func(c Committed) bool { return len(c.Stored) == 0 && len(c.Removed) == 0 })
	if len(committed) > 0 {
		p.Feed.ProjectionsCommitted(ctx, committed)
	}
}

// NewProjections returns the projectors ps.
func NewProjections(ps ...Projector) *Projections {
	return &Projections{list: ps}
}

// Watches reports whether a write to a record of resource type typ changes
// any projection.
func (p *Projections) Watches(typ string) bool {
	if p == nil {
		return false
	}
	for _, x := range p.list {
		if slices.Contains(x.Sources(), typ) {
			return true
		}
	}
	return false
}

// relevant is the subset of changes x projects.
func relevant(x Projector, changes []Change) []Change {
	var out []Change
	for _, c := range changes {
		if slices.Contains(x.Sources(), c.Type) {
			out = append(out, c)
		}
	}
	return out
}

// Gorm runs every projector changes concern inside tx, in order, and returns
// what they did, for Publish once tx commits; the first error is returned
// and should roll tx back.
func (p *Projections) Gorm(ctx context.Context, tx *gorm.DB, changes []Change) ([]Committed, error) {
	if p == nil {
		return nil, nil
	}
	var out []Committed
	for _, x := range p.list {
		if cs := relevant(x, changes); len(cs) > 0 {
			c, err := x.ProjectGorm(ctx, tx, cs)
			if err != nil {
				return nil, fmt.Errorf("projection %s: %w", x.ProjectionName(), err)
			}
			out = append(out, c)
		}
	}
	return out, nil
}
{{- if .GraphQLModule}}

// GraphQL runs every projector changes concern, adding their mutations to
// ops, and returns what they will do once ops commit, for Publish.
func (p *Projections) GraphQL(ctx context.Context, conn Conn, ops *Ops, changes []Change) ([]Committed, error) {
	if p == nil {
		return nil, nil
	}
	var out []Committed
	for _, x := range p.list {
		if cs := relevant(x, changes); len(cs) > 0 {
			c, err := x.ProjectGraphQL(ctx, conn, ops, cs)
			if err != nil {
				return nil, fmt.Errorf("projection %s: %w", x.ProjectionName(), err)
			}
			out = append(out, c)
		}
	}
	return out, nil
}

// ProjectGraphQLAfter brings the projections up to date with a GraphQL
// repository write that has already landed, in one request of its own. The
// engine cannot run the write and the projection in one transaction here --
// the write's own request is built by the repository and sent before the
// projection can be computed -- so a failure leaves the projection behind its
// sources until the next write or a Rebuild: it goes to OnError when set, and
// is returned as ErrProjectionStale otherwise.
func (p *Projections) ProjectGraphQLAfter(ctx context.Context, conn Conn, changes []Change) error {
	if p == nil || len(changes) == 0 {
		return nil
	}
	var ops Ops
	committed, err := p.GraphQL(ctx, conn, &ops, changes)
	if err == nil && len(ops) > 0 {
		tx := conn.GraphQL.Mutation.Tx()
		for _, op := range ops {
			tx.Add(op)
		}
		err = MapGraphQLErr(tx.Commit(ctx))
	}
	if err == nil {
		p.Publish(ctx, committed)
		return nil
	}
	if p.OnError != nil {
		p.OnError(ctx, "", err)
		return nil
	}
	return fmt.Errorf("%w: %v", ErrProjectionStale, err)
}
{{- end}}

// --- the projection value ----------------------------------------------------

// ColumnKind is how a projection column stores its field.
type ColumnKind int

const (
	// ColText is a string.
	ColText ColumnKind = iota
	// ColEnum is an enum, stored as its value name less EnumPrefix.
	ColEnum
	// ColBool is a bool.
	ColBool
	// ColInt is a signed or 32-bit unsigned integer.
	ColInt
	// ColUint64 is a 64-bit unsigned integer, stored as NUMERIC.
	ColUint64
	// ColFloat is a float or double.
	ColFloat
	// ColTimestamp is a google.protobuf.Timestamp.
	ColTimestamp
	// ColDate is a google.type.Date.
	ColDate
	// ColJSON is a google.protobuf.Struct/Value/ListValue or a map, stored
	// as jsonb.
	ColJSON
	// ColStrings is a repeated string, stored as text[].
	ColStrings
	// ColNumbers is a repeated number or bool, stored as an array.
	ColNumbers
	// ColRange is a google.type.Interval, stored as tstzrange.
	ColRange
	// ColMultirange is a repeated google.type.Interval, stored as
	// tstzmultirange.
	ColMultirange
)

// ProjectionColumn is one column of a projection table.
type ProjectionColumn struct {
	Field      string // proto field name
	Column     string // column name
	Kind       ColumnKind
	SQLType    string // the type a written value is cast to
	EnumPrefix string // ColEnum: the value prefix stripped
}

// ProjectionSpec is a projection table's shape, generated per projection.
type ProjectionSpec struct {
	// Name is the projection message's full name.
	Name string
	// Table is the quoted, schema-qualified table.
	Table string
	// Key lists the fields identifying a row; the KeyColumn holds their
	// values as a JSON array.
	Key []string
	// KeyColumn is the primary-key column.
	KeyColumn string
	// VersionColumn is the row-version column every upsert raises.
	VersionColumn string
	// Columns are the message's columns, in field order.
	Columns []ProjectionColumn
	// Range is the range column Search's range is matched against, or "".
	Range string
	// IndexLeads are the columns leading a B-tree index of the table (the
	// key's, and each declared B-tree index's, which ends in the key column
	// so it serves pages in its order). A search with equality on one of
	// them writes its range test so the planner leaves it out of the row
	// estimate (see rangeClause).
	IndexLeads []string
	// Sources are the resource types the projection is derived from.
	Sources []string
	// Filter is the table's AIP-160/AIP-132 surface.
	Filter filterx.Spec
}

// Span is a time window, half-open. A zero bound is unbounded.
type Span struct {
	Start, End time.Time
}

// SearchInput is a projection search: rows whose range contains Range (when
// set) and that match Filter and Where, in OrderBy order, a page at a time.
type SearchInput struct {
	// Range, when set, keeps the rows whose range column contains it: one
	// free interval covering the whole window.
	Range *Span
	// Filter is an AIP-160 filter over the projection's fields, JSON
	// attribute paths included (attributes.floor >= 3).
	Filter string
	// Where are typed conditions ANDed with Filter.
	Where []filterx.Condition
	// OrderBy is an AIP-132 order_by over the projection's sortable fields.
	OrderBy   string
	PageSize  int32
	PageToken string
	// ReadMask names the fields to return (AIP-157); empty returns every
	// field. The key fields always come back.
	ReadMask []string
}

// Bucket is one facet value and how many rows hold it.
type Bucket struct {
	Value string
	Count int64
}

// ProjectionRows is what a ProjectionWriter returns: the rows to store and
// the rows to remove.
type ProjectionRows[T proto.Message] struct {
	// Prune removes, first, every row matching each entry (its conditions
	// ANDed): all of a resource's days, before they are projected again.
	Prune [][]filterx.Condition
	// Delete removes rows by key; only their key fields are read.
	Delete []T
	// Upsert stores rows, each replacing the row with its key.
	Upsert []T
}

// ProjectionWriter computes a projection's rows for the source changes of
// one write. read reads within the write: on GORM it is the write's
// transaction, and sees it; on GraphQL it is the client, and does not yet see
// the writes of the request being built (use Change.Record). Building
// repositories over read (the schema's New(read)) is how a writer reads the
// sources; they do not themselves project.
type ProjectionWriter[T proto.Message] interface {
	Project(ctx context.Context, read Conn, changes []Change) (ProjectionRows[T], error)
}

// ProjectionWriterFunc adapts a function to ProjectionWriter.
type ProjectionWriterFunc[T proto.Message] func(ctx context.Context, read Conn, changes []Change) (ProjectionRows[T], error)

// Project calls f.
func (f ProjectionWriterFunc[T]) Project(ctx context.Context, read Conn, changes []Change) (ProjectionRows[T], error) {
	return f(ctx, read, changes)
}

// Projection is one generated projection: its spec and its operations.
type Projection[T proto.Message] struct {
	Spec ProjectionSpec
	// New returns an empty message.
	New func() T
{{- if .GraphQLModule}}
	// GraphQL reaches the table through the generated client; nil when the
	// client has no handler for it.
	GraphQL *ProjectionGraphQL
{{- end}}
}
{{- if .GraphQLModule}}

// ProjectionGraphQL is a projection table's typed client handlers, generated
// beside the projection. Rows travel as maps keyed by the client's
// camelCase field names.
type ProjectionGraphQL struct {
	List   func(ctx context.Context, conn Conn, req *graphql.ListRequest) ([]map[string]any, error)
	Count  func(ctx context.Context, conn Conn, where *graphql.Predicate) (int64, error)
	Insert func(conn Conn, ops *Ops, row map[string]any) error
	Delete func(conn Conn, ops *Ops, id string)
}
{{- end}}

// projector is Projection[T].Projector's result.
type projector[T proto.Message] struct {
	p *Projection[T]
	w ProjectionWriter[T]
}

// Projector returns the Projector maintaining p through w, for
// NewProjections.
func (p *Projection[T]) Projector(w ProjectionWriter[T]) Projector {
	return &projector[T]{p: p, w: w}
}

func (x *projector[T]) ProjectionName() string { return x.p.Spec.Name }
func (x *projector[T]) Sources() []string      { return x.p.Spec.Sources }

func (x *projector[T]) ProjectGorm(ctx context.Context, tx *gorm.DB, changes []Change) (Committed, error) {
	rows, err := x.w.Project(ctx, Conn{Gorm: tx}, changes)
	if err != nil {
		return Committed{}, err
	}
	return x.p.applyGorm(ctx, tx, rows)
}
{{- if .GraphQLModule}}

func (x *projector[T]) ProjectGraphQL(ctx context.Context, conn Conn, ops *Ops, changes []Change) (Committed, error) {
	rows, err := x.w.Project(ctx, Conn{GraphQL: conn.GraphQL}, changes)
	if err != nil {
		return Committed{}, err
	}
	return x.p.queueGraphQL(ctx, conn, ops, rows)
}
{{- end}}

// Apply stores rows outside any source write -- for a projection maintained
// by hand (PROJECTION_MAINTENANCE_MANUAL), or a repair. GORM applies them in
// one transaction; GraphQL in one request. conn.Projections' Feed hears of
// them once they commit.
func (p *Projection[T]) Apply(ctx context.Context, conn Conn, rows ProjectionRows[T]) error {
	_, err := p.apply(ctx, conn, rows)
	return err
}

// apply is Apply, reporting what it did.
func (p *Projection[T]) apply(ctx context.Context, conn Conn, rows ProjectionRows[T]) (Committed, error) {
	var c Committed
{{- if .GraphQLModule}}
	if conn.GraphQL != nil {
		var ops Ops
		var err error
		if c, err = p.queueGraphQL(ctx, conn, &ops, rows); err != nil {
			return c, err
		}
		if err := commitOps(ctx, conn, ops); err != nil {
			return c, err
		}
		conn.Projections.Publish(ctx, []Committed{c})
		return c, nil
	}
{{- end}}
	err := MapGormErr(conn.Gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		c, err = p.applyGorm(ctx, tx, rows)
		return err
	}))
	if err != nil {
		return c, err
	}
	conn.Projections.Publish(ctx, []Committed{c})
	return c, nil
}
{{- if .GraphQLModule}}

func commitOps(ctx context.Context, conn Conn, ops Ops) error {
	if len(ops) == 0 {
		return nil
	}
	tx := conn.GraphQL.Mutation.Tx()
	for _, op := range ops {
		tx.Add(op)
	}
	return MapGraphQLErr(tx.Commit(ctx))
}
{{- end}}

// RebuildOptions tune Rebuild.
type RebuildOptions struct {
	// Truncate removes every row before the first batch. Searches between
	// then and the batch that restores a row miss it; leave it off to only
	// replace rows (a writer can Prune what it re-projects).
	Truncate bool
}

// RebuildResult counts what a Rebuild did.
type RebuildResult struct {
	Batches  int
	Upserted int
	Deleted  int
}

// Rebuild recomputes the projection from scratch, or for a subset: each batch
// of changes batches yields (every resource, say, in pages, as ChangeUpdate)
// goes through w and is applied in its own transaction (on GraphQL, its own
// request). It is the backfill for a new projection and the repair for one
// that drifted.
func (p *Projection[T]) Rebuild(ctx context.Context, conn Conn, w ProjectionWriter[T], batches iter.Seq[[]Change], opts RebuildOptions) (RebuildResult, error) {
	var res RebuildResult
	if opts.Truncate {
		c, err := p.apply(ctx, conn, ProjectionRows[T]{Prune: [][]filterx.Condition{nil}})
		if err != nil {
			return res, err
		}
		res.Deleted += len(c.Removed)
	}
	for batch := range batches {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var c Committed
{{- if .GraphQLModule}}
		if conn.GraphQL != nil {
			rows, err := w.Project(ctx, Conn{GraphQL: conn.GraphQL}, batch)
			if err != nil {
				return res, err
			}
			var ops Ops
			if c, err = p.queueGraphQL(ctx, conn, &ops, rows); err != nil {
				return res, err
			}
			if err := commitOps(ctx, conn, ops); err != nil {
				return res, err
			}
		} else {
{{- end}}
		err := conn.Gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			rows, err := w.Project(ctx, Conn{Gorm: tx}, batch)
			if err != nil {
				return err
			}
			c, err = p.applyGorm(ctx, tx, rows)
			return err
		})
		if err != nil {
			return res, MapGormErr(err)
		}
{{- if .GraphQLModule}}
		}
{{- end}}
		conn.Projections.Publish(ctx, []Committed{c})
		res.Batches++
		res.Upserted += len(c.Stored)
		res.Deleted += len(c.Removed)
	}
	return res, nil
}

// --- encoding ----------------------------------------------------------------

// Key is the KeyColumn value of m: its key fields' column values as a JSON
// array.
func (p *Projection[T]) Key(m T) (string, error) {
	vals := make([]any, 0, len(p.Spec.Key))
	r := m.ProtoReflect()
	for _, k := range p.Spec.Key {
		col, ok := p.column(k)
		if !ok {
			return "", fmt.Errorf("projection %s: key field %q has no column", p.Spec.Name, k)
		}
		v, err := encodeColumn(r, col)
		if err != nil {
			return "", err
		}
		vals = append(vals, v)
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (p *Projection[T]) column(field string) (ProjectionColumn, bool) {
	for _, c := range p.Spec.Columns {
		if c.Field == field {
			return c, true
		}
	}
	return ProjectionColumn{}, false
}

// row encodes m as column values, keyed by column. Arrays stay slices and
// ranges are PostgreSQL range text; each backend formats them its own way.
func (p *Projection[T]) row(m T) (map[string]any, error) {
	key, err := p.Key(m)
	if err != nil {
		return nil, err
	}
	out := map[string]any{p.Spec.KeyColumn: key}
	r := m.ProtoReflect()
	for _, c := range p.Spec.Columns {
		v, err := encodeColumn(r, c)
		if err != nil {
			return nil, err
		}
		out[c.Column] = v
	}
	return out, nil
}

func encodeColumn(r protoreflect.Message, c ProjectionColumn) (any, error) {
	fd := r.Descriptor().Fields().ByName(protoreflect.Name(c.Field))
	if fd == nil {
		return nil, fmt.Errorf("projection: %s has no field %q", r.Descriptor().FullName(), c.Field)
	}
	v := r.Get(fd)
	switch c.Kind {
	case ColText:
		return v.String(), nil
	case ColEnum:
		ev := fd.Enum().Values().ByNumber(v.Enum())
		if ev == nil {
			return strconv.Itoa(int(v.Enum())), nil
		}
		return strings.TrimPrefix(string(ev.Name()), c.EnumPrefix), nil
	case ColBool:
		return v.Bool(), nil
	case ColInt:
		if fd.Kind() == protoreflect.Uint32Kind || fd.Kind() == protoreflect.Fixed32Kind {
			return int64(v.Uint()), nil
		}
		return v.Int(), nil
	case ColUint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case ColFloat:
		return v.Float(), nil
	case ColTimestamp:
		if !r.Has(fd) {
			return nil, nil
		}
		return timeOf(v.Message()).UTC().Format(time.RFC3339Nano), nil
	case ColDate:
		if !r.Has(fd) {
			return nil, nil
		}
		d := v.Message()
		y, mo, day := intField(d, "year"), intField(d, "month"), intField(d, "day")
		if y == 0 || mo == 0 || day == 0 {
			return nil, nil
		}
		return fmt.Sprintf("%04d-%02d-%02d", y, mo, day), nil
	case ColJSON:
		if fd.IsMap() {
			if v.Map().Len() == 0 {
				return nil, nil
			}
			m := map[string]any{}
			v.Map().Range(func(k protoreflect.MapKey, x protoreflect.Value) bool {
				m[k.String()] = x.Interface()
				return true
			})
			b, err := json.Marshal(m)
			return string(b), err
		}
		if !r.Has(fd) {
			return nil, nil
		}
		b, err := protojson.Marshal(v.Message().Interface())
		return string(b), err
	case ColStrings:
		l := v.List()
		out := make([]string, l.Len())
		for i := range out {
			out[i] = l.Get(i).String()
		}
		return out, nil
	case ColNumbers:
		l := v.List()
		out := make([]any, l.Len())
		for i := range out {
			out[i] = l.Get(i).Interface()
		}
		return out, nil
	case ColRange:
		if !r.Has(fd) {
			return nil, nil
		}
		return FormatRange(spanOf(v.Message())), nil
	case ColMultirange:
		l := v.List()
		spans := make([]Span, l.Len())
		for i := range spans {
			spans[i] = spanOf(l.Get(i).Message())
		}
		return FormatMultirange(spans), nil
	}
	return nil, fmt.Errorf("projection: column %q has an unknown kind", c.Column)
}

func intField(m protoreflect.Message, name string) int64 {
	fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return 0
	}
	return m.Get(fd).Int()
}

func timeOf(m protoreflect.Message) time.Time {
	return time.Unix(intField(m, "seconds"), intField(m, "nanos")).UTC()
}

// spanOf reads a google.type.Interval; an unset bound is unbounded.
func spanOf(m protoreflect.Message) Span {
	var s Span
	fields := m.Descriptor().Fields()
	if fd := fields.ByName("start_time"); fd != nil && m.Has(fd) {
		s.Start = timeOf(m.Get(fd).Message())
	}
	if fd := fields.ByName("end_time"); fd != nil && m.Has(fd) {
		s.End = timeOf(m.Get(fd).Message())
	}
	return s
}

// setSpan writes s into a google.type.Interval.
func setSpan(m protoreflect.Message, s Span) {
	fields := m.Descriptor().Fields()
	if fd := fields.ByName("start_time"); fd != nil && !s.Start.IsZero() {
		m.Set(fd, protoreflect.ValueOfMessage(timestamppb.New(s.Start).ProtoReflect()))
	}
	if fd := fields.ByName("end_time"); fd != nil && !s.End.IsZero() {
		m.Set(fd, protoreflect.ValueOfMessage(timestamppb.New(s.End).ProtoReflect()))
	}
}

// FormatRange renders s as a tstzrange literal, half-open; a zero bound is
// unbounded.
func FormatRange(s Span) string {
	lower, upper := "", ""
	open := "["
	if s.Start.IsZero() {
		open = "("
	} else {
		lower = strconv.Quote(s.Start.UTC().Format(time.RFC3339Nano))
	}
	if !s.End.IsZero() {
		upper = strconv.Quote(s.End.UTC().Format(time.RFC3339Nano))
	}
	return open + lower + "," + upper + ")"
}

// FormatMultirange renders spans as a tstzmultirange literal.
func FormatMultirange(spans []Span) string {
	parts := make([]string, len(spans))
	for i, s := range spans {
		parts[i] = FormatRange(s)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// ParseMultirange reads a tstzmultirange (or a single tstzrange) in
// PostgreSQL's text form. Bounds' inclusivity is not kept: a Span is
// half-open, as the ranges a projection writes are.
func ParseMultirange(s string) ([]Span, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "{}" || s == "empty" {
		return nil, nil
	}
	if strings.HasPrefix(s, "{") {
		if !strings.HasSuffix(s, "}") {
			return nil, fmt.Errorf("malformed multirange %q", s)
		}
		s = s[1 : len(s)-1]
	}
	var out []Span
	for len(s) > 0 {
		s = strings.TrimLeft(s, ", ")
		if s == "" {
			break
		}
		if strings.HasPrefix(s, "empty") {
			s = s[len("empty"):]
			continue
		}
		if s[0] != '[' && s[0] != '(' {
			return nil, fmt.Errorf("malformed range at %q", s)
		}
		s = s[1:]
		lower, rest, err := rangeBound(s, ',')
		if err != nil {
			return nil, err
		}
		upper, rest, err := rangeBound(rest[1:], ')', ']')
		if err != nil {
			return nil, err
		}
		s = rest[1:]
		var sp Span
		if sp.Start, err = parseInstant(lower); err != nil {
			return nil, err
		}
		if sp.End, err = parseInstant(upper); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, nil
}

// rangeBound reads one bound up to a terminator, unquoting it; rest starts at
// the terminator.
func rangeBound(s string, terms ...byte) (bound, rest string, err error) {
	if strings.HasPrefix(s, `"`) {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch s[i] {
			case '\\':
				if i+1 < len(s) {
					i++
					b.WriteByte(s[i])
				}
			case '"':
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				rest = s[i+1:]
				if rest == "" || !slices.Contains(terms, rest[0]) {
					return "", "", fmt.Errorf("malformed range bound in %q", s)
				}
				return b.String(), rest, nil
			default:
				b.WriteByte(s[i])
			}
		}
		return "", "", fmt.Errorf("unterminated range bound in %q", s)
	}
	for i := 0; i < len(s); i++ {
		if slices.Contains(terms, s[i]) {
			return strings.TrimSpace(s[:i]), s[i:], nil
		}
	}
	return "", "", fmt.Errorf("malformed range bound in %q", s)
}

var instantLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z07",
	"2006-01-02T15:04:05.999999999Z07:00:00",
	"2006-01-02T15:04:05.999999999Z07",
}

// parseInstant reads a timestamptz in ISO or PostgreSQL text form; "" and
// the infinities are unbounded.
func parseInstant(s string) (time.Time, error) {
	switch s {
	case "", "infinity", "-infinity":
		return time.Time{}, nil
	}
	for _, l := range instantLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable instant %q", s)
}

// decode builds a message from a row whose values are what JSON makes of
// them (to_jsonb on PostgreSQL, the response on GraphQL); key maps a column
// to the row's key for it.
func (p *Projection[T]) decode(row map[string]any, key func(string) string) (T, error) {
	m := p.New()
	r := m.ProtoReflect()
	for _, c := range p.Spec.Columns {
		v, ok := row[key(c.Column)]
		if !ok || v == nil {
			continue
		}
		if err := decodeColumn(r, c, v); err != nil {
			return m, fmt.Errorf("projection %s: column %q: %w", p.Spec.Name, c.Column, err)
		}
	}
	return m, nil
}

func decodeColumn(r protoreflect.Message, c ProjectionColumn, v any) error {
	fd := r.Descriptor().Fields().ByName(protoreflect.Name(c.Field))
	if fd == nil {
		return fmt.Errorf("no field %q", c.Field)
	}
	text := func() string {
		switch x := v.(type) {
		case string:
			return x
		case json.Number:
			return x.String()
		default:
			return fmt.Sprint(x)
		}
	}
	switch c.Kind {
	case ColText:
		r.Set(fd, protoreflect.ValueOfString(text()))
	case ColEnum:
		ev := fd.Enum().Values().ByName(protoreflect.Name(c.EnumPrefix + text()))
		if ev == nil {
			return fmt.Errorf("unknown enum value %q", text())
		}
		r.Set(fd, protoreflect.ValueOfEnum(ev.Number()))
	case ColBool:
		b, err := strconv.ParseBool(text())
		if err != nil {
			return err
		}
		r.Set(fd, protoreflect.ValueOfBool(b))
	case ColInt, ColUint64:
		switch fd.Kind() {
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
			n, err := strconv.ParseUint(text(), 10, 64)
			if err != nil {
				return err
			}
			if fd.Kind() == protoreflect.Uint32Kind || fd.Kind() == protoreflect.Fixed32Kind {
				r.Set(fd, protoreflect.ValueOfUint32(uint32(n)))
			} else {
				r.Set(fd, protoreflect.ValueOfUint64(n))
			}
		default:
			n, err := strconv.ParseInt(text(), 10, 64)
			if err != nil {
				return err
			}
			if fd.Kind() == protoreflect.Int64Kind || fd.Kind() == protoreflect.Sint64Kind || fd.Kind() == protoreflect.Sfixed64Kind {
				r.Set(fd, protoreflect.ValueOfInt64(n))
			} else {
				r.Set(fd, protoreflect.ValueOfInt32(int32(n)))
			}
		}
	case ColFloat:
		f, err := strconv.ParseFloat(text(), 64)
		if err != nil {
			return err
		}
		if fd.Kind() == protoreflect.FloatKind {
			r.Set(fd, protoreflect.ValueOfFloat32(float32(f)))
		} else {
			r.Set(fd, protoreflect.ValueOfFloat64(f))
		}
	case ColTimestamp:
		t, err := parseInstant(text())
		if err != nil {
			return err
		}
		r.Set(fd, protoreflect.ValueOfMessage(timestamppb.New(t).ProtoReflect()))
	case ColDate:
		t, err := time.Parse("2006-01-02", text()[:min(len(text()), 10)])
		if err != nil {
			return err
		}
		d := r.NewField(fd).Message()
		for name, n := range map[string]int32{"year": int32(t.Year()), "month": int32(t.Month()), "day": int32(t.Day())} {
			if f := d.Descriptor().Fields().ByName(protoreflect.Name(name)); f != nil {
				d.Set(f, protoreflect.ValueOfInt32(n))
			}
		}
		r.Set(fd, protoreflect.ValueOfMessage(d))
	case ColJSON:
		raw, err := jsonRaw(v)
		if err != nil {
			return err
		}
		if fd.IsMap() {
			var m map[string]any
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.UseNumber()
			if err := dec.Decode(&m); err != nil {
				return err
			}
			mv := r.Mutable(fd).Map()
			for k, x := range m {
				val, err := scalarValue(fd.MapValue(), x)
				if err != nil {
					return err
				}
				mv.Set(protoreflect.ValueOfString(k).MapKey(), val)
			}
			return nil
		}
		msg := r.NewField(fd).Message()
		if err := protojson.Unmarshal(raw, msg.Interface()); err != nil {
			return err
		}
		r.Set(fd, protoreflect.ValueOfMessage(msg))
	case ColStrings, ColNumbers:
		items, err := arrayItems(v)
		if err != nil {
			return err
		}
		l := r.Mutable(fd).List()
		for _, x := range items {
			val, err := scalarValue(fd, x)
			if err != nil {
				return err
			}
			l.Append(val)
		}
	case ColRange, ColMultirange:
		spans, err := ParseMultirange(text())
		if err != nil {
			return err
		}
		if c.Kind == ColRange {
			if len(spans) > 0 {
				m := r.NewField(fd).Message()
				setSpan(m, spans[0])
				r.Set(fd, protoreflect.ValueOfMessage(m))
			}
			return nil
		}
		l := r.Mutable(fd).List()
		for _, s := range spans {
			m := l.NewElement().Message()
			setSpan(m, s)
			l.Append(protoreflect.ValueOfMessage(m))
		}
	}
	return nil
}

// jsonRaw is v as JSON: a document already decoded, or a string holding one.
func jsonRaw(v any) ([]byte, error) {
	if s, ok := v.(string); ok && json.Valid([]byte(s)) {
		return []byte(s), nil
	}
	if b, ok := v.(json.RawMessage); ok {
		return b, nil
	}
	return json.Marshal(v)
}

// arrayItems reads an array value: decoded JSON, or PostgreSQL array text.
func arrayItems(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		if strings.HasPrefix(x, "[") {
			var out []any
			err := json.Unmarshal([]byte(x), &out)
			return out, err
		}
		return parsePGArray(x)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Slice {
		out := make([]any, rv.Len())
		for i := range out {
			e := rv.Index(i)
			for e.Kind() == reflect.Pointer || e.Kind() == reflect.Interface {
				if e.IsNil() {
					break
				}
				e = e.Elem()
			}
			out[i] = e.Interface()
		}
		return out, nil
	}
	return nil, fmt.Errorf("not an array: %T", v)
}

// parsePGArray reads a one-dimensional PostgreSQL array literal.
func parsePGArray(s string) ([]any, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("malformed array %q", s)
	}
	s = s[1 : len(s)-1]
	var out []any
	for len(s) > 0 {
		var item string
		if s[0] == '"' {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			item, s = b.String(), s[min(i+1, len(s)):]
		} else {
			j := strings.IndexByte(s, ',')
			if j < 0 {
				j = len(s)
			}
			item, s = s[:j], s[j:]
		}
		out = append(out, item)
		s = strings.TrimPrefix(s, ",")
	}
	return out, nil
}

// pgArray renders a value list as a PostgreSQL array literal.
func pgArray(v any) (string, error) {
	items, err := arrayItems(v)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(items))
	for i, x := range items {
		s := fmt.Sprint(x)
		if _, ok := x.(string); ok {
			s = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
		}
		parts[i] = s
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

// scalarValue converts a decoded JSON scalar to fd's (element) kind.
func scalarValue(fd protoreflect.FieldDescriptor, x any) (protoreflect.Value, error) {
	s := fmt.Sprint(x)
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(s), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(s)
		return protoreflect.ValueOfBool(b), err
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(s, 10, 32)
		return protoreflect.ValueOfInt32(int32(n)), err
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(s, 10, 64)
		return protoreflect.ValueOfInt64(n), err
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(s, 10, 32)
		return protoreflect.ValueOfUint32(uint32(n)), err
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(s, 10, 64)
		return protoreflect.ValueOfUint64(n), err
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(s, 32)
		return protoreflect.ValueOfFloat32(float32(f)), err
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(s, 64)
		return protoreflect.ValueOfFloat64(f), err
	}
	return protoreflect.Value{}, fmt.Errorf("unsupported element kind %s", fd.Kind())
}

// --- GORM --------------------------------------------------------------------

// versionExpr is the version a row stored now starts at: the database
// clock in microseconds. An upsert over a stored row takes the greater of it
// and the stored version plus one, under the row's lock, so versions only
// rise however clocks or commits interleave.
const versionExpr = "(extract(epoch from clock_timestamp()) * 1000000)::bigint"

// removed scans a DELETE ... RETURNING of key and version into removals,
// each versioned past the row's last version.
func removed(db *gorm.DB, sql string, args ...any) ([]RemovedRow, error) {
	var got []struct {
		Key     string
		Version int64
		Now     int64
	}
	if err := db.Raw(sql, args...).Scan(&got).Error; err != nil {
		return nil, err
	}
	out := make([]RemovedRow, len(got))
	for i, g := range got {
		out[i] = RemovedRow{Key: g.Key, Version: max(g.Version+1, g.Now)}
	}
	return out, nil
}

// applyGorm writes rows through tx -- prunes, deletes, then one upsert per
// chunk -- and reports what it stored and removed.
func (p *Projection[T]) applyGorm(ctx context.Context, tx *gorm.DB, rows ProjectionRows[T]) (Committed, error) {
	db := tx.WithContext(ctx)
	out := Committed{Projection: p.Spec.Name}
	ver := p.Spec.VersionColumn
	returning := ` RETURNING "` + p.Spec.KeyColumn + `" AS key, "` + ver + `" AS version, ` + versionExpr + ` AS now`
	eng := filterx.Gorm[struct{}](p.Spec.Filter)
	for _, conds := range rows.Prune {
		where, args, err := eng.Where(conds)
		if err != nil {
			return out, MapFilterxErr(err)
		}
		if where == "" {
			where = "TRUE"
		}
		gone, err := removed(db, "DELETE FROM "+p.Spec.Table+" WHERE "+where+returning, args...)
		if err != nil {
			return out, err
		}
		out.merge(Committed{Removed: gone})
	}
	if len(rows.Delete) > 0 {
		keys := make([]string, 0, len(rows.Delete))
		for _, m := range rows.Delete {
			k, err := p.Key(m)
			if err != nil {
				return out, err
			}
			keys = append(keys, k)
		}
		for chunk := range slices.Chunk(keys, KeysPerRead) {
			gone, err := removed(db, `DELETE FROM `+p.Spec.Table+` WHERE "`+p.Spec.KeyColumn+`" IN ?`+returning, chunk)
			if err != nil {
				return out, err
			}
			out.merge(Committed{Removed: gone})
		}
	}
	if len(rows.Upsert) == 0 {
		return out, nil
	}
	cols := append([]string{p.Spec.KeyColumn}, columnNames(p.Spec.Columns)...)
	casts := map[string]string{p.Spec.KeyColumn: "text"}
	for _, c := range p.Spec.Columns {
		casts[c.Column] = c.SQLType
	}
	// One statement per chunk keeps the parameter count under PostgreSQL's
	// 65535 and dedupes keys within it (ON CONFLICT cannot touch a row twice).
	const perStatement = 500
	seen := map[string]int{}
	var batch [][]any
	var msgs []T
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		var sb strings.Builder
		var args []any
		sb.WriteString("INSERT INTO " + p.Spec.Table + " AS cur (")
		for _, c := range cols {
			sb.WriteString(`"` + c + `", `)
		}
		sb.WriteString(`"` + ver + `") VALUES `)
		for i, vals := range batch {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString("(")
			for j, c := range cols {
				sb.WriteString("CAST(? AS " + casts[c] + "), ")
				args = append(args, vals[j])
			}
			sb.WriteString(versionExpr + ")")
		}
		sb.WriteString(` ON CONFLICT ("` + p.Spec.KeyColumn + `") DO UPDATE SET `)
		for _, c := range cols[1:] {
			sb.WriteString(`"` + c + `" = EXCLUDED."` + c + `", `)
		}
		sb.WriteString(`"` + ver + `" = GREATEST(EXCLUDED."` + ver + `", cur."` + ver + `" + 1)`)
		sb.WriteString(` RETURNING "` + p.Spec.KeyColumn + `" AS key, "` + ver + `" AS version`)
		var got []struct {
			Key     string
			Version int64
		}
		if err := db.Raw(sb.String(), args...).Scan(&got).Error; err != nil {
			return err
		}
		versions := make(map[string]int64, len(got))
		for _, g := range got {
			versions[g.Key] = g.Version
		}
		stored := make([]StoredRow, len(batch))
		for i, vals := range batch {
			key := vals[0].(string)
			stored[i] = StoredRow{Key: key, Version: versions[key], Row: msgs[i]}
		}
		out.merge(Committed{Stored: stored})
		batch, msgs, seen = batch[:0], msgs[:0], map[string]int{}
		return nil
	}
	for _, m := range rows.Upsert {
		row, err := p.row(m)
		if err != nil {
			return out, err
		}
		vals := make([]any, len(cols))
		for i, c := range cols {
			v := row[c]
			switch v.(type) {
			case []string, []any:
				if v, err = pgArray(v); err != nil {
					return out, err
				}
			}
			vals[i] = v
		}
		key := vals[0].(string)
		if i, dup := seen[key]; dup {
			batch[i], msgs[i] = vals, m // the last row for a key wins
			continue
		}
		seen[key] = len(batch)
		batch, msgs = append(batch, vals), append(msgs, m)
		if len(batch) == perStatement {
			if err := flush(); err != nil {
				return out, err
			}
		}
	}
	if err := flush(); err != nil {
		return out, err
	}
	return out, nil
}

func columnNames(cs []ProjectionColumn) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Column
	}
	return out
}

// rangeClause is the SQL keeping rows whose range contains in.Range.
//
// Scoped by equality on a column leading a B-tree index (an organization and
// a day, say), the test is written coalesce(range @> r, false): the same rows
// (WHERE drops a NULL as it drops false), but an expression PostgreSQL gives
// a flat selectivity instead of multiplying the range's into the
// equalities'. Multiplied, a day where nearly every row is free is expected
// to hold a handful of matches, and the planner bitmap-scans and sorts all of
// them before the LIMIT rather than read the index that already holds them
// in page order. Unscoped, the bare test stays, so the GiST index on the
// range serves it.
func (p *Projection[T]) rangeClause(in SearchInput, scoped bool) (string, []any, error) {
	if in.Range == nil {
		return "", nil, nil
	}
	if p.Spec.Range == "" {
		return "", nil, fmt.Errorf("%w: projection %s has no range to search", ErrInvalidArgument, p.Spec.Name)
	}
	test := p.Spec.Table + `."` + p.Spec.Range + `" @> CAST(? AS tstzrange)`
	if scoped {
		test = "coalesce(" + test + ", false)"
	}
	return test, []any{FormatRange(*in.Range)}, nil
}

// scoped reports whether conds hold, at the top level, an equality (= or
// any-of) on a column leading a B-tree index.
func (p *Projection[T]) scoped(conds []filterx.Condition) bool {
	for _, c := range conds {
		if c.Hint || (c.Op != filterx.OpEq && c.Op != filterx.OpIn) {
			continue
		}
		if f, ok := p.Spec.Filter.Fields[c.Field]; ok && f.Via == nil && slices.Contains(p.Spec.IndexLeads, f.Column) {
			return true
		}
	}
	return false
}

// whereGorm is the search's conditions as one SQL clause.
func (p *Projection[T]) whereGorm(in SearchInput) (string, []any, error) {
	conds, err := filterx.Parse(in.Filter)
	if err != nil {
		return "", nil, MapFilterxErr(err)
	}
	conds = append(conds, in.Where...)
	where, args, err := filterx.Gorm[struct{}](p.Spec.Filter).Where(conds)
	if err != nil {
		return "", nil, MapFilterxErr(err)
	}
	rc, rargs, err := p.rangeClause(in, p.scoped(conds))
	if err != nil {
		return "", nil, err
	}
	var parts []string
	if rc != "" {
		parts, args = append(parts, rc), append(rargs, args...)
	}
	if where != "" {
		parts = append(parts, where)
	}
	return strings.Join(parts, " AND "), args, nil
}

// docColumn is the row as one jsonb document, which decode reads: the
// columns mask reads (every column for an empty mask), the key fields', and
// the values the page token needs -- each term's column, and for a JSON path
// term its typed value keyed by the term's name. Selecting only those keeps
// a narrow read (a candidate read asks for one column) from serializing the
// range, arrays and JSON of every row.
func (p *Projection[T]) docColumn(mask []string, terms []filterx.OrderTerm) string {
	col := func(c string) string { return p.Spec.Table + `."` + c + `"` }
	var pairs []string
	seen := map[string]bool{}
	add := func(key, expr string) {
		if !seen[key] {
			seen[key] = true
			pairs = append(pairs, "'"+key+"', "+expr)
		}
	}
	if len(mask) > 0 {
		for _, c := range p.Spec.Columns {
			if slices.Contains(p.Spec.Key, c.Field) || Reads(mask, c.Field) {
				add(c.Column, col(c.Column))
			}
		}
		for _, t := range terms {
			if len(t.Path) == 0 {
				add(t.Column, col(t.Column))
			}
		}
	}
	for _, t := range terms {
		if len(t.Path) > 0 {
			add(t.Name(), filterx.SQLTermExpr(p.Spec.Filter, t))
		}
	}
	// jsonb_build_object takes at most 100 arguments: 50 pairs per call.
	var parts []string
	if len(mask) == 0 {
		table := p.Spec.Table
		if i := strings.LastIndex(table, "."); i >= 0 {
			table = table[i+1:]
		}
		parts = append(parts, "to_jsonb("+table+".*)")
	}
	for chunk := range slices.Chunk(pairs, 50) {
		parts = append(parts, "jsonb_build_object("+strings.Join(chunk, ", ")+")")
	}
	return strings.Join(parts, " || ")
}

// Search returns one page of rows whose range contains in.Range and that
// match in's filter, in in.OrderBy order, and the next page's token. Paging
// is keyset, on the order's columns plus the key, as for every generated
// list. in.ReadMask narrows each row to the fields it names (and the key
// fields).
func (p *Projection[T]) Search(ctx context.Context, conn Conn, in SearchInput) ([]T, string, error) {
	if err := CheckReadMask(p.New(), in.ReadMask); err != nil {
		return nil, "", err
	}
	rows, next, key, err := p.rows(ctx, conn, in)
	if err != nil {
		return nil, "", err
	}
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		m, err := p.decode(r, key)
		if err != nil {
			return nil, "", err
		}
		p.Narrow(m, in.ReadMask)
		out = append(out, m)
	}
	return out, next, nil
}

// Narrow clears every field of m that mask does not read, keeping the key
// fields. An empty mask keeps everything.
func (p *Projection[T]) Narrow(m T, mask []string) {
	if len(mask) == 0 {
		return
	}
	r := m.ProtoReflect()
	var drop []protoreflect.FieldDescriptor
	r.Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !slices.Contains(p.Spec.Key, string(fd.Name())) && !Reads(mask, string(fd.Name())) {
			drop = append(drop, fd)
		}
		return true
	})
	for _, fd := range drop {
		r.Clear(fd)
	}
}

// rows reads one page of raw rows matching in and the next page's token;
// key maps a column to a row's key for it.
func (p *Projection[T]) rows(ctx context.Context, conn Conn, in SearchInput) ([]map[string]any, string, filterx.ColumnKey, error) {
{{- if .GraphQLModule}}
	if conn.GraphQL != nil {
		rows, next, err := p.rowsGraphQL(ctx, conn, in)
		return rows, next, filterx.CamelCase, err
	}
{{- end}}
	terms, err := filterx.OrderTerms(p.Spec.Filter, in.OrderBy)
	if err != nil {
		return nil, "", nil, MapFilterxErr(err)
	}
	where, args, err := p.whereGorm(in)
	if err != nil {
		return nil, "", nil, err
	}
	eng := filterx.Gorm[struct{}](p.Spec.Filter)
	seek, seekArgs, err := eng.Seek(in.OrderBy, in.PageToken)
	if err != nil {
		return nil, "", nil, MapFilterxErr(err)
	}
	order, _ := eng.OrderClause(in.OrderBy)
	limit := filterx.PageLimit(filterx.ListInput{PageSize: in.PageSize})
	q := conn.Gorm.WithContext(ctx).Table(p.Spec.Table).Select(p.docColumn(in.ReadMask, terms) + " AS doc")
	if where != "" {
		q = q.Where(where, args...)
	}
	if seek != "" {
		q = q.Where(seek, seekArgs...)
	}
	var docs []string
	if err := q.Order(order).Limit(limit+1).Pluck("doc", &docs).Error; err != nil {
		return nil, "", nil, MapGormErr(err)
	}
	rows := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		var row map[string]any
		dec := json.NewDecoder(strings.NewReader(d))
		dec.UseNumber()
		if err := dec.Decode(&row); err != nil {
			return nil, "", nil, err
		}
		rows = append(rows, row)
	}
	rows, next, err := nextPage(rows, limit, terms, filterx.SameColumn)
	return rows, next, filterx.SameColumn, err
}

// nextPage trims a limit+1 page of rows and mints the next page's token.
func nextPage(rows []map[string]any, limit int, terms []filterx.OrderTerm, key filterx.ColumnKey) ([]map[string]any, string, error) {
	if len(rows) <= limit {
		return rows, "", nil
	}
	rows = rows[:limit]
	vals := make([]*string, len(terms))
	last := rows[len(rows)-1]
	for i, t := range terms {
		vals[i] = cursorText(last[key(t.Name())])
	}
	next, err := filterx.EncodeCursor(terms, vals)
	return rows, next, err
}

// Stored yields every row of the table whose version is at least since (0
// for all), with its key and version, a page of 1,000 at a time in key
// order: the source a copy of the projection elsewhere is rebuilt from (the
// OpenSearch indexer's Reindex).
func (p *Projection[T]) Stored(ctx context.Context, conn Conn, since int64) iter.Seq2[StoredRow, error] {
	return func(yield func(StoredRow, error) bool) {
		in := SearchInput{PageSize: 1000}
		for {
			rows, next, key, err := p.rows(ctx, conn, in)
			if err != nil {
				yield(StoredRow{}, err)
				return
			}
			for _, r := range rows {
				version, err := strconv.ParseInt(fmt.Sprint(r[key(p.Spec.VersionColumn)]), 10, 64)
				if err != nil {
					yield(StoredRow{}, fmt.Errorf("projection %s: row version: %w", p.Spec.Name, err))
					return
				}
				if version < since {
					continue
				}
				m, err := p.decode(r, key)
				if err != nil {
					yield(StoredRow{}, err)
					return
				}
				id, _ := r[key(p.Spec.KeyColumn)].(string)
				if !yield(StoredRow{Key: id, Version: version, Row: m}, nil) {
					return
				}
			}
			if next == "" {
				return
			}
			in.PageToken = next
		}
	}
}

// Values encodes m as the projection's row: column values keyed by column,
// the key column included. Arrays are slices, JSON documents JSON text,
// dates YYYY-MM-DD, timestamps RFC 3339, ranges PostgreSQL range text.
func (p *Projection[T]) Values(m T) (map[string]any, error) { return p.row(m) }

// Decode builds a message from row, keyed by column as Values returns it
// (JSON documents may also be decoded values).
func (p *Projection[T]) Decode(row map[string]any) (T, error) {
	return p.decode(row, filterx.SameColumn)
}

// cursorText is a decoded value in the text form a page token carries.
func cursorText(v any) *string {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	s := fmt.Sprint(rv.Interface())
	return &s
}

// Count returns how many rows match in (its page fields ignored).
func (p *Projection[T]) Count(ctx context.Context, conn Conn, in SearchInput) (int64, error) {
{{- if .GraphQLModule}}
	if conn.GraphQL != nil {
		return p.countGraphQL(ctx, conn, in)
	}
{{- end}}
	where, args, err := p.whereGorm(in)
	if err != nil {
		return 0, err
	}
	q := conn.Gorm.WithContext(ctx).Table(p.Spec.Table)
	if where != "" {
		q = q.Where(where, args...)
	}
	var n int64
	return n, MapGormErr(q.Count(&n).Error)
}

// Facet counts the rows matching in by the values of field -- a filter field
// of the projection (text, enum, reference, number, bool, date, timestamp in
// FacetTimeLayout), an array field (each element counts), or a JSON attribute
// path -- most frequent first, at most limit values (all when limit is not
// positive).
func (p *Projection[T]) Facet(ctx context.Context, conn Conn, in SearchInput, field string, limit int) ([]Bucket, error) {
	expr, err := p.facetExpr(field)
	if err != nil {
		return nil, err
	}
{{- if .GraphQLModule}}
	if conn.GraphQL != nil {
		return p.facetGraphQL(ctx, conn, in, field, limit)
	}
{{- end}}
	where, args, err := p.whereGorm(in)
	if err != nil {
		return nil, err
	}
	sql := "SELECT v AS value, count(*) AS count FROM (SELECT " + expr + " AS v FROM " + p.Spec.Table
	if where != "" {
		sql += " WHERE " + where
	}
	sql += ") f WHERE v IS NOT NULL GROUP BY v ORDER BY count DESC, value"
	if limit > 0 {
		sql += " LIMIT " + strconv.Itoa(limit)
	}
	var out []Bucket
	return out, MapGormErr(conn.Gorm.WithContext(ctx).Raw(sql, args...).Scan(&out).Error)
}

// facetExpr is the SQL expression yielding field's values, one per row (one
// per element for an array).
func (p *Projection[T]) facetExpr(field string) (string, error) {
	col := func(c string) string { return p.Spec.Table + `."` + c + `"` }
	if f, ok := p.Spec.Filter.Fields[field]; ok && f.Via == nil {
		switch f.Kind {
		case filterx.KindTags:
			return "unnest(" + col(f.Column) + ")::text", nil
		case filterx.KindJSON:
			return "", fmt.Errorf("%w: facet on a path under %q, not the document", ErrInvalidArgument, field)
		default:
			return FacetText(col(f.Column), f.Kind), nil
		}
	}
	path, ok, err := filterx.LookupJSON(p.Spec.Filter, field)
	if err != nil {
		return "", MapFilterxErr(err)
	}
	if !ok {
		return "", fmt.Errorf("%w: cannot facet by %q", ErrInvalidArgument, field)
	}
	// LookupJSON admits only [A-Za-z0-9_-] segments, so the path is safe
	// inline.
	return col(path.Field.Column) + " #>> '{" + strings.Join(path.Path, ",") + "}'", nil
}
{{- if .GraphQLModule}}

// --- GraphQL -----------------------------------------------------------------

// RowMap reads a generated client row into a map keyed by its camelCase field
// names (the graphql struct tags), for a projection to decode.
func RowMap(row any) map[string]any {
	out := map[string]any{}
	rv := reflect.Indirect(reflect.ValueOf(row))
	if rv.Kind() != reflect.Struct {
		return out
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		name := filterx.GraphQLColumn(rt.Field(i))
		if name == "" {
			continue
		}
		f := rv.Field(i)
		if (f.Kind() == reflect.Pointer || f.Kind() == reflect.Interface || f.Kind() == reflect.Slice || f.Kind() == reflect.Map) && f.IsNil() {
			out[name] = nil
			continue
		}
		if b, err := json.Marshal(f.Interface()); err == nil {
			var v any
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.UseNumber()
			if dec.Decode(&v) == nil {
				out[name] = v
				continue
			}
		}
		out[name] = f.Interface()
	}
	return out
}

// Remarshal copies a camelCase row map into a generated client input through
// JSON, so the input's own field types decide each value's encoding.
func Remarshal(row map[string]any, input any) error {
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, input)
}

// gqlRow is row keyed by the client's camelCase field names.
func gqlRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[filterx.CamelCase(k)] = v
	}
	return out
}

func (p *Projection[T]) gql() (*ProjectionGraphQL, error) {
	if p.GraphQL == nil {
		return nil, fmt.Errorf("projection %s: the GraphQL client has no handler for %s", p.Spec.Name, p.Spec.Table)
	}
	return p.GraphQL, nil
}

// queueGraphQL adds rows' mutations to ops -- prunes and deletes by key,
// then each upsert as a delete-and-insert (DDN has no upsert), in one request
// -- and reports what they will do once ops commit. Versions come from this
// process's clock: the engine cannot raise a stored version under the row's
// lock, so two writers racing on one row may version it out of order.
func (p *Projection[T]) queueGraphQL(ctx context.Context, conn Conn, ops *Ops, rows ProjectionRows[T]) (Committed, error) {
	out := Committed{Projection: p.Spec.Name}
	g, err := p.gql()
	if err != nil {
		return out, err
	}
	version := time.Now().UnixMicro()
	gone := map[string]bool{}
	remove := func(id string) {
		if !gone[id] {
			gone[id] = true
			g.Delete(conn, ops, id)
			out.merge(Committed{Removed: []RemovedRow{ {{- `{Key: id, Version: version}` -}} }})
		}
	}
	for _, conds := range rows.Prune {
		ids, err := p.idsGraphQL(ctx, conn, conds)
		if err != nil {
			return out, err
		}
		for _, id := range ids {
			remove(id)
		}
	}
	for _, m := range rows.Delete {
		id, err := p.Key(m)
		if err != nil {
			return out, err
		}
		remove(id)
	}
	for _, m := range rows.Upsert {
		row, err := p.row(m)
		if err != nil {
			return out, err
		}
		id := row[p.Spec.KeyColumn].(string)
		remove(id)
		row[p.Spec.VersionColumn] = version
		if err := g.Insert(conn, ops, gqlRow(row)); err != nil {
			return out, err
		}
		out.merge(Committed{Stored: []StoredRow{ {{- `{Key: id, Version: version, Row: m}` -}} }})
	}
	return out, nil
}

// idsGraphQL lists the keys of every row matching conds.
func (p *Projection[T]) idsGraphQL(ctx context.Context, conn Conn, conds []filterx.Condition) ([]string, error) {
	var ids []string
	in := SearchInput{Where: conds, PageSize: 1000}
	for {
		rows, next, err := p.rowsGraphQL(ctx, conn, in)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if id, ok := r[filterx.CamelCase(p.Spec.KeyColumn)].(string); ok {
				ids = append(ids, id)
			}
		}
		if next == "" {
			return ids, nil
		}
		in.PageToken = next
	}
}

// rowsGraphQL reads one page of raw rows matching in. The range is not
// expressible in the engine's schema (no range operators), so it is checked
// here, on the rows read: pages may come back short, even empty, with a
// next-page token -- the page boundary is the rows read, as for a
// value-object filter (filterx.HasuraEngine.Via).
func (p *Projection[T]) rowsGraphQL(ctx context.Context, conn Conn, in SearchInput) ([]map[string]any, string, error) {
	g, err := p.gql()
	if err != nil {
		return nil, "", err
	}
	conds, err := filterx.Parse(in.Filter)
	if err != nil {
		return nil, "", MapFilterxErr(err)
	}
	conds = append(conds, in.Where...)
	eng := filterx.Hasura[map[string]any](p.Spec.Filter, nil)
	where, hasWhere, err := eng.Predicate(conds)
	if err != nil {
		return nil, "", MapFilterxErr(err)
	}
	terms, err := filterx.OrderTerms(p.Spec.Filter, in.OrderBy)
	if err != nil {
		return nil, "", MapFilterxErr(err)
	}
	order, err := eng.OrderTerms(in.OrderBy)
	if err != nil {
		return nil, "", MapFilterxErr(err)
	}
	seek, hasSeek, err := eng.Seek(in.OrderBy, in.PageToken)
	if err != nil {
		return nil, "", MapFilterxErr(err)
	}
	var preds []graphql.Predicate
	if hasWhere {
		preds = append(preds, where)
	}
	if hasSeek {
		preds = append(preds, seek)
	}
	limit := filterx.PageLimit(filterx.ListInput{PageSize: in.PageSize})
	req := (&graphql.ListRequest{}).Limit(limit + 1).OrderBy(order...)
	if len(preds) > 0 {
		req = req.Where(graphql.And(preds...))
	}
	rows, err := g.List(ctx, conn, req)
	if err != nil {
		return nil, "", MapGraphQLErr(err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		vals := make([]*string, len(terms))
		for i, t := range terms {
			vals[i] = cursorText(rows[len(rows)-1][filterx.CamelCase(t.Column)])
		}
		if next, err = filterx.EncodeCursor(terms, vals); err != nil {
			return nil, "", err
		}
	}
	if in.Range != nil {
		if p.Spec.Range == "" {
			return nil, "", fmt.Errorf("%w: projection %s has no range to search", ErrInvalidArgument, p.Spec.Name)
		}
		rows = slices.DeleteFunc(rows, func(r map[string]any) bool {
			s, _ := r[filterx.CamelCase(p.Spec.Range)].(string)
			spans, err := ParseMultirange(s)
			return err != nil || !anyContains(spans, *in.Range)
		})
	}
	return rows, next, nil
}

// anyContains reports whether one of spans covers w.
func anyContains(spans []Span, w Span) bool {
	for _, s := range spans {
		startOK := s.Start.IsZero() || (!w.Start.IsZero() && !w.Start.Before(s.Start))
		endOK := s.End.IsZero() || (!w.End.IsZero() && !w.End.After(s.End))
		if startOK && endOK {
			return true
		}
	}
	return false
}

// GraphQLScanLimit bounds the rows a GraphQL Count with a range, or a GraphQL
// Facet, reads to answer: the engine cannot filter by range or group, so they
// are answered from the rows themselves, and past this many the call fails
// with ErrInvalidArgument rather than read the table.
var GraphQLScanLimit = 10000

// scanGraphQL calls f for every row matching in, up to GraphQLScanLimit.
func (p *Projection[T]) scanGraphQL(ctx context.Context, conn Conn, in SearchInput, f func(map[string]any)) error {
	in.PageSize, in.PageToken = 1000, ""
	read := 0
	for {
		rows, next, err := p.rowsGraphQL(ctx, conn, in)
		if err != nil {
			return err
		}
		read += 1000
		for _, r := range rows {
			f(r)
		}
		if next == "" {
			return nil
		}
		if read >= GraphQLScanLimit {
			return fmt.Errorf("%w: more than %d rows to scan on GraphQL; narrow the search", ErrInvalidArgument, GraphQLScanLimit)
		}
		in.PageToken = next
	}
}

func (p *Projection[T]) countGraphQL(ctx context.Context, conn Conn, in SearchInput) (int64, error) {
	g, err := p.gql()
	if err != nil {
		return 0, err
	}
	if in.Range == nil {
		conds, err := filterx.Parse(in.Filter)
		if err != nil {
			return 0, MapFilterxErr(err)
		}
		where, has, err := filterx.Hasura[map[string]any](p.Spec.Filter, nil).Predicate(append(conds, in.Where...))
		if err != nil {
			return 0, MapFilterxErr(err)
		}
		if !has {
			return g.Count(ctx, conn, nil)
		}
		return g.Count(ctx, conn, &where)
	}
	var n int64
	err = p.scanGraphQL(ctx, conn, in, func(map[string]any) { n++ })
	return n, err
}

func (p *Projection[T]) facetGraphQL(ctx context.Context, conn Conn, in SearchInput, field string, limit int) ([]Bucket, error) {
	values := func(r map[string]any) []string { return nil }
	if f, ok := p.Spec.Filter.Fields[field]; ok {
		key := filterx.CamelCase(f.Column)
		values = func(r map[string]any) []string {
			v := r[key]
			if v == nil {
				return nil
			}
			if items, err := arrayItems(v); err == nil && f.Kind == filterx.KindTags {
				out := make([]string, 0, len(items))
				for _, x := range items {
					out = append(out, fmt.Sprint(x))
				}
				return out
			}
			return []string{FacetValue(f.Kind, v)}
		}
	} else if path, ok, _ := filterx.LookupJSON(p.Spec.Filter, field); ok {
		key := filterx.CamelCase(path.Field.Column)
		values = func(r map[string]any) []string {
			raw, err := jsonRaw(r[key])
			if err != nil {
				return nil
			}
			var doc any
			if json.Unmarshal(raw, &doc) != nil {
				return nil
			}
			for _, seg := range path.Path {
				m, ok := doc.(map[string]any)
				if !ok {
					return nil
				}
				doc = m[seg]
			}
			if doc == nil {
				return nil
			}
			if s, ok := doc.(string); ok {
				return []string{s}
			}
			b, _ := json.Marshal(doc)
			return []string{string(b)}
		}
	}
	counts := map[string]int64{}
	if err := p.scanGraphQL(ctx, conn, in, func(r map[string]any) {
		for _, v := range values(r) {
			counts[v]++
		}
	}); err != nil {
		return nil, err
	}
	out := make([]Bucket, 0, len(counts))
	for v, n := range counts {
		out = append(out, Bucket{Value: v, Count: n})
	}
	slices.SortFunc(out, func(a, b Bucket) int {
		if a.Count != b.Count {
			if a.Count > b.Count {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Value, b.Value)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
{{- end}}
