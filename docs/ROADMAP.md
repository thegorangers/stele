# Roadmap

Where `stele` stands and what it needs before it can be relied on.

## Where it stands today

- `generate` matches the reference tool **byte for byte on 12 repositories**, managed
  mode included.
- `export` matches a committed vendored tree **byte for byte on one repository**
  (48/48 files), and matches the reference tool invocation by invocation on the
  corpus committed here, which runs in CI on every change. The richest consumer
  fails on a genuine defect in that fleet, not in the tool.
- Two repositories run on it in real CI, one of them the fleet's acceptance suite.
- Dependencies are pinned by commit; plugins are pinned by module+version or by
  per-platform digest, installed into the tool's own cache and verified after
  installation.

That is enough to migrate a repository deliberately, with a human watching. It is
not yet enough to hand to someone who has not been in the room: what a cold
cache, an interrupted clone or a full disk does is still untested (milestone 5).

## Milestone 1 — defects found by use

Every one of these was found by running the tool, not by reading it. They are first
because each has already cost someone time.

- ~~**#1 `--update` writes a lock the tool then refuses to read.**~~ Fixed. A lock
  entry is identified by `(git, ref)`, as the pinned run always looked it up; a
  repeated name is ordinary and no longer refused, and one request pinned to two
  commits is the error, named at write time as well as at read time. Addresses are
  recorded as each manifest wrote them and are not normalised — see the decision
  recorded in `internal/lockfile/lock.go`.
- ~~**#2 `migrate` emits `ssh://` addresses that fail in CI.**~~ Fixed. `migrate` now
  authors `https://` unconditionally, whatever the source `Makefile` used. It is the
  form both environments rewrite with `git config insteadOf`, so the emitted manifest
  needs no editing on a workstation that clones over ssh either. Only `migrate`
  chooses a transport, and only because it authors an address rather than obeying
  one — resolving and the lock still record what a manifest wrote. The reasoning,
  including why no `--transport` flag and no `glab:` shorthand, is in
  `internal/config/migrate/migrate.go`.
- ~~**#3 Dependency order changes the outcome.**~~ Fixed. The behaviour was right and
  the documentation was incomplete: precedence is real — a root a manifest *claims*
  outranks one the tool *inferred* on a producer's behalf — but resolution reached it
  by accident of arrival, letting the first supplier claim a path and judging later
  ones against it. Two stale vendored copies reached before the owner therefore
  killed a run that the same repositories, listed the other way round, resolved
  cleanly. Resolution now collects every supplier of an import path and decides
  afterwards, ranking them by what they are rather than by when they were read, so
  any ordering of `deps` yields the same winner, the same drift report and the same
  error. The rule is stated in `internal/resolve/resolve.go` (`resolveFiles`) and in
  the README, and is held by a test that resolves one closure under every permutation
  of its dependencies.
- ~~**#4 `migrate` parses Makefile comments as invocations.**~~ Fixed. A comment
  documenting an example invocation was read as one, and its prose apostrophe was
  reported as an unterminated quote — an item in a report whose only value is that
  it is trusted enough to be read. The Makefile reader now removes comments before
  looking for an invocation, honouring the two rules that differ and were checked
  against GNU Make 4.4.1 rather than assumed: outside a recipe `#` starts a comment
  even inside quotes and runs to the end of the *logical* line, so a backslash
  continues the comment; inside a recipe the text goes to the shell, so `#` opens a
  comment only at a word boundary outside quotes — leaving `repo.git#subdir=api`
  and `--path 'a#b'` intact — and ends at the newline. It is not a Make parser and
  says so: `.RECIPEPREFIX`, which moves the boundary the model rests on, is refused
  outright rather than mis-read. The limits are stated in
  `internal/config/migrate/makefile.go` (`logicalLines`). Across the 12 migratable
  repositories of the measured fleet the unresolved reports are byte-identical
  before and after; on the repository that produced the defect exactly one item
  disappears, the false one, and the other twelve remain.

## Milestone 2 — releases

~~Without this there is no way to answer "which version broke?" or to roll back.~~
Done, at `v0.1.0`.

- Tags, and a built binary that states its own version instead of `(devel)`. There
  are no `-ldflags`: since Go 1.24 the toolchain stamps the version from the
  repository itself and `debug.ReadBuildInfo` reads it back, so an `-X` flag would
  be a second copy of a fact that is right only when somebody remembers to pass it.
  What replaces the flag is a check — the release workflow runs the binary it just
  built and refuses to publish unless it reports exactly the tag. `report.IsRelease`
  is the predicate, and a build that is not a release says so in the report rather
  than leaving a reader to recognise a pseudo-version.
- Published binaries for `linux/{amd64,arm64}` and `darwin/{amd64,arm64}` with
  checksums, so adopting the tool does not require a Go toolchain. Windows is
  deliberately absent: nothing consuming this runs on it and nothing here has been
  tested there. The defence of the set is in `RELEASING.md`.
