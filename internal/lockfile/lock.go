// Package lockfile reads and writes stele.lock, the pinned record of every
// dependency a build consumed.
//
// The lock exists so that a run without --update reproduces an earlier run
// byte for byte. What it takes to do that is one line of address per
// dependency: the commit it resolved to. A git commit SHA is already a
// cryptographic hash of the content — the commit covers the tree, the tree
// covers the blobs — so a pinned SHA yields byte-identical files from any
// remote, or git refuses to hand them over. Re-hashing those files here would
// re-compute what git computed and guarantee nothing further.
//
// This is deliberately not the go.sum situation. There a proxy serves a zip
// keyed by a version NAME, and a name can be moved onto other bytes, so the
// hash is the only thing tying the name to the content. Here the pin IS the
// content address.
package lockfile

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Version is the only lock format version this tool understands. It is a
// separate number from the manifest version: the two evolve independently, and
// a lock is a generated file while a manifest is written by hand.
//
// Dropping the per-file hashes did not move it. A lock is not a contract
// between two tools but a record this tool writes and reads, and both
// directions still work across the change: a new tool reads an old lock and
// ignores the blocks it no longer needs, and an old tool reading a new lock
// finds no module roots and says so, naming --update. A version bump would
// have bought a worse error than the one already there, at the cost of
// breaking every lock on disk.
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
	// Unlike a dependency, a plugin version is derivable from nothing else,
	// which is why this section stays while the file hashes go.
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

// Entry is one pinned dependency: where it came from, and which commit.
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
	// SHA is the full commit the dependency resolved to. It is the pin: it
	// names the content, not a label somebody can move onto other content.
	SHA string `yaml:"sha"`

	// The fields below belong to locks written before the per-file hashes
	// were dropped. They are parsed so that a lock already on disk keeps
	// working, are read by nothing, and are left out the next time the lock
	// is written. Do not use them for anything.
	DeprecatedModules  []string          `yaml:"modules,omitempty"`
	DeprecatedManifest string            `yaml:"manifest,omitempty"`
	DeprecatedFiles    map[string]string `yaml:"files,omitempty"`
}

// MarshalYAML emits an entry as the address it now is, dropping whatever a
// previous format recorded beside it.
func (e Entry) MarshalYAML() (any, error) {
	return struct {
		Name string `yaml:"name"`
		Git  string `yaml:"git"`
		Ref  string `yaml:"ref"`
		SHA  string `yaml:"sha"`
	}{e.Name, e.Git, e.Ref, e.SHA}, nil
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
// Entries are sorted by name, so that re-running a resolution that changed
// nothing produces an identical file and an empty diff.
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
