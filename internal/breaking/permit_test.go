package breaking_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/breaking"
	"github.com/thegorangers/stele/internal/config"
)

func typeChangedFinding(subject, change string) breaking.Finding {
	return breaking.Finding{
		Rule: breaking.RuleFieldTypeChanged, Category: breaking.Wire,
		Path: "api/orders/v1/order.proto", Subject: subject,
		Message: "field type changed", Change: change,
	}
}

// A matching permission removes the finding, and it disappears from the
// counts.
func TestPermit_MatchingPermissionRemovesFinding(t *testing.T) {
	f := typeChangedFinding("example.orders.v1.Order.total", "int32 -> int64")
	cfg := &config.Breaking{Allow: []config.Permission{
		{Rule: breaking.RuleFieldTypeChanged, Subject: "example.orders.v1.Order.total",
			Change: "int32 -> int64", Reason: "widening on purpose"},
	}}
	kept, stale := breaking.Permit([]breaking.Finding{f}, cfg)
	if len(kept) != 0 {
		t.Fatalf("kept = %+v, want none: the matched finding must leave the run entirely", kept)
	}
	if len(stale) != 0 {
		t.Fatalf("stale = %+v, want none: the permission matched", stale)
	}
}

// A permission whose change differs does not match — this is what stops
// one permission approving every type change on a subject.
func TestPermit_ChangeMismatchDoesNotMatch(t *testing.T) {
	f := typeChangedFinding("example.orders.v1.Order.total", "int32 -> string")
	cfg := &config.Breaking{Allow: []config.Permission{
		{Rule: breaking.RuleFieldTypeChanged, Subject: "example.orders.v1.Order.total",
			Change: "int32 -> int64", Reason: "widening on purpose"},
	}}
	kept, stale := breaking.Permit([]breaking.Finding{f}, cfg)
	if len(kept) != 1 {
		t.Fatalf("kept = %+v, want the finding still standing: change differs", kept)
	}
	if len(stale) != 1 {
		t.Fatalf("stale = %+v, want the permission reported stale: it matched nothing", stale)
	}
}

// A permission matching nothing is reported stale and the run still passes
// (Permit itself has no notion of exit status; this asserts what it hands
// back).
func TestPermit_UnmatchedPermissionIsStale(t *testing.T) {
	cfg := &config.Breaking{Allow: []config.Permission{
		{Rule: breaking.RuleFieldRemoved, Subject: "example.orders.v1.Order.eta",
			Reason: "field resurrected then removed again"},
	}}
	kept, stale := breaking.Permit(nil, cfg)
	if len(kept) != 0 {
		t.Fatalf("kept = %+v, want none", kept)
	}
	if len(stale) != 1 || stale[0].Subject != "example.orders.v1.Order.eta" {
		t.Fatalf("stale = %+v, want the one unmatched permission named", stale)
	}
}

// The rendered finding contains the discriminant, spelled exactly as a
// permission must spell it.
func TestRenderFinding_ContainsChange(t *testing.T) {
	f := typeChangedFinding("example.orders.v1.Order.total", "int32 -> int64")
	out := breaking.Render([]breaking.Finding{f}, breaking.Info{
		Outcome: breaking.Compared, Previous: "abc1234",
	})
	if !strings.Contains(out, "int32 -> int64") {
		t.Errorf("rendered report does not carry the discriminant a permission must spell:\n%s", out)
	}
}

// negative: a permission for Order.total does not match Order.subtotal.
func TestPermit_SubjectMismatchDoesNotMatch(t *testing.T) {
	f := typeChangedFinding("example.orders.v1.Order.subtotal", "int32 -> int64")
	cfg := &config.Breaking{Allow: []config.Permission{
		{Rule: breaking.RuleFieldTypeChanged, Subject: "example.orders.v1.Order.total",
			Change: "int32 -> int64", Reason: "widening on purpose"},
	}}
	kept, stale := breaking.Permit([]breaking.Finding{f}, cfg)
	if len(kept) != 1 {
		t.Fatalf("kept = %+v, want the finding still standing: subject differs", kept)
	}
	if len(stale) != 1 {
		t.Fatalf("stale = %+v, want the permission reported stale", stale)
	}
}

// negative: a permission does not match the same rule on the same field
// name in a different message (a different subject entirely, sharing only
// the leaf field name).
func TestPermit_SameFieldNameDifferentMessageDoesNotMatch(t *testing.T) {
	f := typeChangedFinding("example.billing.v1.Invoice.total", "int32 -> int64")
	cfg := &config.Breaking{Allow: []config.Permission{
		{Rule: breaking.RuleFieldTypeChanged, Subject: "example.orders.v1.Order.total",
			Change: "int32 -> int64", Reason: "widening on purpose"},
	}}
	kept, stale := breaking.Permit([]breaking.Finding{f}, cfg)
	if len(kept) != 1 {
		t.Fatalf("kept = %+v, want the finding still standing: different message entirely", kept)
	}
	if len(stale) != 1 {
		t.Fatalf("stale = %+v, want the permission reported stale", stale)
	}
}
