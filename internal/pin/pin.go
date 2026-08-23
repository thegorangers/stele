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
	// Plugins are the code generation plugins this run resolved, with the
	// versions it resolved them to. They are pinned by the same rules as the
	// dependencies: honoured without Update, rewritten with it.
	Plugins []lockfile.Plugin
	// PluginsAuthoritative says whether Plugins is the manifest's whole set.
	// A run restricted to some targets knows about some plugins only, and
	// must not delete the record of the ones it did not run; a full run
	// replaces the set, which is the only way a plugin that is no longer used
	// ever leaves the lock.
	PluginsAuthoritative bool
}

// Resolve resolves the closure of opts.Manifest.
//
// With a lock present and Update unset, every dependency is materialised at the
// commit the lock recorded, not at the commit its ref names today. That commit
// is the whole guarantee: git hands back the tree it names or nothing at all.
// With no lock, or with Update set, refs are resolved afresh and the
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
		return graph, write(graph, mergePlugins(lock, opts), opts.LockPath)
	}
	if err := verifyPlugins(lock, opts); err != nil {
		return nil, err
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

// mergePlugins decides the plugin set a rewritten lock carries.
//
// An authoritative run states the whole set. A partial one states only what it
// ran, and the rest of the previous record is carried over unchanged rather
// than dropped: a lock that lost entries every time somebody generated one
// target would record less the more it was used.
func mergePlugins(prev *lockfile.Lock, opts Options) []lockfile.Plugin {
	if opts.PluginsAuthoritative || prev == nil {
		return opts.Plugins
	}
	out := append([]lockfile.Plugin(nil), opts.Plugins...)
	named := make(map[string]bool, len(out))
	for _, p := range out {
		named[p.Name] = true
	}
	for _, p := range prev.Plugins {
		if !named[p.Name] {
			out = append(out, p)
		}
	}
	return out
}

// verifyPlugins checks the plugins this run resolved against the ones the lock
// records.
//
// A lock with no plugin section at all is honoured as it stands: it was
// written before plugins were recorded, and failing every such run would
// punish the repositories this feature is meant to help. A lock that does
// record plugins is enforced, including for a plugin taken from PATH — an
// unmanaged plugin whose version moved is precisely the drift that produced
// different bytes in CI and on a laptop with nothing to show for it.
func verifyPlugins(lock *lockfile.Lock, opts Options) error {
	if len(lock.Plugins) == 0 {
		return nil
	}
	recorded := make(map[string]lockfile.Plugin, len(lock.Plugins))
	for _, p := range lock.Plugins {
		recorded[p.Name] = p
	}
	for _, got := range opts.Plugins {
		want, ok := recorded[got.Name]
		if !ok {
			return fmt.Errorf("plugin %q is not recorded in %s; re-resolve with --update", got.Name, opts.LockPath)
		}
		// What is compared is what identifies the binary on its tier: the
		// version and module of an installed one, the digest of a downloaded
		// one, the declared path of one the manifest points at. The origin is
		// compared only when the lock states it, so that a lock written
		// before origins were recorded is not read as drift it is not.
		// A downloaded plugin is pinned per platform: the lock holds the entry
		// whichever machine last recorded it used, and this machine may be a
		// different one. Comparing across platforms would report drift on
		// every mixed-platform team, and drift that fires on a correct
		// manifest teaches people to re-resolve until it stops. What ran here
		// is still pinned — by the digest in the manifest, checked before the
		// download was unpacked — so nothing is unverified; it is only the
		// lock that has nothing comparable to say.
		if want.Origin == lockfile.OriginURL && got.Origin == lockfile.OriginURL &&
			(want.OS != got.OS || want.Arch != got.Arch) {
			continue
		}
		if want.Version == got.Version && want.Module == got.Module &&
			want.SHA256 == got.SHA256 && want.Path == got.Path &&
			(want.Origin == "" || want.Origin == got.Origin) {
			continue
		}
		return fmt.Errorf("plugin %q: %s records %s, this run resolved %s; "+
			"declare module and version in the manifest so the tool installs a fixed one, "+
			"or re-resolve with --update to record what is here",
			got.Name, opts.LockPath, describe(want), describe(got))
	}
	return nil
}

// describe renders a recorded plugin for an error message, naming the module
// when there is one so that a managed and an unmanaged entry cannot read the
// same.
func describe(p lockfile.Plugin) string {
	switch {
	case p.SHA256 != "":
		return p.URL + " at sha256:" + p.SHA256 + " for " + p.OS + "/" + p.Arch
	case p.Path != "":
		return p.Version + " from the path " + p.Path
	case p.Module == "":
		return p.Version + " from PATH"
	default:
		return p.Module + "@" + p.Version
	}
}

// write records the closure that was just resolved.
func write(g *resolve.Graph, plugins []lockfile.Plugin, path string) error {
	lock := &lockfile.Lock{Version: lockfile.Version, Plugins: plugins}
	for _, o := range g.Deps() {
		lock.Deps = append(lock.Deps, lockfile.Entry{Name: o.Name, Git: o.Git, Ref: o.Ref, SHA: o.SHA})
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
