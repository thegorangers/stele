// Command stele-rule-example is a stele lint rule that lives outside stele.
//
// It exists to prove the host carries a rule from another repository, and it
// is a real rule rather than a stub: an unresolved TODO in a comment on a
// declaration that consumers already generate code from is a note to the
// author that has become a warning to everybody else.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/thegorangers/stele/rule"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func main() {
	if err := rule.Serve(noTODO{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// noTODO reports declarations whose leading comment is still marked TODO.
type noTODO struct{}

func (noTODO) ID() string { return "example/no_todo" }

func (noTODO) Description() string {
	return "no declaration carries an unresolved TODO in its comment"
}

func (r noTODO) Check(f rule.File) ([]rule.Finding, error) {
	var out []rule.Finding
	locs := f.Desc.SourceLocations()
	for i := 0; i < locs.Len(); i++ {
		loc := locs.Get(i)
		text := loc.LeadingComments + loc.TrailingComments
		if !strings.Contains(text, "TODO") {
			continue
		}
		// Comments attach to declarations, not to statements inside them, and
		// a location with no leading detached comment and a path of odd
		// length is a field of the enclosing message rather than a
		// declaration in its own right. Reporting the location as given is
		// the honest answer: it is where the comment is.
		out = append(out, rule.Finding{
			Pos:     rule.Position{Line: loc.StartLine + 1, Col: loc.StartColumn + 1},
			Message: fmt.Sprintf("%s carries an unresolved TODO in its comment", subject(f, loc)),
			Fix: "resolve it before the contract ships, or move the note out of the comment: " +
				"a comment is generated into every consumer's code, and a TODO there is a note to " +
				"the author that reaches everybody who depends on this file",
		})
	}
	return out, nil
}

// subject names what the comment is on, falling back to the file when the
// location is not one a descriptor can be found for.
func subject(f rule.File, loc protoreflect.SourceLocation) string {
	if d := f.Desc.SourceLocations().ByPath(loc.Path); d.Path != nil {
		if desc := descriptorAt(f.Desc, loc.Path); desc != nil {
			return string(desc.FullName())
		}
	}
	return string(f.Desc.Path())
}

// descriptorAt walks the source path to the declaration it names, for the two
// kinds a rule this small needs: top-level messages and their fields.
func descriptorAt(fd protoreflect.FileDescriptor, path protoreflect.SourcePath) protoreflect.Descriptor {
	const fileFieldMessage = 4
	const messageFieldField = 2
	if len(path) < 2 || path[0] != fileFieldMessage {
		return nil
	}
	if int(path[1]) >= fd.Messages().Len() {
		return nil
	}
	msg := fd.Messages().Get(int(path[1]))
	if len(path) == 2 {
		return msg
	}
	if len(path) == 4 && path[2] == messageFieldField && int(path[3]) < msg.Fields().Len() {
		return msg.Fields().Get(int(path[3]))
	}
	return msg
}
