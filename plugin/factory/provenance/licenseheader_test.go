// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package provenance_test

// licenseheader_test.go guards the other half of this package's job.
//
// provenance.Render puts the licence block on top of every *generated* file, and
// the goldens under wire/testdata/cases/licenseheader prove it. Nothing, though,
// watches the hand-written sources — and they are the ones a person edits, so
// they are the ones a new file arrives in without a header. A reviewer noticing
// a missing two-line preamble in a 300-line diff is not a control.
//
// The check runs off `git ls-files` rather than a filesystem walk for the same
// reason TestGoldensAreTracked does: the question is what a fresh checkout
// contains, not what happens to be on this disk. Fixtures under testdata/ are
// excluded — they are inputs and expected outputs, not shipped source, and
// several are deliberately minimal in ways a preamble would obscure.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is this package's distance from the module root.
const repoRoot = "../../.."

// TestSourceFilesCarryLicenseHeader requires every tracked Go and proto source
// to open with the block in LICENSE.header, commented for Go.
//
// LICENSE.header is the single source of truth: it is what the license_header
// opt feeds to generated output, so a change there moves the hand-written files
// and the generated ones together instead of letting the two drift.
func TestSourceFilesCarryLicenseHeader(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// A source tree exported without .git (a module zip, a vendored copy) has
	// no file list to check.
	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		t.Skip("not a git checkout")
	}

	raw, err := os.ReadFile(filepath.Join(repoRoot, "LICENSE.header"))
	if err != nil {
		t.Fatalf("read LICENSE.header: %v", err)
	}
	var want strings.Builder
	for _, ln := range strings.Split(strings.Trim(string(raw), "\n"), "\n") {
		want.WriteString("// " + ln + "\n")
	}

	cmd := exec.Command("git", "ls-files", "*.go", "*.proto")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}

	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" || strings.Contains(rel, "/testdata/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !strings.HasPrefix(string(b), want.String()) {
			t.Errorf("%s does not start with the LICENSE.header block.\n"+
				"Hand-written sources: run `just headers`.\n"+
				"Generated output: regenerate it (`just regen`) — protoc-gen-go carries the\n"+
				"block over from the .proto's leading comment, and protoc-gen-store emits it\n"+
				"from the license_header opt.", rel)
		}
	}
}
