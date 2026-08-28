# Breaking-change detection, plan A: the engine

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `stele breaking` compares the working revision against the correct previous revision and reports wire and source breakages in the modules this repository owns.

**Architecture:** A new `internal/gitrepo` reads the repository the tool is run in — the existing `internal/source` deletes `.git` from every cache entry and cannot answer questions about history. `internal/breaking` decides which revision is the previous one, skips the run when the owned trees and the lock are unchanged, materialises the previous revision into a temporary directory, resolves it with `pin.Resolve(Dir: thatDir)` so it is compiled with *its own* lock, and diffs two `linker.Files` by full name.

**Tech Stack:** Go 1.26, `bufbuild/protocompile`, `google.golang.org/protobuf/reflect/protoreflect`, the repository's existing `internal/{config,pin,resolve,compile,lint}`.

**Spec:** `docs/design/2026-08-28-breaking-change-detection.md` (third revision). Read it before Task 1; this plan argues from it and does not restate its reasoning.

**Out of scope, covered by later plans:** plan B is the valve (`allow`, `moves`, `--audit`, `--prune`, manifest keys and JSON schema); plan C is rollout evidence (shadow period, open-merge-request measurement). Until plan B lands there is no way to permit a breaking change, so this command must not be wired into any repository's `make lint` or CI.

## Global Constraints

- **Go 1.26.** The module is `github.com/thegorangers/stele`.
- **No organisation identifiers anywhere**, including tests and fixtures. `internal/hygiene` walks the whole repository and fails on them; example names use `example.` packages. Run `go test ./internal/hygiene/` before every commit.
- **Rule ids are a permanent public contract** under `RELEASING.md`, namespaced `break/`. An id chosen in this plan cannot be renamed later without a breaking release.
- **Silence is forbidden.** Every way of failing to compare is an error naming itself. A run that compared nothing is never reported as clean.
- **Findings do not enter `stele.baseline`.** Nothing in this plan touches `internal/lint/baseline.go`.
- **British spelling in prose and comments**, matching the repository.
- **Commit messages state what changed and why**; no `Co-Authored-By` trailers, no mention of any assistant.

---

### Task 1: `internal/gitrepo` — reading the repository the tool runs in

**Files:**
- Create: `internal/gitrepo/gitrepo.go`
- Create: `internal/gitrepo/gitrepo_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Repo struct{ dir string }`
  - `func Open(dir string) (*Repo, error)`
  - `func (r *Repo) Head() (string, error)` — SHA of HEAD
  - `func (r *Repo) Parents(sha string) ([]string, error)`
  - `func (r *Repo) MergeBase(a, b string) (string, error)`
  - `func (r *Repo) IsAncestor(a, b string) (bool, error)`
  - `func (r *Repo) IsShallow() (bool, error)`
  - `func (r *Repo) TreeSHA(sha, path string) (string, bool, error)` — false when the path does not exist in that revision
  - `func (r *Repo) BlobSHA(sha, path string) (string, bool, error)`
  - `func (r *Repo) Materialise(sha, dir string) error` — extract the revision's tree into `dir`
  - `var ErrNotFound = errors.New("gitrepo: revision not found")`

- [ ] **Step 1: Write the failing test**

The test builds real repositories in `t.TempDir()`, because the whole point of this package is behaviour against real git.

```go
package gitrepo

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// run is a test helper: git, in dir, or fail the test.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestParentsAndMergeBase(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	first := run(t, dir, "rev-parse", "HEAD")

	run(t, dir, "checkout", "-q", "-b", "topic")
	write(t, dir, "b.txt", "two")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "topic work")
	topic := run(t, dir, "rev-parse", "HEAD")

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parents, err := r.Parents(topic)
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 || parents[0] != first {
		t.Fatalf("Parents(topic) = %v, want [%s]", parents, first)
	}
	base, err := r.MergeBase(topic, "main")
	if err != nil {
		t.Fatal(err)
	}
	if base != first {
		t.Fatalf("MergeBase = %s, want %s", base, first)
	}
}

func TestParentsOfFirstCommitIsEmpty(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "one")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	r, _ := Open(dir)
	parents, err := r.Parents(run(t, dir, "rev-parse", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 0 {
		t.Fatalf("Parents(root) = %v, want none", parents)
	}
}

func TestTreeSHAReportsAbsence(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, filepath.Join("api", "x.proto"), "syntax = \"proto3\";")
	run(t, dir, "add", ".")
	run(t, dir, "commit", "-qm", "first")
	head := run(t, dir, "rev-parse", "HEAD")

	r, _ := Open(dir)
	if _, ok, err := r.TreeSHA(head, "api"); err != nil || !ok {
		t.Fatalf("TreeSHA(api) ok=%v err=%v, want ok", ok, err)
	}
	if _, ok, err := r.TreeSHA(head, "nope"); err != nil || ok {
		t.Fatalf("TreeSHA(nope) ok=%v err=%v, want absent and no error", ok, err)
	}
}
```

