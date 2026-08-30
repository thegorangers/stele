package breaking

import (
	"fmt"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/rule"
)

// ApplySeverity resolves every finding's Severity against the working
// tree's breaking block, and drops the findings a rule turned off.
//
// The rules, restated from config.Breaking's own doc comment because this
// is where they take effect:
//
//   - A nil block, or a rule cfg does not name, is SeverityError. Absence
//     means every rule at error over every file this repository owns, not
//     a detector that does nothing.
//   - severity: off drops the finding entirely rather than keeping it at
//     SeverityOff. An unasked question does not appear as a suppressed
//     answer, and in particular it must not inflate any count of findings
//     this run produced.
//   - A rule's ignore list excludes import paths from that rule alone: it
//     is checked per rule, against Finding.Path, and never affects any
//     other rule's findings on the same path.
//
// cfg is always the working tree's block — see runBreaking's comment on
// why the previous revision's is never consulted here.
func ApplySeverity(findings []Finding, cfg *config.Breaking) []Finding {
	var rules map[string]config.BreakingRule
	if cfg != nil {
		rules = make(map[string]config.BreakingRule, len(cfg.Rules))
		for _, r := range cfg.Rules {
			rules[r.ID] = r
		}
	}
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		rc, configured := rules[f.Rule]
		if !configured {
			f.Severity = rule.SeverityError
			out = append(out, f)
			continue
		}
		if lint.Ignores(rc.Ignore, f.Path) {
			continue
		}
		sev := rule.SeverityError
		if rc.Severity != "" {
			// The working manifest has already been through
			// config.Breaking.validate and ValidateConfig by the time
			// this runs, so a spelling ParseSeverity rejects here would
			// be a bug elsewhere, not a shape this function has to
			// explain to a user.
			s, err := rule.ParseSeverity(rc.Severity)
			if err != nil {
				panic(fmt.Sprintf("breaking: rule %s: %v (validated manifest carried an unparseable severity)", f.Rule, err))
			}
			sev = s
		}
		if sev == rule.SeverityOff {
			continue
		}
		f.Severity = sev
		out = append(out, f)
	}
	return out
}

// LoweredNotes turns every rule cfg lowers below error into the lines a
// report opens with, in the shape unpinnedNotes uses for an unpinned lint
// plugin (internal/lint/run.go): a repository may choose to protect
// nothing, but it may not do so quietly, and this is printed on every run
// regardless of whether the lowered rule ever fires.
func LoweredNotes(cfg *config.Breaking) []string {
	if cfg == nil {
		return nil
	}
	var notes []string
	for _, r := range cfg.Rules {
		if r.Severity == "" || r.Severity == rule.SeverityNameError {
			continue
		}
		notes = append(notes, fmt.Sprintf(
			"stele: breaking: the rule %q is %s: %s", r.ID, r.Severity, r.Reason))
	}
	return notes
}

// AuditLowered is LoweredNotes' superset for `stele breaking --audit`: a
// rule counts as lowered when its severity is below error, exactly as
// LoweredNotes already reports, or — even while it stands at error — when
// its ignore list covers every path in owned, so that no finding of it can
// ever be produced. Both are the same fact from a reader's point of view:
// this repository has, in effect, switched the rule off. An audit that a
// mechanism it does not count (ignore) can zero to nothing is worse than
// no audit at all — which is why --audit asks this question and the
// ordinary run does not: LoweredNotes prints on every run, cheaply, before
// a comparison is even known to be possible, and owned is not always in
// hand at that point; --audit always runs the full comparison, so owned
// always is.
//
// owned is the set of import paths the working revision owns — the paths a
// rule would check absent its own ignore list — normally Revision.Owned
// from the current side of the comparison.
func AuditLowered(cfg *config.Breaking, owned []string) []string {
	if cfg == nil {
		return nil
	}
	var notes []string
	for _, r := range cfg.Rules {
		switch {
		case r.Severity != "" && r.Severity != rule.SeverityNameError:
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q is %s: %s", r.ID, r.Severity, r.Reason))
		case len(r.Ignore) > 0 && ignoresEverything(r.Ignore, owned):
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q is lowered: its ignore list covers every path it would check",
				r.ID))
		}
	}
	return notes
}

// ignoresEverything reports whether ignore covers every one of paths. An
// empty paths — nothing owned to check — is deliberately not "everything":
// a rule cannot be said to have silenced itself over a repository that
// owns no protos for it to check in the first place.
func ignoresEverything(ignore, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !lint.Ignores(ignore, p) {
			return false
		}
	}
	return true
}
