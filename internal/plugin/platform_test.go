package plugin_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/plugin"
)

// A download pins bytes, and bytes are per-platform: one url and one digest
// cannot serve linux/amd64 and darwin/arm64 at once. So the manifest declares
// one entry per platform, and exactly one of them may match the machine the
// tool is running on.

func TestSelect_TakesTheEntryForTheRunningPlatform(t *testing.T) {
	ds := []plugin.Download{
		{OS: "darwin", Arch: "arm64", URL: "https://example.com/mac", SHA256: "a"},
		{OS: "linux", Arch: "amd64", URL: "https://example.com/linux", SHA256: "b"},
	}
	got, err := plugin.Select("protoc-gen-dart", ds, "linux", "amd64")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.URL != "https://example.com/linux" {
		t.Errorf("chose %q, want the linux/amd64 entry", got.URL)
	}
}

// Two entries for one platform are two answers to a question with one answer.
// Whichever the tool honoured, the manifest would describe a binary that never
// ran — so it honours neither, and names both.
func TestSelect_TwoEntriesForOnePlatformNameBoth(t *testing.T) {
	ds := []plugin.Download{
		{OS: "linux", Arch: "amd64", URL: "https://example.com/first", SHA256: "a"},
		{OS: "linux", Arch: "amd64", URL: "https://example.com/second", SHA256: "b"},
	}
	_, err := plugin.Select("protoc-gen-dart", ds, "linux", "amd64")
	if err == nil {
		t.Fatal("Select: expected an error")
	}
	for _, want := range []string{"linux/amd64", "https://example.com/first", "https://example.com/second"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// No entry for this platform is a refusal, not a shrug. The error says which
// platform it is and which ones the manifest does cover, because the fix is to
// add one and the author needs to know what to copy.
func TestSelect_NoEntryNamesThePlatformAndWhatIsCovered(t *testing.T) {
	ds := []plugin.Download{
		{OS: "linux", Arch: "amd64", URL: "https://example.com/linux", SHA256: "a"},
		{OS: "darwin", Arch: "arm64", URL: "https://example.com/mac", SHA256: "b"},
	}
	_, err := plugin.Select("protoc-gen-dart", ds, "windows", "amd64")
	if err == nil {
		t.Fatal("Select: expected an error")
	}
	for _, want := range []string{"protoc-gen-dart", "windows/amd64", "linux/amd64", "darwin/arm64"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// Resolve takes the entry for the machine it is on, downloads exactly that,
// and records which entry it used.
func TestCache_ResolveDownloadsForTheRunningPlatform(t *testing.T) {
	body := []byte(script)
	url, _, _ := serve(t, body)
	cache := plugin.Cache{Root: t.TempDir()}

	got, err := cache.Resolve(context.Background(), plugin.Spec{
		Name: "protoc-gen-dart",
		Downloads: []plugin.Download{
			// A platform that is not this one, pinned to a digest nothing
			// will ever match: choosing it would fail loudly.
			{OS: "plan9", Arch: "mips64", URL: url + "-wrong", SHA256: strings.Repeat("f", 64)},
			{OS: runtime.GOOS, Arch: runtime.GOARCH, URL: url, SHA256: sum(body)},
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Origin != plugin.OriginURL {
		t.Errorf("origin %q, want %q", got.Origin, plugin.OriginURL)
	}
	if got.URL != url || got.SHA256 != sum(body) {
		t.Errorf("resolved %+v, want the entry for %s/%s", got, runtime.GOOS, runtime.GOARCH)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Errorf("platform %s/%s, want %s/%s", got.OS, got.Arch, runtime.GOOS, runtime.GOARCH)
	}
}

// The one thing that must never happen: a manifest that covers no platform for
// this machine falling back to whatever is on PATH. That is how the un-pinned
// binary gets back in, silently, on exactly the machine whose platform nobody
// thought about.
func TestCache_ResolveUnsupportedPlatformDoesNotFallBackToPATH(t *testing.T) {
	dir := t.TempDir()
	onPath := filepath.Join(dir, "protoc-gen-dart")
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := plugin.Cache{Root: t.TempDir()}.Resolve(context.Background(), plugin.Spec{
		Name: "protoc-gen-dart",
		Downloads: []plugin.Download{
			{OS: "plan9", Arch: "mips64", URL: "https://example.com/plan9", SHA256: strings.Repeat("a", 64)},
		},
	})
	if err == nil {
		t.Fatalf("Resolve: expected a refusal, got the binary at %q", got.Path)
	}
	if got.Path == onPath {
		t.Fatal("Resolve fell back to PATH, which un-pins the plugin")
	}
	if !strings.Contains(err.Error(), runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("error %q does not name the platform it ran on", err)
	}
}
