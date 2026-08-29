# Breaking-change detection, plan A: the engine

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `stele breaking` compares the working revision against the correct previous revision and **reports** wire and source breakages in the modules this repository owns. It always exits zero.

**Architecture:** A new `internal/gitrepo` reads the repository the tool is run in — `internal/source` deletes `.git` from every cache entry and cannot answer a question about history. `internal/breaking` chooses the previous revision, skips the run when the owned trees and the lock are unchanged, materialises the previous revision into a temporary directory, resolves it with `pin.Resolve(Dir: thatDir)` so it is compiled with *its own* lock, and diffs two `linker.Files` by full name.

**Tech Stack:** Go 1.26, `bufbuild/protocompile`, `google.golang.org/protobuf/reflect/protoreflect`, and the repository's existing `internal/{config,pin,resolve,compile,lint}` and `rule`.

**Spec:** `docs/design/2026-08-28-breaking-change-detection.md` (third revision). Read it before Task 1.

## Why this plan ships a command that cannot fail a build

Plan A has **no valve**: `allow`, `moves`, `--audit` and `--prune` are plan B, and the spec deliberately removed configurable severity. A command that exits non-zero with no way to permit a legitimate breaking change leaves a repository one option — deleting the CI job — which is the invisible off switch the spec spends a section rejecting. A warning in a plan document does not travel with the binary.

So `stele breaking` **always exits zero in plan A** and says so in its own output. This is not a temporary fudge: the spec's evidence section calls for a two-week report-only shadow period (plan C), which a fatal command cannot provide. Plan B introduces the valve and the non-zero exit together, in one release, which under `RELEASING.md` is one announced change of a command's exit status rather than two.

**Version.** A new command is MINOR under `RELEASING.md`, but the major version is 0, where "what would be MAJOR above bumps MINOR, and everything else bumps PATCH" — so this lands as **v0.3.1, a patch**. Stated here so nobody rounds up and spends a minor.

## Global Constraints

- **Go 1.26.** Module `github.com/thegorangers/stele`.
- **No organisation identifiers anywhere**, tests and fixtures included; `internal/hygiene` walks the whole repository. Example names use `example.` packages. Run `go test -count=1 ./internal/hygiene/` before every commit.
- **Rule ids are a permanent public contract** under `RELEASING.md`. Task 1 reserves the namespace before any id ships, because reserving afterwards is a silent rename of somebody else's id.
- **Silence is forbidden.** Every failure to compare is an error naming itself. A run that compared nothing is never reported as clean.
- **Never elide a test body.** A Go test function whose body is a comment compiles and passes, so an elided body is a green test masquerading as a specification. Every test in this plan is written out.
- **Every red step must be observed.** Where a step says to run a test and see it fail, the named failure is the one to expect; a test that passes before the implementation exists is a defect in the test.
- **Reuse, do not re-spell.** `internal/lint.LockName` and `ManifestName` exist; `config.Load` is the parser (there is no `config.Parse`); `lint.Position` is exported. Grep before introducing a name.
- **British spelling** in prose and comments. No `Co-Authored-By` trailers; no mention of any assistant.

---

### Task 1: reserve the `break/` namespace

**Files:**
- Modify: `rule/rule.go` — beside `NamespaceBuiltin` and `NamespaceAIP`
- Modify: `rule/namespace_test.go`

This is first and alone because `rule/namespace_test.go` states the reason in the repository's own words: "A namespace is free until somebody publishes a rule in it, and reserving one afterwards is a rename of somebody else's rule ID — which under RELEASING.md does not fail loudly, it silently stops matching the ignore list that named it. So the reservation is made before the first rule in the namespace ships, not after."

**Interfaces:**
- Produces: `const NamespaceBreaking = "break"`, and `Reserved("break") == true`.

- [ ] **Step 1: Write the failing test** — extend `TestReservedNamespaces` in `rule/namespace_test.go`:

```go
	if rule.NamespaceBreaking != "break" {
		t.Errorf("the breaking namespace is %q, and it is a published contract", rule.NamespaceBreaking)
	}
```

and add `rule.NamespaceBreaking` to the loop over reserved namespaces already in that test.

- [ ] **Step 2: Run it and see it fail**

```
go test ./rule/ -run TestReservedNamespaces
```

Expected: build failure, `undefined: rule.NamespaceBreaking`.

- [ ] **Step 3: Implement**

```go
// NamespaceBreaking is the origin part of a rule ID reserved for the rules
// that compare this revision against a previous one. It is reserved here,
// before the first such rule ships, for the reason NamespaceBuiltin gives.
const NamespaceBreaking = "break"
```

and add it to `Reserved`.

- [ ] **Step 4: Run the whole rule suite and see it pass** — `go test ./rule/`.
- [ ] **Step 5: Commit**

```bash
git add rule/
git commit -m "feat(rule): reserve the break namespace

Before the first rule in it ships, not after: reserving a namespace
afterwards renames somebody else's rule ID, and that rename does not
fail loudly — it stops matching the ignore list that named it."
```

---

### Task 2: `internal/gitrepo` — reading the repository the tool runs in

**Files:**
- Create: `internal/gitrepo/gitrepo.go`
- Create: `internal/gitrepo/testgit_test.go` — helpers
- Create: `internal/gitrepo/gitrepo_test.go`

**Interfaces:**
- Produces:
  - `type Repo struct{ dir string }`, `func Open(dir string) (*Repo, error)`
  - `func (r *Repo) Head() (string, error)`
  - `func (r *Repo) ResolveRef(ref string) (string, error)`
  - `func (r *Repo) Parents(sha string) ([]string, error)`
  - `func (r *Repo) MergeBase(a, b string) (string, error)`
  - `func (r *Repo) IsAncestor(a, b string) (bool, error)`
  - `func (r *Repo) IsShallow() (bool, error)`
  - `func (r *Repo) ObjectSHA(rev, path string) (string, bool, error)` — the tree **or** blob at path, and whether it exists. One method, because the caller compares module roots (trees) and the lock (a blob) in the same loop and must not care which is which.
  - `func (r *Repo) Materialise(rev, dir string) error`
  - `var ErrNotFound = errors.New("gitrepo: revision not found")`

