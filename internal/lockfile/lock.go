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

	"github.com/thegorangers/stele/internal/atomicfile"
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
	// Plugins are the code generation plugins this run observed rather than
	// resolved from a pin, in a stable order. A lock records what the
	// manifest does not determine: a plugin declared as module@version, or as
	// a per-platform url and sha256, is already named exactly by the
	// manifest, and a copy of that here could only drift from the original.
	// What is left is the unpinned tiers — an explicit path, and a bare name
	// found on PATH — where the manifest pins nothing and what ran is
	// whatever the machine had. Comparing that observation on the next run is
	// the only way anyone learns the binary moved under them.
	Plugins []Plugin `yaml:"plugins,omitempty"`
}

// Plugin is one observed code generation plugin.
//
// Origin says which of the unpinned tiers the binary came from: file when the
// manifest pointed at a path, path when a bare name was found on PATH. Version
// is what the binary said about itself — read from its own build metadata when
// there is any, and "unknown" when there is not. It is an observation, not a
// pin, and the origin beside it is what says so.
type Plugin struct {
	// Name is the manifest's own spelling of the plugin.
	Name string `yaml:"name"`
	// Origin is one of the origins below. It is omitted for a lock written
	// before origins were recorded, which is read as "not stated".
	Origin string `yaml:"origin,omitempty"`
	// Version is the version observed in the binary the machine chose, or
	// "unknown".
	Version string `yaml:"version"`
	// Path is the manifest's own spelling of an explicit path.
	Path string `yaml:"path,omitempty"`

	// The fields below identify a plugin on the tiers the manifest pins. They
	// belong to locks written before those tiers stopped being recorded: they
	// are parsed so that a lock already on disk keeps working, are read only
	// to recognise such a record, and are left out the next time the lock is
	// written. Do not use them for anything else.
	DeprecatedModule      string `yaml:"module,omitempty"`
	DeprecatedOS          string `yaml:"os,omitempty"`
	DeprecatedArch        string `yaml:"arch,omitempty"`
	DeprecatedURL         string `yaml:"url,omitempty"`
	DeprecatedSHA256      string `yaml:"sha256,omitempty"`
	DeprecatedArchivePath string `yaml:"archive_path,omitempty"`
}

// MarshalYAML emits the observation, dropping whatever a previous format
// recorded beside it.
func (p Plugin) MarshalYAML() (any, error) {
	return struct {
		Name    string `yaml:"name"`
		Origin  string `yaml:"origin,omitempty"`
		Version string `yaml:"version"`
		Path    string `yaml:"path,omitempty"`
	}{p.Name, p.Origin, p.Version, p.Path}, nil
}

// observation reports whether this record is one the lock keeps. A plugin the
// manifest pins is the manifest's business; only the unpinned tiers are an
// observation this file has anything to add about. A record written before
// origins were stated has to be recognised by its fields instead.
func (p Plugin) observation() bool {
	switch p.Origin {
	case OriginFile, OriginPath:
		return true
	case OriginManaged, OriginURL:
		return false
	}
	return p.DeprecatedModule == "" && p.DeprecatedSHA256 == ""
}

// Origins a plugin can have. They are the manifest's four tiers, and the
// spellings are the resolver's own, so that a lock, a report and an error
// message never call the same thing by two names. Only the two unpinned ones
// are written here; the other two are still accepted, because locks that name
// them exist on disk.
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
	// Name is what the manifest that requested this dependency calls it. It
	// describes the entry; it does not identify it. Names are chosen per
	// manifest, so one repository reached through two manifests may carry two
	// names, and one name may cover two repositories. See validate.
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

// refuseNullSequenceItems refuses a lock that contains an empty (null) entry
// inside a YAML block sequence — a stray "-" line, or an explicit
// "~"/"null" — anywhere in the document.
//
// See the identical helper and its comment in internal/config/parse.go,
// where FuzzParseManifest found the defect this closes: gopkg.in/yaml.v3
// silently drops such an item when decoding a sequence node into a slice of
// a non-nilable element type ([]Entry, []Plugin here), so a corrupted lock
// line can quietly resolve to fewer pinned dependencies than the file
// actually names, with no error. A lock is meant to reproduce a build byte
// for byte; losing an entry without saying so is the one failure mode that
// contract cannot survive.
func refuseNullSequenceItems(raw []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil
	}
	if len(root.Content) == 0 {
		return nil
	}
	return walkForNullSequenceItems(root.Content[0])
}

