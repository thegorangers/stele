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
- What an interrupted clone, an interrupted install, a cold cache with no network
  and a refused write do is tested rather than intended, offline, against real git
  repositories and a real Go toolchain (milestone 5).

- Contract linting has a first slice: an engine, a rule interface designed as the
  one an out-of-tree rule implements, eight rules each measured against 35 real
  proto files before it was chosen, `stele lint`, and a host that runs rules from
  outside this repository, pinned in the manifest exactly as a code generation
  plugin is.
- Breaking-change detection has a first slice too: `stele breaking` compares the
  working revision against the correct previous one — merge-base on a topic
  branch, first parent on the base branch — and reports wire and source
  breakages in 19 rule ids, plus changes in the dependency closure this
  repository re-exports. It is report-only: it always exits zero on a finding,
  because there is no way yet to permit a legitimate one. The valve, and the
  evidence a fleet needs before it can enable the check, are not built
  (milestone 6).

That is enough to migrate a repository deliberately, with a human watching. What is
left before 1.0 is the rest of contract linting (milestone 6), and the honest
caveats each milestone states about what its evidence does not reach.

## Milestone 1 — defects found by use

Every one of these was found by running the tool, not by reading it. They are first
because each has already cost someone time.

- ~~**#1 `--update` writes a lock the tool then refuses to read.**~~ Fixed. A lock
  entry is identified by `(git, ref)`, as the pinned run always looked it up; a
  repeated name is ordinary and no longer refused, and one request pinned to two
  commits is the error, named at write time as well as at read time. Addresses are
  recorded as each manifest wrote them and are not normalised — see the decision
  recorded in `internal/lockfile/lock.go`.

  That decision stands, and the walk no longer pays for it. Resolution now
  deduplicates the walk by the cache's own notion of identity — host and path,
  already what the cache treats as one entry — while the lock goes on recording
  every `(git, ref)` a manifest states. Two notions of identity, each used for
  the one thing it is right about. Before this, a repository the root declared
  over `ssh` and a producer declared over `https` was fetched, walked and merged
  twice, and its files were attributed to whichever address sorted first, which
  made `export --dep` on the root's own dependency report an empty module.
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

  The three shortfalls that fix left behind are closed. A `define`/`endef` body is
  no longer read by its own indentation: a body is stored verbatim and means
  whatever the place it is expanded makes it mean — probed against GNU Make 4.4.1,
  which strips nothing inside a body, so `echo one # two` keeps its hash and
  `a\#b` its backslash, and hands the same body to the shell when it is expanded
  in a recipe. The body is dropped, and refused by name when it holds `buf export`
  or `go install`;
  refusing every `define` instead would refuse Makefiles it translates correctly,
  one of them in the measured fleet. `.RECIPEPREFIX` is now refused in one shape
  rather than two: the plugin reader used to file it as an unreadable invocation,
  where the run aborted on the export reader's error before anyone read it. And
  `\#` is read by counting the backslash run, as measured: `a\\#b` is `a\` and a
  comment, `a\\\#b` is `a\#b` entire, and a backslash with no hash after it is
  left as written. Re-run over the same fleet, every unresolved report and every
  emitted manifest is byte-identical to before.

  "By name" is literal, and the hole it leaves is now stated in `logicalLines`
  beside the reader's other limits: a body reaching the same command through a
  variable — `$(BUF) export` — matches no keyword, is dropped like any other
  body, and nothing says so. Seeing it would mean expanding the variable, which
  is the thing this reader deliberately does not do.

