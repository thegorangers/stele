package breaking

import (
	"fmt"
	"strings"

	"github.com/thegorangers/stele/rule"
)

// Outcome says what kind of run produced a report. There are four of them,
// and three are not a clean comparison: an empty report is indistinguishable
// from a clean one on its own, so a run that never compared anything must
// never render the way a run that compared and found nothing does.
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
}

// blindZone lists what this engine cannot see, printed in every report's
// footer. A report saying "no breaking changes" without this reads as "safe
// to change anything", and these three pass green by design.
const blindZone = `blind zone: this engine does not check json_name renames, ` +
	`int32 widening to int64 under protojson, or google.api.http changes`

// reportOnlyNotice says the run cannot fail a build, so a reader does not
// have to infer that from the exit status.
const reportOnlyNotice = "report-only: this run always exits zero"

// Render renders findings and the outcome of the run that produced them as
// human-facing text.
func Render(findings []Finding, info Info) string {
	var b strings.Builder

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
	b.WriteString(reportOnlyNotice)
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
	if f.Closure {
		message = fmt.Sprintf("[dependency: %s] (category: %s) %s", f.Path, categoryName(f.Category), message)
	} else {
		message = fmt.Sprintf("(category: %s) %s", categoryName(f.Category), message)
	}

	rf := rule.Finding{
		Rule:     f.Rule,
		Severity: rule.SeverityError,
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
