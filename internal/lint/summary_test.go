package lint_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
)

// finding builds one for the renderer to lay out. The renderer is what is
// under test here, not the rules, so the findings are made rather than found.
func finding(ruleID string, sev lint.Severity, line int) lint.Finding {
	return lint.Finding{
		Rule: ruleID, Severity: sev, Path: "example/v1/a.proto",
		Pos: lint.Position{Line: line, Col: 1}, Message: "something", Fix: "do something",
	}
}

func report(fs ...lint.Finding) *lint.Report {
	r := &lint.Report{}
	for _, f := range fs {
		r.Findings = append(r.Findings, f)
		if f.Severity == lint.SeverityWarning {
			r.Warnings++
		} else {
			r.Errors++
		}
	}
	r.Files, r.Rules = 1, 1
	return r
}

func write(t *testing.T, r *lint.Report) string {
	t.Helper()
	var b strings.Builder
	r.Write(&b)
	return b.String()
}

// TestManyWarningsFromOneRuleAreRolledUp is the whole reason the AIP rules can
// be on by default. The measured fleet breaks one of them 111 times; 111 lines
// is not a signal, it is a wall a reader learns to scroll past, and a rule
// whose output is scrolled past is a rule that is off in every way but the
// configuration.
func TestManyWarningsFromOneRuleAreRolledUp(t *testing.T) {
	var fs []lint.Finding
	for i := 1; i <= 40; i++ {
		fs = append(fs, finding("aip/142_timestamp_field_time_suffix", lint.SeverityWarning, i))
	}
	out := write(t, report(fs...))
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("want one line for the rolled-up rule, got %d:\n%s", n, out)
	}
	for _, want := range []string{"aip/142_timestamp_field_time_suffix", "40", "1 file",
		"stele lint --rule aip/142_timestamp_field_time_suffix"} {
		if !strings.Contains(out, want) {
			t.Errorf("the roll-up must carry %q, so that a reader knows how many there are and how to see "+
				"them:\n%s", want, out)
		}
	}
}

// TestFewWarningsAreNotRolledUp holds the threshold. A rule that fires twice
// costs two lines, and sending the reader to a second command for two lines is
// a round trip to save nothing.
func TestFewWarningsAreNotRolledUp(t *testing.T) {
	out := write(t, report(
		finding("aip/135_delete_returns_empty", lint.SeverityWarning, 3),
		finding("aip/135_delete_returns_empty", lint.SeverityWarning, 7),
	))
	if strings.Contains(out, "--rule") {
		t.Errorf("two findings must be printed, not summarised:\n%s", out)
	}
	if n := strings.Count(out, "example/v1/a.proto"); n != 2 {
		t.Errorf("want both findings printed, got %d:\n%s", n, out)
	}
}

// TestErrorsAreNeverRolledUp is the line the roll-up must not cross. A warning
// is information a reader may act on later, and a count is enough to decide
// with. An error is the build failing now, and a failure that will not say
// what it was is not a failure anybody can fix.
func TestErrorsAreNeverRolledUp(t *testing.T) {
	var fs []lint.Finding
	for i := 1; i <= 40; i++ {
		fs = append(fs, finding("stele/enum_value_prefix", lint.SeverityError, i))
	}
	out := write(t, report(fs...))
	if strings.Contains(out, "--rule") {
		t.Errorf("errors must never be rolled up:\n%s", out)
	}
	if n := strings.Count(out, "example/v1/a.proto"); n != 40 {
		t.Errorf("want all 40 errors printed, got %d", n)
	}
}

// TestAskingForOneRulePrintsItsFindings is the other half of the roll-up: the
// detail has to be one command away, and the roll-up line is what names it.
func TestAskingForOneRulePrintsItsFindings(t *testing.T) {
	var fs []lint.Finding
	for i := 1; i <= 40; i++ {
		fs = append(fs, finding("aip/142_timestamp_field_time_suffix", lint.SeverityWarning, i))
	}
	r := report(fs...)
	r.Detail = []string{"aip/142_timestamp_field_time_suffix"}
	out := write(t, r)
	if n := strings.Count(out, "example/v1/a.proto"); n != 40 {
		t.Errorf("want all 40 findings printed, got %d", n)
	}
	if strings.Contains(out, "--rule") {
		t.Errorf("a rule asked for by name must not also be summarised:\n%s", out)
	}
}

// TestTheRollUpIsNotNamespaceSpecific. The summarising is decided by what a
// finding costs and how many there are, not by who wrote the rule. A stele
// rule a repository has lowered to warning while it fixes 200 findings gets
// the same treatment, and an aip rule a repository has raised to error prints
// in full — which is what raising it asked for.
func TestTheRollUpIsNotNamespaceSpecific(t *testing.T) {
	var lowered, raised []lint.Finding
	for i := 1; i <= 40; i++ {
		lowered = append(lowered, finding("stele/enum_value_prefix", lint.SeverityWarning, i))
		raised = append(raised, finding("aip/142_timestamp_field_time_suffix", lint.SeverityError, i))
	}
	if out := write(t, report(lowered...)); !strings.Contains(out, "--rule stele/enum_value_prefix") {
		t.Errorf("a stele rule lowered to warning must be rolled up too:\n%s", out)
	}
	if out := write(t, report(raised...)); strings.Contains(out, "--rule") {
		t.Errorf("an aip rule raised to error must print in full:\n%s", out)
	}
}
