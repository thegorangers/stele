package lint_test

import (
	"testing"

	"github.com/thegorangers/stele/internal/lint"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestFindingNamesItsSubject is the identity a baseline is built on.
//
// A position is not it. Inserting a line above a finding moves every finding
// below it, so a record keyed on (file, rule, line) goes stale on an edit that
// changed nothing it is about — and a record that goes stale on unrelated
// edits is one people regenerate reflexively, which is the same as not having
// one. The full name of the declaration a finding is about survives
// reformatting, reordering and insertion, and it stops being the same name
// exactly when the declaration stops being the same declaration.
//
// The engine stamps it, from the position, for the same reason it stamps the
// rule id and the path: a rule that named its own subject could name somebody
// else's, and — the binding reason — a rule in another process cannot name one
// at all. rule.WireFinding carries a line and a column and nothing else, on
// purpose. An identity a hosted rule could not produce would be an identity
// that worked for built-in rules only.
func TestFindingNamesItsSubject(t *testing.T) {
	const src = `syntax = "proto3"; package example.v1;
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
}
message Order { string id = 1; }`
	res := checkFile(t, compileSource(t, dirtyPath, src), lint.Config{})
	got := map[string]string{}
	for _, f := range res.Findings {
		got[f.Rule] = f.Subject
	}
	// An enum value's full name is scoped to the enclosing scope rather than
	// to the enum, which is the C++ rule this very lint rule exists to
	// enforce — and it is also why the name is still unique: two enums in one
	// package cannot both declare PLACED, because protoc refuses the file.
	if want := "example.v1.PLACED"; got[prefixRule] != want {
		t.Errorf("subject of the %s finding is %q, want %q", prefixRule, got[prefixRule], want)
	}
}

// TestSubjectSurvivesAnEditAboveIt is the property the whole mechanism rests
// on, and the one a line number does not have.
func TestSubjectSurvivesAnEditAboveIt(t *testing.T) {
	const before = `syntax = "proto3"; package example.v1;
enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
}`
	const after = `syntax = "proto3"; package example.v1;

// An unrelated comment somebody added, which moves everything below it.
message Receipt { string id = 1; }

enum OrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0;
  PLACED = 1;
}`
	a := findingsFor(checkFile(t, compileSource(t, dirtyPath, before), lint.Config{}), prefixRule)
	b := findingsFor(checkFile(t, compileSource(t, dirtyPath, after), lint.Config{}), prefixRule)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("expected one finding either side: got %d and %d", len(a), len(b))
	}
	if a[0].Pos.Line == b[0].Pos.Line {
		t.Fatalf("the fixture did not move the finding, so it proves nothing")
	}
	if a[0].Subject != b[0].Subject {
		t.Errorf("the subject moved with the line: %q became %q", a[0].Subject, b[0].Subject)
	}
}

// TestFileLevelFindingHasNoSubject records the honest answer for a finding
// about the file itself. There is no declaration to name, and inventing one —
// the package, say — would let two different findings share an identity.
func TestFileLevelFindingHasNoSubject(t *testing.T) {
	res := checkFile(t, compileSource(t, dirtyPath, `syntax = "proto3";`), lint.Config{})
	for _, f := range res.Findings {
		if f.Rule != "stele/package_declared" {
			continue
		}
		if f.Subject != "" {
			t.Errorf("a file-level finding named a subject %q; it is about the file", f.Subject)
		}
		return
	}
	t.Fatal("the fixture produced no file-level finding")
}

func checkFile(t *testing.T, fd protoreflect.FileDescriptor, cfg lint.Config) lint.Result {
	t.Helper()
	e, err := lint.New(lint.Builtin(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e.Check([]protoreflect.FileDescriptor{fd})
}
