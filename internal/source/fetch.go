package source

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrUnreachableSHA reports that a commit named by a pinned SHA is no longer
// present on the remote. It is its own error because the recovery differs from
// every other fetch failure: the pin has to be moved, not retried.
var ErrUnreachableSHA = errors.New("commit is not reachable on the remote")

// fullSHA matches a complete, unabbreviated object name. Abbreviated names are
// not accepted as pins: they are ambiguous, and a lockfile entry must not be.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// FetchInto materialises the tree of url at ref inside the cache rooted at
// cacheRoot, and returns the directory holding it together with the commit SHA
// it resolved to.
//
// ref may be a branch, a tag or a full commit SHA. It is always resolved to a
// SHA first, because the SHA is the cache coordinate: an entry that exists is
// reused as it stands, without contacting the remote at all.
func FetchInto(ctx context.Context, cacheRoot, url, ref string) (string, string, error) {
	sha, err := ResolveRef(ctx, url, ref)
	if err != nil {
		return "", "", remoteProblem(url, ref, err)
	}
	final := CacheDir(cacheRoot, url, sha)
	if _, err := os.Stat(final); err == nil {
		// The cache is immutable: the SHA is part of the key, so an entry that
		// exists cannot be stale. Nothing is contacted.
		return final, sha, nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", "", err
	}
	// The tree is built in a scratch directory beside its destination and moved
	// into place with a single rename. A CI runner keeps several jobs on one
	// shared HOME, so several processes populate a cold entry at the same time;
	// building in place would let a concurrent reader see a half-written tree
	// and report a missing import once every N runs. The scratch directory is a
	// sibling so that the rename stays within one filesystem, which is what
	// makes it atomic.
	tmp, err := os.MkdirTemp(filepath.Dir(final), tempPrefix)
	if err != nil {
		return "", "", err
	}
	// Removing the scratch directory unconditionally covers every exit: an
	// error, a cancelled context, and the successful path where the rename has
	// already emptied it and this is a no-op.
	defer os.RemoveAll(tmp)

	if err := materialise(ctx, url, sha, tmp); err != nil {
		return "", "", remoteProblem(url, ref, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		// Losing the race is the expected outcome, not a failure: the entry is
		// immutable, so whatever the winner put there is what this call would
		// have written. Anything else is a real error.
		if _, statErr := os.Stat(final); statErr != nil {
			return "", "", fmt.Errorf("moving the fetched tree into %s: %w", final, err)
		}
	}
	return final, sha, nil
}

// tempPrefix names the scratch directories a fetch builds in. The leading dot
// keeps them out of the way of anything listing the cache, and the fixed prefix
// makes a leaked one identifiable.
const tempPrefix = ".tmp-"

// ResolveRef resolves a branch, tag or commit SHA to the full commit SHA it
// names. A full SHA is taken as given: resolving it would mean a network round
// trip that says nothing the caller does not already know, and the fetch itself
// is where an unreachable commit is discovered.
func ResolveRef(ctx context.Context, url, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("no ref given for %s; expected a branch, a tag or a commit SHA", url)
	}
	if fullSHA.MatchString(ref) {
		return ref, nil
	}
	// Two patterns, not one: ls-remote matches its pattern against the whole
	// ref name, so "v1.0.0" finds refs/tags/v1.0.0 but not the peeled
	// refs/tags/v1.0.0^{} line. Without the peeled line an annotated tag would
	// resolve to the tag object rather than to the commit it points at.
	out, err := git(ctx, "", "ls-remote", "--", url, ref, ref+"^{}")
	if err != nil {
		return "", err
	}
	sha, ok := pickRef(out)
	if !ok {
		return "", fmt.Errorf("%s: ref %q does not exist on the remote.\n"+
			"Check the ref in the manifest: a branch that was deleted after a merge, or a tag that was never "+
			"pushed, both read this way. A commit SHA is accepted here as well and does not move.", url, ref)
	}
	return sha, nil
}

// pickRef reads the output of git ls-remote and returns the commit it names.
//
// An annotated tag is listed twice: once as the tag object, and once peeled to
// the commit with a "^{}" suffix. The peeled line is the one that matters —
// what is fetched and checked out is a commit, never a tag object.
func pickRef(out string) (string, bool) {
	var first string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || !fullSHA.MatchString(sha) {
			continue
		}
		if strings.HasSuffix(name, "^{}") {
			return sha, true
		}
		if first == "" {
			first = sha
		}
	}
	return first, first != ""
}

