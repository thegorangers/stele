package breaking

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
)

func TestMoveThatMatchesNothingIsStale(t *testing.T) {
	files := map[string]string{
		"example/ordering/v1/order.proto": `syntax = "proto3";
package example.ordering.v1;
message Order { string id = 1; }
`,
	}
	prev, _ := movesFixture(t, files, files)

	stale := StaleMoves(prev, []config.Move{
		{From: "example.orders.v1", To: "example.ordering.v1"},
	})
	if len(stale) != 1 {
		t.Fatalf("got %d stale moves, want 1", len(stale))
	}
	notes := MoveNotes(stale)
	if len(notes) != 1 || !strings.Contains(notes[0], "example.orders.v1") {
		t.Fatalf("notes %v do not name the stale move", notes)
	}
}

func TestMoveThatStillAppliesIsNotStale(t *testing.T) {
	files := map[string]string{
		"example/orders/v1/order.proto": `syntax = "proto3";
package example.orders.v1;
message Order { string id = 1; }
`,
	}
	prev, _ := movesFixture(t, files, files)

	if stale := StaleMoves(prev, []config.Move{
		{From: "example.orders.v1", To: "example.ordering.v1"},
	}); len(stale) != 0 {
		t.Fatalf("a move that still applies was called stale: %+v", stale)
	}
}
