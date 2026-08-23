package pin_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
)

// A manifest with no dependencies still resolves; these tests are about the
// half of the lock that has nothing to do with fetching.
func manifest() *config.File {
	return &config.File{Version: 1, Modules: []config.Module{{Path: "."}}}
}

func noFetch() resolve.FetchFunc {
	return func(ctx context.Context, git, ref string) (string, string, error) {
		return "", "", nil
	}
}

func resolveWith(t *testing.T, dir string, plugins []lockfile.Plugin, authoritative, update bool) error {
	t.Helper()
	_, err := pin.Resolve(context.Background(), pin.Options{
		Dir:                  dir,
		Manifest:             manifest(),
		LockPath:             filepath.Join(dir, "stele.lock"),
		Fetch:                noFetch(),
		Update:               update,
		Plugins:              plugins,
		PluginsAuthoritative: authoritative,
	})
	return err
}

func TestResolve_WritesPluginsToANewLock(t *testing.T) {
	dir := t.TempDir()
	want := []lockfile.Plugin{{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.11"}}
	if err := resolveWith(t, dir, want, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	l, err := lockfile.Load(filepath.Join(dir, "stele.lock"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(l.Plugins, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", l.Plugins, want)
	}
}

// The drift this whole feature exists to end: the binary that ran is not the
// one the lock records, and nothing says so.
func TestResolve_PluginDriftIsAnError(t *testing.T) {
	dir := t.TempDir()
	locked := []lockfile.Plugin{{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.11"}}
	if err := resolveWith(t, dir, locked, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	drifted := []lockfile.Plugin{{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.6"}}
	err := resolveWith(t, dir, drifted, true, false)
	if err == nil {
		t.Fatal("Resolve: expected an error about the plugin version")
	}
	for _, want := range []string{"protoc-gen-go", "v1.36.11", "v1.36.6", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// --update is how a pin moves, here as everywhere else.
	if err := resolveWith(t, dir, drifted, true, true); err != nil {
		t.Fatalf("Resolve --update: %v", err)
	}
	l, _ := lockfile.Load(filepath.Join(dir, "stele.lock"))
	if !reflect.DeepEqual(l.Plugins, drifted) {
		t.Errorf("after --update plugins are %+v, want %+v", l.Plugins, drifted)
	}
}

// A partial run knows about some plugins only. It must not delete the record
// of the ones it did not run.
func TestResolve_PartialRunKeepsTheOtherPlugins(t *testing.T) {
	dir := t.TempDir()
	both := []lockfile.Plugin{
		{Name: "protoc-gen-dart", Origin: lockfile.OriginPath, Version: "unknown"},
		{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.11"},
	}
	if err := resolveWith(t, dir, both, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	one := []lockfile.Plugin{{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.12"}}
	if err := resolveWith(t, dir, one, false, true); err != nil {
		t.Fatalf("Resolve --update for one target: %v", err)
	}
	l, _ := lockfile.Load(filepath.Join(dir, "stele.lock"))
	want := []lockfile.Plugin{
		{Name: "protoc-gen-dart", Origin: lockfile.OriginPath, Version: "unknown"},
		{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.12"},
	}
	if !reflect.DeepEqual(l.Plugins, want) {
		t.Errorf("plugins:\n got %+v\nwant %+v", l.Plugins, want)
	}
}

// An existing lock written before plugins were recorded must keep working: a
// run against it is pinned for its dependencies and simply unpinned for its
// plugins, which is what it always was.
func TestResolve_LockWithoutPluginsStillHonoured(t *testing.T) {
	dir := t.TempDir()
	if err := lockfile.Save(filepath.Join(dir, "stele.lock"), &lockfile.Lock{Version: lockfile.Version}); err != nil {
		t.Fatal(err)
	}
	plugins := []lockfile.Plugin{{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.36.11"}}
	if err := resolveWith(t, dir, plugins, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func consumer(t *testing.T) *config.File {
	t.Helper()
	return &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "."}},
		Deps:    []config.Dep{{Name: "example", Git: "https://example.com/owner/repo.git", Ref: "main", Module: "proto"}},
	}
}

func resolveDep(t *testing.T, dir string, fetch resolve.FetchFunc, update bool) error {
	t.Helper()
	_, err := pin.Resolve(context.Background(), pin.Options{
		Dir:      dir,
		Manifest: consumer(t),
		LockPath: filepath.Join(dir, "stele.lock"),
		Fetch:    fetch,
		Update:   update,
	})
	return err
}

// moving is a remote whose branch head moves. It answers a SHA with the tree
// that SHA names, which is what makes the pin worth anything.
type moving struct {
	trees    map[string]string // sha -> tree
	head     string
	requests []string
}

func (m *moving) fetch(ctx context.Context, git, ref string) (string, string, error) {
	m.requests = append(m.requests, ref)
	sha := ref
	if ref == "main" {
		sha = m.head
	}
	dir, ok := m.trees[sha]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", source.ErrUnreachableSHA, sha)
	}
	return dir, sha, nil
}

func tree(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	writeFile(t, dir, "proto/example/a.proto", body)
	return dir
}

const (
	shaOne = "1111111111111111111111111111111111111111"
	shaTwo = "2222222222222222222222222222222222222222"
)

// The whole point of the lock: the ref moved, the pinned run did not.
func TestResolve_HonoursTheLockedSHAOverAMovedRef(t *testing.T) {
	dir := t.TempDir()
	m := &moving{trees: map[string]string{
		shaOne: tree(t, "syntax = \"proto3\";\n"),
		shaTwo: tree(t, "syntax = \"proto3\";\n// moved on\n"),
	}, head: shaOne}

	if err := resolveDep(t, dir, m.fetch, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	m.head = shaTwo
	m.requests = nil
	g, err := pin.Resolve(context.Background(), pin.Options{
		Dir:      dir,
		Manifest: consumer(t),
		LockPath: filepath.Join(dir, "stele.lock"),
		Fetch:    m.fetch,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(m.requests, []string{shaOne}) {
		t.Fatalf("the pinned run must ask for the locked commit, asked for %v", m.requests)
	}
	deps := g.Deps()
	if len(deps) != 1 || deps[0].SHA != shaOne {
		t.Fatalf("resolved %+v, want the locked commit %s", deps, shaOne)
	}
}

// A squashed merge makes a pinned commit vanish. The report has to name the
// ref the pin came from and the way out, not pass on a raw git failure.
func TestResolve_UnreachableSHANamesTheRefAndTheWayOut(t *testing.T) {
	dir := t.TempDir()
	m := &moving{trees: map[string]string{shaOne: tree(t, "syntax = \"proto3\";\n")}, head: shaOne}
	if err := resolveDep(t, dir, m.fetch, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	delete(m.trees, shaOne) // the commit was rewritten away

	err := resolveDep(t, dir, m.fetch, false)
	if err == nil {
		t.Fatal("an unreachable pinned commit must stop the run")
	}
	for _, want := range []string{shaOne, "main", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Drift, first direction: the manifest asks for something the lock does not
// pin.
func TestResolve_DependencyMissingFromTheLockIsAnError(t *testing.T) {
	dir := t.TempDir()
	m := &moving{trees: map[string]string{shaOne: tree(t, "syntax = \"proto3\";\n")}, head: shaOne}
	if err := lockfile.Save(filepath.Join(dir, "stele.lock"), &lockfile.Lock{Version: lockfile.Version}); err != nil {
		t.Fatal(err)
	}
	err := resolveDep(t, dir, m.fetch, false)
	if err == nil {
		t.Fatal("a dependency the lock does not pin must stop the run")
	}
	for _, want := range []string{"example.com/owner/repo.git", "main", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Drift, the other direction: the lock pins something no manifest asks for.
func TestResolve_DependencyNoLongerAskedForIsAnError(t *testing.T) {
	dir := t.TempDir()
	m := &moving{trees: map[string]string{shaOne: tree(t, "syntax = \"proto3\";\n")}, head: shaOne}
	if err := resolveDep(t, dir, m.fetch, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err := pin.Resolve(context.Background(), pin.Options{
		Dir:      dir,
		Manifest: manifest(), // the dependency is gone from the manifest
		LockPath: filepath.Join(dir, "stele.lock"),
		Fetch:    m.fetch,
	})
	if err == nil {
		t.Fatal("a pin nothing asks for any more must stop the run")
	}
	for _, want := range []string{"example", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolve_ALockWithoutOriginsIsNotDrift(t *testing.T) {
	dir := t.TempDir()
	old := []lockfile.Plugin{{Name: "protoc-gen-go", Version: "v1.0.0"}}
	if err := resolveWith(t, dir, old, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	now := []lockfile.Plugin{{Name: "protoc-gen-go", Origin: lockfile.OriginPath, Version: "v1.0.0"}}
	if err := resolveWith(t, dir, now, true, false); err != nil {
		t.Fatalf("Resolve over a lock written before origins were recorded: %v", err)
	}
}

// A plugin the manifest pins — module@version, or a url with its digest — is
// already exactly named by the manifest. Recording it here would be a second
// copy of that fact, and a second copy can only drift from the first, so the
// lock says nothing about it.
func TestResolve_PinnedPluginsAreNotRecorded(t *testing.T) {
	dir := t.TempDir()
	pinned := []lockfile.Plugin{
		{Name: "protoc-gen-go", Origin: lockfile.OriginManaged, Version: "v1.0.0"},
		{Name: "protoc-gen-dart", Origin: lockfile.OriginURL, Version: "unknown"},
	}
	if err := resolveWith(t, dir, pinned, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	l, err := lockfile.Load(filepath.Join(dir, "stele.lock"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(l.Plugins) != 0 {
		t.Fatalf("the lock records pinned plugins: %+v", l.Plugins)
	}
	// And the next run, whose manifest may legitimately name a different
	// version or another platform's digest, is not drift against a record
	// that no longer exists.
	moved := []lockfile.Plugin{
		{Name: "protoc-gen-go", Origin: lockfile.OriginManaged, Version: "v2.0.0"},
		{Name: "protoc-gen-dart", Origin: lockfile.OriginURL, Version: "unknown"},
	}
	if err := resolveWith(t, dir, moved, true, false); err != nil {
		t.Fatalf("a pinned plugin must not be compared against the lock: %v", err)
	}
}

// A lock on disk that still lists pinned plugins keeps loading, and --update
// rewrites it without them, leaving the observations alone.
func TestResolve_OldLockWithPinnedPluginsIsRewrittenClean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stele.lock")
	body := "version: 1\nplugins:\n" +
		"  - name: protoc-gen-go\n    origin: managed\n    module: example.com/x\n    version: v1.0.0\n" +
		"  - name: protoc-gen-found\n    origin: path\n    version: v0.3.0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	now := []lockfile.Plugin{
		{Name: "protoc-gen-go", Origin: lockfile.OriginManaged, Version: "v1.0.0"},
		{Name: "protoc-gen-found", Origin: lockfile.OriginPath, Version: "v0.3.0"},
	}
	if err := resolveWith(t, dir, now, true, false); err != nil {
		t.Fatalf("an existing lock must keep working: %v", err)
	}
	if err := resolveWith(t, dir, now, true, true); err != nil {
		t.Fatalf("Resolve --update: %v", err)
	}
	l, err := lockfile.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []lockfile.Plugin{{Name: "protoc-gen-found", Origin: lockfile.OriginPath, Version: "v0.3.0"}}
	if !reflect.DeepEqual(l.Plugins, want) {
		t.Errorf("plugins after --update:\n got %+v\nwant %+v", l.Plugins, want)
	}
}

// What the lock does carry is the unpinned tiers, where the manifest pins
// nothing and what ran is whatever the machine had. A binary that moved under
// the run is the drift that produced different bytes in CI and on a laptop
// with nothing to show for it.
func TestResolve_PathPluginDriftIsAnError(t *testing.T) {
	dir := t.TempDir()
	found := []lockfile.Plugin{{Name: "protoc-gen-found", Origin: lockfile.OriginPath, Version: "v0.3.0"}}
	if err := resolveWith(t, dir, found, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	moved := []lockfile.Plugin{{Name: "protoc-gen-found", Origin: lockfile.OriginPath, Version: "v0.4.0"}}
	err := resolveWith(t, dir, moved, true, false)
	if err == nil {
		t.Fatal("a plugin that moved under the run must stop it")
	}
	for _, want := range []string{"protoc-gen-found", "v0.3.0", "v0.4.0", "--update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
