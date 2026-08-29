package breaking

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/lockfile"
)

// noopFetch never resolves a dependency: the fixtures in this file declare
// none, so a fetch call would mean the fixture drifted.
func noopFetch(_ context.Context, _, ref string) (string, string, error) {
	return "", "", errUnreachable{}
}

type errUnreachable struct{}

func (errUnreachable) Error() string { return "diff fixture declares no dependencies" }

// writeEmptyLock writes a lock with no dependency entries, for fixtures whose
// manifest declares no deps.
func writeEmptyLock(t *testing.T, dir string) {
	t.Helper()
	if err := lockfile.Save(filepath.Join(dir, lint.LockName), &lockfile.Lock{Version: lockfile.Version}); err != nil {
		t.Fatal(err)
	}
}

// diffFixture lays out a single-module repository with no dependencies and
// returns the two Revisions Diff compares: prevSHA compiled from prevBody,
// curSHA compiled from curBody, both under the same import path.
func diffFixture(t *testing.T, path, prevBody, curBody string) (prevRev, curRev Revision) {
	t.Helper()
	dir := repo(t)
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: own\n")
	writeEmptyLock(t, dir)
	write(t, dir, "own/"+path, prevBody)
	prevSHA := commit(t, dir, "marker.txt", "prev", "prev revision")

	write(t, dir, "own/"+path, curBody)
	curSHA := commit(t, dir, "marker.txt", "cur", "cur revision")

	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	prevRev, err = Load(context.Background(), r, prevSHA, noopFetch, true)
	if err != nil {
		t.Fatalf("Load prev: %v", err)
	}
	curRev, err = Load(context.Background(), r, curSHA, noopFetch, false)
	if err != nil {
		t.Fatalf("Load cur: %v", err)
	}
	return prevRev, curRev
}

// Additions are never breaking, but they are reported as changes: a rename
// is a removal and an addition, and the classifier needs both halves to pair
// them. This is why Diff reports changes and not findings.
func TestAdditionIsAChangeAndNotAFinding(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n")

	changes := Diff(prevRev, curRev)

	var found bool
	for _, c := range changes {
		if c.Subject == "example.orders.v1.Order.eta" {
			found = true
			if c.Kind != Added {
				t.Fatalf("Kind = %v, want Added", c.Kind)
			}
			if c.Before != nil {
				t.Fatalf("Before = %v, want nil for an addition", c.Before)
			}
			if c.After == nil {
				t.Fatal("After = nil, want the added field's descriptor")
			}
		}
	}
	if !found {
		t.Fatal("Diff did not report the added field example.orders.v1.Order.eta")
	}
}

// A removed field is reported by its full name, stated by Diff itself: it
// holds both descriptor sets and knows a removed field's name exactly.
func TestFieldRemovedIsReported(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n}\n")

	changes := Diff(prevRev, curRev)

	var found bool
	for _, c := range changes {
		if c.Subject == "example.orders.v1.Order.eta" {
			found = true
			if c.Kind != Removed {
				t.Fatalf("Kind = %v, want Removed", c.Kind)
			}
			if c.After != nil {
				t.Fatalf("After = %v, want nil for a removal", c.After)
			}
			if c.Before == nil {
				t.Fatal("Before = nil, want the removed field's descriptor")
			}
		}
	}
	if !found {
		t.Fatal("Diff did not report the removed field example.orders.v1.Order.eta")
	}
}

// A removed file has no full name. Its subject is its import path, tagged,
// so it can never be mistaken for a declaration.
func TestRemovedFileHasATaggedSubject(t *testing.T) {
	dir := repo(t)
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: own\n")
	writeEmptyLock(t, dir)
	write(t, dir, "own/api/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\nmessage Order {\n  string id = 1;\n}\n")
	write(t, dir, "own/api/orders/v1/keep.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\nmessage Keep {\n  string id = 1;\n}\n")
	prevSHA := commit(t, dir, "marker.txt", "prev", "two files")

	run(t, dir, "rm", "-q", "own/api/orders/v1/order.proto")
	curSHA := commit(t, dir, "marker.txt", "cur", "removed a file")

	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	prevRev, err := Load(context.Background(), r, prevSHA, noopFetch, true)
	if err != nil {
		t.Fatalf("Load prev: %v", err)
	}
	curRev, err := Load(context.Background(), r, curSHA, noopFetch, false)
	if err != nil {
		t.Fatalf("Load cur: %v", err)
	}

	changes := Diff(prevRev, curRev)

	var found bool
	for _, c := range changes {
		if c.Subject == "file:api/orders/v1/order.proto" {
			found = true
			if c.Kind != Removed {
				t.Fatalf("Kind = %v, want Removed", c.Kind)
			}
			if c.Path != "api/orders/v1/order.proto" {
				t.Fatalf("Path = %q, want the file's import path", c.Path)
			}
		}
	}
	if !found {
		t.Fatal("Diff did not report the removed file file:api/orders/v1/order.proto")
	}
}

