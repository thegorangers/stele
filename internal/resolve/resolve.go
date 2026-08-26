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
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/hashing"
	"github.com/thegorangers/stele/internal/protoscan"
	"github.com/thegorangers/stele/internal/source"
	"gopkg.in/yaml.v3"
)

// FetchFunc materialises a repository and reports the directory holding its
// tree and the commit it resolved to.
//
// Fetching is a parameter rather than a call into the source package so that
// the caller decides where trees come from — a cache, a checkout, a test —
// and so that resolution itself needs neither a network nor a cache root.
type FetchFunc func(ctx context.Context, git, ref string) (dir, sha string, err error)

// ErrImportConflict reports that one import path is supplied with two
// different contents by suppliers of equal standing. It is deliberately fatal:
// a silent merge would compile one repository's contract against another's
// copy of it.
//
// It compares contents, not suppliers: two suppliers of one path whose files
// are byte-identical present the compiler with the same bytes either way, so
// there is no ambiguity to protect against and the duplicate is merged. That
// is as far as the leniency goes. Anything content-blind — "first wins",
// "last wins" — is precisely what this error exists to prevent, because it
// would make the meaning of an import depend on the order manifests happened
// to be walked in. Standing is the one thing that does decide between
// disagreeing contents, and standing is a property of the suppliers, not of
// when they were reached: see resolveFiles.
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

// key identifies a fetched repository FOR THE WALK. A repository already
// visited at the same commit is not visited twice; at a different commit it is
// a different repository, and any import path it shares with itself is a
// conflict.
//
// The address is reduced to the cache's own notion of identity — host and
// path, without the transport — because that is already what the cache treats
// as one entry, and because the walk is about the tree: the same repository
// cloned over ssh from a workstation and over https from CI is one tree, and
// walking it twice hashes every file in it twice and files every import path
// with two suppliers of identical bytes.
//
// This is deliberately NOT address normalisation, and it is not the lock's
// key. Addresses are never normalised — a fork at the same path on another
// host is not the same repository, which is why the host stays in the key —
// and the lock goes on recording every (git, ref) a manifest states, because
// that is what a pinned run looks up. Two notions of identity, each used for
// the one thing it is right about.
//
// An address the parser does not understand is left as written. It cannot be
// fetched either, so the walk will not get this far; falling back keeps this
// function total rather than making it a second place an address is judged.
func (o Origin) key() string {
	if addr, err := source.ParseAddr(o.Git, nil); err == nil {
		return addr.CacheKey() + "@" + o.SHA
	}
	return o.Git + "@" + o.SHA
}

// request identifies a dependency request as a manifest states it: an address
// and a ref. It is the lock's key, and this is the one place the two notions
// must agree.
func request(git, ref string) string { return git + "@" + ref }

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
	// Authoritative reports whether the root that supplied this file was
	// claimed by a manifest, as opposed to being admitted by the buf.yaml
	// compatibility fallback. See addModule.
	Authoritative bool
}

// Graph is the resolved closure: every import root the compiler is given, and
// the file each import path resolves to.
//
// The graph holds every file of every module root reached through manifests,
// narrowed by what the manifests asked for. Two narrowings are deliberately
// not the same thing:
//
//   - A generate input's paths select what one TARGET is built from, and they
//     never reach the graph. An import path has to mean one file for the whole
//     build; if availability depended on the selection of one target the same
//     import could resolve differently in two targets of one manifest.
//   - A dependency's paths say what this manifest took from that producer.
//     They are a property of the manifest, identical for every target of it,
//     so honouring them cannot make one import mean two things — and not
//     honouring them made the manifest a description of a build other than the
//     one that ran. See narrow.
type Graph struct {
	files     map[string]Resolved
	suppliers map[string][]supplier
	roots     []string
	deps      []Origin
	drift     []Drift
	// narrowings records, per requested module of per repository, what the
	// dependency edges reaching it asked for.
	narrowings map[string]*narrowing
	// rootNarrowing maps a module root's directory to the key of the
	// narrowing that governs it, so that a supplier found under that root can
	// be matched back to what was asked for.
	rootNarrowing map[string]string
}