- ~~**#5 `migrate` demands the vendor target's dependencies, not the imports.**~~
  Fixed. The unresolved list used to be a copy of what the `Makefile` copied
  in, which made it fleet-uniform boilerplate: all five repositories migrated
  by hand were told they needed `googleapis` and a validation module, one of
  the five imported anything from either, and one declared no third-party
  module at all, had no vendored tree in git, imported nothing outside the
  well-known types, and was told to supply both anyway. A list that is the same
  everywhere is read nowhere; the person doing the migration pasted both
  entries in five times without looking, which is exactly the training a report
  like that gives.

  `migrate` now reads the repository's own `.proto` files. It indexes every
  file under the declared module roots, walks the imports of the files the
  configuration compiles — transitively, since `annotations.proto` is useless
  without `http.proto` — subtracts the `google/protobuf/` files the compiler
  carries, and reports what is left file by file. Three consequences, each
  replacing a piece of guesswork:

  - A dependency is demanded only when something reads it. An imported file
    with no declared source is named, with the references that could have
    produced it. `migrate` still invents no git address: the two conventional
    ones are conventional, not universal, and a guessed address is how a
    manifest comes to name a repository nobody meant.
  - A dependency's `paths:` is now derived — the files that are read plus the
    files that are generated from. Every hand-written manifest needed this
    narrowing and wrote it by hand; the export's own `--path` is discarded,
    because it records what was copied in.
  - A vendored tree nothing imports is drift, so it is left out and said out
    loud in the notes. This is not hypothetical: on the seven repositories
    still to migrate, one vendors a producer its own `buf.gen.yaml` comment
    says is held back for a later phase, and that repository's unresolved count
    falls from three to one — the remaining one naming the two files it does
    read.

  Measured over those seven: unresolved entries fall from 38 to 29, and every
  one of the 14 fleet-uniform registry demands is gone, replaced by 6 reports
  naming the 31 files those repositories actually read. What is left is dominated by `buf export` that
  pinned no commit, which no reading of the repository can fix.

  Without a working copy the import graph cannot be read, and `FromBuf` — two
  files of configuration and nothing else — still reports the vendor target,
  because then it is the only evidence there is.
- ~~**#6 Nothing warns about a floating `ref`.**~~ Fixed, as a warning. A
  pre-existing branch pinned a third-party module at `ref: main`; its options
  land in the descriptor and therefore in the generated bytes, so an upstream
  merge can change a repository's output with no local change. `migrate` now
  warns when a `ref` is not a full commit SHA — an abbreviation included, since
  it names content only while it stays unambiguous — on stderr and again in the
  emitted manifest, on the dependency it is about.

  Not a refusal: `config.Dep` documents a branch or a tag as what an updating
  run resolves, `config.Load` accepts one, and `stele.lock` records the commit
  each run resolved to, so what `migrate` emits is a manifest the rest of the
  tool builds reproducibly from. Refusing here would have `migrate` legislate a
  policy the tool does not hold. The nearby refusals are not counter-examples:
  `#branch=` is refused because buf's fragment is not a ref at all, and an
  absent ref is refused because inventing one is fabrication.

  Not a lint rule either: `internal/lint` checks linked proto descriptors and
  deliberately reaches nothing of the manifest, and every rule ID it carries is
  permanent under [RELEASING.md](../RELEASING.md).

  **What this placement does not reach, stated rather than glossed.** The
  instance that prompted it was a *hand-written* manifest, and `migrate` never
  sees one of those — it warns only on a migration whose source `Makefile`
  pinned a moving ref, which none of the seven remaining repositories does. A
  manifest that acquires a floating ref after migration goes on being silent.
  The honest home for that is a check where the manifest is *read*, which is
  every command rather than one; that is a larger decision than this defect,
  and it is left open here rather than smuggled in with it.

- ~~**#7 `migrate` widens a dependency to its directory name.**~~ Fixed. A buf
  input selects out of the vendored tree; a stele input selects out of the
  producing repository, and the translation used to carry the input's `paths:`
  across as written. Where the `buf export` that filled the tree named a
  `--path` narrower than the directory the input names, the two selections agree
  on disk and disagree everywhere else: `paths: [example/place]` over a tree
  holding only `example/place/v1` selected exactly `v1`, and the same line
  against the place repository also selects `example/place/events/v1`.

  Measured on a fleet migration of 14 repositories: it hit 3 of 11 dependencies
  across two services, and the person migrating narrowed each by hand. The count
  understates it. The failure mode is *extra* generated packages, and extra
  packages compile — nothing errors, nothing is missing, the build is green, and
  in one case an extra package registered a descriptor path that panics when
  imported alongside another library. A tool whose report is trusted for what it
  does not say must not produce a defect that produces no output of its own.

  The source of truth for the narrowing is now the `--path` of the export that
  filled the tree, intersected with the input path: what was actually copied.
  Not the directory name, which is what over-selected; and not the import
  closure, which is the right answer for the *dependency set* and the wrong one
  for a generate input — a generate input exists to produce code for files
  nothing imports yet. The two evidences answer different questions and are both
  kept: #5's import walk decides which dependencies are real and what each is
  read for, and the export's `--path` bounds what any input over that tree can
  have selected. Where the export named no `--path` at all, the tree really does
  hold the producer's whole module and nothing is narrowed.

  This also reaches the widest form of the same mistake, which the fleet did not
  happen to contain: an input naming the vendored tree and no `paths:` at all
  used to become a dependency input with no narrowing — the producer's whole
  module — where in buf it selected exactly what had been exported into that
  tree.

  What it does not reach: a vendored tree that is *wider* than what is imported
  is left to #5's import walk, which drops the unread dependency and reports it
  as drift; and a tree filled by something other than a `buf export` the Makefile
  reader can see is still, as before, reported rather than guessed at.

