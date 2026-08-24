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

### Added

- **`stele lint`** — a first slice of contract linting: an engine, a rule
  interface, eight rules and a command. It compiles the protos this repository
  owns and reports findings as `path:line:col: severity: rule: message` with
  what to do about it on the line beneath. Nothing is written and nothing is
  changed. Dependencies are compiled, because an import has to link, and are not
  judged: a finding about somebody else's contract is one nobody here can act on.

  The eight rules are `stele/syntax_declared`, `stele/package_declared`,
  `stele/package_lower_snake_case`, `stele/package_version_suffix`,
  `stele/directory_matches_package`, `stele/enum_zero_value_unspecified`,
  `stele/enum_value_prefix` and `stele/enum_value_upper_snake_case`. Each was
  measured against 35 hand-written proto files from a fleet where no lint runs
  at all before it was included; the counts, and the rules that were measured
  and rejected, are in [docs/ROADMAP.md](docs/ROADMAP.md).

  There is no plugin host, no breaking-change detection and no AIP profile in
  this slice, and the README says so rather than implying more is covered.
- **A `lint` block in `stele.yaml`**, and in the schema. It sets what a rule
  costs — `error`, `warning` or `off` — and which import paths no rule, or one
  rule, is applied to. This is the adoption mechanism: a repository that has
  never linted has findings on the first run, and the way to a green build is a
  reviewed field in the manifest rather than `allow_failure: true` in a CI job
  that is invisible from the repository the rules are about. A `warning` does
  not protect new code from the same mistake, and the roadmap says so.
- **`stele lint --rules`**, printing the rules this build carries with their ids
  and what each requires. A rule id is a public contract that goes into a
  manifest; a list somebody has to read the source to find is one they guess at.

### Refused input

- A `lint` block that configures nothing, a rule id that is not
  `namespace/name`, a severity outside `error`/`warning`/`off`, two entries
  naming one rule, and an ignore entry that is empty or looks like a glob are
  all refused, naming the field. A glob-looking entry matched literally would be
  an exemption the author believes they have written and does not have.
- A rule id no loaded rule carries fails the run, naming it and listing what is
  loaded. A typo in an ignore list, or a rule that has been removed, otherwise
  leaves the manifest claiming a protection or an exemption that does not exist.

### Note for a future release

Adding a built-in rule can turn a green build red. Under
[RELEASING.md](RELEASING.md) that is a breaking change — "a command's exit status
changes for an input that already worked" — so it bumps the field that signals
"read this before upgrading", and the entry names the rule.

### Fixed

- A plugin published as an archive holding **two binaries** — one release
  tarball, `protoc-gen-x` and `protoc-gen-x-plugin` beside it — could not be
  used for both. The download cache was keyed by the archive's sha256 alone, so
  the two entries shared one directory and one record of what had been
  extracted; taking the second member overwrote the first member's record, and
  every later run refused the first entry as corrupted. The message told the
  reader to delete the directory, which the next run broke again. An entry is
  now identified by the digest *and* the member taken from it. Nothing that
  worked changes; a plugin already downloaded from an archive is fetched once
  more, because it now lives at a different path inside the cache.
- The lock is written through a temporary file and renamed into place instead
  of being overwritten where it lies. A process killed mid-write, or a full
  disk, used to leave a truncated `stele.lock` — and the dangerous truncation
  is not the one that fails to parse but the one that lands on an entry
  boundary and loads cleanly as a lock pinning fewer dependencies than the
  build that wrote it consumed. A failed write now leaves the previous lock
  exactly as it was.

### Reports and messages

- A dependency that cannot be fetched no longer reports a raw `git` error. The
  failure is classified from git's own words and the message names the
  repository, the ref that was asked for, what happened and what to do about
  it: a refused credential, an address that serves no repository and a host
  that cannot be reached have three different recoveries and now read as three
  different problems. git's own output is quoted rather than replaced.
- A ref that does not exist on the remote says what makes a ref disappear — a
  branch deleted after a merge, a tag never pushed — and that a commit SHA is
  accepted in its place.
- A locked commit that is gone is explained once rather than twice. The lock
  layer adds the fact only it knows — which file records that commit, and for
  which ref — and no longer restates the fetcher's explanation of what a
  rewritten commit is.

### Internal

- Failure behaviour is tested rather than intended (milestone 5). An
  interrupted clone, a killed process's abandoned scratch directory, a remote
  that refuses authentication, one that answers with a server error, one that
  closes mid-response, a truncated and a cancelled plugin download, an
  interrupted `go install`, a cache that cannot be written to, and several
  processes sharing one cold cache: each is exercised against real git
  repositories, a real Go toolchain over a `file://` proxy and `httptest`
  servers, offline. Two of the defects above were found this way.

- Parity with the reference tool is measured in this repository's own CI, on
  every push and every pull request, against a corpus committed under
  `test/parity/corpus`. Until now it was proven by hand, on one machine,
  against private checkouts nobody else could obtain — which made the project's
  acceptance criterion an assertion rather than evidence. A release is gated on
  the same check, because output bytes are the contract and a tag that
  generates something different is not releasable.
- The reference tool is pinned by version, and refused if the binary reports
  another one. The plugins are pinned by module and version in the corpus
  manifests, installed into stele's own cache, and put on the reference tool's
  `PATH` by the harness, so both tools run the same plugin binaries.
- `STELE_PARITY_CORPUS` still points the harness at an external corpus, so the
  fleet run stays possible. What the shipped corpus does *not* cover, compared
  with that run, is written down in `test/parity/corpus/README.md`.
- `export` is measured in that CI run too, and no longer only on the single
  repository whose vendored tree was compared by hand. Five invocations over
  three producers cover a dependency's module whole, `--exclude-imports` and
  its absence, `--path` in the producer's coordinates, the caller's own
  modules, a producer reached only through another producer, and the
  well-known types both tools leave out. A corpus with no committed vendored
  tree now has the reference tool run over the same files instead, one
  invocation at a time, so a failure names the invocation.
- The export parity test fails again when nothing was compared. It was
  weakened to a skip when no corpus declared an export block; now that one
  does, a skip would mean a corpus had lost its export blocks and nobody was
  told.

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