// narrowing is the union of what every dependency edge reaching one module of
// one repository asked for.
//
// It is a union, and it is collected from EVERY edge — including an edge to a
// repository the walk has already visited — because the alternative is an
// answer that depends on which manifest was read first. Two consumers of one
// producer, one taking a subtree and one taking the module whole, offer the
// module whole; that is the only reading that does not depend on their order.
type narrowing struct {
	// deps names the dependency entries that asked, for the error message.
	deps []string
	// whole records that some edge asked for the module without narrowing it.
	whole bool
	// paths is the union of the narrowings, cleaned, in the order first seen.
	paths []string
}

// supplier is one root's offer of one import path, recorded during the walk.
// Nothing is decided while walking: an import path is settled only once every
// supplier of it is known, so that the answer cannot depend on which manifest
// happened to be read first. See resolveFiles.
type supplier struct {
	path          string
	root          string
	sha256        string
	origin        Origin
	authoritative bool
}

// less orders the suppliers of one import path. It is a total order over
// properties of the suppliers themselves — never over arrival — so that the
// same set of suppliers is ranked the same way whatever order they were
// reached in.
//
// Authority first, because that is the rule; then the address, the commit, the
// dependency name, and the file, which only break ties and never decide
// between differing contents on their own: a tie among equals of one standing
// is either agreement in bytes, in which case any of them will do, or
// ErrImportConflict.
func (a supplier) less(b supplier) bool {
	if a.authoritative != b.authoritative {
		return a.authoritative
	}
	if a.origin.Git != b.origin.Git {
		return a.origin.Git < b.origin.Git
	}
	if a.origin.SHA != b.origin.SHA {
		return a.origin.SHA < b.origin.SHA
	}
	if a.origin.Name != b.origin.Name {
		return a.origin.Name < b.origin.Name
	}
	if a.root != b.root {
		return a.root < b.root
	}
	return a.path < b.path
}

// ImportRoots returns the import roots, root manifest first and dependencies
// in the order they were reached.
//
// The order is deterministic and carries no precedence. Which root supplies an
// import path is decided by resolveFiles from the standing of the roots that
// offer it, before any of this is handed to a compiler, so listing the same
// dependencies in another order resolves the same files, reports the same
// drift, and fails with the same error.
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

// Drift reports every import path where an authoritative root won over a
// fallback root that supplied different bytes.
type Drift struct {
	ImportPath          string
	Authoritative       Origin
	AuthoritativePath   string
	AuthoritativeSHA256 string
	Fallback            Origin
	FallbackPath        string
	FallbackSHA256      string
}

