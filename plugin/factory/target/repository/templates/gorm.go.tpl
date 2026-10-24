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
	// Refs, when set, is asked before a purge whether anything in another
	// database or service still refers to the record (see Purge).
	Refs *repox.References
	// ListOverrides substitute the generated dispatch for single filter fields
	// (e.g. a derived state computed via subqueries).
	ListOverrides map[string]filterx.SQLHandler
{{- if .Expiring}}
	// Retention is how long a soft-deleted record stays restorable: Delete
	// sets expire_time that far ahead. Zero leaves it unset.
	Retention time.Duration
{{- end}}
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
	{{- if .NameKeyed}}
	rowID := id
	{{- end}}
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
	{{- if .NameKeyed}}
	// The id is unique only under the parent, so the row is keyed by a
	// surrogate of its own: the id itself only when it was minted here.
	id = rowID
	{{- end}}
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
	{{- if .ParentLinks}}
	if err := r.linkParents(ctx, r.DB, m); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- end}}
	{{- range .RefsCreate}}
	{{.}}
	{{- end}}
	{{- if .HasEtag}}
	m.{{.EtagField}} = {{if .EtagPtr}}repox.Ptr(repox.NewULID()){{else}}repox.NewULID(){{end}}
	{{- end}}
	{{- if .HasVOs}}
	if err := r.DB.Transaction(func(tx *gorm.DB) error { return r.insertTx(ctx, tx, in, m) }); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- else}}
	if err := {{$.GormPkg}}.{{.Store}}(r.DB).Create(ctx, m); err != nil {
		return nil, repox.MapGormErr(err)
	}
	{{- end}}
	return r.get(ctx, {{if .NameKeyed}}m.Name{{else}}id{{end}})
}
{{- if .ParentLinks}}

// linkParents points m's columns for its nested ancestors at their rows. A
// nested resource's row is keyed by a surrogate of its own, not by its id, so
// the column is filled by looking the ancestor up by name.
func (r *Gorm{{.Model}}Repository) linkParents(ctx context.Context, db *gorm.DB, m *{{$.GormPkg}}.{{.Model}}) error {
	ids, err := repox.SplitName(m.Name, {{.CollectionsExpr}})
	if err != nil {
		return err
	}
	{{- range .ParentLinks}}
	{{.}}
	{{- end}}
	return nil
}
{{- end}}
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
	if err := r.DB.Transaction(func(tx *gorm.DB) error { return r.insertTx(ctx, tx, in, m) }); err != nil {
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
	key, err := r.rowKey(name)
	if err != nil {
		return nil, err
	}
	return r.get(ctx, key)
}

// rowKey validates name and returns what its row is found by: {{if .NameKeyed}}the name
// itself, because the id is unique only under the parent{{else}}the bare id{{end}}.
func (r *Gorm{{.Model}}Repository) rowKey(name string) (string, error) {
	{{- if .NameKeyed}}
	if _, err := repox.SplitName(name, {{.CollectionsExpr}}); err != nil {
		return "", err
	}
	return name, nil
	{{- else}}
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return "", err
	}
	return ids[len(ids)-1], nil
	{{- end}}
}

