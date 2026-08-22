// Package hygiene contains repository-wide checks that protect the public
// nature of this project. It ships no runtime code.
package hygiene

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bannedIdentifiers returns names that belong to the private deployment this
// tool was first built for. They must never appear anywhere in this
// repository: not in code, not in test data, not in error messages, not in
// documentation.
//
// The words are assembled from fragments on purpose. Spelling them out here
// would make this very file trip the check it implements.
func bannedIdentifiers() []string {
	base := "fu" + "ud"
	return []string{
		base,
		"the" + base,
		"gitlab.com/" + base,
		base + "apis",
	}
}

// TestNoOrganizationSpecificIdentifiers walks the whole repository and fails
// on any file that mentions a private identifier. There are deliberately no
// exemptions: a file that needs one does not belong in a public repository.
func TestNoOrganizationSpecificIdentifiers(t *testing.T) {
	banned := bannedIdentifiers()
	root := repoRoot(t)

	for _, rel := range publishedFiles(t, root) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		lower := bytes.ToLower(b)
		for _, w := range banned {
			if bytes.Contains(lower, []byte(w)) {
				t.Errorf("%s: private identifier %q must not appear in a public repository", rel, w)
			}
		}
	}
}

// publishedFiles returns every file the repository would publish: what is
// tracked, plus what is untracked and not ignored.
//
// The scope is the repository, not the directory it happens to sit in. Walking
// the directory instead would fail on a developer's ignored editor state — a
// file that is not in the repository, cannot reach anyone, and is none of this
// check's business — and a check that fails on somebody's IDE gets exempted,
// which is the one thing this check must never acquire. Where git cannot say,
// the walk is the fallback and errs towards checking too much.
func publishedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err == nil {
		var files []string
		for _, name := range strings.Split(string(out), "\x00") {
			if name == "" {
				continue
			}
			// A tracked file may have been deleted in the working tree.
			if info, err := os.Lstat(filepath.Join(root, name)); err != nil || !info.Mode().IsRegular() {
				continue
			}
			files = append(files, name)
		}
		return files
	}
	return walkFiles(t, root)
}

// walkFiles is the fallback for a tree that is not a git checkout.
func walkFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// repoRoot returns the root of the repository the test is running from.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err == nil {
		if p := strings.TrimSpace(string(out)); p != "" && p != os.DevNull {
			return filepath.Dir(p)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", wd)
		}
		dir = parent
	}
}