// Drift returns the drift observed while resolving.
func (g *Graph) Drift() []Drift { return append([]Drift(nil), g.drift...) }

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
	g := &Graph{
		files:         map[string]Resolved{},
		suppliers:     map[string][]supplier{},
		narrowings:    map[string]*narrowing{},
		rootNarrowing: map[string]string{},
	}

	// The root manifest is not fetched and is not deduplicated against
	// anything: it is where the walk starts.
	rootOrigin := Origin{Dir: dir}
	for _, m := range root.Modules {
		if err := g.addModule(rootOrigin, m.Path, true); err != nil {
			return nil, err
		}
	}

	type pending struct {
		origin Origin
		file   *config.File
	}
	queue := []pending{{rootOrigin, root}}
	visited := map[string]bool{}
	requested := map[string]bool{}

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

			manifest, own, err := manifestOf(treeDir, d.Module)
			if err != nil {
				return nil, fmt.Errorf("dependency %q of %s: %w", d.Name, cur.origin, err)
			}
			// The requested module is checked on every edge, including one
			// leading to a repository already walked: the mistake belongs to
			// the manifest that made the request, not to the repository.
			if err := checkRequestedModule(d, manifest); err != nil {
				return nil, fmt.Errorf("dependency %q of %s: %w", d.Name, cur.origin, err)
			}
			// Every distinct request is recorded, even one leading to a
			// repository already walked. The lock is keyed by (git, ref) —
			// that is what a manifest states and what the pinned run looks
			// up — so a request left unrecorded would be a request the next
			// run cannot answer, and --update would not fix it because
			// --update is what dropped it. Two refs that name one commit are
			// two requests; so are two addresses of one repository.
			if !requested[request(d.Git, d.Ref)] {
				requested[request(d.Git, d.Ref)] = true
				g.deps = append(g.deps, origin)
			}
			// What the edge asked for is recorded on every edge too, and for
			// the same reason: a narrowing seen only on the first edge to
			// reach a repository would make the offered files depend on the
			// order the manifests were read in.
			if err := g.note(origin, d); err != nil {
				return nil, fmt.Errorf("dependency %q of %s: %w", d.Name, cur.origin, err)
			}
			// Walking, unlike recording, is per commit: the tree is the same
			// whichever request reached it.
			if visited[origin.key()] {
				continue
			}
			visited[origin.key()] = true

			// Every module root of the producer is added, not only the one
			// asked for. That is the whole point of reading the manifest: the
			// requested module's files import files from the producer's other
			// roots, and those imports have to resolve.
			for _, m := range manifest.Modules {
				if err := g.addModule(origin, m.Path, own(m.Path)); err != nil {
					return nil, err
				}
			}
			queue = append(queue, pending{origin, manifest})
		}
	}
	if err := g.narrow(); err != nil {
		return nil, err
	}
	if err := g.resolveFiles(); err != nil {
		return nil, err
	}
	return g, nil
}

// addModule records every proto file under one module root as a supplier of
// its import path. It decides nothing: two roots offering one path are settled
// by resolveFiles once the whole walk is done.
//
// authoritative says whether the root is one a manifest claims as its own —
// a "modules:" entry of a stele.yaml, or the module a dependency edge names —
// as opposed to a root admitted only by the buf.yaml compatibility fallback
// so that the producer's own files can resolve their imports.
//
// The test is structural on purpose. Naming a directory ("third_party",
// "vendor") would encode one fleet's habit into the tool and would be wrong
// the first time somebody spells it differently; and it would say nothing
// about authority, only about a word. What actually distinguishes the two is
// who asked for the root: a claimed root is a statement of ownership made by
// a manifest this tool understands, while a fallback root is an inference the
// tool made on the producer's behalf, for the producer's internal benefit.
// Preferring a statement over an inference is the whole of the rule.
func (g *Graph) addModule(origin Origin, modulePath string, authoritative bool) error {
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
	g.rootNarrowing[root] = narrowKey(origin, rel)

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
		g.suppliers[importPath] = append(g.suppliers[importPath], supplier{
			path:          p,
			root:          root,
			sha256:        sum,
			origin:        origin,
			authoritative: authoritative,
		})
		return nil
	})
}

// narrowKey identifies one module of one repository: the unit a dependency
// entry's paths narrow. The repository is reduced to the same identity the
// walk uses, so that one tree reached over two transports is one subject.
func narrowKey(o Origin, moduleRel string) string { return o.key() + "\x00" + moduleRel }

// note records what one dependency edge asked for.
func (g *Graph) note(origin Origin, d config.Dep) error {
	rel, err := cleanModulePath(d.Module)
	if err != nil {
		return err
	}
	k := narrowKey(origin, rel)
	n := g.narrowings[k]
	if n == nil {
		n = &narrowing{}
		g.narrowings[k] = n
	}
	n.deps = appendUnique(n.deps, d.Name)
	if len(d.Paths) == 0 {
		n.whole = true
		return nil
	}
	for _, raw := range d.Paths {
		n.paths = appendUnique(n.paths, path.Clean(filepath.ToSlash(strings.TrimSpace(raw))))
	}
	return nil
}

