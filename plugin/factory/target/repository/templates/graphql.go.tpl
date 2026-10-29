{{.Header}}

package {{.Package}}


import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

// mapGraphQLErr translates the client's errors onto the repox sentinels.
func mapGraphQLErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, graphql.ErrConflict):
		return repox.ErrConflict
	// The engine reports a database rejection as text, so the two the
	// repository contract names are recognised by PostgreSQL's own wording:
	// an exclusion constraint ((store.v1.table).no_overlap) is a conflict, a
	// unique one a duplicate.
	case strings.Contains(err.Error(), "violates exclusion constraint"):
		return fmt.Errorf("%w: %v", repox.ErrConflict, err)
	case strings.Contains(err.Error(), "violates unique constraint"):
		return fmt.Errorf("%w: %v", repox.ErrAlreadyExists, err)
	// A foreign key: repox.ErrForeignKey, with the constraint named.
	case strings.Contains(err.Error(), "violates foreign key constraint"):
		return repox.ForeignKeyError(err)
	default:
		return err
	}
}

{{- range .Resources}}

// GraphQL{{.Model}}Repository is the GraphQL adapter of {{.Model}}Repository
// over the generated client: the same flat CRUD the gorm adapter implements,
// against {{.Domain}}.{{.Resource}}. Exported fields are the Tier-2 seam.
type GraphQL{{.Model}}Repository struct {
	Svc   *{{$.ClientPkg}}.Service
	Hooks {{.Model}}Hooks
	// Refs, when set, is asked before a purge whether anything in another
	// database or service still refers to the record (see Purge).
	Refs *repox.References
	// ListOverrides substitute the generated dispatch for single filter fields.
	ListOverrides map[string]filterx.GraphQLHandler
{{- if .Expiring}}
	// Retention is how long a soft-deleted record stays restorable: Delete
	// sets expire_time that far ahead. Zero leaves it unset.
	Retention time.Duration
{{- end}}
}

// NewGraphQL{{.Model}}Repository returns the GraphQL adapter bound to svc.
func NewGraphQL{{.Model}}Repository(svc *{{$.ClientPkg}}.Service) *GraphQL{{.Model}}Repository {
	return &GraphQL{{.Model}}Repository{Svc: svc}
}

{{if .Parented}}
// Create persists in under parent and returns the stored record.
func (r *GraphQL{{.Model}}Repository) Create(ctx context.Context, parent string, in *{{.PB}}) (*{{.PB}}, error) {
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
	{{- if .RequiredDocs}}
	if err := check{{.Model}}Required(in); err != nil {
		return nil, err
	}
	{{- end}}
	ci := {{.LowerModelV}}ToCreateInput(in)
	ci.Id = id
	{{- range .GQLParentAssigns}}
	{{.}}
	{{- end}}
	{{- if .GQLParentLinks}}
	if err := r.linkParents(ctx, &ci, in.GetName(), nil); err != nil {
		return nil, err
	}
	{{- end}}
	{{- if .EtagInputField}}
	ci.{{.EtagInputField}} = repox.NewULID()
	{{- end}}
	{{- if or .CreateTimeField .UpdateTimeField}}
	now := tsToStr(timestamppb.New(time.Now().UTC()))
	{{- end}}
	{{- if .CreateTimeField}}
	ci.{{.CreateTimeField}} = now
	{{- end}}
	{{- if .UpdateTimeField}}
	ci.{{.UpdateTimeField}} = now
	{{- end}}
	{{- range .GQLVOCreates}}
	{{.}}
	{{- end}}
	{{- if or .HasDeep .Joins}}
	// One transaction: the graph lands whole or not at all.
	gtx := r.Svc.Mutation.Tx()
	{{- range .GQLDeepCreates}}
	{{.}}
	{{- end}}
	gtx.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
	{{- range .GQLDeepAfterCreates}}
	{{.}}
	{{- end}}
	{{- range .Joins}}
	{
		var ops repox.Ops
		if err := r.{{.Var}}Ops(ctx, &ops, ci.Id, in.Get{{.GoField}}(), false); err != nil {
			return nil, err
		}
		for _, op := range ops {
			gtx.Add(op)
		}
	}
	{{- end}}
	if err := gtx.Commit(ctx); err != nil {
		return nil, mapGraphQLErr(err)
	}
	{{- else}}
	if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.Create(ctx, ci); err != nil {
		return nil, mapGraphQLErr(err)
	}
	{{- end}}
	return r.get(ctx, id)
}
{{- if .GQLParentLinks}}

// linkParents points ci's columns for its nested ancestors at their rows,
// found in known (rows a Batch is creating, by name) or else looked up by
// name: a nested resource's row is keyed by a surrogate of its own, not by
// its id.
func (r *GraphQL{{.Model}}Repository) linkParents(ctx context.Context, ci *{{.ResPkg}}.CreateInput, name string, known map[string]string) error {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return err
	}
	{{- range .GQLParentLinks}}
	{{.}}
	{{- end}}
	return nil
}
{{- end}}
{{else}}
// Create persists in and returns the stored record.
func (r *GraphQL{{.Model}}Repository) Create(ctx context.Context, in *{{.PB}}) (*{{.PB}}, error) {
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
	{{- if .RequiredDocs}}
	if err := check{{.Model}}Required(in); err != nil {
		return nil, err
	}
	{{- end}}
	ci := {{.LowerModelV}}ToCreateInput(in)
	ci.Id = id
	{{- if .EtagInputField}}
	ci.{{.EtagInputField}} = repox.NewULID()
	{{- end}}
	{{- if or .CreateTimeField .UpdateTimeField}}
	now := tsToStr(timestamppb.New(time.Now().UTC()))
	{{- end}}
	{{- if .CreateTimeField}}
	ci.{{.CreateTimeField}} = now
	{{- end}}
	{{- if .UpdateTimeField}}
	ci.{{.UpdateTimeField}} = now
	{{- end}}
	{{- range .GQLVOCreates}}
	{{.}}
	{{- end}}
	{{- if or .HasDeep .Joins}}
	// One transaction: the graph lands whole or not at all.
	gtx := r.Svc.Mutation.Tx()
	{{- range .GQLDeepCreates}}
	{{.}}
	{{- end}}
	gtx.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
	{{- range .GQLDeepAfterCreates}}
	{{.}}
	{{- end}}
	{{- range .Joins}}
	{
		var ops repox.Ops
		if err := r.{{.Var}}Ops(ctx, &ops, ci.Id, in.Get{{.GoField}}(), false); err != nil {
			return nil, err
		}
		for _, op := range ops {
			gtx.Add(op)
		}
	}
	{{- end}}
	if err := gtx.Commit(ctx); err != nil {
		return nil, mapGraphQLErr(err)
	}
	{{- else}}
	if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.Create(ctx, ci); err != nil {
		return nil, mapGraphQLErr(err)
	}
	{{- end}}
	return r.get(ctx, id)
}
{{end}}

