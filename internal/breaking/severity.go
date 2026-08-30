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
