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
	// ListOverrides substitute the generated dispatch for single filter fields.
	ListOverrides map[string]filterx.GraphQLHandler
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
	ci := {{.LowerModelV}}ToCreateInput(in)
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
	{{- range .GQLVOCreates}}
	{{.}}
	{{- end}}
	{{- if .HasDeep}}
	// One transaction: the graph lands whole or not at all.
	gtx := r.Svc.Mutation.Tx()
	{{- range .GQLDeepCreates}}
	{{.}}
	{{- end}}
	gtx.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
	{{- range .GQLDeepAfterCreates}}
	{{.}}
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
	{{- if .HasDeep}}
	// One transaction: the graph lands whole or not at all.
	gtx := r.Svc.Mutation.Tx()
	{{- range .GQLDeepCreates}}
	{{.}}
	{{- end}}
	gtx.Add(r.Svc.Mutation.{{.Domain}}.{{.Resource}}.CreateOp(ci, new({{.InsertResp}})))
	{{- range .GQLDeepAfterCreates}}
	{{.}}
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
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	return r.get(ctx, ids[len(ids)-1])
}

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
	return r.toProto(ctx, row)
}

// toProto converts a loaded row, hydrating value objects through their
// stored references, and runs the AfterRead hook.
func (r *GraphQL{{.Model}}Repository) toProto(ctx context.Context, row *{{.Row}}) (*{{.PB}}, error) {
	out := {{.LowerModelV}}FromRow(row)
	{{- range .GQLVOHydrates}}
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
func (r *GraphQL{{.Model}}Repository) List(ctx context.Context, parent string, in repox.ListInput) ([]*{{.PB}}, string, error) {
	parentIDs, err := repox.SplitName(parent, {{.ParentCollExpr}})
	if err != nil {
		return nil, "", err
	}
	return r.list(ctx, in, {{.ParentPred}}.Eq(parentIDs[len(parentIDs)-1]))
}
{{else}}
// List returns one page of records.
func (r *GraphQL{{.Model}}Repository) List(ctx context.Context, in repox.ListInput) ([]*{{.PB}}, string, error) {
	return r.list(ctx, in)
}
{{end}}

func (r *GraphQL{{.Model}}Repository) list(ctx context.Context, in repox.ListInput, scope ...graphql.Predicate) ([]*{{.PB}}, string, error) {
	{{- if .SoftDelete}}
	if !in.ShowDeleted {
		scope = append(scope, {{.DeleteTimePred}}.IsNull(true))
	}
	{{- end}}
	conds, err := filterx.Parse(in.Filter)
	if err != nil {
		return nil, "", repox.MapFilterxErr(err)
	}
	eng := filterx.Hasura[{{.Row}}]({{$.GormPkg}}.{{.Model}}FilterSpec, r.Svc.Query.{{.Domain}}.{{.Resource}}).Scope(scope...)
	for f, h := range r.ListOverrides {
		eng.Override(f, h)
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
// mutable field. When in.Etag is set the write is guarded server-side
// (UpdateIfMatch); a stale etag returns repox.ErrConflict.
func (r *GraphQL{{.Model}}Repository) Update(ctx context.Context, in *{{.PB}}, paths []string) (*{{.PB}}, error) {
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
	{{- range .GQLVOStaleVars}}
	{{.}}
	{{- end}}
	{{- range .GQLDeepStale}}
	{{.}}
	{{- end}}
	existingPB := {{.LowerModelV}}FromRow(row)
	merged := proto.Clone(existingPB).(*{{.PB}})
	apply{{.Model}}Mask(merged, in, paths)
	if h := r.Hooks.BeforeUpdate; h != nil {
		if err := h(ctx, existingPB, merged, paths); err != nil {
			return nil, err
		}
	}
	patch := {{.LowerModelV}}ToUpdatePatch(merged)
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
	return r.get(ctx, id)
}

{{- if .SoftDelete}}
// Delete soft-deletes the record addressed by its resource name: delete_time
// is set, the row stays.
func (r *GraphQL{{.Model}}Repository) Delete(ctx context.Context, name string) error {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return err
	}
	if h := r.Hooks.BeforeDelete; h != nil {
		if err := h(ctx, name); err != nil {
			return err
		}
	}
	patch := {{.ResPkg}}.UpdateInput{ {{- .DeleteTimeInputField}}: graphql.Value(time.Now().UTC().Format(time.RFC3339Nano))}
	{{- if .EtagInputField}}
	patch.{{.EtagInputField}} = graphql.Value(repox.NewULID())
	{{- end}}
	if _, err := r.Svc.Mutation.{{.Domain}}.{{.Resource}}.UpdateIfMatch(ctx, ids[len(ids)-1], patch, {{.DeleteTimePred}}.IsNull(true)); err != nil {
		if errors.Is(err, graphql.ErrConflict) {
			return repox.ErrNotFound
		}
		return mapGraphQLErr(err)
	}
	return nil
}

// Undelete clears the soft-delete mark and returns the restored record.
func (r *GraphQL{{.Model}}Repository) Undelete(ctx context.Context, name string) (*{{.PB}}, error) {
	ids, err := repox.SplitName(name, {{.CollectionsExpr}})
	if err != nil {
		return nil, err
	}
	id := ids[len(ids)-1]
	patch := {{.ResPkg}}.UpdateInput{ {{- .DeleteTimeInputField}}: graphql.Null[string]()}
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

// Compile-time proof the adapter satisfies the interface.
var _ {{.Model}}Repository = (*GraphQL{{.Model}}Repository)(nil)
{{- end}}