Add `write` and the `strings` import; `write` creates parent directories and writes the file.

- [ ] **Step 2: Run the tests and watch them fail**

```
go test ./internal/gitrepo/ -run 'TestParents|TestTreeSHA' -v
```

Expected: build failure — `undefined: Open`. That is the red step; do not skip it, and do not write the implementation first.

- [ ] **Step 3: Implement**

Every method shells out to `git` with `cmd.Dir = r.dir`. Three rules the implementation must follow, because they are what the tests above are really about:

```go
// Parents returns the parents of sha, in git's order. The first parent of a
// merge is the branch the merge was made on, which is the whole reason this
// method exists. A root commit has none, and that is not an error: a
// repository's first commit has nothing before it to compare against.
func (r *Repo) Parents(sha string) ([]string, error) {
	out, err := r.git("rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil, fmt.Errorf("gitrepo: %w: %s", ErrNotFound, sha)
	}
	return fields[1:], nil
}

// TreeSHA reports the tree object at path in sha, and whether it exists.
// Absence is a value, not an error: a module root that a revision did not
// have is an ordinary state, and reporting it as a failure would make the
// caller unable to tell it from a broken repository.
func (r *Repo) TreeSHA(sha, path string) (string, bool, error) {
	out, err := r.git("rev-parse", sha+":"+path)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") ||
			strings.Contains(err.Error(), "exists on disk, but not in") {
			return "", false, nil
		}
		return "", false, err
	}
	return out, true, nil
}
```

`Materialise` uses `git archive <sha> | tar -x -C dir` — not `checkout`, which would move the user's HEAD, and not `worktree add`, which writes into the user's `.git`.

- [ ] **Step 4: Run the tests and watch them pass**

```
go test ./internal/gitrepo/ -v
```

- [ ] **Step 5: Add the shallow-clone test, and make it pass**

```go
func TestIsShallow(t *testing.T) {
	origin := t.TempDir()
	run(t, origin, "init", "-q", "-b", "main")
	for i := range 3 {
		write(t, origin, "a.txt", fmt.Sprintf("%d", i))
		run(t, origin, "add", ".")
		run(t, origin, "commit", "-qm", fmt.Sprintf("c%d", i))
	}
	shallow := filepath.Join(t.TempDir(), "clone")
	run(t, t.TempDir(), "clone", "-q", "--depth", "1", "file://"+origin, shallow)

	r, _ := Open(shallow)
	got, err := r.IsShallow()
	if err != nil || !got {
		t.Fatalf("IsShallow = %v, err = %v, want true", got, err)
	}
}
```

`IsShallow` is `git rev-parse --is-shallow-repository`. It is consulted *before* any merge-base failure is classified, per the spec: a shallow clone otherwise reports as unrelated histories and sends a person to fix the wrong thing.

- [ ] **Step 6: Commit**

```bash
git add internal/gitrepo/
git commit -m "feat(gitrepo): read the repository the tool is run in

internal/source deletes .git from every cache entry, so nothing in the
tool could answer a question about history. Materialise extracts a tree
with git archive rather than checking out or adding a worktree, because
this package must never move the user's HEAD or write into their .git."
```

---

### Task 2: choosing the previous revision

**Files:**
- Create: `internal/breaking/previous.go`
- Create: `internal/breaking/previous_test.go`