// Get returns the record addressed by its resource name.
func (r *GraphQL{{.Model}}Repository) Get(ctx context.Context, name string) (*{{.PB}}, error) {
	{{- if .NameKeyed}}
	row, err := r.find(ctx, name)
	if err != nil {
		return nil, err
	}
	return r.toProto(ctx, row, nil{{if .Joins}}, nil{{end}})
	{{- else}}
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	return r.get(ctx, ids[len(ids)-1])
	{{- end}}
}
{{- if .NameKeyed}}

// rowID returns the surrogate key of the row named name, looked up by name:
// the id is unique only under the parent, so it is not the key.
// repox.ErrNotFound when there is no such row.
func (r *GraphQL{{.Model}}Repository) rowID(ctx context.Context, name string) (string, error) {
	row, err := r.find(ctx, name)
	if err != nil {
		return "", err
	}
	return row.{{.PKInputField}}, nil
}

// find reads the row named name; repox.ErrNotFound when there is none.
func (r *GraphQL{{.Model}}Repository) find(ctx context.Context, name string) (*{{.Row}}, error) {
	if _, err := repox.SplitName(name, {{.CollectionsExpr}}); err != nil {
		return nil, err
	}
	row, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Find(ctx, {{.ResPkg}}.List().Where({{.NamePred}}.Eq(name)))
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if row == nil {
		return nil, repox.ErrNotFound
	}
	return row, nil
}
{{- else}}

// rowID returns the surrogate key of the row named name: its bare id.
func (r *GraphQL{{.Model}}Repository) rowID(_ context.Context, name string) (string, error) {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return "", err
	}
	return ids[len(ids)-1], nil
}
{{- end}}

// get loads by surrogate key — the private read every generated method
// re-reads through.
func (r *GraphQL{{.Model}}Repository) get(ctx context.Context, id string) (*{{.PB}}, error) {
	row, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Get(ctx, id)
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if row == nil {
		return nil, repox.ErrNotFound
	}
	return r.toProto(ctx, row, nil{{if .Joins}}, nil{{end}})
}

// toProto converts a loaded row, hydrating the value objects mask reads (all
// of them for an empty mask) through their stored references, and runs the
// AfterRead hook.
func (r *GraphQL{{.Model}}Repository) toProto(ctx context.Context, row *{{.Row}}, mask []string{{if .Joins}}, joined repox.Joined{{end}}) (*{{.PB}}, error) {
	out := {{.LowerModelV}}FromRow(row)
	{{- range .GQLVOHydrates}}
	{{.}}
	{{- end}}
	{{- if .Joins}}
	// The fields kept in join tables: from joined, read for row alone when
	// nil.
	if joined == nil {
		var err error
		if joined, err = r.readJoins(ctx, mask, row.{{.PKInputField}}); err != nil {
			return nil, err
		}
	}
	{{- $pk := .PKInputField}}
	{{- range .Joins}}
	out.{{.GoField}} = joined["{{.Field}}"][row.{{$pk}}]
	{{- end}}
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
func (r *GraphQL{{.Model}}Repository) List(ctx context.Context, parent string, in repox.ListInput) ([]*{{.PB}}, string, error) {
	scope, err := r.parentScope(ctx, parent)
	if err != nil {
		return nil, "", err
	}
	return r.list(ctx, in, scope...)
}

// Count returns how many records under parent match in; its page fields are
// ignored.
func (r *GraphQL{{.Model}}Repository) Count(ctx context.Context, parent string, in repox.ListInput) (int64, error) {
	scope, err := r.parentScope(ctx, parent)
	if err != nil {
		return 0, err
	}
	return r.count(ctx, in, scope...)
}

// Facet counts the records under parent matching in by the values of field,
// from the rows (see repox.FacetValues).
func (r *GraphQL{{.Model}}Repository) Facet(ctx context.Context, parent string, in repox.ListInput, field string, limit int) ([]repox.Bucket, error) {
	scope, err := r.parentScope(ctx, parent)
	if err != nil {
		return nil, err
	}
	return r.facet(ctx, in, field, limit, scope...)
}

// parentScope is the predicate of the records under parent.
func (r *GraphQL{{.Model}}Repository) parentScope(ctx context.Context, parent string) ([]graphql.Predicate, error) {
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return nil, err
	}
	// AIP-159: "-" for the parent reads across every parent. A "-" widens
	// only its own segment: under a parent naming some of its ancestors
	// ("a/1/b/-"), the records' own names are matched, so they stay under
	// those.
	if parentIDs[len(parentIDs)-1] == "-" {
		if repox.AllWild(parentIDs) {
			return nil, nil
		}
		p, _ := repox.NamePattern(parent + "/{{.OwnCollection}}/-")
		return []graphql.Predicate{ {{- .NameColPred}}.Like(p)}, nil
	}
	{{- if .ParentRows}}
	// The parent is nested, so its row is found by name.
	keys, err := {{.ParentRows.Func}}Keys(ctx, r.Svc, parent)
	if err != nil {
		return nil, err
	}
	return []graphql.Predicate{ {{- .ParentPred}}.In(keys...)}, nil
	{{- else}}
	return []graphql.Predicate{ {{- .ParentPred}}.Eq(parentIDs[len(parentIDs)-1])}, nil
	{{- end}}
}
{{else}}
// List returns one page of records.
func (r *GraphQL{{.Model}}Repository) List(ctx context.Context, in repox.ListInput) ([]*{{.PB}}, string, error) {
	return r.list(ctx, in)
}

// Count returns how many records match in; its page fields are ignored.
func (r *GraphQL{{.Model}}Repository) Count(ctx context.Context, in repox.ListInput) (int64, error) {
	return r.count(ctx, in)
}

// Facet counts the records matching in by the values of field, from the rows
// (see repox.FacetValues).
func (r *GraphQL{{.Model}}Repository) Facet(ctx context.Context, in repox.ListInput, field string, limit int) ([]repox.Bucket, error) {
	return r.facet(ctx, in, field, limit)
}
{{end}}

// engine is the list engine over the records scope selects (and, unless
// in.ShowDeleted, not deleted), with in's filter conditions.
func (r *GraphQL{{.Model}}Repository) engine(in repox.ListInput, scope ...graphql.Predicate) (*filterx.HasuraEngine[{{.Row}}], []filterx.Condition, error) {
	{{- if .SoftDelete}}
	if !in.ShowDeleted {
		scope = append(scope, {{.DeleteTimePred}}.IsNull(true))
	}
	{{- end}}
	conds, err := filterx.Parse(in.Filter)
	if err != nil {
		return nil, nil, repox.MapFilterxErr(err)
	}
	conds = append(conds, in.Where...)
	eng := filterx.Hasura[{{.Row}}]({{$.GormPkg}}.{{.Model}}FilterSpec, r.Svc.Query.{{.Domain}}.{{.Resource}}).Scope(scope...)
	for f, h := range r.ListOverrides {
		eng.Override(f, h)
	}
	{{- range .ViaLookups}}
	eng.Via("{{.Field}}", func(ctx context.Context, keys []string, where graphql.Predicate) ([]string, error) {
		rows, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.List(ctx, {{.ResPkg}}.List().Where(graphql.And({{.ResPkg}}.{{.KeyField}}.In(keys...), where)))
		ids := make([]string, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].{{.KeyField}})
		}
		return ids, err
	})
	{{- end}}
	return eng, conds, nil
}

