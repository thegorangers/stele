package breaking

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
)

// The format is rule.Finding's, whose third field is the SEVERITY — not the
// category. Editors and CI log scrapers parse that shape, and the category
// goes in the message, where it is prose.
func TestRenderMatchesTheStandardDiagnosticShape(t *testing.T) {
	out := Render([]Finding{{
		Rule: "break/field_removed", Category: Source,
		Path: "api/orders/v1/order.proto", Pos: lint.Position{Line: 12, Col: 3},
		Subject: "example.orders.v1.Order.eta",
		Message: "field eta was removed; a consumer that reads it stops compiling",
	}}, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})

	const want = "api/orders/v1/order.proto:12:3: error: break/field_removed: "
	if !strings.Contains(out, want) {
		t.Fatalf("rendered as\n%s\nwant a line beginning %q", out, want)
	}
}

// Every report names the blind zone, because "no breaking changes" without
// qualification reads as "safe to change anything".
func TestFooterNamesTheBlindZone(t *testing.T) {
	out := Render(nil, Info{Previous: "abc1234", Reason: "merge-base with main"})
	for _, want := range []string{"json_name", "int32", "google.api.http"} {
		if !strings.Contains(out, want) {
			t.Errorf("the footer does not name %q", want)
		}
	}
}

// A clean run says what it compared: "no breaking changes" with nothing
// behind it is indistinguishable from a run that compared nothing.
func TestCleanRunNamesWhatItCompared(t *testing.T) {
	out := Render(nil, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})
	for _, want := range []string{"abc1234", "merge-base with main"} {
		if !strings.Contains(out, want) {
			t.Errorf("a clean report does not say %q, so it cannot be told from a run that compared nothing", want)
		}
	}
}

// The three cases that are NOT clean, each rendered as itself.
func TestNothingToCompareIsNotRenderedAsClean(t *testing.T) {
	out := Render(nil, Info{Outcome: NothingToCompare, Reason: "HEAD is the first commit"})
	if strings.Contains(out, "no breaking changes") {
		t.Error("a run that compared nothing must not read as a clean run")
	}
	if !strings.Contains(out, "first commit") {
		t.Error("the reason it compared nothing must be in the output")
	}
}

// The commonest outcome in the measured fleet — 84.7% of base-branch commits
// — and so the one most likely to be misread as a clean comparison.
func TestShortcutSkipIsNotRenderedAsClean(t *testing.T) {
	out := Render(nil, Info{Outcome: Unchanged, Previous: "abc1234"})
	if strings.Contains(out, "no breaking changes") {
		t.Error("a skipped run must not read as a compared one")
	}
	if !strings.Contains(out, "unchanged") {
		t.Error("the output must say the owned trees and the lock did not move")
	}
}

func TestOwningNoProtosIsNotRenderedAsClean(t *testing.T) {
	out := Render(nil, Info{Outcome: NoOwnedProtos})
	if strings.Contains(out, "no breaking changes") {
		t.Error("a repository that owns no protos has not been checked, and must not read as checked")
	}
}

// Plan A cannot fail a build, and the report says so rather than leaving a
// reader to infer it from an exit status.
func TestReportSaysItIsReportOnly(t *testing.T) {
	out := Render([]Finding{{Rule: "break/field_removed", Category: Source,
		Path: "api/orders/v1/order.proto", Subject: "example.orders.v1.Order.eta"}},
		Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})
	if !strings.Contains(out, "report-only") {
		t.Error("a reader must not have to infer from the exit status that nothing failed")
	}
}

// A report containing a closure finding and an owned finding shows the
// distinction between them.
func TestClosureFindingIsVisuallyDistinctFromOwned(t *testing.T) {
	out := Render([]Finding{
		{Rule: "break/field_removed", Category: Source,
			Path: "api/orders/v1/order.proto", Subject: "example.orders.v1.Order.eta",
			Message: "field eta was removed", Closure: false},
		{Rule: "break/field_removed", Category: Source,
			Path: "api/inventory/v1/item.proto", Subject: "example.inventory.v1.Item.sku",
			Message: "field sku was removed", Closure: true},
	}, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})

	ownedLine := ""
	closureLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "example.orders.v1.Order.eta") {
			ownedLine = line
		}
		if strings.Contains(line, "example.inventory.v1.Item.sku") {
			closureLine = line
		}
	}
	if ownedLine == "" || closureLine == "" {
		t.Fatalf("expected both findings to be rendered, got:\n%s", out)
	}
	if ownedLine == closureLine {
		t.Fatal("owned and closure findings render identically")
	}
	if strings.Contains(ownedLine, "dependency") {
		t.Error("owned finding should not read as coming from a dependency")
	}
}

// The footer's three blind-zone items appear even when there are no
// findings at all.
func TestFooterAppearsWithNoFindings(t *testing.T) {
	out := Render(nil, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})
	for _, want := range []string{"json_name", "int32", "google.api.http"} {
		if !strings.Contains(out, want) {
			t.Errorf("the footer does not name %q when there are no findings", want)
		}
	}
}

// Render on a Compared outcome with no findings does not contain the words
// "nothing to compare".
func TestComparedCleanDoesNotSayNothingToCompare(t *testing.T) {
	out := Render(nil, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})
	if strings.Contains(out, "nothing to compare") {
		t.Error("a clean comparison must not read as a run that compared nothing")
	}
}