// narrow removes from the graph the files no manifest asked for.
//
// Why this is here at all: `paths:` on a dependency used to be read by
// `export` and by nothing else, so a manifest that said it took one subtree of
// a producer had the producer's whole module in its graph. That is not
// cosmetic. Every file that enters is judged by the one-import-path-one-content
// rule and reported as drift when it disagrees with its owner, so a producer's
// stale vendored copy of a well-known contract could conflict with, or be
// reported against, a consumer that had explicitly narrowed it away — and a
// report naming files nobody asked for is a report people learn to skim.
//
// What narrowing means, precisely, is what is OFFERED, not what is REACHABLE.
// A file the narrowing excluded is put back if a file that survived imports
// it, directly or transitively. The alternative — an error naming the import
// and the narrowing that excluded it — was rejected: the import structure
// inside the producer's module is the producer's business and changes without
// the consumer touching anything, so an upstream commit adding one import
// would break every narrowed consumer at once, with no fix available except to
// widen the narrowing to include a file they deliberately do not want to
// generate from. `paths:` selects; it has never been a wall, and a second
// meaning for one word is how a manifest stops being readable.
//
// A narrowing that selects no file is an error rather than an empty offer,
// for the same reason the same mistake is an error in `export` and in a
// generate input: the commonest cause by far is a coordinate written relative
// to the repository instead of the module root, and it would otherwise present
// as a file missing somewhere else entirely.
func (g *Graph) narrow() error {
	// The whole pass is skipped unless something was actually narrowed, so
	// that a build with no narrowing reads no file it would not otherwise
	// have read.
	if !g.anyNarrowed() {
		return nil
	}

	matched := map[string]bool{}
	offered := map[string][]string{}
	excluded := map[string][]supplier{}
	for importPath, offers := range g.suppliers {
		var keep []supplier
		for _, o := range offers {
			n := g.narrowings[g.rootNarrowing[o.root]]
			if n == nil || n.whole {
				keep = append(keep, o)
				continue
			}
			offered[g.rootNarrowing[o.root]] = append(offered[g.rootNarrowing[o.root]], importPath)
			hit := false
			for _, want := range n.paths {
				if importPath == want || strings.HasPrefix(importPath, want+"/") {
					matched[g.rootNarrowing[o.root]+"\x00"+want] = true
					hit = true
				}
			}
			if hit {
				keep = append(keep, o)
				continue
			}
			excluded[importPath] = append(excluded[importPath], o)
		}
		if len(keep) == 0 {
			delete(g.suppliers, importPath)
			continue
		}
		g.suppliers[importPath] = keep
	}

	if err := g.checkNarrowingsMatched(matched, offered); err != nil {
		return err
	}
	if err := g.restoreReachable(excluded); err != nil {
		return err
	}
	return nil
}

// anyNarrowed reports whether any module was asked for by paths alone.
func (g *Graph) anyNarrowed() bool {
	for _, n := range g.narrowings {
		if !n.whole && len(n.paths) > 0 {
			return true
		}
	}
	return false
}

// checkNarrowingsMatched refuses a path that selected no file, naming what the
// module does supply.
func (g *Graph) checkNarrowingsMatched(matched map[string]bool, offered map[string][]string) error {
	keys := make([]string, 0, len(g.narrowings))
	for k := range g.narrowings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := g.narrowings[k]
		if n.whole {
			continue
		}
		var missed []string
		for _, want := range n.paths {
			if !matched[k+"\x00"+want] {
				missed = append(missed, want)
			}
		}
		if len(missed) == 0 {
			continue
		}
		available := appendUnique(nil, offered[k]...)
		sort.Strings(available)
		return fmt.Errorf("dependency %s: paths matched no files: %s; the module supplies: %s",
			strings.Join(n.deps, ", "), strings.Join(missed, ", "), strings.Join(available, ", "))
	}
	return nil
}