- ~~**#8 `deps[].paths` was read by one command out of three.**~~ Fixed. The
  narrowing on a dependency was consumed by `export`; resolution ignored it and
  pulled the producer's whole module into the graph, and `generate` narrowed by
  the *input's* paths instead. The field a reader trusts was not the field that
  decided.

  The cost was not that the manifest read wrong. It was that files nobody
  declared were judged: the one-import-path-one-content rule ran over them, and
  the drift report named them. That is how one repository's stale vendored copy
  of a well-known validation contract came into scope for a consumer that had
  narrowed its dependency away from it — and a drift report that names paths a
  consumer deliberately excluded trains people to skim a report whose whole
  value is being read carefully.

  The decision, and the two answers rejected:

  - **Honour the field in resolution.** Taken. The narrowing is a property of
    the manifest, not of one target, so it is identical for every target of
    that manifest and honouring it cannot make one import path mean two files —
    which is the reason a generate input's `paths:` must never reach the graph,
    and it does not apply here.
  - **Drop `paths:` from `deps` and let selection live only where it is
    honoured.** Rejected. `export` needs it, every hand-written manifest in the
    measured fleet wrote it, and `migrate` now derives it from the imports. A
    field people reach for naturally and a tool emits by itself is not one to
    delete; the defect was that it did too little, not that it existed.
  - **Keep it and document that it does not affect resolution.** Rejected,
    though it is the cheapest. It writes the inconsistency down instead of
    removing it, and the thing being documented is invisible at the point of
    use: nothing in a manifest, a report or an error would ever remind a reader
    that this narrowing is decorative. It also does not address the measured
    harm at all — the conflict rule and the drift report would go on judging
    files the manifest excluded, so the note would explain the incident rather
    than prevent it. Documentation is the right answer for a limit that cannot
    be removed; this one could be.

  **What happens to an import that leaves the narrowing.** A file inside the
  narrowed subtree may import a sibling outside it. The sibling is offered
  anyway: narrowing says what was asked for, not what those files are allowed
  to need. The alternative — an error naming the import and the narrowing that
  excluded it — makes the correctness of a consumer's manifest depend on the
  producer's internal import structure, which moves under them; one upstream
  commit would break every narrowed consumer, and their only recovery would be
  to widen the narrowing to include a file they do not want to generate from,
  re-acquiring exactly the extra packages #7 was about. It would also give
  `paths:` a second meaning — a wall as well as a selection — where everywhere
  else in the tool it selects output and lets imports follow.

  **What this does not reach**, stated rather than glossed. `paths:` are
  relative to the requested module, so they cannot exclude a producer's *other*
  module roots; those still enter through the `buf.yaml` fallback, claimed or
  not, and are still judged. Reachability is decided by scanning import
  statements rather than by linking, because the graph is what the compiler is
  built from and cannot be asked what belongs in it; where the two could
  disagree, the compiler reports the missing file by name.

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

~~Concurrent cache access is tested. Interrupted downloads, a cold cache with no
network and a full disk are not. The cache is content-addressed and written
atomically, so the design intends these to be safe. Intent is not evidence.~~
Done, and it found two defects.

The design's claim held where it was made: no interrupted clone, cancelled fetch,
truncated download or interrupted `go install` was ever able to publish an entry a
later run would trust. What it did not cover is what the two defects were.

