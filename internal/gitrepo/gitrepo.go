package gitrepo

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrNotFound = errors.New("gitrepo: revision not found")

type Repo struct{ dir string }

func Open(dir string) (*Repo, error) {
	if dir == "" {
		dir = "."
	}
	r := &Repo{dir: dir}
	if _, err := r.git("rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("gitrepo: %s is not a git repository: %w", dir, err)
	}
	return r, nil
}

// git runs one git command and returns its standard output. Standard error is
// carried into the error, because git says why on stderr and an error that
// dropped it would leave every caller guessing.
func (r *Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Repo) Head() (string, error) { return r.git("rev-parse", "HEAD") }

func (r *Repo) ResolveRef(ref string) (string, error) {
	out, err := r.git("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return out, nil
}

func (r *Repo) Parents(sha string) ([]string, error) {
	out, err := r.git("rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, sha)
	}
	return f[1:], nil
}

func (r *Repo) MergeBase(a, b string) (string, error) { return r.git("merge-base", a, b) }

// IsAncestor reports whether a is an ancestor of b. git says so with an exit
// status, where 1 means "no" and anything else means the question could not be
// answered — a distinction a caller must not lose.
func (r *Repo) IsAncestor(a, b string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", a, b)
	cmd.Dir = r.dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func (r *Repo) IsShallow() (bool, error) {
	out, err := r.git("rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	return out == "true", nil
}

// ObjectSHA reports the object at path in rev, and whether it is there.
// cat-file -e is the test rather than matching rev-parse's message, because
// git's messages are English prose and are not a contract.
func (r *Repo) ObjectSHA(rev, path string) (string, bool, error) {
	spec := rev + ":" + path
	cmd := exec.Command("git", "cat-file", "-e", spec)
	cmd.Dir = r.dir
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", false, nil
		}
		return "", false, err
	}
	sha, err := r.git("rev-parse", spec)
	if err != nil {
		return "", false, err
	}
	return sha, true, nil
}

// Materialise extracts rev's tree into dir. It uses git archive rather than
// checkout, which would move the user's HEAD, or worktree add, which would
// write into their .git. The archive is read with archive/tar rather than
// piped to the tar binary, which the released cross-platform builds cannot
// assume is present.
func (r *Repo) Materialise(rev, dir string) error {
	cmd := exec.Command("git", "archive", "--format=tar", rev)
	cmd.Dir = r.dir
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := untar(out, dir); err != nil {
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %s", rev, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func untar(rc io.Reader, dir string) error {
	tr := tar.NewReader(rc)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// A path from an archive is untrusted input even when git wrote it:
		// a name escaping the destination would write outside the temporary
		// directory this is extracted into.
		target := filepath.Join(dir, filepath.Clean("/"+h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}
