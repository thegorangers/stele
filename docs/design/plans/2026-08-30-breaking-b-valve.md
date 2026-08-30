# Breaking-change detection, plan B: the valve

> **For agentic workers:** REQUIRED SUB-SKILL: use `superpowers:subagent-driven-development`. Steps use checkbox (`- [ ]`) syntax.

**Goal:** a repository can say what it does about each breaking-change rule, and approve individual changes it has decided to make — and then `stele breaking` starts failing a build for the rules the repository chose to keep.

**Spec:** `docs/design/2026-08-28-breaking-change-detection.md`, section "The valve". Read it before Task 1, including the two qualifications it makes about itself: this decision was taken ahead of its measurement, and the producer is configuring a risk the consumer bears.

**What plan A left:** `stele breaking` reports 20 rules and **always exits zero**, because there was no way to accept a finding. This plan is what makes a non-zero exit fair.

**Not here: `moves`.** The design's rename map is a separate plan. A first draft of this one put it in Task 4 and it was **not implementable as written**: `Revision.Files` holds compiled, immutable descriptors, and remapping index keys does not work because a field whose type moved still carries the old fully-qualified `type_name`, so every such field would become `Modified` and the mechanism's headline property — a lossless rename produces no findings — would fail. The approach is genuinely unknown, so it gets a plan that begins with a feasibility probe rather than a task list. Until then a repository facing a package rename holds the rule at `warning`, which this plan gives it.

## Global Constraints

- **Go 1.26.** Module `github.com/thegorangers/stele`.
- **No organisation identifiers anywhere**, tests, fixtures and documents included; `internal/hygiene` walks the whole repository. Use `example.` names; run `go test -count=1 ./internal/hygiene/` before every commit.
- **Reuse, never re-spell.** `rule.Severity` with `SeverityError`/`SeverityWarning`/`SeverityOff`, `String()` and `ParseSeverity` **already exist and are public** (`rule/rule.go`); `internal/lint` reuses them by aliasing. Do not declare a third. The same discipline cost this repository four copies of one fetch helper before `source.NetworkFetch` existed.
- **Rule ids and discriminant spellings are a permanent public contract** under `RELEASING.md`: they land in somebody's manifest.
- **Silence is forbidden.** Every refusal names itself and the line it is about.
- **Every task ships at least one NEGATIVE assertion.** Two Critical defects in plan A survived a suite that asserted only presence.
- **Verify claims about other packages by reading them.** A first draft of this plan asserted two things about this repository that are false — that `KnownFields(true)` does not propagate into nested structs, and that the schema test reflects over `config.File`. Both were checked and both were wrong. If a step tells you how another package behaves, confirm it before relying on it, and report the discrepancy rather than working around it silently.
- Test mutations belong in a copy outside the repository; leave `git status` clean. British spelling. No `Co-Authored-By`; never mention any AI assistant.

---

### Task 1: the canonical rule set

**Files:** create `internal/breaking/rules_registry.go` (name it as you see fit) and its test; modify `internal/breaking/rules.go`.

**Why first:** the 20 rule ids exist only as string literals scattered through `rules.go` (`grep '"break/'` finds them at the site that emits each finding). Nothing can validate a manifest that names a rule, count what a repository disabled, or decide whether a rule carries a discriminant, because no list exists. Tasks 2, 3, 4 and 6 all need it.

**Interfaces produced:**
- `func Rules() []RuleInfo` — the canonical set, sorted by id
- `type RuleInfo struct { ID string; Category Category; HasDiscriminant bool }`
- `func LookupRule(id string) (RuleInfo, bool)`

`HasDiscriminant` is the fact Task 3 needs and nothing currently records: a permission for a rule that carries one is refused without `change`, and a permission for a rule that does not is refused *with* it.