// get loads by row key (rowKey) — the private read every generated method
// re-reads through, so Tier-2 overrides of Get never re-enter generated writes.
func (r *Gorm{{.Model}}Repository) get(ctx context.Context, key string) (*{{.PB}}, error) {
	var m {{$.GormPkg}}.{{.Model}}
	if err := r.DB.WithContext(ctx){{.Preloads}}.First(&m, "{{.KeyColumn}} = ?", key).Error; err != nil {
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
	{{- if .ParentScopeSQL}}
	// The parent is nested, so its row is found by name.
	op, arg := repox.NameMatch(parent)
	scope := r.DB.Where({{.ParentScopeSQL}}+op+")", arg)
	{{- else}}
	scope := r.DB.Where("{{.ParentFKColumn}} = ?", parentIDs[len(parentIDs)-1])
	{{- end}}
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
	if err := repox.CheckReadMask((*{{.PB}})(nil), in.ReadMask); err != nil {
		return nil, "", err
	}
	rows, next, err := eng.List(ctx, r.reads(scope, in), filterx.ListInput{
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
		repox.Narrow(out, in.ReadMask)
		items = append(items, out)
	}
	return items, next, nil
}

// reads is the query a List reads rows through: every column and value object
// without a read mask; with one, only the columns and value objects of the
// fields it names, and the columns the page is sorted by.
func (r *Gorm{{.Model}}Repository) reads(db *gorm.DB, in repox.ListInput) *gorm.DB {
	if len(in.ReadMask) == 0 {
		return db{{.Preloads}}
	}
	if omit := repox.Unread({{.LowerModel}}Columns, in.ReadMask, repox.OrderColumns({{$.GormPkg}}.{{.Model}}FilterSpec, in.OrderBy)...); len(omit) > 0 {
		db = db.Omit(omit...)
	}
	{{- range .ReadPreloads}}
	if repox.Reads(in.ReadMask, "{{.Field}}") {
		db = db{{.Chain}}
	}
	{{- end}}
	return db
}

// {{.LowerModel}}Columns are the row's columns by the field each stores, for
// a read mask to leave out.
var {{.LowerModel}}Columns = map[string][]string{
	{{- range .FieldColumns}}
	"{{.Field}}": { {{- range $i, $c := .Columns}}{{if $i}}, {{end}}"{{$c}}"{{end -}} },
	{{- end}}
}

// Update persists the masked fields of in; an empty mask replaces every
// mutable field. The write happens in one transaction guarded by in.Etag.
func (r *Gorm{{.Model}}Repository) Update(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
	key, err := r.rowKey(in.GetName())
	if err != nil {
		return nil, err
	}
	err = r.DB.Transaction(func(tx *gorm.DB) error { return r.updateTx(ctx, tx, key, in, paths, nil) })
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return r.get(ctx, key)
}
{{- if .OutputOnly}}

// UpdateWithOutputOnly is Update plus the server-owned fields outputPaths
// names, in Update's transaction and under its etag guard.
func (r *Gorm{{.Model}}Repository) UpdateWithOutputOnly(ctx context.Context, in *{{.PB}}, paths, outputPaths []string) (*{{.PB}}, error) {
	key, err := r.rowKey(in.GetName())
	if err != nil {
		return nil, err
	}
	err = r.DB.Transaction(func(tx *gorm.DB) error { return r.updateTx(ctx, tx, key, in, paths, outputPaths) })
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return r.get(ctx, key)
}
{{- end}}

// updateTx is Update's write inside tx, which a Batch shares with other
// writes. The row is read FOR UPDATE: a concurrent update of the same record
// waits for this transaction and then sees the etag it wrote, so two writers
// holding the same etag cannot both pass the guard. outputPaths names the
// server-owned fields written alongside (UpdateWithOutputOnly); nil for Update.
func (r *Gorm{{.Model}}Repository) updateTx(ctx context.Context, tx *gorm.DB, key string, in *{{.PB}}, paths, outputPaths []string) error {
	var existing {{$.GormPkg}}.{{.Model}}
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}){{.Preloads}}.First(&existing, "{{.KeyColumn}} = ?", key).Error; err != nil {
		return err
	}
	id := existing.{{.PKField}}
	_ = id
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
	{{- if .OutputOnly}}
	repox.CopyFields(merged, in, outputPaths)
	{{- end}}
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
	{{- if .OutputOnly}}
	if len(outputPaths) > 0 {
		paths := outputPaths
		{{- range .OutputAssigns}}
		{{.}}
		{{- end}}
	}
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
}
{{- if .OutputOnly}}

// UpdateOutputOnly persists the server-owned fields of in that paths names, in
// one transaction guarded by in.Etag.
func (r *Gorm{{.Model}}Repository) UpdateOutputOnly(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
	key, err := r.rowKey(in.GetName())
	if err != nil {
		return nil, err
	}
	err = r.DB.Transaction(func(tx *gorm.DB) error { return r.updateOutputOnlyTx(ctx, tx, in, paths) })
	if err != nil {
		return nil, repox.MapGormErr(err)
	}
	return r.get(ctx, key)
}