**Interfaces:**
- Consumes: `gitrepo.Repo` from Task 1.
- Produces:
  - `type Previous struct { SHA string; Working string; Reason string }`
  - `func Choose(r *gitrepo.Repo, base string) (Previous, bool, error)` — the bool is false when there is legitimately nothing to compare
  - `var ErrShallow, ErrBaseAbsent, ErrUnrelated error`

This is the task every earlier revision of the design got wrong; the tests are the specification.

- [ ] **Step 1: Write the failing tests, one per case in the spec**

```go
// On a topic branch the previous revision is the merge-base — never the base
// tip, because comparing against the tip makes a neighbour's merged change
// fail an unrelated branch.
func TestTopicBranchUsesMergeBase(t *testing.T) { /* build main, branch, advance main, assert Choose == fork point */ }

// On the base branch the previous revision is the first parent: what the
// branch looked like before the merge that landed. Merge-base here would be
// HEAD itself, which compares nothing and can only ever emit a false signal.
func TestBaseBranchUsesFirstParent(t *testing.T) { /* merge a topic into main, assert Choose == main-before-merge */ }

// A CI checkout that fabricates a merge of the topic into the base tip must
// not be compared against the base tip. The test is structural: the parent
// that is an ancestor of the base identifies the base side.
func TestFabricatedMergeUsesTopicParent(t *testing.T) { /* build refs/pull-style merge commit */ }

// ...and it must still work when the base moved after the checkout, which is
// why the test is ancestry and not equality with the tip.
func TestFabricatedMergeSurvivesBaseMoving(t *testing.T) { /* advance main after building the merge; assert unchanged */ }

// Both parents already on the base: a re-merge. There is nothing outside the
// base to compare, and that is reported as nothing to do, not as clean.
func TestBothParentsOnBaseIsNothingToCompare(t *testing.T) { /* assert ok == false */ }

// A repository's first commit has no previous revision.
func TestFirstCommitIsNothingToCompare(t *testing.T) { /* assert ok == false */ }

// A shallow clone is named as shallow, never as unrelated histories.
func TestShallowIsNamedAsShallow(t *testing.T) { /* depth-1 clone, assert errors.Is(err, ErrShallow) */ }
```

Write these bodies out in full using the `run`/`write` helpers from Task 1 — move them into `internal/breaking/testgit_test.go` rather than importing them across packages.

- [ ] **Step 2: Run and watch them fail** — `go test ./internal/breaking/ -v`.

- [ ] **Step 3: Implement `Choose`**

```go
func Choose(r *gitrepo.Repo, base string) (Previous, bool, error) {
	head, err := r.Head()
	if err != nil {
		return Previous{}, false, err
	}
	parents, err := r.Parents(head)
	if err != nil {
		return Previous{}, false, err
	}
	if len(parents) == 0 {
		return Previous{}, false, nil // a repository's first commit
	}

	baseSHA, err := r.ResolveRef(base)
	if err != nil {
		return Previous{}, false, classify(r, base, err)
	}

	// A fabricated merge, as a pull-request checkout produces, or a human's
	// merge of the base into a topic branch: both have one parent on the
	// base. The working revision is the other one. This is ancestry and not
	// equality with the tip, because the tip moves between checkout and run.
	if len(parents) == 2 {
		onBase := make([]bool, 2)
		for i, p := range parents {
			if onBase[i], err = r.IsAncestor(p, baseSHA); err != nil {
				return Previous{}, false, err
			}
		}
		switch {
		case onBase[0] && onBase[1]:
			return Previous{}, false, nil // a re-merge; nothing outside the base
		case onBase[0] != onBase[1]:
			working := parents[0]
			if onBase[0] {
				working = parents[1]
			}
			prev, err := r.MergeBase(working, baseSHA)
			return Previous{SHA: prev, Working: working,
				Reason: "merge-base with " + base + ", from the topic side of a merge commit"}, true, err
		}
	}

	// On the base branch itself, the previous revision is the first parent.
	onBase, err := r.IsAncestor(head, baseSHA)
	if err != nil {
		return Previous{}, false, err
	}
	if onBase {
		return Previous{SHA: parents[0], Working: head,
			Reason: "the first parent, because HEAD is on " + base}, true, nil
	}

	prev, err := r.MergeBase(head, baseSHA)
	if err != nil {
		return Previous{}, false, classify(r, base, err)
	}
	return Previous{SHA: prev, Working: head, Reason: "merge-base with " + base}, true, nil
}
```

