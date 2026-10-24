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
	// UpdateWithOutputOnly is Update plus the server-owned (OUTPUT_ONLY)
	// fields of m that outputPaths names, in the same write under the same
	// etag guard. paths keeps Update's meaning (an OUTPUT_ONLY path in it is
	// still ignored, AIP-203). A BeforeUpdate hook that sets one of the
	// outputPaths fields on the merged record has it written too.
	UpdateWithOutputOnly(ctx context.Context, m *{{.PB}}, paths, outputPaths []string) (*{{.PB}}, error)
{{- end}}
{{- if .SoftDelete}}
	// Delete soft-deletes the record addressed by its resource name (AIP-164):
	// Get still returns it, with delete_time set; List skips it unless
	// ShowDeleted. repox.ErrNotFound when absent or already deleted.
	Delete(ctx context.Context, name string) error
	// Undelete restores a soft-deleted record and returns it;
	// repox.ErrAlreadyExists when it is not deleted.
	Undelete(ctx context.Context, name string) (*{{.PB}}, error)
	// Purge removes the record, deleted or not, for good. A record something
	// still refers to -- a foreign key, or a referrer store.v1.ReferenceService
	// reports -- is kept instead: Purge returns nil and
	// repox.References.OnKept hears of it. A purge never fails, and never
	// cascades, because of a referrer.
	Purge(ctx context.Context, name string) error
{{- if .Expiring}}
	// PurgeExpired removes for good every deleted record whose expire_time
	// is at or before now (AIP-164), keeping the ones something still
	// refers to, as Purge does. Hooks do not run.
	PurgeExpired(ctx context.Context, now time.Time) (repox.PurgeResult, error)
{{- end}}
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
// Batch queues writes across this schema's resources -- the value-object rows
// of each included -- and applies them together on Commit, so a record and the
// records that depend on it land whole or not at all. Built over a shared
// repox.Unit (NewBatchIn), it commits with the writes of other schemas too.
// Hooks do not run for queued writes.
type Batch interface {
{{- range .Resources}}
	// Create{{.Model}} queues a create and returns the name the record will have.
	Create{{.Model}}({{if .Parented}}parent string, {{end}}m *{{.PB}}) (string, error)
	// Update{{.Model}} queues Update: the masked fields of m, every mutable
	// field when paths is empty. m.Etag, when set, guards it; a guard that
	// fails fails the whole Commit with repox.ErrConflict.
	Update{{.Model}}(m *{{.PB}}, paths []string)
{{- if .OutputOnly}}
	// Update{{.Model}}OutputOnly queues UpdateOutputOnly; m.Etag, when set,
	// guards it.
	Update{{.Model}}OutputOnly(m *{{.PB}}, paths []string)
	// Update{{.Model}}WithOutputOnly queues UpdateWithOutputOnly: Update plus
	// the server-owned fields outputPaths names, under one guard.
	Update{{.Model}}WithOutputOnly(m *{{.PB}}, paths, outputPaths []string)
{{- end}}
{{- if .SoftDelete}}
	// Delete{{.Model}} queues a soft delete, as {{.Model}}Repository.Delete
	// does; a record already deleted or gone is not an error.
	Delete{{.Model}}(name string)
	// Purge{{.Model}} queues the record's removal for good; a record already
	// gone is not an error.
	Purge{{.Model}}(name string)
{{- else}}
	// Delete{{.Model}} queues the record's removal for good; a record already
	// gone is not an error.
	Delete{{.Model}}(name string)
{{- end}}
{{- end}}
	// Commit applies every write queued in the batch's Unit, by this Batch or
	// any other built over it; see repox.Unit.Commit. GORM runs them in one
	// transaction. GraphQL sends a guarded Update<Model>OutputOnly ahead, one
	// by one, then everything else as one mutation request the engine applies
	// atomically, Update<Model>'s etag guards included.
	Commit(ctx context.Context) error
}

// NewBatch returns a Batch on the backend conn carries, over a Unit of its
// own. When conn carries a ChangeObserver, Commit reports every record it
// queued to it.
func NewBatch(conn repox.Conn, opts ...Option) Batch {
	return NewBatchIn(repox.NewUnit(conn), opts...)
}

// NewBatchIn returns a Batch that queues into u, so its writes commit with
// those of every other Batch over u: a write spanning schemas is one Unit and
// one Batch per schema, committed once.
func NewBatchIn(u *repox.Unit, opts ...Option) Batch {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	_ = o
{{- if .HasGraphQL}}
	if svc := u.Conn().GraphQL; svc != nil {
		return &graphQLBatch{u: u, svc: svc{{if .HasExpiring}}, retention: o.retention{{end}}}
	}
{{- end}}
	return &gormBatch{u: u, db: u.Conn().Gorm{{if .HasExpiring}}, retention: o.retention{{end}}}
}
{{- end}}

