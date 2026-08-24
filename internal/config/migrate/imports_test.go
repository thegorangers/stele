package migrate_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thegorangers/stele/internal/config/migrate"
)

// repo builds a working copy in memory. The migration reads the repository's
// own protos, so a test that says anything about dependencies has to carry
// them.
func repo(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return out
}

func mustMigrateFS(t *testing.T, files map[string]string) *migrate.Result {
	t.Helper()
	r, err := migrate.FromFS(repo(files))
	if err != nil {
		t.Fatalf("FromFS: %v", err)
	}
	return r
}

// bufYAMLVendored and friends are the shape every measured repository has:
// one owned module, one vendored tree filled by a `vendor` target.
const (
	bufYAMLVendored = "version: v2\nmodules: [{path: api}, {path: third_party/proto}]\n"
	// pinnedPlugin is the `go install` the plugin version is recovered from.
	// Without it every one of these migrations is incomplete for a reason
	// that has nothing to do with what is under test.
	pinnedPlugin    = "V ?= v1.36.6\ngen:\n\t@go install google.golang.org/protobuf/cmd/protoc-gen-go@$(V)\n"
	genYAMLVendored = `
version: v2
plugins: [{local: protoc-gen-go, out: gen}]
inputs:
  - directory: api
`
)

// TestOnlyImportedDependenciesAreDemanded is defect one. The vendor target
// exports two registry modules; exactly one file out of either is imported.
// Reporting both, every time, is boilerplate rather than a fact about this
// repository, and it teaches its reader to paste both in without looking.
func TestOnlyImportedDependenciesAreDemanded(t *testing.T) {
	r := mustMigrateFS(t, map[string]string{
		"buf.yaml":     bufYAMLVendored,
		"buf.gen.yaml": genYAMLVendored,
		"Makefile": pinnedPlugin + "vendor:\n" +
			"\t@buf export buf.build/googleapis/googleapis --output=third_party/proto\n" +
			"\t@buf export buf.build/bufbuild/protovalidate --output=third_party/proto\n",
		"api/example/orders/v1/orders.proto": `syntax = "proto3";
package example.orders.v1;
import "google/type/money.proto";
import "google/protobuf/timestamp.proto";
`,
		"third_party/proto/google/type/money.proto":      "syntax = \"proto3\";\npackage google.type;\n",
		"third_party/proto/google/type/latlng.proto":     "syntax = \"proto3\";\npackage google.type;\n",
		"third_party/proto/buf/validate/validate.proto":  "syntax = \"proto3\";\npackage buf.validate;\nimport \"google/protobuf/descriptor.proto\";\n",
		"third_party/proto/google/api/annotations.proto": "syntax = \"proto3\";\npackage google.api;\nimport \"google/api/http.proto\";\n",
		"third_party/proto/google/api/http.proto":        "syntax = \"proto3\";\npackage google.api;\n",
	})
	joined := strings.Join(r.Unresolved, "\n")
	if len(r.Unresolved) != 1 {
		t.Fatalf("unresolved = %#v, want exactly the one file that is imported", r.Unresolved)
	}
	if !strings.Contains(joined, "google/type/money.proto") {
		t.Errorf("unresolved does not name the imported file:\n%s", joined)
	}
	for _, never := range []string{"latlng", "validate.proto", "annotations.proto"} {
		if strings.Contains(joined, never) {
			t.Errorf("unresolved demands %s, which nothing imports:\n%s", never, joined)
		}
	}
}

// TestNothingImportedDemandsNothing is the same defect at its extreme: a
// repository whose only external import is a well-known type the compiler
// carries. It was told to supply two third-party modules and needs neither.
func TestNothingImportedDemandsNothing(t *testing.T) {
	r := mustMigrateFS(t, map[string]string{
		"buf.yaml":     "version: v2\nmodules: [{path: api}]\n",
		"buf.gen.yaml": genYAMLVendored,
		"Makefile": pinnedPlugin + "vendor:\n" +
			"\t@buf export buf.build/googleapis/googleapis --output=third_party/proto\n" +
			"\t@buf export buf.build/bufbuild/protovalidate --output=third_party/proto\n",
		"api/example/staff/v1/staff.proto": `syntax = "proto3";
package example.staff.v1;
import "google/protobuf/timestamp.proto";
`,
	})
	if !r.Complete() {
		t.Fatalf("unresolved = %#v, want none: the only external import is a well-known type", r.Unresolved)
	}
	// The vendor target is still evidence of intent, so it is reported —
	// as a hint, in the channel that does not block.
	if !strings.Contains(strings.Join(r.Notes, "\n"), "buf.build/googleapis/googleapis") {
		t.Errorf("notes = %#v, want the unused vendor export reported", r.Notes)
	}
}

// TestDependencyIsNarrowedToWhatIsImported is defect two. `buf export` pulled
// a whole module; one file of it is read. The narrowing is derivable from the
// imports, and every hand-written manifest needed it.
func TestDependencyIsNarrowedToWhatIsImported(t *testing.T) {
	r := mustMigrateFS(t, map[string]string{
		"buf.yaml":     bufYAMLVendored,
		"buf.gen.yaml": genYAMLVendored,
		"Makefile": pinnedPlugin + "vendor:\n" +
			"\t@buf export \"ssh://git@git.example.com/group/catalog.git#subdir=api,ref=0123456789abcdef0123456789abcdef01234567\" --exclude-imports --output=third_party/proto\n",
		"api/example/orders/v1/orders.proto": `syntax = "proto3";
package example.orders.v1;
import "example/catalog/events/v1/events.proto";
`,
		"third_party/proto/example/catalog/events/v1/events.proto": "syntax = \"proto3\";\npackage example.catalog.events.v1;\n",
		"third_party/proto/example/catalog/v1/messages.proto":      "syntax = \"proto3\";\npackage example.catalog.v1;\n",
		"third_party/proto/example/catalog/v1/service.proto":       "syntax = \"proto3\";\npackage example.catalog.v1;\n",
	})
	if len(r.File.Deps) != 1 {
		t.Fatalf("deps = %#v, want the one recovered dependency", r.File.Deps)
	}
	got := r.File.Deps[0].Paths
	if len(got) != 1 || got[0] != "example/catalog/events/v1/events.proto" {
		t.Fatalf("dep paths = %#v, want only the file that is imported", got)
	}
}
