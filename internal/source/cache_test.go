package source_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thegorangers/stele/internal/source"
)

// TestCache_ConcurrentFetchNeverSeesPartialDir is the reason the cache write is
// atomic. A CI runner keeps several jobs on one shared HOME, so several
// processes populate the same cold cache entry at the same time. If a directory
// became visible before it was fully populated, a concurrent reader would see a
// half-written tree and report a missing import once every N runs.
func TestCache_ConcurrentFetchNeverSeesPartialDir(t *testing.T) {
	repo := initTestRepo(t, map[string]string{
		"api/a.proto": "syntax = \"proto3\";\n",
		"api/b.proto": "syntax = \"proto3\";\n",
	})
	cache := t.TempDir()

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	shas := make(chan string, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir, sha, err := source.FetchInto(context.Background(), cache, repo, "HEAD")
			if err != nil {
				errs <- err
				return
			}
			shas <- sha
			for _, want := range []string{"api/a.proto", "api/b.proto"} {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
					errs <- fmt.Errorf("directory visible without its contents: %w", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(shas)
	for err := range errs {
		t.Error(err)
	}
	first := ""
	for sha := range shas {
		if first == "" {
			first = sha
		}
		if sha != first {
			t.Errorf("the same ref resolved to two SHAs: %q and %q", first, sha)
		}
	}
}

// TestCache_SecondFetchIsOffline proves the cache is immutable: an entry that
// is already present is reused without asking git anything. The check is a hard
// one — git is removed from PATH for the second call, so any process the
// fetcher tried to start would fail.
func TestCache_SecondFetchIsOffline(t *testing.T) {
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	cache := t.TempDir()

	dir, sha, err := source.FetchInto(context.Background(), cache, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	dir2, sha2, err := source.FetchInto(context.Background(), cache, repo, sha)
	if err != nil {
		t.Fatalf("a cached entry must be reused without touching git: %v", err)
	}
	if dir2 != dir || sha2 != sha {
		t.Fatalf("cached fetch returned (%q, %q), want (%q, %q)", dir2, sha2, dir, sha)
	}
}

// TestCache_KeyKeepsMultiSegmentRepoPaths guards the GitLab shape: a repository
// path can carry subgroups, so the cache must not be laid out assuming exactly
// two path components.
func TestCache_KeyKeepsMultiSegmentRepoPaths(t *testing.T) {
	addr, err := source.ParseAddr("https://example.com/acme/group/sub/project.git", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := source.CacheDir("/cache", addr.CloneURL(), "0123456789abcdef0123456789abcdef01234567")
	want := filepath.Join("/cache", "example.com", "acme", "group", "sub", "project", "0123456789abcdef0123456789abcdef01234567")
	if got != want {
		t.Fatalf("CacheDir = %q, want %q", got, want)
	}
}

// TestCache_NoTempDirectoryIsLeftBehind checks that a failed fetch cleans up:
// a leftover scratch directory would grow without bound on a CI runner.
func TestCache_NoTempDirectoryIsLeftBehind(t *testing.T) {
	requireGit(t)
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	cache := t.TempDir()

	if _, _, err := source.FetchInto(context.Background(), cache, repo, "refs/heads/does-not-exist"); err == nil {
		t.Fatal("fetching a missing ref must fail")
	}
	var leftovers []string
	err := filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), tempDirPrefix) {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) > 0 {
		t.Fatalf("temporary directories left behind: %v", leftovers)
	}
}

// tempDirPrefix mirrors the prefix the implementation uses for its scratch
// directories. It is duplicated on purpose: the test asserts on the observable
// filesystem, not on an exported implementation detail.
const tempDirPrefix = ".tmp-"
