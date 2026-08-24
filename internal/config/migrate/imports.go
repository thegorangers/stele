package migrate

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// This file is the answer to the question the rest of the package could not
// ask: which third-party files does this repository actually read?
//
// A vendor target in a Makefile says which modules were copied in. It does not
// say which of them are needed, and the two are not the same number: on the
// measured fleet a single `buf export` of one registry module brought 47 files
// in and exactly one of them was ever imported. A translation that reports the
// vendor target as its dependency set therefore reports the same two entries
// for every repository in a fleet, whether or not that repository reads a byte
// of either — and a list that is the same everywhere is not read anywhere.
//
// The protos are in the working copy, so the question is decidable. Collect
// the imports of the files this configuration compiles, follow them
// transitively, subtract the well-known types the compiler carries, and what
// is left is the exact set of external files needed — file by file, not module
// by module. That set is what a dependency is demanded for, and it is also
// the `paths:` narrowing the manifest needs.

// wellKnownPrefix is the import prefix of the descriptors the compiler carries
// itself. protocompile's standard imports supply every file under it, so an
// import of one needs no dependency and must never be reported as a hole.
//
// It is spelled here rather than imported from internal/genreq to keep this
// package's dependencies to the configuration it translates; the two constants
// describe one fact and a test in genreq measures it against the compiler.
const wellKnownPrefix = "google/protobuf/"

// indexedFile is one .proto found in the working copy.
type indexedFile struct {
	// Root is the module root it was found under, in workspace coordinates.
	Root string
	// Work is its path from the workspace root.
	Work string
	// Imports are the import paths it declares, in file order.
	Imports []string
}

// protoIndex is every .proto of the working copy, keyed by the import path it
// is reachable under.
type protoIndex struct {
	files map[string]indexedFile
	// order is the insertion order, so that a walk of the index is stable.
	order []string
}

