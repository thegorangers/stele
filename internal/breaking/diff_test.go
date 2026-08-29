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
