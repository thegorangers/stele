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

func TestTreesUnchangedWhenNeitherProtosNorLockMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "README.md", "prose") })
	got, err := TreesUnchanged(r, prev, watched)
	if err != nil || !got {
		t.Fatalf("Unchanged = %v, %v; want true", got, err)
	}
}

func TestChangedWhenAProtoMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) {
		write(t, dir, "api/x.proto", "syntax = \"proto3\";\nmessage M {}\n")
	})
	got, err := TreesUnchanged(r, prev, watched)
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A dependency bump changes no owned file and can still break the consumers
// of what this repository re-exports, so the lock is watched too.
func TestChangedWhenOnlyTheLockMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "stele.lock", "version: 1\n# bumped\n") })
	got, err := TreesUnchanged(r, prev, watched)
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A path that appeared between the two revisions (absent in the earlier one,
// present in the later one) must not be reported as unchanged.
func TestAbsentPathIsNotEqualToPresentPath(t *testing.T) {
	dir := repo(t)
	write(t, dir, "README.md", "prose")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "no protos yet")
	first := run(t, dir, "rev-parse", "HEAD")
	commit(t, dir, "api/x.proto", "syntax = \"proto3\";\n", "protos arrive")
	head := run(t, dir, "rev-parse", "HEAD")

	r, _ := gitrepo.Open(dir)
	got, err := TreesUnchanged(r, Previous{SHA: first, Working: head}, []string{"api"})
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A manifest that owns the whole repository (`modules: - path: .`) is a
// plausible layout, and it must not turn every run into a permanent
// shortcut. Reverting the ObjectSHA root-resolution fix makes this fail:
// `git cat-file -e HEAD:.` exits non-zero, ObjectSHA reports the root
// absent on both sides, and TreesUnchanged reads that as unchanged even
// though a field was removed.
func TestChangedWhenModuleIsTheRepositoryRoot(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) {
		write(t, dir, "api/x.proto", "syntax = \"proto3\";\nmessage M {}\n")
	})
	got, err := TreesUnchanged(r, prev, []string{"."})
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false (the root module moved)", got, err)
	}
}

// A watched path that names nothing in either revision is a broken watch
// list, not a clean comparison: reporting "unchanged" here is exactly the
// empty-comparison-as-clean failure this package exists to avoid.
func TestErrorsWhenAWatchedPathIsAbsentFromBothRevisions(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "README.md", "prose") })
	got, err := TreesUnchanged(r, prev, []string{"api", "nonexistent"})
	if err == nil {
		t.Fatalf("Unchanged with a path absent from both revisions: err = nil, want an error")
	}
	if got {
		t.Fatalf("Unchanged with a path absent from both revisions: got = true, want false")
	}
}

// An empty set of watched paths compares nothing, and reporting that as
// "unchanged" would skip every run forever. That is the
// empty-comparison-as-clean failure this package exists to avoid, so it must
// be an error.
func TestTreesUnchangedRejectsEmptyPaths(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "README.md", "prose") })
	got, err := TreesUnchanged(r, prev, nil)
	if err == nil {
		t.Fatalf("Unchanged with no paths: err = nil, want an error")
	}
	if got {
		t.Fatalf("Unchanged with no paths: got = true, want false")
	}
}