- [ ] **Step 1: Write the failing test that ties the registry to reality.** This is the whole value of the task, and a hand-maintained list that drifts is worse than none:
  - every `break/` id emitted anywhere in `rules.go` appears in `Rules()`, and vice versa. Derive the emitted set from the source rather than from a second hand-written list — read `internal/aip/ledger_test.go`, which solves exactly this problem for AIP ids and is the pattern to follow.
  - every id's `Category` matches what the rule actually stamps on its findings. The existing fixtures in `rules_test.go` assert `(rule, category, change)` triples; use them as the source of truth rather than restating categories by hand.
  - `HasDiscriminant` agrees with whether the rule's fixtures carry a non-empty `change`.
  - **negative:** `LookupRule("break/nope")` reports not found; `LookupRule("stele/enum_value_prefix")` likewise — this registry is `break/` only.
- [ ] **Step 2: Run and see it fail.**
- [ ] **Step 3: Implement**, and have `rules.go` emit findings using the registry's constants rather than repeating literals, so the two cannot drift.
- [ ] **Step 4: Run and see it pass.** Then delete one id from the registry and confirm the parity test fails; restore.
- [ ] **Step 5: Commit.**

---

### Task 2: the `breaking:` section of the manifest

**Files:** modify `internal/config/config.go`, `internal/config/parse.go`, `internal/config/parse_test.go`, `schema/stele.schema.json`, and add corpus files under `schema/testdata/manifest/`.

**Read first:** `Lint`, `LintRule` and `LintSeverities` in `config.go`, and `LintRule`'s custom `UnmarshalYAML` in `parse.go`.

**Two facts, both verified, because a first draft of this plan got both wrong:**
- `KnownFields(true)` **does** propagate into nested plain structs. It stops propagating where a type has a hand-written `UnmarshalYAML`, because `decodeStrict` ends in `n.Decode(out)`, which is not strict. So: give `Breaking` no custom unmarshaler unless a dual-shape field forces one, and if any type below it gets one, that type needs its own strictness check. Test unknown keys at **every** level — `breaking:`, a `rules` entry, an `allow` entry — not only the top.
- `schema/schema_test.go` is **corpus-driven**: it walks `schema/testdata/manifest/{valid,invalid,beyond,stricter}` and compares the schema's verdict with the parser's, example by example. It does not reflect over `config.File`, so a new field with no schema entry does **not** fail it. Adding corpus examples is therefore part of this task, not a consequence of it. Rejected examples must state their reason in an opening `# invalid:` comment — a test enforces that.

**Interfaces produced:**
- `type Breaking struct { Base string; Rules []BreakingRule; Allow []Permission }`
- `type BreakingRule struct { ID string; Severity string; Reason string; Ignore []string }`
- `type Permission struct { Rule, Subject, Change, Reason string }`
- `File` gains `Breaking *Breaking`

Note `BreakingRule.Reason`: the spec requires a reason for **lowering** a rule, because otherwise approving one change is gated while switching a rule off for ever is free.

- [ ] **Step 1: Write the failing tests**, one per refusal, each asserting the message names the offending thing:
  - unknown key at each of the three levels
  - a severity outside `error|warning|off`, listing the accepted spellings
  - two rule entries naming one id
  - a rule at `warning` or `off` with no `reason`
  - a rule id that no rule carries — use Task 1's registry
  - a permission with no `reason`
  - a permission naming a rule that carries a discriminant, with no `change`
  - a permission naming a rule that carries none, with a `change`
  - **negative:** a manifest with no `breaking:` block parses and `File.Breaking` is nil — absence must not become "everything off"
  - **negative:** `reason` is NOT required on a rule left at `error`
- [ ] **Step 2: Run and see them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Add the schema and its corpus.** Valid and invalid examples for each refusal above that the schema can express; the rule-id and discriminant checks it cannot, and the schema's own description field should say so, as `lint`'s does.
- [ ] **Step 5: Run `go test -count=1 ./...` and commit.**

---

### Task 3: severity, applied and announced

**Files:** modify `internal/breaking/rules.go` or add `internal/breaking/severity.go`; modify `internal/breaking/report.go`; tests for both.

**Interfaces produced:**
- `Finding` gains `Severity rule.Severity` — the existing public type, aliased as `internal/lint` aliases it, not a new one
- `func ApplySeverity(findings []Finding, cfg *config.Breaking) []Finding`