// count is Count over scope: one aggregate, unless a condition is matched
// page by page (one reaching another table, or a hint), when the matching
// rows are counted, at most repox.GraphQLScanLimit of them.
func (r *GraphQL{{.Model}}Repository) count(ctx context.Context, in repox.ListInput, scope ...graphql.Predicate) (int64, error) {
	eng, conds, err := r.engine(in, scope...)
	if err != nil {
		return 0, err
	}
	if repox.ReachesVia({{$.GormPkg}}.{{.Model}}FilterSpec, conds) || repox.AnyHint(conds) {
		var n int64
		err := r.scan(ctx, eng, conds, func(*{{.Row}}) { n++ })
		return n, err
	}
	where, has, err := eng.Predicate(conds)
	if err != nil {
		return 0, repox.MapFilterxErr(err)
	}
	if has {
		scope = append(scope, where)
	}
	req := {{.ResPkg}}.Aggregate()
	if len(scope) > 0 {
		req = req.Where(graphql.And(scope...))
	}
	agg, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Aggregate(ctx, req)
	if err != nil || agg == nil {
		return 0, mapGraphQLErr(err)
	}
	return int64(agg.Count), nil
}

// facet is Facet over scope, counted from the matching rows, at most
// repox.GraphQLScanLimit of them (more fail with repox.ErrInvalidArgument, as
// count's do). A field reached through another table -- a value object's, a
// join table's -- is counted from that table's rows the matching rows reach
// (repox.FacetVia).
func (r *GraphQL{{.Model}}Repository) facet(ctx context.Context, in repox.ListInput, field string, limit int, scope ...graphql.Predicate) ([]repox.Bucket, error) {
	spec := {{$.GormPkg}}.{{.Model}}FilterSpec
	if _, err := repox.FacetExpr(spec, field); err != nil {
		return nil, err
	}
	eng, conds, err := r.engine(in, scope...)
	if err != nil {
		return nil, err
	}
	if f, ok := spec.Fields[field]; ok && f.Via != nil {
		{{- if .ViaLookups}}
		var refs []string
		if err := r.scan(ctx, eng, conds, func(row *{{.Row}}) {
			if ref := repox.ViaRef(spec, field, repox.RowMap(row)); ref != "" {
				refs = append(refs, ref)
			}
		}); err != nil {
			return nil, err
		}
		counts, err := repox.FacetVia(ctx, spec, field, refs, func(ctx context.Context, keys []string) ([]map[string]any, error) {
			return r.viaRows(ctx, field, keys)
		})
		if err != nil {
			return nil, err
		}
		return repox.SortBuckets(counts, limit), nil
		{{- else}}
		return nil, fmt.Errorf("%w: facet by %q, reached through another table, on GraphQL", repox.ErrInvalidArgument, field)
		{{- end}}
	}
	counts := map[string]int64{}
	if err := r.scan(ctx, eng, conds, func(row *{{.Row}}) {
		for _, v := range repox.FacetValues(spec, field, repox.RowMap(row)) {
			counts[v]++
		}
	}); err != nil {
		return nil, err
	}
	return repox.SortBuckets(counts, limit), nil
}

{{- if .ViaLookups}}

// viaRows reads the rows of the table field is reached through (filterx.Via)
// whose key is one of keys, as maps (repox.RowMap): what a facet by field
// counts.
func (r *GraphQL{{.Model}}Repository) viaRows(ctx context.Context, field string, keys []string) ([]map[string]any, error) {
	head, _, _ := strings.Cut(field, ".")
	var out []map[string]any
	switch head {
	{{- range .ViaLookups}}
	case "{{.Field}}":
		rows, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.List(ctx, {{.ResPkg}}.List().Where({{.ResPkg}}.{{.KeyField}}.In(keys...)))
		if err != nil {
			return nil, mapGraphQLErr(err)
		}
		for i := range rows {
			out = append(out, repox.RowMap(&rows[i]))
		}
	{{- end}}
	default:
		return nil, fmt.Errorf("%w: facet by %q, reached through another table, on GraphQL", repox.ErrInvalidArgument, field)
	}
	return out, nil
}
{{- end}}

// scan calls f for every row eng matches under conds, failing past
// repox.GraphQLScanLimit rows.
func (r *GraphQL{{.Model}}Repository) scan(ctx context.Context, eng *filterx.HasuraEngine[{{.Row}}], conds []filterx.Condition, f func(*{{.Row}})) error {
	in := filterx.ListInput{PageSize: 1000, Filter: conds}
	for read := 0; ; {
		rows, next, err := eng.List(ctx, in)
		if err != nil {
			return repox.MapFilterxErr(err)
		}
		for i := range rows {
			f(&rows[i])
		}
		read += len(rows)
		if next == "" {
			return nil
		}
		if read >= repox.GraphQLScanLimit {
			return fmt.Errorf("%w: more than %d rows to scan on GraphQL; narrow the filter", repox.ErrInvalidArgument, repox.GraphQLScanLimit)
		}
		in.PageToken = next
	}
}

func (r *GraphQL{{.Model}}Repository) list(ctx context.Context, in repox.ListInput, scope ...graphql.Predicate) ([]*{{.PB}}, string, error) {
	if err := repox.CheckReadMask((*{{.PB}})(nil), in.ReadMask); err != nil {
		return nil, "", err
	}
	eng, conds, err := r.engine(in, scope...)
	if err != nil {
		return nil, "", err
	}
	rows, next, err := eng.List(ctx, filterx.ListInput{
		PageSize:  in.PageSize,
		PageToken: in.PageToken,
		OrderBy:   in.OrderBy,
		Filter:    conds,
	})
	if err != nil {
		return nil, "", repox.MapFilterxErr(err)
	}
	{{- if .Joins}}
	keys := make([]string, len(rows))
	for i := range rows {
		keys[i] = rows[i].{{.PKInputField}}
	}
	joined, err := r.readJoins(ctx, in.ReadMask, keys...)
	if err != nil {
		return nil, "", err
	}
	{{- end}}
	items := make([]*{{.PB}}, 0, len(rows))
	for i := range rows {
		out, err := r.toProto(ctx, &rows[i], in.ReadMask{{if .Joins}}, joined{{end}})
		if err != nil {
			return nil, "", err
		}
		repox.Narrow(out, in.ReadMask)
		items = append(items, out)
	}
	return items, next, nil
}

// Update persists the masked fields of in; an empty mask replaces every
// mutable field. When in.Etag is set the write is guarded server-side
// (UpdateIfMatch); a stale etag returns repox.ErrConflict.
func (r *GraphQL{{.Model}}Repository) Update(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
	return r.update(ctx, in, paths, nil)
}
{{- if .OutputOnly}}

