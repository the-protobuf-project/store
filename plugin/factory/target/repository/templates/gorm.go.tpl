{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

{{- range .Resources}}

// Gorm{{.Model}}Repository is the GORM adapter of {{.Model}}Repository: flat
// CRUD over the generated model, store, converters, and filterx spec. Exported
// fields and helpers are the Tier-2 seam — embed it and override whole methods
// for behavior the schema cannot express; generated methods only call private
// helpers, never the public interface, so overrides never re-enter generated
// code.
type Gorm{{.Model}}Repository struct {
	DB    *gorm.DB
	Hooks {{.Model}}Hooks
	// ListOverrides substitute the generated dispatch for single filter fields
	// (e.g. a derived state computed via subqueries).
	ListOverrides map[string]filterx.SQLHandler
}

// NewGorm{{.Model}}Repository returns the GORM adapter bound to db.
func NewGorm{{.Model}}Repository(db *gorm.DB) *Gorm{{.Model}}Repository {
	return &Gorm{{.Model}}Repository{DB: db}
}

{{if .Parented}}
// Create persists in under parent and returns the stored record.
func (r *Gorm{{.Model}}Repository) Create(ctx context.Context, parent string, in *{{.PB}}) (*{{.PB}}, error) {
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return nil, err
	}
	id := repox.NewULID()
	if in.GetName() != "" {
		ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
		if err != nil {
			return nil, err
		}
		for i := range parentIDs {
			if ids[i] != parentIDs[i] {
				return nil, fmt.Errorf("%w: name %q is not under parent %q", repox.ErrInvalidArgument, in.GetName(), parent)
			}
		}
		id = ids[len(ids)-1]
	}
	in = proto.Clone(in).(*{{.PB}})
	in.Name = {{.FormatCallParented}}
	if h := r.Hooks.BeforeCreate; h != nil {
		if err := h(ctx, in); err != nil {
			return nil, err
		}
	}
	m := {{$.GormPkg}}.{{.Model}}FromProto(in)
	m.{{.PKField}} = id
	m.Name = in.GetName()
	{{- range .ParentAssigns}}
	{{.}}
	{{- end}}
	{{- range .RefsCreate}}
	{{.}}
	{{- end}}
	{{- if .HasEtag}}
	m.{{.EtagField}} = {{if .EtagPtr}}repox.Ptr(repox.NewULID()){{else}}repox.NewULID(){{end}}
	{{- end}}
	{{- if .HasVOs}}
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		{{- range .VOCreates}}
		{{.}}
		{{- end}}
		{{- if .DeepAfterCreates}}
		if err := {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m); err != nil {
			return err
		}
		{{- range .DeepAfterCreates}}
		{{.}}
		{{- end}}
		return nil
		{{- else}}
		return {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m)
		{{- end}}
	}); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- else}}
	if err := {{$.GormPkg}}.{{.Store}}(r.DB).Create(ctx, m); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- end}}
	return r.get(ctx, id)
}
{{else}}
// Create persists in and returns the stored record.
func (r *Gorm{{.Model}}Repository) Create(ctx context.Context, in *{{.PB}}) (*{{.PB}}, error) {
	id := repox.NewULID()
	if in.GetName() != "" {
		ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
		if err != nil {
			return nil, err
		}
		id = ids[len(ids)-1]
	}
	in = proto.Clone(in).(*{{.PB}})
	in.Name = Format{{.Model}}Name(id)
	if h := r.Hooks.BeforeCreate; h != nil {
		if err := h(ctx, in); err != nil {
			return nil, err
		}
	}
	m := {{$.GormPkg}}.{{.Model}}FromProto(in)
	m.{{.PKField}} = id
	m.Name = in.GetName()
	{{- range .RefsCreate}}
	{{.}}
	{{- end}}
	{{- if .HasEtag}}
	m.{{.EtagField}} = {{if .EtagPtr}}repox.Ptr(repox.NewULID()){{else}}repox.NewULID(){{end}}
	{{- end}}
	{{- if .HasVOs}}
	if err := r.DB.Transaction(func(tx *gorm.DB) error {
		{{- range .VOCreates}}
		{{.}}
		{{- end}}
		{{- if .DeepAfterCreates}}
		if err := {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m); err != nil {
			return err
		}
		{{- range .DeepAfterCreates}}
		{{.}}
		{{- end}}
		return nil
		{{- else}}
		return {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m)
		{{- end}}
	}); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- else}}
	if err := {{$.GormPkg}}.{{.Store}}(r.DB).Create(ctx, m); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- end}}
	return r.get(ctx, id)
}
{{end}}

// Get returns the record addressed by its resource name.
func (r *Gorm{{.Model}}Repository) Get(ctx context.Context, name string) (*{{.PB}}, error) {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	return r.get(ctx, ids[len(ids)-1])
}

// get loads by surrogate key — the private read every generated method re-reads
// through, so Tier-2 overrides of Get never re-enter generated writes.
func (r *Gorm{{.Model}}Repository) get(ctx context.Context, id string) (*{{.PB}}, error) {
	var m {{$.GormPkg}}.{{.Model}}
	if err := r.DB.WithContext(ctx){{.Preloads}}.First(&m, "id = ?", id).Error; err != nil {
		return nil, repox.MapGormErr(err)
	}
	return r.toProto(ctx, &m)
}