**The rules:**
- Absent `breaking:` block, or a rule not named: `error`.
- `off` produces no finding at all — an unasked question, not a suppressed answer, so it must not appear in the summary counts.
- A rule's `ignore` excludes import paths from that rule alone. The lint engine's predicate for this is `ignores` in `internal/lint/lint.go` and is **unexported**: decide between exporting it and moving it somewhere both packages can reach, make the change, and say which you chose and why. Do not write a second copy — that is what the Global Constraints forbid.
- **Every lowered rule is named in the report on every run**, with its severity and its reason, in the shape `unpinned` already uses for plugins. A repository may protect nothing; it may not do so quietly.
- The exit status does **not** change in this task. Plan A's guarantee holds until Task 5.

- [ ] **Step 1: Write the failing tests.**
  - default: a removal is `error`
  - `warning`: still reported, marked, and counted separately
  - `off`: no finding, and the summary does not count it
  - a rule's `ignore` excludes that rule on that path only, and the same rule still fires elsewhere
  - the report names every lowered rule with its reason, and a run with none says nothing extra
  - **negative:** configuring rule A does not change rule B
  - **negative:** the run still exits zero, at every severity — this is Task 5's job, and a test here pins that it has not happened yet
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5:** mutate `ApplySeverity` to ignore the configuration and confirm the tests fail; restore.
- [ ] **Step 6: Commit.**

---

### Task 4: permission for one specific change

**Files:** create `internal/breaking/permit.go` and its test; modify `internal/breaking/report.go`.

**Interfaces produced:**
- `func Permit(findings []Finding, cfg *config.Breaking) (kept []Finding, stale []config.Permission)`

**The rules:**
- Matches on `(rule, subject)` and, where Task 1's registry says the rule carries a discriminant, on `change` too, with the engine's exact spelling — `int32 -> int64`, spaced.
- A matched finding leaves the run entirely and cannot affect the exit status.
- **A permission that matched nothing is stale: reported, never fatal.** To use a stale permission for a removed field the field must first be resurrected under the same full name and removed again; and making it fatal would redden every branch that merged the base in, which is the neighbour-blaming failure by another door.
- **`Finding.Change` must be rendered.** It is not today (`renderFinding` folds in category, subject, message and fix only), which means a user cannot see the discriminant they must copy into a permission. A mechanism whose input is invisible is not a mechanism.

- [ ] **Step 1: Write the failing tests.**
  - a matching permission removes the finding
  - a permission whose `change` differs does **not** match — this is what stops one permission approving every type change on a subject
  - a permission matching nothing is reported stale and the run still passes
  - the rendered finding contains the discriminant, spelled exactly as a permission must spell it
  - **negative:** a permission for `Order.total` does not match `Order.subtotal`
  - **negative:** a permission does not match the same rule on the same field name in a different message
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5:** drop the `change` comparison in a copy; confirm the mismatched-discriminant test fails.
- [ ] **Step 6: Commit.**

---

### Task 5: the exit status, and the base branch from the manifest

**Files:** modify `cmd/stele/breaking.go`, `cmd/stele/main_test.go`, `internal/breaking/report.go`.

This is the task the previous four exist to make fair, and it comes after them deliberately: between Task 3 and here the command must never fail a build it has no way to let a repository accept.

- [ ] **Step 1: Write the failing tests** in `main_test.go`: a removal with no `breaking:` block exits **non-zero**; the same at `warning` exits zero; the same with a matching permission exits zero; a rule that failed to run fails the run even at `off`; `--base` absent but `breaking.base` present now works, where today the command hard-errors when neither `--base` nor `--against` is given (`cmd/stele/breaking.go` guards on this).
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Remove the report-only notice.** `reportOnlyNotice` in `internal/breaking/report.go` is emitted by every `Render` and is now false; so is the paragraph in `breakingUsage` in `cmd/stele/breaking.go`.
- [ ] **Step 6: Verify with the built binary** on a real two-commit repository: once with no `breaking:` block, once at `warning`, once with a permission. Paste all three outputs and exit statuses into your report.
- [ ] **Step 7: Commit.**

