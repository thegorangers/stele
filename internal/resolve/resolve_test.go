package resolve_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/resolve"
)

// repo lays out one synthetic repository under a temporary directory and
// returns its path. files maps a path inside the repository to its contents.
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

// fakeFetch serves repositories from local directories, so that every test in
// this package is hermetic: no network, no cache, no git.
func fakeFetch(repos map[string]string) resolve.FetchFunc {
	return func(_ context.Context, git, ref string) (string, string, error) {
		dir, ok := repos[git]
		if !ok {
			return "", "", errors.New("no such repository: " + git)
		}
		// A synthetic SHA that varies with the ref, so that the same
		// repository at two refs is two cache entries, as it would be in life.
		return dir, strings.Repeat("a", 33) + "-" + ref, nil
	}
}

// TestResolve_TransitiveThroughProducerManifest is the case a "give me a
// subdirectory" model cannot express: the producer's own files import a file
// that lives outside the module the consumer asked for, and is resolved
// through the producer's own dependencies.
func TestResolve_TransitiveThroughProducerManifest(t *testing.T) {
	validate := repo(t, "validate", map[string]string{
		"stele.yaml":                         "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/validate/validate.proto": "syntax = \"proto3\";\n",
	})
	producer := repo(t, "producer", map[string]string{
		"stele.yaml": "version: 1\n" +
			"modules:\n  - path: api\n" +
			"deps:\n  - name: validate\n    git: gh:acme/validate\n    ref: v1.0.0\n    module: proto\n",
		"api/example/v1/place.proto": "syntax = \"proto3\";\nimport \"acme/validate/validate.proto\";\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})

	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "place", Git: "gh:acme/producer", Ref: "v2.0.0", Module: "api"}},
	}
	fetch := fakeFetch(map[string]string{
		"gh:acme/producer": producer,
		"gh:acme/validate": validate,
	})

	g, err := resolve.ResolveIn(context.Background(), consumer, cfg, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.FileFor("acme/validate/validate.proto"); !ok {
		t.Fatal("the producer's transitive import was not resolved")
	}
	if _, ok := g.FileFor("example/v1/place.proto"); !ok {
		t.Fatal("the producer's own file was not resolved")
	}
	if _, ok := g.FileFor("example/v1/order.proto"); !ok {
		t.Fatal("the root manifest's own file was not resolved")
	}
	if got := len(g.ImportRoots()); got != 3 {
		t.Fatalf("import roots: got %d (%v), want 3", got, g.ImportRoots())
	}
}

// TestResolve_ConflictingImportPathIsError pins §4.4: the same import path
// from two sources is an error naming the path and both sources, never a
// silent merge and never "first wins".
func TestResolve_ConflictingImportPathIsError(t *testing.T) {
	first := repo(t, "first", map[string]string{
		"stele.yaml":                         "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/validate/validate.proto": "syntax = \"proto3\";\n// one\n",
	})
	second := repo(t, "second", map[string]string{
		"stele.yaml":                          "version: 1\nmodules:\n  - path: vendor\n",
		"vendor/acme/validate/validate.proto": "syntax = \"proto3\";\n// another\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})

	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps: []config.Dep{
			{Name: "validate", Git: "gh:acme/first", Ref: "v1.0.0", Module: "proto"},
			{Name: "vendored-validate", Git: "gh:acme/second", Ref: "v1.0.0", Module: "vendor"},
		},
	}
	_, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/first":  first,
		"gh:acme/second": second,
	}))
	if !errors.Is(err, resolve.ErrImportConflict) {
		t.Fatalf("want ErrImportConflict, got %v", err)
	}
	for _, want := range []string{"acme/validate/validate.proto", "validate", "vendored-validate"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name the path and both sources, got: %v", err)
		}
	}
}

