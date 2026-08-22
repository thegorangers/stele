package lockfile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
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

// whole is the scope of a producer whose single module root is the repository
// root and which carries no manifest of its own.
var whole = lockfile.Scope{Modules: []string{"."}}

func snapshot(t *testing.T, name, dir string, scope lockfile.Scope) lockfile.Entry {
	t.Helper()
	e, err := lockfile.Snapshot(name, dir, scope)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return e
}

func TestVerify_DetectsTamperedFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	e := snapshot(t, "dep", dir, whole)
	write(t, dir, "a.proto", "tampered")
	err := lockfile.Verify(e, dir, whole)
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
	e := snapshot(t, "dep", dir, whole)
	if err := os.Remove(filepath.Join(dir, "sub", "b.proto")); err != nil {
		t.Fatal(err)
	}
	err := lockfile.Verify(e, dir, whole)
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
	e := snapshot(t, "dep", dir, whole)
	write(t, dir, "sneaked.proto", "not in the lock")
	err := lockfile.Verify(e, dir, whole)
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
	if err := lockfile.Verify(snapshot(t, "dep", dir, whole), dir, whole); err != nil {
		t.Fatalf("an untouched tree must verify: %v", err)
	}
}

// The ref is load-bearing: when a pinned SHA becomes unreachable, the message
// has to be able to name the branch the pin came from.
func TestSaveLoad_KeepsRefBesideSHA(t *testing.T) {
	dir := t.TempDir()
	tree := t.TempDir()
	write(t, tree, "a.proto", "original")

	e := snapshot(t, "example", tree, whole)
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
	if err := lockfile.Verify(got.Deps[0], tree, whole); err != nil {
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
		e := snapshot(t, "example", tree, whole)
		e.Git = "https://example.com/owner/repo.git"
		e.Ref = "main"
		e.SHA = exampleSHA
		second := snapshot(t, "another", tree, whole)
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

// A symlink is recorded by its target text and never followed. Refusing it
// outright, which this once did, makes real repositories unpinnable: the first
// external dependency this tool was pointed at carries one in a directory that
// holds no protos at all.
func TestSnapshot_RecordsSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	if err := os.Symlink("a.proto", filepath.Join(dir, "link.proto")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	e := snapshot(t, "dep", dir, whole)
	got, ok := e.Files["link.proto"]
	if !ok {
		t.Fatalf("the link must be recorded, got %v", e.Files)
	}
	// Recorded as a link, not as its target's contents: otherwise replacing a
	// file with a link to identical bytes would go unnoticed, and a link out
	// of the tree would launder foreign content into a green check.
	if got == e.Files["a.proto"] {
		t.Fatal("a link must not be recorded as the hash of what it points at")
	}
	if !strings.HasPrefix(got, "symlink:") {
		t.Fatalf("a link's record must be distinguishable from a file's, got %q", got)
	}
}

// Repointing a link is a change, even though no file's bytes moved.
func TestVerify_RejectsRepointedSymlink(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	write(t, dir, "b.proto", "original")
	link := filepath.Join(dir, "link.proto")
	if err := os.Symlink("a.proto", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	e := snapshot(t, "dep", dir, whole)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.proto", link); err != nil {
		t.Fatal(err)
	}
	err := lockfile.Verify(e, dir, whole)
	if err == nil {
		t.Fatal("a repointed link must not verify")
	}
	if !strings.Contains(err.Error(), "link.proto") {
		t.Fatalf("error must name the link, got %v", err)
	}
}

// Something with no content at all still cannot be pinned.
func TestSnapshot_RefusesFifo(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.proto", "original")
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.proto"), 0o644); err != nil {
		t.Skipf("fifos unavailable: %v", err)
	}
	if _, err := lockfile.Snapshot("dep", dir, whole); err == nil {
		t.Fatal("a fifo in a pinned tree must be an error")
	}
}

func TestSnapshot_RecordsRelativeSlashPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "sub/dir/a.proto", "x")
	e := snapshot(t, "dep", dir, whole)
	if _, ok := e.Files["sub/dir/a.proto"]; !ok {
		t.Fatalf("want a relative slash-separated key, got %v", e.Files)
	}
}

// A plugin's version decides the generated bytes as surely as a dependency's
// commit does, so it belongs in the same file.
func TestSaveLoad_Plugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	in := &lockfile.Lock{
		Version: lockfile.Version,
		Plugins: []lockfile.Plugin{
			{Name: "protoc-gen-go-grpc", Module: "google.golang.org/grpc/cmd/protoc-gen-go-grpc", Version: "v1.6.0"},
			{Name: "protoc-gen-dart", Version: "unknown"},
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
		{Name: "protoc-gen-dart", Version: "unknown"},
		{Name: "protoc-gen-go-grpc", Module: "google.golang.org/grpc/cmd/protoc-gen-go-grpc", Version: "v1.6.0"},
	}
	if !reflect.DeepEqual(out.Plugins, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", out.Plugins, want)
	}
}

