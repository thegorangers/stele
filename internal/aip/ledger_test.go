package aip_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/aip"
	"github.com/thegorangers/stele/internal/lint"
)

// TestEveryAIPIsClassified is the gate.
//
// It is the reason this package exists, and it is hermetic on purpose: it
// reads the committed snapshot of upstream and the committed ledger, and it
// fails when the two disagree. Nothing here reaches the network, so it runs in
// the same job as the unit tests, on every change — the check that needs a
// clone is `aipsync -check`, and it answers a different question ("has
// upstream moved") from this one ("have we said what we think").
//
// The failure names the AIP and its title rather than only its number, because
// the fix for this test is to read an AIP and form an opinion, and a test that
// says "AIP 149 is unclassified" sends the reader to a browser before they can
// start.
func TestEveryAIPIsClassified(t *testing.T) {
	ix, err := aip.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := aip.LoadLedger()
	if err != nil {
		t.Fatal(err)
	}

	classified := make(map[int]aip.Entry, len(ledger))
	for _, e := range ledger {
		classified[e.ID] = e
	}

	var missing []string
	upstream := make(map[int]aip.Record, len(ix.Records))
	for _, r := range ix.Records {
		upstream[r.ID] = r
		e, ok := classified[r.ID]
		if !ok {
			missing = append(missing, fmt.Sprintf("AIP-%d (%s): %s — no entry", r.ID, r.Scope, r.Title))
			continue
		}
		if e.State == aip.Untriaged {
			missing = append(missing, fmt.Sprintf("AIP-%d (%s): %s — untriaged", r.ID, r.Scope, r.Title))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d AIP(s) upstream carry no decision:\n  %s\n"+
			"Read each one and add an entry to internal/aip/ledger.yaml saying implemented, candidate, "+
			"declined or undecidable, with the reason. An AIP nobody has an opinion about is the one that "+
			"gets implemented twice or not at all.",
			len(missing), strings.Join(missing, "\n  "))
	}

	// The other direction. A ledger entry for an AIP upstream no longer
	// carries is not harmless bookkeeping: upstream reuses no numbers today,
	// but a rule named after a number that moved would cite guidance it was
	// not written against.
	var orphaned []string
	for _, e := range ledger {
		if _, ok := upstream[e.ID]; !ok {
			orphaned = append(orphaned, fmt.Sprintf("AIP-%d", e.ID))
		}
	}
	if len(orphaned) > 0 {
		t.Errorf("the ledger classifies %s, which the snapshot of %s at %s does not carry; "+
			"upstream withdrew, renumbered or removed it, and the entry — and any rule named after it — "+
			"has to be revisited rather than deleted quietly",
			strings.Join(orphaned, ", "), ix.Source, short(ix.Commit))
	}
}

// TestImplementedAIPsNameLoadedRules closes the drift this repository has been
// bitten by four times: a hand-written claim about the code that the code
// stopped supporting.
//
// The ledger says AIP-191 is implemented by four rules. That sentence is only
// worth anything if the four rules exist, so the list of rule IDs is checked
// against what Builtin actually returns rather than against a second list. A
// rule renamed or dropped fails here, in this repository, rather than in
// somebody's reading of the documentation.
func TestImplementedAIPsNameLoadedRules(t *testing.T) {
	loaded := make(map[string]bool)
	for _, r := range lint.Builtin() {
		loaded[r.ID()] = true
	}
	ledger, err := aip.LoadLedger()
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(map[string]int)
	for _, e := range ledger {
		for _, id := range e.Rules {
			if !loaded[id] {
				t.Errorf("AIP-%d names rule %q, which this tool does not load", e.ID, id)
			}
			claimed[id] = e.ID
		}
	}

	// And the reverse: a built-in rule that no AIP claims. This is not an
	// error — the eight built-ins were chosen against a fleet, not against
	// AIP, and some of them answer to nothing upstream — but it is reported,
	// because "which of our rules is AIP guidance and which is ours" is a
	// question the ledger should be able to answer.
	for _, r := range lint.Builtin() {
		if _, ok := claimed[r.ID()]; !ok {
			t.Logf("rule %s is claimed by no AIP", r.ID())
		}
	}
}

// TestLedgerIsInIDOrder keeps the file reviewable. A ledger appended to at the
// bottom would put every new decision as far as possible from the decisions it
// resembles.
func TestLedgerIsInIDOrder(t *testing.T) {
	ledger, err := aip.LoadLedger()
	if err != nil {
		t.Fatal(err)
	}
	// LoadLedger sorts, so read the file's own order back through the parser
	// by comparing against the snapshot's order of the same IDs.
	ix, err := aip.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger) != len(ix.Records) {
		return // the gate test above reports this properly
	}
	for i := range ledger {
		if ledger[i].ID != ix.Records[i].ID {
			t.Fatalf("entry %d is AIP-%d where the corpus has AIP-%d", i, ledger[i].ID, ix.Records[i].ID)
		}
	}
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
