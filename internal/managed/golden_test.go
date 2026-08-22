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
		GoPackagePrefix: managed.Override{
			Path:  "acme",
			Value: "github.com/acme/project/gen",
		},
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
		GoPackagePrefix: managed.Override{Path: "acme", Value: "github.com/acme/project/gen"},
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