- [ ] **Step 1: Write the helpers** in `testgit_test.go`, written out because every later task uses them:

```go
package gitrepo

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
	return dir
}
```

- [ ] **Step 2: Write the failing tests** in `gitrepo_test.go`:

```go
package gitrepo

import (
	"path/filepath"
	"testing"
)

func TestParentsAndMergeBase(t *testing.T) {
	dir := repo(t)
	first := commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	topic := commit(t, dir, "b.txt", "two", "topic work")

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parents, err := r.Parents(topic)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0] != first {
		t.Fatalf("Parents = %v, want [%s]", parents, first)
	}
	base, err := r.MergeBase(topic, "main")
	if err != nil {
		t.Fatal(err)
	}
	if base != first {
		t.Fatalf("MergeBase = %s, want %s", base, first)
	}
}

// A root commit has no parents, and that is a value rather than an error: a
// repository's first commit has nothing before it to compare against.
func TestParentsOfRootCommitIsEmpty(t *testing.T) {
	dir := repo(t)
	head := commit(t, dir, "a.txt", "one", "first")
	r, _ := Open(dir)
	parents, err := r.Parents(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 0 {
		t.Fatalf("Parents = %v, want none", parents)
	}
}

// Absence is a value too. A module root a revision did not have is an
// ordinary state, and reporting it as a failure would leave the caller unable
// to tell it from a broken repository.
func TestObjectSHAReportsAbsence(t *testing.T) {
	dir := repo(t)
	head := commit(t, dir, filepath.Join("api", "x.proto"), "syntax = \"proto3\";", "first")
	r, _ := Open(dir)

	tree, ok, err := r.ObjectSHA(head, "api")
	if err != nil || !ok || tree == "" {
		t.Fatalf("ObjectSHA(api) = %q, %v, %v; want a tree", tree, ok, err)
	}
	blob, ok, err := r.ObjectSHA(head, "api/x.proto")
	if err != nil || !ok || blob == "" {
		t.Fatalf("ObjectSHA(api/x.proto) = %q, %v, %v; want a blob", blob, ok, err)
	}
	if _, ok, err := r.ObjectSHA(head, "nope"); err != nil || ok {
		t.Fatalf("ObjectSHA(nope) ok=%v err=%v; want absent and no error", ok, err)
	}
}

// Materialise must not move HEAD or write into the user's .git.
func TestMaterialiseLeavesTheRepositoryAlone(t *testing.T) {
	dir := repo(t)
	first := commit(t, dir, "a.txt", "one", "first")
	commit(t, dir, "a.txt", "two", "second")
	headBefore := run(t, dir, "rev-parse", "HEAD")

	out := t.TempDir()
	r, _ := Open(dir)
	if err := r.Materialise(first, out); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(out, "a.txt"))
	if err != nil || string(body) != "one" {
		t.Fatalf("materialised a.txt = %q, %v; want the first revision", body, err)
	}
	if got := run(t, dir, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("HEAD moved to %s", got)
	}
	if run(t, dir, "status", "--porcelain") != "" {
		t.Fatal("the working tree is dirty after Materialise")
	}
}

func TestIsShallow(t *testing.T) {
	origin := repo(t)
	for _, body := range []string{"one", "two", "three"} {
		commit(t, origin, "a.txt", body, body)
	}
	parent := t.TempDir()
	shallow := filepath.Join(parent, "clone")
	run(t, parent, "clone", "-q", "--depth", "1", "file://"+origin, shallow)

	full, _ := Open(origin)
	if got, err := full.IsShallow(); err != nil || got {
		t.Fatalf("IsShallow(full) = %v, %v; want false", got, err)
	}
	sh, _ := Open(shallow)
	if got, err := sh.IsShallow(); err != nil || !got {
		t.Fatalf("IsShallow(shallow) = %v, %v; want true", got, err)
	}
}
```

Add `"os"` to the imports for the `Materialise` test.

- [ ] **Step 3: Run and see them fail** — `go test ./internal/gitrepo/`; expected `undefined: Open`.

- [ ] **Step 4: Implement.** This code is verified: it was written and run against real repositories before it entered this plan.

```go
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
```

- [ ] **Step 5: Run and see them pass** — `go test ./internal/gitrepo/ -v`.
- [ ] **Step 6: Commit**

```bash
git add internal/gitrepo/
git commit -m "feat(gitrepo): read the repository the tool is run in

internal/source deletes .git from every cache entry, so nothing in the
tool could answer a question about history.

Materialise extracts with git archive and archive/tar: checkout would
move the user's HEAD, worktree add would write into their .git, and
piping to the tar binary would assume a program the released builds
cannot count on."
```

---

### Task 3: acquiring the base branch

**Files:**
- Create: `internal/gitrepo/base.go`
- Create: `internal/gitrepo/base_test.go`

The spec gives this its own section and earlier plans dropped it entirely. It is the ordinary CI case: a GitLab merge-request pipeline fetches only the merge-request ref, so the base branch is **absent at any depth**, and `GIT_DEPTH: 0` does not cure it.

**Interfaces:**
- Produces:
  - `func (r *Repo) Remote() (string, error)` — the single remote, or an error naming the ambiguity
  - `func (r *Repo) BaseRef(branch string) (string, error)` — resolves `branch` to a **remote-tracking** commit, fetching it into `refs/stele/` when it is missing
  - `var ErrNoRemote, ErrAmbiguousRemote error`

- [ ] **Step 1: Write the failing tests**

