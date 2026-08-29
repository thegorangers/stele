package gitrepo

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A stale local branch would silently place the comparison in the past, so
// the base is resolved as a remote-tracking ref, not as a local branch.
func TestBaseRefPrefersTheRemoteOverAStaleLocalBranch(t *testing.T) {
	origin := repo(t)
	commit(t, origin, "a.txt", "one", "first")
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	run(t, parent, "clone", "-q", "file://"+origin, clone)

	// The origin moves; the clone's local main does not.
	moved := commit(t, origin, "a.txt", "two", "second")
	run(t, clone, "fetch", "-q", "origin")

	r, _ := Open(clone)
	got, fresh, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Error("fresh = false, want true: the fetch should have succeeded")
	}
	if got != moved {
		t.Fatalf("BaseRef = %s, want the remote's %s, not the stale local branch", got, moved)
	}
}

// BaseRef must not cache: a clone that fetched once and never again would
// keep comparing against where the base used to be, the exact failure the
// design exists to avoid.
func TestBaseRefRefetchesOnEveryCall(t *testing.T) {
	origin := repo(t)
	commit(t, origin, "a.txt", "one", "first")
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	run(t, parent, "clone", "-q", "file://"+origin, clone)

	r, _ := Open(clone)
	first, fresh, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("fresh = false on the first call, want true")
	}

	moved := commit(t, origin, "a.txt", "two", "second")

	second, fresh, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Error("fresh = false, want true: the fetch should have succeeded")
	}
	if second == first {
		t.Fatal("BaseRef returned the cached answer instead of refetching")
	}
	if second != moved {
		t.Fatalf("BaseRef = %s, want the moved base %s", second, moved)
	}
}

// When the fetch cannot reach the remote, BaseRef falls back to whatever it
// fetched before, and says so through fresh, rather than failing an offline
// developer outright.
func TestBaseRefFallsBackWhenTheFetchFails(t *testing.T) {
	origin := repo(t)
	commit(t, origin, "a.txt", "one", "first")
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	run(t, parent, "clone", "-q", "file://"+origin, clone)

	r, _ := Open(clone)
	cached, fresh, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("fresh = false on the first call, want true")
	}

	// The remote becomes unreachable.
	run(t, clone, "remote", "set-url", "origin", "file:///nonexistent-gitrepo-base-test")

	got, fresh, err := r.BaseRef("main")
	if err != nil {
		t.Fatalf("BaseRef: %v, want the cached fallback instead of an error", err)
	}
	if fresh {
		t.Error("fresh = true, want false: the fetch failed and a fallback was used")
	}
	if got != cached {
		t.Fatalf("BaseRef = %s, want the cached %s", got, cached)
	}
}

// The merge-request case: the base branch is not in the clone at all. It is
// fetched, into refs/stele, without disturbing the user's refs.
func TestBaseRefFetchesAnAbsentBaseWithoutDisturbingTheRepository(t *testing.T) {
	origin := repo(t)
	first := commit(t, origin, "a.txt", "one", "first")
	run(t, origin, "checkout", "-q", "-b", "topic")
	commit(t, origin, "b.txt", "two", "topic")

	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	// Clone only the topic branch, as a merge-request pipeline does.
	run(t, parent, "clone", "-q", "--single-branch", "--branch", "topic", "file://"+origin, clone)
	if _, err := run2(clone, "rev-parse", "--verify", "origin/main"); err == nil {
		t.Fatal("the fixture is wrong: origin/main is present, so nothing is being tested")
	}

	r, _ := Open(clone)
	got, fresh, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Error("fresh = false, want true: the fetch should have succeeded")
	}
	if got != first {
		t.Fatalf("BaseRef = %s, want %s", got, first)
	}
	if _, err := run2(clone, "rev-parse", "--verify", "FETCH_HEAD"); err == nil {
		t.Error("FETCH_HEAD was written; the fetch must not disturb the user's refs")
	}
	if _, err := run2(clone, "rev-parse", "--verify", "refs/stele/base"); err != nil {
		t.Error("the fetched base was not stored under refs/stele/")
	}
}

func TestRemoteAmbiguityIsNamed(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "remote", "add", "origin", "file:///nonexistent-a")
	run(t, dir, "remote", "add", "upstream", "file:///nonexistent-b")

	r, _ := Open(dir)
	_, err := r.Remote()
	if !errors.Is(err, ErrAmbiguousRemote) {
		t.Fatalf("Remote error = %v, want ErrAmbiguousRemote", err)
	}
	if !strings.Contains(err.Error(), "origin") || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("the error must name the remotes it cannot choose between: %v", err)
	}
}

func TestNoRemoteIsNamed(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	r, _ := Open(dir)
	if _, err := r.Remote(); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("Remote error = %v, want ErrNoRemote", err)
	}
}
