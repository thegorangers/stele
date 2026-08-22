// Package resolve builds the set of proto files a build may import, by
// walking dependency manifests transitively.
//
// The unit of a dependency is a repository that carries a manifest, not a
// subdirectory. "Give me this subdirectory of that repository" cannot express
// what real producers look like: the files of a producer's module import
// files that live outside that module and are satisfied by the producer's own
// dependencies. So a dependency states its module roots and its own
// dependencies, and resolution follows them the way go.mod does.
package resolve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/hashing"
	"gopkg.in/yaml.v3"
)

// FetchFunc materialises a repository and reports the directory holding its
// tree and the commit it resolved to.
//
// Fetching is a parameter rather than a call into the source package so that
// the caller decides where trees come from — a cache, a checkout, a test —
// and so that resolution itself needs neither a network nor a cache root.
type FetchFunc func(ctx context.Context, git, ref string) (dir, sha string, err error)

// ErrImportConflict reports that one import path is supplied by two different
// sources. It is deliberately fatal: "first wins" would make the meaning of an
// import depend on the order manifests happen to be walked in, and a silent
// merge would compile one repository's contract against another's copy of it.
// It compares contents, not sources: two sources supplying one path with
// byte-identical files present the compiler with the same bytes either way,
// so there is no ambiguity to protect against and the duplicate is merged.
// That is as far as the leniency goes. Anything content-blind — "first wins",
// "last wins" — is precisely what this error exists to prevent, because it
// would make the meaning of an import depend on the order manifests happened
// to be walked in.
var ErrImportConflict = errors.New("import path is provided by two different contents")

// Origin identifies where a file came from.
type Origin struct {
	// Name is the dependency name given by the manifest that requested it.
	// It is empty for the root manifest.
	Name string
	// Git, Ref and SHA are the address of the producing repository, the ref
	// the pin was resolved from, and the commit it resolved to. All three are
	// empty for the root manifest, which is not fetched.
	Git string
	Ref string
	SHA string
	// Dir is the root of the materialised tree.
	Dir string
}

// String renders the origin for an error message.
func (o Origin) String() string {
	if o.Git == "" {
		return "the root manifest"
	}
	return fmt.Sprintf("%s (%s@%s)", o.Name, o.Git, o.SHA)
}

// key identifies a fetched repository. A repository already visited at the
// same commit is not visited twice; at a different commit it is a different
// repository, and any import path it shares with itself is a conflict.
func (o Origin) key() string { return o.Git + "@" + o.SHA }

// Resolved is one proto file the build may import.
type Resolved struct {
	// ImportPath is the path an import statement names, relative to the module
	// root that supplies it, always slash-separated.
	ImportPath string
	// Path is the file on disk.
	Path string
	// Root is the import root the file was found under.
	Root string
	// SHA256 is the hex sha256 of the file's contents. It is what decides
	// whether two sources supplying one import path agree.
	SHA256 string
	// Origin is the repository the file came from.
	Origin Origin
	// Sources lists every repository that supplied this exact path with these
	// exact bytes, Origin first.
	Sources []Origin
}

// Graph is the resolved closure: every import root the compiler is given, and
// the file each import path resolves to.
//
// The graph holds every file of every module root reached through manifests,
// not just the files a dep's paths select. An import path has to mean one file
// for the whole build; if availability depended on the selection of one target
// the same import could resolve differently in two targets of one manifest.
// Narrowing by paths belongs to the commands that produce output.
type Graph struct {
	files map[string]Resolved
	roots []string
	deps  []Origin
}

// ImportRoots returns the import roots, root manifest first and dependencies
// in the order they were reached. The order is deterministic; it carries no
// precedence, because a path supplied twice is an error rather than a choice.
func (g *Graph) ImportRoots() []string { return append([]string(nil), g.roots...) }

