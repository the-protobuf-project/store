{{.Header}}

package {{.Package}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

{{- if .Needs.Ts}}

// tsToStr / strToTs cross the timestamptz boundary as RFC 3339 strings.
func tsToStr(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339Nano)
}

func strToTs(s string) *timestamppb.Timestamp {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return timestamppb.New(t)
		}
	}
	return nil
}
{{- end}}
{{- if .Needs.DT}}

// dtToStr / strToDt cross the timestamptz boundary for a google.type.DateTime:
// written as the instant it denotes (a time zone through the IANA database, a
// UTC offset as a fixed zone, a floating value as UTC), read back in UTC.
func dtToStr(d *datetime.DateTime) string {
	if d == nil {
		return ""
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
		int(d.GetHours()), int(d.GetMinutes()), int(d.GetSeconds()), int(d.GetNanos()), loc).
		UTC().Format(time.RFC3339Nano)
}

func strToDt(s string) *datetime.DateTime {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999Z07:00", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &datetime.DateTime{
				Year: int32(u.Year()), Month: int32(u.Month()), Day: int32(u.Day()),
				Hours: int32(u.Hour()), Minutes: int32(u.Minute()), Seconds: int32(u.Second()), Nanos: int32(u.Nanosecond()),
				TimeOffset: &datetime.DateTime_UtcOffset{UtcOffset: durationpb.New(0)},
			}
		}
	}
	return nil
}
{{- end}}
{{- if .Needs.Date}}

// dateToStr / strToDate cross the date boundary as "2006-01-02" strings.
func dateToStr(d *date.Date) string {
	if d == nil || (d.GetYear() == 0 && d.GetMonth() == 0 && d.GetDay() == 0) {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

func strToDate(s string) *date.Date {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil
	}
	return &date.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}
{{- end}}
{{- if .Needs.Struct}}

// structToJSON / jsonToStruct cross the jsonb boundary as raw JSON.
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

func jsonToStruct(r *json.RawMessage) *structpb.Struct {
	if r == nil || len(*r) == 0 {
		return nil
	}
	s := &structpb.Struct{}
	if err := s.UnmarshalJSON(*r); err != nil {
		return nil
	}
	return s
}
{{- end}}
{{- if .Needs.Bytes}}

// bytesToRaw / rawToBytes cross the bytea boundary in Postgres hex form
// ("\x<hex>"), the representation ndc-postgres round-trips.
func bytesToRaw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	out, _ := json.Marshal(`\x` + hex.EncodeToString(b))
	return out
}

func rawToBytes(r *json.RawMessage) []byte {
	if r == nil {
		return nil
	}
	var s string
	if err := json.Unmarshal(*r, &s); err != nil || s == "" {
		return nil
	}
	if strings.HasPrefix(s, `\x`) {
		if b, err := hex.DecodeString(s[2:]); err == nil {
			return b
		}
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
{{- end}}
{{- if .Needs.Interval}}

// intervalToStr / strToInterval cross a google.type.Interval as text: both
// ends RFC 3339 in UTC joined by "/", as the gorm converter stores it.
func intervalToStr(iv *interval.Interval) string {
	if iv == nil {
		return ""
	}
	end := func(ts *timestamppb.Timestamp) string {
		if ts == nil {
			return ""
		}
		return ts.AsTime().UTC().Format(time.RFC3339Nano)
	}
	return end(iv.GetStartTime()) + "/" + end(iv.GetEndTime())
}

func strToInterval(s string) *interval.Interval {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return nil
	}
	end := func(v string) *timestamppb.Timestamp {
		t, err := time.Parse(time.RFC3339Nano, v)
		if v == "" || err != nil {
			return nil
		}
		return timestamppb.New(t)
	}
	return &interval.Interval{StartTime: end(a), EndTime: end(b)}
}
{{- end}}
{{- if .Needs.Duration}}

// durToStr / strToDur cross the interval boundary as Go duration strings.
func durToStr(d *durationpb.Duration) string {
	if d == nil {
		return ""
	}
	return d.AsDuration().String()
}

func strToDur(s string) *durationpb.Duration {
	if s == "" {
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil
	}
	return durationpb.New(d)
}
{{- end}}
{{- if .Needs.StrSlice}}

// strPtrsToSlice / strSliceToPtrs cross the text[] boundary: the client
// represents array elements as nullable (*string).
func strPtrsToSlice(in []*string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}

func strSliceToPtrs(in []string) []*string {
	if len(in) == 0 {
		return nil
	}
	out := make([]*string, 0, len(in))
	for i := range in {
		out = append(out, &in[i])
	}
	return out
}
{{- end}}
{{- if .Needs.Decimal}}

// bigdec / fromBigdec cross the numeric boundary as decimal strings.
func bigdec(f float64) graphql.Bigdecimal {
	return graphql.Bigdecimal(strconv.FormatFloat(f, 'f', -1, 64))
}

func fromBigdec(b graphql.Bigdecimal) float64 {
	f, _ := strconv.ParseFloat(string(b), 64)
	return f
}
{{- end}}

{{- range .Resources}}

// {{.LowerModelV}}ToCreateInput maps the proto onto the client's insert input.
// Identity (id, name resolution), parentage, audit timestamps, and etag are
// set by the adapter.
func {{.LowerModelV}}ToCreateInput(in *{{.PB}}) {{.ResPkg}}.CreateInput {
	var ci {{.ResPkg}}.CreateInput
	{{- range .InputAssigns}}
	{{.}}
	{{- end}}
	return ci
}

// {{.LowerModelV}}ToUpdatePatch maps the merged proto onto a replace-write
// patch of every mutable column (mask semantics already applied to merged),
// mirroring the gorm adapter's write-back exactly.
func {{.LowerModelV}}ToUpdatePatch(merged *{{.PB}}) {{.ResPkg}}.UpdateInput {
	var patch {{.ResPkg}}.UpdateInput
	{{- range .PatchAssigns}}
	{{.}}
	{{- end}}
	return patch
}

{{- if .OutputOnly}}

// {{.LowerModelV}}ToOutputPatch maps the server-owned fields paths names onto
// a patch, for UpdateOutputOnly: every other column is left as it is.
func {{.LowerModelV}}ToOutputPatch(merged *{{.PB}}, paths []string) {{.ResPkg}}.UpdateInput {
	var patch {{.ResPkg}}.UpdateInput
	{{- range .OutputPatchAssigns}}
	{{.}}
	{{- end}}
	return patch
}
{{- end}}

// {{.LowerModelV}}FromRow re-hydrates the proto from a client row, decorating
// reference names from their stored bare ids.
func {{.LowerModelV}}FromRow(row *{{.Row}}) *{{.PB}} {
	if row == nil {
		return nil
	}
	out := &{{.PB}}{}
	{{- range .RowToProto}}
	{{.}}
	{{- end}}
	return out
}
{{- end}}

{{- range .VOConvs}}

// {{.ConvName}}ToCreateInput maps the value-object proto onto its client
// insert input; the adapter mints the id and wires the reference.
func {{.ConvName}}ToCreateInput(in *{{.PBType}}) {{.InputType}} {
	var ci {{.InputType}}
	{{- range .InputAssigns}}
	{{.}}
	{{- end}}
	return ci
}

// {{.ConvName}}FromRow re-hydrates the value-object proto from a client row.
func {{.ConvName}}FromRow(row *{{.RowType}}) *{{.PBType}} {
	if row == nil {
		return nil
	}
	out := &{{.PBType}}{}
	{{- range .RowToProto}}
	{{.}}
	{{- end}}
	return out
}
{{- end}}
