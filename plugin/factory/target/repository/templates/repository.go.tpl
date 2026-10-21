{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

{{- range .Resources}}

// {{.Model}}Repository is the provider-agnostic persistence surface for
// {{.Model}} ({{.Pattern}}): full-message writes, AIP-160/AIP-132 lists with
// opaque page tokens, field-mask updates, and etag optimistic concurrency.
type {{.Model}}Repository interface {
{{- if .Parented}}
	// Create persists m under parent and returns the stored record.
	Create(ctx context.Context, parent string, m *{{.PB}}) (*{{.PB}}, error)
{{- else}}
	// Create persists m and returns the stored record.
	Create(ctx context.Context, m *{{.PB}}) (*{{.PB}}, error)
{{- end}}
	// Get returns the record addressed by its resource name, or
	// repox.ErrNotFound.
	Get(ctx context.Context, name string) (*{{.PB}}, error)
{{- if .Parented}}
	// List returns one page of records under parent.
	List(ctx context.Context, parent string, in repox.ListInput) ([]*{{.PB}}, string, error)
{{- else}}
	// List returns one page of records.
	List(ctx context.Context, in repox.ListInput) ([]*{{.PB}}, string, error)
{{- end}}
	// Update persists the masked fields of m; an empty mask replaces every
	// mutable field. m.Etag, when set, guards against concurrent writes
	// (repox.ErrConflict).
	Update(ctx context.Context, m *{{.PB}}, paths []string) (*{{.PB}}, error)
{{- if .OutputOnly}}
	// UpdateOutputOnly persists the server-owned (OUTPUT_ONLY) fields of m that
	// paths names -- a state, an expiry, a computed list. Update refuses them,
	// since a client may never set them; this is for the service that owns
	// them. paths must name at least one; m.Etag guards as in Update.
	UpdateOutputOnly(ctx context.Context, m *{{.PB}}, paths []string) (*{{.PB}}, error)
{{- end}}
{{- if .SoftDelete}}
	// Delete soft-deletes the record addressed by its resource name (AIP-164):
	// Get still returns it, with delete_time set; List skips it unless
	// ShowDeleted. repox.ErrNotFound when absent or already deleted.
	Delete(ctx context.Context, name string) error
	// Undelete restores a soft-deleted record and returns it;
	// repox.ErrAlreadyExists when it is not deleted.
	Undelete(ctx context.Context, name string) (*{{.PB}}, error)
	// Purge removes the record, deleted or not, for good.
	Purge(ctx context.Context, name string) error
{{- else}}
	// Delete removes the record addressed by its resource name.
	Delete(ctx context.Context, name string) error
{{- end}}
}

// {{.Model}}Hooks lets custom logic run inside the generated {{.Model}}
// adapters without editing them: normalization/derivation before writes,
// derived fields after reads, and guards before deletes. Nil funcs are skipped.
type {{.Model}}Hooks struct {
	// BeforeCreate runs after name/id resolution, before the row is written.
	BeforeCreate func(ctx context.Context, m *{{.PB}}) error
	// AfterRead runs on every record a read returns (Get, List, and the
	// re-reads writes return).
	AfterRead func(ctx context.Context, m *{{.PB}}) error
	// BeforeUpdate runs after the mask is applied to the merged record,
	// before it is written.
	BeforeUpdate func(ctx context.Context, existing, merged *{{.PB}}, paths []string) error
	// BeforeDelete runs before the row is removed (or, with soft delete,
	// marked deleted); returning an error vetoes.
	BeforeDelete func(ctx context.Context, name string) error
}
{{- end}}

// Repositories bundles this schema's repository surfaces behind one factory.
type Repositories struct {
{{- range .Resources}}
	{{.PluralField}} {{.Model}}Repository
{{- end}}
}

{{- if .HasBatch}}
// Batch queues writes across this schema's single-row resources and applies
// them together on Commit, so a record and the records that depend on it land
// whole or not at all. Hooks do not run for queued writes.
type Batch interface {
{{- range .Resources}}{{if .Flat}}
	// Create{{.Model}} queues a create and returns the name the record will have.
	Create{{.Model}}({{if .Parented}}parent string, {{end}}m *{{.PB}}) (string, error)
{{- if .OutputOnly}}
	// Update{{.Model}}OutputOnly queues UpdateOutputOnly; m.Etag, when set,
	// guards it.
	Update{{.Model}}OutputOnly(m *{{.PB}}, paths []string)
{{- end}}
	// Delete{{.Model}} queues the record's removal for good; a record already
	// gone is not an error.
	Delete{{.Model}}(name string)
{{- end}}{{end}}
	// Commit applies every queued write. GORM runs them in one transaction:
	// all land or none do. GraphQL first applies the etag-guarded updates, one
	// by one, returning repox.ErrConflict at the first that fails and before
	// anything else is written -- the engine reports a failed guard by touching
	// no row, which cannot abort a batch -- then commits the rest as one atomic
	// mutation batch.
	Commit(ctx context.Context) error
}

// NewBatch returns a Batch on the backend conn carries. When conn carries a
// ChangeObserver, Commit reports every record it queued to it.
func NewBatch(conn repox.Conn) Batch {
	var b Batch = &gormBatch{db: conn.Gorm}
{{- if .HasGraphQL}}
	if conn.GraphQL != nil {
		b = &graphQLBatch{svc: conn.GraphQL}
	}
{{- end}}
	if conn.Changes != nil {
		b = &observedBatch{Batch: b, changes: conn.Changes}
	}
	return b
}
{{- end}}

// options collects the per-resource customizations the factories thread into
// the adapters they build.
type options struct {
{{- range .Resources}}
	{{.LowerModel}}Hooks     {{.Model}}Hooks
	{{.LowerModel}}Overrides map[string]filterx.SQLHandler
{{- end}}
{{- if .HasGraphQL}}
{{- range .Resources}}
	{{.LowerModel}}GQLOverrides map[string]filterx.GraphQLHandler
{{- end}}
{{- end}}
}

// Option customizes the adapters a factory builds.
type Option func(*options)

{{- range .Resources}}

// With{{.Model}}Hooks installs h on the {{.Model}} adapters.
func With{{.Model}}Hooks(h {{.Model}}Hooks) Option {
	return func(o *options) { o.{{.LowerModel}}Hooks = h }
}

// With{{.Model}}ListOverride substitutes the generated filter dispatch for one
// {{.Model}} filter field (e.g. a derived state computed via subqueries).
func With{{.Model}}ListOverride(field string, h filterx.SQLHandler) Option {
	return func(o *options) {
		if o.{{.LowerModel}}Overrides == nil {
			o.{{.LowerModel}}Overrides = map[string]filterx.SQLHandler{}
		}
		o.{{.LowerModel}}Overrides[field] = h
	}
}
{{- end}}

{{- if .HasGraphQL}}

{{- range .Resources}}

// With{{.Model}}GraphQLListOverride substitutes the generated filter dispatch
// for one {{.Model}} filter field on the GraphQL adapters.
func With{{.Model}}GraphQLListOverride(field string, h filterx.GraphQLHandler) Option {
	return func(o *options) {
		if o.{{.LowerModel}}GQLOverrides == nil {
			o.{{.LowerModel}}GQLOverrides = map[string]filterx.GraphQLHandler{}
		}
		o.{{.LowerModel}}GQLOverrides[field] = h
	}
}
{{- end}}

// New picks the adapter set for the live handle in conn: GraphQL when a
// client connection is present, otherwise GORM.
//
// When conn carries a ChangeObserver, every write the returned repositories
// make is reported to it (see observe).
func New(conn repox.Conn, opts ...Option) Repositories {
	if conn.GraphQL != nil {
		return observe(NewGraphQL(conn.GraphQL, opts...), conn.Changes)
	}
	return observe(NewGorm(conn.Gorm, opts...), conn.Changes)
}

// NewGraphQL builds the GraphQL adapter set over svc.
func NewGraphQL(svc *{{.ClientPkg}}.Service, opts ...Option) Repositories {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return Repositories{
{{- range .Resources}}
		{{.PluralField}}: &GraphQL{{.Model}}Repository{Svc: svc, Hooks: o.{{.LowerModel}}Hooks, ListOverrides: o.{{.LowerModel}}GQLOverrides},
{{- end}}
	}
}
{{- else}}

// New picks the adapter set for the live handle in conn.
//
// When conn carries a ChangeObserver, every write the returned repositories
// make is reported to it (see observe).
func New(conn repox.Conn, opts ...Option) Repositories {
	return observe(NewGorm(conn.Gorm, opts...), conn.Changes)
}
{{- end}}

// NewGorm builds the GORM adapter set over db.
func NewGorm(db *gorm.DB, opts ...Option) Repositories {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return Repositories{
{{- range .Resources}}
		{{.PluralField}}: &Gorm{{.Model}}Repository{DB: db, Hooks: o.{{.LowerModel}}Hooks, ListOverrides: o.{{.LowerModel}}Overrides},
{{- end}}
	}
}

// observe wraps every repository in repos so its writes are reported to
// changes. With no observer it returns repos unchanged, so a process that wires
// none pays nothing.
//
// The wrapper sits over the whole interface, not inside the adapters, so the
// same notification covers GORM and GraphQL alike -- and a Tier-2 adapter that
// embeds a generated one and overrides a write is covered too.
func observe(repos Repositories, changes repox.ChangeObserver) Repositories {
	if changes == nil {
		return repos
	}
	return Repositories{
{{- range .Resources}}
		{{.PluralField}}: &observed{{.Model}}Repository{ {{- .Model}}Repository: repos.{{.PluralField}}, changes: changes},
{{- end}}
	}
}
{{- range .Resources}}

// observed{{.Model}}Repository reports every {{.Model}} write to a ChangeObserver.
//
// It reports a write that returned an error too, whenever the name is known.
// An error does not prove nothing was written -- the adapters re-read after
// writing, and that re-read can fail -- and an observer told about a write
// that did not happen only drops something it did not need to, while one not
// told about a write that did keeps serving what the write replaced.
type observed{{.Model}}Repository struct {
	{{.Model}}Repository
	changes repox.ChangeObserver
}

{{- if .Parented}}

func (o *observed{{.Model}}Repository) Create(ctx context.Context, parent string, m *{{.PB}}) (*{{.PB}}, error) {
	out, err := o.{{.Model}}Repository.Create(ctx, parent, m)
	o.created(ctx, m, out)
	return out, err
}
{{- else}}

func (o *observed{{.Model}}Repository) Create(ctx context.Context, m *{{.PB}}) (*{{.PB}}, error) {
	out, err := o.{{.Model}}Repository.Create(ctx, m)
	o.created(ctx, m, out)
	return out, err
}
{{- end}}

// created reports a Create: by the stored record's name, or failing that by
// the name the caller asked for. A Create that failed before naming anything
// wrote nothing.
func (o *observed{{.Model}}Repository) created(ctx context.Context, m, out *{{.PB}}) {
	switch {
	case out.GetName() != "":
		o.changes.Changed(ctx, out.GetName())
	case m.GetName() != "":
		o.changes.Changed(ctx, m.GetName())
	}
}

func (o *observed{{.Model}}Repository) Update(ctx context.Context, m *{{.PB}}, paths []string) (*{{.PB}}, error) {
	out, err := o.{{.Model}}Repository.Update(ctx, m, paths)
	o.changes.Changed(ctx, m.GetName())
	return out, err
}
{{- if .OutputOnly}}

func (o *observed{{.Model}}Repository) UpdateOutputOnly(ctx context.Context, m *{{.PB}}, paths []string) (*{{.PB}}, error) {
	out, err := o.{{.Model}}Repository.UpdateOutputOnly(ctx, m, paths)
	o.changes.Changed(ctx, m.GetName())
	return out, err
}
{{- end}}

func (o *observed{{.Model}}Repository) Delete(ctx context.Context, name string) error {
	err := o.{{.Model}}Repository.Delete(ctx, name)
	o.changes.Changed(ctx, name)
	return err
}
{{- if .SoftDelete}}

func (o *observed{{.Model}}Repository) Undelete(ctx context.Context, name string) (*{{.PB}}, error) {
	out, err := o.{{.Model}}Repository.Undelete(ctx, name)
	o.changes.Changed(ctx, name)
	return out, err
}

func (o *observed{{.Model}}Repository) Purge(ctx context.Context, name string) error {
	err := o.{{.Model}}Repository.Purge(ctx, name)
	o.changes.Changed(ctx, name)
	return err
}
{{- end}}
{{- end}}
{{- if .HasBatch}}

// observedBatch reports every record a Batch queued once Commit returns --
// whatever it returns: GraphQL applies a batch's guarded updates one by one
// before the rest, so a Commit that failed may still have written some.
type observedBatch struct {
	Batch
	changes repox.ChangeObserver
	names   []string
}

func (o *observedBatch) Commit(ctx context.Context) error {
	err := o.Batch.Commit(ctx)
	for _, name := range o.names {
		o.changes.Changed(ctx, name)
	}
	o.names = nil
	return err
}
{{- range .Resources}}{{if .Flat}}

func (o *observedBatch) Create{{.Model}}({{if .Parented}}parent string, {{end}}m *{{.PB}}) (string, error) {
	name, err := o.Batch.Create{{.Model}}({{if .Parented}}parent, {{end}}m)
	if err == nil {
		o.names = append(o.names, name)
	}
	return name, err
}
{{- if .OutputOnly}}

func (o *observedBatch) Update{{.Model}}OutputOnly(m *{{.PB}}, paths []string) {
	o.Batch.Update{{.Model}}OutputOnly(m, paths)
	o.names = append(o.names, m.GetName())
}
{{- end}}

func (o *observedBatch) Delete{{.Model}}(name string) {
	o.Batch.Delete{{.Model}}(name)
	o.names = append(o.names, name)
}
{{- end}}{{end}}
{{- end}}
