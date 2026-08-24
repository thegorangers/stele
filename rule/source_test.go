package rule_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/thegorangers/stele/rule"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const fixturePath = "example/v1/order.proto"

// fixture holds one of every kind of declaration a comment can lead, because
// the thing being tested is the walk from a source path back to a descriptor
// and the walk is a different branch for each kind.
const fixture = `syntax = "proto3";

package example.v1;

// TODO: rename this before anybody depends on it.
message Order {
  // The identifier.
  string id = 1;

  // A nested message.
  message Line {
    // TODO: this field is a nested one.
    string sku = 1;
  }

  // A nested enum.
  enum Kind {
    KIND_UNSPECIFIED = 0;
    // TODO: an enum value.
    KIND_STANDARD = 1;
  }

  oneof payment {
    string card = 2;
  }
}

// An enum at the top level.
enum Status {
  STATUS_UNSPECIFIED = 0;
}

// The service.
service Orders {
  // TODO: a method.
  rpc Get(Order) returns (Order);
}
`

func compileFixture(t *testing.T) rule.File {
	t.Helper()
	c := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(protocompile.ResolverFunc(
			func(p string) (protocompile.SearchResult, error) {
				if p != fixturePath {
					return protocompile.SearchResult{}, fmt.Errorf("%s: %w", p, os.ErrNotExist)
				}
				return protocompile.SearchResult{Source: strings.NewReader(fixture)}, nil
			})),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := c.Compile(context.Background(), fixturePath)
	if err != nil {
		t.Fatalf("compiling the fixture: %v", err)
	}
	return rule.File{Desc: files[0]}
}

// TestDescriptorAtNamesEveryKindOfDeclaration. A rule that works from source
// locations has a path and needs the declaration it names. Without this every
// such rule writes the walk itself, with the field numbers spelled out, and
// each one covers a different subset of the kinds.
func TestDescriptorAtNamesEveryKindOfDeclaration(t *testing.T) {
	f := compileFixture(t)
	for _, tc := range []struct {
		name string
		path protoreflect.SourcePath
		want string
	}{
		{"message", protoreflect.SourcePath{4, 0}, "example.v1.Order"},
		{"field", protoreflect.SourcePath{4, 0, 2, 0}, "example.v1.Order.id"},
		{"nested message", protoreflect.SourcePath{4, 0, 3, 0}, "example.v1.Order.Line"},
		{"nested field", protoreflect.SourcePath{4, 0, 3, 0, 2, 0}, "example.v1.Order.Line.sku"},
		{"nested enum", protoreflect.SourcePath{4, 0, 4, 0}, "example.v1.Order.Kind"},
		// An enum value's full name is scoped to the enum's parent, not to the
		// enum: that is the C++ scoping rule proto inherits, and it is why
		// stele/enum_value_prefix exists.
		{"enum value", protoreflect.SourcePath{4, 0, 4, 0, 2, 1}, "example.v1.Order.KIND_STANDARD"},
		{"oneof", protoreflect.SourcePath{4, 0, 8, 0}, "example.v1.Order.payment"},
		{"top-level enum", protoreflect.SourcePath{5, 0}, "example.v1.Status"},
		{"service", protoreflect.SourcePath{6, 0}, "example.v1.Orders"},
		{"method", protoreflect.SourcePath{6, 0, 2, 0}, "example.v1.Orders.Get"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := f.DescriptorAt(tc.path)
			if d == nil {
				t.Fatalf("DescriptorAt(%v) = nil, want %s", tc.path, tc.want)
			}
			if got := string(d.FullName()); got != tc.want {
				t.Errorf("DescriptorAt(%v) = %s, want %s", tc.path, got, tc.want)
			}
		})
	}
}

// TestDescriptorAtRefusesToGuess. A path that names something other than a
// declaration — the package statement, an index past the end, a path into a
// declaration's options — has no descriptor, and answering with the enclosing
// one would put a rule's finding on a subject the reader did not write.
func TestDescriptorAtRefusesToGuess(t *testing.T) {
	f := compileFixture(t)
	for _, p := range []protoreflect.SourcePath{
		{2},             // package
		{12},            // syntax
		{4, 99},         // no such message
		{4, 0, 2, 99},   // no such field
		{4, 0, 2, 0, 8}, // the field's options
		{99, 0},         // no such file element
		nil,             // no path at all
	} {
		if d := f.DescriptorAt(p); d != nil {
			t.Errorf("DescriptorAt(%v) = %s, want nil", p, d.FullName())
		}
	}
}

// TestCommentsNameWhatTheyAreOn is what a comment rule actually needs: every
// comment in the file, the declaration it leads, and a name for it. The
// example rule walked source locations by hand to get this, and handled two
// kinds out of the eight above.
func TestCommentsNameWhatTheyAreOn(t *testing.T) {
	f := compileFixture(t)
	got := make(map[string]string) // subject -> leading comment
	n := 0
	for c := range f.Comments() {
		n++
		if !strings.Contains(c.Leading+c.Trailing, "TODO") {
			continue
		}
		got[c.Subject] = strings.TrimSpace(c.Leading)
	}
	if n < 8 {
		t.Errorf("Comments() yielded %d comments; the fixture carries more than that", n)
	}
	want := map[string]string{
		"example.v1.Order":               "TODO: rename this before anybody depends on it.",
		"example.v1.Order.Line.sku":      "TODO: this field is a nested one.",
		"example.v1.Order.KIND_STANDARD": "TODO: an enum value.",
		"example.v1.Orders.Get":          "TODO: a method.",
	}
	for subject, comment := range want {
		if got[subject] != comment {
			t.Errorf("comment on %s = %q, want %q", subject, got[subject], comment)
		}
	}
	if len(got) != len(want) {
		t.Errorf("TODO comments found on %v, want exactly %v", keys(got), keys(want))
	}
}

// TestCommentsOnTheFileNameTheFile. A comment on the package or syntax
// statement is on no declaration, and a rule reporting it has to say
// something: the file is the honest subject, and it is supplied rather than
// left for each rule to remember.
func TestCommentsOnTheFileNameTheFile(t *testing.T) {
	f := compileFixture(t)
	for c := range f.Comments() {
		if c.Desc != nil {
			continue
		}
		if c.Subject != fixturePath {
			t.Errorf("a comment on no declaration names %q; the file is %q", c.Subject, fixturePath)
		}
	}
}

// TestCommentPosIsTheDeclaration pins the semantics of the position a comment
// carries: it is the declaration the comment is about, which is what a finding
// points at.
func TestCommentPosIsTheDeclaration(t *testing.T) {
	f := compileFixture(t)
	for c := range f.Comments() {
		if c.Subject != "example.v1.Order" {
			continue
		}
		// Line 6 is `message Order {`; line 5 is the comment.
		if c.Pos.Line != 6 {
			t.Errorf("the comment on Order has Pos.Line = %d, want 6 (the declaration)", c.Pos.Line)
		}
		return
	}
	t.Fatal("no comment on example.v1.Order")
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