// updateOutputOnlyTx is UpdateOutputOnly's write inside tx, which a Batch
// shares with other writes.
func (r *Gorm{{.Model}}Repository) updateOutputOnlyTx(ctx context.Context, tx *gorm.DB, in *{{.PB}}, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("%w: UpdateOutputOnly needs the fields to write", repox.ErrInvalidArgument)
	}
	key, err := r.rowKey(in.GetName())
	if err != nil {
		return err
	}
	{
		var existing {{$.GormPkg}}.{{.Model}}
		// FOR UPDATE, as in updateTx: concurrent writers holding one etag
		// take turns, and only the first passes the guard.
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&existing, "{{.KeyColumn}} = ?", key).Error; err != nil {
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

// newRow builds the row a Batch inserts for in{{if .Parented}} under parent{{end}}, and the
// record it stands for, named: the same resolution Create applies.
func (r *Gorm{{.Model}}Repository) newRow({{if .Parented}}parent string, {{end}}in *{{.PB}}) (*{{$.GormPkg}}.{{.Model}}, *{{.PB}}, error) {
	{{- if .Parented}}
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return nil, nil, err
	}
	{{- end}}
	id := repox.NewULID()
	{{- if .NameKeyed}}
	rowID := id
	{{- end}}
	if in.GetName() != "" {
		ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
		if err != nil {
			return nil, nil, err
		}
		{{- if .Parented}}
		for i := range parentIDs {
			if ids[i] != parentIDs[i] {
				return nil, nil, fmt.Errorf("%w: name %q is not under parent %q", repox.ErrInvalidArgument, in.GetName(), parent)
			}
		}
		{{- end}}
		id = ids[len(ids)-1]
	}
	in = proto.Clone(in).(*{{.PB}})
	in.Name = {{if .Parented}}{{.FormatCallParented}}{{else}}{{.FormatCallRoot}}{{end}}
	{{- if .NameKeyed}}
	id = rowID // keyed as Create keys it
	{{- end}}
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
	return m, in, nil
}
{{- if .HasVOs}}

// insertTx writes m inside tx with the value-object rows in carries: those
// it points at first, then m, then its children.
func (r *Gorm{{.Model}}Repository) insertTx(ctx context.Context, tx *gorm.DB, in *{{.PB}}, m *{{$.GormPkg}}.{{.Model}}) error {
	id := m.{{.PKField}}
	_ = id
	{{- range .VOCreates}}
	{{.}}
	{{- end}}
	if err := {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m); err != nil {
		return err
	}
	{{- range .DeepAfterCreates}}
	{{.}}
	{{- end}}
	return nil
}
{{- end}}

{{- if .SoftDelete}}
// Delete soft-deletes the record addressed by its resource name: delete_time
// is set, the row stays.{{if .UpdateTimeColumn}} {{.UpdateTimeColumn}} is set to the same instant: the
// delete is the record's latest write, which a reader by update time can tell
// from any write after it.{{end}}
func (r *Gorm{{.Model}}Repository) Delete(ctx context.Context, name string) error {
	key, err := r.rowKey(name)
	if err != nil {
		return err
	}
	if h := r.Hooks.BeforeDelete; h != nil {
		if err := h(ctx, name); err != nil {
			return err
		}
	}
	res := r.markDeleted(ctx, r.DB, key)
	if res.Error != nil {
		return repox.MapGormErr(res.Error)
	}
	if res.RowsAffected == 0 {
		return repox.ErrNotFound
	}
	return nil
}

// markDeleted sets the soft-delete mark on the live row found by key
// (rowKey) through db: Delete's write, which a Batch runs inside its
// transaction.
func (r *Gorm{{.Model}}Repository) markDeleted(ctx context.Context, db *gorm.DB, key string) *gorm.DB {
	now := time.Now()
	return db.WithContext(ctx).Model(&{{$.GormPkg}}.{{.Model}}{}).
		Where("{{.KeyColumn}} = ? AND delete_time IS NULL", key).
		Updates(map[string]any{"delete_time": now{{if .UpdateTimeColumn}}, "{{.UpdateTimeColumn}}": now{{end}}{{if .HasEtag}}, "{{.EtagColumn}}": repox.NewULID(){{end}}{{if .Expiring}}, "{{.ExpireTimeColumn}}": repox.ExpireAt(now, r.Retention){{end}}})
}

