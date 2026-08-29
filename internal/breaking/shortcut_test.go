package breaking

import (
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
)

func twoCommits(t *testing.T, second func(dir string)) (*gitrepo.Repo, Previous) {
	t.Helper()
	dir := repo(t)
	commit(t, dir, "api/x.proto", "syntax = \"proto3\";\n", "first")
	write(t, dir, "stele.lock", "version: 1\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "lock")
	first := run(t, dir, "rev-parse", "HEAD")
	second(dir)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "second")
	head := run(t, dir, "rev-parse", "HEAD")
	r, _ := gitrepo.Open(dir)
	return r, Previous{SHA: first, Working: head}
}

var watched = []string{"api", "stele.lock"}

func TestUnchangedWhenNeitherProtosNorLockMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "README.md", "prose") })
	got, err := Unchanged(r, prev, watched)
	if err != nil || !got {
		t.Fatalf("Unchanged = %v, %v; want true", got, err)
	}
}

func TestChangedWhenAProtoMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) {
		write(t, dir, "api/x.proto", "syntax = \"proto3\";\nmessage M {}\n")
	})
	got, err := Unchanged(r, prev, watched)
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A dependency bump changes no owned file and can still break the consumers
// of what this repository re-exports, so the lock is watched too.
func TestChangedWhenOnlyTheLockMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "stele.lock", "version: 1\n# bumped\n") })
	got, err := Unchanged(r, prev, watched)
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A revision that had no api/ at all must not compare equal to one that does.
func TestAbsentPathIsNotEqualToPresentPath(t *testing.T) {
	dir := repo(t)
	write(t, dir, "README.md", "prose")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "no protos yet")
	first := run(t, dir, "rev-parse", "HEAD")
	commit(t, dir, "api/x.proto", "syntax = \"proto3\";\n", "protos arrive")
	head := run(t, dir, "rev-parse", "HEAD")

	r, _ := gitrepo.Open(dir)
	got, err := Unchanged(r, Previous{SHA: first, Working: head}, []string{"api"})
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}
