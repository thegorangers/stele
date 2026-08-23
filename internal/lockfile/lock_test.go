package lockfile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/lockfile"
)

const exampleSHA = "0123456789abcdef0123456789abcdef01234567"

// The ref is load-bearing: when a pinned SHA becomes unreachable, the message
// has to be able to name the branch the pin came from.
func TestSaveLoad_KeepsRefBesideSHA(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	e := lockfile.Entry{
		Name: "example",
		Git:  "https://example.com/owner/repo.git",
		Ref:  "release-2",
		SHA:  exampleSHA,
	}
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
}

// The lock is reviewed in merge requests, so its bytes must not depend on map
// iteration order.
func TestSave_IsDeterministic(t *testing.T) {
	deps := []lockfile.Entry{
		{Name: "example", Git: "https://example.com/owner/repo.git", Ref: "main", SHA: exampleSHA},
		{Name: "another", Git: "https://example.com/owner/other.git", Ref: "main", SHA: exampleSHA},
	}
	var first []byte
	for i := 0; i < 20; i++ {
		path := filepath.Join(t.TempDir(), "stele.lock")
		// Entries are given out of order on purpose.
		if err := lockfile.Save(path, &lockfile.Lock{Version: lockfile.Version, Deps: deps}); err != nil {
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

// The lock records the unpinned tiers, and it records them whole: the name,
// the tier, the version observed, and the declared path where there was one.
func TestSaveLoad_Plugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	in := &lockfile.Lock{
		Version: lockfile.Version,
		Plugins: []lockfile.Plugin{
			{Name: "protoc-gen-house", Origin: lockfile.OriginFile, Path: "tools/protoc-gen-house", Version: "unknown"},
			{Name: "protoc-gen-dart", Origin: lockfile.OriginPath, Version: "v0.3.0"},
		},
	}
	if err := lockfile.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []lockfile.Plugin{
		{Name: "protoc-gen-dart", Origin: lockfile.OriginPath, Version: "v0.3.0"},
		{Name: "protoc-gen-house", Origin: lockfile.OriginFile, Path: "tools/protoc-gen-house", Version: "unknown"},
	}
	if !reflect.DeepEqual(out.Plugins, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", out.Plugins, want)
	}
}

func TestLoad_PluginEntryValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	body := "version: 1\nplugins:\n  - origin: path\n    version: v1.0.0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := lockfile.Load(path)
	if err == nil || !strings.Contains(err.Error(), "plugins[0].name") {
		t.Fatalf("Load: expected a complaint about the missing name, got %v", err)
	}
}

// The pin is the SHA, and a SHA is already a cryptographic hash of the tree it
// names. Re-hashing the files it covers records nothing git has not recorded,
// so the lock does not carry them.
func TestSave_RecordsOnlyTheAddressOfEachDependency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	in := &lockfile.Lock{Version: lockfile.Version, Deps: []lockfile.Entry{{
		Name: "example",
		Git:  "https://example.com/owner/repo.git",
		Ref:  "main",
		SHA:  exampleSHA,
	}}}
	if err := lockfile.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"files:", "modules:", "manifest:"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("the lock still carries %q:\n%s", gone, b)
		}
	}
	for _, want := range []string{"name: example", "ref: main", "sha: " + exampleSHA} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the lock does not carry %q:\n%s", want, b)
		}
	}
}

// Locks written before the hashes were dropped exist on disk. Reading one must
// keep working, and writing it back must leave the old blocks behind.
func TestLoad_AcceptsALockWrittenBeforeTheHashesWereDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.lock")
	body := "version: 1\ndeps:\n" +
		"  - name: example\n" +
		"    git: https://example.com/owner/repo.git\n" +
		"    ref: main\n" +
		"    sha: " + exampleSHA + "\n" +
		"    modules:\n      - proto\n" +
		"    manifest: stele.yaml\n" +
		"    files:\n" +
		"      proto/a.proto: " + exampleSHA + "\n" +
		"      stele.yaml: " + exampleSHA + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("an existing lock must still load: %v", err)
	}
	if len(l.Deps) != 1 || l.Deps[0].SHA != exampleSHA || l.Deps[0].Ref != "main" {
		t.Fatalf("the address must survive the read: %+v", l.Deps)
	}
	rewritten := filepath.Join(dir, "rewritten.lock")
	if err := lockfile.Save(rewritten, l); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "files:") || strings.Contains(string(b), "modules:") {
		t.Errorf("rewriting must drop the old blocks:\n%s", b)
	}
}

func TestLoad_PluginOriginValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	body := "version: 1\nplugins:\n  - name: protoc-gen-x\n    origin: telepathy\n    version: unknown\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockfile.Load(path); err == nil || !strings.Contains(err.Error(), "telepathy") {
		t.Fatalf("Load: expected a complaint naming the origin, got %v", err)
	}
}