// ImportPaths returns every resolvable import path, sorted.
func (g *Graph) ImportPaths() []string {
	out := make([]string, 0, len(g.files))
	for p := range g.files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Deps returns the transitive closure, one entry per repository, in the order
// the repositories were reached. This is what the lock records, flat.
func (g *Graph) Deps() []Origin { return append([]Origin(nil), g.deps...) }

// FileFor returns the file an import path resolves to.
func (g *Graph) FileFor(importPath string) (Resolved, bool) {
	f, ok := g.files[importPath]
	return f, ok
}

// Resolve resolves the closure of a manifest whose own modules are relative to
// the working directory.
func Resolve(ctx context.Context, root *config.File, fetch FetchFunc) (*Graph, error) {
	return ResolveIn(ctx, ".", root, fetch)
}

// ResolveIn resolves the closure of root, whose own modules are relative to
// dir. The walk is breadth-first over manifests.
func ResolveIn(ctx context.Context, dir string, root *config.File, fetch FetchFunc) (*Graph, error) {
	g := &Graph{files: map[string]Resolved{}}

	// The root manifest is not fetched and is not deduplicated against
	// anything: it is where the walk starts.
	rootOrigin := Origin{Dir: dir}
	for _, m := range root.Modules {
		if err := g.addModule(rootOrigin, m.Path); err != nil {
			return nil, err
		}
	}

	type pending struct {
		origin Origin
		file   *config.File
	}
	queue := []pending{{rootOrigin, root}}
	visited := map[string]bool{}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, d := range cur.file.Deps {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			treeDir, sha, err := fetch(ctx, d.Git, d.Ref)
			if err != nil {
				return nil, fmt.Errorf("dependency %q of %s: %w", d.Name, cur.origin, err)
			}
			origin := Origin{Name: d.Name, Git: d.Git, Ref: d.Ref, SHA: sha, Dir: treeDir}

			manifest, err := manifestOf(treeDir, d.Module)
			if err != nil {
				return nil, fmt.Errorf("dependency %q of %s: %w", d.Name, cur.origin, err)
			}
			// The requested module is checked on every edge, including one
			// leading to a repository already walked: the mistake belongs to
			// the manifest that made the request, not to the repository.
			if err := checkRequestedModule(d, manifest); err != nil {
				return nil, fmt.Errorf("dependency %q of %s: %w", d.Name, cur.origin, err)
			}
			if visited[origin.key()] {
				continue
			}
			visited[origin.key()] = true
			g.deps = append(g.deps, origin)

			// Every module root of the producer is added, not only the one
			// asked for. That is the whole point of reading the manifest: the
			// requested module's files import files from the producer's other
			// roots, and those imports have to resolve.
			for _, m := range manifest.Modules {
				if err := g.addModule(origin, m.Path); err != nil {
					return nil, err
				}
			}
			queue = append(queue, pending{origin, manifest})
		}
	}
	return g, nil
}

// addModule registers every proto file under one module root.
func (g *Graph) addModule(origin Origin, modulePath string) error {
	rel, err := cleanModulePath(modulePath)
	if err != nil {
		return fmt.Errorf("%s: %w", origin, err)
	}
	root := filepath.Join(origin.Dir, filepath.FromSlash(rel))
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("%s: module %q: %w", origin, modulePath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: module %q is not a directory", origin, modulePath)
	}

	g.roots = append(g.roots, root)

	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// A fetched cache entry has no .git, but a fallback path may be
			// handed a working copy that still has one. Repository internals
			// are not contracts and must never become importable files.
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		// Only regular files. A symlink is skipped rather than followed: its
		// target can lead outside the tree, which would make the closure
		// depend on something no manifest names and no lock records.
		if !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".proto") {
			return nil
		}
		relPath, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		importPath := filepath.ToSlash(relPath)
		sum, err := hashing.File(p)
		if err != nil {
			return err
		}
		if prev, ok := g.files[importPath]; ok {
			if prev.SHA256 != sum {
				return fmt.Errorf("%w: %s is supplied by %s as %s (sha256 %s) and by %s as %s (sha256 %s)",
					ErrImportConflict, importPath,
					prev.Origin, prev.Path, prev.SHA256,
					origin, p, sum)
			}
			// Identical bytes: the first registration stays, and the second
			// supplier is recorded so that a later command can report the
			// duplication without having to resolve everything again.
			prev.Sources = append(prev.Sources, origin)
			g.files[importPath] = prev
			return nil
		}
		g.files[importPath] = Resolved{
			ImportPath: importPath,
			Path:       p,
			Root:       root,
			SHA256:     sum,
			Origin:     origin,
			Sources:    []Origin{origin},
		}
		return nil
	})
}