// Undelete clears the soft-delete mark and returns the restored record.
func (r *Gorm{{.Model}}Repository) Undelete(ctx context.Context, name string) (*{{.PB}}, error) {
	key, err := r.rowKey(name)
	if err != nil {
		return nil, err
	}
	res := r.DB.WithContext(ctx).Model(&{{$.GormPkg}}.{{.Model}}{}).
		Where("{{.KeyColumn}} = ? AND delete_time IS NOT NULL", key).
		Updates(map[string]any{"delete_time": nil{{if .HasEtag}}, "{{.EtagColumn}}": repox.NewULID(){{end}}{{if .Expiring}}, "{{.ExpireTimeColumn}}": nil{{end}}})
	if res.Error != nil {
		return nil, repox.MapGormErr(res.Error)
	}
	if res.RowsAffected == 0 {
		if _, err := r.get(ctx, key); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s is not deleted", repox.ErrAlreadyExists, name)
	}
	return r.get(ctx, key)
}

// Purge removes the record addressed by its resource name for good.
func (r *Gorm{{.Model}}Repository) Purge(ctx context.Context, name string) error {
{{- else}}
// Delete removes the record addressed by its resource name.
func (r *Gorm{{.Model}}Repository) Delete(ctx context.Context, name string) error {
{{- end}}
	key, err := r.rowKey(name)
	if err != nil {
		return err
	}
	if h := r.Hooks.BeforeDelete; h != nil {
		if err := h(ctx, name); err != nil {
			return err
		}
	}
	{{- if .SoftDelete}}
	_, err = r.purge(ctx, r.DB, key, name, true)
	return repox.MapGormErr(err)
	{{- else if .HasVOs}}
	return repox.MapGormErr(r.DB.Transaction(func(tx *gorm.DB) error { return r.purgeTx(ctx, tx, key) }))
	{{- else}}
	var existing {{$.GormPkg}}.{{.Model}}
	if err := r.DB.WithContext(ctx).First(&existing, "{{.KeyColumn}} = ?", key).Error; err != nil {
		return repox.MapGormErr(err)
	}
	return repox.MapGormErr({{$.GormPkg}}.{{.Store}}(r.DB).DeleteByID(ctx, existing.{{.PKField}}))
	{{- end}}
}
{{- if .HasVOs}}

// purgeTx removes the row found by key (rowKey) inside tx, with the
// value-object rows it owns; gorm.ErrRecordNotFound when there is no such row.
func (r *Gorm{{.Model}}Repository) purgeTx(ctx context.Context, tx *gorm.DB, key string) error {
	var existing {{$.GormPkg}}.{{.Model}}
	if err := tx.WithContext(ctx){{.DeletePreloads}}.First(&existing, "{{.KeyColumn}} = ?", key).Error; err != nil {
		return err
	}
	if err := {{$.GormPkg}}.{{.Store}}(tx).DeleteByID(ctx, existing.{{.PKField}}); err != nil {
		return err
	}
	{{- range .VODeleteCleanups}}
	{{.}}
	{{- end}}
	return nil
}
{{- end}}
{{- if .SoftDelete}}

// purge removes the row found by key (rowKey), named name, for good through
// db -- unless something
// still refers to it, when it keeps the row and reports kept: a row of this
// database holding a foreign key to it (asked first, then caught again from
// the constraint itself), or, when askReferrers, a referrer in another
// database or service (r.Refs). A purge never fails, and never cascades,
// because of a referrer. gorm.ErrRecordNotFound when there is no such row.
func (r *Gorm{{.Model}}Repository) purge(ctx context.Context, db *gorm.DB, key, name string, askReferrers bool) (kept bool, err error) {
	{{- if .ReferencedSQL}}
	var referenced bool
	if err := db.WithContext(ctx).Raw({{.LowerModel}}ReferencedSQL, key).Scan(&referenced).Error; err != nil {
		return false, err
	}
	if referenced {
		r.Refs.Kept(ctx, name, "foreign key")
		return true, nil
	}
	{{- end}}
	{{- if .Type}}
	if askReferrers && r.Refs.KeepOnPurge(ctx, {{printf "%q" .Type}}, name) {
		return true, nil
	}
	{{- end}}
	// A savepoint inside a caller's transaction: a foreign key the query
	// above does not know (one from another generator run) rolls back only
	// this purge.
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		{{- if .HasVOs}}
		return r.purgeTx(ctx, tx, key)
		{{- else}}
		var existing {{$.GormPkg}}.{{.Model}}
		if err := tx.First(&existing, "{{.KeyColumn}} = ?", key).Error; err != nil {
			return err
		}
		return {{$.GormPkg}}.{{.Store}}(tx).DeleteByID(ctx, existing.{{.PKField}})
		{{- end}}
	})
	if repox.IsForeignKeyViolation(err) {
		r.Refs.Kept(ctx, name, "foreign key")
		return true, nil
	}
	return false, err
}
{{- if .ReferencedSQL}}