- **A partial entry is never reachable.** A clone is interrupted while git is
  running, by cancelling the context once the scratch directory has content, and
  the assertions are the two that matter: no directory that looks like a published
  entry exists, and the next run succeeds completely. The case a cancelled context
  cannot produce — the process itself killed, so no cleanup runs at all — is built
  by hand, because a `SIGKILL` leaves nothing to ask: a half-populated scratch
  directory is placed beside its destination and the next fetch must ignore it.
  The same is asserted for a plugin download that is truncated by the server, one
  cancelled mid-body, and an install interrupted while the toolchain is running.
- **The cache was not the identity it claimed to be.** A plugin download was keyed
  by the archive's sha256 alone, so two binaries published in one release archive
  shared an entry, and the second one extracted made the first fail verification
  for ever after — with a message telling the reader to delete a directory that the
  next run would break again. An entry is now identified by the digest and the
  member taken from it.
- **The lock did not survive a half-write.** It was written with a plain
  overwriting write, so a process killed mid-write — or a disk with no room left —
  truncated the one record of what a pinned build consumed. The dangerous
  truncation is not the one that fails to parse: YAML cut on an entry boundary
  loads cleanly as a lock pinning fewer dependencies than the build that wrote it.
  It is written through a temporary file and renamed now, and a failed `--update`
  leaves the previous pins exactly as they were.
- **A cold cache with no network says what to do.** A raw git error is not an
  answer, and these messages are read in CI logs by somebody who did not write the
  manifest. The failure is classified from git's own words: a refused credential,
  an address serving no repository, and a host that cannot be reached have three
  different recoveries and now read as three different problems. Each names the
  repository and the ref, quotes what git said rather than replacing it, and says
  that the first use of a dependency at a commit is the only step needing the
  network at all. `ErrUnreachableSHA` is checked through the real path — a lock
  pinning a commit no repository holds, fetched by the real fetcher — rather than
  through a double.
- **Concurrency beyond the one existing test.** Two refs of one repository fetched
  side by side; a reader polling a cold entry while another run populates it; and
  several *processes* — not goroutines — sharing one cache root, both installing
  one plugin and downloading one pinned binary. Between processes is the shape the
  cache is built for, a CI runner with several jobs on one `HOME`, and it is the
  honest one: stele has no goroutines at all.

What is **not** honestly covered, and why. A genuine `ENOSPC` needs a filesystem of
its own and the privileges to mount one, so it is not simulated: the write is made
to fail at the same syscall for the neighbouring reason, a cache directory that
cannot be written to. That refuses a write rather than truncating one, which is the
weaker half of the question. The stronger half — a *partial* write that must not
become a cache hit — is covered for real on the fetch side, where a clone is
interrupted with its tree half laid out on disk. There is no test of a disk filling
during archive extraction; extraction happens entirely in memory before anything is
written, which is why the gap is narrow, but it is a gap.

## Milestone 6 — lint

~~Deliberately last, and worth stating why the order is not laziness: in the fleet
this tool was built for, contract linting **does not run at all** — not in CI, not
on merge. There is no protection to preserve here, only protection to introduce. A
lint that lands before the generation half is trustworthy would be a second thing
to maintain and a first thing to switch off.~~ First slice landed: the engine, the
rule interface, eight rules and `stele lint`.

The order argument is also the design argument. Because there is nothing to
preserve, the only failure mode that matters is a lint that gets switched off, and
every decision below is made against that.

### What landed

- **The rule interface, designed as the out-of-tree one.** A rule is handed one
  linked file and returns findings. Nothing in it reaches the filesystem, the
  manifest, the configuration or the process, because none of those survive a
  process boundary — what crosses one is a set of descriptors and a list of
  findings, which is exactly what `Check` takes and returns. The host that landed
  in the second slice is a *transport* for this interface, not a second one: it
  returns `rule.Rule` values the engine cannot tell from a built-in.
- **Rule ids are a public contract, and namespaced from the first release.** An id
  is `namespace/name`; built-ins live in the reserved `stele` namespace. Under
  `RELEASING.md` renaming an id is breaking, and retrofitting a namespace later
  would be exactly that rename — one that does not fail loudly but silently stops
  matching the ignore list that named it. An id no loaded rule carries is an error
  naming it, never a line that does nothing.
