package lockfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thegorangers/stele/internal/lockfile"
)

// FuzzParseLock fuzzes stele.lock parsing on the same terms as
// FuzzParseManifest in internal/config: an input either parses into a valid
// *Lock or is refused with an error, and never panics or hangs. A lock is
// written by this tool but read back on every run without --update, and a
// lock checked in by someone else — or edited by hand, which the format's
// own comment invites nobody to do but nothing stops — reaches this parser
// the same way a stranger's stele.yaml reaches internal/config.
//
// There is no pre-existing testdata corpus of raw lock files in this
// repository (locks are exercised by round-tripping through Save/Load in
// lock_test.go), so the seeds here are a real lock produced by Save,
// plus the deprecated-field shapes lock.go documents as still readable,
// and a couple of the strict-decode refusals (unknown key, duplicate
// request).
func FuzzParseLock(f *testing.F) {
	seedDir := f.TempDir()
	seedPath := filepath.Join(seedDir, "stele.lock")
	real := &lockfile.Lock{
		Version: lockfile.Version,
		Deps: []lockfile.Entry{
			{Name: "example", Git: "https://example.com/owner/repo.git", Ref: "main", SHA: "0123456789abcdef0123456789abcdef01234567"},
			{Name: "another", Git: "https://example.com/owner/other.git", Ref: "v1.2.3", SHA: "abcdef0123456789abcdef0123456789abcdef01"},
		},
		Plugins: []lockfile.Plugin{
			{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "unknown"},
			{Name: "local-plugin", Origin: lockfile.OriginFile, Version: "v1.0.0", Path: "./bin/plugin"},
		},
	}
	if err := lockfile.Save(seedPath, real); err != nil {
		f.Fatalf("seeding a real lock: %v", err)
	}
	b, err := os.ReadFile(seedPath)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)

	f.Add([]byte("version: 1\ndeps: []\n"))
	f.Add([]byte(""))
	f.Add([]byte("version: 1\ndeps:\n  - name: a\n    git: https://example.com/a.git\n    ref: main\n    sha: " +
		"0123456789abcdef0123456789abcdef01234567\n    modules: [api]\n"))
	f.Add([]byte("version: 1\ndeps:\n" +
		"  - name: a\n    git: https://example.com/a.git\n    ref: main\n    sha: 0123456789abcdef0123456789abcdef01234567\n" +
		"  - name: b\n    git: https://example.com/a.git\n    ref: main\n    sha: abcdef0123456789abcdef0123456789abcdef01\n"))
	f.Add([]byte("version: 1\nbogus: true\n"))
	f.Add([]byte("version: 2\ndeps: []\n"))

	f.Fuzz(func(t *testing.T, lock []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "stele.lock")
		if err := os.WriteFile(path, lock, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = lockfile.Load(path)
	})
}
