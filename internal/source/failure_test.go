package source_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thegorangers/stele/internal/source"
)

// The tests in this file are milestone 5: what the fetcher does when the world
// does not cooperate. Nothing here reaches the network — a remote is either a
// local repository, a path that does not exist, or an httptest server that
// answers wrongly on purpose.

// waitForScratch blocks until a fetch has created its scratch directory under
// cache and put something in it, so that an interruption lands in the middle
// of a clone rather than before it starts.
func waitForScratch(t *testing.T, cache string) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		found := false
		filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() || !strings.HasPrefix(d.Name(), tempDirPrefix) {
				return nil
			}
			if entries, err := os.ReadDir(path); err == nil && len(entries) > 0 {
				found = true
			}
			return nil
		})
		if found {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// scratchDirs lists the scratch directories currently under cache.
func scratchDirs(t *testing.T, cache string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), tempDirPrefix) {
			out = append(out, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFetch_InterruptedCloneLeavesNothingUsable interrupts a clone while git is
// running — the context is cancelled once the scratch directory has content,
// which kills the git process — and asserts the two things that matter: the
// cache holds no entry a later run could mistake for a complete tree, and the
// next run succeeds.
func TestFetch_InterruptedCloneLeavesNothingUsable(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		files["api/"+n+".proto"] = "syntax = \"proto3\";\n" + strings.Repeat("// padding\n", 4000)
	}
	repo := initTestRepo(t, files)
	cache := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if !waitForScratch(t, cache) {
			t.Error("the fetch never created a scratch directory to interrupt")
		}
		cancel()
	}()
	_, _, err := source.FetchInto(ctx, cache, repo, "HEAD")
	wg.Wait()
	if err == nil {
		t.Skip("the fetch completed before it could be interrupted; nothing to assert")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if dirs := scratchDirs(t, cache); len(dirs) > 0 {
		t.Errorf("scratch directories left behind: %v", dirs)
	}
	assertNoFinalEntry(t, cache)

	// The next run must succeed, and succeed completely.
	dir, _, err := source.FetchInto(context.Background(), cache, repo, "HEAD")
	if err != nil {
		t.Fatalf("the run after an interrupted one must succeed: %v", err)
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("the entry written after an interruption is incomplete: %v", err)
		}
	}
}