// UpdateWithOutputOnly is Update plus the server-owned fields outputPaths
// names, in the same patch and under the same guard.
func (r *GraphQL{{.Model}}Repository) UpdateWithOutputOnly(ctx context.Context, in *{{.PB}}, paths, outputPaths []string) (*{{.PB}}, error) {
	return r.update(ctx, in, paths, outputPaths)
}
{{- end}}

// update is Update's write, with the server-owned fields outputPaths names
// (UpdateWithOutputOnly) merged into its patch.
func (r *GraphQL{{.Model}}Repository) update(ctx context.Context, in *{{.PB}}, paths, outputPaths []string) (*{{.PB}}, error) {
	{{- if .NameKeyed}}
	row, err := r.find(ctx, in.GetName())
	if err != nil {
		return nil, err
	}
	id := row.{{.PKInputField}}
	{{- else}}
	ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	id := ids[len(ids)-1]
	row, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Get(ctx, id)
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if row == nil {
		return nil, repox.ErrNotFound
	}
	{{- end}}
	{{- range .GQLVOStaleVars}}
	{{.}}
	{{- end}}
	{{- range .GQLDeepStale}}
	{{.}}
	{{- end}}
	existingPB := {{.LowerModelV}}FromRow(row)
	{{- if .Joins}}
	joined, err := r.readJoins(ctx, nil, id)
	if err != nil {
		return nil, err
	}
	{{- range .Joins}}
	existingPB.{{.GoField}} = joined["{{.Field}}"][id]
	{{- end}}
	{{- end}}
	merged := proto.Clone(existingPB).(*{{.PB}})
	apply{{.Model}}Mask(merged, in, paths)
	{{- if .OutputOnly}}
	repox.CopyFields(merged, in, outputPaths)
	{{- end}}
	if h := r.Hooks.BeforeUpdate; h != nil {
		if err := h(ctx, existingPB, merged, paths); err != nil {
			return nil, err
		}
	}
	{{- if .RequiredDocs}}
	if err := check{{.Model}}Required(merged); err != nil {
		return nil, err
	}
	{{- end}}
	patch := {{.LowerModelV}}ToUpdatePatch(merged)
	{{- if .OutputOnly}}
	if len(outputPaths) > 0 {
		{{.LowerModelV}}AddOutputPatch(&patch, merged, outputPaths)
	}
	{{- end}}
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	{{- if .UpdateTimeField}}
	patch.{{.UpdateTimeField}} = graphql.Value(tsToStr(timestamppb.New(time.Now().UTC())))
	{{- end}}
	{{- range .GQLVOUpdates}}
	{{.}}
	{{- end}}
	{{- if .HasDeep}}
	// New singular value objects land before the guarded row update (a lost
	// race leaves them unreferenced); new list rows and the removal of
	// replaced subtrees land only after it succeeds (atx).
	utx := r.Svc.Mutation.Tx()
	atx := r.Svc.Mutation.Tx()
	{{- range .GQLDeepUpdates}}
	{{.}}
	{{- end}}
	if err := utx.Commit(ctx); err != nil {
		return nil, mapGraphQLErr(err)
	}
	{{- end}}
	{{- if .HasEtag}}
	if in.GetEtag() != "" {
		if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateIfMatch(ctx, id, patch, {{.EtagPred}}.Eq(in.GetEtag())); err != nil {
			return nil, mapGraphQLErr(err)
		}
		{{- range .GQLVOStaleDels}}
		{{.}}
		{{- end}}
		{{- if .HasDeep}}
		if err := atx.Commit(ctx); err != nil {
			return nil, mapGraphQLErr(err)
		}
		{{- end}}
		{{- if .Joins}}
		if err := r.writeJoins(ctx, id, merged, paths); err != nil {
			return nil, err
		}
		{{- end}}
		return r.get(ctx, id)
	}
	{{- end}}
	resp, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.Update(ctx, id, patch)
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if resp.AffectedRows == 0 {
		return nil, repox.ErrNotFound
	}
	{{- range .GQLVOStaleDels}}
	{{.}}
	{{- end}}
	{{- if .HasDeep}}
	if err := atx.Commit(ctx); err != nil {
		return nil, mapGraphQLErr(err)
	}
	{{- end}}
	{{- if .Joins}}
	if err := r.writeJoins(ctx, id, merged, paths); err != nil {
		return nil, err
	}
	{{- end}}
	return r.get(ctx, id)
}
{{- if .Joins}}

// writeJoins replaces, in one transaction, the join rows of the fields kept
// in join tables that paths names (all of them for an empty mask) with in's.
// It runs after the row update it belongs to.
func (r *GraphQL{{.Model}}Repository) writeJoins(ctx context.Context, owner string, in *{{.PB}}, paths []string) error {
	var ops repox.Ops
	{{- range .Joins}}
	if repox.InMask(paths, "{{.Field}}") {
		if err := r.{{.Var}}Ops(ctx, &ops, owner, in.Get{{.GoField}}(), true); err != nil {
			return err
		}
	}
	{{- end}}
	if len(ops) == 0 {
		return nil
	}
	tx := r.Svc.Mutation.Tx()
	for _, op := range ops {
		tx.Add(op)
	}
	return mapGraphQLErr(tx.Commit(ctx))
}

// readJoins reads the fields kept in join tables of the rows keyed by keys,
// those of them mask reads (all for an empty mask), by field and then key, in
// the order they were written.
func (r *GraphQL{{.Model}}Repository) readJoins(ctx context.Context, mask []string, keys ...string) (repox.Joined, error) {
	joined := repox.Joined{}
	if len(keys) == 0 {
		return joined, nil
	}
	{{- range .Joins}}
	if repox.Reads(mask, "{{.Field}}") {
		rows, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.List(ctx, {{.ResPkg}}.List().Where({{.ResPkg}}.{{.OwnerField}}.In(keys...)).OrderBy({{.ResPkg}}.{{.KeyField}}.Asc()))
		if err != nil {
			return nil, mapGraphQLErr(err)
		}
		got := map[string][]string{}
		for i := range rows {
			got[rows[i].{{.OwnerField}}] = append(got[rows[i].{{.OwnerField}}], {{if .NameField}}rows[i].{{.NameField}}{{else if .Collection}}"{{.Collection}}/"+rows[i].{{.TargetField}}{{else}}rows[i].{{.TargetField}}{{end}})
		}
		joined["{{.Field}}"] = got
	}
	{{- end}}
	return joined, nil
}
{{- end}}
{{- $model := .Model}}
{{- range .Joins}}

