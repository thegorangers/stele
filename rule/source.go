package rule

// Getting from a source position back to what the author wrote.
//
// A rule that judges declarations is handed descriptors and needs no help.
// A rule that judges what is written *around* them — comments, and everything
// a comment is evidence of — starts from the other end: source information is
// indexed by a path of field numbers into the file's descriptor proto, and the
// only way back is to walk that path. The walk is a dozen field numbers and
// four kinds of container, it is invisible when it is wrong, and every rule
// that needed it would write it again, each covering a different subset of the
// kinds. So it is here, once, in the package the rules are written against.

import (
	"iter"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Field numbers of the declarations a source path can name, in the descriptor
// protos source information is indexed against.
//
// They are constants of this package because a rule that had to spell them out
// would be spelling out the internals of descriptor.proto, and the four rules
// that did it would each stop at a different depth.
const (
	// In FileDescriptorProto.
	fileMessage   = 4
	fileEnum      = 5
	fileService   = 6
	fileExtension = 7
	// In DescriptorProto.
	messageField     = 2
	messageNested    = 3
	messageEnum      = 4
	messageExtension = 6
	messageOneof     = 8
	// In EnumDescriptorProto.
	enumValue = 2
	// In ServiceDescriptorProto.
	serviceMethod = 2
)

// DescriptorAt returns the declaration the source path names, or nil when the
// path names something that is not a declaration.
//
// A path reaches deeper than declarations — into options, into a field's type,
// into the package or syntax statement — and for all of those the answer is
// nil rather than the enclosing declaration. Answering with the enclosing one
// would let a rule report a finding against a subject the author did not
// write at that position, and the caller cannot tell the two apart afterwards.
// A rule that wants a name for such a position has the file, which is what
// Comment.Subject falls back to.
func (f File) DescriptorAt(p protoreflect.SourcePath) protoreflect.Descriptor {
	if f.Desc == nil || len(p) == 0 || len(p)%2 != 0 {
		return nil
	}
	var cur protoreflect.Descriptor = f.Desc
	for len(p) > 0 {
		next := child(cur, p[0], int(p[1]))
		if next == nil {
			return nil
		}
		cur, p = next, p[2:]
	}
	return cur
}

// child returns the index'th member of the list field holds on d, or nil when
// d has no such field or the index is past the end.
func child(d protoreflect.Descriptor, field int32, index int) protoreflect.Descriptor {
	if index < 0 {
		return nil
	}
	switch d := d.(type) {
	case protoreflect.FileDescriptor:
		switch field {
		case fileMessage:
			return at(d.Messages(), index)
		case fileEnum:
			return at(d.Enums(), index)
		case fileService:
			return at(d.Services(), index)
		case fileExtension:
			return at(d.Extensions(), index)
		}
	case protoreflect.MessageDescriptor:
		switch field {
		case messageField:
			return at(d.Fields(), index)
		case messageNested:
			return at(d.Messages(), index)
		case messageEnum:
			return at(d.Enums(), index)
		case messageExtension:
			return at(d.Extensions(), index)
		case messageOneof:
			return at(d.Oneofs(), index)
		}
	case protoreflect.EnumDescriptor:
		if field == enumValue {
			return at(d.Values(), index)
		}
	case protoreflect.ServiceDescriptor:
		if field == serviceMethod {
			return at(d.Methods(), index)
		}
	}
	return nil
}

// list is the shape every descriptor list shares. It is written out because
// protoreflect's lists are distinct types with no common interface.
type list[T protoreflect.Descriptor] interface {
	Len() int
	Get(int) T
}

func at[T protoreflect.Descriptor](l list[T], i int) protoreflect.Descriptor {
	if i >= l.Len() {
		return nil
	}
	return l.Get(i)
}

// Comment is one comment in a file, with what it is attached to.
//
// It exists because a comment is the one thing in a proto file that a rule can
// read and a descriptor cannot hold: comments are generated into every
// consumer's code, so what they say is part of the contract, and a rule about
// them has to get from the comment back to the thing it is about.
type Comment struct {
	// Path is the source path of the declaration the comment is attached to,
	// as source information indexes it.
	Path protoreflect.SourcePath
	// Desc is the declaration the comment is on, or nil when the comment is
	// on something that is not one — the package or syntax statement, an
	// option, a reserved range.
	Desc protoreflect.Descriptor
	// Subject names what the comment is on, for a message a reader reads: the
	// full name of Desc, or the file's import path when there is no Desc.
	// It is supplied rather than left to the rule because every rule reporting
	// a comment needs it and the fallback is the part that gets forgotten.
	Subject string
	// Leading is the comment block immediately above the declaration, with no
	// blank line between. Trailing is the one after it on the same line.
	Leading, Trailing string
	// Detached are the comment blocks above the declaration that are
	// separated from it by a blank line. They are the author's notes about
	// the region rather than about the declaration, which is why protoc keeps
	// them apart and why this does too.
	Detached []string
	// Pos is where the declaration is — the position a finding about it
	// takes. See Finding.Pos for why a finding points at the declaration
	// rather than at the comment.
	Pos Position
	// LeadingPos is where the leading comment block begins: its first line,
	// not its last, and not the declaration's. It is the zero Position when
	// there is no leading comment.
	//
	// It is here because the decision recorded on Finding.Pos — a finding
	// points at the declaration — is only defensible if the rule that wants
	// the other position can have it. A rule that had to compute this itself
	// would compute it from the number of lines in the comment text, which is
	// exactly what happens below, and four rules doing it would get the blank
	// `//` line and the detached blocks differently wrong.
	//
	// Col is the declaration's column. Source information records where a
	// declaration starts and not where its comment does, so there is nothing
	// truer to report; the line is the part a reader navigates by.
	LeadingPos Position
}

// Comments iterates every comment in the file, in the order source information
// records them, which is the order they appear in the file.
//
// A declaration with a leading and a trailing comment is yielded once, with
// both: the subject is the declaration, and a rule that reported it twice
// would report one thing as two.
func (f File) Comments() iter.Seq[Comment] {
	return func(yield func(Comment) bool) {
		if f.Desc == nil {
			return
		}
		locs := f.Desc.SourceLocations()
		for i := 0; i < locs.Len(); i++ {
			loc := locs.Get(i)
			if loc.LeadingComments == "" && loc.TrailingComments == "" && len(loc.LeadingDetachedComments) == 0 {
				continue
			}
			d := f.DescriptorAt(loc.Path)
			subject := string(f.Desc.Path())
			if d != nil {
				subject = string(d.FullName())
			}
			pos := Position{Line: loc.StartLine + 1, Col: loc.StartColumn + 1}
			c := Comment{
				Path:     loc.Path,
				Desc:     d,
				Subject:  subject,
				Leading:  loc.LeadingComments,
				Trailing: loc.TrailingComments,
				Detached: loc.LeadingDetachedComments,
				Pos:      pos,
			}
			if n := strings.Count(loc.LeadingComments, "\n"); n > 0 {
				// Every line of a leading comment block ends in a newline, and
				// the block is immediately above the declaration with no blank
				// line between — that is what makes it leading rather than
				// detached. So the block begins that many lines up.
				c.LeadingPos = Position{Line: pos.Line - n, Col: pos.Col}
			}
			if !yield(c) {
				return
			}
		}
	}
}