// assertNoFinalEntry checks the cache holds no directory that looks like a
// published entry: a 40-character name under a repository path.
func assertNoFinalEntry(t *testing.T, cache string) {
	t.Helper()
	if err := filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && len(d.Name()) == 40 && !strings.ContainsAny(d.Name(), "./") {
			t.Errorf("a published-looking cache entry survives an interrupted clone: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestFetch_AbandonedScratchDirIsNotACacheHit is the case a cancelled context
// cannot produce: the process itself is killed, so no deferred cleanup runs and
// a half-populated scratch directory is still on disk when the next run starts.
// It is built by hand for exactly that reason — a SIGKILL leaves no way to ask
// the dead process what it had written.
func TestFetch_AbandonedScratchDirIsNotACacheHit(t *testing.T) {
	repo := initTestRepo(t, map[string]string{
		"api/a.proto": "syntax = \"proto3\";\npackage api;\n",
		"api/b.proto": "syntax = \"proto3\";\n",
	})
	cache := t.TempDir()

	dir, sha, err := source.FetchInto(context.Background(), cache, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// What a killed process leaves: a scratch sibling holding half a tree.
	orphan, err := os.MkdirTemp(filepath.Dir(dir), tempDirPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(orphan, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "api", "a.proto"), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, gotSHA, err := source.FetchInto(context.Background(), cache, repo, sha)
	if err != nil {
		t.Fatalf("a fetch beside an abandoned scratch directory must succeed: %v", err)
	}
	if got == orphan {
		t.Fatal("an abandoned scratch directory was handed back as a cache entry")
	}
	if gotSHA != sha {
		t.Fatalf("SHA = %q, want %q", gotSHA, sha)
	}
	b, err := os.ReadFile(filepath.Join(got, "api", "a.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "half" {
		t.Fatal("the half-written tree became the cache entry")
	}
}

// TestFetch_UnreachableRemoteSaysWhatToDo is a cold cache with the remote out
// of reach. A raw git error is not an answer: the message has to name the
// repository, say what failed, and say what the reader can do about it.
func TestFetch_UnreachableRemoteSaysWhatToDo(t *testing.T) {
	requireGit(t)
	missing := filepath.Join(t.TempDir(), "no-such-repository")
	cache := t.TempDir()

	_, _, err := source.FetchInto(context.Background(), cache, missing, "main")
	if err == nil {
		t.Fatal("want an error for a remote that is not there")
	}
	assertActionable(t, err, missing, "main")
}

// TestFetch_AuthRefusedSaysItIsAuthentication serves a git endpoint that
// refuses without credentials. With terminal prompting off — which is how CI
// runs — git fails at authentication, and the message must say so rather than
// leaving a reader to recognise a git transport error.
func TestFetch_AuthRefusedSaysItIsAuthentication(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", filepath.Join(t.TempDir(), "no-such-askpass"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	url := srv.URL + "/acme/api.git"
	cache := t.TempDir()

	_, _, err := source.FetchInto(context.Background(), cache, url, "main")
	if err == nil {
		t.Fatal("want an error when the remote refuses to authenticate")
	}
	assertActionable(t, err, url, "main")
	if !strings.Contains(strings.ToLower(err.Error()), "authenticat") {
		t.Fatalf("the message must say the refusal was authentication; got %q", err)
	}
}

// TestFetch_ServerErrorIsNotReportedAsAuthentication keeps the message honest
// in the other direction: a remote that answers with a server error is not an
// authentication problem, and saying so would send a reader after credentials
// that are fine.
func TestFetch_ServerErrorIsNotReportedAsAuthentication(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	url := srv.URL + "/acme/api.git"

	_, _, err := source.FetchInto(context.Background(), t.TempDir(), url, "main")
	if err == nil {
		t.Fatal("want an error when the remote answers with a server error")
	}
	assertActionable(t, err, url, "main")
	if strings.Contains(strings.ToLower(err.Error()), "authenticat") {
		t.Fatalf("a server error is not an authentication failure; got %q", err)
	}
}

// TestFetch_ClosedMidResponseSaysWhatToDo covers the transport that dies in the
// middle rather than refusing at the start.
func TestFetch_ClosedMidResponseSaysWhatToDo(t *testing.T) {
	requireGit(t)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("001e# service=git-upload-pack\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
		}
	}))
	defer srv.Close()
	url := srv.URL + "/acme/api.git"
	cache := t.TempDir()

	_, _, err := source.FetchInto(context.Background(), cache, url, "main")
	if err == nil {
		t.Fatal("want an error when the remote closes mid-response")
	}
	assertActionable(t, err, url, "main")
	if dirs := scratchDirs(t, cache); len(dirs) > 0 {
		t.Errorf("scratch directories left behind: %v", dirs)
	}
	assertNoFinalEntry(t, cache)
}

// assertActionable holds every fetch failure to the same standard: it names the
// dependency, it names what was asked for, and it says what the reader can do.
func assertActionable(t *testing.T, err error, url, ref string) {
	t.Helper()
	msg := err.Error()
	if !strings.Contains(msg, url) {
		t.Errorf("the message must name the repository %q; got %q", url, msg)
	}
	if !strings.Contains(msg, ref) {
		t.Errorf("the message must name the ref %q; got %q", ref, msg)
	}
	// git's own words are quoted rather than being the whole message, and the
	// tool says something of its own about the recovery.
	if !strings.Contains(msg, "git said:") {
		t.Errorf("the message must quote what git said; got %q", msg)
	}
	if !strings.Contains(msg, "cache") {
		t.Errorf("the message must say what the cache means for this failure; got %q", msg)
	}
	if !strings.Contains(msg, "Check") {
		t.Errorf("the message must say what to check; got %q", msg)
	}
}

// TestFetch_ConcurrentDifferentRefsOfOneRepository is the second shape of the
// concurrency the cache is built for: not one entry contended, but two entries
// of the same repository written side by side.
func TestFetch_ConcurrentDifferentRefsOfOneRepository(t *testing.T) {
	repo := initTestRepo(t, map[string]string{"api/a.proto": "syntax = \"proto3\";\n"})
	second := commitMore(t, repo, map[string]string{"api/b.proto": "syntax = \"proto3\";\n"})
	tagRepo(t, repo, "v2.0.0")
	cache := t.TempDir()

	type result struct {
		dir, sha string
		err      error
	}
	refs := []string{"main", "v2.0.0", "main", "v2.0.0"}
	results := make([]result, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, s, err := source.FetchInto(context.Background(), cache, repo, ref)
			results[i] = result{d, s, err}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("ref %q: %v", refs[i], r.err)
		}
		if r.sha != second {
			t.Errorf("ref %q resolved to %q, want %q", refs[i], r.sha, second)
		}
		for _, want := range []string{"api/a.proto", "api/b.proto"} {
			if _, err := os.Stat(filepath.Join(r.dir, filepath.FromSlash(want))); err != nil {
				t.Errorf("ref %q: entry visible without its contents: %v", refs[i], err)
			}
		}
	}
}

// TestFetch_ReadWhileAnotherRunWrites reads a cold cache entry repeatedly while
// another goroutine populates it. Every read must see either nothing or a
// complete tree; a directory that exists but is empty is the failure this test
// exists to catch.
func TestFetch_ReadWhileAnotherRunWrites(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d"} {
		files["api/"+n+".proto"] = "syntax = \"proto3\";\n" + strings.Repeat("// padding\n", 2000)
	}
	repo := initTestRepo(t, files)
	cache := t.TempDir()
	sha, err := source.ResolveRef(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	final := source.CacheDir(cache, repo, sha)

	done := make(chan struct{})
	var bad []string
	var mu sync.Mutex
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := os.Stat(final); err == nil {
				for name := range files {
					if _, err := os.Stat(filepath.Join(final, filepath.FromSlash(name))); err != nil {
						mu.Lock()
						bad = append(bad, err.Error())
						mu.Unlock()
					}
				}
			}
		}
	}()
	if _, _, err := source.FetchInto(context.Background(), cache, repo, "HEAD"); err != nil {
		t.Fatal(err)
	}
	close(done)
	mu.Lock()
	defer mu.Unlock()
	if len(bad) > 0 {
		t.Fatalf("a reader saw the entry before it was complete: %v", bad[0])
	}
}
