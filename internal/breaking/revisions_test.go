package breaking

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
	"github.com/thegorangers/stele/internal/lint"
	"github.com/thegorangers/stele/internal/lockfile"
)

const (
	revShaOne = "1111111111111111111111111111111111111111"
	revShaTwo = "2222222222222222222222222222222222222222"
)

// movingFetch serves a fixed set of trees keyed by the SHA a pinned run asks
// for, following internal/pin/pin_test.go's moving fixture: a pinned
// resolution asks fetch for the locked SHA directly, not for the ref that
// named it. requests records what was actually asked for, which is what lets
// a test tell a run pinned to yesterday's lock apart from one that drifted
// onto today's.
type movingFetch struct {
	trees    map[string]string // sha -> tree dir
	requests []string
}

func (m *movingFetch) fetch(_ context.Context, _, ref string) (string, string, error) {
	m.requests = append(m.requests, ref)
	dir, ok := m.trees[ref]
	if !ok {
		return "", "", fmt.Errorf("unreachable sha: %s", ref)
	}
	return dir, ref, nil
}

// depTree lays out a minimal producer repository, resolvable as a stele
// dependency module, at some content marker so two trees can be told apart.
func depTree(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	write(t, dir, "proto/example/dep.proto",
		"syntax = \"proto3\";\npackage example;\n// "+marker+"\nmessage Dep { int64 value = 1; }\n")
	return dir
}

// consumerManifest is a root manifest depending on one producer, addressed by
// the address movingFetch answers by.
const depGit = "https://example.invalid/example/producer.git"

func writeConsumerManifest(t *testing.T, dir string) {
	t.Helper()
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: own\n"+
		"deps:\n  - name: dep\n    git: "+depGit+"\n    ref: main\n    module: proto\n")
	write(t, dir, "own/example/a.proto", "syntax = \"proto3\";\npackage example;\nmessage Owned { int64 x = 1; }\n")
}

func writeLock(t *testing.T, dir, sha string) {
	t.Helper()
	l := &lockfile.Lock{
		Version: lockfile.Version,
		Deps:    []lockfile.Entry{{Name: "dep", Git: depGit, Ref: "main", SHA: sha}},
	}
	if err := lockfile.Save(filepath.Join(dir, lint.LockName), l); err != nil {
		t.Fatal(err)
	}
}

// The previous revision is resolved with the lock that revision carried, not
// today's: compiling yesterday's protos against today's pins either fails
// outright or attributes another repository's change to this one.
func TestPreviousRevisionUsesItsOwnLock(t *testing.T) {
	dir := repo(t)
	treeOne := depTree(t, "first")
	treeTwo := depTree(t, "second")

	writeConsumerManifest(t, dir)
	writeLock(t, dir, revShaOne)
	prevSHA := commit(t, dir, "marker.txt", "prev", "resolves against shaOne")

	// Today's revision moved the pin onto a different commit of the
	// dependency, exactly as an ordinary --update would.
	writeLock(t, dir, revShaTwo)
	commit(t, dir, "marker.txt", "today", "resolves against shaTwo")

	m := &movingFetch{trees: map[string]string{revShaOne: treeOne, revShaTwo: treeTwo}}
	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), r, prevSHA, m.fetch, true); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(m.requests) != 1 || m.requests[0] != revShaOne {
		t.Fatalf("fetch requests = %v, want exactly [%s]: "+
			"the previous revision must resolve against the lock it carried, not today's", m.requests, revShaOne)
	}
}

// A manifest with no lock is an error: resolving it fresh would pin every
// dependency to the commit its ref names today, silently, and compare this
// revision against dependencies it never used.
func TestManifestWithoutLockIsAnError(t *testing.T) {
	dir := repo(t)
	writeConsumerManifest(t, dir)
	sha := commit(t, dir, "marker.txt", "one", "manifest with no lock")

	m := &movingFetch{trees: map[string]string{}}
	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Load(context.Background(), r, sha, m.fetch, true)
	if err == nil {
		t.Fatal("Load: expected an error, the revision's manifest has no lock")
	}
	if errors.Is(err, ErrNoManifest) || errors.Is(err, ErrNoOwnedProtos) {
		t.Fatalf("Load returned %v, want an error naming the missing lock specifically", err)
	}
}

// A revision with no manifest at all predates adoption of this tool. There is
// nothing to compare, and the caller exits zero rather than failing.
func TestRevisionWithoutManifestIsErrNoManifest(t *testing.T) {
	dir := repo(t)
	sha := commit(t, dir, "readme.txt", "hello", "no stele.yaml here")

	m := &movingFetch{}
	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Load(context.Background(), r, sha, m.fetch, true)
	if !errors.Is(err, ErrNoManifest) {
		t.Fatalf("Load error = %v, want ErrNoManifest", err)
	}
}

// A revision that owns no proto files is its own condition, reported as
// ErrNoOwnedProtos — never as a compile failure (compile.Compile errors with
// "no target files" on an empty target list, so this has to be caught before
// Compile is called) and never as clean.
func TestRevisionOwningNoProtosIsErrNoOwnedProtos(t *testing.T) {
	dir := repo(t)
	write(t, dir, lint.ManifestName, "version: 1\nmodules:\n  - path: empty\n")
	write(t, dir, "empty/.gitkeep", "")
	if err := lockfile.Save(filepath.Join(dir, lint.LockName), &lockfile.Lock{Version: lockfile.Version}); err != nil {
		t.Fatal(err)
	}
	sha := commit(t, dir, "marker.txt", "one", "module with no protos")

	m := &movingFetch{}
	r, err := gitrepo.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Load(context.Background(), r, sha, m.fetch, true)
	if !errors.Is(err, ErrNoOwnedProtos) {
		t.Fatalf("Load error = %v, want ErrNoOwnedProtos", err)
	}
}
