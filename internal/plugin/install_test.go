package plugin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/plugin"
)

// The installer is exercised against a module proxy built in a temporary
// directory, so the tests install a real module with the real toolchain and
// still never touch the network. GOFLAGS, GOMODCACHE and GOPROXY are set for
// the process, which is why these tests do not run in parallel.
const (
	fakeModule  = "example.com/protoc-gen-fake"
	fakeVersion = "v1.2.3"
)

// hermeticGo points the toolchain at a proxy holding one module, and at a
// module cache of its own.
func hermeticGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain on PATH")
	}
	proxy := writeProxy(t, fakeModule, fakeVersion)
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOSUMDB", "off")
	// GOPRIVATE and GONOSUMDB are cleared rather than set: either of them
	// matching the module would send the toolchain past the proxy and out to
	// the network, which is exactly what these tests must not do.
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOSUMDB", "")
	t.Setenv("GONOSUMCHECK", "")
	t.Setenv("GONOSUMVERIFY", "")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", modCache(t))
}

// modCache is a module cache of the test's own, made removable again on the
// way out: the toolchain writes its extracted modules read-only, and a plain
// t.TempDir cleanup cannot delete them.
func modCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			_ = os.Chmod(path, 0o700)
			return nil
		})
	})
	return dir
}

// writeProxy lays out a GOPROXY=file:// tree holding one version of one
// module, whose main package prints its own build metadata on demand.
func writeProxy(t *testing.T, mod, version string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(mod), "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gomod := "module " + mod + "\n\ngo 1.21\n"
	main := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"fake plugin\") }\n"

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"go.mod": gomod, "main.go": main} {
		w, err := zw.Create(mod + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		version + ".info": []byte(`{"Version":"` + version + `"}`),
		version + ".mod":  []byte(gomod),
		version + ".zip":  buf.Bytes(),
		"list":            []byte(version + "\n"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCache_EnsureInstallsTheDeclaredVersion(t *testing.T) {
	hermeticGo(t)
	cache := plugin.Cache{Root: t.TempDir()}

	bin, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !strings.HasPrefix(bin, cache.Root) {
		t.Errorf("Ensure returned %q, which is outside the cache %q", bin, cache.Root)
	}
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatalf("reading the installed binary's build metadata: %v", err)
	}
	if info.Main.Version != fakeVersion {
		t.Errorf("installed version %q, want %q", info.Main.Version, fakeVersion)
	}
}

// A second run must not need the network: that is the whole reason the cache
// is keyed by module and version.
func TestCache_EnsureIsOfflineOnceCached(t *testing.T) {
	hermeticGo(t)
	cache := plugin.Cache{Root: t.TempDir()}
	first, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	t.Setenv("GOPROXY", "off")
	t.Setenv("PATH", "") // and no toolchain either
	second, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err != nil {
		t.Fatalf("Ensure from the cache: %v", err)
	}
	if second != first {
		t.Errorf("second Ensure returned %q, want %q", second, first)
	}
}

// A cached entry is verified, not trusted: a file that carries no module
// metadata is not the version that was asked for, whatever its name says.
func TestCache_EnsureRejectsAnUnverifiableCacheEntry(t *testing.T) {
	hermeticGo(t)
	cache := plugin.Cache{Root: t.TempDir()}
	good, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := os.WriteFile(good, []byte("#!/bin/sh\necho not a go binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err == nil {
		t.Fatal("Ensure: expected an error for a binary with no module metadata")
	}
	if !strings.Contains(err.Error(), fakeModule) || !strings.Contains(err.Error(), fakeVersion) {
		t.Errorf("error %q names neither the module nor the version", err)
	}
}

// Installing needs the network and a toolchain. When either is missing the
// message has to say which, and what to do.
func TestCache_EnsureWithoutAToolchain(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cache := plugin.Cache{Root: t.TempDir()}
	_, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err == nil {
		t.Fatal("Ensure: expected an error when there is no go toolchain")
	}
	if !strings.Contains(err.Error(), "go toolchain") {
		t.Errorf("error %q does not mention the missing go toolchain", err)
	}
}

func TestCache_EnsureWithoutNetwork(t *testing.T) {
	hermeticGo(t)
	t.Setenv("GOPROXY", "off")
	cache := plugin.Cache{Root: t.TempDir()}
	_, err := cache.Ensure(context.Background(), fakeModule, fakeVersion)
	if err == nil {
		t.Fatal("Ensure: expected an error when the module cannot be downloaded")
	}
	for _, want := range []string{fakeModule + "@" + fakeVersion, "network"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}
