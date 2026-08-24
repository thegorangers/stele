// Package rule is what a lint rule is written against.
//
// It is the one package of this repository that is not internal, and that is
// its entire reason to exist: a rule living in somebody else's repository has
// to be able to import the interface it implements, and `internal/lint` is by
// construction unimportable. The engine runs this interface and the host
// transports it; there is no second interface for external rules, because a
// second one would drift from the first and the drift would show up as
// findings that differ depending on which side of a process boundary a rule
// happened to be on.
//
// # What a rule is, and why the interface is this shape
//
// A Rule is handed one linked file and returns what it has to say about it.
// Nothing in Rule reaches the filesystem, the manifest, the configuration or
// the process, because none of those survive a process boundary. What crosses
// one is a set of descriptors and a list of findings, which is exactly what
// Check takes and returns.
//
// Two things a rule deliberately does not decide:
//
//   - Its own severity. Severity is the reader's judgement about their own
//     repository, not the rule author's about somebody else's, and it is what
//     lets a repository switch a rule on before it has fixed everything the
//     rule finds. A rule states what it requires; configuration states what
//     happens when it is not met.
//
//   - Where a finding is reported from. A rule names the descriptor; the
//     engine turns that into a rule ID, an import path and a position. A rule
//     that stamped its own ID could stamp somebody else's.
package rule

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// Severity says what a finding costs.
//
// It is a property of the configuration rather than of the rule, and there are
// three values rather than two because the middle one is the whole adoption
// story: a repository that has never linted anything has violations on day
// one, and a tool whose only settings are "off" and "red build" gets set to
// "off".
type Severity int

const (
	// SeverityError fails the run. This is what an unconfigured rule is.
	SeverityError Severity = iota
	// SeverityWarning reports the finding and does not fail the run.
	SeverityWarning
	// SeverityOff does not run the rule at all over the paths it covers.
	SeverityOff
)

// Severity spellings, as configuration writes them and as output prints them.
const (
	SeverityNameError   = "error"
	SeverityNameWarning = "warning"
	SeverityNameOff     = "off"
)

// String returns the spelling configuration uses.
func (s Severity) String() string {
	switch s {
	case SeverityWarning:
		return SeverityNameWarning
	case SeverityOff:
		return SeverityNameOff
	default:
		return SeverityNameError
	}
}

// ParseSeverity reads the spelling configuration uses.
func ParseSeverity(s string) (Severity, error) {
	switch s {
	case SeverityNameError:
		return SeverityError, nil
	case SeverityNameWarning:
		return SeverityWarning, nil
	case SeverityNameOff:
		return SeverityOff, nil
	}
	return SeverityError, fmt.Errorf("%q is not a severity; write one of %s, %s, %s",
		s, SeverityNameError, SeverityNameWarning, SeverityNameOff)
}

// Position is where in a file a finding is, in the coordinates an editor and a
// compiler both use: one-based line and column.
//
// A position names a declaration, never the comment above it. See Finding.Pos.
//
// A zero Line means the file itself is the subject and no smaller place
// applies. It is never a missing position for something that has one — source
// information is retained through compilation precisely so that this does not
// happen, and a finding without a line is one a reader has to go hunting for.
type Position struct {
	Line int
	Col  int
}

// Finding is one thing a rule has to say about one file.
//
// Message and Fix are separate fields, and both are required, because they
// answer different questions and a reader in a CI log has both. Message says
// what is true of the file. Fix says what to do about it — the standard the
// failure messages of milestone 5 were held to, and the reason a lint's output
// is read rather than skipped.
type Finding struct {
	// Rule is the ID of the rule that produced the finding. It is stamped by
	// the engine: a rule cannot claim to be another rule.
	Rule string
	// Severity is what this finding costs, resolved from configuration.
	Severity Severity
	// Path is the import path of the file. The engine stamps it.
	Path string
	// Pos is where in the file the subject is: the line and column of the
	// declaration the finding is about.
	//
	// The declaration, and not the comment above it, even for a rule whose
	// whole subject is that comment. Two reasons, and the second is the
	// binding one:
	//
	//   - It is what the reader has to change. A comment is evidence about a
	//     declaration; the edit lands on the declaration or on the lines it
	//     owns, and an editor that jumped to the comment would have jumped
	//     one line short of the thing.
	//   - One repository runs rules from several authors, and a position
	//     everybody chooses for themselves is a report whose lines mean
	//     different things on different lines. This is the kind of decision
	//     that has to be made once, by the interface, or it is made four
	//     times.
	//
	// A rule that genuinely reports the comment itself — one that objects to
	// what the comment says rather than to what it describes — takes
	// Comment.LeadingPos and says so in its message. That position is
	// supplied for exactly that case: a decision that leaves the other
	// position unreachable is not a decision, it is a limitation.
	Pos Position
	// Message states what is wrong.
	Message string
	// Fix states what to do about it.
	Fix string
}

// String renders a finding the way a compiler renders a diagnostic, because
// that is the form editors and CI log scrapers already parse:
//
//	path:line:col: severity: rule: message
//
// The fix is a second, indented line: it is prose, and putting it on the first
// line would push the position off the left of anything that truncates.
func (f Finding) String() string {
	var b strings.Builder
	b.WriteString(f.Path)
	if f.Pos.Line > 0 {
		fmt.Fprintf(&b, ":%d:%d", f.Pos.Line, f.Pos.Col)
	}
	fmt.Fprintf(&b, ": %s: %s: %s", f.Severity, f.Rule, f.Message)
	if f.Fix != "" {
		b.WriteString("\n    ")
		b.WriteString(f.Fix)
	}
	return b.String()
}