func walkForNullSequenceItems(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.SequenceNode {
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode && item.Tag == "!!null" {
				return fmt.Errorf("line %d: empty list entry; yaml.v3 would silently drop this item rather "+
					"than decode it, shortening the list without an error — write a value here, or remove the line",
					item.Line)
			}
		}
	}
	for _, c := range n.Content {
		if err := walkForNullSequenceItems(c); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lock) validate() error {
	switch {
	case l.Version == 0:
		return fmt.Errorf("version: missing; expected %d", Version)
	case l.Version != Version:
		return fmt.Errorf("version: %d is not supported; expected %d", l.Version, Version)
	}
	// What identifies an entry is (git, ref), not the name.
	//
	// Two addresses that resolve to one repository are deliberately NOT
	// merged, and the tool does not normalise ssh:// against https:// here.
	// It cannot prove they are the same repository — a fork at the same path
	// on another host is not — and, more concretely, the transport is an
	// environment's choice rather than part of what a dependency is: a
	// workstation clones over ssh where CI clones over https with a token.
	// Rewriting one manifest's address into another's would hand CI an
	// address its image cannot use. So the lock records each request as the
	// manifest that made it wrote it.
	//
	// The consequence is that one name can appear twice, and that is correct
	// rather than tolerated: a name is chosen by the manifest requesting the
	// dependency, and a flat transitive closure holds the requests of
	// manifests belonging to other people. Requiring names to be unique made
	// a lock's validity depend on strangers' naming, with no repair available
	// to anybody in the closure except editing a generated file by hand —
	// which is exactly the corruption reported in issue #1. What must not
	// repeat is the key, because the pinned run looks entries up by it and
	// two answers to one lookup is an ambiguous pin.
	//
	// The manifest is the other case and keeps its rule: there a name IS a
	// key, because a target's inputs name a dependency by it, and one
	// manifest is written by one set of people who can settle a collision.
	seen := make(map[string]int, len(l.Deps))
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
		if j, ok := seen[request(e.Git, e.Ref)]; ok {
			prev := l.Deps[j]
			return fmt.Errorf("deps[%d]: %s at ref %q is pinned twice, by deps[%d] (name %q, sha %s) and deps[%d] (name %q, sha %s); "+
				"a lock cannot answer one request with two commits. Delete the entry that does not belong and re-resolve with --update, "+
				"which rewrites the whole file from one resolution",
				i, e.Git, e.Ref, j, prev.Name, prev.SHA, i, e.Name, e.SHA)
		}
		seen[request(e.Git, e.Ref)] = i
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
		}
		if seenPlugins[p.Name] {
			return fmt.Errorf("plugins[%d].name: duplicate plugin name %q", i, p.Name)
		}
		seenPlugins[p.Name] = true
	}
	return nil
}

// request is the key an entry is identified by: the address and the ref, as
// the manifest making the request wrote them. It is the same key the pinned
// resolution looks entries up by.
func request(git, ref string) string { return git + "@" + ref }

// Save writes lock to path.
//
// Entries are sorted by name and then by (git, ref), so that re-running a
// resolution that changed nothing produces an identical file and an empty
// diff. The lock is validated before it is written: this tool does not emit a
// file it would refuse to read.
func Save(path string, lock *Lock) error {
	out := Lock{
		Version: lock.Version,
		Deps:    append([]Entry(nil), lock.Deps...),
	}
	for _, p := range lock.Plugins {
		if p.observation() {
			out.Plugins = append(out.Plugins, p)
		}
	}
	if out.Version == 0 {
		out.Version = Version
	}
	// By name, then by the key, because a name may legitimately repeat and
	// sorting on it alone would leave the order of those entries to the sort
	// — which is not stable — and so leave the file's bytes to chance.
	sort.Slice(out.Deps, func(i, j int) bool {
		a, b := out.Deps[i], out.Deps[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return request(a.Git, a.Ref) < request(b.Git, b.Ref)
	})
	sort.Slice(out.Plugins, func(i, j int) bool { return out.Plugins[i].Name < out.Plugins[j].Name })

	// A writer must never produce a file this package would refuse to read.
	// That was the whole of issue #1: --update wrote a lock, and the next run
	// could not load it. Validating here holds the invariant for every writer
	// there will ever be, instead of for the ones somebody remembered.
	if err := out.validate(); err != nil {
		return fmt.Errorf("%s: refusing to write a lock this tool could not read: %w", path, err)
	}

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
	return atomicfile.Write(path, buf.Bytes())
}
