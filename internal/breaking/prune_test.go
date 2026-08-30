package breaking

import (
	"os"
	"path/filepath"
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

	if err := Prune(path, []config.Permission{stale}); err != nil {
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