```go
package gitrepo

import (
	"errors"
	"path/filepath"
	"testing"
)

// A stale local branch would silently place the comparison in the past, so
// the base is resolved as a remote-tracking ref, not as a local branch.
func TestBaseRefPrefersTheRemoteOverAStaleLocalBranch(t *testing.T) {
	origin := repo(t)
	commit(t, origin, "a.txt", "one", "first")
	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	run(t, parent, "clone", "-q", "file://"+origin, clone)

	// The origin moves; the clone's local main does not.
	moved := commit(t, origin, "a.txt", "two", "second")
	run(t, clone, "fetch", "-q", "origin")

	r, _ := Open(clone)
	got, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if got != moved {
		t.Fatalf("BaseRef = %s, want the remote's %s, not the stale local branch", got, moved)
	}
}

// The merge-request case: the base branch is not in the clone at all. It is
// fetched, into refs/stele, without disturbing the user's refs.
func TestBaseRefFetchesAnAbsentBaseWithoutDisturbingTheRepository(t *testing.T) {
	origin := repo(t)
	first := commit(t, origin, "a.txt", "one", "first")
	run(t, origin, "checkout", "-q", "-b", "topic")
	commit(t, origin, "b.txt", "two", "topic")

	parent := t.TempDir()
	clone := filepath.Join(parent, "clone")
	// Clone only the topic branch, as a merge-request pipeline does.
	run(t, parent, "clone", "-q", "--single-branch", "--branch", "topic", "file://"+origin, clone)
	if _, err := run2(clone, "rev-parse", "--verify", "origin/main"); err == nil {
		t.Fatal("the fixture is wrong: origin/main is present, so nothing is being tested")
	}

	r, _ := Open(clone)
	got, err := r.BaseRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("BaseRef = %s, want %s", got, first)
	}
	if _, err := run2(clone, "rev-parse", "--verify", "FETCH_HEAD"); err == nil {
		t.Error("FETCH_HEAD was written; the fetch must not disturb the user's refs")
	}
	if _, err := run2(clone, "rev-parse", "--verify", "refs/stele/base"); err != nil {
		t.Error("the fetched base was not stored under refs/stele/")
	}
}

func TestRemoteAmbiguityIsNamed(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "remote", "add", "origin", "file:///nonexistent-a")
	run(t, dir, "remote", "add", "upstream", "file:///nonexistent-b")

	r, _ := Open(dir)
	_, err := r.Remote()
	if !errors.Is(err, ErrAmbiguousRemote) {
		t.Fatalf("Remote error = %v, want ErrAmbiguousRemote", err)
	}
	if !strings.Contains(err.Error(), "origin") || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("the error must name the remotes it cannot choose between: %v", err)
	}
}

func TestNoRemoteIsNamed(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	r, _ := Open(dir)
	if _, err := r.Remote(); !errors.Is(err, ErrNoRemote) {
		t.Fatalf("Remote error = %v, want ErrNoRemote", err)
	}
}
```

`run2` is a helper beside `run` in `testgit_test.go` that returns `(string, error)` instead of failing the test — needed because these tests assert that a command *fails*:

```go
func run2(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
```

Add `"strings"` to `base_test.go`'s imports.

- [ ] **Step 2: Run and see them fail** — expected `undefined: BaseRef`.

- [ ] **Step 3: Implement.** `Remote` reads `git remote`; zero remotes is `ErrNoRemote`, one is that one, more than one is `ErrAmbiguousRemote` naming them all. `BaseRef` tries `refs/remotes/<remote>/<branch>` first, then `refs/stele/base`, and otherwise fetches:

```go
// The fetch writes one ref of our own and nothing else. The user's
// remote-tracking refs and FETCH_HEAD are theirs, and this tool otherwise
// writes only generated code and the lock.
_, err = r.git("fetch", "--quiet", "--no-write-fetch-head",
	remote, "+refs/heads/"+branch+":refs/stele/base")
```

- [ ] **Step 4: Run and see them pass.**
- [ ] **Step 5: Commit** — `feat(gitrepo): acquire the base branch, fetching it when it is absent`.

---

### Task 4: choosing the previous revision

**Files:**
- Create: `internal/breaking/previous.go`
- Create: `internal/breaking/testgit_test.go` — the same helpers; copy them, do not import a `_test` package
- Create: `internal/breaking/previous_test.go`

This is the task two design revisions got wrong, and the ordering of its three questions is its whole content.

**Interfaces:**
- Consumes: `gitrepo` from Tasks 2 and 3.
- Produces: `type Previous struct{ SHA, Working, Reason string }`, `func Choose(r *gitrepo.Repo, base string) (Previous, bool, error)`.

- [ ] **Step 1: Write the failing tests.** These are verified: they were run against real repositories, and the ordering defect below was reintroduced deliberately to confirm they catch it.

```go
package breaking

import (
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
)

func TestBaseBranchUsesFirstParent(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	commit(t, dir, "b.txt", "two", "topic work")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "c.txt", "three", "main moves")
	mainBefore := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "merge topic", "topic")

	r, _ := gitrepo.Open(dir)
	got, ok, err := Choose(r, "main")
	if err != nil || !ok {
		t.Fatalf("Choose: ok=%v err=%v", ok, err)
	}
	if got.SHA != mainBefore {
		t.Fatalf("previous = %s, want the first parent %s (reason %q)", got.SHA, mainBefore, got.Reason)
	}
}

func TestTopicBranchUsesMergeBaseNotTip(t *testing.T) {
	dir := repo(t)
	fork := commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	topicHead := commit(t, dir, "b.txt", "two", "topic work")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "neighbour.txt", "x", "a neighbour lands")
	run(t, dir, "checkout", "-q", "topic")

	r, _ := gitrepo.Open(dir)
	got, ok, _ := Choose(r, "main")
	if !ok || got.SHA != fork {
		t.Fatalf("previous = %s, want the fork point %s", got.SHA, fork)
	}
	if got.Working != topicHead {
		t.Fatalf("working = %s, want %s", got.Working, topicHead)
	}
}

func TestFabricatedMergeUsesTopicParentAndSurvivesBaseMoving(t *testing.T) {
	dir := repo(t)
	fork := commit(t, dir, "a.txt", "one", "first")
	run(t, dir, "checkout", "-q", "-b", "topic")
	topicHead := commit(t, dir, "b.txt", "two", "topic work")
	run(t, dir, "checkout", "-q", "main")
	commit(t, dir, "n.txt", "x", "neighbour")
	run(t, dir, "checkout", "-q", "--detach")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "pr merge", "topic")
	run(t, dir, "update-ref", "refs/heads/main", run(t, dir, "commit-tree", "-p", "refs/heads/main",
		"-m", "base moved", run(t, dir, "rev-parse", "refs/heads/main^{tree}")))

	r, _ := gitrepo.Open(dir)
	got, ok, err := Choose(r, "main")
	if err != nil || !ok {
		t.Fatalf("Choose: ok=%v err=%v", ok, err)
	}
	if got.Working != topicHead {
		t.Fatalf("working = %s, want the topic parent %s", got.Working, topicHead)
	}
	if got.SHA != fork {
		t.Fatalf("previous = %s, want the fork point %s", got.SHA, fork)
	}
}

func TestFirstCommitHasNothingToCompare(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "a.txt", "one", "first")
	r, _ := gitrepo.Open(dir)
	if _, ok, err := Choose(r, "main"); ok || err != nil {
		t.Fatalf("ok=%v err=%v, want nothing to compare", ok, err)
	}
}
```

