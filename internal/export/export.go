// Package export writes proto files out of a resolved dependency closure into
// a plain directory tree, laid out by import path.
//
// This is the command that removes a registry from the dependency path: the
// files a consumer needs come from the repositories that own them, and the
// result is a directory a compiler can be pointed at with no tool at all.
package export

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ManifestName is the manifest an export is driven by.
const ManifestName = "stele.yaml"

// LockName is the file the resolved closure is pinned in.
const LockName = "stele.lock"

// Options configures one export.
//
// There is deliberately no IncludeImports beside ExcludeImports. Imports are
// included unless excluded, and a second field saying the same thing in the
// other direction can only ever disagree with the first — the caller would
// then need a rule for what both-set means, which is a rule about the tool's
// own fields rather than about proto files. The command line still accepts
// only --exclude-imports for the same reason.
type Options struct {
	// Dir is the module root: the directory holding stele.yaml, and the
	// directory paths in that manifest are relative to. Empty means ".".
	Dir string
	// Output is the directory the tree is written into. It is created if it
	// does not exist; existing files under it are overwritten, and files it
	// already holds that this run did not produce are left alone.
	Output string
	// Paths selects what is emitted, in coordinates relative to the ROOT OF
	// THE MODULE that supplies the file — the same coordinates an import
	// statement uses, and not the workspace-relative coordinates buf takes.
	// A path may name a file or a directory. Empty selects every file this
	// manifest's own modules supply.
	//
	// Paths never restrict resolution: the closure is resolved in full and
	// then narrowed here, because an import path has to mean one file for the
	// whole build regardless of what any one command chose to emit.
	Paths []string
	// ExcludeImports emits only the selected files, leaving the files they
	// import to whoever consumes the output.
	ExcludeImports bool
	// Fetch materialises dependency repositories. When nil, repositories are
	// fetched from the network into CacheRoot.
	Fetch resolve.FetchFunc
	// CacheRoot is where fetched repositories are kept. It is required only
	// when Fetch is nil.
	CacheRoot string
	// NoLock suppresses writing stele.lock. It exists for callers that must
	// not touch the working tree, such as the acceptance harness comparing an
	// export against a checkout it does not own.
	NoLock bool
}

// Run resolves the manifest at Options.Dir and writes the selected files to
// Options.Output.
//
// On success the lock is rewritten from the graph that was just resolved, so
// that the pins a build consumed and the pins recorded cannot describe
// different runs.
func Run(ctx context.Context, opts Options) error {
	dir := opts.Dir
	if dir == "" {
		dir = "."
	}
	if opts.Output == "" {
		return errors.New("export: no output directory")
	}

	cfg, err := config.Load(filepath.Join(dir, ManifestName))
	if err != nil {
		return err
	}

	fetch := opts.Fetch
	if fetch == nil {
		if opts.CacheRoot == "" {
			return errors.New("export: no cache root and no fetcher")
		}
		fetch = networkFetch(opts.CacheRoot)
	}

	graph, err := resolve.ResolveIn(ctx, dir, cfg, fetch)
	if err != nil {
		return err
	}

	selected, err := selectFiles(graph, opts.Paths)
	if err != nil {
		return err
	}
	if !opts.ExcludeImports {
		selected, err = withImports(ctx, graph, selected)
		if err != nil {
			return err
		}
	}
	// Belt and braces: selectFiles already refuses an empty selection, but the
	// rule the design states is about the command as a whole, and a future
	// filter added between here and there must not be able to slip past it.
	if len(selected) == 0 {
		return errors.New("export: nothing to write; a run that produces no files is an error, not an empty success")
	}

	if err := write(graph, selected, opts.Output); err != nil {
		return err
	}
	if opts.NoLock {
		return nil
	}
	return writeLock(graph, filepath.Join(dir, LockName))
}

// selectFiles returns the import paths this manifest's own modules supply,
// narrowed to paths.
//
// Only the root manifest's files are candidates. A dependency's files reach
// the output as imports of what was selected, if at all; exporting them
// because they happen to be resolvable would make the output depend on the
// closure rather than on what this repository asked for.
func selectFiles(g *resolve.Graph, paths []string) ([]string, error) {
	var own []string
	for _, p := range g.ImportPaths() {
		f, ok := g.FileFor(p)
		if ok && f.Origin.Git == "" {
			own = append(own, p)
		}
	}

	if len(paths) == 0 {
		if len(own) == 0 {
			return nil, errors.New("export: this manifest's modules contain no proto files")
		}
		return own, nil
	}

	matched := make(map[string]bool, len(paths))
	keep := make(map[string]bool, len(own))
	for _, raw := range paths {
		want := path.Clean(filepath.ToSlash(strings.TrimSpace(raw)))
		for _, p := range own {
			if p == want || strings.HasPrefix(p, want+"/") {
				matched[raw] = true
				keep[p] = true
			}
		}
	}

	var missed []string
	for _, raw := range paths {
		if !matched[raw] {
			missed = append(missed, raw)
		}
	}
	if len(missed) > 0 {
		// Naming what is available is not decoration: the single most likely
		// cause is a coordinate written relative to the workspace instead of
		// the module root, and seeing the available paths says so at a glance.
		return nil, fmt.Errorf("export: --path matched no files: %s; this manifest's modules supply: %s",
			strings.Join(missed, ", "), strings.Join(own, ", "))
	}

	out := make([]string, 0, len(keep))
	for p := range keep {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// withImports adds the transitive imports of the selected files.
//
// The closure is taken from the compiler rather than from a scan of import
// statements: an import statement is text, and what a file actually depends on
// is what the linker resolved it to.
//
// Files the graph does not supply are left out. In practice those are the
// well-known types the compiler carries inside itself; they have no source in
// any repository here, so there is nothing to copy, and a consumer's compiler
// carries them too.
func withImports(ctx context.Context, g *resolve.Graph, targets []string) ([]string, error) {
	files, err := compile.Compile(ctx, g, targets)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		seen[t] = true
	}
	var visit func(fd protoreflect.FileDescriptor)
	visit = func(fd protoreflect.FileDescriptor) {
		imports := fd.Imports()
		for i := 0; i < imports.Len(); i++ {
			dep := imports.Get(i).FileDescriptor
			p := dep.Path()
			if seen[p] {
				continue
			}
			seen[p] = true
			visit(dep)
		}
	}
	for _, f := range files {
		visit(f)
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		if _, ok := g.FileFor(p); !ok {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// write copies each selected file into out, at its import path.
func write(g *resolve.Graph, selected []string, out string) error {
	for _, p := range selected {
		f, ok := g.FileFor(p)
		if !ok {
			return fmt.Errorf("export: %q is not provided by any module", p)
		}
		body, err := os.ReadFile(f.Path)
		if err != nil {
			return fmt.Errorf("export %q: %w", p, err)
		}
		dst := filepath.Join(out, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeLock records the closure that was just resolved.
func writeLock(g *resolve.Graph, path string) error {
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

// networkFetch fetches repositories into a cache root.
func networkFetch(cacheRoot string) resolve.FetchFunc {
	hosts := source.DefaultHosts()
	return func(ctx context.Context, git, ref string) (string, string, error) {
		addr, err := source.ParseAddr(git, hosts)
		if err != nil {
			return "", "", err
		}
		return source.FetchInto(ctx, cacheRoot, addr.CloneURL(), ref)
	}
}
