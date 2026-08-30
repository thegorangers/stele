package breaking

import "github.com/thegorangers/stele/internal/config"

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

	matched := make([]bool, len(cfg.Allow))
	kept = make([]Finding, 0, len(findings))

	for _, f := range findings {
		i := indexOfMatch(cfg.Allow, f)
		if i == -1 {
			kept = append(kept, f)
			continue
		}
		matched[i] = true
		// The finding leaves the run entirely: not reported, not counted.
	}

	for i, p := range cfg.Allow {
		if !matched[i] {
			stale = append(stale, p)
		}
	}

	return kept, stale
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