`classify` consults `IsShallow` **first**, then distinguishes an absent ref from unrelated histories, and returns an error naming the cause and its fix. The message for an absent base names the refspec, not the depth: a merge-request pipeline fetches only its own ref, so the base is absent at any depth and `GIT_DEPTH: 0` does not cure it.

- [ ] **Step 4: Run and watch them pass.**

- [ ] **Step 5: Record the known gap in a comment, where it will be read**

```go
// Two exposures are known and documented rather than designed away.
//
// A human's merge of the base into a topic branch has the shape this
// function reads as a fabricated merge, so the merge commit itself is not
// compared, and a conflict resolution that dropped a field is not seen until
// the branch lands on the base. The base-branch run catches it then.
//
// A direct multi-commit push to the base branch bypasses the first-parent
// rule: only the last commit of the push is compared. The fleet this was
// measured on is configured so every change lands as a merge commit (14 of
// 14 on 2026-08-28), but branch protection permits a direct push for
// maintainers and one repository has taken 97 linear commits that way.
```

- [ ] **Step 6: Commit**

```bash
git add internal/breaking/
git commit -m "feat(breaking): choose the previous revision

Merge-base on a topic branch, the first parent on the base branch. The
first two design revisions used merge-base for both, which on the base
branch is HEAD itself: nothing compared, and the only signal the job
could emit was a false one.

The fabricated-merge test is ancestry, not equality with the base tip,
because the tip moves between checkout and run."
```

---

### Task 3: the tree shortcut

**Files:**
- Create: `internal/breaking/shortcut.go`
- Create: `internal/breaking/shortcut_test.go`

**Interfaces:**
- Consumes: `gitrepo.Repo`, `Previous` from Task 2, `config.File` for the module roots.
- Produces: `func Unchanged(r *gitrepo.Repo, prev Previous, roots []string, lockPath string) (bool, error)`

Measured at 84.7% of base-branch commits fleet-wide, so this is what makes the ordinary commit free. It runs *before* anything is fetched or compiled.

- [ ] **Step 1: Write the failing test**

```go
func TestUnchangedWhenNeitherProtosNorLockMoved(t *testing.T) {
	// two commits differing only in README.md
	// assert Unchanged == true
}

func TestChangedWhenAProtoMoved(t *testing.T) { /* assert false */ }

func TestChangedWhenOnlyTheLockMoved(t *testing.T) {
	// A dependency bump changes no owned file, and can still break the
	// consumers of what this repository re-exports.
	// assert false
}

func TestAbsentRootIsNotEqualToPresentRoot(t *testing.T) {
	// A revision that had no api/ at all must not compare equal to one that
	// does: the sentinel for absence has to differ from every tree SHA.
	// assert false
}
```

- [ ] **Step 2: Run and watch them fail.**

- [ ] **Step 3: Implement**

```go
func Unchanged(r *gitrepo.Repo, prev Previous, roots []string, lockPath string) (bool, error) {
	for _, p := range append(append([]string(nil), roots...), lockPath) {
		a, aok, err := r.TreeOrBlob(prev.Working, p)
		if err != nil {
			return false, err
		}
		b, bok, err := r.TreeOrBlob(prev.SHA, p)
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

- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit** — `perf(breaking): skip a run whose owned trees and lock are unchanged`.

---

### Task 4: compiling both revisions

**Files:**
- Create: `internal/breaking/revisions.go`
- Create: `internal/breaking/revisions_test.go`

**Interfaces:**
- Consumes: `gitrepo.Repo`, `Previous`, `config`, `pin`, `compile`.
- Produces: `func Load(ctx context.Context, r *gitrepo.Repo, sha string, fetch resolve.FetchFunc) (linker.Files, []string, error)` — the files and the import paths this revision owns.

- [ ] **Step 1: Write the failing test**

Build a repository with a manifest, a lock and one proto; commit; change the proto; commit. Assert that `Load` at the first commit sees the *first* version of the file, and that both loads succeed. Use a fetch function that serves a local fixture repository, as `internal/pin`'s own tests do — read them first and follow that pattern rather than inventing a second one.

Two behaviours the test must pin down, because they are the reason this task exists:

```go
// The previous revision is resolved with the lock that revision carried, not
// with today's. Compiling yesterday's protos against today's pins either
// fails outright or attributes another repository's change to this one.
func TestPreviousRevisionUsesItsOwnLock(t *testing.T) { /* ... */ }