// A lock records what the manifest does not determine. For a plugin declared
// as module@version, or as a per-platform url and sha256, the manifest already
// names the exact thing; a copy of it here could only drift from the original.
// So a pinned plugin is not written at all, whichever tier it came from.
func TestSave_RecordsOnlyTheUnpinnedPlugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	in := &lockfile.Lock{
		Version: lockfile.Version,
		Plugins: []lockfile.Plugin{
			{Name: "protoc-gen-go", Origin: lockfile.OriginManaged, DeprecatedModule: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11"},
			{Name: "protoc-gen-dart", Origin: lockfile.OriginURL, Version: "unknown", DeprecatedOS: "linux", DeprecatedArch: "amd64", DeprecatedURL: "https://example.com/dart.tar.gz", DeprecatedSHA256: "abc"},
			{Name: "protoc-gen-house", Origin: lockfile.OriginFile, Path: "tools/protoc-gen-house", Version: "unknown"},
			{Name: "protoc-gen-found", Origin: lockfile.OriginPath, Version: "v0.3.0"},
		},
	}
	if err := lockfile.Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"protoc-gen-go", "protoc-gen-dart", "module:", "url:", "sha256:", "os:", "arch:"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("the lock still carries %q:\n%s", gone, b)
		}
	}
	out, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []lockfile.Plugin{
		{Name: "protoc-gen-found", Origin: lockfile.OriginPath, Version: "v0.3.0"},
		{Name: "protoc-gen-house", Origin: lockfile.OriginFile, Path: "tools/protoc-gen-house", Version: "unknown"},
	}
	if !reflect.DeepEqual(out.Plugins, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", out.Plugins, want)
	}
}

// Locks that list pinned plugins exist on disk. Reading one must keep working,
// exactly as it does for the per-file hashes dropped before, and writing it
// back must leave the pinned records behind.
func TestLoad_AcceptsALockWrittenBeforeThePinnedPluginsWereDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.lock")
	body := "version: 1\nplugins:\n" +
		"  - name: protoc-gen-dart\n    origin: url\n    version: unknown\n" +
		"    os: linux\n    arch: amd64\n" +
		"    url: https://example.com/dart.tar.gz\n    sha256: abc\n" +
		"    archive_path: bin/protoc-gen-dart\n" +
		"  - name: protoc-gen-go\n    origin: managed\n" +
		"    module: google.golang.org/protobuf/cmd/protoc-gen-go\n    version: v1.36.11\n" +
		"  - name: protoc-gen-found\n    origin: path\n    version: unknown\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("an existing lock must still load: %v", err)
	}
	rewritten := filepath.Join(dir, "rewritten.lock")
	if err := lockfile.Save(rewritten, l); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "protoc-gen-found") {
		t.Errorf("the observation must survive the rewrite:\n%s", b)
	}
	for _, gone := range []string{"protoc-gen-go", "protoc-gen-dart", "sha256:", "module:"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("rewriting must drop %q:\n%s", gone, b)
		}
	}
}

// writeLock puts a lock body on disk and returns its path.
func writeLock(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stele.lock")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A repeated name is not a conflict. Names are chosen per manifest, and a flat
// transitive closure holds requests made by manifests belonging to other
// people.
func TestLoad_ARepeatedNameIsNotAConflict(t *testing.T) {
	path := writeLock(t, "version: 1\ndeps:\n"+
		"  - name: place\n    git: ssh://git@git.example.com/acme/place.git\n    ref: main\n    sha: "+exampleSHA+"\n"+
		"  - name: place\n    git: https://git.example.com/acme/place.git\n    ref: main\n    sha: "+exampleSHA+"\n")
	l, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(l.Deps) != 2 {
		t.Fatalf("Load kept %d entries, want 2", len(l.Deps))
	}
}

// What is a conflict is one request pinned to two commits: the pinned run
// looks entries up by (git, ref) and cannot be answered with two. The error
// has to name both entries and the way out, not merely say something repeats.
func TestLoad_OneRequestPinnedTwiceNamesBothEntries(t *testing.T) {
	other := "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
	path := writeLock(t, "version: 1\ndeps:\n"+
		"  - name: place\n    git: gh:acme/place\n    ref: main\n    sha: "+exampleSHA+"\n"+
		"  - name: elsewhere\n    git: gh:acme/place\n    ref: main\n    sha: "+other+"\n")
	_, err := lockfile.Load(path)
	if err == nil {
		t.Fatal("one request pinned to two commits must be refused")
	}
	for _, want := range []string{"deps[0]", "deps[1]", "place", "elsewhere", exampleSHA, other, "gh:acme/place", "main", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The invariant issue #1 broke: nothing in this package may emit a file it
// would refuse to read.
func TestSave_RefusesALockItCouldNotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	err := lockfile.Save(path, &lockfile.Lock{Version: lockfile.Version, Deps: []lockfile.Entry{
		{Name: "place", Git: "gh:acme/place", Ref: "main", SHA: exampleSHA},
		{Name: "elsewhere", Git: "gh:acme/place", Ref: "main", SHA: "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"},
	}})
	if err == nil {
		t.Fatal("Save wrote a lock Load would reject")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("Save left a file behind")
	}
}