- **Severity is configuration, not a property of the rule.** `error`, `warning`,
  `off`, per rule, in `stele.yaml`. This is the adoption mechanism, and it is
  deliberately in a file reviewed with the contracts rather than in
  `allow_failure: true` on a CI job that is invisible from here.
- **`stele lint`,** checking only the modules this repository owns. Findings
  render as `path:line:col: severity: rule: message` with what to do about it
  indented beneath — the standard milestone 5's failure messages were held to.
- **A run that checked no files is an error**, not a clean run. An ignore list
  that grew until it covered everything, and a module path that no longer holds
  protos, are silent otherwise: the build stays green and the protection is gone.

### The second slice: rules from outside this repository

- **`lint.plugins`, pinned on the terms code generation plugins are.** The same
  four tiers — module and version, per-platform downloads with digests, an
  explicit path, a bare name on `PATH` — in the same words, with the same
  refusals, enforced by *the same code*: the part of a plugin declaration that
  says where a binary comes from is one type both kinds hand their fields to.
  Two vocabularies for one question would have been two sets of mistakes, and
  one kind of plugin would have ended up pinned more carefully than the other
  for no reason anybody could state. A rule that is not pinned can change what
  it says about an unchanged repository overnight.
- **A host that is a transport, not a second interface.** Length-prefixed JSON
  frames on a subprocess's stdin and stdout, one process per plugin, one
  question at a time. What the engine receives are `rule.Rule` values.
- **Every way a rule can fail is named, and none of them is silence.** Missing,
  dead halfway, hanging, answering with rubbish, announcing a rule id that is
  malformed or reserved, returning a finding with no fix: each is reported
  naming the rule, the plugin and the file, and each fails the run whatever
  severity that rule was configured at. Severity says what a *finding* costs,
  and a rule that never reached a finding has said nothing about this
  repository. `stele lint`'s own failure message was changed here too: it
  counted findings, so a run that failed only because a rule crashed said "0
  finding(s) at severity error; fix them".
- **Two decisions about rule identity, made as refusals.** The `stele` namespace
  is reserved, so a third-party rule cannot claim a built-in's configuration or
  be silently replaced by a built-in of the same name later. Two plugins
  claiming one id are refused naming both, rather than resolved by declaration
  order — any order would make the meaning of a manifest depend on it, and the
  loser's `severity: warning` line would read as exempting a rule that is still
  failing the build.
- **The proof is a rule outside this module.** `internal/lint/host/testdata/examplerule`
  is its own Go module, depends on the published `rule` package and nothing
  under `internal/`, is built from source in a temporary directory — never
  downloaded — and finds a real thing: an unresolved TODO in a comment that
  every consumer generates code from. Writing it is what added the `error` to
  `Check`: across a process boundary a rule can crash, hang or answer with
  rubbish, and a signature that could only return findings would report every
  one of those as a rule that found nothing.

### What the first consumer found, and what it cost to fix

Writing the example rule is what turned three gaps in the published interface
from opinions into observations, and all three were closed while `rule` was
still unreleased — the only window in which changing it is free:

- **A position had no way back to a descriptor.** The rule walked the source
  path with field numbers in its own source and covered two kinds of
  declaration. `File.DescriptorAt` and `File.Comments` cover nine, and the
  example rule got shorter rather than longer, which is the only evidence that
  an interface addition was the right one.
- **A position's meaning was unstated.** A test asserted the comment's line and
  got the declaration's. `Finding.Pos` now says it is the declaration and why,
  and `Comment.LeadingPos` gives the other position to the rule that wants it.
- **The bare-`PATH` tier was accepted for rule plugins.** It exists because the
  vocabulary is shared with code generation plugins on purpose. It is now
  refused without `unpinned: true`, and an unpinned plugin is named in every
  report and every listing: a generator that changes is visible in a diff, and a
  rule that changes is visible nowhere.

### The rules, measured before they were chosen

Each rule was run over 35 hand-written `.proto` files from fourteen services of
the fleet described above — the same fleet where no lint runs today — before it
was included. A rule that reddens a fleet on day one gets disabled and never
returns, so the counts are the argument, not a footnote to it:

