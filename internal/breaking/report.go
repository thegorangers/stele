package breaking

import (
	"fmt"
	"strings"

	"github.com/thegorangers/stele/rule"
)

// Outcome says what kind of run produced a report. There are five of them,
// and four are not a clean comparison: an empty report is indistinguishable
// from a clean one on its own, so a run that never compared anything — and,
// just as much, a run that never compared for a merge at all, such as
// --audit — must never render the way a run that compared and found nothing
// does.
type Outcome int

const (
	// Compared means the engine diffed a previous revision against the
	// current one. Findings, if any, are real.
	Compared Outcome = iota
	// NothingToCompare means there was no previous revision to diff
	// against — the first commit in a repository's history, for example.
	NothingToCompare
	// Unchanged means the owned trees and the lock did not move since the
	// previous revision, so the engine skipped the comparison rather than
	// repeat one it already knows the answer to.
	Unchanged
	// NoOwnedProtos means this repository owns no protobuf files, so
	// there was nothing for the engine to check in the first place.
	NoOwnedProtos
	// Audited means this is a report about the manifest itself — stale and
	// lowered permissions and rules — produced by --audit, not a comparison.
	// findings passed to Render is always nil for this outcome; Info.Findings
	// carries how many findings the run judged, so the report can say what
	// it did not render rather than being silently indistinguishable from a
	// clean Compared report.
	Audited
)

// Info carries what Render needs about the run beyond the findings
// themselves: which outcome it was, and enough about it to explain that
// outcome to a reader.
type Info struct {
	// Outcome is which of the four cases this run was.
	Outcome Outcome
	// Previous is the revision compared against, where one exists.
	Previous string
	// Reason explains Outcome — why there was nothing to compare, why the
	// comparison was skipped, and so on.
	Reason string
	// Notes are things about this run that qualify everything below them
	// but are not findings — at present, every rule the working manifest
	// lowered below error, named with its severity and its reason (see
	// LoweredNotes). They are printed above everything else for the same
	// reason internal/lint/run.go's unpinnedNotes are: a reader who meets
	// them afterwards has already read the findings as the whole answer,
	// and a repository that protects nothing must not be able to say so
	// quietly. Printed on every run, whether or not the lowered rule ever
	// fired.
	Notes []string
	// Findings is the number of findings the run judged, used only by the
	// Audited outcome: --audit renders no findings themselves (an audit is
	// about the manifest, not a re-statement of the comparison), but a
	// reader still needs to know it looked at something rather than
	// nothing.
	Findings int
}

// blindZone lists what this engine cannot see, printed in every report's
// footer. A report saying "no breaking changes" without this reads as "safe
// to change anything", and these three pass green by design.
const blindZone = `blind zone: this engine does not check json_name renames, ` +
	`int32 widening to int64 under protojson, or google.api.http changes`

// Render renders findings and the outcome of the run that produced them as
// human-facing text.
func Render(findings []Finding, info Info) string {
	var b strings.Builder

	for _, n := range info.Notes {
		b.WriteString(n)
		b.WriteString("\n")
	}

	switch info.Outcome {
	case NothingToCompare:
		fmt.Fprintf(&b, "nothing to compare: %s\n", info.Reason)
	case Unchanged:
		b.WriteString("unchanged: the owned trees and the lock did not move")
		if info.Previous != "" {
			fmt.Fprintf(&b, " since %s", info.Previous)
		}
		b.WriteString("; comparison skipped\n")
	case NoOwnedProtos:
		b.WriteString("no owned protos: this repository has nothing for breaking-change detection to check\n")
	case Audited:
		fmt.Fprintf(&b, "audit: this repository's manifest compared against %s", info.Previous)
		if info.Reason != "" {
			fmt.Fprintf(&b, " (%s)", info.Reason)
		}
		fmt.Fprintf(&b, "; %d finding(s) judged\n", info.Findings)
	default: // Compared
		if len(findings) == 0 {
			b.WriteString("no breaking changes")
			if info.Previous != "" {
				fmt.Fprintf(&b, " compared against %s", info.Previous)
			}
			if info.Reason != "" {
				fmt.Fprintf(&b, " (%s)", info.Reason)
			}
			b.WriteString("\n")
		} else {
			fmt.Fprintf(&b, "breaking changes compared against %s", info.Previous)
			if info.Reason != "" {
				fmt.Fprintf(&b, " (%s)", info.Reason)
			}
			b.WriteString(":\n\n")
			for i, f := range findings {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(renderFinding(f))
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("\n")
	b.WriteString(blindZone)
	b.WriteString("\n")

	return b.String()
}

// renderFinding renders one Finding in the standard diagnostic shape,
// rule.Finding's, with the category folded into the message as prose since
// the third field of that shape is severity, not category. A closure
// finding is marked distinctly and names the dependency it came from,
// because it is acted on differently from a finding in this repository's
// own contract: it cannot be fixed by editing a file here.
func renderFinding(f Finding) string {
	message := f.Message
	if f.Subject != "" {
		message = fmt.Sprintf("%s: %s", f.Subject, message)
	}
	// ClassifyClosure has already prefixed Message with "[dependency <name>]"
	// (closure.go), naming the dependency the finding came from. A second
	// "[dependency: <path>]" here would print two dependency markers, one of
	// them a bare import path rather than the name a reader can act on — so
	// this marker stays bare and lets the message carry the name.
	if f.Closure {
		message = fmt.Sprintf("[dependency] (category: %s) %s", categoryName(f.Category), message)
	} else {
		message = fmt.Sprintf("(category: %s) %s", categoryName(f.Category), message)
	}
	// Change is the discriminant a permission must spell exactly to match
	// this finding (see Permit, permit.go). It has to be visible in the
	// rendered line, not only on the struct, or a user has no way to learn
	// what to copy into an allow[] entry — a mechanism whose input is
	// invisible is not a mechanism. Folded into the message, like category
	// and subject above it, rather than a new field on rule.Finding, so
	// the rendered shape stays path:line:col: severity: rule: message.
	if f.Change != "" {
		message = fmt.Sprintf("%s (change: %s)", message, f.Change)
	}

	rf := rule.Finding{
		Rule:     f.Rule,
		Severity: f.Severity,
		Path:     f.Path,
		Pos:      f.Pos,
		Subject:  f.Subject,
		Message:  message,
		Fix:      f.Fix,
	}
	return rf.String()
}

func categoryName(c Category) string {
	if c == Wire {
		return "wire"
	}
	return "source"
}
