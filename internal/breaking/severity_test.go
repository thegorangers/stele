package breaking_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/breaking"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/rule"
)

func removedFinding(path string) breaking.Finding {
	return breaking.Finding{
		Rule: breaking.RuleFieldRemoved, Category: breaking.Source,
		Path: path, Subject: "example.orders.v1.Order.eta",
		Message: "field eta was removed",
	}
}

// Absent block: every rule is error.
func TestApplySeverity_DefaultIsError(t *testing.T) {
	out := breaking.ApplySeverity([]breaking.Finding{removedFinding("api/orders/v1/order.proto")}, nil)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if out[0].Severity != rule.SeverityError {
		t.Errorf("Severity = %v, want error", out[0].Severity)
	}
}

// A rule not named in the block is still error.
func TestApplySeverity_UnnamedRuleIsError(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: "break/message_removed", Severity: "off", Reason: "not tracked yet"},
	}}
	out := breaking.ApplySeverity([]breaking.Finding{removedFinding("api/orders/v1/order.proto")}, cfg)
	if len(out) != 1 || out[0].Severity != rule.SeverityError {
		t.Fatalf("out = %+v, want one error-severity finding: configuring rule A must not change rule B", out)
	}
}

// warning: still reported, marked, and counted separately.
func TestApplySeverity_Warning(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Severity: "warning", Reason: "still churning"},
	}}
	out := breaking.ApplySeverity([]breaking.Finding{removedFinding("api/orders/v1/order.proto")}, cfg)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if out[0].Severity != rule.SeverityWarning {
		t.Errorf("Severity = %v, want warning", out[0].Severity)
	}
}

// off: no finding at all, not a suppressed one.
func TestApplySeverity_OffProducesNoFinding(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Severity: "off", Reason: "known and accepted"},
	}}
	out := breaking.ApplySeverity([]breaking.Finding{removedFinding("api/orders/v1/order.proto")}, cfg)
	if len(out) != 0 {
		t.Fatalf("len(out) = %d, want 0: off must produce no finding, not a suppressed one", len(out))
	}
}

// A rule's ignore excludes that rule alone, on that path only.
func TestApplySeverity_IgnoreScopedToRuleAndPath(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Ignore: []string{"api/legacy/v1"}},
	}}
	findings := []breaking.Finding{
		removedFinding("api/legacy/v1/order.proto"),
		removedFinding("api/orders/v1/order.proto"),
	}
	out := breaking.ApplySeverity(findings, cfg)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1: ignore covers one path only", len(out))
	}
	if out[0].Path != "api/orders/v1/order.proto" {
		t.Errorf("Path = %q, want the non-ignored path", out[0].Path)
	}
}

// negative: configuring rule A does not change rule B's severity.
func TestApplySeverity_ConfiguringOneRuleDoesNotAffectAnother(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: "break/message_removed", Severity: "off", Reason: "not tracked"},
	}}
	out := breaking.ApplySeverity([]breaking.Finding{removedFinding("api/orders/v1/order.proto")}, cfg)
	if len(out) != 1 || out[0].Severity != rule.SeverityError {
		t.Fatalf("out = %+v, want break/field_removed untouched at error", out)
	}
}

// The report names every lowered rule with its reason, on every run.
func TestRender_NamesLoweredRules(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Severity: "warning", Reason: "still churning while the schema settles"},
	}}
	out := breaking.Render(nil, breaking.Info{
		Outcome: breaking.Compared, Previous: "abc1234",
		Notes: breaking.LoweredNotes(cfg),
	})
	if !strings.Contains(out, breaking.RuleFieldRemoved) {
		t.Errorf("report does not name the lowered rule:\n%s", out)
	}
	if !strings.Contains(out, "still churning while the schema settles") {
		t.Errorf("report does not carry the reason:\n%s", out)
	}
}

// A run with nothing lowered says nothing extra.
func TestRender_NoLoweredRulesSaysNothingExtra(t *testing.T) {
	out := breaking.Render(nil, breaking.Info{
		Outcome: breaking.Compared, Previous: "abc1234",
		Notes: breaking.LoweredNotes(nil),
	})
	if strings.Contains(out, "severity") {
		t.Errorf("a run with nothing lowered should say nothing extra about severity:\n%s", out)
	}
}