// options collects the per-resource customizations the factories thread into
// the adapters they build.
type options struct {
	references *repox.References
{{- range .Resources}}
	{{.LowerModel}}Hooks     {{.Model}}Hooks
	{{.LowerModel}}Overrides map[string]filterx.SQLHandler
{{- end}}
{{- if .HasGraphQL}}
{{- range .Resources}}
	{{.LowerModel}}GQLOverrides map[string]filterx.GraphQLHandler
{{- end}}
{{- end}}
{{- if .HasExpiring}}
	retention time.Duration
{{- end}}
}

// Option customizes the adapters a factory builds.
type Option func(*options)

// WithReferences enforces reference integrity through refs (see
// repox.References): New passes repox.Conn.References. With none, writes do
// not resolve external references and deletes do not consult referrers;
// foreign keys still hold.
func WithReferences(refs *repox.References) Option {
	return func(o *options) { o.references = refs }
}
{{- if .HasExpiring}}

// WithRetention is how long a soft-deleted record stays restorable (AIP-164):
// Delete sets its expire_time that far ahead, in the same write as its
// delete_time, and Undelete clears it. Zero leaves expire_time unset.
func WithRetention(d time.Duration) Option {
	return func(o *options) { o.retention = d }
}
{{- end}}

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
	opts = append([]Option{WithReferences(conn.References)}, opts...)
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
	return guard(Repositories{
{{- range .Resources}}
		{{.PluralField}}: &GraphQL{{.Model}}Repository{Svc: svc, Hooks: o.{{.LowerModel}}Hooks, Refs: o.references, ListOverrides: o.{{.LowerModel}}GQLOverrides{{if .Expiring}}, Retention: o.retention{{end}}},
{{- end}}
	}, o.references)
}
{{- else}}

// New picks the adapter set for the live handle in conn.
//
// When conn carries a ChangeObserver, every write the returned repositories
// make is reported to it (see observe).
func New(conn repox.Conn, opts ...Option) Repositories {
	opts = append([]Option{WithReferences(conn.References)}, opts...)
	return observe(NewGorm(conn.Gorm, opts...), conn.Changes)
}
{{- end}}

// NewGorm builds the GORM adapter set over db.
func NewGorm(db *gorm.DB, opts ...Option) Repositories {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return guard(Repositories{
{{- range .Resources}}
		{{.PluralField}}: &Gorm{{.Model}}Repository{DB: db, Hooks: o.{{.LowerModel}}Hooks, Refs: o.references, ListOverrides: o.{{.LowerModel}}Overrides{{if .Expiring}}, Retention: o.retention{{end}}},
{{- end}}
	}, o.references)
}

