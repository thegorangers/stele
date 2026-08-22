//go:build parity

// Package parity holds the acceptance test that decides whether this tool may
// replace the one in use.
//
// The criterion is not a unit test's: it is that exporting a real repository
// reproduces, byte for byte, the vendored tree that repository already carries
// — because that tree was produced by the tool being replaced. Anything less
// is an opinion about correctness; this is a measurement of it.
//
// The corpus lives OUTSIDE this repository and is named by
// STELE_PARITY_CORPUS. Nothing here may know the name of any organisation,
// repository or host: this is a public repository, and a check in
// internal/hygiene enforces that. Without the variable the test skips, which
// is the honest outcome — a corpus is a set of checkouts, and no unit-test run
// has one.
//
//	go test -tags parity -run TestExport_MatchesVendoredTree ./test/parity/
package parity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/thegorangers/stele/internal/export"
	"gopkg.in/yaml.v3"
)

// EnvCorpus names the file describing the checkouts to compare against.
const EnvCorpus = "STELE_PARITY_CORPUS"

// Corpus is the external description of what to compare.
//
//	root: /path/to/checkouts
//	repos:
//	  - dir: some/repo          # relative to root
//	    vendored: third_party/proto
//	    paths: [example/v1]     # optional; --path, module-relative
//	    exclude_imports: false  # optional; --exclude-imports
type Corpus struct {
	// Root is the directory the checkouts sit in.
	Root string `yaml:"root"`
	// Repos are the checkouts to compare, one export each.
	Repos []Repo `yaml:"repos"`
}

// Repo is one checkout to compare.
type Repo struct {
	// Dir is the repository, relative to the corpus root.
	Dir string `yaml:"dir"`
	// Vendored is the tree the export must reproduce, relative to Dir.
	Vendored string `yaml:"vendored"`
	// Paths, when set, is passed as --path, in coordinates relative to the
	// module root.
	Paths []string `yaml:"paths"`
	// ExcludeImports mirrors --exclude-imports.
	ExcludeImports bool `yaml:"exclude_imports"`
}

func TestExport_MatchesVendoredTree(t *testing.T) {
	corpus := loadCorpus(t)
	for _, r := range corpus.Repos {
		t.Run(r.Dir, func(t *testing.T) {
			repo := filepath.Join(corpus.Root, r.Dir)
			got := t.TempDir()
			exportPerManifest(t, repo, r, got)
			assertTreesEqual(t, filepath.Join(repo, r.Vendored), got)
		})
	}
}

// loadCorpus reads the corpus file, or skips.
func loadCorpus(t *testing.T) Corpus {
	t.Helper()
	path := os.Getenv(EnvCorpus)
	if path == "" {
		t.Skipf("%s is not set; the acceptance corpus lives outside this repository", EnvCorpus)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", EnvCorpus, err)
	}
	var c Corpus
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if c.Root == "" {
		t.Fatalf("%s: root is missing", path)
	}
	if len(c.Repos) == 0 {
		// An empty corpus would pass in silence, which is the one outcome an
		// acceptance test must never produce.
		t.Fatalf("%s: repos is empty", path)
	}
	for i, r := range c.Repos {
		if r.Dir == "" || r.Vendored == "" {
			t.Fatalf("%s: repos[%d]: dir and vendored are both required", path, i)
		}
	}
	return c
}

// exportPerManifest exports every manifest the repository carries into one
// output tree.
//
// Every manifest, because a repository may hold more than one, and the
// vendored tree it carries is the union of what they need. The vendored tree
// itself is not searched for manifests: it is output, not source.
func exportPerManifest(t *testing.T, repo string, r Repo, out string) {
	t.Helper()
	manifests := findManifests(t, repo, filepath.Join(repo, r.Vendored))
	if len(manifests) == 0 {
		t.Fatalf("%s carries no %s", repo, export.ManifestName)
	}
	for _, dir := range manifests {
		err := export.Run(context.Background(), export.Options{
			Dir:            dir,
			Output:         out,
			Paths:          r.Paths,
			ExcludeImports: r.ExcludeImports,
			// The corpus is somebody's checkout. Writing a lock into it would
			// make running the acceptance test modify the thing it measures.
			NoLock: true,
		})
		if err != nil {
			t.Fatalf("exporting %s: %v", dir, err)
		}
	}
}

// findManifests returns every directory under repo holding a manifest, except
// under skip.
func findManifests(t *testing.T, repo, skip string) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || p == skip {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == export.ManifestName {
			dirs = append(dirs, filepath.Dir(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(dirs)
	return dirs
}

// assertTreesEqual compares two trees and reports the first difference in a
// form that says what to do about it.
func assertTreesEqual(t *testing.T, want, got string) {
	t.Helper()
	wantFiles := listFiles(t, want)
	gotFiles := listFiles(t, got)

	for _, p := range union(wantFiles, gotFiles) {
		_, inWant := wantFiles[p]
		_, inGot := gotFiles[p]
		switch {
		case !inGot:
			t.Fatalf("%s is in the vendored tree but was not exported", p)
		case !inWant:
			t.Fatalf("%s was exported but is not in the vendored tree", p)
		}
		a, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("%s differs\n%s", p, firstDifference(a, b))
		}
	}
}

// listFiles returns the regular files of a tree, keyed by slash-separated
// relative path. A non-regular entry is an error rather than a skip, for the
// same reason the lock refuses one: its content is not what is being compared.
func listFiles(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files := map[string]bool{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", slashed)
		}
		files[slashed] = true
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return files
}

func union(a, b map[string]bool) []string {
	seen := map[string]bool{}
	for p := range a {
		seen[p] = true
	}
	for p := range b {
		seen[p] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// firstDifference renders where two files start to disagree, by line, with the
// two lines shown. A byte offset alone would say that something is wrong
// without saying what.
func firstDifference(want, got []byte) string {
	wl := bytes.Split(want, []byte("\n"))
	gl := bytes.Split(got, []byte("\n"))
	for i := 0; i < len(wl) || i < len(gl); i++ {
		w, g := line(wl, i), line(gl, i)
		if w == g {
			continue
		}
		return fmt.Sprintf("  line %d\n  vendored: %q\n  exported: %q", i+1, w, g)
	}
	return "  the files differ in bytes but not in lines"
}

func line(lines [][]byte, i int) string {
	if i >= len(lines) {
		return ""
	}
	return string(lines[i])
}
