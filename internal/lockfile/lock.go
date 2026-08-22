// Package lockfile reads and writes stele.lock, the pinned record of every
// dependency a build consumed.
//
// The lock exists so that a run without --update reproduces an earlier run
// byte for byte: it stores the commit each dependency resolved to and the
// hash of every file taken from it, and a mismatch stops the build.
package lockfile

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/hashing"
	"gopkg.in/yaml.v3"
)

// Version is the only lock format version this tool understands. It is a
// separate number from the manifest version: the two evolve independently, and
// a lock is a generated file while a manifest is written by hand.
const Version = 1

// Lock is a parsed stele.lock.
//
// Deps holds the full transitive closure, one entry per resolved dependency,
// flat and not nested. A lock that recorded only direct dependencies would not
// reproduce anything: resolution is transitive (a producer's manifest brings in
// its own dependencies), so the files that actually reach the compiler come
// mostly from repositories the root manifest never names. Whether an entry is
// direct is deliberately not stored — it is already stated by the manifest, and
// a second copy of that fact could only drift out of agreement with it.
type Lock struct {
	// Version of the lock format.
	Version int `yaml:"version"`
	// Deps are the pinned dependencies, in a stable order.
	Deps []Entry `yaml:"deps"`
	// Plugins are the code generation plugins the run went through, in a
	// stable order. They are here for the same reason the dependencies are:
	// the version of a plugin decides the bytes it emits, so a lock that
	// recorded only the inputs would pin half of what produced the output.
	Plugins []Plugin `yaml:"plugins,omitempty"`
}

// Plugin is one recorded code generation plugin.
//
// Module is empty for a plugin taken from PATH, and that emptiness is the
// record's most useful field: it says the tool did not choose this binary and
// cannot promise the next machine will run the same one. Version is then
// whatever the binary reported about itself, which for a plugin that is not a
// Go program is "unknown" — an observation, not a pin.
type Plugin struct {
	// Name is the manifest's own spelling of the plugin.
	Name string `yaml:"name"`
	// Module is the Go module the plugin was installed from, when the tool
	// installed it.
	Module string `yaml:"module,omitempty"`
	// Version is the installed version, or the version observed on PATH.
	Version string `yaml:"version"`
}

// Entry is one pinned dependency.
type Entry struct {
	// Name identifies the dependency, as in the manifest that requested it.
	Name string `yaml:"name"`
	// Git is the address of the producing repository.
	Git string `yaml:"git"`
	// Ref is the branch, tag or SHA the pin was resolved from. It is kept
	// beside the SHA because a squashed merge can make a pinned commit
	// unreachable, and the report has to be able to name the branch the pin
	// came from instead of passing on a raw git failure.
	Ref string `yaml:"ref"`
	// SHA is the full commit the dependency resolved to.
	SHA string `yaml:"sha"`
	// Modules are the producer's module roots this entry covers, relative to
	// the root of the fetched tree and slash-separated. They are recorded
	// rather than recomputed so that a root the producer added or removed is
	// a difference somebody can be told about, instead of a silent change in
	// how much of the tree is pinned.
	Modules []string `yaml:"modules"`
	// Manifest is the producer's own manifest, relative to the root of the
	// fetched tree, or empty when the producer carries none.
	Manifest string `yaml:"manifest,omitempty"`
	// Files maps a path, relative to the root of the fetched tree and always
	// slash-separated, to the hex sha256 of its contents.
	//
	// Only what can affect the build is here: every .proto under one of
	// Modules, and Manifest. A README cannot satisfy an import, so hashing it
	// would buy nothing and would make every producer's prose edit conflict
	// in every consumer's merge request.
	Files map[string]string `yaml:"files"`
}

// Scope says which part of a fetched tree can affect the build: the module
// roots the graph reads for this dependency, and the producer's own manifest.
//
// It is the answer to "what must the lock record". Two kinds of file qualify.
// A .proto under a module root can satisfy an import — all of them, including
// the ones no import currently names, because a file the lock does not list is
// the one disagreement a build consumes in silence. The producer's manifest
// qualifies because it decides the module roots and the transitive
// dependencies, so a change there changes the build even when no proto moved.
type Scope struct {
	// Modules are module roots relative to the tree, slash-separated. "."
	// means the whole repository.
	Modules []string
	// Manifest is the producer's manifest relative to the tree, empty when it
	// has none.
	Manifest string
}

// MarshalYAML emits the entry with its files in sorted order. The lock is read
// by people in merge requests, so its bytes must depend only on its content,
// never on map iteration order.
func (e Entry) MarshalYAML() (any, error) {
	return struct {
		Name     string     `yaml:"name"`
		Git      string     `yaml:"git"`
		Ref      string     `yaml:"ref"`
		SHA      string     `yaml:"sha"`
		Modules  []string   `yaml:"modules"`
		Manifest string     `yaml:"manifest,omitempty"`
		Files    fileHashes `yaml:"files"`
	}{e.Name, e.Git, e.Ref, e.SHA, e.Modules, e.Manifest, fileHashes(e.Files)}, nil
}