// {{.Var}}Ops queues onto ops the join rows keeping {{.Field}} for the row
// keyed owner, one per name in order (a repeat keeps its first place), after
// removing the rows it holds when replace is set. A name no record holds is
// repox.ErrForeignKey.
func (r *GraphQL{{$model}}Repository) {{.Var}}Ops(ctx context.Context, ops *repox.Ops, owner string, names []string, replace bool) error {
	if replace {
		rows, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.List(ctx, {{.ResPkg}}.List().Where({{.ResPkg}}.{{.OwnerField}}.Eq(owner)))
		if err != nil {
			return mapGraphQLErr(err)
		}
		for i := range rows {
			ops.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.DeleteOp(rows[i].{{.KeyField}}, new({{.DeleteResp}})))
		}
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		{{- if .Lookup}}
		key, found, err := {{.Lookup.Func}}Key(ctx, r.Svc, n)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: {{.Field}}: no record named %q", repox.ErrForeignKey, n)
		}
		{{- else}}
		{{- if .Collection}}
		if !strings.HasPrefix(n, "{{.Collection}}/") {
			return fmt.Errorf("%w: {{.Field}}: %q is not a {{.Collection}} name", repox.ErrInvalidArgument, n)
		}
		{{- end}}
		key := repox.LastSegment(n)
		{{- end}}
		ops.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp({{.ResPkg}}.CreateInput{
			{{.KeyField}}: repox.NewULID(), {{.OwnerField}}: owner, {{.TargetField}}: key,{{if .NameField}} {{.NameField}}: n,{{end}}
		}, new({{.InsertResp}})))
	}
	return nil
}
{{- end}}


// newInput builds the insert a Batch queues for in{{if .Parented}} under parent{{end}}, and
// the record it stands for, named: the same resolution Create applies.
func (r *GraphQL{{.Model}}Repository) newInput({{if .Parented}}parent string, {{end}}in *{{.PB}}) ({{.ResPkg}}.CreateInput, *{{.PB}}, error) {
	var ci {{.ResPkg}}.CreateInput
	{{- if .Parented}}
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return ci, nil, err
	}
	{{- end}}
	id := repox.NewULID()
	{{- if .NameKeyed}}
	rowID := id
	{{- end}}
	if in.GetName() != "" {
		ids, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}})
		if err != nil {
			return ci, nil, err
		}
		{{- if .Parented}}
		for i := range parentIDs {
			if ids[i] != parentIDs[i] {
				return ci, nil, fmt.Errorf("%w: name %q is not under parent %q", repox.ErrInvalidArgument, in.GetName(), parent)
			}
		}
		{{- end}}
		id = ids[len(ids)-1]
	}
	in = proto.Clone(in).(*{{.PB}})
	in.Name = {{if .Parented}}{{.FormatCallParented}}{{else}}Format{{.Model}}Name(id){{end}}
	{{- if .NameKeyed}}
	id = rowID // keyed as Create keys it
	{{- end}}
	{{- if .RequiredDocs}}
	if err := check{{.Model}}Required(in); err != nil {
		return ci, nil, err
	}
	{{- end}}
	ci = {{.LowerModelV}}ToCreateInput(in)
	ci.Id = id
	{{- range .GQLParentAssigns}}
	{{.}}
	{{- end}}
	{{- if .EtagInputField}}
	ci.{{.EtagInputField}} = repox.NewULID()
	{{- end}}
	{{- if or .CreateTimeField .UpdateTimeField}}
	now := tsToStr(timestamppb.New(time.Now().UTC()))
	{{- end}}
	{{- if .CreateTimeField}}
	ci.{{.CreateTimeField}} = now
	{{- end}}
	{{- if .UpdateTimeField}}
	ci.{{.UpdateTimeField}} = now
	{{- end}}
	return ci, in, nil
}
{{- if not .Flat}}

// insertOps queues the inserts that create in onto gtx, for a Batch: the
// value objects it points at, its row (ci), then its children.
func (r *GraphQL{{.Model}}Repository) insertOps(ctx context.Context, gtx *repox.Ops, in *{{.PB}}, ci {{.ResPkg}}.CreateInput) (repox.Check, error) {
	id := ci.Id
	_ = id
	{{- range .GQLQueuedCreates}}
	{{.}}
	{{- end}}
	{{- range .GQLDeepCreates}}
	{{.}}
	{{- end}}
	gtx.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
	{{- range .GQLDeepAfterCreates}}
	{{.}}
	{{- end}}
	{{- range .Joins}}
	if err := r.{{.Var}}Ops(ctx, gtx, ci.Id, in.Get{{.GoField}}(), false); err != nil {
		return nil, err
	}
	{{- end}}
	return nil, nil
}
{{- end}}

// updateOps queues Update's writes onto ops, for a Batch: the value-object
// rows that replace the masked ones, the row update, then the removal of what
// they replaced. With in.Etag set the update is guarded inside the batch: a
// self-update of the key whose post-check asserts the etag fails the request
// when the record moved on, and the guarded update after it writes the row.
//
// outputPaths names the server-owned fields written alongside
// (Update<Model>WithOutputOnly); nil for Update.
func (r *GraphQL{{.Model}}Repository) updateOps(ctx context.Context, ops *repox.Ops, id string, in *{{.PB}}, paths, outputPaths []string) (repox.Check, error) {
	row, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Get(ctx, id)
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if row == nil {
		return nil, repox.ErrNotFound
	}
	existingPB := {{.LowerModelV}}FromRow(row)
	{{- if .HasEtag}}
	if in.GetEtag() != "" && existingPB.GetEtag() != in.GetEtag() {
		return nil, repox.ErrConflict
	}
	{{- end}}
	{{- range .GQLVOStaleVars}}
	{{.}}
	{{- end}}
	{{- range .GQLDeepStale}}
	{{.}}
	{{- end}}
	merged := proto.Clone(existingPB).(*{{.PB}})
	apply{{.Model}}Mask(merged, in, paths)
	{{- if .OutputOnly}}
	repox.CopyFields(merged, in, outputPaths)
	{{- end}}
	{{- if .RequiredDocs}}
	if err := check{{.Model}}Required(merged); err != nil {
		return nil, err
	}
	{{- end}}
	patch := {{.LowerModelV}}ToUpdatePatch(merged)
	{{- if .OutputOnly}}
	if len(outputPaths) > 0 {
		{{.LowerModelV}}AddOutputPatch(&patch, merged, outputPaths)
	}
	{{- end}}
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	{{- if .UpdateTimeField}}
	patch.{{.UpdateTimeField}} = graphql.Value(tsToStr(timestamppb.New(time.Now().UTC())))
	{{- end}}
	var utx, atx repox.Ops
	{{- range .GQLQueuedUpdates}}
	{{.}}
	{{- end}}
	{{- range .GQLDeepUpdates}}
	{{.}}
	{{- end}}
	*ops = append(*ops, utx...)
	resp := new({{.UpdateResp}})
	{{- if .HasEtag}}
	etag := in.GetEtag()
	if etag != "" {
		ops.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateOp(id, {{.ResPkg}}.UpdateInput{ {{- .PKInputField}}: graphql.Value(id)}, new({{.UpdateResp}}), {{.ResPkg}}.Update().PostCheck({{.EtagPred}}.Eq(etag))))
		ops.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateOp(id, patch, resp, {{.ResPkg}}.Update().PreCheck({{.EtagPred}}.Eq(etag))))
	} else {
		ops.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateOp(id, patch, resp))
	}
	{{- else}}
	ops.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateOp(id, patch, resp))
	{{- end}}
	{{- range .GQLQueuedStaleDels}}
	{{.}}
	{{- end}}
	{{- range .Joins}}
	if repox.InMask(paths, "{{.Field}}") {
		if err := r.{{.Var}}Ops(ctx, &atx, id, merged.Get{{.GoField}}(), true); err != nil {
			return nil, err
		}
	}
	{{- end}}
	*ops = append(*ops, atx...)
	return func(ctx context.Context, failed error) error {
		if failed == nil {
			if resp.AffectedRows == 0 {
				{{- if .HasEtag}}
				if etag != "" {
					return repox.ErrConflict
				}
				{{- end}}
				return repox.ErrNotFound
			}
			return nil
		}
		{{- if .HasEtag}}
		// The request failed; if this guard is why, say so.
		if etag != "" {
			if now, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Get(ctx, id); err == nil && (now == nil || {{.LowerModelV}}FromRow(now).GetEtag() != etag) {
				return repox.ErrConflict
			}
		}
		{{- end}}
		return nil
	}, nil
}
{{- if not .Flat}}

