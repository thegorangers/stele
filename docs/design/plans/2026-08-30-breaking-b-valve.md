# Breaking-change detection, plan B: the valve

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development` to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** a repository can say what it does about each breaking-change rule, approve individual changes it has decided to make, declare a migration as a rename map — and `stele breaking` starts failing a build for the rules the repository chose to keep.

**Architecture:** a `breaking:` section in `stele.yaml`, mirroring `lint:` exactly rather than inventing a second vocabulary for the same question. Severity resolution and exit status live beside the existing engine; permissions and moves are applied inside `Classify` and before `Diff` respectively.

**Tech Stack:** Go 1.26, the existing `internal/{config,breaking}`, `schema/stele.schema.json`.

**Spec:** `docs/design/2026-08-28-breaking-change-detection.md`, sections "The valve" and "Migrations: a rename map, not a permission". Read both before Task 1. The valve section was rewritten on 2026-08-30 and its reasoning is the argument this plan implements — including why an earlier revision's refusal of per-rule severity was wrong.

**What plan A left:** `stele breaking` exists, reports 20 rules, compares owned modules and the re-exported closure, and **always exits zero** because there was no way to accept a finding. This plan is what makes a non-zero exit fair.

## Global Constraints

- **Go 1.26.** Module `github.com/thegorangers/stele`.
- **No organisation identifiers anywhere**, tests, fixtures and documents included. `internal/hygiene` walks the whole repository. Use `example.` names; run `go test -count=1 ./internal/hygiene/` before every commit.
- **One vocabulary.** `error | warning | off` is what `lint.rules` already uses. Do not invent a second spelling, and where a predicate already exists — severity parsing, rule-id shape, path prefix matching — reuse it rather than writing a second copy. This repository has paid four times for duplicated predicates; `internal/source.NetworkFetch` exists because of the fourth.
- **Rule ids and discriminant spellings are a permanent public contract** under `RELEASING.md`: they land in somebody's manifest.
- **Silence is forbidden.** Every refusal names itself and names the line it is about.
- **Every task ships at least one NEGATIVE assertion** — a test that the mechanism does NOT fire where it must not. Two Critical defects in plan A survived a suite that only asserted presence.
- **Test mutations belong in a copy outside the repository.** Leave `git status` clean.
- British spelling. No `Co-Authored-By` trailers; never mention any AI assistant.

---

### Task 1: the `breaking:` section of the manifest

**Files:**
- Modify: `internal/config/config.go` — the `File` struct and new types
- Modify: `internal/config/parse.go` if the strictness helper needs it
- Modify: `internal/config/parse_test.go`
- Modify: `schema/stele.schema.json`

**Read first:** `Lint`, `LintRule` and `LintSeverities` in `config.go`, and the comment on `Lint` explaining why the block's absence means every rule at its default rather than a lint that does nothing. The same reasoning governs `breaking:`.

**Interfaces produced:**
- `type Breaking struct { Base string; Rules []BreakingRule; Allow []Permission; Moves []Move }`
- `type BreakingRule struct { ID, Severity string; Ignore []string }` — deliberately the same shape as `LintRule`
- `type Permission struct { Rule, Subject, Change, Reason string }`
- `type Move struct { From, To string }`
- `File` gains `Breaking *Breaking`

- [ ] **Step 1: Write the failing tests.** The refusals are the contract; write one test per refusal and give each a message assertion, not just an error check:
  - an unknown key inside `breaking:` is refused naming the key (the parser is strict; confirm the nested strictness helper covers a new block — `decodeStrict` exists because `KnownFields(true)` does not propagate into nested `node.Decode`)
  - a severity that is not `error`, `warning` or `off` is refused, listing the accepted spellings
  - two rule entries naming one rule id are refused naming it
  - a permission with no `reason` is refused
  - a permission naming a rule id that is not a `break/` id is refused
  - a `move` with `from` equal to `to` is refused
  - two moves with the same `from` are refused naming it
  - **negative:** a manifest with no `breaking:` block parses, and `File.Breaking` is nil — absence must not become "everything off"
- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement.** Follow `Lint`'s parsing exactly. `Base` is the base branch; it replaces plan A's `--base` flag as the default (the flag stays and wins).
- [ ] **Step 4: Run and see them pass.**
- [ ] **Step 5: Update `schema/stele.schema.json`.** `schema/schema_test.go` holds the schema against the parser; a new manifest field with no schema entry fails it. Read that test first to learn what it checks.
- [ ] **Step 6: Run `go test -count=1 ./...` and commit.**

---

### Task 2: severity, and the exit status this plan exists to justify

**Files:**
- Create: `internal/breaking/severity.go`, `severity_test.go`
- Modify: `internal/breaking/report.go` and its test
- Modify: `cmd/stele/breaking.go` and `cmd/stele/main_test.go`

**Interfaces produced:**
- `type Severity int` with `SeverityError`, `SeverityWarning`, `SeverityOff`
- `func Resolve(f Finding, cfg *config.Breaking) Severity`
- `Finding` gains a `Severity` field, stamped by the engine

**The rules, from the design:**
- Every rule is `error` unless the manifest says otherwise. An absent `breaking:` block means every rule at `error`.
- A rule at `off` produces no finding at all — it is not a suppressed finding, it is an unasked question.
- A rule's `ignore` list excludes import paths from that rule alone, exactly as `lint.rules[].ignore` does. Reuse the path-prefix predicate the lint engine already uses; do not write a second one.
- **The run exits non-zero exactly when a finding stands at `error`.** Warnings never fail it.
- A rule id in `breaking.rules` that no rule carries is an error naming it — the two ways that happens are a typo and a rule that was removed, and both mean the manifest claims something that does not exist. `lint` already refuses this; find how and match it.

- [ ] **Step 1: Write the failing tests.**
  - no `breaking:` block: a removal is `error` and the command exits non-zero
  - `severity: warning` on that rule: the finding is still reported, marked warning, and the command exits **zero**
  - `severity: off`: no finding at all, and the summary does not count it
  - a rule's `ignore` excludes only that rule on that path, and the same finding on another path still stands
  - **negative:** configuring rule A does not change rule B's severity
  - **negative:** a warning-only run exits zero even when there are many warnings
  - an unknown rule id in `breaking.rules` is refused naming it
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Change the exit status and say so where a reader will see it.** Plan A's `runBreaking` always returned zero and its output says "report-only". Both change together. Update `cmd/stele/main_test.go`'s cases: a removal with default configuration now exits non-zero; a removal at `warning` exits zero.
- [ ] **Step 6: Verify with the built binary** on a two-commit repository, once with no `breaking:` block and once with the rule at `warning`. Paste both outputs and both exit statuses into your report.
- [ ] **Step 7: Run the whole suite and commit.**

---

### Task 3: permission for one specific change

**Files:**
- Create: `internal/breaking/permit.go`, `permit_test.go`
- Modify: `internal/breaking/report.go` if a permitted finding renders differently

**Interfaces produced:**
- `func Permit(findings []Finding, cfg *config.Breaking) (kept []Finding, stale []config.Permission)`

**The rules:**
- A permission matches on `(rule, subject)` and, where the rule carries a discriminant, on `change` too. **Removals have no discriminant**, and a permission that supplies one for a removal is refused — at parse time if the rule can be known there, otherwise here, naming the line.
- The discriminant spelling is the engine's: `int32 -> int64`, with spaces. A manifest copied from the documentation must match. `Finding.Change` already carries it.
- A matched finding is removed from the run and does not affect the exit status.
- **A permission that matched nothing is stale: reported, never fatal.** The design's reasoning is that to use a stale permission for a removed field the field must first be resurrected under the same full name and removed again — and that making it fatal reddens every branch that merged the base in, which is the neighbour-blaming failure by another door.

- [ ] **Step 1: Write the failing tests.**
  - a permission matching a finding removes it, and the run exits zero where it would have exited non-zero
  - a permission whose `change` differs from the finding's does NOT match — this is the test that stops one permission approving every type change
  - a permission for a removal that supplies `change` is refused
  - a permission matching nothing is reported stale and the run still passes
  - **negative:** a permission for `Order.total` does not match a finding about `Order.subtotal`
  - **negative:** a permission does not match the same rule on a different subject in another message
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Prove it load-bearing** — drop the `change` comparison in a copy and confirm the mismatched-discriminant test fails.
- [ ] **Step 6: Commit.**

---

### Task 4: moves, a rename map applied before the comparison

**Files:**
- Create: `internal/breaking/moves.go`, `moves_test.go`
- Modify: `internal/breaking/diff.go` at the point revisions are indexed

**Read first:** the design's "Migrations: a rename map, not a permission". The invariant is the whole of it: **a move suppresses nothing.** The previous revision's declarations are renamed through the map and then the ordinary comparison runs, so a lossless rename produces no findings because there is nothing left to report, and a rename that also dropped a field reports that field by its new name as an ordinary removal. Laundering is impossible by construction rather than by a check that has to be got right.

**Interfaces produced:**
- `func ApplyMoves(prev Revision, moves []config.Move) (Revision, error)`

**The rules:**
- A move renames a package prefix. `file:`-tagged path moves take the same form.
- `to` must be a module this repository owns; pointing a move at a dependency would make a pinned third party the authority on whether your declarations still exist.
- Refused, not resolved, when ambiguous: two entries with the same `from`, a cycle, or a `from` that still exists in the current revision while claiming to have moved.
- A `go_package` change explained by a declared move is part of that move; one that is not explained is a finding.
- A move that matches nothing is stale, on the same terms as a permission.
- A file **split** — one file becoming two in the same package — is not expressible. Say so in the doc comment and in the README, and let it report as ordinary removals and additions.

- [ ] **Step 1: Write the failing tests.**
  - a lossless package rename declared as a move produces NO findings
  - the same rename with one field dropped reports exactly that field's removal, under its NEW name
  - **negative:** a move does not suppress an unrelated removal elsewhere in the package
  - a move whose `to` is not owned is refused
  - a cycle, a duplicate `from`, and a `from` that still exists are each refused naming the entry
  - a move matching nothing is reported stale, not fatal
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Prove the invariant by execution** — take the lossless-rename fixture, delete one field from it, and confirm the finding appears. A move that could hide it would pass the lossless test and fail this one.
- [ ] **Step 6: Commit.**

---

### Task 5: `--audit` and `--prune`

**Files:**
- Create: `cmd/stele/breaking_audit.go` or extend `cmd/stele/breaking.go`
- Modify: `cmd/stele/main_test.go`

**The rules, from the design:**
- `--audit` reports stale permissions, stale moves, **and how many rules this repository has switched off.** The last is the whole of what replaces the earlier revision's refusal to allow configuration: it does not forbid and does not argue, it makes visible across a fleet what is actually being protected.
- `--audit` is meant for a scheduled job that reddens alone and blocks nobody. Decide its exit status and state the reasoning: a job that never fails reports to nobody, and one that fails on the merge path is the thing this design refuses.
- `--prune` deletes stale entries from the manifest and leaves a reviewable diff. It must not reformat or reorder anything it did not delete — a diff nobody can read is a diff nobody reviews. Check whether the repository already has a manifest-rewriting primitive before writing one.
- The manifest records no dates, so `--audit` reports *stale*, not *long-lived*, and must make no claim about age.

- [ ] **Step 1: Write the failing tests**, including: `--audit` on a clean manifest reports nothing stale and still reports the count of disabled rules; `--prune` removes exactly the stale entries and leaves every other line byte-identical (assert on the file's bytes, not on a parse — a reformat that round-trips would pass a parse-based test).
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Commit.**

---

### Task 6: the documents, and the announced change of exit status

**Files:** `README.md`, `docs/ROADMAP.md`, `CHANGELOG.md`

`RELEASING.md` makes a command's exit status for an input that already worked a breaking change. This plan changes it deliberately and it must be announced as one, in the same release as the valve that makes it fair — that pairing is the reason plan A shipped report-only rather than shipping a fatal command and a valve separately.

- [ ] **Step 1: `README.md`** — document `breaking:`, its four keys, the severity vocabulary and the default (every rule at error), permissions with their discriminant rule, moves and the file-split limit, `--audit` and `--prune`. Remove every claim that the command is report-only and always exits zero; `README.md:19` and `README.md:289` both say so today.
- [ ] **Step 2: `docs/ROADMAP.md`** — three places say report-only (lines 30, 678-679, 706). Correct them, and state what remains: plan C's shadow period and the open question about repositories with no consumers.
- [ ] **Step 3: `CHANGELOG.md`** — under `[Unreleased]`, in the file's own categories. The exit-status change belongs in the category the file reserves for behaviour a consumer depends on, and the entry must say plainly that a build which passed before can now fail, and what to write in the manifest to keep it passing. Under `RELEASING.md`'s 0.x rule this is a MINOR: what would be MAJOR bumps MINOR while the major version is 0.
- [ ] **Step 4: Run the whole suite and commit.**

---

## Self-review

**Spec coverage.** Manifest section → 1. Per-rule severity, per-rule ignore, exit status → 2. Permissions and their discriminant → 3. Moves as a rename map → 4. `--audit` including the disabled-rule count, `--prune` → 5. Documents and the announced exit-status change → 6.

**Deliberately not here.** Plan C: the shadow period and the measurement over open work with an independent oracle. And the design's open question — a repository with no consumers yet — which per-rule severity now largely answers but does not close; the candidate worth measuring first is that having consumers is a *fact* the fleet's locks record, not a claim a manifest makes.

**Known limits carried from the design:** a file split is not expressible as a move; `--audit` can report stale but not long-lived, because the manifest records no dates.
