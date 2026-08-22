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
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/config"
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
//	    export:                 # optional; absent means export is not compared
//	      manifest: manifests/some-repo.yaml   # relative to the corpus file
//	      invocations:                         # one per invocation of the other tool
//	        - dep: some-producer
//	          exclude_imports: true
//	          paths: [example/v1]
//	    generate:               # optional; absent means generate is not compared
//	      manifest: manifests/some-repo.yaml   # relative to the corpus file
//	      include_imports: false               # optional
type Corpus struct {
	// Root is the directory the checkouts sit in.
	Root string `yaml:"root"`
	// Cache is where fetched dependency repositories are kept. Empty means
	// this tool's own cache directory. It is named here rather than made a
	// temporary directory because the closure of a real repository includes
	// trees of thousands of files, and re-fetching them for every run would
	// make the acceptance test too slow to run.
	Cache string `yaml:"cache"`
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
	// Export, when present, asks for the vendored tree to be reproduced.
	Export *Export `yaml:"export"`
	// Generate, when present, asks for generation to be compared as well.
	Generate *Generate `yaml:"generate"`
}

// Export describes how to reproduce a checkout's vendored tree with this tool.
//
// The manifest is named rather than taken from the checkout, for the reason
// given on Generate. The invocations are listed rather than derived: the
// vendored tree is the union of several runs of the other tool, each pointed
// at a different producer, and that list is a fact about the checkout's build
// recipe, not something a manifest states.
type Export struct {
	// Manifest is the stele.yaml to export with, relative to the corpus file.
	Manifest string `yaml:"manifest"`
	// Invocations are the runs whose union is the vendored tree.
	Invocations []Invocation `yaml:"invocations"`
}

// Invocation is one export run.
type Invocation struct {
	// Dep names the dependency to export. Empty exports the manifest's own
	// modules.
	Dep string `yaml:"dep"`
	// Paths mirrors --path, in coordinates relative to the module root.
	Paths []string `yaml:"paths"`
	// ExcludeImports mirrors --exclude-imports.
	ExcludeImports bool `yaml:"exclude_imports"`
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
		if r.Export != nil {
			if r.Export.Manifest == "" {
				t.Fatalf("%s: repos[%d]: export needs a manifest", path, i)
			}
			if len(r.Export.Invocations) == 0 {
				// No invocations would export nothing and then compare
				// nothing, reporting success.
				t.Fatalf("%s: repos[%d]: export needs at least one invocation", path, i)
			}
		}
	}
	c.dir = filepath.Dir(path)
	return c
}

// runExport reproduces a checkout's vendored tree with this tool, into out.
//
// The checkout is staged rather than exported from, for the reason given on
// runStele: the manifest lives in the corpus, and a test must not write into
// the thing it measures. Every invocation writes into the same output tree,
// because the vendored tree is their union — that is how the other tool
// produced it.
func runExport(t *testing.T, c Corpus, repo string, e Export, out string) {
	t.Helper()
	stage := stageManifest(t, c, repo, e.Manifest)
	cache, err := cachedir.Root(c.Cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, inv := range e.Invocations {
		err := export.Run(context.Background(), export.Options{
			Dir:            stage,
			Output:         out,
			Dep:            inv.Dep,
			Paths:          inv.Paths,
			ExcludeImports: inv.ExcludeImports,
			CacheRoot:      cache,
			// The corpus is somebody's checkout, and the manifest is the
			// corpus's. Writing a lock would make running the acceptance test
			// modify the thing it measures.
			NoLock: true,
		})
		if err != nil {
			t.Fatalf("exporting dep %q of %s: %v", inv.Dep, repo, err)
		}
	}
}

// stageManifest copies the modules a corpus manifest declares out of a
// checkout, puts the manifest beside them, and returns the directory.
func stageManifest(t *testing.T, c Corpus, repo, manifest string) string {
	t.Helper()
	full := filepath.Join(c.dir, filepath.FromSlash(manifest))
	raw, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(full)
	if err != nil {
		t.Fatalf("%s: %v", full, err)
	}
	stage := t.TempDir()
	for _, m := range cfg.Modules {
		copyTree(t, filepath.Join(repo, filepath.FromSlash(m.Path)), filepath.Join(stage, filepath.FromSlash(m.Path)))
	}
	if err := os.WriteFile(filepath.Join(stage, export.ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return stage
}

// assertTreesEqual compares two trees and reports every difference in a form
// that says what to do about it.
//
// Every difference, not the first: a tree of dozens of files that disagrees in
// one way about twenty of them and another way about one is a single finding
// and a single fix, and stopping at the first turns that into twenty runs. The
// count comes first because it is what distinguishes "one stale file" from
// "the wrong set entirely", and the contents of the first differing file are
// printed because a difference in bytes is otherwise unreadable.
func assertTreesEqual(t *testing.T, want, got string) {
	t.Helper()
	wantFiles := listFiles(t, want)
	gotFiles := listFiles(t, got)

	var missing, extra, differing []string
	var firstDiff string
	for _, p := range union(wantFiles, gotFiles) {
		_, inWant := wantFiles[p]
		_, inGot := gotFiles[p]
		switch {
		case !inGot:
			missing = append(missing, p)
			continue
		case !inWant:
			extra = append(extra, p)
			continue
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
			if firstDiff == "" {
				firstDiff = fmt.Sprintf("%s differs\n%s", p, describeDifference(a, b))
			}
			differing = append(differing, p)
		}
	}

	if len(missing) == 0 && len(extra) == 0 && len(differing) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d file(s) expected, %d produced: %d missing, %d unexpected, %d differing\n",
		len(wantFiles), len(gotFiles), len(missing), len(extra), len(differing))
	report(&b, "expected but not produced", missing)
	report(&b, "produced but not expected", extra)
	report(&b, "produced with different content", differing)
	if firstDiff != "" {
		fmt.Fprintf(&b, "\nfirst difference in content:\n%s", firstDiff)
	}
	t.Fatal(b.String())
}

// listLimit caps how many paths one category prints. A category with hundreds
// of entries has already said what it needs to with its count.
const listLimit = 25

func report(b *strings.Builder, what string, paths []string) {
	if len(paths) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s (%d):\n", what, len(paths))
	for i, p := range paths {
		if i == listLimit {
			fmt.Fprintf(b, "  ... and %d more\n", len(paths)-listLimit)
			break
		}
		fmt.Fprintf(b, "  %s\n", p)
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