// Two byte-identical revisions produce no changes at all. Diff reports
// difference, not presence: a declaration untouched between revisions is not
// a Change, however it is indexed.
func TestIdenticalRevisionsProduceNoChanges(t *testing.T) {
	body := "syntax = \"proto3\";\npackage example.orders.v1;\n" +
		"message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n" +
		"enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_PLACED = 1;\n}\n" +
		"service Orders {\n  rpc Get(Order) returns (Order);\n}\n"
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto", body, body)

	changes := Diff(prevRev, curRev)
	if len(changes) != 0 {
		t.Fatalf("Diff = %+v, want no changes between identical revisions", changes)
	}
}

// Adding one map field produces exactly one change — the field itself — and
// nothing about the synthesised map-entry message or its key/value fields.
func TestAddingAMapFieldProducesOnlyTheField(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n  map<string, string> tags = 2;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 1 {
		t.Fatalf("Diff = %+v, want exactly one change (the added field)", changes)
	}
	c := changes[0]
	if c.Subject != "example.orders.v1.Order.tags" {
		t.Fatalf("Subject = %q, want example.orders.v1.Order.tags", c.Subject)
	}
	if c.Kind != Added {
		t.Fatalf("Kind = %v, want Added", c.Kind)
	}
}

// Removing a field reports the field's removal alone: no Modified for its
// parent message or for a sibling field that did not change.
func TestFieldRemovalDoesNotTouchItsParentOrSiblings(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 1 {
		t.Fatalf("Diff = %+v, want exactly one change (the removed field)", changes)
	}
	c := changes[0]
	if c.Subject != "example.orders.v1.Order.eta" {
		t.Fatalf("Subject = %q, want example.orders.v1.Order.eta", c.Subject)
	}
	if c.Kind != Removed {
		t.Fatalf("Kind = %v, want Removed", c.Kind)
	}
}

// Retyping a map's value is a wire breakage, and it must be visible even
// though the map field's own FieldDescriptorProto never changes: the entry
// type name it points at is the same synthesised *Entry on both sides. The
// change has to be reported against the field's own subject, never against
// the entry type — a subject naming a type nobody wrote is exactly the
// defect the map-entry guard exists to prevent.
func TestRetypingAMapValueIsDetected(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  map<string, string> tags = 1;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  map<string, int64> tags = 1;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 1 {
		t.Fatalf("Diff = %+v, want exactly one change (the retyped field)", changes)
	}
	c := changes[0]
	if c.Subject != "example.orders.v1.Order.tags" {
		t.Fatalf("Subject = %q, want example.orders.v1.Order.tags", c.Subject)
	}
	if c.Kind != Modified {
		t.Fatalf("Kind = %v, want Modified", c.Kind)
	}
}

// Retyping a map's key must be detected exactly the same way as its value:
// the fix is not allowed to cover only one half of the map entry.
func TestRetypingAMapKeyIsDetected(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  map<string, string> tags = 1;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  map<int64, string> tags = 1;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 1 {
		t.Fatalf("Diff = %+v, want exactly one change (the retyped field)", changes)
	}
	c := changes[0]
	if c.Subject != "example.orders.v1.Order.tags" {
		t.Fatalf("Subject = %q, want example.orders.v1.Order.tags", c.Subject)
	}
	if c.Kind != Modified {
		t.Fatalf("Kind = %v, want Modified", c.Kind)
	}
}

// The round-1 fix must still hold: adding a map field is one change (the
// field itself), and nothing is reported about the synthesised TagsEntry
// message or its key/value fields.
func TestAddingAMapFieldStillProducesOnlyTheFieldAfterMapValueFix(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string id = 1;\n  map<string, string> tags = 2;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 1 {
		t.Fatalf("Diff = %+v, want exactly one change (the added field)", changes)
	}
	c := changes[0]
	if c.Subject != "example.orders.v1.Order.tags" {
		t.Fatalf("Subject = %q, want example.orders.v1.Order.tags", c.Subject)
	}
	if c.Kind != Added {
		t.Fatalf("Kind = %v, want Added", c.Kind)
	}
}

// This is the test that proves the comparison is not vacuous: an ordinary,
// non-map field retype must be detected too, by the same descEqual path
// every other field goes through.
func TestFieldTypeChangeIsDetected(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  int64 eta = 1;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n  string eta = 1;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 1 {
		t.Fatalf("Diff = %+v, want exactly one change (the retyped field)", changes)
	}
	c := changes[0]
	if c.Subject != "example.orders.v1.Order.eta" {
		t.Fatalf("Subject = %q, want example.orders.v1.Order.eta", c.Subject)
	}
	if c.Kind != Modified {
		t.Fatalf("Kind = %v, want Modified", c.Kind)
	}
}

// A reformat — whitespace, comments, field declaration order in the source
// text — must never read as a change: descEqual compares descriptorpb forms,
// which carry no source position, and Diff is walked by full name rather
// than declaration order.
func TestReformattingOnlyProducesNoChanges(t *testing.T) {
	prevRev, curRev := diffFixture(t, "example/orders/v1/order.proto",
		"syntax = \"proto3\";\npackage example.orders.v1;\n\n"+
			"// Order is a customer order.\n"+
			"message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n",
		"syntax = \"proto3\";\npackage example.orders.v1;\n"+
			"message Order {\n\n  // reformatted: blank line and reordered comment\n"+
			"  int64 eta = 2;\n  string id = 1;\n}\n")

	changes := Diff(prevRev, curRev)
	if len(changes) != 0 {
		t.Fatalf("Diff = %+v, want no changes from a pure reformat", changes)
	}
}