// guard wraps every repository in repos so its writes keep reference
// integrity through refs: Create and Update resolve the external references
// they set (repox.References.Resolve) before anything is written, and Delete
// consults the record's referrers (repox.References.CheckDelete) and
// publishes the NOTIFY event after it. Purge's checks sit in the adapters,
// which keep a still-referenced row row by row. With no refs it returns repos
// unchanged.
//
// It sits over the whole interface, like observe, so the GORM and GraphQL
// adapters enforce the same contract above their backends.
func guard(repos Repositories, refs *repox.References) Repositories {
	if refs == nil {
		return repos
	}
	return Repositories{
{{- range .Resources}}
		{{.PluralField}}: &guarded{{.Model}}Repository{ {{- .Model}}Repository: repos.{{.PluralField}}, refs: refs},
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

func (o *observed{{.Model}}Repository) UpdateWithOutputOnly(ctx context.Context, m *{{.PB}}, paths, outputPaths []string) (*{{.PB}}, error) {
	out, err := o.{{.Model}}Repository.UpdateWithOutputOnly(ctx, m, paths, outputPaths)
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
{{- if .Expiring}}

func (o *observed{{.Model}}Repository) PurgeExpired(ctx context.Context, now time.Time) (repox.PurgeResult, error) {
	res, err := o.{{.Model}}Repository.PurgeExpired(ctx, now)
	for _, name := range res.Purged {
		o.changes.Changed(ctx, name)
	}
	return res, err
}
{{- end}}
{{- end}}
{{- end}}


{{- range .Resources}}

// guarded{{.Model}}Repository keeps {{.Model}}'s references whole; see guard.
type guarded{{.Model}}Repository struct {
	{{.Model}}Repository
	refs *repox.References
}
{{- if .ExtRefs}}
{{- if .Parented}}

func (g *guarded{{.Model}}Repository) Create(ctx context.Context, parent string, m *{{.PB}}) (*{{.PB}}, error) {
	if err := resolve{{.Model}}References(ctx, g.refs, m, nil); err != nil {
		return nil, err
	}
	return g.{{.Model}}Repository.Create(ctx, parent, m)
}
{{- else}}

func (g *guarded{{.Model}}Repository) Create(ctx context.Context, m *{{.PB}}) (*{{.PB}}, error) {
	if err := resolve{{.Model}}References(ctx, g.refs, m, nil); err != nil {
		return nil, err
	}
	return g.{{.Model}}Repository.Create(ctx, m)
}
{{- end}}

func (g *guarded{{.Model}}Repository) Update(ctx context.Context, m *{{.PB}}, paths []string) (*{{.PB}}, error) {
	if err := resolve{{.Model}}References(ctx, g.refs, m, paths); err != nil {
		return nil, err
	}
	return g.{{.Model}}Repository.Update(ctx, m, paths)
}
{{- if .OutputOnly}}

func (g *guarded{{.Model}}Repository) UpdateWithOutputOnly(ctx context.Context, m *{{.PB}}, paths, outputPaths []string) (*{{.PB}}, error) {
	if err := resolve{{.Model}}References(ctx, g.refs, m, paths); err != nil {
		return nil, err
	}
	return g.{{.Model}}Repository.UpdateWithOutputOnly(ctx, m, paths, outputPaths)
}
{{- end}}
{{- end}}
{{- if .Type}}

func (g *guarded{{.Model}}Repository) Delete(ctx context.Context, name string) error {
	check, err := g.refs.CheckDelete(ctx, {{printf "%q" .Type}}, name)
	if err != nil {
		return err
	}
	if err := g.{{.Model}}Repository.Delete(ctx, name); err != nil {
		return err
	}
	check.Done(ctx, {{if .SoftDelete}}false{{else}}true{{end}})
	return nil
}
{{- end}}

// resolve{{.Model}}References proves, through refs, that every external
// reference m sets within paths (all of them for an empty mask) names a
// resource that exists.
func resolve{{.Model}}References(ctx context.Context, refs *repox.References, m *{{.PB}}, paths []string) error {
	{{- if .ExtRefs}}
	if refs == nil {
		return nil
	}
	{{- range .ExtRefs}}
	if repox.InMask(paths, {{printf "%q" .Field}}) {
		{{- if .Repeated}}
		for _, name := range m.{{.Getter}}() {
			if err := refs.Resolve(ctx, {{printf "%q" .Field}}, {{printf "%q" .Type}}, name); err != nil {
				return err
			}
		}
		{{- else}}
		if err := refs.Resolve(ctx, {{printf "%q" .Field}}, {{printf "%q" .Type}}, m.{{.Getter}}()); err != nil {
			return err
		}
		{{- end}}
	}
	{{- end}}
	{{- end}}
	return nil
}

// queue{{.Model}}References makes u resolve m's external references within
// paths before it commits, as guard does for the repositories.
func queue{{.Model}}References(u *repox.Unit, m *{{.PB}}, paths []string) {
	{{- if .ExtRefs}}
	if refs := u.Conn().References; refs != nil {
		u.Before(func(ctx context.Context) error { return resolve{{.Model}}References(ctx, refs, m, paths) })
	}
	{{- end}}
}

// queue{{.Model}}Delete makes u consult name's referrers before it commits
// and publish the NOTIFY event after, as guard does for Delete.
func queue{{.Model}}Delete(u *repox.Unit, name string, purged bool) {
	{{- if .Type}}
	refs := u.Conn().References
	if refs == nil {
		return
	}
	var check *repox.DeleteCheck
	u.Before(func(ctx context.Context) (err error) {
		check, err = refs.CheckDelete(ctx, {{printf "%q" .Type}}, name)
		return err
	})
	u.After(func(ctx context.Context) { check.Done(ctx, purged) })
	{{- end}}
}
{{- if .SoftDelete}}

// queue{{.Model}}Purge makes u ask name's referrers before it commits; the
// purge it queues keeps the row when *keep is true.
func queue{{.Model}}Purge(u *repox.Unit, name string) (keep *bool) {
	keep = new(bool)
	{{- if .Type}}
	if refs := u.Conn().References; refs != nil {
		u.Before(func(ctx context.Context) error {
			*keep = refs.KeepOnPurge(ctx, {{printf "%q" .Type}}, name)
			return nil
		})
	}
	{{- end}}
	return keep
}
{{- end}}
{{- end}}