// purgeOps queues the removal of row id and of the value-object rows it owns
// onto dtx, for a Batch. A row already gone queues nothing.
func (r *GraphQL{{.Model}}Repository) purgeOps(ctx context.Context, dtx *repox.Ops, id string) (repox.Check, error) {
	row, err := r.Svc.Query.{{.Domain}}.{{.Resource}}.Get(ctx, id)
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if row == nil {
		return nil, nil
	}
	dtx.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.DeleteOp(id, new({{.DeleteResp}})))
	{{- range .GQLQueuedPurges}}
	{{.}}
	{{- end}}
	return nil, nil
}
{{- end}}
{{- if .OutputOnly}}

// UpdateOutputOnly persists the server-owned fields of in that paths names.
// When in.Etag is set the write is guarded server-side, as in Update.
func (r *GraphQL{{.Model}}Repository) UpdateOutputOnly(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("%w: UpdateOutputOnly needs the fields to write", repox.ErrInvalidArgument)
	}
	id, err := r.rowID(ctx, in.GetName())
	if err != nil {
		return nil, err
	}
	patch := {{.LowerModelV}}ToOutputPatch(in, paths)
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	{{- if .UpdateTimeField}}
	patch.{{.UpdateTimeField}} = graphql.Value(tsToStr(timestamppb.New(time.Now().UTC())))
	{{- end}}
	{{- if .HasEtag}}
	if in.GetEtag() != "" {
		if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateIfMatch(ctx, id, patch, {{.EtagPred}}.Eq(in.GetEtag())); err != nil {
			return nil, mapGraphQLErr(err)
		}
		return r.get(ctx, id)
	}
	{{- end}}
	resp, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.Update(ctx, id, patch)
	if err != nil {
		return nil, mapGraphQLErr(err)
	}
	if resp.AffectedRows == 0 {
		return nil, repox.ErrNotFound
	}
	return r.get(ctx, id)
}
{{- end}}

{{- if .SoftDelete}}
// Delete soft-deletes the record addressed by its resource name: delete_time
// is set, the row stays.{{if .UpdateTimeField}} The update time is set to the same instant: the
// delete is the record's latest write, which a reader by update time can tell
// from any write after it.{{end}}
func (r *GraphQL{{.Model}}Repository) Delete(ctx context.Context, name string) error {
	if _, err := repox.SplitName(name, {{.CollectionsExpr}}); err != nil {
		return err
	}
	if h := r.Hooks.BeforeDelete; h != nil {
		if err := h(ctx, name); err != nil {
			return err
		}
	}
	id, err := r.rowID(ctx, name)
	if err != nil {
		return err
	}
	if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateIfMatch(ctx, id, r.deletePatch(), {{.DeleteTimePred}}.IsNull(true)); err != nil {
		if errors.Is(err, graphql.ErrConflict) {
			return repox.ErrNotFound
		}
		return mapGraphQLErr(err)
	}
	return nil
}

// deletePatch is the write that soft-deletes a record now: Delete's, and a
// Batch's.
func (r *GraphQL{{.Model}}Repository) deletePatch() {{.ResPkg}}.UpdateInput {
	at := time.Now().UTC()
	now := at.Format(time.RFC3339Nano)
	patch := {{.ResPkg}}.UpdateInput{ {{- .DeleteTimeInputField}}: graphql.Value(now)}
	{{- if .UpdateTimeField}}
	patch.{{.UpdateTimeField}} = graphql.Value(now) // the delete is the record's latest write
	{{- end}}
	{{- if .Expiring}}
	if r.Retention > 0 {
		patch.{{.ExpireTimeInputField}} = graphql.Value(at.Add(r.Retention).Format(time.RFC3339Nano))
	}
	{{- end}}
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	return patch
}

// Undelete clears the soft-delete mark and returns the restored record.
func (r *GraphQL{{.Model}}Repository) Undelete(ctx context.Context, name string) (*{{.PB}}, error) {
	id, err := r.rowID(ctx, name)
	if err != nil {
		return nil, err
	}
	patch := {{.ResPkg}}.UpdateInput{ {{- .DeleteTimeInputField}}: graphql.Null[string]()}
	{{- if .UpdateTimeField}}
	patch.{{.UpdateTimeField}} = graphql.Value(time.Now().UTC().Format(time.RFC3339Nano))
	{{- end}}
	{{- if .Expiring}}
	patch.{{.ExpireTimeInputField}} = graphql.Null[string]()
	{{- end}}
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateIfMatch(ctx, id, patch, {{.DeleteTimePred}}.IsNull(false)); err != nil {
		if !errors.Is(err, graphql.ErrConflict) {
			return nil, mapGraphQLErr(err)
		}
		if _, err := r.get(ctx, id); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s is not deleted", repox.ErrAlreadyExists, name)
	}
	return r.get(ctx, id)
}