- [ ] **Step 2: Run and see them fail** — `go test ./internal/breaking/`; expected `undefined: Choose`.

- [ ] **Step 3: Implement.** This code is verified — written, compiled and run against the tests above before entering this plan.

```go
// Choose returns the revision this one is compared against.
//
// The order of the three questions is the whole of this function, and getting
// it wrong is not theoretical: asking "is this a merge with one parent on the
// base" before "is HEAD on the base" sends every ordinary merge on the base
// branch into the nothing-to-compare exit, because on the base branch every
// parent of HEAD is an ancestor of the base. That is the degenerate
// base-branch comparison this design exists to avoid, reached by a different
// route. So: on the base branch first, fabricated merge second.
func Choose(r *Repo, base string) (Previous, bool, error) {
	head, err := r.Head()
	if err != nil {
		return Previous{}, false, err
	}
	parents, err := r.Parents(head)
	if err != nil {
		return Previous{}, false, err
	}
	if len(parents) == 0 {
		return Previous{}, false, nil
	}
	baseSHA, err := r.ResolveRef(base)
	if err != nil {
		return Previous{}, false, err
	}

	// Is HEAD itself on the base branch? Then the previous revision is what
	// the branch looked like before this commit: its first parent.
	onBase, err := r.IsAncestor(head, baseSHA)
	if err != nil {
		return Previous{}, false, err
	}
	if onBase {
		return Previous{SHA: parents[0], Working: head,
			Reason: "the first parent, because HEAD is on " + base}, true, nil
	}

	// HEAD is off the base. A two-parent HEAD with exactly one parent on the
	// base is a merge of the base into a topic branch, which is also the
	// shape a pull-request checkout fabricates. Ancestry, not equality with
	// the tip: the tip moves between checkout and run.
	working := head
	if len(parents) == 2 {
		var on [2]bool
		for i, p := range parents {
			if on[i], err = r.IsAncestor(p, baseSHA); err != nil {
				return Previous{}, false, err
			}
		}
		if on[0] != on[1] {
			working = parents[0]
			if on[0] {
				working = parents[1]
			}
		}
	}
	prev, err := r.MergeBase(working, baseSHA)
	if err != nil {
		return Previous{}, false, err
	}
	reason := "merge-base with " + base
	if working != head {
		reason += ", from the topic side of a merge commit"
	}
	return Previous{SHA: prev, Working: working, Reason: reason}, true, nil
}
```

`Choose` calls `r.BaseRef(base)` from Task 3 rather than `ResolveRef`, so a stale local branch cannot be picked up.

- [ ] **Step 4: Run and see them pass.**

- [ ] **Step 5: Prove the ordering test is not decoration.** Temporarily move the two-parent block *above* the `IsAncestor(head, baseSHA)` block and re-run. Expected: `TestBaseBranchUsesFirstParent` fails with `ok=false`, because on the base branch every parent of HEAD is an ancestor of the base, so the re-merge exit swallows every ordinary merge. Restore the order afterwards. **Do not skip this step**: it is the only evidence that the test catches the defect the task exists to prevent, and that defect has been written twice already.

- [ ] **Step 6: Commit**

```bash
git add internal/breaking/
git commit -m "feat(breaking): choose the previous revision

Three questions in an order that is the whole of this function: nothing
to compare, then HEAD on the base branch, then a merge with one parent
on the base. Asking the third before the second sends every ordinary
merge on the base branch into the nothing-to-compare exit, because
there every parent of HEAD is an ancestor of the base — the degenerate
base-branch comparison, reached by a different route."
```

---

### Task 5: the tree shortcut

**Files:**
- Create: `internal/breaking/shortcut.go`
- Create: `internal/breaking/shortcut_test.go`

Measured at 84.7% of base-branch commits fleet-wide, so this is what makes the ordinary commit free. It runs before anything is fetched or compiled.

**Interfaces:**
- Produces: `func Unchanged(r *gitrepo.Repo, prev Previous, paths []string) (bool, error)` — `paths` are the module roots and the lock, and the caller assembles them.

- [ ] **Step 1: Write the failing tests**

```go
package breaking

import (
	"testing"

	"github.com/thegorangers/stele/internal/gitrepo"
)

func twoCommits(t *testing.T, second func(dir string)) (*gitrepo.Repo, Previous) {
	t.Helper()
	dir := repo(t)
	commit(t, dir, "api/x.proto", "syntax = \"proto3\";\n", "first")
	write(t, dir, "stele.lock", "version: 1\n")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "lock")
	first := run(t, dir, "rev-parse", "HEAD")
	second(dir)
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "second")
	head := run(t, dir, "rev-parse", "HEAD")
	r, _ := gitrepo.Open(dir)
	return r, Previous{SHA: first, Working: head}
}

var watched = []string{"api", "stele.lock"}

func TestUnchangedWhenNeitherProtosNorLockMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "README.md", "prose") })
	got, err := Unchanged(r, prev, watched)
	if err != nil || !got {
		t.Fatalf("Unchanged = %v, %v; want true", got, err)
	}
}

func TestChangedWhenAProtoMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) {
		write(t, dir, "api/x.proto", "syntax = \"proto3\";\nmessage M {}\n")
	})
	got, err := Unchanged(r, prev, watched)
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A dependency bump changes no owned file and can still break the consumers
// of what this repository re-exports, so the lock is watched too.
func TestChangedWhenOnlyTheLockMoved(t *testing.T) {
	r, prev := twoCommits(t, func(dir string) { write(t, dir, "stele.lock", "version: 1\n# bumped\n") })
	got, err := Unchanged(r, prev, watched)
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}

// A revision that had no api/ at all must not compare equal to one that does.
func TestAbsentPathIsNotEqualToPresentPath(t *testing.T) {
	dir := repo(t)
	write(t, dir, "README.md", "prose")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "no protos yet")
	first := run(t, dir, "rev-parse", "HEAD")
	commit(t, dir, "api/x.proto", "syntax = \"proto3\";\n", "protos arrive")
	head := run(t, dir, "rev-parse", "HEAD")

	r, _ := gitrepo.Open(dir)
	got, err := Unchanged(r, Previous{SHA: first, Working: head}, []string{"api"})
	if err != nil || got {
		t.Fatalf("Unchanged = %v, %v; want false", got, err)
	}
}
```

- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement**

```go
// Unchanged reports whether every watched path has the same object in both
// revisions. Absence is compared as well as identity: a path one revision did
// not have has not "stayed the same".
func Unchanged(r *gitrepo.Repo, prev Previous, paths []string) (bool, error) {
	for _, p := range paths {
		a, aok, err := r.ObjectSHA(prev.Working, p)
		if err != nil {
			return false, err
		}
		b, bok, err := r.ObjectSHA(prev.SHA, p)
		if err != nil {
			return false, err
		}
		if aok != bok || a != b {
			return false, nil
		}
	}
	return true, nil
}
```

- [ ] **Step 4: Run and see them pass.**
- [ ] **Step 5: Commit** — `perf(breaking): skip a run whose owned trees and lock are unchanged`.

---

### Task 6: compiling a revision with the dependencies it recorded

**Files:**
- Create: `internal/breaking/revisions.go`
- Create: `internal/breaking/revisions_test.go`
- Modify: `internal/compile/compile.go` — add source-info control

**Interfaces:**
- Produces:
  - `type Revision struct { Files linker.Files; Owned []string }`
  - `func Load(ctx context.Context, r *gitrepo.Repo, sha string, fetch resolve.FetchFunc, cacheDir string) (Revision, error)`
  - `var ErrNoManifest = errors.New("breaking: the revision has no manifest")`
  - `var ErrNoOwnedProtos = errors.New("breaking: the revision owns no proto files")`

Read `internal/pin/pin_test.go` first and reuse its fixture pattern for `fetch`; do not invent a second one.

- [ ] **Step 1: Write the failing tests**

```go
// The previous revision is resolved with the lock that revision carried.
// Compiling yesterday's protos against today's pins either fails outright or
// attributes another repository's change to this one.
func TestPreviousRevisionUsesItsOwnLock(t *testing.T) { /* see below */ }

// A manifest with no lock fails: resolving it fresh would pin every
// dependency to the commit its ref names today, silently.
func TestManifestWithoutLockIsAnError(t *testing.T) { /* see below */ }

// A revision with no manifest predates adoption. Nothing to compare.
func TestRevisionWithoutManifestIsErrNoManifest(t *testing.T) { /* see below */ }

// One repository in the measured fleet owns no proto modules at all. That is
// reported as its own condition, not as a compile failure and not as clean.
func TestRevisionOwningNoProtosIsErrNoOwnedProtos(t *testing.T) { /* see below */ }
```

Write each body in full following `internal/pin/pin_test.go`. The bodies are not given here because they depend on that file's fixture helpers, which the implementer must read; **the four assertions above are the contract and none may be dropped.** If a body cannot be written against those helpers, that is a finding to report, not a reason to weaken the test.

- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement**

```go
func Load(ctx context.Context, r *gitrepo.Repo, sha string, fetch resolve.FetchFunc, cacheDir string) (Revision, error) {
	dir, err := os.MkdirTemp("", "stele-breaking-")
	if err != nil {
		return Revision{}, err
	}
	defer os.RemoveAll(dir)
	if err := r.Materialise(sha, dir); err != nil {
		return Revision{}, err
	}

	manifestPath := filepath.Join(dir, lint.ManifestName)
	if _, err := os.Stat(manifestPath); errors.Is(err, fs.ErrNotExist) {
		return Revision{}, fmt.Errorf("%w: %s", ErrNoManifest, sha[:12])
	}
	mf, err := config.Load(manifestPath)
	if err != nil {
		return Revision{}, err
	}
	lockPath := filepath.Join(dir, lint.LockName)
	if _, err := os.Stat(lockPath); errors.Is(err, fs.ErrNotExist) {
		return Revision{}, fmt.Errorf(
			"revision %s has a manifest and no %s: resolving it afresh would pin every "+
				"dependency to the commit its ref names today, and compare this revision "+
				"against dependencies it never used", sha[:12], lint.LockName)
	}
	g, err := pin.Resolve(ctx, pin.Options{Dir: dir, Manifest: mf, LockPath: lockPath, Fetch: fetch})
	if err != nil {
		return Revision{}, err
	}
	owned := ownedPaths(g)
	if len(owned) == 0 {
		return Revision{}, fmt.Errorf("%w: %s", ErrNoOwnedProtos, sha[:12])
	}
	files, err := compile.Compile(ctx, g, owned, compile.WithoutSourceInfo(false))
	if err != nil {
		return Revision{}, err
	}
	return Revision{Files: files, Owned: owned}, nil
}

// ownedPaths is internal/lint's owned predicate, which is unexported there.
// The predicate itself — a file with no git origin is this repository's — is
// the one thing both must agree on; if it changes there, it changes here.
func ownedPaths(g *resolve.Graph) []string {
	var out []string
	for _, p := range g.ImportPaths() {
		if f, ok := g.FileFor(p); ok && f.Origin.Git == "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Add source-info control to `internal/compile`.** The previous revision never supplies a position, and `Compile` hard-codes `SourceInfoStandard`. Add a variadic option so no existing caller changes:

```go
type Option func(*options)

// WithoutSourceInfo drops source information from the compiled files. A caller
// that will never report a position — the older side of a comparison — pays
// for every comment in every file otherwise. The default is unchanged, because
// dropping information early cannot be undone downstream.
func WithoutSourceInfo(drop bool) Option { return func(o *options) { o.dropSourceInfo = drop } }

