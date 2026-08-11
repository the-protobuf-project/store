{{.Header}}

package {{.Package}}

{{.Imports}}

// {{.Iface}} is the full data-access surface of {{.Name}}: everything
// {{.Store}} does, as an interface.
//
// It exists so a decorator — caching, tracing, retries, a test double — can be
// written in its own package against this contract, without that package needing
// to edit anything in this tree. Embed it, override the methods you care about,
// and delegate the rest:
//
//	type cached{{.Name}}Store struct {
//		{{.Iface}}
//		cache Cache
//	}
//
// Construction is deliberately absent. New{{.Store}} returns the concrete
// *{{.Store}}, and WithTelemetry is a chainable setter that also returns it, so
// a caller keeps the full type at the point of wiring and narrows to this
// interface at the point of use — which is the direction that stays useful when
// a decorator is added later.
type {{.Iface}} interface {
	Create(ctx context.Context, m *{{.Name}}) error
	List(ctx context.Context, opts gormx.ListOptions) ([]{{.Name}}, error)
	Count(ctx context.Context, opts gormx.ListOptions) (int64, error)
	Update(ctx context.Context, m *{{.Name}}) error
{{- if .HasPK}}
	GetByID(ctx context.Context, id {{.PKArgType}}) (*{{.Name}}, error)
	DeleteByID(ctx context.Context, id {{.PKArgType}}) error
{{- end}}
{{- range .UniqueFinders}}
	{{.Method}}(ctx context.Context, v {{.ArgType}}) (*{{$.Name}}, error)
{{- end}}
{{- range .FKFinders}}
	{{.Method}}(ctx context.Context, id {{.ArgType}}, opts gormx.ListOptions) ([]{{$.Name}}, error)
{{- end}}
}

// {{.Store}} provides typed CRUD access to {{.Name}} records.
// {{.Comment}}
type {{.Store}} struct {
	DB *gorm.DB
	// Telemetry observes every operation; nil is a no-op. Wire the generated
	// adapter: New{{.Store}}(db).WithTelemetry(telemetry.New(o)).
	Telemetry gormx.Telemetry
}

// Compile-time proof that {{.Store}} implements its own interface. Without it a
// signature could drift from {{.Iface}} and only break in whichever downstream
// package decorates it.
var _ {{.Iface}} = (*{{.Store}})(nil)
{{- if .AssertStore}}

// Compile-time proof that {{.Store}} satisfies the generic gormx.Store, so the
// generic engine can drive it alongside the typed finders below.
var _ gormx.Store[{{.Name}}] = (*{{.Store}})(nil)
{{- end}}

// New{{.Store}} returns a {{.Store}} backed by db.
func New{{.Store}}(db *gorm.DB) *{{.Store}} { return &{{.Store}}{DB: db} }

// WithTelemetry sets the store's Telemetry and returns the store for chaining.
func (s *{{.Store}}) WithTelemetry(t gormx.Telemetry) *{{.Store}} {
	s.Telemetry = t
	return s
}

// Create inserts m.
func (s *{{.Store}}) Create(ctx context.Context, m *{{.Name}}) error {
{{- if .Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if .Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{.SpanPrefix}}/Create", m, func(ctx context.Context) error {
		return s.DB.WithContext(ctx).Create(m).Error
	})
{{- if .Metrics}}
	tel.RecordOp(ctx, "{{.TableName}}", "create", time.Since(start), err)
{{- end}}
	return err
{{- else}}
	return s.DB.WithContext(ctx).Create(m).Error
{{- end}}
}

// List returns the {{.Name}} records matching opts.
func (s *{{.Store}}) List(ctx context.Context, opts gormx.ListOptions) ([]{{.Name}}, error) {
	var out []{{.Name}}
{{- if .Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if .Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{.SpanPrefix}}/List", nil, func(ctx context.Context) error {
		return opts.Apply(s.DB.WithContext(ctx)).Find(&out).Error
	})
{{- if .Metrics}}
	tel.RecordOp(ctx, "{{.TableName}}", "list", time.Since(start), err)
{{- end}}
	if err != nil {
		return nil, err
	}
{{- else}}
	if err := opts.Apply(s.DB.WithContext(ctx)).Find(&out).Error; err != nil {
		return nil, err
	}
{{- end}}
	return out, nil
}

