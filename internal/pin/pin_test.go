package pin_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/resolve"
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
	want := []lockfile.Plugin{{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11"}}
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
	locked := []lockfile.Plugin{{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11"}}
	if err := resolveWith(t, dir, locked, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	drifted := []lockfile.Plugin{{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.6"}}
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
		{Name: "protoc-gen-dart", Version: "unknown"},
		{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11"},
	}
	if err := resolveWith(t, dir, both, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	one := []lockfile.Plugin{{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.12"}}
	if err := resolveWith(t, dir, one, false, true); err != nil {
		t.Fatalf("Resolve --update for one target: %v", err)
	}
	l, _ := lockfile.Load(filepath.Join(dir, "stele.lock"))
	want := []lockfile.Plugin{
		{Name: "protoc-gen-dart", Version: "unknown"},
		{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.12"},
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
	plugins := []lockfile.Plugin{{Name: "protoc-gen-go", Module: "google.golang.org/protobuf/cmd/protoc-gen-go", Version: "v1.36.11"}}
	if err := resolveWith(t, dir, plugins, true, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}