// indexProtos reads every .proto under each root. Roots are given owner-first:
// where two roots supply one import path the first wins, which is the same
// precedence the resolver applies to a local module over a vendored copy of
// it. A duplicate is not an error here — this index answers "what is
// imported", and the resolver is the place that refuses an ambiguous path.
func indexProtos(fsys fs.FS, roots []string) (*protoIndex, error) {
	ix := &protoIndex{files: map[string]indexedFile{}}
	for _, root := range roots {
		root = path.Clean(root)
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				// A root that is not there is not a failure: a vendored tree
				// is generated, and a fresh clone has not run the vendor
				// target yet. What that costs is reported by the caller,
				// which knows whether anything needed the tree.
				if d == nil {
					return fs.SkipAll
				}
				return err
			case d.IsDir() || !strings.HasSuffix(p, ".proto"):
				return nil
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			ip := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
			if root == "." {
				ip = p
			}
			if _, seen := ix.files[ip]; seen {
				return nil
			}
			ix.files[ip] = indexedFile{Root: root, Work: p, Imports: parseImports(b)}
			ix.order = append(ix.order, ip)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return ix, nil
}

// seeds returns the import paths of the files selected by the given
// workspace-relative prefixes, sorted. A prefix names either a directory or a
// single file, which is how a buf input writes one.
func (ix *protoIndex) seeds(prefixes []string) []string {
	var out []string
	for _, ip := range ix.order {
		w := ix.files[ip].Work
		for _, pre := range prefixes {
			pre = path.Clean(pre)
			if w == pre || strings.HasPrefix(w, pre+"/") {
				out = append(out, ip)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// closure follows the imports of the seed files transitively.
//
// It returns every import path reached that is not a seed and not a well-known
// type — the files this repository reads but does not select — split into
// those the working copy supplies and those nothing here accounts for. Both
// halves are sorted, because they are read by a person and a set that reorders
// between runs is a set nobody trusts.
//
// Transitivity is not optional. google/api/annotations.proto is useless
// without google/api/http.proto, and a narrowing derived from direct imports
// alone would produce a manifest that resolves nothing and says so only when
// the compiler runs.
func (ix *protoIndex) closure(seeds []string) (known, unknown []string) {
	seen := map[string]bool{}
	queue := append([]string(nil), seeds...)
	for _, s := range seeds {
		seen[s] = true
	}
	reached := map[string]bool{}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range ix.files[cur].Imports {
			if strings.HasPrefix(imp, wellKnownPrefix) || seen[imp] {
				continue
			}
			seen[imp] = true
			reached[imp] = true
			if _, ok := ix.files[imp]; ok {
				queue = append(queue, imp)
			}
		}
	}
	for imp := range reached {
		if _, ok := ix.files[imp]; ok {
			known = append(known, imp)
		} else {
			unknown = append(unknown, imp)
		}
	}
	sort.Strings(known)
	sort.Strings(unknown)
	return known, unknown
}

// parseImports reads the import statements of one .proto.
//
// It is a scanner, not a parser, and the difference is deliberate: linking the
// file would mean resolving it, which is the thing that cannot be done yet —
// the dependencies this reads the imports to discover are exactly what a
// resolver would need. What it models is the one statement it cares about:
// `import`, optionally `public` or `weak`, then a quoted path. Comments are
// stripped first so that a commented-out import is not counted, and a string
// inside a comment marker inside a string is left alone.
func parseImports(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inBlockComment := false
	for sc.Scan() {
		line, _ := stripProtoComments(sc.Text(), &inBlockComment)
		rest := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(rest, "import")
		if !ok || (rest != "" && !isSpace(rest[0])) {
			continue
		}
		rest = strings.TrimSpace(rest)
		for _, kw := range []string{"public", "weak"} {
			if r, ok := strings.CutPrefix(rest, kw); ok && r != "" && isSpace(r[0]) {
				rest = strings.TrimSpace(r)
			}
		}
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		if end := strings.IndexByte(rest[1:], '"'); end >= 0 {
			out = append(out, rest[1:1+end])
		}
	}
	return out
}

// stripProtoComments removes // and /* */ comments from one line, tracking an
// open block comment across lines. Quoted strings are honoured so that a path
// containing the comment markers survives.
func stripProtoComments(line string, inBlock *bool) (string, bool) {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case *inBlock:
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				*inBlock = false
				i++
			}
		case quote != 0:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(line) {
				i++
				b.WriteByte(line[i])
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			b.WriteByte(c)
		case c == '/' && i+1 < len(line) && line[i+1] == '/':
			return b.String(), true
		case c == '/' && i+1 < len(line) && line[i+1] == '*':
			*inBlock = true
			i++
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

// willAnalyse reports whether the import graph can be read. Without a working
// copy it cannot, and then the vendor target is the only evidence there is.
func (m *migration) willAnalyse() bool { return m.fsys != nil }

// seed records what one generate input selects, in workspace coordinates —
// which is how buf writes them and how the files are found on disk.
func (m *migration) seed(in bufInput) {
	if in.Directory == "" {
		return
	}
	if len(in.Paths) == 0 {
		m.seedPrefixes = append(m.seedPrefixes, path.Clean(in.Directory))
		return
	}
	for _, p := range in.Paths {
		m.seedPrefixes = append(m.seedPrefixes, path.Clean(p))
	}
}

// analyse reads the repository's own protos and records which third-party
// files they actually need.
//
// A tree that is not on disk is not an error: a vendored tree is generated,
// and a fresh clone has not run the vendor target. What that costs is visible
// in the result — an import nothing supplies is reported as an import nothing
// supplies, which is exactly what it is.
func (m *migration) analyse() error {
	if !m.willAnalyse() {
		return nil
	}
	var own, vendored []string
	for _, mod := range m.buf.Modules {
		p := path.Clean(mod.Path)
		if _, ok := m.vendored[p]; ok {
			vendored = append(vendored, p)
			continue
		}
		own = append(own, p)
	}
	// Owner-first: where a local module and a vendored copy supply one import
	// path, the local one is what an import reaches.
	roots := append(append([]string{}, own...), vendored...)
	if len(roots) == 0 {
		// Nothing local to read. A consumer that owns no protos generates
		// from a git input, which names its own paths and needs no walk.
		return nil
	}
	ix, err := indexProtos(m.fsys, roots)
	if err != nil {
		return err
	}
	prefixes := m.seedPrefixes
	if m.seedAll {
		prefixes = roots
	}
	known, unknown := ix.closure(ix.seeds(prefixes))
	m.analysed = true
	for _, ip := range known {
		root := ix.files[ip].Root
		if _, ok := m.vendored[root]; ok {
			m.needed[root] = append(m.needed[root], ip)
		}
	}
	m.unknownImports = unknown
	return nil
}

// attribute turns the needed files into the dependency set and its narrowing.
//
// Three things come out of it, and each replaces a piece of guesswork:
//
//   - A dependency's paths become the files that are read, plus the files that
//     are generated from. The export invocation's own --path is deliberately
//     discarded here: it says what somebody copied in, which is the number
//     that was 47 when the number that mattered was 1.
//
//   - A dependency nothing reads and nothing generates from is dropped, and
//     reported. A vendored path nothing imports is drift, and carrying it into
//     the manifest would carry the drift with it — but it is evidence of
//     intent, so it is said out loud rather than deleted in silence.
//
//   - A needed file no export accounts for is reported by name, with the
//     references that could have produced it. This tool cannot invent a git
//     address: the two conventional ones are conventional, not universal, and
//     a guessed address is how a manifest comes to name a repository nobody
//     meant. What it can do is say exactly which files are missing a source,
//     which is a question a person can answer in one look.
func (m *migration) attribute() {
	if !m.analysed {
		return
	}
	dropped := map[string]bool{}
	for _, root := range m.vendoredOrder {
		deps := m.vendored[root]
		used := map[string]bool{}
		for _, name := range deps {
			// What the export copied in is not what the manifest needs.
			m.deps[name].Paths = nil
			if m.wideInput[name] {
				used[name] = true
			}
			if len(m.inputPaths[name]) > 0 {
				used[name] = true
				m.deps[name].Paths = appendUnique(nil, m.inputPaths[name]...)
			}
		}
		var orphan []string
		for _, p := range m.needed[root] {
			owner := m.ownerOf(p, deps)
			if owner == "" {
				orphan = append(orphan, p)
				continue
			}
			used[owner] = true
			if !m.wideInput[owner] {
				m.deps[owner].Paths = appendUnique(m.deps[owner].Paths, p)
			}
		}
		for _, name := range deps {
			d := m.deps[name]
			if m.wideInput[name] {
				// A generate input takes the whole module, so there is no
				// narrowing left to express.
				d.Paths = nil
				continue
			}
			d.Paths = subsume(d.Paths)
			if !used[name] {
				dropped[name] = true
				m.note("Makefile: `buf export %s` fills %s, but no file this configuration compiles imports anything from it; the dependency was left out. A vendored path nothing imports is drift — declare it by hand if it is meant for something this configuration does not describe",
					d.Git, root)
			}
		}
		m.reportOrphans(root, orphan)
	}
	if len(dropped) > 0 {
		kept := m.depOrder[:0]
		for _, name := range m.depOrder {
			if dropped[name] {
				delete(m.deps, name)
				continue
			}
			kept = append(kept, name)
		}
		m.depOrder = kept
	}
	m.reportUnknownImports()
}

// reportOrphans names the files that are read from a vendored tree but that no
// export invocation accounts for.
func (m *migration) reportOrphans(root string, orphan []string) {
	if len(orphan) == 0 {
		if len(m.registryOf[root]) > 0 {
			m.note("Makefile: %s fills %s, but no file this configuration compiles imports anything from it; nothing was carried over for it",
				strings.Join(m.registryOf[root], " and "), root)
		}
		return
	}
	from := "No invocation in the Makefile fills that tree"
	if refs := m.registryOf[root]; len(refs) > 0 {
		from = fmt.Sprintf("The Makefile fills that tree from %s, which name a schema registry and carry no git address",
			strings.Join(refs, " and "))
	}
	m.unresolved("deps: %d file(s) under %s are imported but have no declared source: %s. %s. Declare the producing repository by hand, and narrow it with paths: those file(s) are the whole set that is read",
		len(orphan), root, strings.Join(orphan, ", "), from)
}

// reportUnknownImports names the imports nothing in the working copy supplies.
// They are not necessarily a mistake — a vendored tree that has not been
// generated yet has this shape — but nothing here says where they come from,
// and saying so is the only honest answer available.
func (m *migration) reportUnknownImports() {
	if len(m.unknownImports) == 0 {
		return
	}
	m.unresolved("deps: %d import(s) are supplied by nothing in this working copy: %s. Either the vendored tree has not been generated, or the producer is undeclared; declare the producing repository by hand",
		len(m.unknownImports), strings.Join(m.unknownImports, ", "))
}

// subsume drops a path already covered by another path in the same list, so
// that a directory selected by a generate input does not appear beside the
// files under it that happen also to be imported.
func subsume(paths []string) []string {
	var out []string
	for _, p := range paths {
		covered := false
		for _, q := range paths {
			if q != p && (p == q || strings.HasPrefix(p, q+"/")) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

// isCommit reports whether a ref names content rather than a label.
//
// A commit SHA is the full hex digest and nothing else. An abbreviation is
// deliberately not one: it identifies content only for as long as it stays
// unambiguous in a repository that keeps growing.
//
// This is a warning and not a refusal, which is a decision worth writing down.
// migrate refuses a shape it cannot translate faithfully, and a branch ref
// translates perfectly: config.Dep documents a branch or a tag as what an
// updating run resolves, `stele` loads such a manifest, and stele.lock pins
// the commit it resolved to — so what is emitted here is a manifest the rest
// of the tool accepts and builds reproducibly from. Refusing it would have
// migrate legislate a policy the tool does not hold. The nearby refusals are
// not counter-examples: `#branch=` is refused because buf's fragment is not a
// ref at all, and an absent ref is refused because inventing one is
// fabrication. Carrying across a ref somebody wrote is neither.
//
// It is not a lint rule either. internal/lint checks linked proto descriptors
// and deliberately reaches nothing of the manifest, and every rule ID it
// carries is permanent under RELEASING.md. A manifest check does not belong in
// that surface, and inventing a second lint for one rule would be a larger
// decision than this one. The roadmap records it as the open question it is.
func isCommit(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
