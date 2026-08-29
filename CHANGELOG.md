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

### Generated output

- Nothing. `stele breaking` reads descriptors; it writes nothing `generate` or
  `export` produce.

### Added

- **`stele breaking`.** Compares the working revision against the correct
  previous one — the merge-base with `--base BRANCH` on a topic branch, the
  first parent when already on the base branch — and reports wire and source
  breakages in the proto contracts this repository owns, plus changes in the
  dependency closure it re-exports. 20 rule ids, in a new reserved `break/`
  namespace alongside `stele/` and `aip/`.

  ```
  $ stele breaking --base master
  breaking changes compared against b43992ea4f37bdc0116ac912f543ac3d0e4d6734 (merge-base with master):

  example/v1/order.proto:5:3: error: break/field_type_changed: (category: source) example.v1.Order.total: field example.v1.Order.total changed type from int32 to int64
      revert the type, or add a new field instead of changing an existing one's type

  report-only: this run always exits zero
  blind zone: this engine does not check json_name renames, int32 widening to int64 under protojson, or google.api.http changes
  ```

  **This release is report-only: it always exits zero when it finds
  something.** There is no mechanism yet to permit a breaking change
  deliberately, and a command that failed a build with no way to accept a
  finding would leave a repository one option — deleting the CI job. A
  failure to *compare* is different and still fails the run: a shallow clone,
  an unreadable manifest, a revision that cannot be fetched. The non-zero
  exit arrives in a later release together with the valve that permits a
  named change, as one announced change, not as a surprise under an
  unrelated entry.

  `--against REF` compares directly against `REF`, with no merge-base. It is
  documented as unsuitable for a CI default: `--against origin/master` is
  exactly the neighbour-blaming comparison a merge-base exists to avoid, one
  flag away from the wrong thing. `--base` has no default yet and is
  required unless `--against` is given; a later release moves it into the
  manifest as `breaking.base`.

  **Versioning.** A new command is `MINOR` on the full scale in
  [RELEASING.md](RELEASING.md); while the major version is 0 that bumps
  `PATCH`, not `MINOR` — this ships as `v0.3.1`.

- **`break/oneof_renamed`.** A oneof's name is not on the wire — only each
  member's `oneof_index` is — so renaming `oneof pick {...}` to
  `oneof choose {...}` with its members untouched used to report nothing at
  all: no descriptor index shifts, and this engine indexes declarations, not
  container names. The generated wrapper type (`M_Pick` → `M_Choose`) and
  getter (`GetPick`) are renamed regardless, so every Go consumer using
  either stops compiling. This closes a case that was previously named in
  the blind-zone footer as "a oneof renamed with its members intact"; it no
  longer is. Category: Source. Pairs a removed oneof with an added one in
  the same message when, and only when, their member field-number sets are
  identical — a field belongs to at most one oneof, so that set is disjoint
  from every sibling oneof's in the same message, making the match exact
  rather than a shape guess. A oneof whose member set also changed is left
  to `break/field_oneof_changed`, per field.

### Reports and messages

- **Every `stele breaking` report ends with the blind zone**, naming three
  breakages that pass green so that "no breaking changes" is never read as
  more safety than the two categories checked can promise: a `json_name`
  rename, `int32` widened to `int64` (wire-compatible, but `protojson`
  serialises a 64-bit integer as a string), and a changed `google.api.http`
  annotation.

### Internal

- **`internal/compile` gains a `WithoutSourceInfo` option.** The previous
  revision in a comparison never supplies a position, so it does not need
  source info; this narrows the compile the comparison's older side pays for.
  Nothing outside `stele breaking` sets it, and no other command's output
  changes.

## [v0.3.0] — 2026-08-28

### Generated output

