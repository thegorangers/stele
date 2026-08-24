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
// There are two corpora, and the harness cannot tell them apart on purpose.
//
//   - The one that ships with the tool, in test/parity/corpus. It is synthetic,
//     public, and committed, so that parity is measured on every change by
//     anyone who checks the repository out. What it deliberately does not cover
//     is written down in test/parity/corpus/README.md.
//   - A private one, named by STELE_PARITY_CORPUS: real checkouts of a real
//     fleet. Nothing here may know the name of any organisation, repository or
//     host — this is a public repository, and a check in internal/hygiene
//     enforces that — so that corpus stays outside, and the variable is how it
//     is pointed at.
//
// Without the variable the shipped corpus is used. Without either, the tests
// skip.
//
//	go test -tags parity ./test/parity/
//	STELE_PARITY_CORPUS=/path/to/corpus.yaml go test -tags parity ./test/parity/
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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/cachedir"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/export"
	"github.com/thegorangers/stele/internal/plugin"
	"github.com/thegorangers/stele/internal/resolve"
	"gopkg.in/yaml.v3"
)

// EnvCorpus names the file describing the checkouts to compare against. It
// overrides the corpus that ships with the tool.
const EnvCorpus = "STELE_PARITY_CORPUS"

// shippedCorpus is the corpus committed to this repository, relative to this
// package's directory.
const shippedCorpus = "corpus/corpus.yaml"

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
//	          reference:            # required when vendored is absent
//	            input: third_party/some-producer/proto
//	            paths: [third_party/some-producer/proto/example/v1]
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
	// BufVersion, when set, is the exact version the reference binary must
	// report. Parity against a moving reference measures nothing: a change in
	// the other tool would arrive as a failure in this one, and the reader
	// would have no way to tell the two apart. So the version is declared by
	// the corpus and checked before anything is compared, rather than being
	// whatever the runner happened to install.
	BufVersion string `yaml:"buf_version"`
	// BufDownloads are the published binaries of the reference tool at
	// BufVersion, one entry per platform, in exactly the vocabulary a manifest
	// uses to pin a plugin: os, arch, url, sha256. When Buf names no binary,
	// the entry matching this machine is downloaded, verified against its
	// digest, and used.
	//
	// It is here, beside the version, because the version and the bytes are
	// one pin. They used to be two: the version in this file and the digest in
	// a CI workflow, which failed closed but could be half-updated, and left
	// somebody running the harness on a workstation measuring against a build
	// nothing in the corpus described. A pin split across two files is a pin
	// with a seam.
	BufDownloads []config.Download `yaml:"buf_downloads"`
	// Producers maps the dependency addresses this corpus declares onto trees
	// on disk. A corpus that names one fetches nothing.
	Producers []Producer `yaml:"producers"`
	// Repos are the checkouts to compare, one export each.
	Repos []Repo `yaml:"repos"`
	// dir is the directory the corpus file itself sits in; paths inside the
	// corpus that name files of the corpus rather than of a checkout are
	// relative to it.
	dir string `yaml:"-"`
}

// Producer answers one dependency address with a tree that is already on disk.
//
// It exists because a corpus can be committed but a clone cannot: a shipped
// corpus that fetched would need a reachable host, a real commit, and a
// network, and would then be measuring git rather than generation. What is
// under measurement is the bytes the plugins write, and those depend on the
// producer's tree, not on how it arrived. The cost is stated where it belongs:
// a corpus that declares producers does not exercise fetching or the cache,
// and the shipped corpus's README says so.
type Producer struct {
	// Git is the address a manifest of the corpus declares.
	Git string `yaml:"git"`
	// Dir is the tree answering it, relative to the corpus file.
	Dir string `yaml:"dir"`
	// SHA is the commit the fetch reports. Empty reports the requested ref.
	SHA string `yaml:"sha"`
}