// File is what a rule is given: one linked file, and the means to locate a
// descriptor inside it.
//
// It is a struct rather than the descriptor itself so that what a rule is
// handed can grow without every out-of-tree rule having to be rewritten. Pos,
// DescriptorAt and Comments are here rather than left to each rule because
// reaching source locations correctly is fiddly and getting it wrong is
// invisible until somebody reads a finding that points at the wrong line.
//
// Positions from any of them name the declaration, which is what Finding.Pos
// means; Comment.LeadingPos is the one exception and is documented as such.
type File struct {
	// Desc is the linked file. Its imports are reachable through it.
	Desc protoreflect.FileDescriptor
}

// Pos returns where d is declared. A descriptor the file has no source
// information for yields the zero Position, which renders as a finding about
// the file as a whole rather than about a line that does not exist.
func (f File) Pos(d protoreflect.Descriptor) Position {
	if f.Desc == nil || d == nil {
		return Position{}
	}
	loc := f.Desc.SourceLocations().ByDescriptor(d)
	if loc.StartLine == 0 && loc.StartColumn == 0 && loc.Path == nil {
		return Position{}
	}
	return Position{Line: loc.StartLine + 1, Col: loc.StartColumn + 1}
}

// fileFieldPackage and fileFieldSyntax are the FileDescriptorProto field
// numbers of the two file-level declarations a rule can point at. Source
// information is indexed by the path of field numbers that reaches a
// declaration, and for a top-level field of the file that path is one number.
const (
	fileFieldPackage = 2
	fileFieldSyntax  = 12
)

// PosOfPackage returns where the file's package statement is written. A file
// that declares no package has none, and the zero Position is the honest
// answer: there is no line to send a reader to.
func (f File) PosOfPackage() Position { return f.posOfPath(fileFieldPackage) }

// PosOfSyntax returns where the file's syntax statement is written, or the
// zero Position when it declares none.
func (f File) PosOfSyntax() Position { return f.posOfPath(fileFieldSyntax) }

func (f File) posOfPath(field int32) Position {
	if f.Desc == nil {
		return Position{}
	}
	loc := f.Desc.SourceLocations().ByPath(protoreflect.SourcePath{field})
	if loc.Path == nil {
		return Position{}
	}
	return Position{Line: loc.StartLine + 1, Col: loc.StartColumn + 1}
}

// Rule is one check over one file.
//
// Implementations must be pure: the same file must produce the same findings,
// in the same order, on every run. A rule that reads the clock, the
// environment or the network makes a lint run that cannot be reproduced from
// the repository, which is the failure this whole tool exists to remove.
type Rule interface {
	// ID identifies the rule wherever it is named — configuration, output,
	// somebody's ignore list. It is a public contract; see CheckID.
	ID() string
	// Description is one line stating what the rule requires, in the
	// present tense. It is what `stele lint --rules` prints, and it is how
	// somebody decides whether to switch the rule off.
	Description() string
	// Check returns what the rule has to say about f. Rule and Path on each
	// finding are ignored: the engine stamps them.
	//
	// The error is not for a file that breaks the rule — that is a finding.
	// It is for a rule that could not reach an answer at all, and it exists
	// because of the process boundary: a rule that crashed, hung or answered
	// with rubbish has no findings to return, and a signature with no way to
	// say so would report it as a rule that found nothing. Silence and
	// cleanliness must not be the same value. A rule that runs in this
	// process returns nil for it and always will.
	Check(f File) ([]Finding, error)
}

// NamespaceBuiltin is the origin part of a rule ID reserved for the rules that
// ship with this tool.
//
// The scheme is `namespace/name`. It is namespaced from the first release
// rather than from the first collision, because retrofitting a namespace is a
// rename, a rename of a rule ID silently stops matching the ignore list that
// named it, and a protection that switches itself off is worse than one that
// was never switched on.
const NamespaceBuiltin = "stele"

// Namespace is the origin part of id, or the empty string when id has none.
func Namespace(id string) string {
	ns, _, ok := strings.Cut(id, "/")
	if !ok {
		return ""
	}
	return ns
}

// CheckID reports whether id is well formed: `namespace/name`, both parts
// lower_snake_case and starting with a letter.
//
// The separator is `/` because it cannot occur in a proto identifier or in a
// severity, so an ID is never ambiguous with the thing it is written beside.
func CheckID(id string) error {
	ns, name, ok := strings.Cut(id, "/")
	if !ok {
		return fmt.Errorf("%q is not a rule ID; write it as namespace/name, such as %s/enum_value_prefix",
			id, NamespaceBuiltin)
	}
	if err := lowerSnake(ns); err != nil {
		return fmt.Errorf("%q: the namespace %q %s", id, ns, err)
	}
	if err := lowerSnake(name); err != nil {
		return fmt.Errorf("%q: the name %q %s", id, name, err)
	}
	return nil
}

func lowerSnake(s string) error {
	if s == "" {
		return fmt.Errorf("is empty")
	}
	if s[0] < 'a' || s[0] > 'z' {
		return fmt.Errorf("must start with a lower-case letter")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return fmt.Errorf("must be lower_snake_case")
		}
	}
	return nil
}