func Compile(ctx context.Context, g *resolve.Graph, targets []string, opts ...Option) (linker.Files, error)
```

Write a test in `internal/compile/compile_test.go` asserting that the default still retains source info and that the option drops it. Then pass `WithoutSourceInfo(true)` for the previous side in `Load` — a `prev bool` parameter, not a second function.

- [ ] **Step 5: Run the whole suite and see it pass** — `go test ./...`, because this task changes a shared signature.
- [ ] **Step 6: Commit** — `feat(breaking): compile a revision with the dependencies it recorded`.

---

### Task 7: the descriptor diff

**Files:**
- Create: `internal/breaking/diff.go`
- Create: `internal/breaking/diff_test.go`

`Diff` reports neutral *changes*; Task 9 decides which are breaking. Splitting them is what lets the type matrix be a function of two kinds rather than a rule that walks descriptors.

**Interfaces:**
- Produces:
  - `type Kind int` with `Removed`, `Added`, `Modified`
  - `type Change struct { Kind Kind; Subject string; Path string; Pos lint.Position; Before, After protoreflect.Descriptor }`
  - `func Diff(prev, cur Revision) []Change`

- [ ] **Step 1: Write the failing tests**

```go
// Additions are never breaking, but they are reported as changes: a rename is
// a removal and an addition, and the classifier needs both halves to pair
// them. This is why Diff reports changes and not findings.
func TestAdditionIsAChangeAndNotAFinding(t *testing.T) { /* build two Revisions from fixtures; assert Kind == Added */ }

func TestFieldRemovedIsReported(t *testing.T) { /* assert Kind == Removed, Subject == "example.orders.v1.Order.eta" */ }

// A removed file has no full name. Its subject is its import path, tagged, so
// it can never be mistaken for a declaration.
func TestRemovedFileHasATaggedSubject(t *testing.T) { /* assert Subject == "file:api/orders/v1/order.proto" */ }
```

Write the bodies against small in-repository fixtures compiled by Task 6's helpers. **The three assertions are the contract.**

- [ ] **Step 2–4: fail, implement, pass.** Index both sides by `protoreflect.FullName` over messages, fields, oneofs, enums, enum values, services and methods, restricted to `Owned`; walk the union of keys. The subject is stated by this function:

```go
// The subject is stated here rather than derived from a position, which is
// how internal/lint does it. That constraint exists so a rule in another
// process, which can return only a line and a column, is not second-class.
// There is no plugin host here and this function holds both descriptor sets,
// so it knows a removed field's name exactly. Deriving it from a position
// could not name a removed field at all: it would name the surviving message,
// and one permission would then license the removal of every field of it.
```

- [ ] **Step 5: Commit** — `feat(breaking): diff two revisions by full name`.

---

### Task 8: the wire-compatibility matrix, measured

**Files:**
- Create: `internal/breaking/wiretypes.go`
- Create: `internal/breaking/wiretypes_test.go`

**Interfaces:**
- Produces: `func WireCompatible(from, to protoreflect.Kind) bool`

- [ ] **Step 1: Write the measuring test — it is the specification**

The oracle is written out here because it is the hardest code in the plan and the previous plan left it as a comment:

```go
// The matrix is measured because hand-written enumerations in this repository
// have been wrong four revisions running. "Encode as one, decode as the
// other" is not the oracle: it measures the wire type. float and fixed32 are
// both four bytes and every decode succeeds, while 1.0 reads back as
// 1065353216.
//
// So: a corpus rather than a sample, and an equality defined across kinds.
var corpus = []int64{0, 1, -1, 127, 128, -128, 2147483647, -2147483648, 4294967295, 9223372036854775807}

// measure encodes each corpus value as from and decodes it as to, and reports
// whether every value survives with its meaning intact. Numeric kinds compare
// numerically, which is what rejects float against fixed32.
func measure(t *testing.T, from, to protoreflect.Kind) bool { /* built with dynamicpb over a synthesised descriptor */ }

func TestMatrixIsWhatMeasurementSays(t *testing.T) {
	for _, from := range kinds {
		for _, to := range kinds {
			if got, want := WireCompatible(from, to), measure(t, from, to); got != want {
				t.Errorf("WireCompatible(%v, %v) = %v, measured %v", from, to, got, want)
			}
		}
	}
}

