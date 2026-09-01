package breaking

import (
	"fmt"
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
	rewriteRef := func(s *string) {
		if s == nil || *s == "" {
			return
		}
		name := strings.TrimPrefix(*s, ".")
		for from, to := range pkgMoves {
			if name == from || strings.HasPrefix(name, from+".") {
				renamed := "." + to + strings.TrimPrefix(name, from)
				*s = renamed
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
// segments the proto package contributed to it, in both the forms a
// go_package uses them: the slash-separated import path, and the
// dot-stripped identifier after the semicolon. Neither form need carry the
// whole package — a repository generating under gen/ contributes only the
// tail — so the longest matching tail wins, and a go_package that bears no
// relation to the proto package at all is left alone: this cannot guess at a
// convention the repository did not follow, and a go_package the rewrite
// leaves behind shows up as an ordinary finding rather than being silently
// accepted.
//
// SHORTCUT: go_package is mapped by textual substitution of the package path;
// предел: a go_package unrelated to the proto package is left alone and shows
// up as a finding; апгрейд: an explicit go_package field on the move entry, if
// a repository hits it.
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

	joinTail := func(seg []string, n int, sep string) string {
		return strings.Join(seg[len(seg)-n:], sep)
	}
	substituteTail := func(s, sep string) string {
		for n := min(len(oldSeg), len(newSeg)); n >= minTail; n-- {
			from, to := joinTail(oldSeg, n, sep), joinTail(newSeg, n, sep)
			if from != to && strings.Contains(s, from) {
				return strings.ReplaceAll(s, from, to)
			}
		}
		return s
	}

	// The import path and the identifier are rewritten independently: a
	// go_package may carry the package in one form, the other, or both.
	gp = substituteTail(gp, "/")
	return substituteTail(gp, "")
}
