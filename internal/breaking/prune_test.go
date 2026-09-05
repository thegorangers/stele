package breaking

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
)

// TestPruneMatchesByIdentityNotPosition is fix round 2's regression test:
// Prune must not trust that the entry at some index is still the one the
// caller meant. It re-reads the file itself, and anything can have
// reordered allow[] between the caller's own read (the one that decided
// which permission is stale) and this one. A version keyed on index would
// delete whatever now sits where the stale permission used to be; matching
// on (rule, subject, change) instead must delete the actual stale entry
// wherever it has moved to, and leave the other one — which now sits at
// the position the stale one used to occupy — alone.
func TestPruneMatchesByIdentityNotPosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.yaml")

	// stale is written first, at index 0; kept second, at index 1.
	original := "version: 1\n" +
		"modules:\n" +
		"  - path: api\n" +
		"breaking:\n" +
		"  allow:\n" +
		"    - rule: break/field_removed\n" +
		"      subject: example.v1.Order.status\n" +
		"      reason: dropped in the v2 rollout\n" +
		"    - rule: break/field_type_changed\n" +
		"      subject: example.v1.Order.total\n" +
		"      change: int32 -> int64\n" +
		"      reason: widening; no consumer stores this in a 32-bit field\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	stale := config.Permission{
		Rule:    "break/field_removed",
		Subject: "example.v1.Order.status",
		Reason:  "dropped in the v2 rollout",
	}

	// The window: the file is rewritten with the two entries swapped
	// after the caller decided stale names index 0, but before Prune
	// itself reads the file. A position-keyed Prune would now delete
	// break/field_type_changed — the one now sitting at index 0 — instead
	// of the actual stale permission.
	reordered := "version: 1\n" +
		"modules:\n" +
		"  - path: api\n" +
		"breaking:\n" +
		"  allow:\n" +
		"    - rule: break/field_type_changed\n" +
		"      subject: example.v1.Order.total\n" +
		"      change: int32 -> int64\n" +
		"      reason: widening; no consumer stores this in a 32-bit field\n" +
		"    - rule: break/field_removed\n" +
		"      subject: example.v1.Order.status\n" +
		"      reason: dropped in the v2 rollout\n"
	if err := os.WriteFile(path, []byte(reordered), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Prune(path, []config.Permission{stale}, nil); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	want := "version: 1\n" +
		"modules:\n" +
		"  - path: api\n" +
		"breaking:\n" +
		"  allow:\n" +
		"    - rule: break/field_type_changed\n" +
		"      subject: example.v1.Order.total\n" +
		"      change: int32 -> int64\n" +
		"      reason: widening; no consumer stores this in a 32-bit field\n"

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("Prune must delete the entry that matches the stale permission's identity, not whatever now sits at its old index:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestPruneRefusesFlowStyleList is the regression test for F2: a flow-style
// allow list — [{...}, {...}] on one source line — has no line range that
// belongs to one entry alone. Deleting the stale entry's line range would
// delete the whole allow: line, taking the live permission beside it with
// it. Prune must refuse rather than guess.
func TestPruneRefusesFlowStyleList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.yaml")

	original := "version: 1\n" +
		"modules:\n" +
		"  - path: api\n" +
		"breaking:\n" +
		"  allow: [{rule: break/field_removed, subject: example.v1.Order.status, " +
		"reason: dropped in the v2 rollout}, {rule: break/field_type_changed, " +
		"subject: example.v1.Order.total, change: int32 -> int64, reason: widening}]\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	stale := config.Permission{
		Rule:    "break/field_removed",
		Subject: "example.v1.Order.status",
		Reason:  "dropped in the v2 rollout",
	}

	_, _, err := Prune(path, []config.Permission{stale}, nil)
	if err == nil {
		t.Fatal("Prune must refuse a flow-style allow list, not guess at the surgery")
	}
	if !strings.Contains(err.Error(), "flow-style") {
		t.Errorf("the refusal must name flow style so the reader knows why: %v", err)
	}
	if !strings.Contains(err.Error(), "block-style") {
		t.Errorf("the refusal must say --prune edits block-style lists only: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("a refused prune must leave the file untouched:\ngot:\n%s\nwant (unchanged):\n%s", got, original)
	}
}

// TestPruneRemovesAStaleMove is Task 5's own test: a stale move is pruned
// from breaking.moves on the same terms a stale permission is pruned from
// breaking.allow, and a move that is not stale survives.
func TestPruneRemovesAStaleMove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.yaml")
	const before = `breaking:
  base: master
  moves:
    - from: example.orders.v1
      to: example.ordering.v1
    - from: example.billing.v1
      to: example.invoicing.v1
`
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Prune(path, nil, []config.Move{{From: "example.orders.v1", To: "example.ordering.v1"}}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "example.orders.v1") {
		t.Fatalf("the stale move survived pruning:\n%s", got)
	}
	if !strings.Contains(string(got), "example.billing.v1") {
		t.Fatalf("pruning removed a move that was not stale:\n%s", got)
	}
}

// TestPruneCountsWhatItMatchedNotWhatItWasAsked: the count Prune reports is
// what the file actually gave up. An entry whose text changed under the
// command — the same window TestPruneMatchesByIdentityNotPosition covers —
// is not deleted, and must not be counted as if it had been, or --prune
// would tell a reader the manifest is clean when the entry is still there.
func TestPruneCountsWhatItMatchedNotWhatItWasAsked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.yaml")

	original := "version: 1\n" +
		"modules:\n" +
		"  - path: api\n" +
		"breaking:\n" +
		"  moves:\n" +
		"    - from: example.renamed.again\n" +
		"      to: example.ordering.v1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	perms, moves, err := Prune(path, nil, []config.Move{
		{From: "example.orders.v1", To: "example.ordering.v1"},
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if perms != 0 || moves != 0 {
		t.Errorf("Prune counted %d permission(s) and %d move(s) it did not remove", perms, moves)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("Prune edited a file it matched nothing in:\ngot:\n%s", got)
	}
}

// TestPruneEmptyingBothListsRemovesTheBreakingKey: the "never leave a bare
// key" rule is about the block as a whole, not one list at a time. A
// breaking: block holding only allow and moves, both entirely pruned, must
// take the breaking: key with it.
func TestPruneEmptyingBothListsRemovesTheBreakingKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.yaml")

	head := "version: 1\n" +
		"modules:\n" +
		"  - path: api\n"
	original := head +
		"breaking:\n" +
		"  allow:\n" +
		"    - rule: break/field_removed\n" +
		"      subject: example.v1.Order.status\n" +
		"      reason: dropped in the v2 rollout\n" +
		"  moves:\n" +
		"    - from: example.orders.v1\n" +
		"      to: example.ordering.v1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	perms, moves, err := Prune(path,
		[]config.Permission{{
			Rule:    "break/field_removed",
			Subject: "example.v1.Order.status",
			Reason:  "dropped in the v2 rollout",
		}},
		[]config.Move{{From: "example.orders.v1", To: "example.ordering.v1"}})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if perms != 1 || moves != 1 {
		t.Errorf("Prune removed %d permission(s) and %d move(s), want 1 and 1", perms, moves)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != head {
		t.Errorf("pruning every list under breaking: must take the breaking: key too:\ngot:\n%s\nwant:\n%s", got, head)
	}
}