// Purge removes the record addressed by its resource name for good.
func (r *GraphQL{{.Model}}Repository) Purge(ctx context.Context, name string) error {
{{- else}}
// Delete removes the record addressed by its resource name.
func (r *GraphQL{{.Model}}Repository) Delete(ctx context.Context, name string) error {
{{- end}}
	if _, err := repox.SplitName(name, {{.CollectionsExpr}}); err != nil {
		return err
	}
	if h := r.Hooks.BeforeDelete; h != nil {
		if err := h(ctx, name); err != nil {
			return err
		}
	}
	id, err := r.rowID(ctx, name)
	if err != nil {
		return err
	}
	{{- if .SoftDelete}}
	_, err = r.purge(ctx, id, name, true)
	return err
}

// purge removes row id, named name, for good -- unless something still
// refers to it, when it keeps the row and reports kept: a row of this
// database holding a foreign key to it (asked first{{if .GQLReferrers}}, see referenced{{end}}, then caught
// again from the database's own refusal), or, when askReferrers, a referrer
// in another database or service (r.Refs). A purge never fails, and never
// cascades, because of a referrer. repox.ErrNotFound when there is no such
// row.
func (r *GraphQL{{.Model}}Repository) purge(ctx context.Context, id, name string, askReferrers bool) (kept bool, err error) {
	{{- if .GQLReferrers}}
	if referenced, err := r.referenced(ctx, id, name); err != nil {
		return false, err
	} else if referenced {
		r.Refs.Kept(ctx, name, "foreign key")
		return true, nil
	}
	{{- end}}
	{{- if .Type}}
	if askReferrers && r.Refs.KeepOnPurge(ctx, {{printf "%q" .Type}}, name) {
		return true, nil
	}
	{{- end}}
	err = r.remove(ctx, id)
	if repox.IsForeignKeyViolation(err) {
		r.Refs.Kept(ctx, name, "foreign key")
		return true, nil
	}
	return false, err
}

{{- if .Expiring}}

// PurgeExpired removes for good every deleted record whose expire_time is at
// or before now, keeping the ones something still refers to (see purge).
// Hooks do not run.
func (r *GraphQL{{.Model}}Repository) PurgeExpired(ctx context.Context, now time.Time) (repox.PurgeResult, error) {
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
		page, next, err := r.list(ctx, in)
		if err != nil {
			return res, err
		}
		for _, m := range page {
			id, err := r.rowID(ctx, m.GetName())
			if errors.Is(err, repox.ErrNotFound) {
				continue // gone since the page was read
			}
			if err != nil {
				return res, err
			}
			kept, err := r.purge(ctx, id, m.GetName(), true)
			switch {
			case errors.Is(err, repox.ErrNotFound):
			case err != nil:
				return res, err
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
	{{- else}}
	{{- if .GQLReferrers}}
	// A join table's row naming it would cascade away with it: refused, as a
	// singular reference's constraint refuses it.
	if referenced, err := r.referenced(ctx, id, name); err != nil {
		return err
	} else if referenced {
		return repox.ReferencedByJoin({{printf "%q" .Type}}, name)
	}
	{{- end}}
	return r.remove(ctx, id)
}
	{{- end}}

// remove deletes row id with the value-object rows it owns.
func (r *GraphQL{{.Model}}Repository) remove(ctx context.Context, id string) error {
	resp, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.Delete(ctx, id)
	if err != nil {
		return mapGraphQLErr(err)
	}
	if resp.AffectedRows == 0 {
		return repox.ErrNotFound
	}
	{{- range .GQLVODeleteCleanups}}
	{{.}}
	{{- end}}
	{{- if .HasDeep}}
	dtx := r.Svc.Mutation.Tx()
	{{- range .GQLDeepDeletes}}
	{{.}}
	{{- end}}
	if err := dtx.Commit(ctx); err != nil {
		return mapGraphQLErr(err)
	}
	{{- end}}
	return nil
}
{{- if .GQLReferrers}}

// referenced reports whether a row of this database, deleted or not, still
// holds a foreign key to row id, named name -- what the GORM adapter asks
// with its {{if .SoftDelete}}ReferencedSQL{{else}}JoinReferencedSQL: a join table's row{{end}}. A key whose action cascades or
// sets null (a join table's, a reference Prisma made optional) would not
// refuse the delete.
func (r *GraphQL{{.Model}}Repository) referenced(ctx context.Context, id, name string) (bool, error) {
	for _, holds := range []struct {
		f   func(context.Context, *{{$.ClientPkg}}.Service, string) (bool, error)
		key string
	}{
		{{- range .GQLReferrers}}
		{ {{- .Func}}, {{.Value -}} },
		{{- end}}
	} {
		if found, err := holds.f(ctx, r.Svc, holds.key); err != nil || found {
			return found, err
		}
	}
	return false, nil
}
{{- end}}

// ListByKeys reads every record whose filter field field is one of keys; see
// repox.ListByKeys.
func (r *GraphQL{{.Model}}Repository) ListByKeys(ctx context.Context, field string, keys []string, in repox.ListInput) ([]*{{.PB}}, error) {
	return repox.ListByKeys(ctx, field, keys, in, func(ctx context.Context, in repox.ListInput) ([]*{{.PB}}, string, error) {
		return r.List(ctx, {{if .Parented}}{{printf "%q" .WildcardParent}}, {{end}}in)
	})
}

// Compile-time proof the adapter satisfies the interface.
var _ {{.Model}}Repository = (*GraphQL{{.Model}}Repository)(nil)
{{- end}}
{{- if .HasBatch}}

// graphQLBatch is Batch on GraphQL: queued writes go into the Unit's mutation
// request on Commit (see repox.Unit.Commit). A guarded UpdateOutputOnly runs
// ahead of it, one by one: the engine reports its failed guard by touching no
// row, which cannot abort a request.
type graphQLBatch struct {
	u   *repox.Unit
	svc *{{$.ClientPkg}}.Service
{{- if .HasExpiring}}
	retention time.Duration
{{- end}}
	// keys holds the row key of every record this batch creates, by name: a
	// later write in the batch finds its row here, since it is not stored
	// until the batch's request runs.
	keys map[string]string
}

// Commit applies the Unit's writes; see repox.Unit.Commit.
func (b *graphQLBatch) Commit(ctx context.Context) error { return b.u.Commit(ctx) }

// rowID returns the row key of the record named name: one this batch creates,
// else what resolve (a repository's rowID) finds.
func (b *graphQLBatch) rowID(ctx context.Context, name string, resolve func(context.Context, string) (string, error)) (string, error) {
	if id, ok := b.keys[name]; ok {
		return id, nil
	}
	return resolve(ctx, name)
}
{{- range .Resources}}

// Create{{.Model}} queues a create and returns the name the record will have.
func (b *graphQLBatch) Create{{.Model}}({{if .Parented}}parent string, {{end}}in *{{.PB}}) (string, error) {
	r := &GraphQL{{.Model}}Repository{Svc: b.svc}
	ci, named, err := r.newInput({{if .Parented}}parent, {{end}}in)
	if err != nil {
		return "", err
	}
	if b.keys == nil {
		b.keys = map[string]string{}
	}
	b.keys[named.GetName()] = ci.Id
	queue{{.Model}}References(b.u, named, nil)
	b.u.QueueGraphQL(func(ctx context.Context, ops *repox.Ops) (repox.Check, error) {
		{{- if .GQLParentLinks}}
		if err := r.linkParents(ctx, &ci, named.GetName(), b.keys); err != nil {
			return nil, err
		}
		{{- end}}
		{{- if .Flat}}
		ops.Add(b.svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
		return nil, nil
		{{- else}}
		return r.insertOps(ctx, ops, named, ci)
		{{- end}}
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: named.GetName(), Op: repox.ChangeCreate, Record: named})
	return named.GetName(), nil
}

// Update{{.Model}} queues Update; in.Etag, when set, guards it inside the
// batch's request (see updateOps).
func (b *graphQLBatch) Update{{.Model}}(in *{{.PB}}, paths []string) {
	if _, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}}); err != nil {
		b.u.Fail(err)
		return
	}
	r := &GraphQL{{.Model}}Repository{Svc: b.svc}
	queue{{.Model}}References(b.u, in, paths)
	b.u.QueueGraphQL(func(ctx context.Context, ops *repox.Ops) (repox.Check, error) {
		id, err := b.rowID(ctx, in.GetName(), r.rowID)
		if err != nil {
			return nil, err
		}
		return r.updateOps(ctx, ops, id, in, paths, nil)
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: in.GetName(), Op: repox.ChangeUpdate, Record: in, LoadPrior: repox.PriorOf(in.GetName(), get{{.Model}})})
}
{{- if .OutputOnly}}

// Update{{.Model}}WithOutputOnly queues UpdateWithOutputOnly; in.Etag, when
// set, guards it inside the batch's request, as Update{{.Model}}'s does.
func (b *graphQLBatch) Update{{.Model}}WithOutputOnly(in *{{.PB}}, paths, outputPaths []string) {
	if _, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}}); err != nil {
		b.u.Fail(err)
		return
	}
	r := &GraphQL{{.Model}}Repository{Svc: b.svc}
	queue{{.Model}}References(b.u, in, paths)
	b.u.QueueGraphQL(func(ctx context.Context, ops *repox.Ops) (repox.Check, error) {
		id, err := b.rowID(ctx, in.GetName(), r.rowID)
		if err != nil {
			return nil, err
		}
		return r.updateOps(ctx, ops, id, in, paths, outputPaths)
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: in.GetName(), Op: repox.ChangeUpdate, Record: in, LoadPrior: repox.PriorOf(in.GetName(), get{{.Model}})})
}
{{- end}}
{{- if .OutputOnly}}

