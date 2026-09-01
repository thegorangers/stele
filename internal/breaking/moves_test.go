package breaking

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// movesFixture builds a two-commit repository whose revisions may differ in
// which files they contain, which diffFixture's single-file signature cannot
// express.
func movesFixture(t *testing.T, prevFiles, curFiles map[string]string) (prevRev, curRev Revision) {
	t.Helper()
	dir := repo(t)
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: own\n")
	writeEmptyLock(t, dir)
	for p, body := range prevFiles {
		write(t, dir, "own/"+p, body)
	}
	prevSHA := commit(t, dir, "marker.txt", "prev", "prev revision")

	for p := range prevFiles {
		if _, kept := curFiles[p]; !kept {
			if err := os.Remove(filepath.Join(dir, "own", p)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for p, body := range curFiles {
		write(t, dir, "own/"+p, body)
	}
	curSHA := commit(t, dir, "marker.txt", "cur", "cur revision")

	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if prevRev, err = Load(context.Background(), r, prevSHA, noopFetch, true); err != nil {
		t.Fatalf("Load prev: %v", err)
	}
	if curRev, err = Load(context.Background(), r, curSHA, noopFetch, false); err != nil {
		t.Fatalf("Load cur: %v", err)
	}
	return prevRev, curRev
}

func TestLosslessPackageRenameProducesNoChanges(t *testing.T) {
	prev, cur := movesFixture(t,
		map[string]string{
			"example/orders/v1/order.proto": `syntax = "proto3";
package example.orders.v1;
option go_package = "example.test/gen/orders/v1;ordersv1";
message Order { string id = 1; Line line = 2; }
message Line { string sku = 1; }
`,
		},
		map[string]string{
			"example/orders/v1/order.proto": `syntax = "proto3";
package example.ordering.v1;
option go_package = "example.test/gen/ordering/v1;orderingv1";
message Order { string id = 1; Line line = 2; }
message Line { string sku = 1; }
`,
		})

	moved, err := ApplyMoves(prev, []config.Move{{From: "example.orders.v1", To: "example.ordering.v1"}})
	if err != nil {
		t.Fatalf("ApplyMoves: %v", err)
	}
	if got := Diff(moved, cur); len(got) != 0 {
		t.Fatalf("a lossless rename produced %d changes, want 0: %+v", len(got), got)
	}
}

func TestRenameThatDropsAFieldReportsItUnderTheNewName(t *testing.T) {
	prev, cur := movesFixture(t,
		map[string]string{
			"example/orders/v1/order.proto": `syntax = "proto3";
package example.orders.v1;
option go_package = "example.test/gen/orders/v1;ordersv1";
message Order { string id = 1; string note = 2; }
`,
		},
		map[string]string{
			"example/orders/v1/order.proto": `syntax = "proto3";
package example.ordering.v1;
option go_package = "example.test/gen/ordering/v1;orderingv1";
message Order { string id = 1; }
`,
		})

	moved, err := ApplyMoves(prev, []config.Move{{From: "example.orders.v1", To: "example.ordering.v1"}})
	if err != nil {
		t.Fatalf("ApplyMoves: %v", err)
	}
	findings := Classify(Diff(moved, cur), moved, cur)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want exactly 1: %+v", len(findings), findings)
	}
	if want := "example.ordering.v1.Order.note"; findings[0].Subject != want {
		t.Fatalf("subject %q, want %q — the removal must be reported under the new name",
			findings[0].Subject, want)
	}
}

func TestLosslessFileMoveProducesNoChanges(t *testing.T) {
	prev, cur := movesFixture(t,
		map[string]string{
			"example/orders/v1/order.proto": `syntax = "proto3";
package example.orders.v1;
option go_package = "example.test/gen/orders/v1;ordersv1";
message Order { string id = 1; }
`,
		},
		map[string]string{
			"example/orders/v1/entities.proto": `syntax = "proto3";
package example.orders.v1;
option go_package = "example.test/gen/orders/v1;ordersv1";
message Order { string id = 1; }
`,
		})

	moved, err := ApplyMoves(prev, []config.Move{{
		From: "file:example/orders/v1/order.proto",
		To:   "file:example/orders/v1/entities.proto",
	}})
	if err != nil {
		t.Fatalf("ApplyMoves: %v", err)
	}
	if got := Diff(moved, cur); len(got) != 0 {
		t.Fatalf("a lossless file move produced %d changes, want 0: %+v", len(got), got)
	}
}

func TestMoveOntoAnExistingPackageIsRefused(t *testing.T) {
	files := map[string]string{
		"example/orders/v1/order.proto": `syntax = "proto3";
package example.orders.v1;
message Order { string id = 1; }
`,
		"example/ordering/v1/order.proto": `syntax = "proto3";
package example.ordering.v1;
message Order { string id = 1; }
`,
	}
	// Only the previous side matters here; pass the same files twice.
	prev, _ := movesFixture(t, files, files)

	_, err := ApplyMoves(prev, []config.Move{{From: "example.orders.v1", To: "example.ordering.v1"}})
	if err == nil {
		t.Fatal("accepted a move that renames two Orders onto one name")
	}
	if !strings.Contains(err.Error(), "example.ordering.v1.Order") {
		t.Fatalf("error %q does not name the colliding declaration", err)
	}
}

// Prefix-overlapping moves are accepted by the manifest layer — it refuses a
// duplicate source, a cycle and a self-move, but not two sources where one is
// a prefix of the other. The file's own package is looked up exactly and so
// takes the longer source; a type reference matched against the shorter one
// would name a package the exact lookup never produced.
func TestOverlappingMovesRewriteReferencesByTheLongestSource(t *testing.T) {
	body := `syntax = "proto3";
package example.orders.v1;
message Order { string id = 1; Line line = 2; }
message Line { string sku = 1; }
`
	prev, _ := movesFixture(t,
		map[string]string{"example/orders/v1/order.proto": body},
		map[string]string{"example/orders/v1/order.proto": body})

	moved, err := ApplyMoves(prev, []config.Move{
		{From: "example.orders", To: "example.legacy"},
		{From: "example.orders.v1", To: "example.ordering.v1"},
	})
	if err != nil {
		t.Fatalf("ApplyMoves: %v", err)
	}

	var order protoreflect.MessageDescriptor
	for _, fd := range moved.Files {
		if fd.Path() != "example/orders/v1/order.proto" {
			continue
		}
		order = fd.Messages().ByName("Order")
	}
	if order == nil {
		t.Fatal("the rewritten revision has no Order in example/orders/v1/order.proto")
	}
	if got, want := order.FullName(), protoreflect.FullName("example.ordering.v1.Order"); got != want {
		t.Fatalf("message full name %q, want %q", got, want)
	}
	ref := order.Fields().ByName("line").Message().FullName()
	if want := protoreflect.FullName("example.ordering.v1.Line"); ref != want {
		t.Fatalf("field line refers to %q, want %q — a reference must match the longest move source",
			ref, want)
	}
}

// The identifier half of a go_package is rewritten with the dots stripped out
// of the package name, which makes it a very short needle. Confined to the
// half after the semicolon it is harmless; loose over the whole value it
// rewrites bytes of the import path that have nothing to do with the move.
func TestGoPackageRewriteLeavesTheImportPathAlone(t *testing.T) {
	const gp = "example.test/lab/gen/a/b;ab"
	got := rewriteGoPackage(gp, "example.a.b", "example.a.c")
	if want := "example.test/lab/gen/a/c;ac"; got != want {
		t.Fatalf("rewriteGoPackage(%q) = %q, want %q", gp, got, want)
	}
}
