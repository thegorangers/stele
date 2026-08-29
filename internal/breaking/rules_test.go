package breaking

import (
	"context"
	"sort"
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
)

// classifyFixture lays out a single-module, no-dependency repository whose
// "own/" tree holds prevFiles at one commit and curFiles at the next, then
// runs Diff and Classify across the two revisions.
//
// This generalises diff_test.go's diffFixture, which only ever rewrites one
// file at the same path: several of these rules (file_removed,
// package_renamed, go_package_changed) need more than one file, or a file
// that exists on only one side, to fire at all.
func classifyFixture(t *testing.T, prevFiles, curFiles map[string]string) []Finding {
	t.Helper()
	dir := repo(t)
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: own\n")
	writeEmptyLock(t, dir)
	for name, body := range prevFiles {
		write(t, dir, "own/"+name, body)
	}
	prevSHA := commit(t, dir, "marker.txt", "prev", "prev revision")

	for name := range prevFiles {
		if _, ok := curFiles[name]; !ok {
			run(t, dir, "rm", "-q", "own/"+name)
		}
	}
	for name, body := range curFiles {
		write(t, dir, "own/"+name, body)
	}
	curSHA := commit(t, dir, "marker.txt", "cur", "cur revision")

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
	return Classify(changes, prevRev, curRev)
}

// rulePair is a (rule id, category, change) triple, the unit assertFires
// and assertClean compare on. Asserting rule ids alone lets a
// miscategorised finding (Wire reported as Source, or vice versa) hide
// behind a green test — Category is half of what a permanent id means —
// and asserting rule+category alone still lets the Change discriminant
// drift silently, which is exactly what a later plan's permissions match
// on: a spelling change in "%d -> %d" that nothing here pins is invisible
// to this suite. So every assertion in this file checks all three.
type rulePair struct {
	rule   string
	cat    Category
	change string
}

// rulePairs reports the sorted, de-duplicated set of (rule, category,
// change) triples a set of findings carries.
func rulePairs(findings []Finding) []rulePair {
	seen := make(map[rulePair]bool)
	for _, f := range findings {
		seen[rulePair{f.Rule, f.Category, f.Change}] = true
	}
	out := make([]rulePair, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].rule != out[j].rule {
			return out[i].rule < out[j].rule
		}
		if out[i].cat != out[j].cat {
			return out[i].cat < out[j].cat
		}
		return out[i].change < out[j].change
	})
	return out
}

// assertFires checks that exactly want fired, in full, and that nothing
// else did: a case that only asserts presence cannot tell a correctly
// narrow, correctly categorised, correctly discriminated rule apart from
// one that also fires on everything else, fires with the wrong category,
// or carries the wrong Change spelling.
func assertFires(t *testing.T, findings []Finding, want []rulePair) {
	t.Helper()
	got := rulePairs(findings)
	wantSorted := append([]rulePair(nil), want...)
	sort.Slice(wantSorted, func(i, j int) bool {
		if wantSorted[i].rule != wantSorted[j].rule {
			return wantSorted[i].rule < wantSorted[j].rule
		}
		if wantSorted[i].cat != wantSorted[j].cat {
			return wantSorted[i].cat < wantSorted[j].cat
		}
		return wantSorted[i].change < wantSorted[j].change
	})
	if !equalPairs(got, wantSorted) {
		t.Fatalf("(rule, category, change) triples fired = %v, want exactly %v", got, wantSorted)
	}
}

// assertClean checks that nothing fired at all: a legal change of the same
// shape as a firing case must leave every rule silent.
func assertClean(t *testing.T, findings []Finding) {
	t.Helper()
	if got := rulePairs(findings); len(got) != 0 {
		t.Fatalf("(rule, category, change) triples fired = %v, want none", got)
	}
}

