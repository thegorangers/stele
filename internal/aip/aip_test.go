package aip

import (
	"strings"
	"testing"
	"testing/fstest"
)

func file(front, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\n" + front + "\n---\n" + body)}
}

func TestParseCorpusReadsWhatUpstreamWrites(t *testing.T) {
	fsys := fstest.MapFS{
		"aip/general/0131.md": file("id: 131\nstate: approved\nplacement:\n  category: operations\n  order: 10",
			"\n# Standard methods: Get\n\ntext\n\n```proto\nrpc GetBook(GetBookRequest) returns (Book);\n```\n"),
		"aip/general/0001.md": file("id: 1\nstate: approved\nplacement:\n  category: meta", "\n# AIP Purpose and Guidelines\n"),
		"aip/auth/4110.md":    file("id: 4110\nstate: draft", "\n# Application Default Credentials\n"),
		"aip/general/README":  {Data: []byte("not an AIP")},
	}
	got, err := ParseCorpus(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d AIPs, want 3", len(got))
	}
	if got[0].ID != 1 || got[2].ID != 4110 {
		t.Errorf("not in ID order: %v", got)
	}
	// The proto fence is the mechanical prior, and it is read off the body
	// rather than assumed from the category.
	if !got[1].Proto || got[0].Proto {
		t.Errorf("proto fences read wrongly: %+v", got)
	}
	if got[1].Category != "operations" || got[1].Title != "Standard methods: Get" {
		t.Errorf("front matter read wrongly: %+v", got[1])
	}
	if got[2].Scope != "auth" || got[2].State != "draft" {
		t.Errorf("scope or state read wrongly: %+v", got[2])
	}
}

// TestParseCorpusRefusesRatherThanShrinks covers the failure that matters: a
// change to upstream's layout that makes the inventory smaller rather than
// wrong. A shorter list looks exactly like a complete one, so every one of
// these is an error naming the file.
func TestParseCorpusRefusesRatherThanShrinks(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no front matter": {"aip/general/0131.md": {Data: []byte("# Standard methods: Get\n")}},
		"no id":           {"aip/general/0131.md": file("state: approved", "\n# Get\n")},
		"no state":        {"aip/general/0131.md": file("id: 131", "\n# Get\n")},
		"no heading":      {"aip/general/0131.md": file("id: 131\nstate: approved", "\ntext\n")},
		"id is not a number": {"aip/general/0131.md": file("id: one-three-one\nstate: approved",
			"\n# Get\n")},
		"empty corpus": {"aip/general/README": {Data: []byte("x")}},
	} {
		if _, err := ParseCorpus(fsys); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	twice := fstest.MapFS{
		"aip/general/0131.md": file("id: 131\nstate: approved", "\n# Get\n"),
		"aip/apps/0131.md":    file("id: 131\nstate: draft", "\n# Something else\n"),
	}
	if _, err := ParseCorpus(twice); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("one number in two scopes: %v", err)
	}
}

func TestIndexRoundTrips(t *testing.T) {
	recs := []Record{
		{ID: 1, Scope: "general", State: "approved", Category: "meta", Title: "AIP Purpose and Guidelines"},
		{ID: 131, Scope: "general", State: "approved", Category: "operations", Proto: true, Title: "Standard methods: Get"},
	}
	var b strings.Builder
	if err := WriteIndex(&b, "https://example.test/aip", "0123456789abcdef", recs); err != nil {
		t.Fatal(err)
	}
	ix, err := ParseIndex(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if ix.Commit != "0123456789abcdef" || ix.Source != "https://example.test/aip" {
		t.Errorf("provenance lost: %+v", ix)
	}
	if len(ix.Records) != 2 || ix.Records[1] != recs[1] {
		t.Errorf("records lost: %+v", ix.Records)
	}
}

// TestParseIndexRefusesASnapshotWithoutProvenance: a snapshot that does not
// say where it came from cannot be checked against upstream, and a check that
// cannot run is worse than one that fails.
func TestParseIndexRefusesASnapshotWithoutProvenance(t *testing.T) {
	if _, err := ParseIndex(strings.NewReader(indexHeader + "\n1\tgeneral\tapproved\tmeta\tno\tX\n")); err == nil {
		t.Error("parsed a snapshot with no source or commit")
	}
	if _, err := ParseIndex(strings.NewReader("1\tgeneral\tapproved\tmeta\tno\tX\n")); err == nil {
		t.Error("parsed something with no header as a snapshot")
	}
}

func TestLedgerParserRefusesAnIncompleteDecision(t *testing.T) {
	for name, src := range map[string]string{
		"implemented with no rule": "version: 1\naips:\n  - id: 1\n    state: implemented\n",
		"declined with no reason":  "version: 1\naips:\n  - id: 1\n    state: declined\n",
		"unknown state":            "version: 1\naips:\n  - id: 1\n    state: someday\n    reason: x\n",
		"two entries":              "version: 1\naips:\n  - id: 1\n    state: declined\n    reason: x\n  - id: 1\n    state: declined\n    reason: y\n",
		"unknown key":              "version: 1\naips:\n  - id: 1\n    state: declined\n    reason: x\n    note: y\n",
		"wrong version":            "version: 2\naips: []\n",
	} {
		if _, err := parseLedger([]byte(src)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	if _, err := parseLedger([]byte("version: 1\naips:\n  - id: 131\n    state: untriaged\n")); err != nil {
		t.Errorf("untriaged is refused by the test, not by the parser: %v", err)
	}
}