| rule | findings | files affected |
| --- | --- | --- |
| `stele/syntax_declared` | 0 | 0/35 |
| `stele/package_declared` | 0 | 0/35 |
| `stele/package_lower_snake_case` | 0 | 0/35 |
| `stele/package_version_suffix` | 0 | 0/35 |
| `stele/directory_matches_package` | 0 | 0/35 |
| `stele/enum_zero_value_unspecified` | 0 | 0/35 |
| `stele/enum_value_prefix` | 40 | 1/35 |
| `stele/enum_value_upper_snake_case` | 0 | 0/35 |

Forty findings, all of one rule, all in one file: one service wrote its enums
without the prefix and the other thirteen did not. That is a rule a fleet can
switch on — one file to fix, or one `severity: warning` line while it is fixed.

The measurement corpus is not committed and cannot be: it is an organisation's
private repositories, and `internal/hygiene` forbids organisation-specific
identifiers with no exemptions. What is committed is the neutral fixtures, and
every rule is proved twice — that it fires on a file that breaks it, and,
separately, that it says nothing about a file that keeps it. The second half is
the one usually skipped, and a rule that fires on everything passes the first.

### Measured and rejected

These were implemented, measured against the same fleet, and left out. They are
recorded because "we did not think of it" and "we measured it and it was wrong"
are different answers.

- **`rpc_response_named_for_method`** (a method `Foo` returns `FooResponse`) —
  2 findings, both correct code. Both are server-streaming methods returning a
  stream of domain events, which is what a streaming method should return. The
  rule is right most of the time, and right most of the time is the property that
  teaches people to ignore output.
- **`rpc_request_named_for_method`**, **`rpc_types_not_shared`** — 0 findings, but
  the same objection reaching further: two methods legitimately share an empty
  request, and a rule cannot tell that from two methods that will need to evolve
  apart.
- **Message, field, service, rpc and oneof casing** — 0 findings, mechanical, and
  left out anyway. They protect nothing the tool can name, and every id shipped is
  one that can never be renamed. They are the obvious first batch to add if a
  consumer wants them.

### The AIP direction: the inventory, then the first five rules

The next direction is AIP breadth. The first slice of it deliberately added no
rule. AIP is an external corpus of 118 documents that is edited upstream, and a
hand-written list of "the AIPs we implement" is the failure this repository has
already had four times — an enumeration typed by a person that drifts from what
it enumerates and reads as complete while it drifts. So the list is derived:
`internal/aip/index.tsv` is written from a clone of upstream, `ledger.yaml`
records a decision per AIP, and a hermetic test fails when the two disagree in
either direction.

What the measurement found, in full in [AIP.md](AIP.md):

- **118 AIPs**, 101 approved. 45 are vendor sets (Firebase, Workspace, gcloud,
  credentials, client-library generators); **73 are general**.
- Of the 73, **29 are statically decidable** from a `FileDescriptorProto` in
  whole or in part, **32 are not** — AIP-121 and AIP-122 are the honest examples:
  whether a message set is a resource model, and whether a string is a resource
  name, are not in a descriptor. AIP-180 is decidable but not from one file,
  which is breaking-change detection and stays separate work.
- Against the same 35-file fleet the eight built-ins were measured on, the four
  broadest candidates — `field_behavior` on every field, a comment on every
  declaration, `google.api.http` on every method, `google.api.resource` on every
  message — **fire on every file**, and the `_at`/`_time` split of AIP-142 fires
  111 times across 19 of 35. The affordable ones are the shapes: 8 findings for
  delete, 8 and 15 for pagination, 0 for create, update and the batch methods.

Two decisions followed from those numbers. AIP rules are `aip/<number>_<name>`,
not `aip/<number>`, because one AIP implies several checks and an id can never
be renamed — that one stands. The second, that they be an opt-in profile rather
than built-ins, was **overruled by the owner** and did not ship; AIP.md §10
records both the decision and why the reasoning behind it did not survive. The
short version is that the objection to a profile is this roadmap's own objection
to rules that default to off: it ships dead.

#### What shipped: five rules, on by default, at warning, rolled up

Five rules across three AIPs — AIP-135, AIP-142, and AIP-158's three fields —
each **re-measured with the rule itself**, which is the standard this document
sets and which the estimates above did not meet. Two of the estimates were
wrong, in opposite directions: AIP-158's `page_size` was estimated at 8 and
measures 14; AIP-135 was estimated at 8 and measures 7.