// TestResolve_BufYAMLFallback covers a producer that has not migrated yet: its
// buf.yaml is read for module roots and for nothing else.
func TestResolve_BufYAMLFallback(t *testing.T) {
	producer := repo(t, "producer", map[string]string{
		"buf.yaml": "version: v2\n" +
			"modules:\n  - path: api\n  - path: third_party/proto\n" +
			"lint:\n  use: [STANDARD]\n",
		"api/example/v1/place.proto":              "syntax = \"proto3\";\n",
		"third_party/proto/acme/validate/v.proto": "syntax = \"proto3\";\n",
		// Repository internals must never become importable files, even when
		// the fetched tree is a working copy that still carries them.
		".git/config":          "[core]\n",
		".git/hooks/bad.proto": "syntax = \"proto3\";\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})

	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "place", Git: "gh:acme/producer", Ref: "v1.0.0", Module: "api"}},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/producer": producer,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.FileFor("acme/validate/v.proto"); !ok {
		t.Fatal("the second module root of buf.yaml was not read")
	}
	for _, p := range g.ImportPaths() {
		if strings.Contains(p, ".git") {
			t.Fatalf("repository internals became importable: %q", p)
		}
	}
}

// TestResolve_EmptyModuleMeansRepositoryRoot pins the reading of an omitted
// dep.module (§4.7): the module of the producer is its repository root.
func TestResolve_EmptyModuleMeansRepositoryRoot(t *testing.T) {
	producer := repo(t, "producer", map[string]string{
		"acme/example/v1/place.proto": "syntax = \"proto3\";\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "place", Git: "gh:acme/producer", Ref: "v1.0.0"}},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/producer": producer,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.FileFor("acme/example/v1/place.proto"); !ok {
		t.Fatalf("an omitted module must mean the repository root, got %v", g.ImportPaths())
	}
}

// TestResolve_UnknownProducerModuleIsError: asking for a module the producer's
// manifest does not declare is a mistake worth naming, not an empty result.
func TestResolve_UnknownProducerModuleIsError(t *testing.T) {
	producer := repo(t, "producer", map[string]string{
		"stele.yaml":                 "version: 1\nmodules:\n  - path: api\n",
		"api/example/v1/place.proto": "syntax = \"proto3\";\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps:    []config.Dep{{Name: "place", Git: "gh:acme/producer", Ref: "v1.0.0", Module: "proto"}},
	}
	_, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/producer": producer,
	}))
	if err == nil || !strings.Contains(err.Error(), "proto") || !strings.Contains(err.Error(), "api") {
		t.Fatalf("want an error naming the requested and the declared modules, got %v", err)
	}
}

