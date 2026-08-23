# Roadmap

Where `stele` stands and what it needs before it can be relied on.

## Where it stands today

- `generate` matches the reference tool **byte for byte on 12 repositories**, managed
  mode included.
- `export` matches a committed vendored tree **byte for byte on one repository**
  (48/48 files). The richest consumer fails on a genuine defect in that fleet, not
  in the tool.
- Two repositories run on it in real CI, one of them the fleet's acceptance suite.
- Dependencies are pinned by commit; plugins are pinned by module+version or by
  per-platform digest, installed into the tool's own cache and verified after
  installation.

That is enough to migrate a repository deliberately, with a human watching. It is
not yet enough to hand to someone who has not been in the room.

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

Without this there is no way to answer "which version broke?" or to roll back.

- Tags, and `-ldflags` so a built binary states its own version instead of `(devel)`.
- Published binaries for the platforms that consume it, so adopting the tool does not
  require a Go toolchain.
- A changelog that records **behaviour** changes, since output bytes are the contract.

## Milestone 3 — parity in the tool's own CI

Parity is currently proven by hand, on one machine, against a corpus in a temporary
directory. A regression would reach a user before it reached us.

- Fixed snapshots of real repositories as test data, so the corpus is reproducible.
- Parity runs on every change, not on request.
- The reference tool pinned, so a change in it cannot be mistaken for a change in us.

## Milestone 4 — export parity beyond one repository

`generate` is well covered; `export` is not, and it is the half that removes the
registry. It needs several shapes: many dependencies, narrow `paths`, and a
dependency reached only transitively.

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