| rule | findings | files |
| --- | --- | --- |
| `aip/135_delete_returns_empty` | 7 | 4/59 |
| `aip/142_timestamp_field_time_suffix` | 111 | 19/59 |
| `aip/158_list_request_page_size` | 14 | 8/59 |
| `aip/158_list_request_page_token` | 15 | 8/59 |
| `aip/158_list_response_next_page_token` | 15 | 8/59 |

111 warnings printed one to a line is a wall a reader learns to scroll past,
which is the same failure as a profile nobody enables. So a rule reporting more
than five **warnings** prints one line — count, files, and the command that
expands it — and errors are never rolled up. What decides is severity and
volume, not the namespace, so a `stele/` rule lowered to `warning` is rolled up
on the same terms.

Two further rules were written, measured at 0 findings and not shipped. A rule
id is permanent and 0 findings is not evidence for one. The four broadest
candidates in the table above did not ship either, and volume is not the whole
reason: a rule that cannot tell an unannotated file from a descriptor set
assembled without the import would report the same count on every run of every
repository, and a line that never moves teaches nothing.

**The baseline is now more pressing, not less.** A roll-up makes a large
standing count comfortable to live with, and nothing here separates the 111
existing `_at` fields from the 112th written tomorrow. It landed next; see
below.

### Not in this slice, and said plainly rather than implied

- **The breadth of AIP coverage.** Thirteen rules is thirteen rules, over 3 of
  the 29 general AIPs a descriptor can decide. The inventory that says which is
  which is in `internal/aip`; see above.
- ~~**Breaking-change detection.**~~ First slice landed; see below.
- ~~**A baseline.**~~ Landed; see below.
- **Suppression at the source.** Deliberate, not an oversight. A `// stele:ignore`
  comment lives in the producer's file and travels to every consumer that vendors
  it, carrying one repository's decision into repositories that never made it.
  The manifest is where an exemption is auditable by the people it binds.

#### What shipped: breaking-change detection (first slice)