// TestResolve_RepositoryVisitedOnceAtTheSameSHA: a diamond must not be walked
// twice, and must not report a conflict against itself.
func TestResolve_RepositoryVisitedOnceAtTheSameSHA(t *testing.T) {
	shared := repo(t, "shared", map[string]string{
		"stele.yaml":                         "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/validate/validate.proto": "syntax = \"proto3\";\n",
	})
	left := repo(t, "left", map[string]string{
		"stele.yaml": "version: 1\nmodules:\n  - path: api\n" +
			"deps:\n  - name: shared\n    git: gh:acme/shared\n    ref: v1.0.0\n    module: proto\n",
		"api/example/v1/left.proto": "syntax = \"proto3\";\n",
	})
	right := repo(t, "right", map[string]string{
		"stele.yaml": "version: 1\nmodules:\n  - path: api\n" +
			"deps:\n  - name: shared\n    git: gh:acme/shared\n    ref: v1.0.0\n    module: proto\n",
		"api/example/v2/right.proto": "syntax = \"proto3\";\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps: []config.Dep{
			{Name: "left", Git: "gh:acme/left", Ref: "v1.0.0", Module: "api"},
			{Name: "right", Git: "gh:acme/right", Ref: "v1.0.0", Module: "api"},
		},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/left": left, "gh:acme/right": right, "gh:acme/shared": shared,
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := g.ImportPaths()
	sort.Strings(got)
	want := []string{"acme/validate/validate.proto", "example/v1/left.proto", "example/v1/order.proto", "example/v2/right.proto"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	if n := len(g.Deps()); n != 3 {
		t.Fatalf("the transitive closure must hold one entry per repository, got %d", n)
	}
}

// TestResolve_IdenticalContentFromTwoSourcesIsNotAConflict covers the case the
// transition makes common: while producers still vendor well-known protos, two
// of them supply the same import path from byte-identical files. The compiler's
// view is the same either way, so there is nothing to protect against.
func TestResolve_IdenticalContentFromTwoSourcesIsNotAConflict(t *testing.T) {
	const body = "syntax = \"proto3\";\npackage acme.validate;\n"
	first := repo(t, "first", map[string]string{
		"stele.yaml":                         "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/validate/validate.proto": body,
	})
	second := repo(t, "second", map[string]string{
		"stele.yaml":                          "version: 1\nmodules:\n  - path: vendor\n",
		"vendor/acme/validate/validate.proto": body,
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})

	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps: []config.Dep{
			{Name: "validate", Git: "gh:acme/first", Ref: "v1.0.0", Module: "proto"},
			{Name: "vendored-validate", Git: "gh:acme/second", Ref: "v1.0.0", Module: "vendor"},
		},
	}
	g, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/first":  first,
		"gh:acme/second": second,
	}))
	if err != nil {
		t.Fatalf("identical contents must not conflict: %v", err)
	}
	f, ok := g.FileFor("acme/validate/validate.proto")
	if !ok {
		t.Fatal("the deduplicated file was not resolved")
	}
	// Whichever was registered first stays, and both suppliers are recorded so
	// that a later command can report the duplication.
	if f.Origin.Name != "validate" {
		t.Fatalf("the first registration must stay, got %q", f.Origin.Name)
	}
	var names []string
	for _, o := range f.Sources {
		names = append(names, o.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "validate,vendored-validate" {
		t.Fatalf("both suppliers must be recorded, got %v", names)
	}
}

// TestResolve_ConflictNamesHashes: when the contents differ, the error has to
// carry the evidence that decided it.
func TestResolve_ConflictNamesHashes(t *testing.T) {
	first := repo(t, "first", map[string]string{
		"stele.yaml":                         "version: 1\nmodules:\n  - path: proto\n",
		"proto/acme/validate/validate.proto": "syntax = \"proto3\";\n// one\n",
	})
	second := repo(t, "second", map[string]string{
		"stele.yaml":                          "version: 1\nmodules:\n  - path: vendor\n",
		"vendor/acme/validate/validate.proto": "syntax = \"proto3\";\n// another\n",
	})
	consumer := repo(t, "consumer", map[string]string{
		"api/example/v1/order.proto": "syntax = \"proto3\";\n",
	})
	cfg := &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "api"}},
		Deps: []config.Dep{
			{Name: "validate", Git: "gh:acme/first", Ref: "v1.0.0", Module: "proto"},
			{Name: "vendored-validate", Git: "gh:acme/second", Ref: "v1.0.0", Module: "vendor"},
		},
	}
	_, err := resolve.ResolveIn(context.Background(), consumer, cfg, fakeFetch(map[string]string{
		"gh:acme/first":  first,
		"gh:acme/second": second,
	}))
	if !errors.Is(err, resolve.ErrImportConflict) {
		t.Fatalf("want ErrImportConflict, got %v", err)
	}
	h1 := sha256Hex(t, filepath.Join(first, "proto/acme/validate/validate.proto"))
	h2 := sha256Hex(t, filepath.Join(second, "vendor/acme/validate/validate.proto"))
	for _, want := range []string{"acme/validate/validate.proto", "validate", "vendored-validate", h1, h2} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error must name the path, both sources and both hashes; %q is missing from: %v", want, err)
		}
	}
}

func sha256Hex(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
