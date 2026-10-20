{{.Header}}

package {{.Package}}

{{.Imports}}

{{range .Enums}}
// {{.LocalName}}FromProto maps the proto enum onto its stored form ("" for
// unspecified or unknown values, so the column is omitted / left to default).
func {{.LocalName}}FromProto(v {{.PbType}}) {{.LocalName}} {
	switch v {
{{range .Pairs}}	case {{.PbConst}}:
		return {{.ModelConst}}
{{end}}	}
	return ""
}

// {{.LocalName}}ToProto maps the stored form back onto the proto enum
// (unspecified for empty or unknown values).
func {{.LocalName}}ToProto(v {{.LocalName}}) {{.PbType}} {
	switch v {
{{range .Pairs}}	case {{.ModelConst}}:
		return {{.PbConst}}
{{end}}	}
	return 0
}

// {{.LocalName}}PtrFromProto is {{.LocalName}}FromProto for nullable columns:
// unspecified maps to nil.
func {{.LocalName}}PtrFromProto(v {{.PbType}}) *{{.LocalName}} {
	if s := {{.LocalName}}FromProto(v); s != "" {
		return &s
	}
	return nil
}

// {{.LocalName}}PtrToProto is {{.LocalName}}ToProto for nullable columns.
func {{.LocalName}}PtrToProto(v *{{.LocalName}}) {{.PbType}} {
	if v == nil {
		return 0
	}
	return {{.LocalName}}ToProto(*v)
}
{{end}}
{{range .Models}}
// {{.Name}}ToProto converts the stored row (with its belongs-to associations,
// when preloaded) to its proto message. Nil in, nil out.
func {{.Name}}ToProto(m *{{.Name}}) *{{.PbType}} {
	if m == nil {
		return nil
	}
	out := &{{.PbType}}{}
{{if .ToSkip}}	{{.ToSkip}}
{{end}}{{range .ToLines}}	{{.}}
{{end}}	return out
}

