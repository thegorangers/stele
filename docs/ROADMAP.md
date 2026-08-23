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
- **#2 `migrate` emits `ssh://` addresses that fail in CI.** It carries over what the
  source `Makefile` used, but a typical CI image has no ssh binary — access is an
  `insteadOf` rewrite over https. Every migration hits this.
- **#3 Dependency order changes the outcome.** `Graph.ImportRoots` documents order as
  carrying no precedence; in practice, listing the owner before a repository that
  vendors a stale copy turns a fatal conflict into a reported drift. Either the
  documentation or the behaviour is wrong.
- **#4 `migrate` parses Makefile comments as invocations**, producing spurious
  "could not translate" reports that train people to ignore the report.

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
