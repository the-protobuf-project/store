// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package wire_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGoPathsHaveNoUnderscores pins the rule for every Go path this plugin
// generates: no directory of Go output, and no import of one, holds an
// underscore. It walks the goldens of the Go targets and the Go examples, so a
// new target or a new path into an existing one is covered as it lands.
func TestGoPathsHaveNoUnderscores(t *testing.T) {
	roots, err := filepath.Glob("testdata/cases/*/golden/*")
	if err != nil {
		t.Fatal(err)
	}
	var goRoots []string
	for _, r := range roots {
		switch filepath.Base(r) {
		case "gorm", "repository", "opensearch", "graphql":
			goRoots = append(goRoots, r)
		}
	}
	goRoots = append(goRoots, filepath.Join("..", "..", "..", "examples", "generated", "go"))

	// Only paths the generator builds are its to answer for: the proto stubs'
	// go_package and third-party modules arrive from outside.
	generated := []string{"example.com/test/gen", "store/examples/generated/go"}
	for _, root := range goRoots {
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if d.IsDir() {
				if strings.Contains(rel, "_") {
					t.Errorf("%s: Go directory %q holds an underscore", root, rel)
				}
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				for _, g := range generated {
					if strings.Contains(p, g) && strings.Contains(p, "_") {
						t.Errorf("%s imports %q, which holds an underscore", path, p)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