// Count returns the number of {{.Name}} records matching opts.Where
// (pagination and ordering are ignored).
func (s *{{.Store}}) Count(ctx context.Context, opts gormx.ListOptions) (int64, error) {
	var n int64
{{- if .Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if .Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{.SpanPrefix}}/Count", nil, func(ctx context.Context) error {
		db := s.DB.WithContext(ctx).Model(&{{.Name}}{})
		if opts.Where != nil {
			db = db.Where(opts.Where, opts.Args...)
		}
		return db.Count(&n).Error
	})
{{- if .Metrics}}
	tel.RecordOp(ctx, "{{.TableName}}", "count", time.Since(start), err)
{{- end}}
	if err != nil {
		return 0, err
	}
{{- else}}
	db := s.DB.WithContext(ctx).Model(&{{.Name}}{})
	if opts.Where != nil {
		db = db.Where(opts.Where, opts.Args...)
	}
	if err := db.Count(&n).Error; err != nil {
		return 0, err
	}
{{- end}}
	return n, nil
}

// Update persists every field of m, which must carry its primary key.
func (s *{{.Store}}) Update(ctx context.Context, m *{{.Name}}) error {
{{- if .Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if .Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{.SpanPrefix}}/Update", m, func(ctx context.Context) error {
		return s.DB.WithContext(ctx).Save(m).Error
	})
{{- if .Metrics}}
	tel.RecordOp(ctx, "{{.TableName}}", "update", time.Since(start), err)
{{- end}}
	return err
{{- else}}
	return s.DB.WithContext(ctx).Save(m).Error
{{- end}}
}
{{if .HasPK}}
// GetByID fetches the {{.Name}} with the given primary key.
func (s *{{.Store}}) GetByID(ctx context.Context, id {{.PKArgType}}) (*{{.Name}}, error) {
	var m {{.Name}}
{{- if .Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if .Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{.SpanPrefix}}/GetByID", nil, func(ctx context.Context) error {
		return s.DB.WithContext(ctx).First(&m, "{{.PKColumn}} = ?", id).Error
	})
{{- if .Metrics}}
	tel.RecordOp(ctx, "{{.TableName}}", "get", time.Since(start), err)
{{- end}}
	if err != nil {
		return nil, err
	}
{{- else}}
	if err := s.DB.WithContext(ctx).First(&m, "{{.PKColumn}} = ?", id).Error; err != nil {
		return nil, err
	}
{{- end}}
	return &m, nil
}

// DeleteByID removes the {{.Name}} with the given primary key.
func (s *{{.Store}}) DeleteByID(ctx context.Context, id {{.PKArgType}}) error {
{{- if .Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if .Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{.SpanPrefix}}/DeleteByID", nil, func(ctx context.Context) error {
		return s.DB.WithContext(ctx).Delete(&{{.Name}}{}, "{{.PKColumn}} = ?", id).Error
	})
{{- if .Metrics}}
	tel.RecordOp(ctx, "{{.TableName}}", "delete", time.Since(start), err)
{{- end}}
	return err
{{- else}}
	return s.DB.WithContext(ctx).Delete(&{{.Name}}{}, "{{.PKColumn}} = ?", id).Error
{{- end}}
}
{{end}}
{{- range .UniqueFinders}}
// {{.Method}} fetches the {{$.Name}} with the given {{.Column}} (a unique column).
func (s *{{$.Store}}) {{.Method}}(ctx context.Context, v {{.ArgType}}) (*{{$.Name}}, error) {
	var m {{$.Name}}
{{- if $.Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if $.Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{$.SpanPrefix}}/{{.Method}}", nil, func(ctx context.Context) error {
		return s.DB.WithContext(ctx).First(&m, "{{.Column}} = ?", v).Error
	})
{{- if $.Metrics}}
	tel.RecordOp(ctx, "{{$.TableName}}", "{{.Op}}", time.Since(start), err)
{{- end}}
	if err != nil {
		return nil, err
	}
{{- else}}
	if err := s.DB.WithContext(ctx).First(&m, "{{.Column}} = ?", v).Error; err != nil {
		return nil, err
	}
{{- end}}
	return &m, nil
}
{{end}}
{{- range .FKFinders}}
// {{.Method}} returns the {{$.Name}} records whose {{.Column}} matches id, with opts applied.
func (s *{{$.Store}}) {{.Method}}(ctx context.Context, id {{.ArgType}}, opts gormx.ListOptions) ([]{{$.Name}}, error) {
	var out []{{$.Name}}
{{- if $.Telemetry}}
	tel := gormx.OrNop(s.Telemetry)
{{- if $.Metrics}}
	start := time.Now()
{{- end}}
	err := tel.Span(ctx, "{{$.SpanPrefix}}/{{.Method}}", nil, func(ctx context.Context) error {
		return opts.Apply(s.DB.WithContext(ctx).Where("{{.Column}} = ?", id)).Find(&out).Error
	})
{{- if $.Metrics}}
	tel.RecordOp(ctx, "{{$.TableName}}", "{{.Op}}", time.Since(start), err)
{{- end}}
	if err != nil {
		return nil, err
	}
{{- else}}
	q := opts.Apply(s.DB.WithContext(ctx).Where("{{.Column}} = ?", id))
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
{{- end}}
	return out, nil
}
{{end}}