// restoreReachable puts back every excluded file that a file still offered
// imports, transitively.
//
// The imports are read by scanning the file rather than by linking it, because
// linking is what this graph is being built for: the compiler is handed the
// graph and cannot be asked what belongs in it. The scanner reads the one
// statement that matters and strips comments first; where it and the compiler
// could disagree — an import written in a way no scanner sees — the compiler
// reports the missing file by name, which is a legible failure rather than a
// silent one.
func (g *Graph) restoreReachable(excluded map[string][]supplier) error {
	if len(excluded) == 0 {
		return nil
	}
	queue := make([]string, 0, len(g.suppliers))
	for importPath := range g.suppliers {
		queue = append(queue, importPath)
	}
	sort.Strings(queue)

	seen := make(map[string]bool, len(queue))
	for _, p := range queue {
		seen[p] = true
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		offers := g.suppliers[cur]
		if len(offers) == 0 {
			continue
		}
		best := offers[0]
		for _, o := range offers[1:] {
			if o.less(best) {
				best = o
			}
		}
		body, err := os.ReadFile(best.path)
		if err != nil {
			return err
		}
		for _, imp := range protoscan.Imports(body) {
			if seen[imp] {
				continue
			}
			back, ok := excluded[imp]
			if !ok {
				continue
			}
			seen[imp] = true
			g.suppliers[imp] = append(g.suppliers[imp], back...)
			delete(excluded, imp)
			queue = append(queue, imp)
		}
	}
	return nil
}

