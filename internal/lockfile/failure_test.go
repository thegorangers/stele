package lockfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/lockfile"
)

// The lock is a committed file that people read in a merge request, and the
// only record of what a pinned build consumed. A write that fails half way
// through must therefore leave the previous lock exactly as it was: a
// truncated lock either fails to load, or — worse, when the truncation lands on
// an entry boundary — loads as a shorter one that pins fewer dependencies than
// the build that wrote it consumed.

func sample() *lockfile.Lock {
	return &lockfile.Lock{
		Version: lockfile.Version,
		Deps: []lockfile.Entry{
			{Name: "api", Git: "https://example.com/acme/api.git", Ref: "main", SHA: "0123456789abcdef0123456789abcdef01234567"},
			{Name: "platform", Git: "https://example.com/acme/platform.git", Ref: "main", SHA: "89abcdef0123456789abcdef0123456789abcdef"},
		},
	}
}

// TestSave_FailedWriteLeavesThePreviousLockIntact makes the write fail at the
// last moment a filesystem can refuse one — the directory cannot be written to,
// though the file itself still can. A writer that opens the existing lock for
// truncation succeeds at destroying it and only then discovers it has nowhere
// to put the new one.
func TestSave_FailedWriteLeavesThePreviousLockIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permissions do not refuse a write")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.lock")
	if err := lockfile.Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	next := sample()
	next.Deps = append(next.Deps, lockfile.Entry{
		Name: "telemetry", Git: "https://example.com/acme/telemetry.git", Ref: "main",
		SHA: "fedcba9876543210fedcba9876543210fedcba98",
	})
	if err := lockfile.Save(path, next); err == nil {
		t.Fatal("want an error when the lock cannot be written")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous lock is gone after a failed write: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("a failed write changed the lock on disk:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	got, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("the lock left behind by a failed write does not load: %v", err)
	}
	if len(got.Deps) != 2 {
		t.Fatalf("the lock left behind pins %d dependencies, want the 2 it pinned before", len(got.Deps))
	}
}

// TestSave_LeavesNoTemporaryFileBehind guards the other side of writing
// through a temporary file: a lock directory is a repository, and a stray file
// beside the lock would be committed by the next person running git add.
func TestSave_LeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.lock")
	if err := lockfile.Save(path, sample()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "stele.lock" {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the directory holds %v, want only the lock", names)
	}
}