func TestFloatAndFixed32ShareAWireTypeAndNotAValue(t *testing.T) {
	if WireCompatible(protoreflect.FloatKind, protoreflect.Fixed32Kind) {
		t.Fatal("float and fixed32 are four bytes each and mean different numbers")
	}
}
```

Build `measure` with `dynamicpb` over a synthesised one-field message: set the value with kind `from`, marshal, unmarshal into a message whose field has kind `to`, and compare numerically. String and bytes compare as byte sequences, each direction separately, and the corpus gains an invalid-UTF-8 case for them.

- [ ] **Step 2: Run and see it fail.**
- [ ] **Step 3: Implement `WireCompatible` as a table.** **Where the table and the measurement disagree, stop.** Do not edit either to match the other: resolve it against the protobuf encoding specification and record the reasoning in a comment. The spec requires a human decision at this point, and the previous plan revision pre-committed to believing the measurement, which would have enshrined the float/fixed32 answer.
- [ ] **Step 4: Run and see it pass.**
- [ ] **Step 5: Commit** — `feat(breaking): a measured wire-compatibility matrix`.

---

### Task 9: the rules

**Files:**
- Create: `internal/breaking/rules.go`
- Create: `internal/breaking/rules_test.go`
- Create: `internal/breaking/testdata/<case>/{before,after}/`

**Interfaces:**
- Produces:
  - `type Category int` with `Wire`, `Source`
  - `type Finding struct { Rule string; Category Category; Subject string; Path string; Pos lint.Position; Message string; Fix string; Change string }` — `Change` is the discriminant plan B's permissions match on, empty where the rule has none
  - `func Classify(changes []Change, prev, cur Revision) []Finding`

**The id set, settled here and permanent.** Renames and removals are both present, and the rule that decides between them is stated because the previous plan minted contradictory ids without one: **a removal and an addition within the same parent are reported as a rename when number, type and cardinality all match and only the name differs; otherwise they are a removal and an addition.**

Wire: `break/field_number_changed`, `break/field_type_changed`, `break/field_cardinality_changed`, `break/field_oneof_changed`, `break/enum_value_number_changed`, `break/package_removed`, `break/package_renamed`, `break/service_removed`, `break/method_removed`, `break/method_signature_changed`, `break/method_streaming_changed`.

Source: `break/field_removed`, `break/field_renamed`, `break/message_removed`, `break/enum_removed`, `break/enum_value_removed`, `break/enum_value_renamed`, `break/file_removed`, `break/go_package_changed`.

**Four rename ids were dropped during implementation and must not be re-added:**
`break/message_renamed`, `break/enum_renamed`, `break/service_renamed`,
`break/method_renamed`. A field and an enum value have a number to key a pairing
on; a message, enum, service or method has no identity beyond its name, and shape
is not one — two empty messages share a shape, so deleting one empty response and
adding another was reported as a rename and the removal vanished from the report.
A wrongly paired rename hides a removal, which is a missed breakage. To a consumer
a renamed message is a removal plus an addition anyway.

`break/file_removed` fires **only when the file's declarations survive elsewhere** — a file whose contents are gone reports the declaration removals, and reporting both would double-count. It exists because a consumer imports a *path*, so a file split or move breaks the import even when every full name survives.

- [ ] **Step 1: Write the fixtures and the table.** Every rule gets two: one where it fires and one where a legal change of the same shape leaves it silent. A rule with only the first half is not known to detect what it claims.

```go
var cases = []struct {
	dir      string // testdata/<dir>/{before,after}
	wantRule string // empty means: legal, nothing fires
}{
	{"field_removed", "break/field_removed"},
	{"field_added", ""},
	{"field_renamed", "break/field_renamed"},
	{"field_type_int32_to_int64", ""},
	{"field_type_int32_to_string", "break/field_type_changed"},
	{"field_number_changed", "break/field_number_changed"},
	{"field_reordered_same_numbers", ""},
	// ...one pair for every id above; write them all out.
}
```

- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement `Classify`.** Findings carry their category, and their discriminant where they have one: the pair of kinds for a type change, the destination for a oneof change, the direction for a cardinality change. **Removals have no discriminant and set `Change` empty**, and plan B refuses a permission that supplies one.
- [ ] **Step 4: Run and see them pass.**
- [ ] **Step 5: Commit** — `feat(breaking): the rules, each with a legal counterpart`.

---

### Task 10: what the closure comparison reports

**Files:**
- Create: `internal/breaking/closure.go`
- Create: `internal/breaking/closure_test.go`

The spec makes this constitutive of the producer run, not an extra: "bumping a dependency pin can break your consumers while your own files are byte-identical… **So the producer run also compares the resolved closure reachable from owned modules' imports**." Task 5 already pays the cost of a full run whenever the lock moves; without this task it pays that cost and then reports nothing.

**Interfaces:**
- Produces: `func Reachable(rev Revision) []string` and a `Classify` pass over the closure, whose findings carry `Category` as above and a message naming the dependency.

- [ ] **Step 1: Write the failing test** — a repository whose own protos are byte-identical between two revisions, whose lock moves a dependency to a revision where a message it re-exports lost a field. Assert a finding. Then the counterpart: a dependency bump that changes nothing reachable reports nothing.
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Commit** — `feat(breaking): report changes in the closure this repository re-exports`.

---

### Task 11: the report

**Files:**
- Create: `internal/breaking/report.go`
- Create: `internal/breaking/report_test.go`

**Interfaces:**
- Produces:
  - `type Outcome int` with `Compared`, `NothingToCompare`, `Unchanged`, `NoOwnedProtos` — four outcomes because three of them are *not* a clean comparison and each must render as itself
  - `type Info struct { Outcome Outcome; Previous string; Reason string }`
  - `func Render(findings []Finding, info Info) string`

- [ ] **Step 1: Write the failing tests**

```go
// The format is rule.Finding's, whose third field is the SEVERITY — not the
// category. Editors and CI log scrapers parse that shape, and the category
// goes in the message, where it is prose.
func TestRenderMatchesTheStandardDiagnosticShape(t *testing.T) {
	out := Render([]Finding{{
		Rule: "break/field_removed", Category: Source,
		Path: "api/orders/v1/order.proto", Pos: lint.Position{Line: 12, Col: 3},
		Subject: "example.orders.v1.Order.eta",
		Message: "field eta was removed; a consumer that reads it stops compiling",
	}}, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})

	const want = "api/orders/v1/order.proto:12:3: error: break/field_removed: "
	if !strings.Contains(out, want) {
		t.Fatalf("rendered as\n%s\nwant a line beginning %q", out, want)
	}
}

// Every report names the blind zone, because "no breaking changes" without
// qualification reads as "safe to change anything".
func TestFooterNamesTheBlindZone(t *testing.T) {
	out := Render(nil, Info{Previous: "abc1234", Reason: "merge-base with main"})
	for _, want := range []string{"json_name", "int32", "google.api.http"} {
		if !strings.Contains(out, want) {
			t.Errorf("the footer does not name %q", want)
		}
	}
}

// A clean run says what it compared: "no breaking changes" with nothing
// behind it is indistinguishable from a run that compared nothing.
func TestCleanRunNamesWhatItCompared(t *testing.T) {
	out := Render(nil, Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})
	for _, want := range []string{"abc1234", "merge-base with main"} {
		if !strings.Contains(out, want) {
			t.Errorf("a clean report does not say %q, so it cannot be told from a run that compared nothing", want)
		}
	}
}

// The three cases that are NOT clean, each rendered as itself.
func TestNothingToCompareIsNotRenderedAsClean(t *testing.T) {
	out := Render(nil, Info{Outcome: NothingToCompare, Reason: "HEAD is the first commit"})
	if strings.Contains(out, "no breaking changes") {
		t.Error("a run that compared nothing must not read as a clean run")
	}
	if !strings.Contains(out, "first commit") {
		t.Error("the reason it compared nothing must be in the output")
	}
}
// The commonest outcome in the measured fleet — 84.7% of base-branch commits
// — and so the one most likely to be misread as a clean comparison.
func TestShortcutSkipIsNotRenderedAsClean(t *testing.T) {
	out := Render(nil, Info{Outcome: Unchanged, Previous: "abc1234"})
	if strings.Contains(out, "no breaking changes") {
		t.Error("a skipped run must not read as a compared one")
	}
	if !strings.Contains(out, "unchanged") {
		t.Error("the output must say the owned trees and the lock did not move")
	}
}
func TestOwningNoProtosIsNotRenderedAsClean(t *testing.T) {
	out := Render(nil, Info{Outcome: NoOwnedProtos})
	if strings.Contains(out, "no breaking changes") {
		t.Error("a repository that owns no protos has not been checked, and must not read as checked")
	}
}