// Repo is one checkout to compare.
type Repo struct {
	// Dir is the repository, relative to the corpus root.
	Dir string `yaml:"dir"`
	// Vendored is the tree the export must reproduce, relative to Dir. It is
	// how a corpus of real checkouts measures export: the committed tree is
	// the other tool's output, produced over months by the builds that
	// actually run. A corpus that has none — the synthetic one shipped here
	// cannot have one, because nothing produced it — leaves this empty and
	// gives each invocation a `reference` instead, so the other tool is run
	// and compared against there and then.
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
	// ExcludeImports mirrors --exclude-imports. It applies to both tools:
	// the import closure is the decision the flag exists to make, so an
	// invocation that set it for one tool only would be comparing two
	// different questions.
	ExcludeImports bool `yaml:"exclude_imports"`
	// Reference is the same invocation, expressed for the other tool. It is
	// required when the checkout carries no vendored tree, and is what lets a
	// corpus nobody has a vendored tree for still measure export: the other
	// tool is run over the same physical files and its output is the
	// expectation.
	//
	// It is stated rather than derived because the two tools do not take the
	// same coordinates. stele's Paths are relative to the module root that
	// supplies the file — the coordinates an import statement uses — and the
	// other tool's are relative to the workspace. Deriving one from the other
	// would put the translation under test into the harness that is supposed
	// to measure it.
	Reference *ReferenceExport `yaml:"reference"`
	// ExpectAbsent lists path prefixes that must appear in neither tool's
	// output. It pins a decision rather than an agreement: the well-known
	// types are carried by every compiler and supplied by no repository, so
	// both tools leave them out — and a test that only compared the two would
	// go on passing if both started emitting them.
	ExpectAbsent []string `yaml:"expect_absent"`
}

// ReferenceExport is one invocation of the other tool.
type ReferenceExport struct {
	// Input is what the other tool is pointed at, relative to the checkout.
	Input string `yaml:"input"`
	// Paths mirrors its --path, in its own coordinates.
	Paths []string `yaml:"paths"`
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
		// The corpus that ships with the tool. It is used unless the
		// environment names another, so that a plain run measures something.
		path = shippedCorpus
		if _, err := os.Stat(path); err != nil {
			t.Skipf("no corpus: %s is not set and %s is not present", EnvCorpus, shippedCorpus)
		}
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
		if r.Dir == "" {
			t.Fatalf("%s: repos[%d]: dir is required", path, i)
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
			for j, inv := range r.Export.Invocations {
				if r.Vendored == "" && (inv.Reference == nil || inv.Reference.Input == "") {
					t.Fatalf("%s: repos[%d]: export invocation %d has no reference input, and the checkout has no vendored tree to compare against; one of the two has to say what the expected bytes are",
						path, i, j)
				}
			}
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	c.dir = filepath.Dir(abs)
	// A corpus that ships with the repository is checked out wherever the
	// repository is, so its paths are relative to itself. An absolute one is
	// left alone: that is how the external corpus names a root that is
	// nowhere near the corpus file.
	c.Root = c.rel(c.Root)
	for i := range c.Producers {
		if c.Producers[i].Git == "" || c.Producers[i].Dir == "" {
			t.Fatalf("%s: producers[%d]: git and dir are both required", path, i)
		}
		c.Producers[i].Dir = filepath.Join(c.Root, filepath.FromSlash(c.Producers[i].Dir))
		if info, err := os.Stat(c.Producers[i].Dir); err != nil || !info.IsDir() {
			t.Fatalf("%s: producers[%d]: %s is not a directory", path, i, c.Producers[i].Dir)
		}
	}
	return c
}

// rel interprets a corpus path relative to the corpus file, leaving an
// absolute one alone.
func (c Corpus) rel(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.dir, filepath.FromSlash(p))
}

// fetcher answers dependency addresses from the corpus's producers, and
// refuses anything else by name.
//
// Refusing rather than falling through to the network is the point: a corpus
// is a measurement, and a run that quietly cloned would be measuring a tree
// nobody committed.
func (c Corpus) fetcher() resolve.FetchFunc {
	return func(_ context.Context, git, ref string) (string, string, error) {
		for _, p := range c.Producers {
			if p.Git == git {
				sha := p.SHA
				if sha == "" {
					sha = ref
				}
				return p.Dir, sha, nil
			}
		}
		return "", "", fmt.Errorf("the parity corpus answers no address %q; it declares %s",
			git, strings.Join(c.producerAddrs(), ", "))
	}
}

func (c Corpus) producerAddrs() []string {
	if len(c.Producers) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(c.Producers))
	for _, p := range c.Producers {
		out = append(out, p.Git)
	}
	return out
}

// referenceBin is the reference tool, checked against the version the corpus
// pins before it is used.
//
// The check is here rather than in a CI step because the claim belongs to the
// corpus: someone running the harness on a workstation is making the same
// claim as CI and deserves the same refusal when the binary is not the one the
// corpus was measured against.
func referenceBin(t *testing.T, c Corpus) string {
	t.Helper()
	bin := c.Buf
	switch {
	case bin != "":
		// The corpus names a binary. It is taken as given and still
		// version-checked: naming one is not a claim about which build it is.
	case len(c.BufDownloads) > 0:
		bin = downloadReference(t, c)
	default:
		bin = "buf"
	}
	if c.BufVersion == "" {
		return bin
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("asking %s for its version: %v", bin, err)
	}
	got := strings.TrimSpace(string(out))
	if got != c.BufVersion {
		t.Fatalf("the corpus pins the reference tool at %s, but %s reports %s;\n"+
			"parity against a different build measures the difference between two tools, not a regression in this one",
			c.BufVersion, bin, got)
	}
	return bin
}

