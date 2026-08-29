package breaking

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run is git, in dir, or a failed test. The config environment is pinned so a
// developer's global git configuration cannot change what these tests mean.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, name, body, msg string) string {
	t.Helper()
	write(t, dir, name, body)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", msg)
	return run(t, dir, "rev-parse", "HEAD")
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	// Choose resolves the base through gitrepo.BaseRef, which requires a
	// configured remote (it never trusts a stale local branch). The
	// fixture points a remote at itself so BaseRef's fetch has somewhere
	// to fetch refs/heads/<branch> from; the tests never push or pull for
	// real, so a self-referential file:// remote is sufficient.
	run(t, dir, "remote", "add", "origin", "file://"+dir)
	return dir
}

// run2 is git, in dir, returning its error rather than failing the test —
// needed for tests that assert a command fails.
func run2(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