// {{.LowerModel}}ReferencedSQL asks whether any row, deleted or not, still
// holds a foreign key to the {{.Model}} row with the given key (rowKey).
const {{.LowerModel}}ReferencedSQL = `{{.ReferencedSQL}}`
{{- end}}
{{- if .Expiring}}

// PurgeExpired removes for good every deleted record whose expire_time is at
// or before now, keeping the ones something still refers to (see purge).
// Hooks do not run.
func (r *Gorm{{.Model}}Repository) PurgeExpired(ctx context.Context, now time.Time) (repox.PurgeResult, error) {
	var res repox.PurgeResult
	in := repox.ListInput{
		PageSize:    500,
		ShowDeleted: true,
		ReadMask:    []string{"name"},
		Where: []filterx.Condition{
			{Field: "delete_time", Op: filterx.OpNull, Value: "false"},
			{Field: "{{.ExpireTimeColumn}}", Op: filterx.OpLte, Value: now.UTC().Format(time.RFC3339Nano)},
		},
	}
	for {
		page, next, err := r.list(ctx, r.DB, in)
		if err != nil {
			return res, err
		}
		for _, m := range page {
			key, err := r.rowKey(m.GetName())
			if err != nil {
				return res, err
			}
			kept, err := r.purge(ctx, r.DB, key, m.GetName(), true)
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
			case err != nil:
				return res, repox.MapGormErr(err)
			case kept:
				res.Kept = append(res.Kept, m.GetName())
			default:
				res.Purged = append(res.Purged, m.GetName())
			}
		}
		if next == "" {
			return res, nil
		}
		in.PageToken = next
	}
}
{{- end}}
{{- end}}

// ListByKeys reads every record whose filter field field is one of keys; see
// repox.ListByKeys.
func (r *Gorm{{.Model}}Repository) ListByKeys(ctx context.Context, field string, keys []string, in repox.ListInput) ([]*{{.PB}}, error) {
	return repox.ListByKeys(ctx, field, keys, in, func(ctx context.Context, in repox.ListInput) ([]*{{.PB}}, string, error) {
		return r.List(ctx, {{if .Parented}}{{printf "%q" .WildcardParent}}, {{end}}in)
	})
}

// Compile-time proof the adapter satisfies the interface.
var _ {{.Model}}Repository = (*Gorm{{.Model}}Repository)(nil)
{{- end}}
{{- if .HasBatch}}

// gormBatch is Batch on GORM: queued writes run in the Unit's transaction on
// Commit.
type gormBatch struct {
	u  *repox.Unit
	db *gorm.DB
{{- if .HasExpiring}}
	retention time.Duration
{{- end}}
}

// Commit applies the Unit's writes; see repox.Unit.Commit.
func (b *gormBatch) Commit(ctx context.Context) error { return b.u.Commit(ctx) }
{{- range .Resources}}

// Create{{.Model}} queues a create and returns the name the record will have.
func (b *gormBatch) Create{{.Model}}({{if .Parented}}parent string, {{end}}in *{{.PB}}) (string, error) {
	r := &Gorm{{.Model}}Repository{DB: b.db}
	m, named, err := r.newRow({{if .Parented}}parent, {{end}}in)
	if err != nil {
		return "", err
	}
	var locks []string
	if k, ok := any(m).(interface{ NoOverlapKeys() []string }); ok {
		locks = k.NoOverlapKeys()
	}
	queue{{.Model}}References(b.u, named, nil)
	b.u.QueueGorm(locks, func(ctx context.Context, tx *gorm.DB) error {
		{{- if .ParentLinks}}
		if err := r.linkParents(ctx, tx, m); err != nil {
			return err
		}
		{{- end}}
		{{- if .HasVOs}}
		return r.insertTx(ctx, tx, named, m)
		{{- else}}
		return {{$.GormPkg}}.{{.Store}}(tx).Create(ctx, m)
		{{- end}}
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: named.GetName(), Op: repox.ChangeCreate, Record: named})
	return named.GetName(), nil
}