`stele breaking` compares the working revision against the correct previous
one — the merge-base with the base branch on a topic branch, the base
branch's own first parent when already on it, never the base tip, because
comparing against a moving tip fails an unrelated branch for a neighbour's
change that already landed. It reports two categories: wire breakage
(already-serialised bytes stop decoding correctly) and source breakage (a
consumer's generated code stops compiling), 19 rule ids in the reserved
`break/` namespace, and it also compares the resolved closure reachable from
this repository's owned modules — a producer's own files can be
byte-identical while a bumped dependency pin still breaks their consumers.

**It is report-only, and says so in every invocation.** `stele breaking`
always exits zero when it finds something to report; only a failure to
*compare* — a shallow clone, an unreadable manifest, a revision that cannot
be fetched — fails the run. There is no way yet to permit a legitimate
breaking change, and a command that failed a build with nothing to accept a
finding with would leave a repository one option: deleting the CI job.

**The blind zone is named in every report's footer, not left to be
discovered:** `json_name` renames, `int32` widening to `int64` under
`protojson` (a 64-bit integer serialises as a string), and `google.api.http`
changes all pass green. Two narrower gaps are named beside it: a direct
multi-commit push to the base branch is compared only against the last
commit of the push, and a human's merge of the base into a topic branch
leaves the merge commit itself uncompared until the branch lands.

**What is still missing, and it is what stands between this and a check a
fleet can turn on:**

- **The valve.** `breaking.allow` — a permission for one named change, with a
  required reason — and `breaking.moves` — a rename map applied before the
  comparison, so a package rename does not read as removing everything inside
  it — do not exist yet. Without them the only recovery from a legitimate
  breaking change is to not run the check, which is the same failure mode
  `severity: "off"` exists to avoid for lint. The non-zero exit ships together
  with the valve, in one announced change, because failing a build with
  nothing to accept a finding with is the thing this slice deliberately does
  not do.
- **The evidence.** Two kinds are still owed before a fleet enables the check
  by default: a shadow period, running in CI in report-only mode with every
  firing classified by hand as true or false, because "did it fire" is not
  the question that matters — "was it right" is; and a measurement over open,
  unmerged work, not just merged history, because a repository's history
  arrives clean by construction and says nothing about a three-week-old
  branch with reviewed removals still on it, which is where day-one findings
  actually come from.

#### What shipped: the baseline

`stele lint --update-baseline` writes `stele.baseline`, a generated file
committed and read in review beside `stele.lock`. The findings it names cost
nothing; every other finding costs what its severity says.

**The identity is the hard part, and the obvious answer is wrong.** (file,
rule, line) goes stale on an edit above it, so the file is regenerated
reflexively — and a file regenerated without being read launders the new
findings in with the old on every regeneration, which is worse than not having
one. An entry is (import path, rule, **subject**, count), where the subject is
the full name of the declaration the finding is about. It survives
reformatting, reordering and insertion, and stops being the same name exactly
when the declaration stops being the same declaration.

The engine derives the subject from the position rather than the rule stating
it, and that is not a convenience. `rule.WireFinding` — what crosses the plugin
boundary — carries a line, a column, a message and a fix, deliberately. An
identity a hosted rule could not produce would work for built-in rules only,
and this repository's whole claim about the rule interface is that it does not
matter which side of a process boundary a rule is on.

**A stale entry is reported and never fatal.** Keeping it silently is how a
record of debt rots into a standing permission; failing on it reddens the build
of whoever fixed the finding, and a repository learns quickly not to fix
things. `--update-baseline` drops it, which is the only way the file shrinks.

**A baselined finding is still printed**, marked and keeping the severity it
would otherwise have cost, and the summary line names the count the file is
holding — because the roll-up's own failure mode is a comfortable line with an
unread number behind it, and a baseline that hid its contents would be that
failure with a file attached.

**It is not `ignore`.** `ignore` is prospective and unbounded and hand-written
in the manifest, where it is reviewed as intent. A baseline is retrospective,
exhaustive and generated. An exemption nobody intends to revisit belongs in
`stele.yaml`; debt somebody intends to pay belongs in the baseline.

It is **per repository and not per rule**. A per-rule switch would be a second
severity axis, and severity is already the axis that decides which rules are
worth holding: a rule a repository intends to fix is held at `error` and
baselined, a rule it has not decided about is a `warning`, and a rule it will
never apply is `off` or ignored.

Measured on the repository in the fleet that made the argument, which held 40
`stele/enum_value_prefix` findings at `severity: warning` because renaming public enum values would break four
consumers at once: with a baseline the rule went back to `error`, the run
passed at `0 errors, 0 warnings, 54 findings held by stele.baseline`, twenty
lines inserted above every finding changed nothing, one new unprefixed value
failed the run by name, and fixing one of the forty printed a stale line and
still passed.

### Arguments against this, recorded

**A baseline can be regenerated.** Nothing stops somebody running
`--update-baseline` to make a red build green, which is `allow_failure: true`
with more steps. What is different is that it lands in a diff: the file is
generated, committed and reviewed, one line per finding, and a commit adding
forty lines to it is a commit that says what it is doing. That is the same
protection the lock has and it is not a stronger one. A `--check` mode that
failed when the baseline is not what the run would write was considered and not
built: it would fail on exactly the case above — a fixed finding — which is the
thing this design refuses to punish.

**A subject is not a perfect identity.** Two findings of one rule about one
declaration are separated by a count and not individually, so fixing one of two
and introducing another the same day is invisible. That case is rare enough to
be worth the file staying readable; the alternative, an ordinal, moves for the
same reason a line number does.

**A rule that reworded its message keeps matching.** Deliberate. An entry that
stopped matching because somebody improved a sentence would be a new build
failure with no cause, and prose is not a contract this tool keeps.

### An argument against part of this, recorded

Every built-in rule runs unless configured otherwise. That is right — a rule
that shipped switched off would ship dead, and the whole point is protection that
arrives. But it means **adding a built-in rule can turn a green build red**, which
under `RELEASING.md` is a breaking change: "a command's exit status changes for an
input that already worked". So it bumps the field that signals "read this before
upgrading", and the changelog names the rule. The alternative — new rules default
to `warning` for a release — was rejected as a rule that means two things
depending on when you adopted it.

## Not planned

- **A registry.** The absence of one is the point.
- **Full compatibility with the reference tool's configuration.** A measured subset,
  and an error naming anything outside it.
- **Writing code generation plugins.** Existing ones over the standard protocol.
