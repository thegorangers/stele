package breaking

import (
	"fmt"
	"strings"

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
		if r.Severity != "" && r.Severity != rule.SeverityNameError {
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q is %s: %s", r.ID, r.Severity, r.Reason))
		}
		// An ignore list is an off switch over the paths it names, same as
		// severity: off is over every path, and it gets the same treatment:
		// named out loud on every run, whether or not it ever silences
		// anything, rather than left for a reader to find by going and
		// looking at the manifest. Printed even when severity was already
		// reported above — the two are independent things a rule config can
		// say, and a rule can carry both at once.
		if len(r.Ignore) > 0 {
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q ignores %s: %s", r.ID, strings.Join(r.Ignore, ", "), r.Reason))
		}
	}
	return notes
}

// AuditLowered is LoweredNotes' superset for `stele breaking --audit`: a
// rule counts as lowered when its severity is below error, exactly as
// LoweredNotes already reports, or — even while it stands at error — when
// its ignore list covers every path where that rule actually fired this
// run, so that every finding it would otherwise have produced was
// silenced. Both are the same fact from a reader's point of view: this
// repository has, in effect, switched the rule off.
//
// This asks a different, narrower question than "does the ignore list
// cover every path this repository owns" — a manifest naming one package
// out of a hundred is not lowered over the other ninety-nine, and asking
// about all of owned would call it lowered only in the rare case a
// repository is nearly all ignored. The question that matters is whether
// the paths this rule would have complained about are exactly the paths
// silenced, and that is a question only the findings themselves can
// answer: a path with no finding for this rule was never going to fire
// regardless of the ignore list, and its absence from the ignore list
// proves nothing about whether the mechanism did anything.
//
// An ignore list that covers only some of a rule's hits is named too, on
// its own line, distinct from "lowered": an ordinary run (LoweredNotes)
// announces every ignore list with its reason regardless of how much of
// the rule it silences, and an audit that said less than the run it is
// auditing would be exactly the gap this function exists to close. Naming
// a partial ignore is not the same claim as "lowered" — the rule still
// produced findings this run, on the paths it was not told to ignore — so
// it gets its own wording rather than being folded into "lowered" or
// dropped.
//
// findings is the full set Classify/ClassifyClosure produced, before
// ApplySeverity has dropped anything an ignore list or severity: off
// removes — the run always has this in hand by the time --audit's full
// comparison has happened, unlike LoweredNotes, which is printed before a
// comparison is even known to be possible and so cannot ask this question
// at all.
func AuditLowered(cfg *config.Breaking, findings []Finding) []string {
	if cfg == nil {
		return nil
	}
	var notes []string
	for _, r := range cfg.Rules {
		switch {
		case r.Severity != "" && r.Severity != rule.SeverityNameError:
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q is %s: %s", r.ID, r.Severity, r.Reason))
		case len(r.Ignore) > 0 && ignoresEveryHit(r.Ignore, r.ID, findings):
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q is lowered: its ignore list covers every path where it fired this run",
				r.ID))
		case len(r.Ignore) > 0:
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the rule %q ignores %s: %s",
				r.ID, strings.Join(r.Ignore, ", "), r.Reason))
		}
	}
	return notes
}

// ignoresEveryHit reports whether ignore covers the path of every finding
// of ruleID among findings. No finding of ruleID at all — the rule never
// had anything to say this run — is deliberately not "everything": a rule
// cannot be said to have silenced itself over a run it had nothing to
// report in the first place.
func ignoresEveryHit(ignore []string, ruleID string, findings []Finding) bool {
	hit := false
	for _, f := range findings {
		if f.Rule != ruleID {
			continue
		}
		hit = true
		if !lint.Ignores(ignore, f.Path) {
			return false
		}
	}
	return hit
}