// materialise lays the tree of one commit out in dir.
//
// The clone is blobless: the object graph is fetched without file contents, and
// the checkout that follows pulls in only the blobs of the commit actually
// wanted. For a repository with a long history this is the difference between
// transferring every version of every file and transferring one.
func materialise(ctx context.Context, url, sha, dir string) error {
	if _, err := git(ctx, dir, "init", "--quiet"); err != nil {
		return err
	}
	if _, err := git(ctx, dir, "remote", "add", "origin", url); err != nil {
		return err
	}
	// Asking for a bare SHA is the cheap path, but a server may refuse it:
	// fetching an object that no ref points at requires uploadpack.allowAnySHA1InWant,
	// which is off by default. When it is refused, fall back to fetching the
	// advertised refs and look for the commit among what arrived — a squashed
	// or force-pushed commit is genuinely gone, and only that second attempt can
	// tell the two cases apart.
	_, direct := git(ctx, dir, "fetch", "--quiet", "--depth=1", "--filter=blob:none", "origin", sha)
	if direct != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := git(ctx, dir, "fetch", "--quiet", "--filter=blob:none", "--tags", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return err
		}
		if _, err := git(ctx, dir, "cat-file", "-e", sha+"^{commit}"); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("%w: %s in %s. The branch has most likely moved on and the commit was rewritten, as a squash merge does; re-resolve the ref with --update", ErrUnreachableSHA, sha, url)
		}
	}
	if _, err := git(ctx, dir, "checkout", "--quiet", "--detach", sha); err != nil {
		return err
	}
	// The repository itself is not part of what the cache holds: consumers read
	// files, and leaving .git behind would both double the size of an entry and
	// invite something downstream to treat a cache entry as a working copy.
	return os.RemoveAll(filepath.Join(dir, ".git"))
}

// git runs one git command and returns its standard output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", &gitError{args: args, stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return string(out), nil
}

// gitError is a git invocation that failed, with what git said about it kept
// apart from the command that was run.
//
// It is a type rather than a formatted string because the caller decides how a
// failure reads: the same exit status 128 is an unreachable host, a refused
// credential or a repository that is not there, and those have three different
// recoveries. Classifying needs git's own words, so they are carried rather
// than folded into a sentence at the point they are produced.
type gitError struct {
	args   []string
	stderr string
	err    error
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.args, " "), e.err, e.stderr)
}

func (e *gitError) Unwrap() error { return e.err }

// remoteProblem turns a git failure into a message a reader can act on.
//
// A raw git error is not an answer. It names neither the dependency as the
// manifest wrote it nor the ref that was asked for, and it says nothing about
// what to do — and these messages are read in CI logs, by somebody who did not
// write the manifest. So the failure is classified from git's own words, the
// words are quoted rather than replaced, and the recovery is stated.
//
// Errors this package authored itself, a cancelled context and ErrUnreachableSHA
// pass through untouched: each already says what it means.
func remoteProblem(url, ref string, err error) error {
	var g *gitError
	if err == nil || !errors.As(err, &g) {
		return err
	}
	headline, advice := classify(g.stderr)
	return &remoteError{
		msg: fmt.Sprintf("%s at ref %q: %s\ngit said:\n%s\n%s\n"+
			"Nothing was cached, so this run changed nothing: the first use of a dependency at a commit "+
			"is the only step that reaches the remote at all.",
			url, ref, headline, indent(g.stderr), advice),
		err: err,
	}
}

// remoteError is a fetch failure as a reader sees it. The git error it came
// from is kept for errors.Is and errors.As, but is not printed a second time:
// git's words are already quoted in the message, and repeating them is how a
// diagnostic becomes something people scroll past.
type remoteError struct {
	msg string
	err error
}

func (e *remoteError) Error() string { return e.msg }

func (e *remoteError) Unwrap() error { return e.err }

// indent marks git's own words as a quotation, so that a multi-line
// diagnostic does not read as if this tool had written it.
func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, line := range lines {
		lines[i] = "  " + strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// classify reads git's diagnostics and decides which failure this is. The
// alternative — one message for every failure — is what makes a tool's errors
// worth skipping, and the three cases below have nothing in common but the
// exit status.
func classify(stderr string) (headline, advice string) {
	low := strings.ToLower(stderr)
	contains := func(needles ...string) bool {
		for _, n := range needles {
			if strings.Contains(low, n) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("authentication failed", "could not read username", "could not read password",
		"terminal prompts disabled", "permission denied (publickey", "invalid username or password",
		"403 forbidden", "401 unauthorized", "access denied", "authentication required"):
		return "the remote refused authentication",
			"Check the credentials this machine offers git for that host: in CI that is a token in the job's " +
				"git configuration or a credential helper, and on a workstation an ssh key or a stored login. " +
				"Prompting is off in a non-interactive run, so a missing credential fails here rather than waiting."
	case contains("not appear to be a git repository", "repository not found", "404 not found",
		"no such file or directory", "does not exist"):
		return "there is no repository at that address",
			"Check the git: address in the manifest against the one the repository is actually served at; " +
				"a private repository that is unreadable to this machine also answers this way."
	default:
		return "could not reach the remote",
			"Check that this machine can reach that host. The first use of a dependency at a commit is the " +
				"only step that needs the network; every later run answers from the cache."
	}
}