- Nothing. Everything below is the lint half of the tool: five new rules, a
  roll-up of their output, a baseline file, and a manifest parser that stops
  refusing an `aip/` rule id. None of them is read by `generate` or `export`.

  That is measured rather than claimed:

  - The parity corpus is byte-identical, and the release is gated on it —
    `generate` and `export` both, through the same composite action CI runs on
    every change.
  - A repository from the fleet this tool was built for was generated twice,
    once with `v0.2.0` and once with this release, from the same manifest and
    the same lock: the generated tree and `stele.lock` were byte-identical, file
    for file.

  Lint output does change, and says so under *Refused input* below. What does
  not change is the exit status: the five rules ship at `warning`, so a
  repository that linted green stays green. Measured over the fleet's fourteen
  repositories — thirteen services and an acceptance suite — every one of them
  reports `0 errors` under this release, exactly as under `v0.2.0`, and the
  warning counts are the new lines they will read.

### Refused input

- **Five lint rules are added, they run in every repository, and every lint
  run's output changes.** This is a breaking change under
  [RELEASING.md](RELEASING.md) and it bumps the minor, even though **no build
  that passed will now fail**: the rules are warnings, the exit status is
  unchanged, and the policy is written around the bytes this tool writes rather
  than around its exit status alone. A consumer reviewing a lint log will see
  lines they did not ask for, and the recovery — set the severity, or ignore
  the paths — is the same one a new built-in rule has always had.

  The rules implement the [API Improvement Proposals](https://google.aip.dev)
  and live in a new `aip/` namespace, which is reserved alongside `stele/`: a
  rule plugin from outside this repository can no longer serve a rule id
  beginning `aip/`. A finding of a rule the manifest says nothing about now
  costs an **error** in `stele/` and a **warning** in `aip/`.

  | id | what it requires | measured fleet findings |
  | --- | --- | --- |
  | `aip/135_delete_returns_empty` | a method named `DeleteX` returns `google.protobuf.Empty`, the soft-deleted `X`, or an `Operation` | 7, in 4 of 59 files |
  | `aip/142_timestamp_field_time_suffix` | a `google.protobuf.Timestamp` field is named `*_time`, or `*_times` when repeated | 111, in 19 files |
  | `aip/158_list_request_page_size` | the request of a `ListX` method carries an `int32 page_size` | 14, in 8 files |
  | `aip/158_list_request_page_token` | the request of a `ListX` method carries a `string page_token` | 15, in 8 files |
  | `aip/158_list_response_next_page_token` | the response of a `ListX` method carries a `string next_page_token` | 15, in 8 files |

  Each count was measured with the rule itself over a fleet of 59 hand-written
  proto files, not estimated. Two rules were written, measured at zero findings
  and **not** shipped, because a rule id is permanent and zero findings is not
  evidence for one. Why these five and not the broader checks the design
  document lists is in [docs/AIP.md](docs/AIP.md) §10.

- **A rule id may now begin with a digit after the namespace.** `aip/158_…`
  needs it. Nothing that was accepted before is refused.

### Fixed

- **`lint.rules` in `stele.yaml` accepts an `aip/` rule id.** It did not. The
  manifest parser carried its own copy of the rule-id spelling and the copy did
  not move when the published rule was widened to let a name lead with a digit,
  so every `aip/` id was refused with "must start with a lower-case letter" —
  which meant the five rules shipping at `warning` above could not be raised to
  `error`, ignored on a path, or switched off. The published schema had drifted
  the same way, so an editor reddened the line too. Both now defer to
  `rule.CheckID`. Nothing that was accepted before is refused.

### Added

- **A rule reporting more than five warnings prints one line instead of one
  line per finding.** The line carries the count, how many files it spans, and
  the command that prints the detail:

  ```
  stele: aip/142_timestamp_field_time_suffix: 111 warnings in 19 files; see `stele lint --rule aip/142_timestamp_field_time_suffix`
  ```

  **Errors are never rolled up.** What decides is severity and volume, not the
  namespace: a `stele/` rule lowered to `warning` is rolled up too, and an
  `aip/` rule raised to `error` prints in full. No count, exit status or
  configuration changes — the run's summary line still counts every warning,
  including the rolled-up ones.

- **`stele lint --rule ID`** narrows a run to one rule and prints every finding
  it makes. It is the command a rolled-up line names. Repeatable. A rule id
  nothing carries is refused rather than silently checking nothing, and a rule
  the manifest switches `off` says so rather than printing an empty report that
  reads as a clean one.

- **`stele lint --all-findings`** prints every finding of every rule.

- **A baseline: `stele.baseline`, and `stele lint --update-baseline`.** This is
  what `severity: warning` does not give. A repository with 111 fields named
  `_at` gets the same line about the 112th, written tomorrow, and the roll-up
  above made that comfortable rather than better. A baseline holds what is
  already there so that a run fails on what is not.

  ```
  stele lint --update-baseline    # once, when adopting a rule
  git add stele.baseline          # generated, committed, read in review
  ```

  It is **not written by an ordinary run** and it is not mandatory. Nothing
  changes for a repository that has no `stele.baseline`: the same findings, the
  same summary line, the same exit status, byte for byte.

  - **An entry is a file, a rule and a declaration — not a line.**
    `example/v1/order.proto`, `stele/enum_value_prefix`, `example.v1.PLACED`.
    Inserting a line above a finding moves every finding below it, so a
    baseline keyed on position goes stale on an edit that changed nothing it is
    about; one people regenerate reflexively launders new findings in on every
    regeneration. A count is carried for the case a subject cannot separate —
    two findings of one rule about the file as a whole.
  - **A baselined finding is still printed**, marked `(baselined)` and keeping
    the severity it would otherwise have cost, and the summary line says how
    many the file is holding. A suppression nobody can see is not debt, it is a
    decision nobody remembers making.
  - **An entry nothing finds any more is reported and never fails the run.**
    Fixing a finding must not redden the build of whoever fixed it. Re-derive
    to drop it; the file is meant to shrink.
  - **It is not `ignore`, and both are worth having.** `ignore` is prospective
    and unbounded — this rule does not apply to these paths, including the file
    added tomorrow — written by hand where it is reviewed as intent. A baseline
    is retrospective and exhaustive: these exact declarations violate this rule
    today and nothing else does. An exemption nobody intends to revisit belongs
    in `stele.yaml`; debt somebody intends to pay belongs in the baseline.
  - A baseline naming a rule no loaded rule carries stops the run naming it, as
    `lint.rules` already does. A baseline that cannot be parsed stops the run
    rather than being ignored.

  Measured on the repository in the fleet that made the argument. It holds
  40 `stele/enum_value_prefix` findings at `severity: warning` because renaming
  public enum values would break four consumers at once. With a baseline the
  rule goes back to `error` today — `0 errors, 0 warnings, 54 findings held by
  stele.baseline` — and the 41st unprefixed value fails the run, by name, on
  the line it was written.

  A JSON Schema for the file is published as
  [`schema/stele.baseline.schema.json`](schema/stele.baseline.schema.json) and
  held to the parser by the same corpus the manifest and the lock are.

  **Versioning.** On its own this would be a patch under
  [RELEASING.md](RELEASING.md): a new flag with the previous behaviour as the
  default, a new file that no existing repository has, and byte-identical
  output for every input that already worked — which is `MINOR` on the full
  scale and therefore `PATCH` while the major version is 0. It ships in a minor
  release because the AIP rules above already bump one. It is recorded this way
  rather than folded into them because the two are different promises: the
  rules change what every repository's lint says, and the baseline changes
  nothing until somebody asks for it.

  One caveat that is not covered by "the contract is the bytes stele writes":
  `rule.Finding`, in the published package an out-of-tree rule imports, gains
  two fields. Both are stamped by the engine and neither is a rule's to set, so
  a rule reading findings is unaffected — but a rule constructing one with an
  *unkeyed* struct literal will no longer compile. Keyed literals, which is
  what the documentation and every example use, are unaffected.

### Reports and messages

- **`stele lint --rules` says what a finding of each rule costs** — "fails the
  build" or "warns". The rules no longer all cost the same thing, and the id
  does not say which is which.

### Internal

- **The atomic write the lock file uses moved to `internal/atomicfile`,** now
  that the baseline needs it too. No behaviour changed; a second copy of it
  would have been a second place to get it subtly wrong.

- **The AIP corpus is inventoried mechanically, and every AIP has a recorded
  decision.** `internal/aip` holds a derived snapshot of
  `github.com/aip-dev/google.aip.dev` (118 AIPs at commit `23e176e733`), written
  by `go run ./internal/aip/aipsync` and never by hand, and a ledger saying of
  each AIP whether it is implemented, a candidate, declined or undecidable. A
  hermetic test fails when upstream carries an AIP nothing has classified, or
  when the ledger classifies one upstream no longer carries; a networked CI job
  fails when the snapshot is not what upstream says. Nothing about linting
  changes in that slice: no rule was added and none was altered by it.
  The reasoning, the decidability split (29 of 73 general AIPs decidable in
  whole or part, 32 not) and the fleet measurement are in
  [docs/AIP.md](docs/AIP.md).

## [v0.2.0] — 2026-08-26

### Generated output

- **A generate input that names no paths of its own, over a dependency that
  narrows, now generates from the narrowed set** rather than from the
  producer's whole module. The extra packages that input used to produce are
  gone, which is a diff to review. This is the byte-visible half of the
  `paths:` change described under *Refused input*, and it is recorded here as
  well because a category of change that looks like nothing in a diff of
  features and fixes is the one this section exists for.

  Nothing else here moves bytes. That is measured rather than claimed:

  - The parity corpus is byte-identical, and the release is gated on it.
  - Every manifest whose dependencies declare no `paths:` is unchanged by
    construction — there is no narrowing to honour.
  - The fleet this tool was built for was run twice, once with the commit it
    pins today and once with this release, over `origin/master` of all
    fourteen repositories — thirteen services and an acceptance suite — and
    every one of the fourteen declares `paths:` on its dependencies. The
    generated trees and `stele.lock` were byte-identical in all fourteen: no
    package disappeared, none appeared, no file's contents differed. Those
    manifests give their generate inputs paths of their own, which is the
    shape that is unaffected.

  So the shape that changes is real and is described above, and the corpus it
  was looked for in did not contain it. A repository that lets an input take
  whatever its dependency offers should regenerate and read the diff before
  upgrading.

### Refused input

- **A dependency's `paths:` now decide what enters the resolved graph.** The
  field was consumed by `stele export` and by nothing else: resolution pulled
  the producer's whole module in regardless, and `generate` narrowed by the
  *input's* paths instead. A manifest saying it took one subtree of a producer
  did not mean it, and the difference was not cosmetic — every file that enters
  is judged by the one-import-path-one-content rule and reported as drift when
  it disagrees with its owner, so a producer's stale vendored copy of a
  well-known validation contract could conflict with, or be reported against, a
  consumer that had explicitly narrowed it away.

  Three things a consumer can observe:

  - A file outside a dependency's `paths:` no longer supplies an import path.
    If something in the closure was resolving through it, that import now
    fails, naming the file. It was resolving through a file the manifest did
    not ask for.
  - Drift reports and import conflicts no longer name files nobody asked for. A
    closure that used to fail with a conflict between a producer's vendored
    copy and that contract's owner now resolves, with the owner supplying it.
  - A `paths:` entry that selects no file is now an error naming the entry and
    what the module does supply, instead of being silently ignored. This is the
    same refusal `export` and a generate input already made.

  What narrowing means is what is **offered**, not what is **reachable**: a
  file the narrowing excluded is offered again when a file that survived it
  imports it, transitively. The alternative — an error naming the import and
  the narrowing that excluded it — was rejected, because the import structure
  inside a producer's module is the producer's business and changes without a
  consumer touching anything: one upstream commit adding one import would break
  every narrowed consumer at once, and the only fix available to them would be
  to widen the narrowing to include a file they deliberately do not generate
  from.

  The narrowings of every dependency edge reaching one module of one repository
  are unioned, including edges to a repository the walk has already visited, so
  what is offered still cannot depend on the order `deps` are listed in.

  Generated bytes are unchanged for every manifest whose dependencies have no
  `paths:`, and the parity corpus is byte-identical. Where a dependency does
  narrow and a generate input over it names no paths of its own, that input now
  generates from the narrowed set rather than the producer's whole module — the
  extra packages it used to produce are gone, which is a diff to review.


- **`stele migrate` refuses a `Makefile` whose `define` body holds `buf export`
  or `go install`**, and one whose `define` has no `endef`. A `define` body is
  stored verbatim and means whatever the place it is expanded makes it mean;
  the Makefile reader does not follow expansion, so it used to read a body by
  its own indentation and could recover an invocation that never runs, or lose
  half of one that does, saying nothing either way.
  A body holding neither word is dropped, which loses nothing the reader was
  going to recover from it, so the ordinary uses of `define` still migrate.
  The refusal is keyword-shaped, and that is now written down where the
  reader's other limits are — see *Reports and messages*.

- **A `Makefile` this reader cannot read is refused whichever half reaches it
  first.** `.RECIPEPREFIX` was an error from the export reader and, from the
  plugin reader, an entry in the list of unreadable invocations — an entry
  nobody ever saw, since the run aborted on the other reader's error first.

- **A `lint.plugins` entry that declares no `module`, `downloads` or `path` is
  refused** unless it also says `unpinned: true`. Such an entry is whatever the
  machine's `PATH` resolves the name to. The vocabulary is deliberately shared
  with code generation plugins, and it stays shared — what differs is the
  consequence: a generator that changes writes different generated code into a
  diff somebody reviews, and a rule that changes writes a different judgement,
  which leaves no artefact at all. The error says how to pin it and how to opt
  in. `unpinned: true` beside a declared tier is refused too: the manifest would
  be saying two things that cannot both be true.

- **Two `managed.override` entries for one file option at one path are
  refused**, whether their values agree or not. The reference tool takes the
  last of the two in silence; one of the lines then describes output nothing
  ever has. Overriding one option at *different* paths is what is now allowed,
  and is the point.

- **An override `path` with a leading or trailing slash is refused.** The
  reference tool refuses both spellings, so accepting them would produce a
  manifest that cannot be written back out as a configuration the other tool
  reads.


- A `lint` block that configures nothing, a rule id that is not
  `namespace/name`, a severity outside `error`/`warning`/`off`, two entries
  naming one rule, and an ignore entry that is empty or looks like a glob are
  all refused, naming the field. A glob-looking entry matched literally would be
  an exemption the author believes they have written and does not have.
- A rule plugin that is not named, is named as though it were a rule id, shares
  a name with another, or declares where its binary comes from in two ways at
  once — or in one way, incompletely — is refused naming the field, exactly as a
  code generation plugin is and by the same code.
- A rule plugin claiming a rule id in the reserved `stele` namespace is refused
  naming the plugin and the id, and two plugins claiming one id are refused
  naming both. An id is a public contract that appears in somebody's ignore
  list; there is no declaration order worth inventing to resolve a collision.
- A rule id no loaded rule carries fails the run, naming it and listing what is
  loaded. A typo in an ignore list, or a rule that has been removed, otherwise
  leaves the manifest claiming a protection or an exemption that does not exist.

### Added

- **Releases are signed.** `SHA256SUMS` is signed with cosign, keylessly,
  through the OIDC identity GitHub issues to the release workflow, and
  `SHA256SUMS.sig` and `SHA256SUMS.pem` are published beside it. The digest
  file alone only ever established that a download arrived intact — whoever can
  publish a release can publish matching checksums beside it — and the tool is
  now baked into a CI image other repositories build against, so provenance is
  a question their supply chain is entitled to ask. Verification asks for the
  signer by name; the recipe is in the README and in RELEASING.md.


- **`stele migrate` works out which dependencies a repository actually has, by
  reading its protos.** Before this it copied the `Makefile`'s vendor target,
  which says what somebody exported and not what anything imports — on the
  fleet it was measured against, one `buf export` of one registry module
  brought in 47 files and exactly one was ever imported, and every repository
  of that fleet was handed the same two unresolved entries whether or not it
  read a byte of either. `migrate` now indexes the repository's own `.proto`
  files, follows their imports transitively, subtracts the well-known types the
  compiler carries, and reports what is left file by file.

  For a reader of the output this changes three things. A dependency is
  demanded only when something reads it, and an imported file with no declared
  source is named individually together with the references that could have
  produced it — no git address is invented. Each dependency is emitted with the
  `paths:` narrowing its imports imply, instead of taking a whole module for one
  file. A vendored tree nothing imports is left out of the manifest and
  reported as drift, because a vendored path nothing reads is drift even though
  it is evidence of intent.

  Over seven repositories still on the old toolchain, unresolved entries fell
  from 38 to 29 and all 14 fleet-uniform registry demands were replaced by 6
  reports naming exactly the 31 files those repositories read.

  `migrate.FromDir` reads the working copy; the two-files-of-configuration
  entry point behaves as before, because without the protos there is nothing
  better to go on.
- **`stele migrate` warns when a dependency's `ref` is not a full commit SHA.**
  A branch or a tag is a label somebody else can move, and a producer's options
  land in the descriptor and therefore in your generated bytes, so an upstream
  merge can change your output with no change of yours. The warning is printed
  by the command and repeated in the emitted manifest on the dependency it is
  about; it does not make the migration incomplete, because the manifest
  accepts such a ref and `stele.lock` records the commit each run resolved to.
  An abbreviated SHA warns too: it names content only for as long as it stays
  unambiguous.
- **`rule.File` can get from a source position back to what the author wrote.**
  `DescriptorAt` walks the source path a location carries to the declaration it
  names — message, field, nested type, enum, enum value, oneof, extension,
  service, method — and answers nil rather than guessing when the path names
  something that is not a declaration. `Comments` yields every comment in the
  file with that declaration and a name for it. Before this, a rule about
  comments wrote the walk itself with descriptor-proto field numbers spelled
  out; the one rule written against the interface handled two kinds out of nine
  and named the file for the rest. The example rule's `Check` is now a loop over
  `f.Comments()` and twenty-five lines shorter.
- **`unpinned: true` on a `lint.plugins` entry**, and it is now required for the
  bare-`PATH` tier — see *Refused input*. Every run opens its report with the
  plugin named and the reason, the summary counts it, and `stele lint --rules`
  prints it beneath the rule. An unpinned rule that looked like a pinned one in
  every output was the part worth fixing: a rule can then start disagreeing with
  itself and nothing says so.

- **`go_package_prefix` can be overridden once per path**, instead of once per
  target. A repository whose generated Go lives under more than one import
  prefix could not be expressed at all before; migrating a `buf.gen.yaml` that
  declares several path-scoped overrides produced a manifest this tool then
  refused to load.

  Which of several matching overrides applies to a file was measured against
  the reference tool at 1.48.0 rather than reasoned about: it is the **last**
  matching entry in declaration order, not the most specific one. Reversing
  the order of three overrides — unscoped, `two`, `two/deep` — over the same
  three files changed every answer. Order in `managed.override` is therefore
  meaning, and it is the one place in the manifest where order is. A selector
  matches on element boundaries and may name a file as well as a directory,
  both also measured. The parity corpus exercises it: `repos/managed` declares
  four overrides, with the narrow `parity/order/v1` deliberately ahead of the
  broader `parity/order`, so a reading that preferred the most specific
  selector fails there.

### Fixed

- **`stele migrate` no longer widens a dependency to the directory name an
  input happened to use.** A buf input selects out of the vendored tree; a
  stele input selects out of the producing repository. Where the `buf export`
  that filled the tree named a `--path` narrower than the directory the input
  names, the two agree on disk and disagree everywhere else — an input reading
  `example/place` over a tree holding only `example/place/v1` used to emit a
  manifest that also pulls `example/place/events/v1`. On the fleet this was
  measured against it hit 3 of 11 dependencies across two services, each time
  producing generated packages the repository never had. Those packages
  compile, which is what made this worth naming: the build stayed green and the
  report said nothing.

  The narrowing emitted for a dependency and for its generate input is now the
  input's path intersected with the export's `--path` — what was actually
  copied in. An input that names a vendored tree and no paths at all is
  narrowed the same way, instead of taking the producer's whole module. An
  export that named no `--path` is unchanged: the tree does hold the whole
  module. Migrations of configurations whose exports were not narrowed are
  byte-identical.


- **A repository reached over two transports is walked once, and `export --dep`
  finds its files again.** The lock identifies a dependency by `(git, ref)` and
  addresses are never normalised — a fork at the same path on another host is
  not the same repository — so both requests are still recorded, and a pinned
  run can still answer either. But the *walk* now deduplicates by the cache's
  own notion of identity, host and path, which is already what the cache treats
  as one entry: the same repository cloned over `ssh` from a workstation and
  over `https` from CI is one tree.

  Walking it twice was not only wasted work. Two suppliers of identical bytes
  were settled by address order, so a repository the root manifest declared
  over `ssh` and a producer declared over `https` had every one of its files
  attributed to the producer's request — and `stele export --dep <name>` then
  reported that the module *contains no proto files*. Measured on a twelve-
  dependency manifest of the fleet this tool was built for, with the shape
  present: `export --dep` on the affected dependency wrote 0 files before and 8 after. Resolution
  over the same manifest goes from 23 import roots to 21 and stops re-hashing
  52 proto files, the drift report stops naming the same import path twice
  (27 entries to 26), and the resolved file set — every import path, the bytes
  it resolves to and the root that supplied it — is unchanged.


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

- **The `define` refusal in the `Makefile` reader is documented as
  keyword-shaped.** It looks for the literal words `buf export` and
  `go install`, so a body that reaches the same command through a variable —
  `$(BUF) export` — matches nothing, is dropped like any other body, and
  nothing says so. Narrowing the refusal to a keyword was the right call over
  following Make's expansion, but every other limit of that reader was stated
  in `logicalLines`' doc comment and this one was not. Behaviour is unchanged;
  what changes is that the hole is now findable by someone reading the reader.

- **`stele migrate` reads a backslash run before a `#` in a `Makefile` as GNU
  Make does.** It had a two-character lookahead, which agreed with Make only on
  a single backslash: it read `a\\#b` as `a\\` where Make gives `a\`, and
  `a\\\#b` as `a\\#b` where Make gives `a\#b`. Make collapses each pair of
  backslashes in the run immediately before the hash, and a leftover odd
  backslash escapes the hash; the behaviour was measured against GNU Make 4.4.1
  rather than taken from its documentation, which is vague here. No repository
  in the measured fleet writes such a line, so nothing in it changes; a
  migration report that offered a guess as a reading would have.

- **A finding's position means the declaration, not the comment above it**, and
  it now says so on `Finding.Pos`, on `Position` and in the README. Nothing
  stated it before, so two rules could choose differently and give one
  repository a report whose lines meant different things on different lines. A
  rule that really is reporting a comment takes the new `Comment.LeadingPos` —
  the first line of the comment block — because a decision that leaves the other
  position unreachable is a limitation rather than a decision.

  All of this is in the published `rule` package, which has not been released:
  under [RELEASING.md](RELEASING.md) the same change after a release would be
  breaking, and there was exactly one window in which it was free.

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

  There is no breaking-change detection and no AIP profile in this slice, and
  the README says so rather than implying more is covered.
- **Rules from outside this repository.** `lint.plugins` in `stele.yaml`
  declares a binary that serves rules; the rules it serves are loaded beside the
  built-ins, see the same linked files, sort into the same report, and are
  demoted or switched off by the same `lint.rules` block. A rule is written
  against the published `rule` package — the one package here that is not
  internal — and a `rule.Serve` call is the whole of the plumbing.

  A rule plugin is pinned exactly as a code generation plugin is: `module` plus
  an exact `version`, `downloads` with a `url` and `sha256` per platform, an
  explicit `path`, or a bare name on `PATH`. The same tiers, the same words, the
  same refusals, and the same installer and cache. A rule that is not pinned can
  change what it says about an unchanged repository between two runs.

  A rule that could not run is never reported as a rule that found nothing. A
  plugin that is missing, dies mid-run, hangs, answers with something that is
  not a response, or returns a finding with no fix is reported naming the rule,
  the plugin and the file, and fails the run whatever severity that rule was
  configured at — severity says what a finding costs, and there was no finding.
- **A `lint` block in `stele.yaml`**, and in the schema. It sets what a rule
  costs — `error`, `warning` or `off` — and which import paths no rule, or one
  rule, is applied to. This is the adoption mechanism: a repository that has
  never linted has findings on the first run, and the way to a green build is a
  reviewed field in the manifest rather than `allow_failure: true` in a CI job
  that is invisible from the repository the rules are about. A `warning` does
  not protect new code from the same mistake, and the roadmap says so.
- **`stele lint --rules`**, printing the rules a run here would apply with their
  ids and what each requires. A rule id is a public contract that goes into a
  manifest; a list somebody has to read the source to find is one they guess at.
  It loads the rule plugins the manifest declares and lists what they serve too,
  each naming its plugin: those ids are precisely the ones that cannot be read
  out of this repository's source.


- `stele lint` counted findings when it failed, so a run that failed only
  because a rule could not run said "0 finding(s) at severity error; fix them" —
  sending the reader to hunt a finding that was not there, and implying a
  severity line would silence it. Nothing silences it. The two failures are now
  reported apart, and the one that means "this repository has not been checked"
  says so and says what to do.


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

- **Releases are cut by goreleaser.** It replaced a hand-written loop over four
  platforms, `sha256sum` and `gh release create`; signing is what earned it its
  place, since it has cosign support built in. Nothing a consumer fetches
  changed: the same four raw binaries under the same names, the same
  `SHA256SUMS`, plus the two signature files above.

  Version stamping stays with the Go toolchain. goreleaser's default `-ldflags
  -X` stamping is turned off, for a reason that was measured rather than
  argued: its default flags carry a wall-clock `-X main.date`, the whole
  `-ldflags` string is recorded in the binary's build settings, and two builds
  of one tag twenty seconds apart produced different bytes. With it off, a
  published binary is byte-identical to a plain `go build -trimpath` at the same
  tag — so a release can be re-derived without goreleaser installed. See
  RELEASING.md.

  The reproducibility check now covers all four platforms rather than
  `linux/amd64` alone, and fails if a build leaves the working tree dirty.


- **The reference tool's pin lives in one file.** `test/parity/corpus.yaml` now
  carries `buf_downloads` beside `buf_version`: one `os`/`arch`/`url`/`sha256`
  entry per platform, in exactly the vocabulary a manifest uses to pin a
  plugin, and the harness fetches and verifies it through the same cache and
  the same digest check a pinned plugin goes through. The sha256 used to sit in
  the CI workflow while the version sat in the corpus. That failed closed, but a
  pin split across two files can be half-updated, and only CI read the half that
  named the bytes — a workstation run measured against whatever `buf` the
  machine had. The CI step that installed the tool is gone with it.

- **The translation from a manifest's `downloads` entries to the plugin
  resolver's own type is written once**, as `config.PluginDownloads`. It had
  been written twice, in generation and in lint, and the parity harness would
  have been the third.


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

### Note for a future release

Adding a built-in rule can turn a green build red. Under
[RELEASING.md](RELEASING.md) that is a breaking change — "a command's exit status
changes for an input that already worked" — so it bumps the field that signals
"read this before upgrading", and the entry names the rule.

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
