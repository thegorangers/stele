package pin_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thegorangers/stele/internal/config"
	"github.com/thegorangers/stele/internal/lockfile"
	"github.com/thegorangers/stele/internal/pin"
	"github.com/thegorangers/stele/internal/resolve"
	"github.com/thegorangers/stele/internal/source"
)

// Milestone 5 at the layer where a failure is actually read: what the lock does
// when a fetch cannot be completed. The fetcher here is the real one, driven
// against a git repository in a temporary directory — the pinned path, the
// cache and the lock's (git, ref) identity are all exercised, and nothing
// reaches the network.

// gitRepo builds a local repository holding one module and returns its path
// together with the commit it created.
func gitRepo(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	dir := t.TempDir()
	home := t.TempDir()
	writeFile(t, dir, "stele.yaml", "version: 1\nmodules:\n  - path: proto\n")
	writeFile(t, dir, "proto/example/a.proto", "syntax = \"proto3\";\n")
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "nonexistent"),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--initial-branch=main", "--quiet")
	run("add", "-A")
	run("commit", "--quiet", "-m", "initial")
	return dir, run("rev-parse", "HEAD")
}

// realFetch is the fetcher the commands use, pointed at a cache of the test's
// own.
func realFetch(cache string) resolve.FetchFunc {
	return func(ctx context.Context, git, ref string) (string, string, error) {
		return source.FetchInto(ctx, cache, git, ref)
	}
}

func manifestFor(repo string) *config.File {
	return &config.File{
		Version: 1,
		Modules: []config.Module{{Path: "."}},
		Deps:    []config.Dep{{Name: "example", Git: repo, Ref: "main", Module: "proto"}},
	}
}

// TestResolve_LockedCommitGoneSurvivesTheRealPath checks that the message pin
// wraps ErrUnreachableSHA in reaches a reader when the error comes from git
// rather than from a test double. The pinned SHA is one no repository holds,
// which is what a squash merge leaves behind.
func TestResolve_LockedCommitGoneSurvivesTheRealPath(t *testing.T) {
	repo, _ := gitRepo(t)
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "stele.lock")
	gone := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := lockfile.Save(lockPath, &lockfile.Lock{
		Version: lockfile.Version,
		Deps:    []lockfile.Entry{{Name: "example", Git: repo, Ref: "main", SHA: gone}},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := pin.Resolve(context.Background(), pin.Options{
		Dir: dir, Manifest: manifestFor(repo), LockPath: lockPath, Fetch: realFetch(t.TempDir()),
	})
	if err == nil {
		t.Fatal("want an error for a locked commit the remote does not hold")
	}
	if !errors.Is(err, source.ErrUnreachableSHA) {
		t.Fatalf("the sentinel must survive the real path; got %v", err)
	}
	for _, want := range []string{gone, "main", repo, "--update", "squash"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message must mention %q; got %q", want, err)
		}
	}
	t.Logf("message:\n%v", err)
}

// TestResolve_UpdateThatCannotFetchLeavesTheLockAlone is the failure that would
// be worst to get wrong: --update is the only thing that moves a pin, so an
// --update that cannot reach the remote must leave the previous pins exactly
// as they were rather than writing a lock describing a build that never ran.
func TestResolve_UpdateThatCannotFetchLeavesTheLockAlone(t *testing.T) {
	repo, sha := gitRepo(t)
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "stele.lock")
	if err := lockfile.Save(lockPath, &lockfile.Lock{
		Version: lockfile.Version,
		Deps:    []lockfile.Entry{{Name: "example", Git: repo, Ref: "main", SHA: sha}},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	// The remote is gone by the time --update runs.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	_, err = pin.Resolve(context.Background(), pin.Options{
		Dir: dir, Manifest: manifestFor(repo), LockPath: lockPath,
		Fetch: realFetch(t.TempDir()), Update: true,
	})
	if err == nil {
		t.Fatal("want an error when --update cannot reach the remote")
	}
	if !strings.Contains(err.Error(), repo) {
		t.Errorf("the message must name the dependency; got %q", err)
	}
	after, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("--update destroyed the lock when it could not fetch: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("a failed --update rewrote the lock:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestResolve_PinnedRunUsesTheCacheWithNoGitAtAll is the property every other
// failure here rests on: once a pinned commit is in the cache, a run needs
// nothing from the outside world. git is removed from PATH and the remote is
// deleted, and the run still resolves.
func TestResolve_PinnedRunUsesTheCacheWithNoGitAtAll(t *testing.T) {
	repo, sha := gitRepo(t)
	dir := t.TempDir()
	cache := t.TempDir()
	lockPath := filepath.Join(dir, "stele.lock")
	if err := lockfile.Save(lockPath, &lockfile.Lock{
		Version: lockfile.Version,
		Deps:    []lockfile.Entry{{Name: "example", Git: repo, Ref: "main", SHA: sha}},
	}); err != nil {
		t.Fatal(err)
	}
	opts := pin.Options{Dir: dir, Manifest: manifestFor(repo), LockPath: lockPath, Fetch: realFetch(cache)}
	if _, err := pin.Resolve(context.Background(), opts); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}

	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if _, err := pin.Resolve(context.Background(), opts); err != nil {
		t.Fatalf("a pinned run with a warm cache must need neither git nor the remote: %v", err)
	}
}