// Update{{.Model}}OutputOnly queues UpdateOutputOnly; in.Etag, when set, guards
// it, and a guarded update runs ahead of the batch (see graphQLBatch).
func (b *graphQLBatch) Update{{.Model}}OutputOnly(in *{{.PB}}, paths []string) {
	if len(paths) == 0 {
		b.u.Fail(fmt.Errorf("%w: UpdateOutputOnly needs the fields to write", repox.ErrInvalidArgument))
		return
	}
	if _, err := repox.SplitName(in.GetName(), {{.CollectionsExpr}}); err != nil {
		b.u.Fail(err)
		return
	}
	r := &GraphQL{{.Model}}Repository{Svc: b.svc}
	patch := {{.LowerModelV}}ToOutputPatch(in, paths)
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	{{- if .UpdateTimeField}}
	patch.{{.UpdateTimeField}} = graphql.Value(tsToStr(timestamppb.New(time.Now().UTC())))
	{{- end}}
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: in.GetName(), Op: repox.ChangeUpdate, Record: in, LoadPrior: repox.PriorOf(in.GetName(), get{{.Model}})})
	{{- if .HasEtag}}
	if etag := in.GetEtag(); etag != "" {
		b.u.QueueGraphQLAhead(func(ctx context.Context) error {
			id, err := b.rowID(ctx, in.GetName(), r.rowID)
			if err != nil {
				return err
			}
			_, err = b.svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateIfMatch(ctx, id, patch, {{.EtagPred}}.Eq(etag))
			return mapGraphQLErr(err)
		})
		return
	}
	{{- end}}
	b.u.QueueGraphQL(func(ctx context.Context, ops *repox.Ops) (repox.Check, error) {
		id, err := b.rowID(ctx, in.GetName(), r.rowID)
		if err != nil {
			return nil, err
		}
		ops.Add(b.svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateOp(id, patch, new({{.UpdateResp}})))
		return nil, nil
	})
}
{{- end}}
{{- if .SoftDelete}}

// Delete{{.Model}} queues a soft delete. A record already deleted or gone is
// not an error.
func (b *graphQLBatch) Delete{{.Model}}(name string) {
	if _, err := repox.SplitName(name, {{.CollectionsExpr}}); err != nil {
		b.u.Fail(err)
		return
	}
	r := &GraphQL{{.Model}}Repository{Svc: b.svc{{if .Expiring}}, Retention: b.retention{{end}}}
	queue{{.Model}}Delete(b.u, name, false)
	b.u.QueueGraphQL(func(ctx context.Context, ops *repox.Ops) (repox.Check, error) {
		id, err := b.rowID(ctx, name, r.rowID)
		if errors.Is(err, repox.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		ops.Add(b.svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateOp(id, r.deletePatch(), new({{.UpdateResp}}), {{.ResPkg}}.Update().PreCheck({{.DeleteTimePred}}.IsNull(true))))
		return nil, nil
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: name, Op: repox.ChangeDelete, LoadPrior: repox.PriorOf(name, get{{.Model}})})
}
{{- end}}

// {{if .SoftDelete}}Purge{{else}}Delete{{end}}{{.Model}} queues the record's removal for good. A record already
// gone is not an error.
func (b *graphQLBatch) {{if .SoftDelete}}Purge{{else}}Delete{{end}}{{.Model}}(name string) {
	if _, err := repox.SplitName(name, {{.CollectionsExpr}}); err != nil {
		b.u.Fail(err)
		return
	}
	r := &GraphQL{{.Model}}Repository{Svc: b.svc{{if .SoftDelete}}, Refs: b.u.Conn().References{{end}}}
	{{- if .SoftDelete}}
	// A record a referrer in another database or service names is kept, as
	// Purge keeps it{{if .GQLReferrers}}, and so is one a row of this database is found to
	// hold a foreign key to when the purge is queued{{end}}. One a foreign key of
	// this database {{if .GQLReferrers}}names by then{{else}}still names{{end}} fails the commit with
	// repox.ErrForeignKey: the request is one transaction, and the engine
	// cannot skip one of its mutations.
	keep := queue{{.Model}}Purge(b.u, name)
	{{- else}}
	queue{{.Model}}Delete(b.u, name, true)
	{{- end}}
	b.u.QueueGraphQL(func(ctx context.Context, ops *repox.Ops) (repox.Check, error) {
		{{- if .SoftDelete}}
		if *keep {
			return nil, nil
		}
		{{- end}}
		id, err := b.rowID(ctx, name, r.rowID)
		if errors.Is(err, repox.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		{{- if and .SoftDelete .GQLReferrers}}
		if referenced, err := r.referenced(ctx, id, name); err != nil || referenced {
			if referenced {
				r.Refs.Kept(ctx, name, "foreign key")
			}
			return nil, err
		}
		{{- else if .GQLReferrers}}
		if referenced, err := r.referenced(ctx, id, name); err != nil {
			return nil, err
		} else if referenced {
			return nil, repox.ReferencedByJoin({{printf "%q" .Type}}, name)
		}
		{{- end}}
		{{- if .Flat}}
		ops.Add(b.svc.Mutation.{{.Domain}}.{{.Resource}}.DeleteOp(id, new({{.DeleteResp}})))
		return nil, nil
		{{- else}}
		return r.purgeOps(ctx, ops, id)
		{{- end}}
	})
	b.u.Record(repox.Change{Type: {{printf "%q" .Type}}, Name: name, Op: {{if .SoftDelete}}repox.ChangePurge{{else}}repox.ChangeDelete{{end}}, LoadPrior: repox.PriorOf(name, get{{.Model}})})
}
{{- end}}
{{- end}}
