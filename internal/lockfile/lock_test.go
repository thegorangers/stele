package lockfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lockfile"
)

const exampleSHA = "0123456789abcdef0123456789abcdef01234567"

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshot(t *testing.T, name, dir string) lockfile.Entry {
	t.Helper()
	e, err := lockfile.Snapshot(name, dir)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return e
}

func TestVerify_DetectsTamperedFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	e := snapshot(t, "dep", dir)
	write(t, dir, "a.proto", "tampered")
	err := lockfile.Verify(e, dir)
	if err == nil {
		t.Fatal("tampered content must be an error")
	}
	if !strings.Contains(err.Error(), "a.proto") {
		t.Fatalf("error must name the file, got %v", err)
	}
}

func TestVerify_DetectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	write(t, dir, "sub/b.proto", "other")
	e := snapshot(t, "dep", dir)
	if err := os.Remove(filepath.Join(dir, "sub", "b.proto")); err != nil {
		t.Fatal(err)
	}
	err := lockfile.Verify(e, dir)
	if err == nil {
		t.Fatal("a missing file must be an error")
	}
	if !strings.Contains(err.Error(), "sub/b.proto") {
		t.Fatalf("error must name the file, got %v", err)
	}
}

// An extra file is the dangerous case: it is the only one of the three that a
// build can consume without any recorded hash disagreeing.
func TestVerify_DetectsExtraFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	e := snapshot(t, "dep", dir)
	write(t, dir, "sneaked.proto", "not in the lock")
	err := lockfile.Verify(e, dir)
	if err == nil {
		t.Fatal("a file the lock does not list must be an error")
	}
	if !strings.Contains(err.Error(), "sneaked.proto") {
		t.Fatalf("error must name the file, got %v", err)
	}
}

func TestVerify_AcceptsUnchangedTree(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	write(t, dir, "sub/b.proto", "other")
	if err := lockfile.Verify(snapshot(t, "dep", dir), dir); err != nil {
		t.Fatalf("an untouched tree must verify: %v", err)
	}
}

// The ref is load-bearing: when a pinned SHA becomes unreachable, the message
// has to be able to name the branch the pin came from.
func TestSaveLoad_KeepsRefBesideSHA(t *testing.T) {
	dir := t.TempDir()
	tree := t.TempDir()
	write(t, tree, "a.proto", "original")

	e := snapshot(t, "example", tree)
	e.Git = "https://example.com/owner/repo.git"
	e.Ref = "release-2"
	e.SHA = exampleSHA

	path := filepath.Join(dir, "stele.lock")
	if err := lockfile.Save(path, &lockfile.Lock{Version: lockfile.Version, Deps: []lockfile.Entry{e}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Deps) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got.Deps))
	}
	if got.Deps[0].Ref != "release-2" || got.Deps[0].SHA != exampleSHA {
		t.Fatalf("ref/sha not round-tripped: %+v", got.Deps[0])
	}
	if err := lockfile.Verify(got.Deps[0], tree); err != nil {
		t.Fatalf("a round-tripped entry must still verify: %v", err)
	}
}

// The lock is reviewed in merge requests, so its bytes must not depend on map
// iteration order.
func TestSave_IsDeterministic(t *testing.T) {
	tree := t.TempDir()
	for _, n := range []string{"z.proto", "a.proto", "m/k.proto", "b.proto"} {
		write(t, tree, n, n)
	}
	var first []byte
	for i := 0; i < 20; i++ {
		e := snapshot(t, "example", tree)
		e.Git = "https://example.com/owner/repo.git"
		e.Ref = "main"
		e.SHA = exampleSHA
		second := snapshot(t, "another", tree)
		second.Git = "https://example.com/owner/other.git"
		second.Ref = "main"
		second.SHA = exampleSHA

		path := filepath.Join(t.TempDir(), "stele.lock")
		// Entries are given out of order on purpose.
		if err := lockfile.Save(path, &lockfile.Lock{Version: lockfile.Version, Deps: []lockfile.Entry{e, second}}); err != nil {
			t.Fatalf("Save: %v", err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
			continue
		}
		if string(b) != string(first) {
			t.Fatalf("serialisation is not stable:\n--- first ---\n%s\n--- run %d ---\n%s", first, i, b)
		}
	}
	if !strings.Contains(string(first), "another") {
		t.Fatalf("unexpected lock body:\n%s", first)
	}
}

func TestLoad_UnknownKeyIsAnErrorNamingIt(t *testing.T) {
	cases := map[string]string{
		"top level": "version: 1\nspurious: 1\n",
		"entry":     "version: 1\ndeps:\n  - name: example\n    spurious: 1\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stele.lock")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := lockfile.Load(path)
			if err == nil {
				t.Fatal("an unknown key must be an error")
			}
			if !strings.Contains(err.Error(), "spurious") {
				t.Fatalf("error must name the key, got %v", err)
			}
		})
	}
}

func TestLoad_RejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	if err := os.WriteFile(path, []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockfile.Load(path); err == nil {
		t.Fatal("an unsupported lock version must be an error")
	}
}

// A symlink cannot be pinned: its target is outside the hashed content, so the
// recorded hashes would keep matching while what is read changes.
func TestSnapshot_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	if err := os.Symlink("a.proto", filepath.Join(dir, "link.proto")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := lockfile.Snapshot("dep", dir)
	if err == nil {
		t.Fatal("a symlink in a pinned tree must be an error")
	}
	if !strings.Contains(err.Error(), "link.proto") {
		t.Fatalf("error must name the link, got %v", err)
	}
}

func TestSnapshot_RecordsRelativeSlashPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sub/dir/a.proto", "x")
	e := snapshot(t, "dep", dir)
	if _, ok := e.Files["sub/dir/a.proto"]; !ok {
		t.Fatalf("want a relative slash-separated key, got %v", e.Files)
	}
}
