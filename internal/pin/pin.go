// Package pin resolves a manifest through its lock file.
//
// It exists as one entry point rather than as a step each command performs for
// itself. A lock that a command can forget to consult is decorative: the whole
// value of the file is that a build without --update consumes exactly what an
// earlier build consumed, and that guarantee is only as good as the least
// careful caller. Every command that resolves a closure goes through Resolve,
// so a command added later inherits the enforcement instead of reimplementing
// it.
package pin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
)

// Options configures one resolution.
type Options struct {
	// Dir is the module root: the directory the manifest's own module paths
	// are relative to. Empty means ".".
	Dir string
	// Manifest is the already-parsed root manifest.
	Manifest *config.File
	// LockPath is the lock this resolution is pinned by and, when it moves,
	// written to.
	LockPath string
	// Fetch materialises dependency repositories.
	Fetch resolve.FetchFunc
	// Update re-resolves every ref to the commit it names today, re-snapshots
	// the trees and rewrites the lock. It is the only way a pin moves.
	Update bool
	// NoLock leaves the lock out of the run entirely: it is neither read nor
	// written. It exists for callers that must not depend on, or touch, a
	// working tree they do not own, such as the acceptance harness.
	NoLock bool
}

// Resolve resolves the closure of opts.Manifest.
//
// With a lock present and Update unset, every dependency is materialised at the
// commit the lock recorded — not at the commit its ref names today — and the
// tree that comes back is verified against the recorded hashes before anything
// reads it. With no lock, or with Update set, refs are resolved afresh and the
// lock is written from the closure that was just resolved, so that the pins a
// build consumed and the pins recorded can never describe different runs.
func Resolve(ctx context.Context, opts Options) (*resolve.Graph, error) {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	if opts.Fetch == nil {
		return nil, errors.New("pin: no fetcher")
	}

	if opts.NoLock {
		return resolve.ResolveIn(ctx, dir, opts.Manifest, opts.Fetch)
	}

	lock, err := load(opts.LockPath)
	if err != nil {
		return nil, err
	}
	if lock == nil || opts.Update {
		graph, err := resolve.ResolveIn(ctx, dir, opts.Manifest, opts.Fetch)
		if err != nil {
			return nil, err
		}
		return graph, write(graph, opts.LockPath)
	}

	p := &pinned{lock: lock, inner: opts.Fetch, index: index(lock), used: map[string]bool{}}
	graph, err := resolve.ResolveIn(ctx, dir, opts.Manifest, p.fetch)
	if err != nil {
		return nil, err
	}
	// The other direction of the same drift: the manifest no longer asks for
	// something the lock still pins. Left unreported it would rot quietly,
	// and the file people read in a merge request would describe a build that
	// no longer happens.
	if unused := p.unused(); len(unused) > 0 {
		return nil, fmt.Errorf("%s records %s, which no manifest in the closure asks for any more; "+
			"re-resolve with --update", opts.LockPath, list(unused))
	}
	return graph, nil
}

// load reads the lock, reporting its absence as a nil lock rather than as an
// error: a first run has nothing to be pinned by.
func load(path string) (*lockfile.Lock, error) {
	if path == "" {
		return nil, errors.New("pin: no lock path")
	}
	l, err := lockfile.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return l, nil
}

// write records the closure that was just resolved.
func write(g *resolve.Graph, path string) error {
	lock := &lockfile.Lock{Version: lockfile.Version}
	for _, o := range g.Deps() {
		entry, err := lockfile.Snapshot(o.Name, o.Dir)
		if err != nil {
			return err
		}
		entry.Git, entry.Ref, entry.SHA = o.Git, o.Ref, o.SHA
		lock.Deps = append(lock.Deps, entry)
	}
	return lockfile.Save(path, lock)
}

// pinned is a fetcher that answers from the lock.
type pinned struct {
	lock  *lockfile.Lock
	inner resolve.FetchFunc
	// index maps a request as a manifest makes it — a repository and a ref —
	// to the entry that pins it. The ref is part of the key because a lock may
	// legitimately hold one repository twice, pinned from two refs, and a
	// key of the repository alone could not tell those apart. The name is not
	// the key: it is chosen by the manifest that made the request, and two
	// manifests may well call one repository by different names.
	index map[string]lockfile.Entry
	used  map[string]bool
}

func key(git, ref string) string { return git + "@" + ref }

func index(l *lockfile.Lock) map[string]lockfile.Entry {
	m := make(map[string]lockfile.Entry, len(l.Deps))
	for _, e := range l.Deps {
		m[key(e.Git, e.Ref)] = e
	}
	return m
}

func (p *pinned) fetch(ctx context.Context, git, ref string) (string, string, error) {
	k := key(git, ref)
	entry, ok := p.index[k]
	if !ok {
		// The caller names the dependency; what is added here is what the
		// manifest asked for and what to do about it.
		return "", "", fmt.Errorf("%s at ref %q is not recorded in the lock; re-resolve with --update", git, ref)
	}
	p.used[k] = true

	// The recorded commit, not the ref: this is the whole point of the lock.
	dir, sha, err := p.inner(ctx, git, entry.SHA)
	if err != nil {
		if errors.Is(err, source.ErrUnreachableSHA) {
			return "", "", fmt.Errorf("the locked commit %s, resolved from ref %q, is no longer on %s. "+
				"A squashed merge rewrites commits, which makes a pin vanish; re-resolve with --update: %w",
				entry.SHA, entry.Ref, git, err)
		}
		return "", "", err
	}
	if err := lockfile.Verify(entry, dir); err != nil {
		return "", "", err
	}
	return dir, sha, nil
}

// unused reports the dependencies the lock pins that nothing asked for, sorted.
func (p *pinned) unused() []string {
	var out []string
	for _, e := range p.lock.Deps {
		if !p.used[key(e.Git, e.Ref)] {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

// list renders names for an error message.
func list(names []string) string {
	out := ""
	for i, n := range names {
		switch {
		case i == 0:
		case i == len(names)-1:
			out += " and "
		default:
			out += ", "
		}
		out += fmt.Sprintf("%q", n)
	}
	return out
}
