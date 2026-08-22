package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// The extractor is the instrument the golden data was measured with, so a
// wrong reading here would be silently baked into everything downstream. The
// test writes generated-looking code around a descriptor it built itself and
// checks the descriptor comes back whole.
func TestRawDescriptor_RoundTripsThroughGeneratedShapedCode(t *testing.T) {
	want := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("acme/widget/v1/types.proto"),
		Package: proto.String("acme.widget.v1"),
		Options: &descriptorpb.FileOptions{
			JavaPackage:     proto.String("com.acme.widget.v1"),
			ObjcClassPrefix: proto.String("AWX"),
		},
	}
	blob, err := proto.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "types.pb.go")
	if err := os.WriteFile(path, []byte(generatedShapedSource(blob)), 0o600); err != nil {
		t.Fatal(err)
	}

	raw, err := rawDescriptor(path)
	if err != nil {
		t.Fatal(err)
	}
	got := &descriptorpb.FileDescriptorProto{}
	if err := proto.Unmarshal(raw, got); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, want) {
		t.Errorf("descriptor did not survive extraction\n got: %v\nwant: %v", got, want)
	}
}

func TestRawDescriptor_ReportsAFileWithoutADescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.go")
	if err := os.WriteFile(path, []byte("package p\n\nvar x = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDescriptor(path); err == nil {
		t.Fatal("want an error for a file carrying no raw descriptor, got none")
	}
}

// generatedShapedSource mimics the layout generated code uses: a constant
// assembled from one string literal per line.
func generatedShapedSource(blob []byte) string {
	var b strings.Builder
	b.WriteString("package p\n\nconst file_acme_widget_v1_types_proto_rawDesc = \"\"")
	for _, chunk := range chunks(blob, 16) {
		b.WriteString(" +\n\t" + strconv.Quote(string(chunk)))
	}
	b.WriteString("\n")
	return b.String()
}

func chunks(b []byte, n int) [][]byte {
	var out [][]byte
	for len(b) > n {
		out = append(out, b[:n])
		b = b[n:]
	}
	return append(out, b)
}
