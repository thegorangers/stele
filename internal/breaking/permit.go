package breaking

import (
	"fmt"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/rule"
)

// Permit applies the working manifest's allow[] list against findings,
// removing every finding one specific permission approves and reporting
// every permission that approved nothing.
//
// Permit runs after ApplySeverity, not before. Severity answers "what does
// this repository say about the rule"; a permission answers "what does this
// repository say about one specific change against that rule". The second
// question does not need the first one's answer to be computed, but the
// order matters for what a permission naming an off rule means: run after
// ApplySeverity, that rule's findings are already gone (dropped, not kept
// at SeverityOff — see ApplySeverity's doc comment), so a permission for it
// never has anything left to match and is reported stale. That is the
// correct reading: a permission is stale when it is not doing anything to
// approve, and one naming a rule the manifest has already silenced for
// every change is not doing anything either. It is not an error — nothing
// about turning a rule off makes a leftover permission for it dangerous —
// but it is exactly what "matched nothing" means, and hiding it by running
// permissions first (and so matching against findings the rule would have
// produced at error) would let a stale permission for an off rule pass as
// live, which is the one case this ordering exists to catch.
func Permit(findings []Finding, cfg *config.Breaking) (kept []Finding, stale []config.Permission) {
	if cfg == nil || len(cfg.Allow) == 0 {
		return findings, nil
	}

	matched := matchedAllow(findings, cfg)
	kept = make([]Finding, 0, len(findings))

	for _, f := range findings {
		if indexOfMatch(cfg.Allow, f) == -1 {
			kept = append(kept, f)
		}
		// A matched finding leaves the run entirely: not reported, not
		// counted.
	}

	for i, p := range cfg.Allow {
		if !matched[i] {
			stale = append(stale, p)
		}
	}

	return kept, stale
}

// matchedAllow reports, for each entry of cfg.Allow in order, whether some
// finding matched it. It is the computation Permit's stale list and
// StaleAllowIndices both need, factored out because --audit and --prune
// need the indices themselves, not copies of the values: --prune has to
// find the line in the manifest a stale entry came from, and a copy
// carries nothing to find it by.
func matchedAllow(findings []Finding, cfg *config.Breaking) []bool {
	matched := make([]bool, len(cfg.Allow))
	for _, f := range findings {
		if i := indexOfMatch(cfg.Allow, f); i != -1 {
			matched[i] = true
		}
	}
	return matched
}

// StaleAllowIndices returns the indices into cfg.Allow that matched no
// finding — the same test Permit's stale return applies, restated for
// --audit and --prune, which need to name or delete a specific manifest
// entry rather than compare copied fields. A caller distinguishing stale
// from dormant still has to consult cfg.Rules, the way PermitNotes and
// IsDormant do.
func StaleAllowIndices(findings []Finding, cfg *config.Breaking) []int {
	if cfg == nil || len(cfg.Allow) == 0 {
		return nil
	}
	matched := matchedAllow(findings, cfg)
	var idx []int
	for i, m := range matched {
		if !m {
			idx = append(idx, i)
		}
	}
	return idx
}

// IsDormant reports whether p is dormant rather than spent: its rule was
// set to off in this same manifest, so "matched nothing" says nothing
// about whether the change it approved is still there. See PermitNotes for
// the full distinction.
func IsDormant(cfg *config.Breaking, p config.Permission) bool {
	if cfg == nil {
		return false
	}
	for _, r := range cfg.Rules {
		if r.ID == p.Rule {
			return r.Severity == rule.SeverityNameOff
		}
	}
	return false
}

// indexOfMatch returns the index of the first permission in allow that
// matches f, or -1 if none does.
//
// A permission matches on (rule, subject) and, only where the registry says
// the named rule carries a discriminant, on change too. A permission for a
// rule with no discriminant never compares change: config.ValidateConfig
// has already refused such a permission a change field, so there is nothing
// to compare, and f.Change is expected to be empty for the same rules
// (Finding's own doc comment). Comparing change unconditionally would make
// a permission for a discriminant-carrying rule match by accident whenever
// both sides happened to be empty; gating the comparison on HasDiscriminant
// is what keeps "differs" and "carries none" from collapsing into each
// other.
// PermitNotes turns the permissions Permit reported unmatched into the
// lines a report prints, distinguishing why each matched nothing: the
// remedy is opposite depending on the reason, and calling both "stale"
// would tell a reader to delete a permission they still need.
//
//   - The permission's rule stands at its default (error) or was lowered to
//     warning — a finding of that rule could exist, and none of them
//     matched. The change this permission approved is behind the base:
//     spent, and the right move is to delete it.
//   - The permission's rule was set to off in this same manifest — no
//     finding of that rule can exist at all right now, so "matched
//     nothing" says nothing about whether the change is still there.
//     Deleting this one would be wrong: raising the rule back to error
//     needs it again immediately.
//
// cfg is the same working-manifest block Permit was called with; the
// distinction costs nothing beyond a second look at cfg.Rules, which
// ApplySeverity has already parsed once for the same manifest.
func PermitNotes(cfg *config.Breaking, stale []config.Permission) []string {
	if len(stale) == 0 {
		return nil
	}
	var rules map[string]config.BreakingRule
	if cfg != nil {
		rules = make(map[string]config.BreakingRule, len(cfg.Rules))
		for _, r := range cfg.Rules {
			rules[r.ID] = r
		}
	}
	notes := make([]string, 0, len(stale))
	for _, p := range stale {
		if rc, ok := rules[p.Rule]; ok && rc.Severity == rule.SeverityNameOff {
			notes = append(notes, fmt.Sprintf(
				"stele: breaking: the permission for %s on %s is dormant: rule %q is off, not spent; "+
					"keep it, it will be needed again the moment the rule is raised back to error",
				p.Rule, p.Subject, p.Rule))
			continue
		}
		notes = append(notes, fmt.Sprintf(
			"stele: breaking: the permission for %s on %s is stale: it matched nothing; "+
				"the change it approved is behind the base, so it can be removed",
			p.Rule, p.Subject))
	}
	return notes
}

func indexOfMatch(allow []config.Permission, f Finding) int {
	for i, p := range allow {
		if p.Rule != f.Rule || p.Subject != f.Subject {
			continue
		}
		info, ok := LookupRule(p.Rule)
		if ok && info.HasDiscriminant && p.Change != f.Change {
			continue
		}
		return i
	}
	return -1
}