// A manifest with no lock fails the run. Resolving it fresh would resolve
// every ref to today's tip, silently, which is the thing above forbidden.
func TestManifestWithoutLockIsAnError(t *testing.T) { /* ... */ }

// A revision with no manifest at all predates adoption. Nothing to compare.
func TestRevisionWithoutManifestIsNothingToCompare(t *testing.T) { /* ... */ }
```

- [ ] **Step 2: Run and watch them fail.**

- [ ] **Step 3: Implement**

`Load` materialises the revision into `t.TempDir()`-style scratch (`os.MkdirTemp`, removed by the caller), parses `stele.yaml` from it, and calls the existing resolver pointed at that directory — no new resolution path:

```go
dir, err := os.MkdirTemp("", "stele-breaking-")
if err != nil { return nil, nil, err }
if err := r.Materialise(sha, dir); err != nil { return nil, nil, err }

manifestPath := filepath.Join(dir, "stele.yaml")
if _, err := os.Stat(manifestPath); errors.Is(err, fs.ErrNotExist) {
	return nil, nil, ErrNoManifest // predates adoption; the caller exits zero
}
mf, err := config.Parse(manifestPath)
if err != nil { return nil, nil, err }
lockPath := filepath.Join(dir, "stele.lock")
if _, err := os.Stat(lockPath); errors.Is(err, fs.ErrNotExist) {
	return nil, nil, fmt.Errorf("revision %s has a manifest and no stele.lock: "+
		"resolving it afresh would pin every dependency to the commit its ref "+
		"names today, and compare this revision against dependencies it never "+
		"used", sha[:12])
}
g, err := pin.Resolve(ctx, pin.Options{Dir: dir, Manifest: mf, LockPath: lockPath, Fetch: fetch})
```

Owned import paths are those whose `Origin.Git` is empty, exactly as `internal/lint`'s `owned` does — read it and use the same predicate rather than a second spelling of it.

- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit** — `feat(breaking): compile a revision with the dependencies it recorded`.

---

### Task 5: the descriptor diff

**Files:**
- Create: `internal/breaking/diff.go`
- Create: `internal/breaking/diff_test.go`

**Interfaces:**
- Produces:
  - `type Change struct { Kind Kind; Subject string; Path string; Pos lint.Position; Detail string }`
  - `func Diff(prev, cur linker.Files, owned []string) []Change`

`Diff` produces neutral *changes*; Task 6 decides which are breaking. Splitting them is what lets the type matrix be a pure function of two types rather than a rule that has to walk descriptors.

- [ ] **Step 1: Write the failing test** — index by full name, both directions:

```go
func TestFieldRemovedIsDetected(t *testing.T)   { /* Order.eta present, then absent */ }
func TestFieldAddedIsNotAChange(t *testing.T)   { /* additions are never breaking; assert none reported */ }
func TestRenameIsRemovalPlusAddition(t *testing.T) { /* same number, new name */ }
func TestFileRemovedYieldsFileSubject(t *testing.T) {
	// A removed file has no full name. Its subject is its import path,
	// tagged, so it can never be mistaken for a declaration.
	// assert Subject == "file:api/orders/v1/order.proto"
}
```

- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement** — build `map[protoreflect.FullName]descriptor` for both sides over messages, fields, oneofs, enums, enum values, services and methods, restricted to `owned`; walk the union of keys.

Subjects are supplied by this function, not derived from a position, and the comment must say why:

```go
// The subject is stated here rather than derived from a position, which is
// how internal/lint does it. That constraint exists so a rule in another
// process, which can return only a line and a column, is not second-class.
// There is no plugin host here, and this function holds both descriptor
// sets, so it knows a removed field's name exactly. Deriving it from a
// position could not name a removed field at all: it would name the
// surviving message, and one permission would then license the removal of
// every field of that message.
```

- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit** — `feat(breaking): diff two revisions by full name`.

---

### Task 6: the type-compatibility matrix, measured

**Files:**
- Create: `internal/breaking/wiretypes.go`
- Create: `internal/breaking/wiretypes_test.go`

**Interfaces:**
- Produces: `func WireCompatible(from, to protoreflect.Kind) bool`

- [ ] **Step 1: Write the measuring test first — it is the specification**

```go
// The matrix is measured, not written, because hand-written enumerations in
// this repository have been wrong four revisions running.
//
// "Encode as one, decode as the other" is not the oracle: it measures the
// wire type. float and fixed32 are both four bytes and every decode
// succeeds, while 1.0 reads back as 1065353216. So the oracle is a corpus
// and a defined cross-type equality: numeric kinds compare numerically,
// string and bytes compare as byte sequences, each direction separately, and
// a pair is compatible only if the whole corpus agrees.
func TestMatrixIsWhatMeasurementSays(t *testing.T) {
	for _, from := range kinds {
		for _, to := range kinds {
			want := measure(t, from, to) // encodes the corpus, decodes, compares
			if got := WireCompatible(from, to); got != want {
				t.Errorf("WireCompatible(%v, %v) = %v, measured %v", from, to, got, want)
			}
		}
	}
}

