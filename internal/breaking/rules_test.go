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

// rules reports the sorted, de-duplicated set of rule ids a set of findings
// carries, for a clean assertion of exactly what fired.
func rules(findings []Finding) []string {
	seen := make(map[string]bool)
	for _, f := range findings {
		seen[f.Rule] = true
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// assertFires checks that wantRule fired and that nothing else did: a case
// that only asserts presence cannot tell a correctly narrow rule apart from
// one that also fires on everything else.
func assertFires(t *testing.T, findings []Finding, wantRule string) {
	t.Helper()
	got := rules(findings)
	if len(got) != 1 || got[0] != wantRule {
		t.Fatalf("rules fired = %v, want exactly [%s]", got, wantRule)
	}
}

// assertClean checks that nothing fired at all: a legal change of the same
// shape as a firing case must leave every rule silent.
func assertClean(t *testing.T, findings []Finding) {
	t.Helper()
	if got := rules(findings); len(got) != 0 {
		t.Fatalf("rules fired = %v, want none", got)
	}
}

const hdr = "syntax = \"proto3\";\npackage example.orders.v1;\n"

var cases = []struct {
	name      string
	prev, cur map[string]string
	wantRule  string // empty means: legal, nothing should fire
}{
	// -- field_removed / field_added --
	{
		"field_removed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		"break/field_removed",
	},
	{
		"field_added",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		"",
	},

	// -- field_renamed --
	{
		"field_renamed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 promised_at = 2;\n}\n"},
		"break/field_renamed",
	},
	{
		"field_not_renamed_type_differs",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  string promised_at = 3;\n}\n"},
		"break/field_removed", // eta removed, promised_at is a new field: shape (number, kind) differs
	},

	// -- field_type_changed --
	{
		"field_type_int32_to_string",
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string code = 1;\n}\n"},
		"break/field_type_changed",
	},
	{
		"field_type_unchanged",
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  int32 code = 1;\n  string note = 2;\n}\n"},
		"",
	},

	// -- field_number_changed --
	{
		"field_number_changed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 2;\n}\n"},
		"break/field_number_changed",
	},
	{
		"field_number_unchanged_reordered",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  int64 eta = 2;\n  string id = 1;\n}\n"},
		"",
	},

	// -- field_cardinality_changed --
	{
		"field_cardinality_changed",
		map[string]string{"a.proto": hdr + "message Order {\n  string tag = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  repeated string tag = 1;\n}\n"},
		"break/field_cardinality_changed",
	},
	{
		"field_cardinality_unchanged",
		map[string]string{"a.proto": hdr + "message Order {\n  repeated string tag = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  repeated string tag = 1;\n  string note = 2;\n}\n"},
		"",
	},

	// -- field_oneof_changed --
	{
		"field_oneof_changed",
		map[string]string{"a.proto": hdr + "message Order {\n  string a = 1;\n  string b = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n  }\n  string b = 2;\n}\n"},
		"break/field_oneof_changed",
	},
	{
		"field_oneof_unchanged",
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n  }\n}\n"},
		map[string]string{"a.proto": hdr + "message Order {\n  oneof choice {\n    string a = 1;\n    string c = 3;\n  }\n}\n"},
		"",
	},

	// -- message_removed / renamed --
	{
		"message_removed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\nmessage Keep {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Keep {\n  string id = 1;\n}\n"},
		"break/message_removed",
	},
	{
		"message_renamed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		map[string]string{"a.proto": hdr + "message Purchase {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		"break/message_renamed",
	},
	{
		"message_not_renamed_shape_differs",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Purchase {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		"break/message_removed",
	},

	// -- enum_removed / renamed --
	{
		"enum_removed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\nmessage Keep {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "message Keep {\n  string id = 1;\n}\n"},
		"break/enum_removed",
	},
	{
		"enum_renamed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum State {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		"break/enum_renamed",
	},

	// -- enum_value_removed / renamed / number_changed --
	{
		"enum_value_removed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n}\n"},
		"break/enum_value_removed",
	},
	{
		"enum_value_added",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		"",
	},
	{
		"enum_value_renamed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_DONE = 1;\n}\n"},
		"break/enum_value_renamed",
	},
	{
		"enum_value_number_changed",
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "enum Status {\n  STATUS_UNSPECIFIED = 0;\n  STATUS_OK = 2;\n  STATUS_RESERVED_1 = 1;\n}\n"},
		"break/enum_value_number_changed",
	},

	// -- service_removed / renamed --
	{
		"service_removed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\n",
		},
		"break/service_removed",
	},
	{
		"service_renamed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice OrderService {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		"break/service_renamed",
	},

	// -- method_removed / renamed / signature / streaming --
	{
		"method_removed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n  rpc List(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc List(Req) returns (Resp);\n}\n",
		},
		"break/method_removed",
	},
	{
		"method_added",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc List(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc List(Req) returns (Resp);\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		"",
	},
	{
		"method_renamed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Fetch(Req) returns (Resp);\n}\n",
		},
		"break/method_renamed",
	},
	{
		"method_signature_changed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nmessage Resp2 {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nmessage Resp2 {}\nservice Orders {\n  rpc Get(Req) returns (Resp2);\n}\n",
		},
		"break/method_signature_changed",
	},
	{
		"method_signature_unchanged",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n  rpc List(Req) returns (Resp);\n}\n",
		},
		"",
	},
	{
		"method_streaming_changed",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (stream Resp);\n}\n",
		},
		"break/method_streaming_changed",
	},
	{
		"method_streaming_unchanged",
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (stream Resp);\n}\n",
		},
		map[string]string{
			"a.proto": hdr + "message Req {}\nmessage Resp {}\nservice Orders {\n  rpc Get(Req) returns (stream Resp);\n  rpc List(Req) returns (stream Resp);\n}\n",
		},
		"",
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
		"break/package_removed",
	},
	{
		"package_renamed",
		map[string]string{"a.proto": hdr + "message Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": "syntax = \"proto3\";\npackage example.orders.v2;\nmessage Order {\n  string id = 1;\n}\n"},
		"break/package_renamed",
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
		"break/file_removed",
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
		"break/message_removed",
	},

	// -- go_package_changed --
	{
		"go_package_changed",
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\nmessage Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v2;ordersv2\";\nmessage Order {\n  string id = 1;\n}\n"},
		"break/go_package_changed",
	},
	{
		"go_package_unchanged",
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\nmessage Order {\n  string id = 1;\n}\n"},
		map[string]string{"a.proto": hdr + "option go_package = \"example.com/orders/v1;ordersv1\";\nmessage Order {\n  string id = 1;\n  int64 eta = 2;\n}\n"},
		"",
	},
}

func TestClassify(t *testing.T) {
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := classifyFixture(t, tc.prev, tc.cur)
			if tc.wantRule == "" {
				assertClean(t, findings)
				return
			}
			assertFires(t, findings, tc.wantRule)
		})
	}
}
