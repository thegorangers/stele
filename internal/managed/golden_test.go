package managed_test

import (
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/thegorangers/stele/internal/managed"
)

// The golden files hold options extracted from real generated code, so this
// test is the only statement in the repository about what a managed-mode
// generator emits. Anything the synthesiser adds, drops or spells differently
// shows up here as a diff against a measurement.
func TestApply_MatchesGolden(t *testing.T) {
	cfg := managed.Config{
		GoPackagePrefix: []managed.Override{{
			Path:  "acme",
			Value: "github.com/acme/project/gen",
		}},
	}

	for _, tc := range []struct {
		golden      string
		file        string
		protoPkg    string
		sourceGoPkg string
	}{
		{
			golden:   "two_part_package",
			file:     "acme/widget/v1/types.proto",
			protoPkg: "acme.widget.v1",
			// The source file already names a Go package. Managed mode
			// replaces it: the artefact this golden came from did exactly
			// that, appending an alias the source never had.
			sourceGoPkg: "github.com/acme/elsewhere/gen/acme/widget/v1",
		},
		{
			golden:   "three_part_package",
			file:     "acme/widget/events/v1/events.proto",
			protoPkg: "acme.widget.events.v1",
		},
		{
			golden:   "underscored_file_name",
			file:     "acme/widget/events/v1/merchant_status.proto",
			protoPkg: "acme.widget.events.v1",
		},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			fd := &descriptorpb.FileDescriptorProto{
				Name:    proto.String(tc.file),
				Package: proto.String(tc.protoPkg),
			}
			if tc.sourceGoPkg != "" {
				fd.Options = &descriptorpb.FileOptions{GoPackage: proto.String(tc.sourceGoPkg)}
			}

			managed.Apply(fd, cfg)

			assertOptionsEqualGolden(t, tc.golden, fd.GetOptions())
		})
	}
}

// TestApply_LeavesGoPackageAloneOutsideTheSelectedPath covers a rule the
// artefacts could not: every measured file sat under the configured path, so
// nothing observed says what happens outside it. The behaviour asserted here
// is our own decision — an override with a path selector applies to that path
// and to nothing else — and is recorded as a decision, not as a measurement.
func TestApply_LeavesGoPackageAloneOutsideTheSelectedPath(t *testing.T) {
	fd := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("other/widget/v1/types.proto"),
		Package: proto.String("other.widget.v1"),
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("github.com/other/gen/other/widget/v1")},
	}

	managed.Apply(fd, managed.Config{
		GoPackagePrefix: []managed.Override{{Path: "acme", Value: "github.com/acme/project/gen"}},
	})

	if got, want := fd.GetOptions().GetGoPackage(), "github.com/other/gen/other/widget/v1"; got != want {
		t.Errorf("go_package = %q, want the source value %q left untouched", got, want)
	}
	if got := fd.GetOptions().GetJavaPackage(); got != "com.other.widget.v1" {
		t.Errorf("java_package = %q, want the other options still synthesised", got)
	}
}

// assertOptionsEqualGolden compares options against a golden file by value,
// not by text: prototext output is deliberately unstable, so a textual diff
// would fail on whitespace the format is free to change.
func assertOptionsEqualGolden(t *testing.T, name string, got *descriptorpb.FileOptions) {
	t.Helper()

	path := filepath.Join("testdata", name+".golden")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := &descriptorpb.FileOptions{}
	if err := prototext.Unmarshal(b, want); err != nil {
		t.Fatalf("%s: %v", path, err)
	}

	if proto.Equal(got, want) {
		return
	}
	t.Errorf("options differ from %s\n got: %s\nwant: %s",
		path, mustText(t, got), mustText(t, want))
}

func mustText(t *testing.T, m proto.Message) string {
	t.Helper()
	b, err := prototext.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return "\n" + string(b)
}

// TestApply_LastMatchingOverrideWins pins how several path-scoped overrides of
// one file option settle, which is a measurement rather than a decision.
//
// Measured against the reference tool at 1.48.0, on a workspace with one
// module root holding one/v1/a.proto, two/v1/b.proto and two/deep/v1/c.proto:
//
//	buf.gen.yaml override order        file            prefix applied
//	(none), two, two/deep              two/deep/v1/c   two/deep
//	two/deep, two, (none)              two/deep/v1/c   (none)
//
// So it is not the most specific selector that wins, it is the last one
// declared that matches. The second row is what settles it: reversing the
// order alone changes the answer.
func TestApply_LastMatchingOverrideWins(t *testing.T) {
	apply := func(name string, overrides ...managed.Override) string {
		fd := &descriptorpb.FileDescriptorProto{
			Name:    proto.String(name),
			Package: proto.String("acme.widget.v1"),
		}
		managed.Apply(fd, managed.Config{GoPackagePrefix: overrides})
		return fd.GetOptions().GetGoPackage()
	}

	broad := managed.Override{Value: "example.com/root/gen"}
	narrow := managed.Override{Path: "acme/widget", Value: "example.com/widget/gen"}

	if got, want := apply("acme/widget/v1/types.proto", broad, narrow),
		"example.com/widget/gen/acme/widget/v1;widgetv1"; got != want {
		t.Errorf("go_package = %q, want %q: the later, narrower override matches and wins", got, want)
	}
	if got, want := apply("acme/widget/v1/types.proto", narrow, broad),
		"example.com/root/gen/acme/widget/v1;widgetv1"; got != want {
		t.Errorf("go_package = %q, want %q: the later, broader override matches and wins too — it is order, not specificity", got, want)
	}
	if got, want := apply("acme/other/v1/types.proto", broad, narrow),
		"example.com/root/gen/acme/other/v1;otherv1"; got != want {
		t.Errorf("go_package = %q, want %q: the narrow override does not match this file", got, want)
	}
	if got := apply("acme/other/v1/types.proto", narrow); got != "" {
		t.Errorf("go_package = %q, want it left unset: no override matches this file", got)
	}
}
