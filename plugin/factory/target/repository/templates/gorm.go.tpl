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
	// AIP-159: "-" for the parent reads across every parent; narrow with a
	// Where on the parent field instead.
	if parentIDs[len(parentIDs)-1] == "-" {
		return r.list(ctx, r.DB, in)
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
	{{- if .SoftDelete}}
	if !in.ShowDeleted {
		scope = scope.Where("delete_time IS NULL")
	}
	{{- end}}
	conds, err := filterx.Parse(in.Filter)
	if err != nil {
		return nil, "", repox.MapFilterxErr(err)
	}
	conds = append(conds, in.Where...)
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
{{- if .OutputOnly}}

// UpdateOutputOnly persists the server-owned fields of in that paths names, in
// one transaction guarded by in.Etag.
func (r *Gorm{{.Model}}Repository) UpdateOutputOnly(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
	ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	err = r.DB.Transaction(func(tx *gorm.DB) error { return r.updateOutputOnlyTx(ctx, tx, in, paths) })
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return r.get(ctx, ids[len(ids)-1])
}

// updateOutputOnlyTx is UpdateOutputOnly's write inside tx, which a Batch
// shares with other writes.
func (r *Gorm{{.Model}}Repository) updateOutputOnlyTx(ctx context.Context, tx *gorm.DB, in *{{.PB}}, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("%w: UpdateOutputOnly needs the fields to write", repox.ErrInvalidArgument)
	}
	ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
	if err != nil {
		return err
	}
	id := ids[len(ids)-1]
	{
		var existing {{$.GormPkg}}.{{.Model}}
		if err := tx.WithContext(ctx).First(&existing, "id = ?", id).Error; err != nil {
			return err
		}
		{{- if .HasEtag}}
		if in.GetEtag() != "" && {{if .EtagPtr}}existing.{{.EtagField}} != nil && *existing.{{.EtagField}}{{else}}existing.{{.EtagField}}{{end}} != in.GetEtag() {
			return repox.ErrConflict
		}
		{{- end}}
		next := {{$.GormPkg}}.{{.Model}}FromProto(in)
		{{- range .OutputAssigns}}
		{{.}}
		{{- end}}
		{{- if .HasEtag}}
		existing.{{.EtagField}} = {{if .EtagPtr}}repox.Ptr(repox.NewULID()){{else}}repox.NewULID(){{end}}
		{{- end}}
		return {{$.GormPkg}}.{{.Store}}(tx).Update(ctx, &existing)
	}
}
{{- end}}
{{- if .Flat}}

// newRow builds the row a Batch inserts for in{{if .Parented}} under parent{{end}}, and the
// name the record will have: the same resolution Create applies.
func (r *Gorm{{.Model}}Repository) newRow({{if .Parented}}parent string, {{end}}in *{{.PB}}) (*{{$.GormPkg}}.{{.Model}}, string, error) {
	{{- if .Parented}}
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return nil, "", err
	}
	{{- end}}
	id := repox.NewULID()
	if in.GetName() != "" {
		ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
		if err != nil {
			return nil, "", err
		}
		{{- if .Parented}}
		for i := range parentIDs {
			if ids[i] != parentIDs[i] {
				return nil, "", fmt.Errorf("%w: name %q is not under parent %q", repox.ErrInvalidArgument, in.GetName(), parent)
			}
		}
		{{- end}}
		id = ids[len(ids)-1]
	}
	in = proto.Clone(in).(*{{.PB}})
	in.Name = {{if .Parented}}{{.FormatCallParented}}{{else}}{{.FormatCallRoot}}{{end}}
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
	return m, in.GetName(), nil
}
{{- end}}

{{- if .SoftDelete}}
// Delete soft-deletes the record addressed by its resource name: delete_time
// is set, the row stays.
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
	res := r.DB.WithContext(ctx).Model(&{{$.GormPkg}}.{{.Model}}{}).
		Where("id = ? AND delete_time IS NULL", ids[len(ids)-1]).
		Updates(map[string]any{"delete_time": time.Now(){{if .HasEtag}}, "{{.EtagColumn}}": repox.NewULID(){{end}}})
	if res.Error != nil {
		return repox.MapGormErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return repox.ErrNotFound
	}
	return nil
}

// Undelete clears the soft-delete mark and returns the restored record.
func (r *Gorm{{.Model}}Repository) Undelete(ctx context.Context, name string) (*{{.PB}}, error) {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	id := ids[len(ids)-1]
	res := r.DB.WithContext(ctx).Model(&{{$.GormPkg}}.{{.Model}}{}).
		Where("id = ? AND delete_time IS NOT NULL", id).
		Updates(map[string]any{"delete_time": nil{{if .HasEtag}}, "{{.EtagColumn}}": repox.NewULID(){{end}}})
	if res.Error != nil {
		return nil, repox.MapGormErr(res.Error)
	}
	if res.RowsAffected == 0 {
		if _, err := r.get(ctx, id); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s is not deleted", repox.ErrAlreadyExists, name)
	}
	return r.get(ctx, id)
}

// Purge removes the record addressed by its resource name for good.
func (r *Gorm{{.Model}}Repository) Purge(ctx context.Context, name string) error {
{{- else}}
// Delete removes the record addressed by its resource name.
func (r *Gorm{{.Model}}Repository) Delete(ctx context.Context, name string) error {
{{- end}}
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
{{- if .HasBatch}}

// gormBatch is Batch on GORM: queued writes run in one transaction on Commit.
type gormBatch struct {
	db  *gorm.DB
	ops []func(ctx context.Context, tx *gorm.DB) error
	err error // the first queueing error; Commit returns it and writes nothing
}

// Commit runs every queued write in one transaction: all land or none do.
func (b *gormBatch) Commit(ctx context.Context) error {
	if b.err != nil {
		return b.err
	}
	if len(b.ops) == 0 {
		return nil
	}
	return repox.MapGormErr(b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, op := range b.ops {
			if err := op(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}))
}

func (b *gormBatch) fail(err error) {
	if b.err == nil {
		b.err = err
	}
}
{{- range .Resources}}{{if .Flat}}

// Create{{.Model}} queues a create and returns the name the record will have.
func (b *gormBatch) Create{{.Model}}({{if .Parented}}parent string, {{end}}in *{{.PB}}) (string, error) {
	m, name, err := (&Gorm{{.Model}}Repository{DB: b.db}).newRow({{if .Parented}}parent, {{end}}in)
	if err != nil {
		return "", err
	}
	b.ops = append(b.ops, func(ctx context.Context, tx *gorm.DB) error {
		return {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m)
	})
	return name, nil
}
{{- if .OutputOnly}}

// Update{{.Model}}OutputOnly queues UpdateOutputOnly; in.Etag, when set, guards it.
func (b *gormBatch) Update{{.Model}}OutputOnly(in *{{.PB}}, paths []string) {
	r := &Gorm{{.Model}}Repository{DB: b.db}
	b.ops = append(b.ops, func(ctx context.Context, tx *gorm.DB) error {
		return r.updateOutputOnlyTx(ctx, tx, in, paths)
	})
}
{{- end}}

// Delete{{.Model}} queues the record's removal for good. A record already
// gone is not an error: two batches clearing the same row must not fail each
// other.
func (b *gormBatch) Delete{{.Model}}(name string) {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		b.fail(err)
		return
	}
	id := ids[len(ids)-1]
	b.ops = append(b.ops, func(ctx context.Context, tx *gorm.DB) error {
		return tx.WithContext(ctx).Delete(&{{$.GormPkg}}.{{.Model}}{}, "id = ?", id).Error
	})
}
{{- end}}{{end}}
{{- end}}
