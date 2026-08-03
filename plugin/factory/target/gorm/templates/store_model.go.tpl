{{.Header}}

package {{.Package}}

{{.Imports}}

// {{.Store}} provides typed CRUD access to {{.Name}} records.
// {{.Comment}}
type {{.Store}} struct {
	DB *gorm.DB
	// Telemetry observes every operation; nil is a no-op. Wire the generated
	// adapter: New{{.Store}}(db).WithTelemetry(telemetry.New(o)).
	Telemetry gormx.Telemetry
}
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
{{- if .Validation}}
	// Validated before the span: a value rejected here never reaches the
	// database, and the span and its op metric describe a database operation.
	if err := m.Validate(); err != nil {
		return err
	}
{{- end}}
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
{{- if .Validation}}
	if err := m.Validate(); err != nil {
		return err
	}
{{- end}}
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
