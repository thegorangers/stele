package breaking

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/thegorangers/stele/internal/config"
)

// ApplyMoves rewrites prev so that every declaration carries the name the
// current revision knows it by, and returns the rewritten revision.
//
// The rewrite happens before the comparison, not after it, and that ordering
// is the whole safety argument: nothing here can drop a finding, because no
// finding exists yet. A move that renames losslessly leaves the comparison
// with nothing to report; a move that also dropped something leaves the drop
// standing, under its new name.
//
// Descriptors are immutable, so nothing is mutated: each file is converted
// back to its FileDescriptorProto, rewritten there, and the whole set is
// recompiled. Recompilation is also the collision check — two declarations
// renamed onto one name fail to build, and that failure is the refusal.
func ApplyMoves(prev Revision, moves []config.Move) (Revision, error) {
	if len(moves) == 0 {
		return prev, nil
	}

	pkgMoves := map[string]string{}
	fileMoves := map[string]string{}
	for _, m := range moves {
		if strings.HasPrefix(m.From, config.MoveFilePrefix) {
			fileMoves[strings.TrimPrefix(m.From, config.MoveFilePrefix)] =
				strings.TrimPrefix(m.To, config.MoveFilePrefix)
			continue
		}
		pkgMoves[m.From] = m.To
	}

	set := &descriptorpb.FileDescriptorSet{}
	owned := make(map[string]bool, len(prev.Owned))
	for _, p := range prev.Owned {
		owned[p] = true
	}

	newOwned := make([]string, 0, len(prev.Owned))
	for _, fd := range prev.Files {
		fdp := protodesc.ToFileDescriptorProto(fd)
		rewriteFile(fdp, pkgMoves, fileMoves)
		set.File = append(set.File, fdp)
		if owned[fd.Path()] {
			newOwned = append(newOwned, fdp.GetName())
		}
	}

	files, err := protodesc.NewFiles(set)
	if err != nil {
		return Revision{}, fmt.Errorf("breaking: the declared moves do not build: %w; "+
			"a move that renames two declarations onto one name is refused, not resolved", err)
	}

	out := Revision{Owned: newOwned, DepName: prev.DepName}
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		out.Files = append(out.Files, fd)
		return true
	})
	return out, nil
}

// ValidateMoves checks the two things about a move that the manifest alone
// cannot decide, and that therefore are not in config's validation.
//
// A destination this repository does not own would make a pinned third party
// the authority on whether your declarations still exist: the comparison
// would pass for as long as the dependency happened to carry a matching
// name, and break the day it did not, over a change nobody here made.
//
// A source that still exists in the current revision has not moved. Renaming
// it in the previous revision would make the surviving original read as an
// addition and the rename read as clean, which is the one shape that could
// hide a removal.
//
// The destination is checked before the source, and that order is what
// catches a contradictory chain (a moved to b, and b moved to c) without a
// third check: the first entry requires b to be owned in the current
// revision, and the second entry's source check then finds that b is still
// there and refuses, naming the fact both entries disagree about.
func ValidateMoves(prev, cur Revision, moves []config.Move) error {
	curPkgs := map[string]bool{}
	curPaths := map[string]bool{}
	owned := map[string]bool{}
	for _, p := range cur.Owned {
		owned[p] = true
	}
	for _, fd := range cur.Files {
		if !owned[fd.Path()] {
			continue
		}
		curPkgs[string(fd.Package())] = true
		curPaths[fd.Path()] = true
	}

	for i, m := range moves {
		field := fmt.Sprintf("breaking.moves[%d]", i)
		if strings.HasPrefix(m.From, config.MoveFilePrefix) {
			from := strings.TrimPrefix(m.From, config.MoveFilePrefix)
			to := strings.TrimPrefix(m.To, config.MoveFilePrefix)
			if !curPaths[to] {
				return fmt.Errorf("%s.to: %s is not a file this repository owns in the current revision; "+
					"a move may only point at your own declarations", field, to)
			}
			if curPaths[from] {
				return fmt.Errorf("%s.from: %s still exists in the current revision, so it has not moved", field, from)
			}
			continue
		}
		if !curPkgs[m.To] {
			return fmt.Errorf("%s.to: %s is not a package this repository owns in the current revision; "+
				"a move may only point at your own declarations, or a pinned dependency becomes "+
				"the authority on whether they exist", field, m.To)
		}
		if curPkgs[m.From] {
			return fmt.Errorf("%s.from: %s still exists in the current revision, so it has not moved", field, m.From)
		}
	}
	return nil
}

