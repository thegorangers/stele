// Package lockfile reads and writes stele.lock, the pinned record of every
// dependency a build consumed.
//
// The lock exists so that a run without --update reproduces an earlier run
// byte for byte: it stores the commit each dependency resolved to and the
// hash of every file taken from it, and a mismatch stops the build.
package lockfile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	// Files maps a path, relative to the root of the fetched tree and always
	// slash-separated, to the hex sha256 of its contents.
	Files map[string]string `yaml:"files"`
}

// MarshalYAML emits the entry with its files in sorted order. The lock is read
// by people in merge requests, so its bytes must depend only on its content,
// never on map iteration order.
func (e Entry) MarshalYAML() (any, error) {
	return struct {
		Name  string     `yaml:"name"`
		Git   string     `yaml:"git"`
		Ref   string     `yaml:"ref"`
		SHA   string     `yaml:"sha"`
		Files fileHashes `yaml:"files"`
	}{e.Name, e.Git, e.Ref, e.SHA, fileHashes(e.Files)}, nil
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
	return nil
}

// Save writes lock to path.
//
// Entries are sorted by name and file hashes by path, so that re-running a
// resolution that changed nothing produces an identical file and an empty diff.
func Save(path string, lock *Lock) error {
	out := Lock{Version: lock.Version, Deps: append([]Entry(nil), lock.Deps...)}
	if out.Version == 0 {
		out.Version = Version
	}
	sort.Slice(out.Deps, func(i, j int) bool { return out.Deps[i].Name < out.Deps[j].Name })

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

// Snapshot hashes every file of the tree at dir and returns the entry
// describing it. Git, Ref and SHA are left to the caller, which is what
// resolved them.
func Snapshot(name, dir string) (Entry, error) {
	files, err := hashTree(dir)
	if err != nil {
		return Entry{}, fmt.Errorf("snapshotting %q: %w", name, err)
	}
	return Entry{Name: name, Files: files}, nil
}

// Verify checks the tree at dir against entry.
//
// All three kinds of disagreement are errors: changed content, a file the lock
// lists that is absent, and a file present that the lock does not list. The
// last one is the reason this walks the tree instead of only re-hashing the
// recorded paths: an unlisted file is the only disagreement that a build can
// consume in silence, because it satisfies an import without contradicting any
// recorded hash.
func Verify(entry Entry, dir string) error {
	have, err := hashTree(dir)
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

// hashTree walks dir and returns the sha256 of every file in it, keyed by
// slash-separated relative path.
//
// Only regular files are accepted. A symlink is refused rather than followed or
// skipped: its target lies outside the hashed content, so the recorded hashes
// would keep matching while what is actually read changes, and a link may point
// out of the tree entirely. Every other irregular entry — a device, a socket, a
// fifo — is refused for the same reason: it has no content to pin. File modes
// are deliberately not recorded; nothing in a fetched proto tree is executed,
// so a mode carries no meaning worth failing a build over.
func hashTree(dir string) (map[string]string, error) {
	files := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file (%s); a pinned tree may contain files only",
				slashed, kindOf(d.Type()))
		}
		sum, err := hashFile(path)
		if err != nil {
			return err
		}
		files[slashed] = sum
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

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

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
