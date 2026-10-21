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
func New(conn repox.Conn, opts ...Option) Repositories {
	if conn.GraphQL != nil {
		return NewGraphQL(conn.GraphQL, opts...)
	}
	return NewGorm(conn.Gorm, opts...)
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
func New(conn repox.Conn, opts ...Option) Repositories {
	return NewGorm(conn.Gorm, opts...)
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
