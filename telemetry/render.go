package telemetry

// render.go emits the generated telemetry packages: the SDK adapter every
// instrumented store observes through, and the filterx observer that reports
// query-engine work.
//
// These are the only generated files that import the opentelementry SDK. Keeping
// their templates here rather than in the GORM target is what makes the eventual
// protoc-gen-telemetry a `git mv` — the target asks for the files, it does not
// know what is in them.

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"io"

	"github.com/the-protobuf-project/protokit/schema"
	"github.com/the-protobuf-project/protokit/templates"
)

//go:embed templates/*.tpl
var templateFS embed.FS

// tmpl is this package's parsed template set, keyed by file base name.
var tmpl = templates.MustParse(templateFS, "templates/*.tpl")

// AdapterPath is the generated adapter's path, relative to the output root.
const AdapterPath = Package + "/" + Package + ".go"

// AdapterNote is the banner note describing what the adapter is, so a caller
// building the header states the same thing this package would.
const AdapterNote = "First-party opentelementry adapter: the stores' gormx.Telemetry and the SQL-level gorm plugin."

// WriteAdapter renders the SDK adapter package for db into w.
//
// header is the caller's rendered banner. It arrives from outside rather than
// being built here because a banner names the plugin that produced the file, and
// that is the host binary's identity — today protoc-gen-store, tomorrow a
// standalone protoc-gen-telemetry. Taking it as an argument is also what keeps
// this package free of any dependency on the plugin that currently hosts it.
//
// goModule is the import path of the output directory, needed because the
// adapter imports the generated gormx runtime by its full path. stores reports
// whether typed stores were generated, which decides whether the adapter carries
// the store-level gormx.Telemetry implementation or only the SQL-level plugin.
func WriteAdapter(w io.Writer, db *schema.Database, header, goModule, gormxPkg string, stores bool) error {
	return renderGo(w, "telemetry.go.tpl", map[string]any{
		"Header":               header,
		"Package":              Package,
		"Stores":               stores,
		"Metrics":              Metrics(db),
		"Logs":                 Logs(db),
		"OpentelementryImport": Module,
		"GormxImport":          goModule + "/" + gormxPkg,
	})
}

// WriteFilterxObserver renders the filterx engine's observer into w. view is the
// filterx package's own template data, which this package passes through
// untouched — the observer is one file of that package and shares its shape.
func WriteFilterxObserver(w io.Writer, view map[string]any) error {
	return renderGo(w, "filterx_observer.go.tpl", view)
}

// renderGo executes a template into w, gofmt-formatting it first so import order
// does not depend on template emission order, and so a malformed template
// surfaces as a clear error instead of broken Go.
func renderGo(w io.Writer, name string, data any) error {
	var buf bytes.Buffer
	if err := templates.Render(tmpl, &buf, name, data); err != nil {
		return err
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("gofmt %s: %w\nrendered source:\n%s", name, err, buf.String())
	}
	_, err = w.Write(formatted)
	return err
}
