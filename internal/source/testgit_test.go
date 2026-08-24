package source_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when there is no git binary to drive. The fetcher
// shells out to the system git on purpose (authentication), so without it
// there is nothing to test.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
}

// gitEnv returns a hermetic environment for test repositories: a fixed
// identity so commits are reproducible, and no system or global config so a
// developer's own settings cannot change the outcome.
func gitEnv(home string) []string {
	return []string{
		"PATH=" + pathEnv(),
		"HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "nonexistent-gitconfig"),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
}

func pathEnv() string {
	p, err := exec.LookPath("git")
	if err != nil {
		return "/usr/bin:/bin"
	}
	return filepath.Dir(p) + ":/usr/bin:/bin"
}

// initTestRepo creates a local git repository holding files and returns its
// path. Tests fetch from it over the filesystem: no network, no environment.
func initTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()
	home := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv(home)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--initial-branch=main", "--quiet")
	writeFiles(t, dir, files)
	run("add", "-A")
	run("commit", "--quiet", "-m", "initial")
	return dir
}

// writeFiles materialises a map of slash-separated paths into dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// tagRepo puts an annotated tag on the current commit of a test repository.
func tagRepo(t *testing.T, dir, tag string) {
	t.Helper()
	cmd := exec.Command("git", "tag", "-a", tag, "-m", tag)
	cmd.Dir = dir
	cmd.Env = gitEnv(t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git tag: %v\n%s", err, out)
	}
}

// commitMore adds files to a test repository as a second commit and returns
// the SHA it created.
func commitMore(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv(home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeFiles(t, dir, files)
	run("add", "-A")
	run("commit", "--quiet", "-m", "more")
	return run("rev-parse", "HEAD")
}