// Update{{.Model}} queues Update; in.Etag, when set, guards it, and a guard
// that fails rolls the whole batch back.
func (b *gormBatch) Update{{.Model}}(in *{{.PB}}, paths []string) {
	r := &Gorm{{.Model}}Repository{DB: b.db}
	key, err := r.rowKey(in.GetName())
	if err != nil {
		b.u.Fail(err)
		return
	}
	queue{{.Model}}References(b.u, in, paths)
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		return r.updateTx(ctx, tx, key, in, paths, nil)
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: in.GetName(), Op: repox.ChangeUpdate, Record: in, LoadPrior: repox.PriorOf(in.GetName(), get{{.Model}})})
}
{{- if .OutputOnly}}

// Update{{.Model}}WithOutputOnly queues UpdateWithOutputOnly; in.Etag, when
// set, guards it, and a guard that fails rolls the whole batch back.
func (b *gormBatch) Update{{.Model}}WithOutputOnly(in *{{.PB}}, paths, outputPaths []string) {
	r := &Gorm{{.Model}}Repository{DB: b.db}
	key, err := r.rowKey(in.GetName())
	if err != nil {
		b.u.Fail(err)
		return
	}
	queue{{.Model}}References(b.u, in, paths)
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		return r.updateTx(ctx, tx, key, in, paths, outputPaths)
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: in.GetName(), Op: repox.ChangeUpdate, Record: in, LoadPrior: repox.PriorOf(in.GetName(), get{{.Model}})})
}
{{- end}}
{{- if .OutputOnly}}

// Update{{.Model}}OutputOnly queues UpdateOutputOnly; in.Etag, when set, guards it.
func (b *gormBatch) Update{{.Model}}OutputOnly(in *{{.PB}}, paths []string) {
	r := &Gorm{{.Model}}Repository{DB: b.db}
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		return r.updateOutputOnlyTx(ctx, tx, in, paths)
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: in.GetName(), Op: repox.ChangeUpdate, Record: in, LoadPrior: repox.PriorOf(in.GetName(), get{{.Model}})})
}
{{- end}}
{{- if .SoftDelete}}

// Delete{{.Model}} queues a soft delete. A record already deleted or gone is
// not an error: two batches deleting the same record must not fail each other.
func (b *gormBatch) Delete{{.Model}}(name string) {
	r := &Gorm{{.Model}}Repository{DB: b.db{{if .Expiring}}, Retention: b.retention{{end}}}
	key, err := r.rowKey(name)
	if err != nil {
		b.u.Fail(err)
		return
	}
	queue{{.Model}}Delete(b.u, name, false)
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		return r.markDeleted(ctx, tx, key).Error
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: name, Op: repox.ChangeDelete, LoadPrior: repox.PriorOf(name, get{{.Model}})})
}
{{- end}}

// {{if .SoftDelete}}Purge{{else}}Delete{{end}}{{.Model}} queues the record's removal for good. A record already
// gone is not an error: two batches clearing the same row must not fail each
// other.
func (b *gormBatch) {{if .SoftDelete}}Purge{{else}}Delete{{end}}{{.Model}}(name string) {
	{{- if .SoftDelete}}
	// A record something still refers to is kept, as Purge keeps it.
	r := &Gorm{{.Model}}Repository{DB: b.db, Refs: b.u.Conn().References}
	{{- else}}
	r := &Gorm{{.Model}}Repository{DB: b.db}
	{{- end}}
	key, err := r.rowKey(name)
	if err != nil {
		b.u.Fail(err)
		return
	}
	{{- if .SoftDelete}}
	keep := queue{{.Model}}Purge(b.u, name)
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		if *keep {
			return nil
		}
		if _, err := r.purge(ctx, tx, key, name, false); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	})
	{{- else if .HasVOs}}
	queue{{.Model}}Delete(b.u, name, true)
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		if err := r.purgeTx(ctx, tx, key); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	})
	{{- else}}
	queue{{.Model}}Delete(b.u, name, true)
	b.u.QueueGorm(nil, func(ctx context.Context, tx *gorm.DB) error {
		return tx.WithContext(ctx).Delete(&{{$.GormPkg}}.{{.Model}}{}, "{{.KeyColumn}} = ?", key).Error
	})
	{{- end}}
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: name, Op: {{if .SoftDelete}}repox.ChangePurge{{else}}repox.ChangeDelete{{end}}, LoadPrior: repox.PriorOf(name, get{{.Model}})})
}
{{- end}}
{{- end}}
