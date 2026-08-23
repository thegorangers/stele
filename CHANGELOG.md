# Changelog

What changed, and what it changed for someone using the tool.

## What earns an entry

This tool's contract is the bytes it writes. A change that alters generated
output is a breaking change even when no flag, no manifest field and no exported
Go symbol moved — so the categories are not the usual ones. `Generated output`
exists precisely because such a change looks like nothing in a diff of features
and fixes, and is the one a consumer must read.

- **Generated output** — the bytes of `generate` or `export` differ for an input
  that already worked. The entry must say for which inputs, and what a consumer
  will see in a diff. This category is load-bearing: everything in it is
  breaking under the versioning policy in [RELEASING.md](RELEASING.md),
  regardless of how small the change was to write.
- **Refused input** — a manifest, lock or repository the tool used to accept and
  now rejects, or the reverse. The bytes did not change; whether there are any
  did. Also breaking.
- **Added** — new commands, flags or manifest fields. Existing inputs behave as
  before.
- **Fixed** — a defect repaired without changing the output of anything that was
  already correct. If the fix changed bytes for anybody, it belongs in
  `Generated output` as well, and the entry says both.
- **Reports and messages** — the version report, error text, the lock file's
  contents. Read by people and by diffs, so worth recording; not the generated
  bytes.
- **Internal** — refactoring, tests, CI. Recorded only when a reader would
  otherwise wonder where something went.

An entry names the effect on the user, not the commit. "Resolution collects
every supplier before deciding" is an effect; "refactored resolveFiles" is not.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
loosely — the categories above replace its fixed set for the reason given.
Versions follow the policy in [RELEASING.md](RELEASING.md).

## [Unreleased]

Nothing yet.

## [v0.1.0] — 2026-08-23

First tagged release. Everything before it was consumed by commit, which is what
this release exists to stop.

The state being released: `generate` matches the reference tool byte for byte on
twelve repositories, managed mode included. `export` matches a committed
vendored tree byte for byte on one repository, 48 files of 48. Two repositories
run on it in real CI. That is the evidence, and it is why the number is 0.1.0
rather than 1.0.0 — see [RELEASING.md](RELEASING.md).

### Added

- `generate` and `export`, driven by a `stele.yaml` manifest, with dependencies
  fetched by git and pinned by commit in `stele.lock`.
- `migrate`, which translates an existing buf configuration into a manifest and
  reports every part it could not translate rather than translating it wrongly.
- `plugins`, which warms the plugin cache and says which binary a run would
  invoke.
- `version`, which names everything that determined the output bytes: stele
  itself, protocompile, protobuf-go, and each plugin with its origin and pin.
- Plugin pinning by module and version, or by per-platform URL and SHA-256
  digest, installed into the tool's own cache and verified after installation.
- A JSON schema for `stele.yaml` and `stele.lock`, held to the parser by a test
  rather than written beside it.
- Published binaries for linux/amd64, linux/arm64, darwin/amd64 and
  darwin/arm64, with checksums, so adopting the tool no longer requires a Go
  toolchain.

### Fixed

Four defects, each found by using the tool rather than by reading it.

- **`--update` wrote a lock the tool then refused to read**
  ([#1](https://github.com/thegorangers/stele/issues/1)). A lock entry is now
  identified by `(git, ref)`, as the pinned run always looked it up. A repeated
  dependency name is ordinary and is no longer refused; one request pinned to two
  commits is the error, and is named when the lock is written as well as when it
  is read.
- **`migrate` emitted `ssh://` addresses that fail in a typical CI image**
  ([#2](https://github.com/thegorangers/stele/issues/2)). It now authors
  `https://` unconditionally, whatever the source Makefile used — the form both
  a CI image and a workstation rewrite with `git config insteadOf`. Only
  `migrate` chooses a transport, and only because it authors an address rather
  than obeying one.
- **Dependency order changed the outcome** ([#3](https://github.com/thegorangers/stele/issues/3)).
  Precedence between suppliers of an import path was real but was reached by
  accident of arrival, so two stale vendored copies read before the owner killed
  a run that the same repositories, listed the other way round, resolved
  cleanly. Resolution now collects every supplier and decides afterwards; any
  ordering of `deps` yields the same winner, the same drift report and the same
  error.
- **`migrate` parsed Makefile comments as invocations**
  ([#4](https://github.com/thegorangers/stele/issues/4)). A commented-out example
  was read as a real invocation and its prose apostrophe reported as an
  unterminated quote. Comments are now removed before an invocation is looked
  for, honouring the two rules that differ inside and outside a recipe.
  `.RECIPEPREFIX`, which moves the boundary this rests on, is refused outright
  rather than mis-read.

### Generated output

- Nothing. Of the four fixes above, three change which runs succeed rather than
  what a successful run writes, and the fourth changes only a report. Across the
  twelve migratable repositories of the measured fleet the unresolved reports
  are byte-identical before and after #4; on the repository that produced the
  defect exactly one item disappears, the false one.

### Reports and messages

- A binary that was not built from a tag now says so beside its version, instead
  of leaving a reader to recognise a pseudo-version. `stele version` reports what
  the Go toolchain stamped into the binary; there is no `-ldflags` stamping to
  forget.