// fileHashes is a path-to-hash mapping that serialises in sorted key order.
type fileHashes map[string]string

func (f fileHashes) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, p := range sortedKeys(f) {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: p},
			&yaml.Node{Kind: yaml.ScalarNode, Value: f[p]},
		)
	}
	return node, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Load reads and validates the lock at path.
//
// Parsing is strict, as it is for the manifest: a key outside the format is an
// error naming that key. No type here carries a custom UnmarshalYAML, which is
// what keeps KnownFields(true) in force all the way down — yaml.v3 does not
// propagate it into a nested node.Decode.
func Load(path string) (*Lock, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // an unknown key is an error naming that key
	var l Lock
	if err := dec.Decode(&l); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &l, nil
}

func (l *Lock) validate() error {
	switch {
	case l.Version == 0:
		return fmt.Errorf("version: missing; expected %d", Version)
	case l.Version != Version:
		return fmt.Errorf("version: %d is not supported; expected %d", l.Version, Version)
	}
	seen := make(map[string]bool, len(l.Deps))
	for i, e := range l.Deps {
		switch {
		case e.Name == "":
			return fmt.Errorf("deps[%d].name: missing", i)
		case e.Git == "":
			return fmt.Errorf("deps[%d].git: missing for dependency %q", i, e.Name)
		case e.SHA == "":
			return fmt.Errorf("deps[%d].sha: missing for dependency %q", i, e.Name)
		case e.Ref == "":
			// The ref is what makes an unreachable SHA explainable; an entry
			// without one degrades that report to a raw git error.
			return fmt.Errorf("deps[%d].ref: missing for dependency %q", i, e.Name)
		}
		if seen[e.Name] {
			return fmt.Errorf("deps[%d].name: duplicate dependency name %q", i, e.Name)
		}
		seen[e.Name] = true
	}
	seenPlugins := make(map[string]bool, len(l.Plugins))
	for i, p := range l.Plugins {
		switch {
		case p.Name == "":
			return fmt.Errorf("plugins[%d].name: missing", i)
		case p.Version == "":
			return fmt.Errorf("plugins[%d].version: missing for plugin %q", i, p.Name)
		}
		if seenPlugins[p.Name] {
			return fmt.Errorf("plugins[%d].name: duplicate plugin name %q", i, p.Name)
		}
		seenPlugins[p.Name] = true
	}
	return nil
}

// Save writes lock to path.
//
// Entries are sorted by name and file hashes by path, so that re-running a
// resolution that changed nothing produces an identical file and an empty diff.
func Save(path string, lock *Lock) error {
	out := Lock{
		Version: lock.Version,
		Deps:    append([]Entry(nil), lock.Deps...),
		Plugins: append([]Plugin(nil), lock.Plugins...),
	}
	if out.Version == 0 {
		out.Version = Version
	}
	sort.Slice(out.Deps, func(i, j int) bool { return out.Deps[i].Name < out.Deps[j].Name })
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].Name < out.Plugins[j].Name })

	var buf bytes.Buffer
	buf.WriteString("# Generated by stele. Edited by the tool, reviewed by people.\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// Snapshot hashes the part of the tree at dir that scope admits and returns
// the entry describing it. Git, Ref and SHA are left to the caller, which is
// what resolved them.
func Snapshot(name, dir string, scope Scope) (Entry, error) {
	modules, err := normaliseModules(scope.Modules)
	if err != nil {
		return Entry{}, fmt.Errorf("snapshotting %q: %w", name, err)
	}
	files, err := hashScope(dir, Scope{Modules: modules, Manifest: scope.Manifest})
	if err != nil {
		return Entry{}, fmt.Errorf("snapshotting %q: %w", name, err)
	}
	return Entry{Name: name, Modules: modules, Manifest: scope.Manifest, Files: files}, nil
}

// Verify checks the tree at dir against entry, given the scope this run
// derived from the producer itself.
//
// The scope is checked before the files are. A root the producer added or
// removed changes what the lock covers, and a lock that quietly covered less
// than it did yesterday would be worse than no lock: the difference has to
// reach a person. The manifest is part of that check for the same reason — a
// producer that grows a stele.yaml beside the buf.yaml the compatibility
// fallback used to read changes which roots are read, without touching a
// single recorded byte.
//
// Then all three kinds of disagreement are errors: changed content, a file the
// lock lists that is absent, and a file present that the lock does not list.
// The last one is why this walks the tree instead of only re-hashing the
// recorded paths: an unlisted .proto is the only disagreement a build can
// consume in silence, because it satisfies an import without contradicting any
// recorded hash.
func Verify(entry Entry, dir string, scope Scope) error {
	if len(entry.Modules) == 0 {
		return fmt.Errorf("%s: the lock records no module roots for this dependency; "+
			"it was written before the lock was narrowed to what affects the build. Re-resolve with --update",
			entry.Name)
	}
	modules, err := normaliseModules(scope.Modules)
	if err != nil {
		return fmt.Errorf("verifying %q: %w", entry.Name, err)
	}
	if !equalStrings(entry.Modules, modules) {
		return fmt.Errorf("%s: the producer now declares module root(s) %s; the lock records %s. "+
			"That changes which files are pinned; re-resolve with --update",
			entry.Name, quoted(modules), quoted(entry.Modules))
	}
	if entry.Manifest != scope.Manifest {
		return fmt.Errorf("%s: %s decides this producer's module roots now; the lock was taken from %s. "+
			"That changes which files are pinned; re-resolve with --update",
			entry.Name, describeManifest(scope.Manifest), describeManifest(entry.Manifest))
	}

	have, err := hashScope(dir, Scope{Modules: modules, Manifest: scope.Manifest})
	if err != nil {
		return fmt.Errorf("verifying %q: %w", entry.Name, err)
	}
	for _, p := range sortedKeys(entry.Files) {
		got, ok := have[p]
		if !ok {
			return fmt.Errorf("%s: %s is missing from %s", entry.Name, p, dir)
		}
		if got != entry.Files[p] {
			return fmt.Errorf("%s: %s does not match the lock (locked %s, found %s)",
				entry.Name, p, entry.Files[p], got)
		}
	}
	for _, p := range sortedKeys(have) {
		if _, ok := entry.Files[p]; !ok {
			return fmt.Errorf("%s: %s is present in %s but not listed in the lock", entry.Name, p, dir)
		}
	}
	return nil
}

func describeManifest(p string) string {
	if p == "" {
		return "no manifest"
	}
	return p
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func quoted(paths []string) string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, fmt.Sprintf("%q", p))
	}
	return strings.Join(out, ", ")
}

