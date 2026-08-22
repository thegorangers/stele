package export_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/export"
	"github.com/thegorangers/stele/internal/resolve"
)

// world builds a consumer whose module holds one file importing a file that
// only the producer supplies, and returns the consumer directory and a fetch
// that hands out the producer's tree without touching a network.
func world(t *testing.T) (string, resolve.FetchFunc) {
	t.Helper()

	producer := t.TempDir()
	write(t, producer, "stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write(t, producer, "api/dep/v1/b.proto", `syntax = "proto3";
package dep.v1;
message B { string name = 1; }
`)

	consumer := t.TempDir()
	write(t, consumer, "stele.yaml", `version: 1
modules:
  - path: api
deps:
  - name: dep
    git: https://example.test/dep.git
    ref: main
    module: api
`)
	write(t, consumer, "api/own/v1/a.proto", `syntax = "proto3";
package own.v1;
import "dep/v1/b.proto";
message A { dep.v1.B b = 1; }
`)
	write(t, consumer, "api/own/v1/plain.proto", `syntax = "proto3";
package own.v1;
message Plain { int32 n = 1; }
`)

	fetch := func(_ context.Context, git, ref string) (string, string, error) {
		return producer, "0123456789abcdef0123456789abcdef01234567", nil
	}
	return consumer, fetch
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestExport_IncludesImportsByDefault(t *testing.T) {
	dir, fetch := world(t)
	out := t.TempDir()

	if err := export.Run(context.Background(), export.Options{
		Dir:    dir,
		Output: out,
		Paths:  []string{"own/v1/a.proto"},
		Fetch:  fetch,
	}); err != nil {
		t.Fatal(err)
	}

	got := tree(t, out)
	want := []string{"dep/v1/b.proto", "own/v1/a.proto"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("exported %v, want %v", got, want)
	}
}

func TestExport_ExcludeImportsOmitsImports(t *testing.T) {
	dir, fetch := world(t)
	out := t.TempDir()

	if err := export.Run(context.Background(), export.Options{
		Dir:            dir,
		Output:         out,
		Paths:          []string{"own/v1/a.proto"},
		ExcludeImports: true,
		Fetch:          fetch,
	}); err != nil {
		t.Fatal(err)
	}

	got := tree(t, out)
	want := []string{"own/v1/a.proto"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("exported %v, want %v", got, want)
	}
}

func TestExport_PathsSelectFromTheModuleRootNotTheWorkspace(t *testing.T) {
	dir, fetch := world(t)
	out := t.TempDir()

	// The file lives at api/own/v1/a.proto; api is the module root, so the
	// coordinate is own/v1 and never api/own/v1.
	if err := export.Run(context.Background(), export.Options{
		Dir:            dir,
		Output:         out,
		Paths:          []string{"own/v1"},
		ExcludeImports: true,
		Fetch:          fetch,
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := len(tree(t, out)), 2; got != want {
		t.Fatalf("exported %d files, want %d: %v", got, want, tree(t, out))
	}

	err := export.Run(context.Background(), export.Options{
		Dir:            dir,
		Output:         t.TempDir(),
		Paths:          []string{"api/own/v1"},
		ExcludeImports: true,
		Fetch:          fetch,
	})
	if err == nil {
		t.Fatal("a workspace-relative coordinate must not match anything")
	}
	if !strings.Contains(err.Error(), "api/own/v1") {
		t.Fatalf("the error must name the path that matched nothing: %v", err)
	}
}

func TestExport_EmptyPathMatchIsError(t *testing.T) {
	dir, fetch := world(t)

	err := export.Run(context.Background(), export.Options{
		Dir:    dir,
		Output: t.TempDir(),
		Paths:  []string{"own/v1/a.proto", "nosuch/v1"},
		Fetch:  fetch,
	})
	if err == nil {
		t.Fatal("zero matches must be an error, not an empty success")
	}
	if !strings.Contains(err.Error(), "nosuch/v1") {
		t.Fatalf("the error must name the path that matched nothing: %v", err)
	}
}

func TestExport_WritesTheLockFromTheGraph(t *testing.T) {
	dir, fetch := world(t)

	if err := export.Run(context.Background(), export.Options{
		Dir:    dir,
		Output: t.TempDir(),
		Fetch:  fetch,
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "stele.lock"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: dep", "ref: main", "sha: 0123456789abcdef0123456789abcdef01234567", "api/dep/v1/b.proto"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lock does not mention %q:\n%s", want, raw)
		}
	}
}