// {{.Name}}FromProto maps the proto message's data fields onto a fresh row.
// It never invents keys or relations: primary keys, resource references, and
// sub-row graph wiring stay with the caller. Nil in, nil out.
func {{.Name}}FromProto(pb *{{.PbType}}) *{{.Name}} {
	if pb == nil {
		return nil
	}
	m := &{{.Name}}{}
{{if .FromSkip}}	{{.FromSkip}}
{{end}}{{range .FromLines}}	{{.}}
{{end}}	return m
}
{{end}}
{{if .Needs.Ptr}}
// toPtr returns a pointer to v, or nil for the zero value — a nullable column
// stores absence, not the proto zero.
func toPtr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// fromPtr dereferences p, returning the zero value for nil.
func fromPtr[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
{{end}}{{if .Needs.Ts}}
// tsToGo maps a proto timestamp onto a nullable TIMESTAMPTZ value.
func tsToGo(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// tsToGoVal maps a proto timestamp onto a NOT NULL TIMESTAMPTZ value.
func tsToGoVal(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// goToTs maps a nullable TIMESTAMPTZ value back onto a proto timestamp.
func goToTs(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

// goValToTs maps a NOT NULL TIMESTAMPTZ value back onto a proto timestamp.
func goValToTs(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
{{end}}{{if .Needs.DT}}
// dtToGo maps a google.type.DateTime onto a nullable TIMESTAMPTZ instant. A
// time zone resolves through the IANA database and a UTC offset as a fixed
// zone; a floating value (neither) has no instant of its own and is read as
// UTC.
func dtToGo(d *datetime.DateTime) *time.Time {
	if d == nil {
		return nil
	}
	t := dtToGoVal(d)
	return &t
}

// dtToGoVal is dtToGo for a NOT NULL column.
func dtToGoVal(d *datetime.DateTime) time.Time {
	if d == nil {
		return time.Time{}
	}
	loc := time.UTC
	switch {
	case d.GetTimeZone().GetId() != "":
		if l, err := time.LoadLocation(d.GetTimeZone().GetId()); err == nil {
			loc = l
		}
	case d.GetUtcOffset() != nil:
		loc = time.FixedZone("", int(d.GetUtcOffset().AsDuration()/time.Second))
	}
	return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()),
		int(d.GetHours()), int(d.GetMinutes()), int(d.GetSeconds()), int(d.GetNanos()), loc)
}

// goToDt maps a TIMESTAMPTZ instant back onto a DateTime, in UTC: the stored
// instant is what survives, not the zone it was written in.
func goToDt(t *time.Time) *datetime.DateTime {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &datetime.DateTime{
		Year: int32(u.Year()), Month: int32(u.Month()), Day: int32(u.Day()),
		Hours: int32(u.Hour()), Minutes: int32(u.Minute()), Seconds: int32(u.Second()), Nanos: int32(u.Nanosecond()),
		TimeOffset: &datetime.DateTime_UtcOffset{UtcOffset: durationpb.New(0)},
	}
}
{{end}}{{if .Needs.Date}}
// dateToGo maps a proto date onto a nullable DATE value (UTC midnight).
func dateToGo(d *date.Date) *time.Time {
	if d == nil || (d.GetYear() == 0 && d.GetMonth() == 0 && d.GetDay() == 0) {
		return nil
	}
	t := time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
	return &t
}

// goToDate maps a nullable DATE value back onto a proto date.
func goToDate(t *time.Time) *date.Date {
	if t == nil || t.IsZero() {
		return nil
	}
	return &date.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}
{{end}}{{if .Needs.DateVal}}
// dateToGoVal maps a proto date onto a NOT NULL DATE value (UTC midnight).
func dateToGoVal(d *date.Date) time.Time {
	if d == nil || (d.GetYear() == 0 && d.GetMonth() == 0 && d.GetDay() == 0) {
		return time.Time{}
	}
	return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
}

// goValToDate maps a NOT NULL DATE value back onto a proto date.
func goValToDate(t time.Time) *date.Date {
	if t.IsZero() {
		return nil
	}
	return &date.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}
{{end}}{{if .Needs.Tod}}
// todToGo maps a proto time-of-day onto a nullable TIME value (carried on the
// zero date).
func todToGo(t *timeofday.TimeOfDay) *time.Time {
	if t == nil {
		return nil
	}
	v := time.Date(0, time.January, 1, int(t.GetHours()), int(t.GetMinutes()), int(t.GetSeconds()), 0, time.UTC)
	return &v
}

// goToTod maps a nullable TIME value back onto a proto time-of-day.
func goToTod(t *time.Time) *timeofday.TimeOfDay {
	if t == nil || t.IsZero() {
		return nil
	}
	return &timeofday.TimeOfDay{Hours: int32(t.Hour()), Minutes: int32(t.Minute()), Seconds: int32(t.Second())}
}
{{end}}{{if .Needs.TodVal}}
// todToGoVal maps a proto time-of-day onto a NOT NULL TIME value (carried on
// the zero date).
func todToGoVal(t *timeofday.TimeOfDay) time.Time {
	if t == nil {
		return time.Time{}
	}
	return time.Date(0, time.January, 1, int(t.GetHours()), int(t.GetMinutes()), int(t.GetSeconds()), 0, time.UTC)
}

// goValToTod maps a NOT NULL TIME value back onto a proto time-of-day.
func goValToTod(t time.Time) *timeofday.TimeOfDay {
	if t.IsZero() {
		return nil
	}
	return &timeofday.TimeOfDay{Hours: int32(t.Hour()), Minutes: int32(t.Minute()), Seconds: int32(t.Second())}
}
{{end}}{{if .Needs.Dur}}
// durToGo maps a proto duration onto a nullable INTERVAL-as-text value
// (Go duration syntax, e.g. "1h30m0s").
func durToGo(d *durationpb.Duration) *string {
	if d == nil {
		return nil
	}
	s := d.AsDuration().String()
	return &s
}

// goToDur parses a stored Go duration string back onto a proto duration.
func goToDur(s *string) *durationpb.Duration {
	if s == nil || *s == "" {
		return nil
	}
	d, err := time.ParseDuration(*s)
	if err != nil {
		return nil
	}
	return durationpb.New(d)
}
{{end}}{{if .Needs.DurVal}}
// durToGoVal maps a proto duration onto a NOT NULL INTERVAL-as-text value
// (Go duration syntax, e.g. "1h30m0s").
func durToGoVal(d *durationpb.Duration) string {
	if d == nil {
		return ""
	}
	return d.AsDuration().String()
}

// goValToDur parses a NOT NULL stored Go duration string back onto a proto
// duration.
func goValToDur(s string) *durationpb.Duration {
	if s == "" {
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil
	}
	return durationpb.New(d)
}
{{end}}{{if .Needs.EnumCSV}}
// enumsToCSV stores a repeated proto enum as the comma-joined list of its
// value names; nil for an empty list, so the column stays NULL.
func enumsToCSV[E interface {
	~int32
	fmt.Stringer
}](vs []E) *string {
	if len(vs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(vs))
	for _, v := range vs {
		parts = append(parts, v.String())
	}
	s := strings.Join(parts, ",")
	return &s
}

// enumsFromCSV parses a stored comma-joined list of proto enum value names
// back onto the repeated enum, resolving each name through the enum's value
// map; unknown names map to 0.
func enumsFromCSV[E ~int32](s *string, byName map[string]int32) []E {
	if s == nil || *s == "" {
		return nil
	}
	names := strings.Split(*s, ",")
	out := make([]E, 0, len(names))
	for _, n := range names {
		out = append(out, E(byName[strings.TrimSpace(n)]))
	}
	return out
}
{{end}}{{if .Needs.JSON}}
// structToJSON maps a proto struct onto a nullable JSONB value.
func structToJSON(s *structpb.Struct) json.RawMessage {
	if s == nil {
		return nil
	}
	b, err := s.MarshalJSON()
	if err != nil {
		return nil
	}
	return b
}

// jsonToStruct maps a stored JSONB value back onto a proto struct.
func jsonToStruct(b json.RawMessage) *structpb.Struct {
	if len(b) == 0 {
		return nil
	}
	s := &structpb.Struct{}
	if err := s.UnmarshalJSON(b); err != nil {
		return nil
	}
	return s
}
{{end}}