- Reproducibility: `-trimpath`, `CGO_ENABLED=0`, the Go version pinned exactly, and
  the workflow builds `linux/amd64` twice and fails if the digests differ.
- A changelog that records **behaviour** changes, since output bytes are the
  contract. `Generated output` is its own category, breaking by definition and
  separate from features and fixes.
- The versioning policy is written down in `RELEASING.md`, including why the first
  release is `v0.1.0` and what would make it `v1.0.0`.

## Milestone 3 — parity in the tool's own CI

~~Parity is currently proven by hand, on one machine, against a corpus in a temporary
directory. A regression would reach a user before it reached us.~~ Done.

- A corpus committed to this repository, under `test/parity/corpus`. It could not
  be snapshots of the measured fleet: those are an organisation's private
  repositories, and `internal/hygiene` forbids organisation-specific identifiers
  with no exemptions. So it is synthetic — and its weakness is real and stated
  where it is used: a synthetic corpus exercises only what somebody thought to
  include. What went into it was not imagined. It is the shapes the fleet run
  measured and the design notes name as where parity is at risk: managed mode, a
  target with two inputs, `paths` rebased into a producer's coordinates, an input
  that reads a dependency, a producer whose own vendored tree resolves its
  imports, and plugins pinned by module and version. The import that crosses a
  repository boundary is a genuine googleapis file rather than one written to be
  easy.
- Parity runs on every push and every pull request, and gates a release: output
  bytes are the contract, so a tag that generates something different is not
  releasable however green the unit tests are.
- The reference tool is pinned by the corpus, and the harness refuses a binary
  reporting another version — on a workstation as much as in CI. The plugins are
  pinned by the corpus manifests and installed into stele's own cache, which is
  then put on the reference tool's `PATH`: both tools run the same binaries, so
  a difference can only be the difference under measurement.
- What the shipped corpus does **not** cover — scale, fetching, export, real API
  surfaces, other plugins — is listed in `test/parity/corpus/README.md`, and the
  fleet run stays possible through `STELE_PARITY_CORPUS`.

## Milestone 4 — export parity beyond one repository

~~`generate` is well covered; `export` is not, and it is the half that removes the
registry. It needs several shapes: many dependencies, narrow `paths`, and a
dependency reached only transitively.~~ Done, for what a corpus of this kind can
reach.

- The shipped corpus measures `export` rather than skipping it. Five
  invocations, listed with what each is for in
  `test/parity/corpus/README.md`: a dependency's module whole — the shape every
  invocation in the measured fleet has, since an export is always pointed at
  somebody else's repository; the same with `--exclude-imports`, so the import
  closure is compared against its own absence; `--path` in the producer's
  coordinates, against the reference tool's workspace-relative ones, with a
  sibling directory present so that a filter which widened by one level fails;
  the caller's own modules, a shape the fleet never uses but the command line
  reaches; and a producer whose own vendored tree supplies the import that
  reaches the output.
- A producer reached **only transitively** is in the corpus: `parity/platform`
  is named by no checkout, reaches the closure because another producer
  declares it, and reaches the output because a selected file imports it.
- The well-known types are **pinned, not merely agreed on**. Neither tool
  emits `google/protobuf/*`, and the corpus asserts their absence against both
  trees — a comparison of two tools would go on passing if both started
  emitting them.
- The guard weakened in milestone 3 is back: nothing compared is a failure
  again, not a skip.

What this does **not** reach is stated where it is used, and it is the reason
this milestone does not close the question on its own. A synthetic corpus has no
vendored tree that anybody's builds produced: the expectation is the reference
tool run now, at one pinned version, so a drift that only shows across versions
of that tool is invisible here. It is five invocations over seven proto files
against closures of thousands. And it exercises only the shapes somebody thought
of. The fleet run — a committed tree grown over months, compared through
`STELE_PARITY_CORPUS` — remains the stronger measurement, and the harness keeps
both: a checkout that declares `vendored` is compared against it.

## Milestone 5 — failure behaviour

Concurrent cache access is tested. These are not:

- interrupted download or clone; is a partial entry ever visible?
- a cold cache with the network unavailable — does the error say what to do?
- disk exhaustion during extraction.

The cache is content-addressed and written atomically, so the design intends these to
be safe. Intent is not evidence.

## Milestone 6 — lint

Deliberately last, and worth stating why the order is not laziness: in the fleet this
tool was built for, contract linting **does not run at all** — not in CI, not on
merge. There is no protection to preserve here, only protection to introduce. A lint
that lands before the generation half is trustworthy would be a second thing to
maintain and a first thing to switch off.

When it lands it should use the plugin host, so the first rules are written against
the same interface an outside contributor would use.

## Not planned

- **A registry.** The absence of one is the point.
- **Full compatibility with the reference tool's configuration.** A measured subset,
  and an error naming anything outside it.
- **Writing code generation plugins.** Existing ones over the standard protocol.