// normaliseModules cleans, deduplicates and sorts module roots, so that the
// same set declared in a different order is the same set.
func normaliseModules(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("no module roots: a dependency always has at least one")
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, m := range in {
		clean := path.Clean(filepath.ToSlash(m))
		if clean == "" {
			clean = "."
		}
		if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return nil, fmt.Errorf("module root %q leaves the tree", m)
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	sort.Strings(out)
	return out, nil
}

// hashScope hashes the producer's manifest and every .proto under the module
// roots, keyed by slash-separated path relative to dir.
//
// A symlink is recorded by the TEXT OF ITS TARGET and never followed. Following
// it would let a link out of the tree launder foreign content into a green
// check; refusing it outright — which this did first — makes real repositories
// unpinnable. Recording the target keeps both threats covered: the bytes behind
// the link are not part of what is pinned, an added or repointed link
// contradicts the record, and a link can never satisfy an import in the first
// place, because resolution reads only regular files. The prefix keeps a link's
// record distinct from any file's, so that replacing one with the other is a
// disagreement rather than a match.
//
// Every other irregular entry — a device, a socket, a fifo — is still refused
// where it could have been a proto: it has no content to pin and does not occur
// in a repository tree. File modes are deliberately not recorded; nothing in a
// fetched proto tree is executed, so a mode carries no meaning worth failing a
// build over.
func hashScope(dir string, scope Scope) (map[string]string, error) {
	files := make(map[string]string)
	if scope.Manifest != "" {
		sum, err := hashing.File(filepath.Join(dir, filepath.FromSlash(scope.Manifest)))
		if err != nil {
			return nil, err
		}
		files[scope.Manifest] = sum
	}
	for _, m := range scope.Modules {
		root := filepath.Join(dir, filepath.FromSlash(m))
		info, err := os.Stat(root)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("module root %q is not a directory", m)
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Repository internals are not contracts, and a working copy
				// handed to the tool still carries them.
				if d.Name() == ".git" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".proto") {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			slashed := filepath.ToSlash(rel)
			switch {
			case d.Type().IsRegular():
				sum, err := hashing.File(p)
				if err != nil {
					return err
				}
				files[slashed] = sum
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				files[slashed] = symlinkPrefix + hashing.Bytes([]byte(filepath.ToSlash(target)))
			default:
				return fmt.Errorf("%s is not a regular file (%s); a pinned tree may contain files and symbolic links only",
					slashed, kindOf(d.Type()))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// symlinkPrefix marks a recorded symlink target so that it cannot collide with
// the hash of a file's contents.
const symlinkPrefix = "symlink:"

func kindOf(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symbolic link"
	case m&fs.ModeDir != 0:
		return "directory"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return strings.TrimSpace(m.String())
	}
}