// downloadReference fetches the reference tool the corpus pins, verifies it
// against the digest declared for this machine's platform, and returns the
// path to it.
//
// It reuses the plugin cache and the plugin download tier wholesale, because
// the question is the same one and was already answered once: which bytes ran.
// A digest names bytes, bytes are per-platform, an entry is chosen by GOOS and
// GOARCH, the digest is checked before anything is made executable, and an
// unmatched platform is refused rather than falling back to whatever is on
// PATH. Giving the reference tool its own second answer to that question would
// be a second set of mistakes to make.
func downloadReference(t *testing.T, c Corpus) string {
	t.Helper()
	root, err := cachedir.Root(c.Cache)
	if err != nil {
		t.Fatal(err)
	}
	d, err := plugin.Select("the reference tool", config.PluginDownloads(c.BufDownloads), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := plugin.Cache{Root: root}.EnsureURL(context.Background(), d.URL, d.SHA256, d.ArchivePath)
	if err != nil {
		t.Fatalf("fetching the reference tool the corpus pins (%s, %s): %v", c.BufVersion, d.URL, err)
	}
	return bin
}

// referencePATH is the PATH the reference tool is run with: the directories
// holding the plugin binaries this manifest pins, ahead of everything else.
//
// Both tools must run the same plugin binaries or the comparison is between
// two generators rather than between two drivers of one. stele installs what
// the manifest pins into its own cache; the reference tool looks the plugin up
// on PATH and has no way to pin it. So the pinned binaries are put on its PATH,
// which makes the manifest the single place a plugin version is declared.
func referencePATH(t *testing.T, c Corpus, cfg *config.File) []string {
	t.Helper()
	root, err := cachedir.Root(c.Cache)
	if err != nil {
		t.Fatal(err)
	}
	cache := plugin.Cache{Root: root}
	var dirs []string
	seen := map[string]bool{}
	for _, tgt := range cfg.Generate {
		for _, p := range tgt.Plugins {
			if p.Module == "" {
				continue
			}
			bin, err := cache.Ensure(context.Background(), p.Module, p.Version)
			if err != nil {
				t.Fatalf("installing %s@%s, which the manifest pins: %v", p.Module, p.Version, err)
			}
			d := filepath.Dir(bin)
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	if len(dirs) == 0 {
		// Nothing pinned means the reference tool would take whatever the
		// machine has, and so would this one; the comparison would then be
		// between two unknown binaries.
		t.Fatal("no plugin in this manifest is pinned by module and version; both tools would run whatever is on PATH")
	}
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + strings.Join(dirs, string(os.PathListSeparator)) +
				string(os.PathListSeparator) + strings.TrimPrefix(e, "PATH=")
			return env
		}
	}
	return append(env, "PATH="+strings.Join(dirs, string(os.PathListSeparator)))
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
	for _, inv := range e.Invocations {
		runOneExport(t, c, stage, repo, inv, out)
	}
}

// runOneExport performs one invocation of this tool against an already staged
// manifest.
//
// It is separate because the two corpora ask different questions of the same
// run. A checkout with a committed vendored tree can only be compared with the
// union of every invocation, since that union is what produced the tree. A
// corpus without one compares each invocation against the other tool
// separately — which is the more diagnosable of the two, because a failure
// names the invocation that caused it rather than the tree they all wrote to.
func runOneExport(t *testing.T, c Corpus, stage, repo string, inv Invocation, out string) {
	t.Helper()
	cache, err := cachedir.Root(c.Cache)
	if err != nil {
		t.Fatal(err)
	}
	// A corpus that declares producers answers every address from a committed
	// tree and must never reach the network; one that declares none — the
	// fleet's, whose dependencies are real repositories — keeps the fetching
	// behaviour export has always had here.
	var fetch resolve.FetchFunc
	if len(c.Producers) > 0 {
		fetch = c.fetcher()
	}
	err = export.Run(context.Background(), export.Options{
		Dir:            stage,
		Output:         out,
		Dep:            inv.Dep,
		Paths:          inv.Paths,
		ExcludeImports: inv.ExcludeImports,
		CacheRoot:      cache,
		Fetch:          fetch,
		// The corpus is somebody's checkout, and the manifest is the
		// corpus's. Writing a lock would make running the acceptance test
		// modify the thing it measures.
		NoLock: true,
	})
	if err != nil {
		t.Fatalf("exporting dep %q of %s: %v", inv.Dep, repo, err)
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
