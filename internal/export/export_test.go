package export_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/export"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
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

// remote is a fake git remote: a set of trees keyed by commit, and a single
// branch that can be moved between them. It is what makes it testable that a
// run without --update takes the commit the lock recorded and not the one the
// ref points at today.
type remote struct {
	trees map[string]string // sha -> directory holding that commit's tree
	head  string            // the commit "main" resolves to now
}

func (r *remote) fetch(_ context.Context, git, ref string) (string, string, error) {
	if ref == "main" {
		ref = r.head
	}
	dir, ok := r.trees[ref]
	if !ok {
		return "", "", fmt.Errorf("%w: %s in %s", source.ErrUnreachableSHA, ref, git)
	}
	return dir, ref, nil
}

const (
	sha1 = "1111111111111111111111111111111111111111"
	sha2 = "2222222222222222222222222222222222222222"
)

// movable builds the same consumer as world, against a producer whose branch
// can be moved from sha1 to sha2. The two commits differ in the body of the
// one file the consumer imports.
func movable(t *testing.T) (string, *remote) {
	t.Helper()

	old := t.TempDir()
	write(t, old, "stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write(t, old, "api/dep/v1/b.proto", `syntax = "proto3";
package dep.v1;
message B { string name = 1; }
`)

	fresh := t.TempDir()
	write(t, fresh, "stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write(t, fresh, "api/dep/v1/b.proto", `syntax = "proto3";
package dep.v1;
message B { string name = 1; int32 added_later = 2; }
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

	return consumer, &remote{trees: map[string]string{sha1: old, sha2: fresh}, head: sha1}
}

func exportOnce(t *testing.T, dir string, r *remote, update bool) (string, error) {
	t.Helper()
	out := t.TempDir()
	err := export.Run(context.Background(), export.Options{
		Dir:    dir,
		Output: out,
		Fetch:  r.fetch,
		Update: update,
	})
	return out, err
}

func lockedSHA(t *testing.T, dir, name string) string {
	t.Helper()
	l, err := lockfile.Load(filepath.Join(dir, export.LockName))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Deps {
		if e.Name == name {
			return e.SHA
		}
	}
	t.Fatalf("dependency %q is not in the lock", name)
	return ""
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExport_LockIsHonouredOverAMovedRef(t *testing.T) {
	dir, r := movable(t)
	if _, err := exportOnce(t, dir, r, false); err != nil {
		t.Fatal(err)
	}
	r.head = sha2 // the branch moves on

	out, err := exportOnce(t, dir, r, false)
	if err != nil {
		t.Fatal(err)
	}
	if body := read(t, filepath.Join(out, "dep/v1/b.proto")); strings.Contains(body, "added_later") {
		t.Fatalf("export followed the moved ref instead of the lock:\n%s", body)
	}
	if got := lockedSHA(t, dir, "dep"); got != sha1 {
		t.Fatalf("lock moved without --update: %s", got)
	}
}

func TestExport_TamperedDependencyIsRejected(t *testing.T) {
	dir, r := movable(t)
	if _, err := exportOnce(t, dir, r, false); err != nil {
		t.Fatal(err)
	}
	write(t, r.trees[sha1], "api/dep/v1/b.proto", `syntax = "proto3";
package dep.v1;
message B { string name = 1; }
// tampered
`)

	_, err := exportOnce(t, dir, r, false)
	if err == nil {
		t.Fatal("tampered dependency accepted")
	}
	if !strings.Contains(err.Error(), "dep") || !strings.Contains(err.Error(), "api/dep/v1/b.proto") {
		t.Fatalf("error names neither the dependency nor the file: %v", err)
	}
}

func TestExport_UpdateRewritesTheLock(t *testing.T) {
	dir, r := movable(t)
	if _, err := exportOnce(t, dir, r, false); err != nil {
		t.Fatal(err)
	}
	r.head = sha2

	out, err := exportOnce(t, dir, r, true)
	if err != nil {
		t.Fatal(err)
	}
	if body := read(t, filepath.Join(out, "dep/v1/b.proto")); !strings.Contains(body, "added_later") {
		t.Fatalf("--update did not re-resolve the ref:\n%s", body)
	}
	if got := lockedSHA(t, dir, "dep"); got != sha2 {
		t.Fatalf("--update left the lock at %s", got)
	}
}

func TestExport_DependencyMissingFromLock(t *testing.T) {
	dir, r := movable(t)
	if _, err := exportOnce(t, dir, r, false); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	write(t, other, "stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write(t, other, "api/other/v1/c.proto", "syntax = \"proto3\";\npackage other.v1;\nmessage C {}\n")
	r.trees[sha2] = other
	write(t, dir, "stele.yaml", `version: 1
modules:
  - path: api
deps:
  - name: dep
    git: https://example.test/dep.git
    ref: main
    module: api
  - name: other
    git: https://example.test/other.git
    ref: `+sha2+`
    module: api
`)

	_, err := exportOnce(t, dir, r, false)
	if err == nil {
		t.Fatal("a dependency absent from the lock was accepted")
	}
	if !strings.Contains(err.Error(), "other") || !strings.Contains(err.Error(), "--update") {
		t.Fatalf("error names neither the dependency nor the recovery: %v", err)
	}
}

func TestExport_LockEntryMissingFromManifest(t *testing.T) {
	dir, r := movable(t)
	if _, err := exportOnce(t, dir, r, false); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: api\n")
	write(t, dir, "api/own/v1/a.proto", "syntax = \"proto3\";\npackage own.v1;\nmessage A {}\n")

	_, err := exportOnce(t, dir, r, false)
	if err == nil {
		t.Fatal("a lock entry no manifest asks for was accepted")
	}
	if !strings.Contains(err.Error(), "dep") || !strings.Contains(err.Error(), "--update") {
		t.Fatalf("error names neither the dependency nor the recovery: %v", err)
	}
}

func TestExport_UnreachableLockedCommitIsExplained(t *testing.T) {
	dir, r := movable(t)
	if _, err := exportOnce(t, dir, r, false); err != nil {
		t.Fatal(err)
	}
	delete(r.trees, sha1) // squashed away

	_, err := exportOnce(t, dir, r, false)
	if err == nil {
		t.Fatal("an unreachable pin was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"dep", "main", "--update"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error does not mention %q: %v", want, err)
		}
	}
}
