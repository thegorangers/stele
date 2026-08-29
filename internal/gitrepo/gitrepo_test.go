package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParentsAndMergeBase(t *testing.T) {
	dir := repo(t)
	first := commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	topic := commit(t, dir, "b.txt", "two", "topic work")

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parents, err := r.Parents(topic)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0] != first {
		t.Fatalf("Parents = %v, want [%s]", parents, first)
	}
	base, err := r.MergeBase(topic, "main")
	if err != nil {
		t.Fatal(err)
	}
	if base != first {
		t.Fatalf("MergeBase = %s, want %s", base, first)
	}
}

// A root commit has no parents, and that is a value rather than an error: a
// repository's first commit has nothing before it to compare against.
func TestParentsOfRootCommitIsEmpty(t *testing.T) {
	dir := repo(t)
	head := commit(t, dir, "a.txt", "one", "first")
	r, _ := Open(dir)
	parents, err := r.Parents(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 0 {
		t.Fatalf("Parents = %v, want none", parents)
	}
}

// Absence is a value too. A module root a revision did not have is an
// ordinary state, and reporting it as a failure would leave the caller unable
// to tell it from a broken repository.
func TestObjectSHAReportsAbsence(t *testing.T) {
	dir := repo(t)
	head := commit(t, dir, filepath.Join("api", "x.proto"), "syntax = \"proto3\";", "first")
	r, _ := Open(dir)

	tree, ok, err := r.ObjectSHA(head, "api")
	if err != nil || !ok || tree == "" {
		t.Fatalf("ObjectSHA(api) = %q, %v, %v; want a tree", tree, ok, err)
	}
	blob, ok, err := r.ObjectSHA(head, "api/x.proto")
	if err != nil || !ok || blob == "" {
		t.Fatalf("ObjectSHA(api/x.proto) = %q, %v, %v; want a blob", blob, ok, err)
	}
	if _, ok, err := r.ObjectSHA(head, "nope"); err != nil || ok {
		t.Fatalf("ObjectSHA(nope) ok=%v err=%v; want absent and no error", ok, err)
	}
}

// The breaking-change shortcut (internal/breaking.TreesUnchanged) leans on this
// exact contract: a non-empty SHA if and only if ok is true. Pin it here,
// where it is made, so a future change to ObjectSHA cannot silently turn
// "appeared" into "unchanged" for that caller.
func TestObjectSHAEmptyIffAbsent(t *testing.T) {
	dir := repo(t)
	head := commit(t, dir, filepath.Join("api", "x.proto"), "syntax = \"proto3\";", "first")
	r, _ := Open(dir)

	sha, ok, err := r.ObjectSHA(head, "api/x.proto")
	if err != nil {
		t.Fatal(err)
	}
	if ok && sha == "" {
		t.Fatalf("ObjectSHA: ok=true but sha is empty")
	}
	if !ok && sha != "" {
		t.Fatalf("ObjectSHA: ok=false but sha=%q, want empty", sha)
	}

	sha, ok, err = r.ObjectSHA(head, "nope")
	if err != nil {
		t.Fatal(err)
	}
	if ok && sha == "" {
		t.Fatalf("ObjectSHA: ok=true but sha is empty")
	}
	if !ok && sha != "" {
		t.Fatalf("ObjectSHA: ok=false but sha=%q, want empty", sha)
	}
}

// Materialise must not move HEAD or write into the user's .git.
func TestMaterialiseLeavesTheRepositoryAlone(t *testing.T) {
	dir := repo(t)
	first := commit(t, dir, "a.txt", "one", "first")
	commit(t, dir, "a.txt", "two", "second")
	headBefore := run(t, dir, "rev-parse", "HEAD")

	out := t.TempDir()
	r, _ := Open(dir)
	if err := r.Materialise(first, out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(out, "a.txt"))
	if err != nil || string(body) != "one" {
		t.Fatalf("materialised a.txt = %q, %v; want the first revision", body, err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD moved to %s", got)
	}
	if run(t, dir, "status", "--porcelain") != "" {
		t.Fatal("the working tree is dirty after Materialise")
	}
}

func TestIsShallow(t *testing.T) {
	origin := repo(t)
	for _, body := range []string{"one", "two", "three"} {
		commit(t, origin, "a.txt", body, body)
	}
	parent := t.TempDir()
	shallow := filepath.Join(parent, "clone")
	run(t, parent, "clone", "-q", "--depth", "1", "file://"+origin, shallow)

	full, _ := Open(origin)
	if got, err := full.IsShallow(); err != nil || got {
		t.Fatalf("IsShallow(full) = %v, %v; want false", got, err)
	}
	sh, _ := Open(shallow)
	if got, err := sh.IsShallow(); err != nil || !got {
		t.Fatalf("IsShallow(shallow) = %v, %v; want true", got, err)
	}
}