// cleanModulePath normalises a module path and refuses one that would leave
// the tree it is relative to.
func cleanModulePath(p string) (string, error) {
	if p == "" {
		// An omitted module means the repository root. It is the only reading
		// that requires no guessing: a repository that keeps its protos at the
		// top level has no subdirectory to name, and "." would say the same
		// thing more noisily.
		return ".", nil
	}
	if path.IsAbs(p) || filepath.IsAbs(p) {
		return "", fmt.Errorf("module %q must be relative to the repository root", p)
	}
	clean := path.Clean(filepath.ToSlash(p))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("module %q leaves the repository", p)
	}
	return clean, nil
}

// checkRequestedModule verifies that the module a dependency asks for is one
// the producer declares. An unknown module would otherwise resolve to an
// absent directory or, worse, to a directory that exists and holds nothing,
// which is the silent empty result the design forbids everywhere.
func checkRequestedModule(d config.Dep, manifest *config.File) error {
	want, err := cleanModulePath(d.Module)
	if err != nil {
		return err
	}
	declared := make([]string, 0, len(manifest.Modules))
	for _, m := range manifest.Modules {
		got, err := cleanModulePath(m.Path)
		if err != nil {
			return err
		}
		if got == want {
			return nil
		}
		declared = append(declared, m.Path)
	}
	return fmt.Errorf("module %q is not declared by the producer (it declares: %s)",
		d.Module, strings.Join(declared, ", "))
}

// manifestOf reads the manifest of a fetched repository.
//
// A producer that has migrated carries stele.yaml and is read in full: its
// module roots and its own dependencies. A producer that has not yet migrated
// carries buf.yaml, and it is read ONLY to learn module roots — no lint, no
// breaking, no deps, no plugin configuration, nothing else, ever. The
// boundary matters: this is not a second configuration format the tool
// supports, it is the minimum needed to treat a dependency that has not
// updated its manifest yet, and it goes away with the last buf.yaml.
//
// A producer with no manifest at all is taken at its word: the module the
// dependency asked for is its only root.
func manifestOf(dir, requestedModule string) (*config.File, error) {
	if p := filepath.Join(dir, "stele.yaml"); exists(p) {
		return config.Load(p)
	}
	if p := filepath.Join(dir, "buf.yaml"); exists(p) {
		roots, err := bufModuleRoots(p)
		if err != nil {
			return nil, err
		}
		return &config.File{Version: config.Version, Modules: roots}, nil
	}
	m, err := cleanModulePath(requestedModule)
	if err != nil {
		return nil, err
	}
	return &config.File{Version: config.Version, Modules: []config.Module{{Path: m}}}, nil
}

// bufModuleRoots reads the module roots out of a buf.yaml, and nothing else.
//
// Parsing is deliberately lenient, which is the opposite of how this tool
// reads its own manifest: an unknown key in stele.yaml is a hard error because
// stele.yaml is a contract with this tool, while buf.yaml belongs to somebody
// else and is full of keys that are none of this tool's business.
func bufModuleRoots(p string) ([]config.Module, error) {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Modules []struct {
			Path string `yaml:"path"`
		} `yaml:"modules"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	if len(doc.Modules) == 0 {
		// A buf.yaml without modules describes a single module rooted where
		// the file sits.
		return []config.Module{{Path: "."}}, nil
	}
	out := make([]config.Module, 0, len(doc.Modules))
	for i, m := range doc.Modules {
		if m.Path == "" {
			return nil, fmt.Errorf("%s: modules[%d].path: missing", p, i)
		}
		out = append(out, config.Module{Path: m.Path})
	}
	return out, nil
}

func exists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
