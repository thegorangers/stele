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
	"slices"
	"sort"
	"strings"

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
// Origin says which tier of the manifest the binary came from, and the fields
// beside it are what identifies it on that tier: module and version when the
// tool installed it, url and sha256 when it downloaded and verified it, path
// when the manifest pointed at a file, and nothing but the name when it was
// found on PATH. Recording the tier is the point: it is the difference between
// a run another machine can reproduce and one it cannot, and a reader of a
// merge request should not have to infer it from which fields are empty.
//
// Version is "unknown" wherever no version can be had — which is every tier
// but the first. That is an observation, not a pin; the pin, where there is
// one, is the sha256.
type Plugin struct {
	// Name is the manifest's own spelling of the plugin.
	Name string `yaml:"name"`
	// Origin is one of the origins below. It is omitted for a lock written
	// before origins were recorded, which is read as "not stated".
	Origin string `yaml:"origin,omitempty"`
	// Module is the Go module the plugin was installed from, when the tool
	// installed it.
	Module string `yaml:"module,omitempty"`
	// Version is the installed version, or the version observed on PATH, or
	// "unknown".
	Version string `yaml:"version"`
	// URL is where a downloaded plugin came from.
	URL string `yaml:"url,omitempty"`
	// SHA256 is the digest that download was verified against. It is the pin.
	SHA256 string `yaml:"sha256,omitempty"`
	// ArchivePath is the member taken from a downloaded archive, if any.
	ArchivePath string `yaml:"archive_path,omitempty"`
	// Path is the manifest's own spelling of an explicit path.
	Path string `yaml:"path,omitempty"`
}

// Origins a recorded plugin can have. They are the manifest's four tiers, and
// the spellings are the resolver's own, so that a lock, a report and an error
// message never call the same thing by two names.
const (
	OriginManaged = "managed"
	OriginURL     = "url"
	OriginFile    = "file"
	OriginPath    = "path"
)

// origins is the accepted set, for validation and for the message that names
// it.
var origins = []string{OriginManaged, OriginURL, OriginFile, OriginPath}

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
		case p.Origin != "" && !slices.Contains(origins, p.Origin):
			return fmt.Errorf("plugins[%d].origin: %q is not an origin this tool records for plugin %q (known: %s)",
				i, p.Origin, p.Name, strings.Join(origins, ", "))
		case p.Origin == OriginURL && (p.URL == "" || p.SHA256 == ""):
			// A url record without both halves would claim a pin it does not
			// have, which is worse than recording no origin at all.
			return fmt.Errorf("plugins[%d]: plugin %q is recorded as %s but does not carry both a url and a sha256",
				i, p.Name, OriginURL)
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
