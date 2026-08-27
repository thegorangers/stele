package lint_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The fixture is the shape the whole mechanism is about: a rule a repository
// cannot fix today, held at error, with two known violations.
const (
	baselineBefore = `syntax = "proto3"; package example.v1;
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
  PAID = 2;
}`
	// The same two violations, moved down the file by an edit that is about
	// nothing they are about.
	baselineMoved = `syntax = "proto3"; package example.v1;

// A receipt, added later, which pushes everything below it down.
message Receipt {
  string id = 1;
}

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
  PAID = 2;
}`
	// The 112th field: one more value of the same rule in the same file.
	baselineGrown = `syntax = "proto3"; package example.v1;
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
  PAID = 2;
  REFUNDED = 3;
}`
	// One of the two fixed, and nothing else changed.
	baselineFixed = `syntax = "proto3"; package example.v1;
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  ORDER_STATUS_PLACED = 1;
  PAID = 2;
}`
)

func checkSrc(t *testing.T, src string, b *lint.Baseline) lint.Result {
	t.Helper()
	e, err := lint.New(lint.Builtin(), lint.Config{Baseline: b})
	if err != nil {
		t.Fatal(err)
	}
	return e.Check([]protoreflect.FileDescriptor{compileSource(t, dirtyPath, src)})
}

// takeBaseline is what `stele lint --update-baseline` does: run, and record
// what was found.
func takeBaseline(t *testing.T, src string) *lint.Baseline {
	t.Helper()
	b := lint.BaselineFrom(checkSrc(t, src, nil))
	if len(b.Findings) == 0 {
		t.Fatal("the fixture produced nothing to baseline")
	}
	return b
}

// TestABaselinedFindingDoesNotFailTheRun is the whole purpose. Two findings at
// error severity, both already known, and the run passes.
func TestABaselinedFindingDoesNotFailTheRun(t *testing.T) {
	base := takeBaseline(t, baselineBefore)
	res := checkSrc(t, baselineBefore, base)
	if res.Errors != 0 {
		t.Errorf("a baselined error still failed the run: %d errors", res.Errors)
	}
	if res.Warnings != 0 {
		t.Errorf("a baselined finding was counted as a warning: %d", res.Warnings)
	}
	if res.Baselined == 0 {
		t.Error("the run reported nothing as baselined; invisible debt is forgotten debt")
	}
	// Still reported. A finding nobody can see is not debt, it is a decision
	// nobody remembers making.
	found := findingsFor(res, prefixRule)
	if len(found) == 0 {
		t.Fatal("a baselined finding disappeared from the report")
	}
	for _, f := range found {
		if !f.Baselined {
			t.Errorf("%s was not marked as baselined", f.Subject)
		}
	}
}

// TestANewFindingOfABaselinedRuleFails is the half `severity: warning` does
// not give. The 112th violation is not the 111 that were already there.
func TestANewFindingOfABaselinedRuleFails(t *testing.T) {
	base := takeBaseline(t, baselineBefore)
	res := checkSrc(t, baselineGrown, base)
	if res.Errors != 1 {
		t.Fatalf("a new finding of a baselined rule must fail: got %d errors", res.Errors)
	}
	for _, f := range res.Findings {
		if f.Baselined {
			continue
		}
		if f.Subject != "example.v1.REFUNDED" {
			t.Errorf("the failing finding is about %q, not the new value", f.Subject)
		}
	}
}

// TestAMovedFindingStillMatches is the property a line number does not have,
// and the reason the identity is a declaration's name.
func TestAMovedFindingStillMatches(t *testing.T) {
	base := takeBaseline(t, baselineBefore)
	res := checkSrc(t, baselineMoved, base)
	if res.Errors != 0 {
		t.Errorf("an unrelated edit above the findings reddened the build: %d errors", res.Errors)
	}
	if len(res.Stale) != 0 {
		t.Errorf("an unrelated edit made %d entries look stale: %v", len(res.Stale), res.Stale)
	}
}

