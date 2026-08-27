package lint

import "google.golang.org/protobuf/reflect/protoreflect"

// subjectOf returns the full name of the declaration at pos, or the empty
// string when no declaration is written there.
//
// It is the inverse of File.Pos, and it is done here rather than asked of the
// rule because a rule cannot answer it: what crosses the plugin boundary is a
// line and a column, and an identity a hosted rule could not produce would
// work for built-in rules only. See rule.Finding.Subject.
//
// Source information indexes locations by a path of field numbers, and several
// paths can start at one position — a field's declaration and its type, a
// message and its first member on the same line. The longest path is taken:
// it names the innermost thing written there, which is the thing a rule
// pointing at that position meant. Positions that name no declaration at all —
// a package statement, an option — yield nothing, and a finding about one is
// a finding about the file. Naming the enclosing declaration instead would let
// two findings about different things share an identity, and a baseline keyed
// on it would suppress the second because the first was already known.
func subjectOf(f File, pos Position) string {
	if f.Desc == nil || pos.Line == 0 {
		return ""
	}
	locs := f.Desc.SourceLocations()
	var best protoreflect.SourcePath
	subject := ""
	for i := 0; i < locs.Len(); i++ {
		l := locs.Get(i)
		if l.StartLine+1 != pos.Line || l.StartColumn+1 != pos.Col {
			continue
		}
		if len(l.Path) <= len(best) {
			continue
		}
		d := f.DescriptorAt(l.Path)
		if d == nil {
			continue
		}
		best, subject = l.Path, string(d.FullName())
	}
	return subject
}
