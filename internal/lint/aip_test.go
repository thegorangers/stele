package lint_test

import (
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/rule"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// aipCase is the same claim TestRulesFireAndDoNotFire makes, for the rules in
// the aip namespace: the rule says something about a file that breaks it, and
// says nothing about a file that keeps it. The clean fixture is not a trivial
// file — it is the dirty one with the violation repaired, so that a rule which
// fires on everything cannot pass.
type aipCase struct {
	rule  string
	clean string
	dirty string
	want  int // findings expected on dirty; 1 unless the case is about volume
}

const aipPreamble = `syntax = "proto3"; package example.v1;
import "google/protobuf/empty.proto";
import "google/protobuf/timestamp.proto";
`

func aipCases() []aipCase {
	return []aipCase{
		{
			rule: "aip/135_delete_returns_empty",
			clean: aipPreamble + `message Book { string name = 1; }
				message DeleteBookRequest { string name = 1; }
				service Library { rpc DeleteBook(DeleteBookRequest) returns (google.protobuf.Empty); }`,
			dirty: aipPreamble + `message Book { string name = 1; }
				message DeleteBookRequest { string name = 1; }
				message DeleteBookResponse {}
				service Library { rpc DeleteBook(DeleteBookRequest) returns (DeleteBookResponse); }`,
		},
		{
			rule: "aip/158_list_request_page_size",
			clean: aipPreamble + `message Book { string name = 1; }
				message ListBooksRequest { int32 page_size = 1; string page_token = 2; }
				message ListBooksResponse { repeated Book books = 1; string next_page_token = 2; }
				service Library { rpc ListBooks(ListBooksRequest) returns (ListBooksResponse); }`,
			dirty: aipPreamble + `message Book { string name = 1; }
				message ListBooksRequest { string page_token = 2; }
				message ListBooksResponse { repeated Book books = 1; string next_page_token = 2; }
				service Library { rpc ListBooks(ListBooksRequest) returns (ListBooksResponse); }`,
		},
		{
			rule: "aip/158_list_request_page_token",
			clean: aipPreamble + `message Book { string name = 1; }
				message ListBooksRequest { int32 page_size = 1; string page_token = 2; }
				message ListBooksResponse { repeated Book books = 1; string next_page_token = 2; }
				service Library { rpc ListBooks(ListBooksRequest) returns (ListBooksResponse); }`,
			dirty: aipPreamble + `message Book { string name = 1; }
				message ListBooksRequest { int32 page_size = 1; int64 page_token = 2; }
				message ListBooksResponse { repeated Book books = 1; string next_page_token = 2; }
				service Library { rpc ListBooks(ListBooksRequest) returns (ListBooksResponse); }`,
		},
		{
			rule: "aip/158_list_response_next_page_token",
			clean: aipPreamble + `message Book { string name = 1; }
				message ListBooksRequest { int32 page_size = 1; string page_token = 2; }
				message ListBooksResponse { repeated Book books = 1; string next_page_token = 2; }
				service Library { rpc ListBooks(ListBooksRequest) returns (ListBooksResponse); }`,
			dirty: aipPreamble + `message Book { string name = 1; }
				message ListBooksRequest { int32 page_size = 1; string page_token = 2; }
				message ListBooksResponse { repeated Book books = 1; }
				service Library { rpc ListBooks(ListBooksRequest) returns (ListBooksResponse); }`,
		},
		{
			rule: "aip/142_timestamp_field_time_suffix",
			clean: aipPreamble + `message Book {
					google.protobuf.Timestamp create_time = 1;
					repeated google.protobuf.Timestamp review_times = 2;
				}`,
			dirty: aipPreamble + `message Book {
					google.protobuf.Timestamp created_at = 1;
					repeated google.protobuf.Timestamp review_times = 2;
				}`,
		},
	}
}

func TestAIPRulesFireAndDoNotFire(t *testing.T) {
	for _, tc := range aipCases() {
		t.Run(tc.rule, func(t *testing.T) {
			e, err := lint.New(lint.Builtin(), lint.Config{})
			if err != nil {
				t.Fatal(err)
			}
			const path = "example/v1/a.proto"
			clean := e.Check([]protoreflect.FileDescriptor{compileSource(t, path, tc.clean)})
			if got := findingsFor(clean, tc.rule); len(got) != 0 {
				t.Errorf("the rule fires on a file that keeps it: %v", got)
			}
			want := tc.want
			if want == 0 {
				want = 1
			}
			dirty := e.Check([]protoreflect.FileDescriptor{compileSource(t, path, tc.dirty)})
			got := findingsFor(dirty, tc.rule)
			if len(got) != want {
				t.Fatalf("want %d finding(s) on a file that breaks the rule, got %d: %v", want, len(got), got)
			}
			if got[0].Pos.Line == 0 {
				t.Errorf("the finding carries no line; it is read in a CI log by somebody who must open the file")
			}
			if got[0].Fix == "" {
				t.Errorf("the finding says what is wrong but not what to do about it")
			}
			if !strings.Contains(got[0].String(), path) {
				t.Errorf("the rendered finding does not name the file:\n%s", got[0])
			}
		})
	}
}

// TestAIPRulesAreWarningsByDefault records the decision that overrides the
// design document: the rules are on for every repository and none of them can
// fail a build until somebody says it should. A profile nobody enables ships
// dead; a rule that reddens a fleet on upgrade gets switched off.
func TestAIPRulesAreWarningsByDefault(t *testing.T) {
	e, err := lint.New(lint.Builtin(), lint.Config{})
	if err != nil {
		t.Fatal(err)
	}
	src := aipPreamble + `message Book { google.protobuf.Timestamp created_at = 1; }`
	res := e.Check([]protoreflect.FileDescriptor{compileSource(t, "example/v1/a.proto", src)})
	var aip int
	for _, f := range res.Findings {
		if rule.Namespace(f.Rule) != rule.NamespaceAIP {
			continue
		}
		aip++
		if f.Severity != lint.SeverityWarning {
			t.Errorf("%s is at severity %s, not warning", f.Rule, f.Severity)
		}
	}
	if aip == 0 {
		t.Fatal("the fixture triggered no aip rule at all, so this proves nothing")
	}
	if res.Errors != 0 {
		t.Errorf("an unconfigured run counted %d error(s); no aip rule may fail a build by default", res.Errors)
	}
}

// TestAIPSeverityIsStillTheRepositorysToSet holds the other half: the default
// is a default and not a ceiling. A repository that has fixed its contracts
// can hold them.
func TestAIPSeverityIsStillTheRepositorysToSet(t *testing.T) {
	cfg := lint.Config{Rules: map[string]lint.RuleConfig{
		"aip/142_timestamp_field_time_suffix": {Severity: lint.SeverityError},
	}}
	e, err := lint.New(lint.Builtin(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	src := aipPreamble + `message Book { google.protobuf.Timestamp created_at = 1; }`
	res := e.Check([]protoreflect.FileDescriptor{compileSource(t, "example/v1/a.proto", src)})
	got := findingsFor(res, "aip/142_timestamp_field_time_suffix")
	if len(got) != 1 || got[0].Severity != lint.SeverityError {
		t.Fatalf("a repository that raised the rule to error got %v", got)
	}
}
