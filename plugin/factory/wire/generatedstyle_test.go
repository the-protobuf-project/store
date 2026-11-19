package wire_test

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Generated files carry no separator comments (`// ----`, `-- ====`,
// `// ── section ──`) and no TODO, FIXME or XXX: output people commit is not a
// place for scaffolding or a to-do list. The goldens and the committed
// examples are everything the plugin writes, so checking them checks it.
var (
	commentLine = regexp.MustCompile(`^\s*(//|#|--|/\*|\*)`)
	separator   = regexp.MustCompile(`-{4,}|={4,}|[─━]{2,}`)
	todo        = regexp.MustCompile(`\b(TODO|FIXME|XXX)\b`)
)

func TestGeneratedStyle(t *testing.T) {
	for _, root := range []string{"testdata/cases", "../../../examples/generated"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !generated(path) {
				return err
			}
			checkStyle(t, path)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

// generated reports whether path is protoc-gen-store output: under a golden
// directory or the examples' generated tree, and not another plugin's file.
func generated(path string) bool {
	slash := filepath.ToSlash(path)
	if !strings.Contains(slash, "/golden/") && !strings.Contains(slash, "examples/generated/") {
		return false
	}
	// protoc-gen-go and protoc-gen-go-grpc write these, not this plugin.
	return !strings.HasSuffix(slash, ".pb.go")
}

func checkStyle(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if commentLine.MatchString(line) && separator.MatchString(line) {
			t.Errorf("%s:%d: separator comment in generated output: %s", path, n, strings.TrimSpace(line))
		}
		if todo.MatchString(line) {
			t.Errorf("%s:%d: to-do marker in generated output: %s", path, n, strings.TrimSpace(line))
		}
	}
}
