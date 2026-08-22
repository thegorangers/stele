package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/source"
)

// TestFetch_UnreachableSHAGivesActionableError covers the failure a lockfile
// makes routine: a squash merge replaces the commit a pinned SHA names, the
// object disappears from the remote, and a raw git error tells the reader
// nothing about what to do next.
func TestFetch_UnreachableSHAGivesActionableError(t *testing.T) {
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	cache := t.TempDir()

	_, _, err := source.FetchInto(context.Background(), cache, repo, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if !errors.Is(err, source.ErrUnreachableSHA) {
		t.Fatalf("want ErrUnreachableSHA, got %v", err)
	}
	if !strings.Contains(err.Error(), "--update") {
		t.Fatalf("the message must point at the way out; got %q", err)
	}
}

// TestFetch_ResolvesBranchTagAndSHAToTheSameCommit checks the three shapes a
// ref can take all land on one commit and one cache entry.
func TestFetch_ResolvesBranchTagAndSHAToTheSameCommit(t *testing.T) {
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	tagRepo(t, repo, "v1.0.0")
	cache := t.TempDir()

	dirs := map[string]string{}
	var sha string
	for _, ref := range []string{"main", "v1.0.0", "HEAD"} {
		d, s, err := source.FetchInto(context.Background(), cache, repo, ref)
		if err != nil {
			t.Fatalf("ref %q: %v", ref, err)
		}
		dirs[ref] = d
		if sha != "" && s != sha {
			t.Fatalf("ref %q resolved to %q, want %q", ref, s, sha)
		}
		sha = s
	}
	byShaDir, byShaSHA, err := source.FetchInto(context.Background(), cache, repo, sha)
	if err != nil {
		t.Fatal(err)
	}
	if byShaSHA != sha || byShaDir != dirs["main"] {
		t.Fatalf("fetching by SHA gave (%q, %q), want (%q, %q)", byShaDir, byShaSHA, dirs["main"], sha)
	}
	if len(sha) != 40 {
		t.Fatalf("resolved ref is not a full SHA: %q", sha)
	}
}

// TestFetch_MaterialisesTheWorkingTree checks the entry holds the files
// themselves, not just a repository to check out later.
func TestFetch_MaterialisesTheWorkingTree(t *testing.T) {
	repo := initTestRepo(t, map[string]string{
		"api/v1/service.proto": "syntax = \"proto3\";\npackage api.v1;\n",
		"README.md":            "example\n",
	})
	cache := t.TempDir()

	dir, _, err := source.FetchInto(context.Background(), cache, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "api", "v1", "service.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "package api.v1;") {
		t.Fatalf("unexpected file contents: %q", b)
	}
}

// TestFetch_HonoursContextCancellation checks a cancelled context stops the
// fetch instead of running it to completion.
func TestFetch_HonoursContextCancellation(t *testing.T) {
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	cache := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := source.FetchInto(ctx, cache, repo, "HEAD"); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// TestFetch_UnknownRefNamesTheRef checks the error for a ref that never existed
// is distinct from the unreachable-SHA one and names what was asked for.
func TestFetch_UnknownRefNamesTheRef(t *testing.T) {
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	cache := t.TempDir()

	_, _, err := source.FetchInto(context.Background(), cache, repo, "no-such-branch")
	if err == nil {
		t.Fatal("want an error for a ref that does not exist")
	}
	if errors.Is(err, source.ErrUnreachableSHA) {
		t.Fatalf("a name that never existed is not an unreachable SHA: %v", err)
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("the error must name the ref; got %q", err)
	}
}