// toProto converts a loaded row, decorating reference names and running the
// AfterRead hook.
func (r *Gorm{{.Model}}Repository) toProto(ctx context.Context, m *{{$.GormPkg}}.{{.Model}}) (*{{.PB}}, error) {
	out := {{$.GormPkg}}.{{.Model}}ToProto(m)
	{{- range .RefsToProto}}
	{{.}}
	{{- end}}
	if h := r.Hooks.AfterRead; h != nil {
		if err := h(ctx, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

{{if .Parented}}
// List returns one page of records under parent.
func (r *Gorm{{.Model}}Repository) List(ctx context.Context, parent string, in repox.ListInput) ([]*{{.PB}}, string, error) {
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return nil, "", err
	}
	scope := r.DB.Where("{{.ParentFKColumn}} = ?", parentIDs[len(parentIDs)-1])
	return r.list(ctx, scope, in)
}
{{else}}
// List returns one page of records.
func (r *Gorm{{.Model}}Repository) List(ctx context.Context, in repox.ListInput) ([]*{{.PB}}, string, error) {
	return r.list(ctx, r.DB, in)
}
{{end}}

func (r *Gorm{{.Model}}Repository) list(ctx context.Context, scope *gorm.DB, in repox.ListInput) ([]*{{.PB}}, string, error) {
	conds, err := filterx.Parse(in.Filter)
	if err != nil {
		return nil, "", repox.MapFilterxErr(err)
	}
	eng := filterx.Gorm[{{$.GormPkg}}.{{.Model}}]({{$.GormPkg}}.{{.Model}}FilterSpec)
	for f, h := range r.ListOverrides {
		eng.Override(f, h)
	}
	rows, next, err := eng.List(ctx, scope{{.Preloads}}, filterx.ListInput{
		PageSize:  in.PageSize,
		PageToken: in.PageToken,
		OrderBy:   in.OrderBy,
		Filter:    conds,
	})
	if err != nil {
		return nil, "", repox.MapFilterxErr(err)
	}
	items := make([]*{{.PB}}, 0, len(rows))
	for i := range rows {
		out, err := r.toProto(ctx, &rows[i])
		if err != nil {
			return nil, "", err
		}
		items = append(items, out)
	}
	return items, next, nil
}

// Update persists the masked fields of in; an empty mask replaces every
// mutable field. The write happens in one transaction guarded by in.Etag.
func (r *Gorm{{.Model}}Repository) Update(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
	ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	id := ids[len(ids)-1]
	err = r.DB.Transaction(func(tx *gorm.DB) error {
		var existing {{$.GormPkg}}.{{.Model}}
		if err := tx.WithContext(ctx){{.Preloads}}.First(&existing, "id = ?", id).Error; err != nil {
			return err
		}
		{{- range .VOStaleVars}}
		{{.}}
		{{- end}}
		{{- if .HasEtag}}
		if in.GetEtag() != "" && {{if .EtagPtr}}existing.{{.EtagField}} != nil && *existing.{{.EtagField}}{{else}}existing.{{.EtagField}}{{end}} != in.GetEtag() {
			return repox.ErrConflict
		}
		{{- end}}
		existingPB := {{$.GormPkg}}.{{.Model}}ToProto(&existing)
		{{- if .RefsToProto}}
		{
			m, out := &existing, existingPB
			{{- range .RefsToProto}}
			{{.}}
			{{- end}}
		}
		{{- end}}
		merged := proto.Clone(existingPB).(*{{.PB}})
		apply{{.Model}}Mask(merged, in, paths)
		if h := r.Hooks.BeforeUpdate; h != nil {
			if err := h(ctx, existingPB, merged, paths); err != nil {
				return err
			}
		}
		next := {{$.GormPkg}}.{{.Model}}FromProto(merged)
		_ = next
		{{- range .MutableAssigns}}
		{{.}}
		{{- end}}
		{{- range .VOUpdates}}
		{{.}}
		{{- end}}
		{{- range .DeepDetach}}
		{{.}}
		{{- end}}
		{{- if .HasEtag}}
		existing.{{.EtagField}} = {{if .EtagPtr}}repox.Ptr(repox.NewULID()){{else}}repox.NewULID(){{end}}
		{{- end}}
		{{- if .HasVOs}}
		if err := {{$.GormPkg}}.{{.Store}}(tx).Update(ctx, &existing); err != nil {
			return err
		}
		{{- range .VOStaleDels}}
		{{.}}
		{{- end}}
		return nil
		{{- else}}
		return {{$.GormPkg}}.{{.Store}}(tx).Update(ctx, &existing)
		{{- end}}
	})
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return r.get(ctx, id)
}

// Delete removes the record addressed by its resource name.
func (r *Gorm{{.Model}}Repository) Delete(ctx context.Context, name string) error {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return err
	}
	if h := r.Hooks.BeforeDelete; h != nil {
		if err := h(ctx, name); err != nil {
			return err
		}
	}
	id := ids[len(ids)-1]
	var existing {{$.GormPkg}}.{{.Model}}
	if err := r.DB.WithContext(ctx){{.DeletePreloads}}.First(&existing, "id = ?", id).Error; err != nil {
		return repox.MapGormErr(err)
	}
	{{- if .HasVOs}}
	return repox.MapGormErr(r.DB.Transaction(func(tx *gorm.DB) error {
		if err := {{$.GormPkg}}.{{.Store}}(tx).DeleteByID(ctx, id); err != nil {
			return err
		}
		{{- range .VODeleteCleanups}}
		{{.}}
		{{- end}}
		return nil
	}))
	{{- else}}
	return repox.MapGormErr({{$.GormPkg}}.{{.Store}}(r.DB).DeleteByID(ctx, id))
	{{- end}}
}

// Compile-time proof the adapter satisfies the interface.
var _ {{.Model}}Repository = (*Gorm{{.Model}}Repository)(nil)
{{- end}}