// appendUnique appends the values not already present, preserving order.
func appendUnique(dst []string, vals ...string) []string {
	for _, v := range vals {
		found := false
		for _, have := range dst {
			if have == v {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}

// resolveFiles settles every import path once all of its suppliers are known.
//
// This is where the design's one-version rule is applied, and it is applied
// to the whole set rather than to each arrival in turn. Deciding on arrival
// was the older shape and it was wrong in a way that only a real closure
// showed: two stale vendored copies of a contract, reached before the owner,
// conflicted with each other and killed the run, while the same repositories
// listed the other way round resolved cleanly with the owner winning. The
// answer must be a property of the inputs, so the choice is made here, from
// the suppliers, in an order that comes from what they are and not from when
// they were read.
//
// The rule, stated plainly:
//
//   - A root a manifest CLAIMS outranks a root the tool INFERRED on a
//     producer's behalf. A claim is a statement of ownership; the fallback is
//     a guess made so that somebody else's files can compile.
//   - Among the suppliers of the highest standing present, disagreeing bytes
//     are ErrImportConflict, naming both. Two owners is a genuine ownership
//     conflict; two guesses leaves nothing worth preferring, so it is fatal
//     too.
//   - A lower-standing supplier that disagrees is drift: the claim wins and
//     the disagreement is reported, never silenced. That report is how a
//     fleet learns its vendored copies have gone stale.
//   - Bytes that agree deduplicate whatever the standing, and every supplier
//     of those bytes is recorded.
func (g *Graph) resolveFiles() error {
	paths := make([]string, 0, len(g.suppliers))
	for p := range g.suppliers {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, importPath := range paths {
		offers := g.suppliers[importPath]
		sort.Slice(offers, func(i, j int) bool { return offers[i].less(offers[j]) })
		winner := offers[0]

		// Disagreement among equals of the winner's standing is fatal. It is
		// checked before anything is recorded, so a conflict never leaves a
		// half-built answer behind.
		for _, o := range offers[1:] {
			if o.authoritative == winner.authoritative && o.sha256 != winner.sha256 {
				return fmt.Errorf("%w: %s is supplied by %s as %s (sha256 %s) and by %s as %s (sha256 %s)",
					ErrImportConflict, importPath,
					winner.origin, winner.path, winner.sha256,
					o.origin, o.path, o.sha256)
			}
		}

		resolved := Resolved{
			ImportPath:    importPath,
			Path:          winner.path,
			Root:          winner.root,
			SHA256:        winner.sha256,
			Origin:        winner.origin,
			Sources:       []Origin{winner.origin},
			Authoritative: winner.authoritative,
		}
		for _, o := range offers[1:] {
			if o.sha256 == winner.sha256 {
				resolved.Sources = append(resolved.Sources, o.origin)
				continue
			}
			// Only a lower standing can reach here; equals were refused above.
			g.recordDrift(importPath, winner, o)
		}
		g.files[importPath] = resolved
	}
	return nil
}

// recordDrift notes that a claimed root and a fallback root supplied one
// import path with different bytes. It is not an error, and it must not be
// silent: it is exactly the staleness that vendored trees accumulate, and the
// only moment at which anything can see it.
func (g *Graph) recordDrift(importPath string, authoritative, fallback supplier) {
	g.drift = append(g.drift, Drift{
		ImportPath:          importPath,
		Authoritative:       authoritative.origin,
		AuthoritativePath:   authoritative.path,
		AuthoritativeSHA256: authoritative.sha256,
		Fallback:            fallback.origin,
		FallbackPath:        fallback.path,
		FallbackSHA256:      fallback.sha256,
	})
}

// WriteDrift prints drift in a form a person reads at the end of a command.
// It writes nothing when there is none, so that a quiet run stays quiet.
func WriteDrift(w io.Writer, drift []Drift) {
	if len(drift) == 0 {
		return
	}
	fmt.Fprintf(w, "stele: %d import path(s) supplied by a stale vendored copy; the owner was used:\n", len(drift))
	for _, d := range drift {
		fmt.Fprintf(w, "  %s\n    owner:    %s %s (sha256 %s)\n    vendored: %s %s (sha256 %s)\n",
			d.ImportPath,
			d.Authoritative, d.AuthoritativePath, d.AuthoritativeSHA256,
			d.Fallback, d.FallbackPath, d.FallbackSHA256)
	}
}

// ModuleRoot returns the directory a module path names inside a materialised
// tree. It exists so that a caller holding an Origin and a module path — the
// generator selecting the files of a dependency, for one — computes that
// directory the same way resolution did, instead of growing a second, subtly
// different notion of where a module root is.
func ModuleRoot(treeDir, modulePath string) (string, error) {
	rel, err := cleanModulePath(modulePath)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(treeDir, filepath.FromSlash(rel))), nil
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
// It also reports, per module path, whether the producer claims that root as
// its own. A stele.yaml claims all of them: every entry of "modules:" is a
// statement by the producer. A buf.yaml claims none of them by itself, since
// this tool is only guessing at what that file means; the one root that is
// still claimed there is the module the dependency edge asked for, which the
// consumer's own manifest names. Everything else the fallback admits exists
// solely so the producer's files can compile.
func manifestOf(dir, requestedModule string) (*config.File, func(string) bool, error) {
	all := func(string) bool { return true }
	if p := filepath.Join(dir, "stele.yaml"); exists(p) {
		f, err := config.Load(p)
		return f, all, err
	}
	requested, err := cleanModulePath(requestedModule)
	if err != nil {
		return nil, nil, err
	}
	onlyRequested := func(m string) bool {
		got, err := cleanModulePath(m)
		return err == nil && got == requested
	}
	if p := filepath.Join(dir, "buf.yaml"); exists(p) {
		roots, err := bufModuleRoots(p)
		if err != nil {
			return nil, nil, err
		}
		return &config.File{Version: config.Version, Modules: roots}, onlyRequested, nil
	}
	// No manifest at all: the module asked for is the only root there is, and
	// it is the one the consumer named.
	return &config.File{Version: config.Version, Modules: []config.Module{{Path: requested}}}, all, nil
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
