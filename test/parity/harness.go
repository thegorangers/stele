//go:build parity

// Package parity holds the acceptance tests that decide whether this tool may
// replace the one in use.
//
// The criterion is not a unit test's. Exporting a real repository must
// reproduce, byte for byte, the vendored tree that repository already carries,
// and generating from a real repository must reproduce, byte for byte, what
// the other tool generates from the same sources — because both trees were
// produced by the tool being replaced. Anything less is an opinion about
// correctness; this is a measurement of it.
//
// The corpus lives OUTSIDE this repository and is named by
// STELE_PARITY_CORPUS. Nothing here may know the name of any organisation,
// repository or host: this is a public repository, and a check in
// internal/hygiene enforces that. Without the variable the tests skip, which
// is the honest outcome — a corpus is a set of checkouts, and no unit-test run
// has one.
//
//	go test -tags parity ./test/parity/
//
// This file is the harness: everything the two acceptance tests share, and
// nothing about either one. It is a non-test file on purpose — a test file
// cannot be seen from a non-test one, and the corpus has to be described once
// and compared one way.
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
//	buf: /path/to/other/tool   # optional; the reference binary, default "buf"
//	repos:
//	  - dir: some/repo          # relative to root
//	    vendored: third_party/proto
//	    paths: [example/v1]     # optional; --path, module-relative
//	    exclude_imports: false  # optional; --exclude-imports
//	    generate:               # optional; absent means generate is not compared
//	      manifest: manifests/some-repo.yaml   # relative to the corpus file
//	      include_imports: false               # optional
type Corpus struct {
	// Root is the directory the checkouts sit in.
	Root string `yaml:"root"`
	// Buf is the reference binary the output is measured against. Empty means
	// "buf", found on PATH. It is named in the corpus rather than in code
	// because which build produced a repository's committed output is a fact
	// about that checkout, not about this tool.
	Buf string `yaml:"buf"`
	// Repos are the checkouts to compare, one export each.
	Repos []Repo `yaml:"repos"`
	// dir is the directory the corpus file itself sits in; paths inside the
	// corpus that name files of the corpus rather than of a checkout are
	// relative to it.
	dir string `yaml:"-"`
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
	// Generate, when present, asks for generation to be compared as well.
	Generate *Generate `yaml:"generate"`
}

// Generate describes how to generate from a checkout with this tool.
//
// The manifest is named rather than taken from the checkout, and it lives in
// the corpus, because a checkout under measurement has not been migrated yet:
// its configuration is the other tool's. Writing one into it would make the
// acceptance test modify the thing it measures, and would also decide the
// migration before the measurement that is supposed to justify it.
type Generate struct {
	// Manifest is the stele.yaml to generate with, relative to the corpus
	// file.
	Manifest string `yaml:"manifest"`
	// IncludeImports mirrors --include-imports on both tools.
	IncludeImports bool `yaml:"include_imports"`
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
		if r.Generate != nil && r.Generate.Manifest == "" {
			t.Fatalf("%s: repos[%d]: generate needs a manifest", path, i)
		}
	}
	c.dir = filepath.Dir(path)
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
			t.Fatalf("%s differs\n%s", p, describeDifference(a, b))
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

// copyTree copies a directory recursively. It exists so that a run of this
// tool happens somewhere other than in the checkout being measured.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
