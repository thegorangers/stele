package genreq_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile/linker"
	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/genreq"
	"github.com/thegorangers/stele/internal/managed"
	"github.com/thegorangers/stele/internal/resolve"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// repo lays out one synthetic repository under a temporary directory.
func repo(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func fakeFetch(repos map[string]string) resolve.FetchFunc {
	return func(_ context.Context, git, ref string) (string, string, error) {
		dir, ok := repos[git]
		if !ok {
			return "", "", errors.New("no such repository: " + git)
		}
		return dir, strings.Repeat("a", 33) + "-" + ref, nil
	}
}

// diamond builds an import graph shaped like a diamond, which a walk that
// merely follows one chain would get wrong:
//
//	       base.proto
//	      /          \
//	left.proto    right.proto
//	      \          /
//	        top.proto   (the only target)
//
// base is imported twice, so a walk must also refuse to emit it twice.
func diamond(t *testing.T) linker.Files {
	t.Helper()
	producer := repo(t, "producer", map[string]string{
		"stele.yaml": "version: 1\nmodules:\n  - path: proto\n",
		"proto/dep/v1/base.proto": `syntax = "proto3";
package dep.v1;
// Base carries the amount.
message Base { int64 units = 1; }
`,
		"proto/dep/v1/left.proto": `syntax = "proto3";
package dep.v1;
import "dep/v1/base.proto";
message Left { Base base = 1; }
`,
		"proto/dep/v1/right.proto": `syntax = "proto3";
package dep.v1;
import "dep/v1/base.proto";
message Right { Base base = 1; }
`,
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/top.proto": topProto,
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "dep", Git: "gh:acme/producer", Ref: "v1.0.0", Module: "proto"}},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg,
		fakeFetch(map[string]string{"gh:acme/producer": producer}))
	if err != nil {
		t.Fatal(err)
	}
	files, err := compile.Compile(context.Background(), g, []string{"example/v1/top.proto"})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

const topProto = `syntax = "proto3";

package example.v1;

import "dep/v1/left.proto";
import "dep/v1/right.proto";

// Top is the target message.
message Top {
  // left_side is the left branch.
  dep.v1.Left left_side = 1;
  dep.v1.Right right_side = 2;
}
`

func mustBuild(t *testing.T, files linker.Files) *pluginpb.CodeGeneratorRequest {
	t.Helper()
	req, err := genreq.Build(files, genreq.Target{})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func indexByName(fds []*descriptorpb.FileDescriptorProto) map[string]int {
	idx := make(map[string]int, len(fds))
	for i, fd := range fds {
		idx[fd.GetName()] = i
	}
	return idx
}

// TestBuild_ProtoFileIsTopologicallyOrdered pins the ordering the plugin
// protocol requires: an import must appear before whatever imports it.
func TestBuild_ProtoFileIsTopologicallyOrdered(t *testing.T) {
	req := mustBuild(t, diamond(t))
	idx := indexByName(req.ProtoFile)

	for _, fd := range req.ProtoFile {
		for _, dep := range fd.GetDependency() {
			di, ok := idx[dep]
			if !ok {
				t.Fatalf("%s imports %s, which is missing from proto_file", fd.GetName(), dep)
			}
			if di > idx[fd.GetName()] {
				t.Errorf("%s (at %d) imports %s (at %d): the import must come first",
					fd.GetName(), idx[fd.GetName()], dep, di)
			}
		}
	}
	if len(idx) != len(req.ProtoFile) {
		t.Errorf("proto_file has duplicates: %d entries, %d distinct names", len(req.ProtoFile), len(idx))
	}
	for _, want := range []string{"dep/v1/base.proto", "dep/v1/left.proto", "dep/v1/right.proto", "example/v1/top.proto"} {
		if _, ok := idx[want]; !ok {
			t.Errorf("proto_file is missing %s", want)
		}
	}
}

// TestBuild_FileToGenerateHoldsTargetsOnly pins that imports are carried for
// the plugin to read, not for it to generate code from.
func TestBuild_FileToGenerateHoldsTargetsOnly(t *testing.T) {
	req := mustBuild(t, diamond(t))
	want := []string{"example/v1/top.proto"}
	if !slices.Equal(req.FileToGenerate, want) {
		t.Fatalf("file_to_generate: got %v, want %v", req.FileToGenerate, want)
	}
}

// TestBuild_SourceCodeInfoKeptForTargetsStrippedForImports pins both halves.
// Getting it wrong in one direction loses every comment in generated code; in
// the other it bloats every embedded descriptor with the imports' source info.
func TestBuild_SourceCodeInfoKeptForTargetsStrippedForImports(t *testing.T) {
	req := mustBuild(t, diamond(t))
	for _, fd := range req.ProtoFile {
		isTarget := slices.Contains(req.FileToGenerate, fd.GetName())
		switch {
		case isTarget && fd.SourceCodeInfo == nil:
			t.Errorf("%s: target lost its SourceCodeInfo — comments would vanish", fd.GetName())
		case !isTarget && fd.SourceCodeInfo != nil:
			t.Errorf("%s: import kept its SourceCodeInfo — descriptors would bloat", fd.GetName())
		}
	}
	// A retained SourceCodeInfo that carries no comment is retained in name
	// only, so check the comment itself survives.
	var found bool
	for _, fd := range req.ProtoFile {
		if fd.GetName() != "example/v1/top.proto" {
			continue
		}
		for _, loc := range fd.GetSourceCodeInfo().GetLocation() {
			if strings.Contains(loc.GetLeadingComments(), "left_side is the left branch") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the target's leading comments are not in its SourceCodeInfo")
	}
}

// TestBuild_JSONNameOnEveryField asserts that what protocompile already sets
// survives the copying, stripping and option rewriting done here.
func TestBuild_JSONNameOnEveryField(t *testing.T) {
	req := mustBuild(t, diamond(t))
	var checked int
	for _, fd := range req.ProtoFile {
		for _, msg := range fd.GetMessageType() {
			for _, f := range msg.GetField() {
				checked++
				if f.GetJsonName() == "" {
					t.Errorf("%s: %s.%s has no json_name", fd.GetName(), msg.GetName(), f.GetName())
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no fields were checked: the fixture proves nothing")
	}
	// The one field whose json_name differs from its name.
	var got string
	for _, fd := range req.ProtoFile {
		if fd.GetName() != "example/v1/top.proto" {
			continue
		}
		got = fd.GetMessageType()[0].GetField()[0].GetJsonName()
	}
	if got != "leftSide" {
		t.Errorf("json_name for left_side: got %q, want %q", got, "leftSide")
	}
}

// TestBuild_StripsSourceRetentionOptions pins that an option declared
// retention = RETENTION_SOURCE never reaches the plugin.
func TestBuild_StripsSourceRetentionOptions(t *testing.T) {
	files := withRetentionOption(t)
	req := mustBuild(t, files)

	for _, fd := range req.ProtoFile {
		if fd.GetName() != "example/v1/opt.proto" {
			continue
		}
		b, err := proto.Marshal(fd.GetOptions())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "kept-in-source-only") {
			t.Errorf("%s: a source-retention option reached the plugin", fd.GetName())
		}
		if !strings.Contains(string(b), "kept-everywhere") {
			t.Errorf("%s: a runtime-retention option was stripped as well", fd.GetName())
		}
	}
}

// withRetentionOption compiles a file carrying two custom file options: one
// retained at runtime, one only in source.
func withRetentionOption(t *testing.T) linker.Files {
	t.Helper()
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/ext.proto": `syntax = "proto2";
package example.v1;
import "google/protobuf/descriptor.proto";
extend google.protobuf.FileOptions {
  optional string runtime_note = 50001;
  optional string source_note = 50002 [retention = RETENTION_SOURCE];
}
`,
		"api/example/v1/opt.proto": `syntax = "proto3";
package example.v1;
import "example/v1/ext.proto";
option (example.v1.runtime_note) = "kept-everywhere";
option (example.v1.source_note) = "kept-in-source-only";
message Opt { string name = 1; }
`,
	})
	cfg := &config.File{Version: 1, Modules: []config.Module{{Path: "api"}}}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(nil))
	if err != nil {
		t.Fatal(err)
	}
	files, err := compile.Compile(context.Background(), g, []string{"example/v1/opt.proto"})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestBuild_LeavesCompilerVersionUnset pins the measurement that settled the
// open question: the tool being replaced sends no compiler_version, and
// protoc-gen-go writes whatever is there into a comment in every file it
// emits. Setting the field, however truthfully, would differ in every
// generated file.
func TestBuild_LeavesCompilerVersionUnset(t *testing.T) {
	req := mustBuild(t, diamond(t))
	if req.CompilerVersion != nil {
		t.Fatalf("compiler_version = %v, want unset", req.CompilerVersion)
	}
}

// TestBuild_Deterministic pins the property the whole acceptance criterion
// rests on: same input, same bytes.
func TestBuild_Deterministic(t *testing.T) {
	files := diamond(t)
	opts := proto.MarshalOptions{Deterministic: true}
	first, err := opts.Marshal(mustBuild(t, files))
	if err != nil {
		t.Fatal(err)
	}
	second, err := opts.Marshal(mustBuild(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first, second) {
		t.Fatal("two builds over the same input marshalled differently")
	}
}

// TestBuild_AppliesManagedOptions pins the wiring: when the target asks for
// managed options they are in the descriptors the plugin sees, and when it
// does not, the file's own options are left alone.
func TestBuild_AppliesManagedOptions(t *testing.T) {
	files := diamond(t)

	plain := mustBuild(t, files)
	for _, fd := range plain.ProtoFile {
		if fd.GetOptions().GetJavaPackage() != "" {
			t.Fatalf("%s: java_package was synthesised without being asked for", fd.GetName())
		}
	}

	req, err := genreq.Build(files, genreq.Target{
		Managed: &managed.Config{GoPackagePrefix: managed.Override{Value: "example.com/gen"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range req.ProtoFile {
		if strings.HasPrefix(fd.GetName(), "google/protobuf/") {
			continue
		}
		if fd.GetOptions().GetJavaPackage() == "" {
			t.Errorf("%s: managed options were not applied", fd.GetName())
		}
	}
	var top *descriptorpb.FileDescriptorProto
	for _, fd := range req.ProtoFile {
		if fd.GetName() == "example/v1/top.proto" {
			top = fd
		}
	}
	if got, want := top.GetOptions().GetGoPackage(), "example.com/gen/example/v1;examplev1"; got != want {
		t.Errorf("go_package: got %q, want %q", got, want)
	}
	// Building without managed options after building with them must not see
	// the earlier run's mutation: Apply rewrites in place, so the request has
	// to own its descriptors.
	again := mustBuild(t, files)
	for _, fd := range again.ProtoFile {
		if fd.GetOptions().GetJavaPackage() != "" {
			t.Errorf("%s: an earlier build leaked its mutation into the compiled files", fd.GetName())
		}
	}
}

// TestBuild_Parameter passes the plugin's opt through unchanged.
func TestBuild_Parameter(t *testing.T) {
	req, err := genreq.Build(diamond(t), genreq.Target{Parameter: "paths=source_relative"})
	if err != nil {
		t.Fatal(err)
	}
	if req.GetParameter() != "paths=source_relative" {
		t.Errorf("parameter: got %q", req.GetParameter())
	}
}

// TestBuild_NoFiles refuses an empty request rather than producing one.
func TestBuild_NoFiles(t *testing.T) {
	if _, err := genreq.Build(nil, genreq.Target{}); err == nil {
		t.Fatal("expected an error for an empty file set")
	}
}