// rewriteFile applies the move maps to one file descriptor in place. It is
// called on a fresh FileDescriptorProto produced by protodesc, never on
// anything shared.
func rewriteFile(fdp *descriptorpb.FileDescriptorProto, pkgMoves, fileMoves map[string]string) {
	if to, ok := fileMoves[fdp.GetName()]; ok {
		fdp.Name = &to
	}
	for i, dep := range fdp.Dependency {
		if to, ok := fileMoves[dep]; ok {
			fdp.Dependency[i] = to
		}
	}

	oldPkg := fdp.GetPackage()
	newPkg, moved := pkgMoves[oldPkg]
	if moved {
		fdp.Package = &newPkg
		if fdp.Options != nil && fdp.Options.GoPackage != nil {
			// go_package does not follow the proto package by itself. A
			// probe established this: leaving it behind leaves a change the
			// comparison then reports, so carrying it is part of the move.
			gp := rewriteGoPackage(fdp.Options.GetGoPackage(), oldPkg, newPkg)
			fdp.Options.GoPackage = &gp
		}
	}

	// Type references are fully qualified with a leading dot, and a reference
	// may point into any moved package, not only this file's own.
	//
	// The sources are tried longest first, because the manifest layer permits
	// two moves where one source is a prefix of the other (it refuses a
	// duplicate source, a cycle and a self-move — not an overlap). A file's
	// own Package is looked up exactly and therefore always takes the longer
	// source, so a reference matched against the shorter one would name a
	// package no file declares. Iterating the map directly picked whichever
	// source the draw offered first, which made that mismatch a coin toss.
	froms := make([]string, 0, len(pkgMoves))
	for from := range pkgMoves {
		froms = append(froms, from)
	}
	sort.Slice(froms, func(i, j int) bool {
		if len(froms[i]) != len(froms[j]) {
			return len(froms[i]) > len(froms[j])
		}
		return froms[i] < froms[j]
	})

	rewriteRef := func(s *string) {
		if s == nil || *s == "" {
			return
		}
		name := strings.TrimPrefix(*s, ".")
		for _, from := range froms {
			if name == from || strings.HasPrefix(name, from+".") {
				*s = "." + pkgMoves[from] + strings.TrimPrefix(name, from)
				return
			}
		}
	}

	var walkMsg func(m *descriptorpb.DescriptorProto)
	walkMsg = func(m *descriptorpb.DescriptorProto) {
		for _, f := range m.Field {
			rewriteRef(f.TypeName)
			rewriteRef(f.Extendee)
		}
		for _, f := range m.Extension {
			rewriteRef(f.TypeName)
			rewriteRef(f.Extendee)
		}
		for _, n := range m.NestedType {
			walkMsg(n)
		}
	}
	for _, m := range fdp.MessageType {
		walkMsg(m)
	}
	for _, f := range fdp.Extension {
		rewriteRef(f.TypeName)
		rewriteRef(f.Extendee)
	}
	for _, s := range fdp.Service {
		for _, mth := range s.Method {
			rewriteRef(mth.InputType)
			rewriteRef(mth.OutputType)
		}
	}
}

// rewriteGoPackage maps a go_package through a package move by replacing the
// segments the proto package contributed to it. A go_package carries the
// package in up to two forms — the slash-separated import path, and the
// dot-stripped identifier after the semicolon — and each half is rewritten
// only in its own form: the identifier form is a very short needle
// ("ab" for example.a.b), and letting it loose over the whole value rewrote
// bytes of the import path that had nothing to do with the move.
//
// Neither half need carry the whole package — a repository generating under
// gen/ contributes only the tail — so the longest matching tail wins. A
// go_package that bears no relation to the proto package at all is left
// alone: this cannot guess at a convention the repository did not follow.
//
// SHORTCUT: go_package is mapped by substituting the tail of the proto
// package, in the import path and in the identifier separately; ceiling: a
// go_package whose relation to the proto package is not that tail is left
// unchanged and surfaces as an ordinary finding, and a half that happens to
// contain the tail elsewhere is rewritten there too, producing a spurious
// one; upgrade: an explicit go_package field on the move entry, if a
// repository hits either.
func rewriteGoPackage(gp, oldPkg, newPkg string) string {
	oldSeg := strings.Split(oldPkg, ".")
	newSeg := strings.Split(newPkg, ".")

	// A single trailing segment is almost always a version ("v1"), which
	// matches far too much to substitute on, so tails of one segment are
	// only tried when that is the whole package name.
	minTail := 2
	if len(oldSeg) < minTail || len(newSeg) < minTail {
		minTail = 1
	}

	substituteTail := func(s, sep string) string {
		for n := min(len(oldSeg), len(newSeg)); n >= minTail; n-- {
			from := strings.Join(oldSeg[len(oldSeg)-n:], sep)
			to := strings.Join(newSeg[len(newSeg)-n:], sep)
			if from != to && strings.Contains(s, from) {
				return strings.ReplaceAll(s, from, to)
			}
		}
		return s
	}

	// "import/path;identifier", or just "import/path" when the generated
	// package name is left to be derived from the path.
	path, ident, hasIdent := strings.Cut(gp, ";")
	path = substituteTail(path, "/")
	if !hasIdent {
		return path
	}
	return path + ";" + substituteTail(ident, "")
}
