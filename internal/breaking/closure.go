// Closure comparison: what this repository re-exports, not just what it owns.
//
// Scoping the comparison to owned files leaves a producer action with a
// consumer consequence invisible: bumping a dependency pin can break your
// consumers while your own files are byte-identical, because a consumer
// resolves this repository's manifest transitively. So the producer run also
// compares the resolved closure reachable from owned modules' imports and
// reports changes there — see docs/design/2026-08-28-breaking-change-detection.md,
// "Scope, and the hole it leaves".
package breaking

import (
	"sort"
	"strings"

	"github.com/bufbuild/protocompile/linker"
)

// Reachable returns the import paths reachable, transitively, from the
// imports of rev's owned modules — excluding the owned paths themselves.
//
// It answers "what do I re-export", not "what do I import": a dependency
// file that no owned file imports, directly or transitively, is never
// something a consumer resolving through this repository can be handed, so
// it has no business in the result — that is exactly what stops this
// becoming "diff the whole world" (see ClassifyClosure's tests).
//
// The well-known types (google/protobuf/*.proto) are excluded. They ship
// with the protobuf runtime itself, are reachable from almost every
// repository in existence, and are not something a consumer of THIS
// repository's pins can break: their own versioning is the protobuf
// project's, not this dependency graph's, and reporting on them would add
// noise the pin owner has no lever to act on.
func Reachable(rev Revision) []string {
	paths, _ := reachableClosure(rev)
	return paths
}

// reachableClosure walks rev's owned files' imports, transitively, and
// returns both the reachable import paths and the linker.File each one
// resolved to — the latter is what lets the closure comparison build
// synthetic Revisions and hand them straight to Diff and Classify, reusing
// their machinery exactly rather than reimplementing it against a second
// descriptor shape.
func reachableClosure(rev Revision) ([]string, linker.Files) {
	owned := ownedSet(rev.Owned)
	visited := make(map[string]linker.File)

	var walk func(f linker.File)
	walk = func(f linker.File) {
		imports := f.Imports()
		for i := 0; i < imports.Len(); i++ {
			p := imports.Get(i).Path()
			if _, ok := visited[p]; ok {
				continue
			}
			if _, ok := owned[p]; ok {
				continue
			}
			if isWellKnownType(p) {
				continue
			}
			child := f.FindImportByPath(p)
			if child == nil {
				// The graph promised this import resolves (compilation
				// would already have failed otherwise); if it somehow
				// doesn't, there is nothing to walk into or report on.
				continue
			}
			visited[p] = child
			walk(child)
		}
	}

	for _, f := range rev.Files {
		if _, ok := owned[f.Path()]; ok {
			walk(f)
		}
	}

	paths := make([]string, 0, len(visited))
	files := make(linker.Files, 0, len(visited))
	for p, f := range visited {
		paths = append(paths, p)
		files = append(files, f)
	}
	sort.Strings(paths)
	sort.Slice(files, func(i, j int) bool { return files[i].Path() < files[j].Path() })
	return paths, files
}

// isWellKnownType reports whether path is one of the protobuf well-known
// types, vendored under google/protobuf/ by every proto toolchain.
func isWellKnownType(path string) bool {
	return strings.HasPrefix(path, "google/protobuf/")
}

// ClassifyClosure compares the resolved closure reachable from prev's and
// cur's owned modules' imports, and reports what changed there on the same
// terms as Classify reports what changed in what this repository owns: the
// same Finding type, the same rule ids, the same categories.
//
// It builds a synthetic Revision on each side — Owned set to the reachable
// paths, Files to the descriptors they resolved to — and hands both straight
// to Diff and Classify. That is deliberate, not a shortcut: Diff and
// Classify already know how to index declarations, pair renames, and avoid
// double-counting a removed container's children, and none of that logic is
// specific to what "owned" means, only to the Owned set the caller states.
// Reimplementing it against the closure would be a second copy of rules a
// change here would then have to remember to keep in step with.
//
// Every finding returned has Closure set to true, and its Message is
// prefixed with the dependency that supplied the file the finding is about —
// looked up by import path in whichever of cur or prev's DepName recorded it
// (cur first: a file present in both revisions is looked up on the side that
// still has it; a file removed from cur is looked up on prev, the only side
// that still knows it existed).
func ClassifyClosure(prev, cur Revision) []Finding {
	prevPaths, prevFiles := reachableClosure(prev)
	curPaths, curFiles := reachableClosure(cur)

	prevClosure := Revision{Files: prevFiles, Owned: prevPaths, DepName: prev.DepName}
	curClosure := Revision{Files: curFiles, Owned: curPaths, DepName: cur.DepName}

	changes := Diff(prevClosure, curClosure)
	findings := Classify(changes, prevClosure, curClosure)

	for i := range findings {
		findings[i].Closure = true
		dep := curClosure.DepName[findings[i].Path]
		if dep == "" {
			dep = prevClosure.DepName[findings[i].Path]
		}
		if dep != "" {
			findings[i].Message = "[dependency " + dep + "] " + findings[i].Message
		}
	}

	return findings
}
