package wire_test

// goldentracked_test.go guards a trap that is invisible locally and only shows
// up in CI.
//
// The prisma target generates a .gitignore into its output tree, and that file
// lists .env. Because the golden tree is a faithful copy of the output, the
// generated .gitignore lands in the repository — where git honours it. So the
// .env golden sitting beside it becomes untracked, `git add` silently skips it,
// and every local run still passes because the file is right there on disk. CI
// then checks out a tree without it and fails on a missing golden.
//
// A nested .gitignore takes precedence over any rule in a parent, so this cannot
// be fixed once and for all from the root .gitignore: the file has to be added
// with `git add -f`. What can be fixed is the silence, which is what this test
// is for — it fails at the moment the golden is written rather than in CI.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldensAreTracked requires every file under testdata to be known to git.
//
// It runs `git ls-files` rather than inspecting .gitignore rules, because the
// question is not "should this be ignored?" but the one that actually breaks CI:
// "will a fresh checkout have this file?".
func TestGoldensAreTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// A source tree exported without .git (a module zip, a vendored copy) has
	// nothing to compare against.
	if _, err := os.Stat(filepath.Join("..", "..", "..", ".git")); err != nil {
		t.Skip("not a git checkout")
	}

	out, err := exec.Command("git", "ls-files", "testdata").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	tracked := map[string]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			tracked[filepath.Clean(line)] = true
		}
	}
	if len(tracked) == 0 {
		t.Fatal("git ls-files testdata returned nothing; the guard would pass vacuously")
	}

	var untracked []string
	err = filepath.WalkDir("testdata", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || tracked[filepath.Clean(path)] {
			return nil
		}
		untracked = append(untracked, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk testdata: %v", err)
	}

	for _, p := range untracked {
		t.Errorf("%s exists on disk but is not tracked by git, so CI will not see it — add it with:\n\tgit add -f %s", p, p)
	}
}