func TestFloatAndFixed32AreNotCompatible(t *testing.T) {
	if WireCompatible(protoreflect.FloatKind, protoreflect.Fixed32Kind) {
		t.Fatal("float and fixed32 share a wire type and not a value")
	}
}
```

The corpus is zero, one, minus one, the boundaries of every width, a value that overflows a narrower type, and invalid UTF-8 for `string`/`bytes`.

- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement `WireCompatible` as a table**, and let the test correct it. Where the table and the measurement disagree, **stop and resolve it against the protobuf encoding specification** — do not edit the table to match the measurement reflexively, and do not edit the measurement to match the table. The spec requires a human decision here.
- [ ] **Step 4: Run and watch it pass.**
- [ ] **Step 5: Commit** — `feat(breaking): a measured wire-compatibility matrix`.

---

### Task 7: the rules

**Files:**
- Create: `internal/breaking/rules.go`
- Create: `internal/breaking/rules_test.go`
- Create: `internal/breaking/testdata/` — paired fixtures

**Interfaces:**
- Produces: `func Classify(changes []Change, prev, cur linker.Files) []Finding`, and the ids below.

Ids, fixed now because `RELEASING.md` makes them permanent:

`break/field_removed`, `break/field_renamed`, `break/field_number_changed`, `break/field_type_changed`, `break/field_cardinality_changed`, `break/field_oneof_changed`, `break/message_removed`, `break/enum_removed`, `break/enum_value_removed`, `break/enum_value_number_changed`, `break/service_removed`, `break/method_removed`, `break/method_signature_changed`, `break/method_streaming_changed`, `break/file_removed`, `break/go_package_changed`.

- [ ] **Step 1: Write paired fixtures, one per rule, in both directions**

Every rule gets two fixtures: one where it fires, one where a legal change of the same shape leaves it silent. A rule with only the first half is not known to detect what it claims.

```go
// Each entry names a directory under testdata/ holding before/ and after/.
var cases = []struct {
	dir      string
	wantRule string // empty means: this change is legal and nothing fires
}{
	{"field_removed", "break/field_removed"},
	{"field_added", ""},                    // additions are never breaking
	{"field_type_widened_int32_int64", ""}, // wire-compatible
	{"field_type_int32_string", "break/field_type_changed"},
	// ...one pair per rule
}
```

- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement `Classify`.** Each finding carries the category (wire or source) and, where the rule has one, the discriminant that plan B's permissions will match on — for a type change the pair of kinds, for a oneof change the destination. **Removals have no discriminant**: the subject is the whole of the change.
- [ ] **Step 4: Run and watch them pass.**
- [ ] **Step 5: Commit** — `feat(breaking): the first slice of rules, each with a legal counterpart`.

---

### Task 8: the report

**Files:**
- Create: `internal/breaking/report.go`
- Create: `internal/breaking/report_test.go`

- [ ] **Step 1: Write the failing test**

Findings render as `path:line:col: category: rule: message`, matching the standard `stele lint` renders to. Three things the test must hold:

```go
// Every report names the blind zone, because a report that says "no breaking
// changes" without qualification reads as "safe to change anything".
func TestFooterNamesTheBlindZone(t *testing.T) {
	out := Render(nil, Info{Previous: "abc1234", Reason: "merge-base with master"})
	for _, want := range []string{"json_name", "int32", "google.api.http"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer does not name %q", want)
		}
	}
}