func equalPairs(a, b []rulePair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const hdr = "syntax = \"proto3\";\npackage example.orders.v1;\n"

var cases = []struct {
	name      string
	prev, cur map[string]string
	want      []rulePair // empty means: legal, nothing should fire
}{
	// -- field_removed / field_added --
	{
		"field_removed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		[]rulePair{{"break/field_removed", Source, ""}},
	},
	{
		"field_added",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		nil,
	},

	// -- field_renamed --
	{
		"field_renamed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 promised_at = 2;\n}\n"},
		[]rulePair{{"break/field_renamed", Source, ""}},
	},
	{
		"field_not_renamed_type_differs",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  string promised_at = 3;\n}\n"},
		[]rulePair{{"break/field_removed", Source, ""}}, // eta removed, promised_at is a new field: shape (number, kind) differs
	},
	// I1: a field entering a oneof is never paired into a rename, even when
	// it also changes name in the same commit — oneof membership is part of
	// the rename rule's shape, so this is a removal, and the entry into the
	// oneof is a fresh addition (not a finding on its own).
	{
		"field_not_renamed_oneof_membership_differs",
		map[string]string{"a.proto": hdr + "message Order {\n  string a = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string b = 1;\n  }\n}\n"},
		[]rulePair{{"break/field_removed", Source, ""}},
	},

	// -- field_type_changed --
	{
		"field_type_int32_to_string",
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string code = 1;\n}\n"},
		[]rulePair{{"break/field_type_changed", Wire, "int32 -> string"}},
	},
	// R4: int32 -> int64 is wire-compatible (WireCompatible reports true),
	// but the generated accessor's Go type still changes underneath every
	// caller, which is a source breakage, not a wire one. The brief's table
	// called this legal; it was wrong, per the design's own permission
	// example for exactly this change.
	{
		"field_type_int32_to_int64",
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  int64 code = 1;\n}\n"},
		[]rulePair{{"break/field_type_changed", Source, "int32 -> int64"}},
	},
	{
		"field_type_unchanged",
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n  string note = 2;\n}\n"},
		nil,
	},

	// -- field_number_changed --
	{
		"field_number_changed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 2;\n}\n"},
		[]rulePair{{"break/field_number_changed", Wire, "1 -> 2"}},
	},
	{
		"field_number_unchanged_reordered",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  int64 eta = 2;\n  string id = 1;\n}\n"},
		nil,
	},

	// -- field_cardinality_changed --
	{
		"field_cardinality_changed",
		map[string]string{"a.proto": hdr + "message Order {\n  string tag = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  repeated string tag = 1;\n}\n"},
		[]rulePair{{"break/field_cardinality_changed", Wire, "optional -> repeated"}},
	},
	{
		"field_cardinality_unchanged",
		map[string]string{"a.proto": hdr + "message Order {\n  repeated string tag = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  repeated string tag = 1;\n  string note = 2;\n}\n"},
		nil,
	},

	// -- field_oneof_changed --
	// R8: one rule applies uniformly to all three directions (join, leave,
	// move between two distinct oneofs): does sibling-clearing semantics
	// change on the wire? That is true exactly when EITHER side's oneof —
	// when it has one — has another member besides the field itself. A
	// decoder only ever clears a sibling on receipt when there is a
	// sibling to clear, so a move, join or leave touching only singleton
	// oneofs changes nothing observable on the wire (Source); one touching
	// a populated oneof on either side does (Wire) — including a move
	// between two DISTINCT oneofs, which is why that case still needs its
	// own fixture even though it is not a join or a leave.
	{
		"field_oneof_join_singleton_is_source",
		map[string]string{"a.proto": hdr + "message Order {\n  string a = 1;\n  string b = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n  }\n  string b = 2;\n}\n"},
		[]rulePair{{"break/field_oneof_changed", Source, "none -> choice"}},
	},
	{
		"field_oneof_join_populated_is_wire",
		map[string]string{"a.proto": hdr + "message Order {\n  string a = 1;\n  oneof choice {\n    string c = 3;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string c = 3;\n    string a = 1;\n  }\n}\n"},
		[]rulePair{{"break/field_oneof_changed", Wire, "none -> choice"}},
	},
	{
		"field_oneof_leave_singleton_is_source",
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string a = 1;\n}\n"},
		[]rulePair{{"break/field_oneof_changed", Source, "choice -> none"}},
	},
	{
		"field_oneof_leave_populated_is_wire",
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n    string c = 3;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string a = 1;\n  oneof choice {\n    string c = 3;\n  }\n}\n"},
		[]rulePair{{"break/field_oneof_changed", Wire, "choice -> none"}},
	},
	// Moving between two DISTINCT oneofs where at least one side is
	// populated: the sibling-clearing group changes on decode, so Wire.
	{
		"field_oneof_between_populated_is_wire",
		map[string]string{"a.proto": hdr + "message Order {\n  oneof x {\n    string a = 1;\n    string keep_x = 3;\n  }\n  oneof y {\n    string b = 2;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof x {\n    string keep_x = 3;\n  }\n  oneof y {\n    string b = 2;\n    string a = 1;\n  }\n}\n"},
		[]rulePair{{"break/field_oneof_changed", Wire, "x -> y"}},
	},
	// The case R8 exists to fix: a move between two SINGLETON oneofs.
	// Neither x nor y ever has a sibling to clear, before or after, so a
	// decoder behaves identically byte for byte — Source, not Wire. A third
	// oneof (w, with its own field p, untouched throughout) has to be
	// present on both sides purely so that a's declared oneof_index
	// actually changes value (0 -> 1) between revisions — otherwise this
	// diffs as a same-shaped field descriptor and Diff has nothing to
	// report at the field level at all, independent of Classify.
	{
		"field_oneof_between_singletons_is_source",
		map[string]string{"a.proto": hdr + "message Order {\n  oneof x {\n    string a = 1;\n  }\n  oneof w {\n    string p = 5;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof w {\n    string p = 5;\n  }\n  oneof y {\n    string a = 1;\n  }\n}\n"},
		[]rulePair{{"break/field_oneof_changed", Source, "x -> y"}},
	},
	{
		"field_oneof_unchanged",
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n    string c = 3;\n  }\n}\n"},
		nil,
	},

	// -- message_removed --
	// There is no break/message_renamed: a message has no number to key a
	// rename pairing on. A message that is removed under one name and
	// added back with an identical (or different) shape under another name
	// is reported as a removal, never hidden behind a "rename".
	{
		"message_removed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\nmessage Keep {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Keep {\n  string id = 1;\n}\n"},
		[]rulePair{{"break/message_removed", Source, ""}},
	},
	{
		"message_removed_even_with_matching_shape",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Purchase {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		[]rulePair{{"break/message_removed", Source, ""}},
	},
	{
		"message_kept",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\nmessage Extra {\n  string id = 1;\n}\n"},
		nil,
	},

	// -- enum_removed --
	// There is no break/enum_renamed, for the same reason as messages.
	{
		"enum_removed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\nmessage Keep {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Keep {\n  string id = 1;\n}\n"},
		[]rulePair{{"break/enum_removed", Source, ""}},
	},
	{
		"enum_kept",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n}\nmessage Extra {\n  string id = 1;\n}\n"},
		nil,
	},

	// -- enum_value_removed / renamed / number_changed --
	{
		"enum_value_removed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n}\n"},
		[]rulePair{{"break/enum_value_removed", Source, ""}},
	},
	{
		"enum_value_added",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		nil,
	},
	{
		"enum_value_renamed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_DONE = 1;\n}\n"},
		[]rulePair{{"break/enum_value_renamed", Source, ""}},
	},
	{
		"enum_value_number_changed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 2;\n  STATUS_RESERVED_1 = 1;\n}\n"},
		[]rulePair{{"break/enum_value_number_changed", Wire, "1 -> 2"}},
	},
	{
		"enum_value_number_unchanged_reordered",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n  STATUS_DONE = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_DONE = 2;\n  STATUS_OK = 1;\n}\n"},
		nil,
	},

	// -- service_removed --
	// There is no break/service_renamed, for the same reason as messages.
	{
		"service_removed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\n",
		},
		[]rulePair{{"break/service_removed", Wire, ""}},
	},
	{
		"service_kept",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nmessage Extra {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		nil,
	},

	// -- method_removed / signature / streaming --
	// There is no break/method_renamed, for the same reason as messages.
	{
		"method_removed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n  rpc List(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc List(Req) returns (Resp);\n}\n",
		},
		[]rulePair{{"break/method_removed", Wire, ""}},
	},
	{
		"method_added",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc List(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc List(Req) returns (Resp);\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		nil,
	},
	{
		"method_signature_changed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nmessage Resp2 {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nmessage Resp2 {}\nservice Orders {\n  rpc Get(Req) returns (Resp2);\n}\n",
		},
		[]rulePair{{"break/method_signature_changed", Wire, "example.orders.v1.Req/example.orders.v1.Resp -> example.orders.v1.Req/example.orders.v1.Resp2"}},
	},
	{
		"method_signature_unchanged",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n  rpc List(Req) returns (Resp);\n}\n",
		},
		nil,
	},
	{
		"method_streaming_changed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (stream Resp);\n}\n",
		},
		[]rulePair{{"break/method_streaming_changed", Wire, "unary -> server-streaming"}},
	},
	{
		"method_streaming_unchanged",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (stream Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (stream Resp);\n  rpc List(Req) returns (stream Resp);\n}\n",
		},
		nil,
	},

	// -- package_removed / renamed --
	{
		"package_removed",
		map[string]string{
			"a.proto": hdr + "message Order {\n  string id = 1;\n}\n",
			"b.proto": "syntax = \"proto3\";\npackage example.other.v1;\nmessage Keep {\n  string id = 1;\n}\n",
		},
		map[string]string{
			"b.proto": "syntax = \"proto3\";\npackage example.other.v1;\nmessage Keep {\n  string id = 1;\n}\n",
		},
		[]rulePair{{"break/package_removed", Wire, ""}},
	},
	{
		"package_renamed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": "syntax = \"proto3\";\npackage example.orders.v2;\nmessage Order {\n  string id = 1;\n}\n"},
		[]rulePair{{"break/package_renamed", Wire, ""}},
	},
	// The negative half of the rename rule for packages: the old package
	// survives untouched, so an unrelated new package appearing alongside
	// it must never be paired into a rename — package_renamed degrades to
	// firing nothing at all, not to package_removed, because the old
	// package didn't go anywhere.
	{
		"package_not_renamed_old_package_survives",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{
			"a.proto": hdr + "message Order {\n  string id = 1;\n}\n",
			"c.proto": "syntax = \"proto3\";\npackage example.other.v1;\nmessage Unrelated {\n  string id = 1;\n}\n",
		},
		nil,
	},

	// -- file_removed --
	{
		"file_removed_declarations_survive",
		map[string]string{
			"a.proto": hdr + "message Order {\n  string id = 1;\n}\n",
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\n",
		},
		map[string]string{
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\nmessage Order {\n  string id = 1;\n}\n",
		},
		[]rulePair{{"break/file_removed", Source, ""}},
	},
	// I2: partial survival still breaks the import of the old path. Gone is
	// deleted outright (reported by its own message_removed); Moved
	// migrates to another file. file_removed must still fire because the
	// path itself is gone and at least one declaration survives.
	{
		"file_removed_declarations_partially_survive",
		map[string]string{
			"a.proto": hdr + "message Moved {\n  string id = 1;\n}\nmessage Gone {\n  string id = 1;\n}\n",
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\n",
		},
		map[string]string{
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\nmessage Moved {\n  string id = 1;\n}\n",
		},
		[]rulePair{{"break/file_removed", Source, ""}, {"break/message_removed", Source, ""}},
	},
	{
		"file_removed_declarations_gone_too",
		map[string]string{
			"a.proto": hdr + "message Order {\n  string id = 1;\n}\n",
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\n",
		},
		map[string]string{
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\n",
		},
		[]rulePair{{"break/message_removed", Source, ""}},
	},
	// (b): a file with no message/enum/service at all — only syntax,
	// package and an option — has nothing a declaration-level finding could
	// already have reported, so there is no double-count to avoid: it must
	// fire on its own the same as any other removed file, because a
	// consumer still imports its PATH.
	{
		"file_removed_no_declarations",
		map[string]string{
			"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\n",
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\n",
		},
		map[string]string{
			"b.proto": hdr + "message Keep {\n  string id = 1;\n}\n",
		},
		[]rulePair{{"break/file_removed", Source, ""}},
	},

	// -- go_package_changed --
	{
		"go_package_changed",
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\nmessage Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v2;ordersv2\";\nmessage Order {\n  string id = 1;\n}\n"},
		[]rulePair{{"break/go_package_changed", Source, "example.com/orders/v1;ordersv1 -> example.com/orders/v2;ordersv2"}},
	},
	{
		"go_package_unchanged",
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\nmessage Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\nmessage Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		nil,
	},
}

func TestClassify(t *testing.T) {
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifyFixture(t, tc.prev, tc.cur)
			if len(tc.want) == 0 {
				assertClean(t, findings)
				return
			}
			assertFires(t, findings, tc.want)
		})
	}
}
