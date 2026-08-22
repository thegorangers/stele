package compile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/compile"
	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/resolve"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
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

// fakeFetch serves repositories from local directories: no network, no git.
func fakeFetch(repos map[string]string) resolve.FetchFunc {
	return func(_ context.Context, git, ref string) (string, string, error) {
		dir, ok := repos[git]
		if !ok {
			return "", "", errors.New("no such repository: " + git)
		}
		return dir, strings.Repeat("a", 33) + "-" + ref, nil
	}
}

// graphWithExternalImport builds a graph whose root module imports a file
// supplied by a dependency, so that compilation has to go through the graph
// rather than through the working directory.
func graphWithExternalImport(t *testing.T, rootFile string) *resolve.Graph {
	t.Helper()
	producer := repo(t, "producer", map[string]string{
		"stele.yaml":                  "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/type/money.proto": "syntax = \"proto3\";\npackage acme.type;\nmessage Money { int64 units = 1; }\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": rootFile,
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "type", Git: "gh:acme/producer", Ref: "v1.0.0", Module: "proto"}},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg,
		fakeFetch(map[string]string{"gh:acme/producer": producer}))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

const orderProto = `syntax = "proto3";

package example.v1;

import "acme/type/money.proto";

// Order is a customer order.
message Order {
  // total is what the customer pays.
  acme.type.Money total = 1;
}
`

// TestCompile_ExternalImportThroughGraph pins that the file accessor reads
// through the graph: the imported file lives in another repository's tree and
// is nowhere near the working directory.
func TestCompile_ExternalImportThroughGraph(t *testing.T) {
	g := graphWithExternalImport(t, orderProto)

	files, err := compile.Compile(context.Background(), g, []string{"example/v1/order.proto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("compiled files: got %d, want 1", len(files))
	}
	fd := protodesc.ToFileDescriptorProto(files[0])
	if fd.GetName() != "example/v1/order.proto" {
		t.Fatalf("file name: got %q", fd.GetName())
	}
	if len(fd.GetDependency()) != 1 || fd.GetDependency()[0] != "acme/type/money.proto" {
		t.Fatalf("dependency: got %v", fd.GetDependency())
	}
}

// TestCompile_RetainsSourceInfo pins the requirement that costs every comment
// in generated code if it is missed: the library default drops SourceCodeInfo.
func TestCompile_RetainsSourceInfo(t *testing.T) {
	g := graphWithExternalImport(t, orderProto)

	files, err := compile.Compile(context.Background(), g, []string{"example/v1/order.proto"})
	if err != nil {
		t.Fatal(err)
	}
	fd := protodesc.ToFileDescriptorProto(files[0])
	si := fd.GetSourceCodeInfo()
	if si == nil || len(si.GetLocation()) == 0 {
		t.Fatal("SourceCodeInfo is absent or empty")
	}
	var comments []string
	for _, loc := range si.GetLocation() {
		if c := loc.GetLeadingComments(); c != "" {
			comments = append(comments, c)
		}
	}
	if len(comments) < 2 {
		t.Fatalf("leading comments: got %v, want the message and the field comment", comments)
	}
}

// TestCompile_ErrorNamesFileAndLine pins that a broken proto is reported with
// coordinates rather than swallowed.
func TestCompile_ErrorNamesFileAndLine(t *testing.T) {
	broken := "syntax = \"proto3\";\n\npackage example.v1;\n\nmessage Order {\n  int32 total = ;\n}\n"
	g := graphWithExternalImport(t, broken)

	_, err := compile.Compile(context.Background(), g, []string{"example/v1/order.proto"})
	if err == nil {
		t.Fatal("compiling a broken proto succeeded")
	}
	msg := err.Error()
	if !strings.Contains(msg, "example/v1/order.proto") {
		t.Fatalf("error does not name the file: %s", msg)
	}
	if !strings.Contains(msg, ":6:") {
		t.Fatalf("error does not name line 6: %s", msg)
	}
}

// TestCompile_DeterministicOrderAndBytes pins §6.3: the descriptor bytes are
// the acceptance criterion, so neither the order of the returned files nor
// their contents may depend on the order the targets were named in.
func TestCompile_DeterministicOrderAndBytes(t *testing.T) {
	producer := repo(t, "producer", map[string]string{
		"stele.yaml":                  "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/type/money.proto": "syntax = \"proto3\";\npackage acme.type;\nmessage Money { int64 units = 1; }\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": orderProto,
		"api/example/v1/place.proto": "syntax = \"proto3\";\npackage example.v1;\nmessage Place { string id = 1; }\n",
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "type", Git: "gh:acme/producer", Ref: "v1.0.0", Module: "proto"}},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg,
		fakeFetch(map[string]string{"gh:acme/producer": producer}))
	if err != nil {
		t.Fatal(err)
	}

	bytesOf := func(targets []string) []byte {
		t.Helper()
		files, err := compile.Compile(context.Background(), g, targets)
		if err != nil {
			t.Fatal(err)
		}
		set := &descriptorpb.FileDescriptorSet{}
		for _, f := range files {
			set.File = append(set.File, protodesc.ToFileDescriptorProto(f))
		}
		raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(set)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	forward := bytesOf([]string{"example/v1/order.proto", "example/v1/place.proto"})
	reverse := bytesOf([]string{"example/v1/place.proto", "example/v1/order.proto"})
	if string(forward) != string(reverse) {
		t.Fatal("the descriptor bytes depend on the order the targets were named in")
	}
	again := bytesOf([]string{"example/v1/order.proto", "example/v1/place.proto"})
	if string(forward) != string(again) {
		t.Fatal("the descriptor bytes vary between runs")
	}
}

// TestCompile_UnknownTargetIsError pins the global constraint that an empty or
// unmatched selection is an error naming what did not match.
func TestCompile_UnknownTargetIsError(t *testing.T) {
	g := graphWithExternalImport(t, orderProto)

	_, err := compile.Compile(context.Background(), g, []string{"example/v1/absent.proto"})
	if err == nil {
		t.Fatal("compiling an unknown target succeeded")
	}
	if !strings.Contains(err.Error(), "example/v1/absent.proto") {
		t.Fatalf("error does not name the target: %v", err)
	}
}

// TestCompile_NoTargetsIsError pins that an empty result is never a success.
func TestCompile_NoTargetsIsError(t *testing.T) {
	g := graphWithExternalImport(t, orderProto)

	if _, err := compile.Compile(context.Background(), g, nil); err == nil {
		t.Fatal("compiling nothing succeeded")
	}
}