// A clean run says what it compared. "No breaking changes" with nothing
// behind it is indistinguishable from a run that compared nothing.
func TestCleanRunNamesWhatItCompared(t *testing.T) { /* assert the SHA and the reason appear */ }

// A run with nothing to compare says so, and is not rendered as clean.
func TestNothingToCompareIsNotClean(t *testing.T) { /* ... */ }
```

- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Commit** — `feat(breaking): a report that names what it compared and what it cannot see`.

---

### Task 9: `stele breaking`

**Files:**
- Create: `cmd/stele/breaking.go`
- Modify: `cmd/stele/main.go` — add `case "breaking":` to the dispatch at line 55
- Modify: `cmd/stele/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Flags: `--against <ref>` (uses that revision directly, no merge-base), `--base <branch>` (overrides `breaking.base`; until plan B lands the manifest key does not exist, so this flag is how the base is named).

- [ ] **Step 1: Write the failing end-to-end test** in `main_test.go`, following the shape the existing command tests use: build a repository, run the command, assert exit status and output. Cases: a clean run exits 0; a removal exits non-zero and names the field; nothing-to-compare exits 0 and says so; a shallow clone exits non-zero naming shallowness.
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement.** Wire Tasks 2–8 in order: choose, shortcut, load both, diff, classify, render. Document `--against` in `README.md` as unsuitable for CI, because `--against origin/master` is exactly the neighbour-blaming comparison one copy-paste away.
- [ ] **Step 4: Run and watch it pass.**
- [ ] **Step 5: Run the whole suite, and the hygiene test explicitly**

```
go test ./... && go test -count=1 ./internal/hygiene/
```

- [ ] **Step 6: Commit** — `feat(stele): the breaking command`.

---

### Task 10: the narrowing that makes the previous side cheaper

**Files:**
- Modify: `internal/compile/compile.go:47-79`
- Modify: `internal/compile/compile_test.go`

The previous revision never supplies a position, so it does not need source info, and `Compile` hard-codes `SourceInfoStandard` with no way to ask for less.

- [ ] **Step 1: Write the failing test** — a compile with source info off produces descriptors with no `SourceCodeInfo`, and one with it on still does. The existing comment in `compile.go` says dropping information early cannot be undone downstream; the test must show the default is unchanged, so no existing caller is affected.
- [ ] **Step 2–4: fail, implement (an option, defaulting to today's behaviour), pass.**
- [ ] **Step 5: Commit** — `perf(compile): let a caller compile without source info`.

---

## Self-review

**Spec coverage.** Acquiring the previous revision → Tasks 1, 2. Tree shortcut → 3. Compiling with the revision's own lock, and the three revision states → 4. Findings and typed subjects → 5. Type matrix and its oracle → 6. The two categories and paired fixtures → 7. Blind-zone footer and "never report an empty comparison as clean" → 8. Command and failure taxonomy → 9. Cost narrowing → 10.

**Deliberately not covered here, and why.** `allow`, `moves`, `--audit`, `--prune`, the `breaking:` manifest key and `schema/stele.schema.json` are plan B — they are the valve, and none of them can be designed against a comparison nobody has run yet. The shadow period, the open-merge-request measurement and its independent oracle are plan C. The dependency-closure comparison (a pin bump breaking consumers while owned files are identical) is **deferred to plan B** with the valve, because until a permission exists there is no way to accept one of its findings; the spec's scope section is otherwise implemented by Task 4's `owned` predicate.

**Known gaps carried from the spec, not introduced here:** a human's merge of the base into a topic branch leaves the merge commit itself uncompared; a direct multi-commit push to the base branch is compared only against its last commit; a repository owning no proto modules (one exists in the measured fleet) has an empty producer comparison, which Task 8 must report rather than render as clean.

---

## Execution

Plan complete. Two options:

1. **Subagent-driven (recommended)** — a fresh subagent per task, reviewed between tasks.
2. **Inline** — executed in this session with checkpoints.
