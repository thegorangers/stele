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
	for c := range f.Comments() {
		if !strings.Contains(c.Leading+c.Trailing, "TODO") {
			continue
		}
		out = append(out, rule.Finding{
			// The declaration, not the comment: a finding points at the
			// thing that has to change. A rule objecting to the comment
			// itself would take c.LeadingPos instead, and say so.
			Pos:     c.Pos,
			Message: fmt.Sprintf("%s carries an unresolved TODO in its comment", c.Subject),
			Fix: "resolve it before the contract ships, or move the note out of the comment: " +
				"a comment is generated into every consumer's code, and a TODO there is a note to " +
				"the author that reaches everybody who depends on this file",
		})
	}
	return out, nil
}