// negative, pinning Plan A: the command still exits zero at every severity.
// This is exercised at the command level too; it is asserted here directly
// against ApplySeverity's output feeding Render, which is the shape the
// command uses.
func TestApplySeverity_DoesNotChangeExitStatusContract(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Severity: "error"},
	}}
	out := breaking.ApplySeverity([]breaking.Finding{removedFinding("api/orders/v1/order.proto")}, cfg)
	if len(out) != 1 || out[0].Severity != rule.SeverityError {
		t.Fatalf("out = %+v", out)
	}
	// ApplySeverity itself carries no notion of exit status: it is the
	// command (runBreaking) that decides never to fail on findings, and
	// that is asserted in cmd/stele's TestBreakingErrorSeverityStillExitsZero.
}

// TestAuditLowered_IgnoreCoveringEveryHitIsLowered is AuditLowered's
// positive case: a rule standing at error whose ignore list happens to
// cover every path where it fired this run is, in every sense a reader
// cares about, switched off — and --audit must say so even though severity
// still reads "error".
func TestAuditLowered_IgnoreCoveringEveryHitIsLowered(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Ignore: []string{"api/legacy/v1"}, Reason: "pre-release, no consumers"},
	}}
	findings := []breaking.Finding{removedFinding("api/legacy/v1/order.proto")}
	notes := breaking.AuditLowered(cfg, findings)
	found := false
	for _, n := range notes {
		if strings.Contains(n, breaking.RuleFieldRemoved) && strings.Contains(n, "lowered") {
			found = true
		}
	}
	if !found {
		t.Errorf("a rule whose ignore list covers every path it fired on must be reported lowered: %v", notes)
	}
}

// TestAuditLowered_RuleWithNoHitAtAllIsNotLowered is the carve-out
// ignoresEveryHit's doc comment describes and the mutant this task names
// specifically inverts: a rule that produced no finding at all this run —
// its ignore list never had anything to prove itself against — must not be
// reported as having "switched everything off". Before this test existed,
// flipping ignoresEveryHit's initial hit value from false to true made
// every idle, ignored rule announce that its ignore list covers every path
// where it fired, for a rule that fired nowhere, and the suite stayed
// green. (The rule's ignore list is still named on its own, unconditional,
// non-"lowered" line — see TestAuditLowered_PartialIgnoreIsNamedButNotLowered
// — so this test asserts the specific wording is absent, not that the rule
// goes unmentioned.)
func TestAuditLowered_RuleWithNoHitAtAllIsNotLowered(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Ignore: []string{"api/legacy/v1"}, Reason: "pre-release, no consumers"},
	}}
	// No findings at all this run: the rule never fired anywhere.
	notes := breaking.AuditLowered(cfg, nil)
	for _, n := range notes {
		if strings.Contains(n, breaking.RuleFieldRemoved) && strings.Contains(n, "is lowered") {
			t.Errorf("a rule with no finding at all this run must not be reported lowered: %v", notes)
		}
	}
}

// TestAuditLowered_PartialIgnoreIsNamedButNotLowered closes the first
// coherence gap the re-review raised: an ordinary run (LoweredNotes)
// announces every ignore list with its reason, whatever fraction of the
// rule it silences; --audit, before this, said nothing at all about an
// ignore list that covered only some of a rule's hits, making the report
// whose job is "what has this repository switched off" less informative
// than the ordinary one. AuditLowered now names a partial ignore too, with
// its own wording distinct from "lowered": the rule still produced a
// finding this run, on the path its ignore list does not cover.
func TestAuditLowered_PartialIgnoreIsNamedButNotLowered(t *testing.T) {
	cfg := &config.Breaking{Rules: []config.BreakingRule{
		{ID: breaking.RuleFieldRemoved, Ignore: []string{"api/legacy/v1"}, Reason: "pre-release, no consumers"},
	}}
	findings := []breaking.Finding{
		removedFinding("api/legacy/v1/order.proto"), // covered by the ignore list
		removedFinding("api/orders/v1/order.proto"), // not covered: the rule still fired here
	}
	notes := breaking.AuditLowered(cfg, findings)
	var named, calledLowered bool
	for _, n := range notes {
		if strings.Contains(n, breaking.RuleFieldRemoved) {
			named = true
			if strings.Contains(n, "is lowered") {
				calledLowered = true
			}
		}
	}
	if !named {
		t.Errorf("a rule with a partial ignore list must still be named in the audit, on the same terms an ordinary run names it: %v", notes)
	}
	if calledLowered {
		t.Errorf("a partial ignore silences some findings, not all: it must not be worded as lowered: %v", notes)
	}
}