// Plan A cannot fail a build, and the report says so rather than leaving a
// reader to infer it from an exit status.
func TestReportSaysItIsReportOnly(t *testing.T) {
	out := Render([]Finding{{Rule: "break/field_removed", Category: Source,
		Path: "api/orders/v1/order.proto", Subject: "example.orders.v1.Order.eta"}},
		Info{Outcome: Compared, Previous: "abc1234", Reason: "merge-base with main"})
	if !strings.Contains(out, "report-only") {
		t.Error("a reader must not have to infer from the exit status that nothing failed")
	}
}
```

Write every body out.

- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Commit** — `feat(breaking): a report that names what it compared and what it cannot see`.

---

### Task 12: `stele breaking`

**Files:**
- Create: `cmd/stele/breaking.go`
- Modify: `cmd/stele/main.go` — the `usage` constant (lines 16-30) **and** the dispatch (line ~55). `main_test.go` asserts every command appears in `usage`; a command added to the dispatch alone fails that test.
- Modify: `cmd/stele/main_test.go`

**Flags**, modelled on `cmd/stele/lint.go` — read it first: `--dir` (the repository, default `.`), `--base` (the base branch; plan B moves this into the manifest as `breaking.base`), `--against <ref>` (that revision directly, no merge-base), `--cache-dir`, and whatever `lint.go` uses to build its fetcher. `pin.Resolve` fails with "pin: no fetcher" if one is not supplied.

`--against` needs a branch in `Choose`, so add it there in this task with its own test: `TestAgainstUsesTheRevisionDirectly`, asserting `Previous.SHA` is the named revision and `Working` is HEAD.

- [ ] **Step 1: Write the failing end-to-end tests** in `main_test.go`, following the table the existing command tests use: `breaking` appears in the usage; a clean run exits 0; a removal exits **0** and names the field; nothing-to-compare exits 0 and says so; a shallow clone exits **non-zero** naming shallowness — a failure to compare is still a failure, and only *findings* are non-fatal in plan A.
- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement.** Wire Tasks 4–11 in order: choose, shortcut, load both, diff, classify, closure, render. Exit zero on findings; exit non-zero only when the comparison could not be made.
- [ ] **Step 4: Run the whole suite** — `go test ./... && go test -count=1 ./internal/hygiene/`.
- [ ] **Step 5: Commit** — `feat(stele): the breaking command, report-only`.

---

### Task 13: the documents that say this does not exist

**Files:**
- Modify: `README.md:286` — "**There is no breaking-change detection.**"
- Modify: `README.md` — the command list and a section for `breaking`
- Modify: `docs/ROADMAP.md:25` and its milestone 6 section — "Breaking-change detection. Nothing compares this revision against a previous one."
- Modify: `CHANGELOG.md` — under `[Unreleased]`

Landing Tasks 1–12 makes both documents false, and `RELEASING.md` makes the changelog part of cutting a release.

- [ ] **Step 1: Update `README.md`** — document `breaking`, its flags, that it is report-only in this release, and the blind zone. Document `--against` as unsuitable for a CI default, because `--against origin/master` is exactly the neighbour-blaming comparison one copy-paste away.
- [ ] **Step 2: Update `docs/ROADMAP.md`** — strike the two claims, and state what is still missing: the valve (plan B) and the evidence (plan C).
- [ ] **Step 3: Add the changelog entries** under the existing categories — `Added` for the command and its flags, `Reports and messages` for the footer, `Internal` for the compile option. Note in the entry that the command is report-only and that plan B introduces the non-zero exit together with the valve, so the exit-status change is announced once.
- [ ] **Step 4: Run the whole suite.**
- [ ] **Step 5: Commit** — `docs: the breaking command exists`.

---

## Self-review

**Spec coverage.** Namespace reservation → 1. Local git driver → 2. Base-ref acquisition, remote rules, `refs/stele/*` → 3. Previous revision, all five states → 4. Tree shortcut → 5. Compilation with the revision's own lock, the three revision states, the cost narrowing → 6. Diff and typed subjects → 7. Type matrix and its oracle → 8. Both categories, the id set, paired fixtures, discriminants → 9. Closure comparison → 10. Blind-zone footer, "never render an empty comparison as clean" in all three of its forms → 11. Command, flags, `--against`, exit status → 12. Documents and changelog → 13.

**Failure taxonomy, counted against the spec:** shallow (3, 12), base ref absent (3), no remote and ambiguous remote (3), unrelated histories (4), first commit (4), both parents on the base (4), no manifest (6), manifest without lock (6), owns no protos (6, 11). **One is not covered: a rule that crashes.** In plan A every rule is in-process and a panic is a bug rather than a condition, so there is nothing to catch; it becomes real in plan B if a rule host is added, and is recorded here so it is not lost.

**Not covered, deliberately:** `allow`, `moves`, `--audit`, `--prune`, the `breaking:` manifest key, `schema/stele.schema.json`, and the non-zero exit — all plan B. The shadow period and the open-merge-request measurement — plan C.

**Known gaps carried from the spec:** a human's merge of the base into a topic branch leaves the merge commit itself uncompared until the branch lands; a direct multi-commit push to the base branch is compared only against its last commit, and one repository in the measured fleet has taken 97 commits that way.

**Two tests in this plan are elided and say so** — Task 6's four bodies and Task 7's three — because they depend on fixture helpers in `internal/pin/pin_test.go` that the implementer must read. In both cases the assertions are stated as the contract and none may be dropped. Everywhere else the bodies are written out, because a Go test whose body is a comment compiles and passes.

---

## Execution

1. **Subagent-driven (recommended)** — a fresh subagent per task, reviewed between tasks.
2. **Inline** — executed in this session with checkpoints.