---

### Task 6: `--audit` and `--prune`

**Files:** modify `cmd/stele/breaking.go`; modify `cmd/stele/main_test.go`.

**Decisions already made — do not re-open them:**
- `--audit` **exits non-zero when it finds a stale permission**, and never fails for what a repository has lowered. A scheduled job that can never fail reports to nobody; a job that fails on the merge path is what this design refuses. Stale is a fact about a file that needs an edit; a lowered rule is a decision that needs no one's approval.
- `--audit` counts a rule as lowered when its `severity` is not `error` **or** its `ignore` list covers every path the rule would otherwise check. An audit that a mechanism it does not count can zero is worse than no audit.
- `--audit` reports about **one repository**. It makes no claim about a fleet; there is no aggregator behind such a claim and an earlier revision of this design was caught making one.
- `--prune` deletes stale permissions and nothing else.

**The unbudgeted part, named rather than hidden:** `--prune` must leave every line it did not delete byte-identical, and this repository has **no** primitive for that. `internal/config/migrate/emit.go` *generates* a manifest from a struct with a fresh encoder; it never reads an existing one and does not round-trip, and re-emitting a `yaml.Node` loses quoting style, blank lines and indentation. So `--prune` is line-range surgery driven by `yaml.Node.Line`. If that proves larger than this task, **ship `--audit` alone and report that `--prune` needs its own task** — that is a correct outcome, not a failure.

- [ ] **Step 1: Write the failing tests**: `--audit` on a clean manifest reports no stale entries, still reports what is lowered, and exits zero; with a stale permission it names it and exits non-zero; a rule whose `ignore` covers everything is counted as lowered; `--prune` removes exactly the stale entries and every other byte of the file is unchanged — assert on the file's bytes, since a reformat that round-trips would pass a parse-based test.
- [ ] **Step 2–4: fail, implement, pass.**
- [ ] **Step 5: Commit.**

---

### Task 7: the documents, and the announced change of exit status

**Files:** `README.md`, `docs/ROADMAP.md`, `CHANGELOG.md`.

`RELEASING.md` makes a command's exit status for an input that already worked a breaking change. It changes here deliberately, in the same release as the valve that makes it fair — that pairing is why plan A shipped report-only.

- [ ] **Step 1: `README.md`.** Document `breaking:`, the severity vocabulary and the default, the required `reason` for a lowered rule, permissions and the discriminant, `--audit` and `--prune`. **Four** places say the command is report-only: lines 19, 289, the sample output around 598, and the section at 677. Grep rather than trusting this list.
- [ ] **Step 2: `docs/ROADMAP.md`.** Lines 30 and 678-679 say report-only and are now false. **Line 706 is about plan C's shadow period and is correct — leave it.**
- [ ] **Step 3: `CHANGELOG.md`** under `[Unreleased]`, in the file's own categories. The exit-status entry must say plainly that a build which passed before can now fail, and what to write in the manifest to keep it passing. Under `RELEASING.md`'s 0.x rule this is a MINOR.
- [ ] **Step 4: Run the whole suite and commit.**

---

## Self-review

**Spec coverage.** Canonical rule set → 1 (the spec assumes it; nothing had it). Manifest section, reasons, refusals, schema corpus → 2. Severity, per-rule ignore, announcement of lowered rules → 3. Permissions, discriminant, stale, and rendering the discriminant so it can be copied → 4. Exit status and `breaking.base` → 5. `--audit`, `--prune` → 6. Documents → 7.

**Deliberately not here.** `moves`, for the reason at the top — the approach is unknown and needs a probe, not a task list. Plan C's shadow period and the measurement over open work. And the design's remaining open question: defaults derived from who pins whom, which needs a fleet measurement nobody has made.

**Ordering.** Nothing depends on a later task. Task 5 flips the exit status only after Tasks 3 and 4 have given a repository two ways to accept a finding.
