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
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := bytes.ToLower(b)
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, w := range banned {
			if bytes.Contains(lower, []byte(w)) {
				t.Errorf("%s: private identifier %q must not appear in a public repository", rel, w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
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
