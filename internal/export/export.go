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
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/pin"
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
	// Dep names a dependency of the manifest whose files are to be exported
	// instead of this manifest's own. It is what makes vendoring a producer's
	// contract expressible: the command being replaced is pointed at somebody
	// else's repository, never at the caller's own, and every such invocation
	// in the fleet this tool was measured against is of that shape. The
	// selection is the dependency entry's own `paths`, because that entry
	// already says which part of the producer this manifest asked for;
	// Paths narrows further within it.
	Dep string
	// ExcludeImports emits only the selected files, leaving the files they
	// import to whoever consumes the output.
	ExcludeImports bool
	// Warn, when set, receives non-fatal findings from resolution — today,
	// the drift between a contract's owner and a stale vendored copy of it.
	// A finding that blocks nothing has to be visible somewhere, or the
	// staleness it reports goes on accumulating unseen.
	Warn io.Writer
	// Fetch materialises dependency repositories. When nil, repositories are
	// fetched from the network into CacheRoot.
	Fetch resolve.FetchFunc
	// CacheRoot is where fetched repositories are kept. It is required only
	// when Fetch is nil.
	CacheRoot string
	// NoLock leaves stele.lock out of the run entirely: it is neither read
	// nor written. It exists for callers that must not depend on, or touch, a
	// working tree they do not own, such as the acceptance harness comparing
	// an export against a checkout it did not produce.
	NoLock bool
	// Update re-resolves every ref to a fresh commit and rewrites the lock.
	// It is the only thing that moves a pin.
	Update bool
}

// Run resolves the manifest at Options.Dir and writes the selected files to
// Options.Output.
//
// Resolution goes through the lock: see pin.Resolve for what a run with and
// without Update takes from it.
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
		fetch = source.NetworkFetch(opts.CacheRoot)
	}

	graph, err := pin.Resolve(ctx, pin.Options{
		Dir:      dir,
		Manifest: cfg,
		LockPath: filepath.Join(dir, LockName),
		Fetch:    fetch,
		Update:   opts.Update,
		NoLock:   opts.NoLock,
	})
	if err != nil {
		return err
	}

	if opts.Warn != nil {
		resolve.WriteDrift(opts.Warn, graph.Drift())
	}

	selected, err := selectFiles(cfg, graph, opts.Dep, opts.Paths)
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

	return write(graph, selected, opts.Output)
}

// selectFiles returns the import paths to write, narrowed to paths.
//
// Without a dependency named, only the root manifest's files are candidates. A
// dependency's files reach the output as imports of what was selected, if at
// all; exporting them because they happen to be resolvable would make the
// output depend on the closure rather than on what this repository asked for.
//
// With one named, the candidates are that dependency's files and only those:
// its own modules', not those of the producers it in turn depends on, which
// reach the output as imports or not at all — the same rule, applied one
// repository further out.
func selectFiles(cfg *config.File, g *resolve.Graph, dep string, paths []string) ([]string, error) {
	var candidates []string
	var scope string
	if dep == "" {
		for _, p := range g.ImportPaths() {
			f, ok := g.FileFor(p)
			if ok && f.Origin.Git == "" {
				candidates = append(candidates, p)
			}
		}
		scope = "this manifest's modules"
		if len(candidates) == 0 && len(paths) == 0 {
			return nil, errors.New("export: this manifest's modules contain no proto files")
		}
	} else {
		d, err := findDep(cfg, dep)
		if err != nil {
			return nil, err
		}
		candidates, err = depFiles(g, d)
		if err != nil {
			return nil, err
		}
		scope = fmt.Sprintf("module %q of dependency %q", d.Module, d.Name)
		if len(candidates) == 0 {
			return nil, fmt.Errorf("export: %s contains no proto files", scope)
		}
		// The dependency entry already states which part of the producer this
		// manifest asked for. Applying it here rather than making the caller
		// repeat it on the command line keeps one statement of that fact.
		//
		// Resolution honours the same narrowing, so most of what this would
		// remove is not in the graph to begin with. It is applied again rather
		// than dropped because the two answer different questions: resolution
		// keeps an excluded file that a selected one imports, and an import is
		// not something this manifest asked to vendor under its own name.
		candidates, err = narrow(candidates, d.Paths, scope+": paths")
		if err != nil {
			return nil, err
		}
	}

	if len(paths) == 0 {
		return candidates, nil
	}
	return narrow(candidates, paths, "--path")
}

// findDep returns the dependency the manifest declares under name.
func findDep(cfg *config.File, name string) (config.Dep, error) {
	names := make([]string, 0, len(cfg.Deps))
	for _, d := range cfg.Deps {
		if d.Name == name {
			return d, nil
		}
		names = append(names, d.Name)
	}
	if len(names) == 0 {
		return config.Dep{}, fmt.Errorf("export: --dep %q: this manifest declares no dependencies", name)
	}
	return config.Dep{}, fmt.Errorf("export: --dep %q: this manifest declares: %s", name, strings.Join(names, ", "))
}

// depFiles returns the import paths supplied by the requested module of one
// direct dependency.
//
// A file qualifies on two counts, and both are needed. The origin must be that
// dependency, so that a producer reached only transitively is not exported by
// a name the root manifest never gave it; and the import root must be the
// module the dependency entry asked for, because resolution deliberately adds
// every module root a producer declares — including the vendored tree it has
// not stopped carrying yet — and those are somebody else's files, reachable so
// that imports resolve, not files this manifest asked to vendor.
func depFiles(g *resolve.Graph, d config.Dep) ([]string, error) {
	var out []string
	for _, p := range g.ImportPaths() {
		f, ok := g.FileFor(p)
		if !ok || f.Origin.Name != d.Name || f.Origin.Git != d.Git {
			continue
		}
		root, err := resolve.ModuleRoot(f.Origin.Dir, d.Module)
		if err != nil {
			return nil, err
		}
		if filepath.Clean(f.Root) != root {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// narrow keeps the candidates a path selects, and refuses a path that selects
// none.
//
// Naming what is available is not decoration: the single most likely cause is
// a coordinate written relative to the workspace instead of the module root,
// and seeing the available paths says so at a glance.
func narrow(candidates, paths []string, what string) ([]string, error) {
	if len(paths) == 0 {
		return candidates, nil
	}
	matched := make(map[string]bool, len(paths))
	keep := make(map[string]bool, len(candidates))
	for _, raw := range paths {
		want := path.Clean(filepath.ToSlash(strings.TrimSpace(raw)))
		for _, p := range candidates {
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
		return nil, fmt.Errorf("export: %s matched no files: %s; available: %s",
			what, strings.Join(missed, ", "), strings.Join(candidates, ", "))
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

// networkFetch fetches repositories into a cache root.