// TestAFixedFindingIsReportedAsStaleAndDoesNotFail is the decision this file
// records. Silently keeping a stale entry is how a baseline rots into a
// permanent permission; failing on one is how fixing a finding reddens the
// build of the person who fixed it, which teaches people not to fix things.
// So: reported, loudly, and never fatal.
func TestAFixedFindingIsReportedAsStaleAndDoesNotFail(t *testing.T) {
	base := takeBaseline(t, baselineBefore)
	res := checkSrc(t, baselineFixed, base)
	if res.Errors != 0 {
		t.Errorf("fixing a finding failed the run: %d errors", res.Errors)
	}
	if len(res.Stale) != 1 {
		t.Fatalf("a fixed finding must leave exactly one stale entry: got %v", res.Stale)
	}
	if res.Stale[0].Subject != "example.v1.PLACED" {
		t.Errorf("the stale entry is %q, not the fixed one", res.Stale[0].Subject)
	}
	// And it has to be visible, or the file only ever grows.
	rep := &lint.Report{Result: res}
	var b strings.Builder
	rep.Write(&b)
	if !strings.Contains(b.String(), "--update-baseline") {
		t.Errorf("a stale entry does not say how to drop it:\n%s", b.String())
	}
}

// TestABaselineNamingAnUnloadedRuleIsRefused holds it to the manifest's
// standard. A rule id is a permanent public identifier, and an entry naming
// one nothing carries is an exemption this repository believes it has.
func TestABaselineNamingAnUnloadedRuleIsRefused(t *testing.T) {
	_, err := lint.New(lint.Builtin(), lint.Config{Baseline: &lint.Baseline{
		Version:  lint.BaselineVersion,
		Findings: []lint.BaselineEntry{{Rule: "stele/no_such_rule", Path: "a/v1/a.proto"}},
	}})
	if err == nil {
		t.Fatal("a baseline naming a rule nobody loads was accepted")
	}
	if !strings.Contains(err.Error(), "stele/no_such_rule") {
		t.Errorf("the error does not name the rule: %v", err)
	}
}

// TestABaselinedWarningAndABaselinedErrorAreBothHeld records that severity
// decides what a finding costs when it is *not* baselined, and that a
// baselined finding costs nothing either way — but the summary says how much
// of the silence is bought rather than earned.
func TestABaselinedWarningAndABaselinedErrorAreBothHeld(t *testing.T) {
	base := takeBaseline(t, baselineBefore)
	e, err := lint.New(lint.Builtin(), lint.Config{
		Baseline: base,
		Rules:    map[string]lint.RuleConfig{prefixRule: {Severity: lint.SeverityWarning}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := e.Check([]protoreflect.FileDescriptor{compileSource(t, dirtyPath, baselineBefore)})
	if res.Warnings != 0 || res.Errors != 0 {
		t.Errorf("a baselined warning was counted: %d warnings, %d errors", res.Warnings, res.Errors)
	}
	if res.Baselined != 2 {
		t.Errorf("baselined count is %d, want 2", res.Baselined)
	}
	rep := &lint.Report{Result: res}
	if !strings.Contains(rep.Summary(), "held by "+lint.BaselineName) {
		t.Errorf("the summary hides what the baseline is holding: %s", rep.Summary())
	}
}

// TestARollUpNeverHidesANewFindingBehindOldOnes is the trap this whole
// mechanism would otherwise walk into. A rule with forty held findings and one
// new one must print the new one: rolling all forty-one into a count would be
// the roll-up doing precisely what the baseline was built to stop.
func TestARollUpNeverHidesANewFindingBehindOldOnes(t *testing.T) {
	const id = "aip/142_timestamp_field_time_suffix"
	rep := &lint.Report{}
	for i := 1; i <= 40; i++ {
		rep.Findings = append(rep.Findings, lint.Finding{
			Rule: id, Severity: lint.SeverityWarning, Baselined: true,
			Path: "example/v1/a.proto", Pos: lint.Position{Line: i, Col: 1},
			Subject: "example.v1.Old", Message: "old", Fix: "fix",
		})
	}
	rep.Baselined = 40
	rep.Findings = append(rep.Findings, lint.Finding{
		Rule: id, Severity: lint.SeverityWarning,
		Path: "example/v1/a.proto", Pos: lint.Position{Line: 99, Col: 1},
		Subject: "example.v1.New", Message: "the one written today", Fix: "fix",
	})
	rep.Warnings = 1
	var b strings.Builder
	rep.Write(&b)
	out := b.String()
	if !strings.Contains(out, "the one written today") {
		t.Errorf("the new finding was rolled up with the held ones:\n%s", out)
	}
	if strings.Count(out, "old") > 0 {
		t.Errorf("forty held findings were printed one to a line:\n%s", out)
	}
	if !strings.Contains(out, "40 baselined findings") {
		t.Errorf("the held findings are not accounted for:\n%s", out)
	}
}