func TestLoad_PluginEntryValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stele.lock")
	body := "version: 1\nplugins:\n  - module: example.com/x\n    version: v1.0.0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := lockfile.Load(path)
	if err == nil || !strings.Contains(err.Error(), "plugins[0].name") {
		t.Fatalf("Load: expected a complaint about the missing name, got %v", err)
	}
}

// The lock records what can affect the build and nothing else. A README cannot
// satisfy an import; hashing it buys nothing and costs a merge conflict every
// time somebody edits one in a producer.
func TestSnapshot_IgnoresWhatCannotAffectTheBuild(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stele.yaml", "version: 1\n")
	write(t, dir, "proto/a.proto", "syntax")
	write(t, dir, "README.md", "before")
	write(t, dir, "Makefile", "all:")
	write(t, dir, "alerts/rules.yaml", "groups: []")
	write(t, dir, "proto/notes.md", "prose that sits among protos")

	sc := lockfile.Scope{Modules: []string{"proto"}, Manifest: "stele.yaml"}
	before := snapshot(t, "dep", dir, sc)
	want := []string{"proto/a.proto", "stele.yaml"}
	if got := sortedPaths(before.Files); !reflect.DeepEqual(got, want) {
		t.Fatalf("recorded files:\n got %v\nwant %v", got, want)
	}

	write(t, dir, "README.md", "after")
	after := snapshot(t, "dep", dir, sc)
	if !reflect.DeepEqual(before.Files, after.Files) {
		t.Errorf("editing a README changed the lock:\n before %v\n after  %v", before.Files, after.Files)
	}
	if err := lockfile.Verify(before, dir, sc); err != nil {
		t.Errorf("editing a README must not fail verification: %v", err)
	}
}

// The narrowing must not lose the one disagreement a build consumes in
// silence: a proto the lock does not list satisfies an import without
// contradicting any recorded hash.
func TestVerify_DetectsAnExtraProtoUnderAModuleRoot(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "proto/a.proto", "syntax")
	sc := lockfile.Scope{Modules: []string{"proto"}}
	e := snapshot(t, "dep", dir, sc)

	write(t, dir, "proto/deep/sneaked.proto", "not in the lock")
	err := lockfile.Verify(e, dir, sc)
	if err == nil {
		t.Fatal("a proto the lock does not list must be an error")
	}
	if !strings.Contains(err.Error(), "proto/deep/sneaked.proto") {
		t.Fatalf("error must name the file, got %v", err)
	}
}

func TestVerify_DetectsAChangedProto(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "proto/a.proto", "syntax")
	sc := lockfile.Scope{Modules: []string{"proto"}}
	e := snapshot(t, "dep", dir, sc)

	write(t, dir, "proto/a.proto", "syntax, tampered")
	err := lockfile.Verify(e, dir, sc)
	if err == nil || !strings.Contains(err.Error(), "proto/a.proto") {
		t.Fatalf("a changed proto must be an error naming it, got %v", err)
	}
}

// The producer's manifest decides the module roots and the transitive
// dependencies, so a change there changes the build even when no proto moved.
func TestVerify_DetectsAChangedProducerManifest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	write(t, dir, "proto/a.proto", "syntax")
	sc := lockfile.Scope{Modules: []string{"proto"}, Manifest: "stele.yaml"}
	e := snapshot(t, "dep", dir, sc)

	write(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n  - path: extra\n")
	err := lockfile.Verify(e, dir, sc)
	if err == nil || !strings.Contains(err.Error(), "stele.yaml") {
		t.Fatalf("a changed producer manifest must be an error naming it, got %v", err)
	}
}

// A producer that grows a stele.yaml beside the buf.yaml the fallback used to
// read changes which roots are covered, without touching the recorded file.
// The consequence must be an error a person can act on, not a quiet difference
// in coverage.
func TestVerify_DetectsAManifestThatTookOver(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "buf.yaml", "version: v2\n")
	write(t, dir, "proto/a.proto", "syntax")
	e := snapshot(t, "dep", dir, lockfile.Scope{Modules: []string{"proto"}, Manifest: "buf.yaml"})

	write(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	err := lockfile.Verify(e, dir, lockfile.Scope{Modules: []string{"proto"}, Manifest: "stele.yaml"})
	if err == nil {
		t.Fatal("a manifest taking over from another must not verify")
	}
	for _, want := range []string{"stele.yaml", "buf.yaml", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A module root the lock covered that the producer no longer declares leaves
// its recorded files unexplained; that has to be said, not silently narrowed.
func TestVerify_DetectsAModuleRootThatWentAway(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stele.yaml", "version: 1\n")
	write(t, dir, "proto/a.proto", "syntax")
	write(t, dir, "extra/b.proto", "syntax")
	e := snapshot(t, "dep", dir, lockfile.Scope{Modules: []string{"proto", "extra"}, Manifest: "stele.yaml"})

	err := lockfile.Verify(e, dir, lockfile.Scope{Modules: []string{"proto"}, Manifest: "stele.yaml"})
	if err == nil {
		t.Fatal("a root that went away must not verify")
	}
	for _, want := range []string{"extra", "proto", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func sortedPaths(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
