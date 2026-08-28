# Breaking-change detection

Status: design, second revision. Not implemented.

The first revision of this document was reviewed adversarially and did not
survive. What it got right was the core — compare two compiled revisions, in two
categories — and what it got wrong was almost everything around it: the valve
reproduced the failure the design was built to avoid, the identity of a finding
could not name what it was about, and the acquisition of the base revision was
specified against the wrong cause. The findings are recorded in "What the first
revision got wrong" at the end, because a design that hides its own refuted
version invites the next author to re-derive it.

## What this is for

`stele` generates code from contracts and pins the contracts it consumes. It
cannot yet answer the question every consumer of those contracts has: **did this
change break me?**

Two people ask it:

- The **producer**, changing a contract, asking whether consumers will break.
- The **consumer**, taking a newer revision of a dependency, asking whether
  anything they actually import changed under them.

One comparison engine answers both; only the source of the *previous* revision
differs. This document specifies the producer side and states what the consumer
side inherits.

## What counts as breaking

- **Wire.** Already-serialised bytes stop being read correctly: field numbers,
  incompatible type changes, cardinality, movement between distinct oneofs, enum
  value numbers, and — because in gRPC a name is a path on the wire — renaming or
  removing a package, service or method, changing a method's request or response
  type, or whether it streams.
- **Source.** The bytes survive; a consumer's code stops compiling: field renames
  at a stable number, removals, removal or rename of a message, enum or enum
  value, a changed `go_package`, moving a field into a oneof.

### The blind zone, stated because silence reads as safety

JSON and behavioural compatibility is **deliberately out of scope**, and that
decision costs real coverage. Three breakages pass green, and they are named here
and printed in every report's footer rather than left for a consumer to discover:

- **`json_name` changed.** Same field name, same number. Every `protojson` and
  transcoding consumer breaks.
- **`int32` → `int64`.** Wire-compatible, so this design calls it safe. In
  `protojson`, 64-bit integers serialise as **strings**: `5` becomes `"5"`. A
  strict JSON consumer breaks.
- **`google.api.http` changed.** Path, method or body. Pure behaviour, invisible
  to both categories, and in a fleet with transcoding it is the most likely real
  breakage of the three.

A report that says "no breaking changes" means no breaking changes *in the two
categories defined here*, and says so in those words.

### The type-compatibility matrix is measured, and the oracle is specified

Hand-written enumerations in this project have been wrong four revisions running,
so the matrix is built by a test. The first revision of this document said only
"encode as one, decode as the other" — which measures the wire *type*, not
compatibility, and would have declared `float` and `fixed32` compatible because
both are four bytes and every decode succeeds, while `1.0` reads back as
`1065353216`.

The oracle is therefore stated:

- **Corpus, not a sample value.** Zero, one, minus one, the boundaries of every
  width, values that overflow a narrower type, invalid UTF-8 for `string`/`bytes`.
  A single sample decides nothing; a pair is compatible only if the whole corpus
  agrees.
- **Equality is cross-type and defined.** Numeric types compare numerically —
  which is what rejects `float`/`fixed32`. `string` and `bytes` compare as byte
  sequences, in both directions separately, which is what exposes the asymmetry.
- **Disagreement halts.** Where the measurement and the prose above disagree,
  neither wins automatically: the run fails and a human resolves it against the
  protobuf encoding specification. The first revision pre-committed to believing
  the measurement, which would have enshrined the `float`/`fixed32` answer.

The matrix covers ordered pairs of scalar types. Every other hazard — `map` versus
a repeated entry message, oneof movement, cardinality — is not a pair of scalars
and is covered by paired fixtures instead, one per rule, in both directions.

## Acquiring the previous revision

This is where the first revision was specified against the wrong cause. Depth is
not the problem.

**On a topic branch**, the previous revision is the merge-base of the working
revision and the base branch named by `breaking.base`. Never the tip: comparing
against the tip makes a neighbour's merged change fail an unrelated branch.

**On the base branch itself**, the previous revision is HEAD's **first parent** —
what the branch looked like before the change that just landed. The first revision
of this document used merge-base here too, which on the base branch is HEAD
itself: the comparison was empty, no rule could ever fire, and the only signal the
job could produce over its whole life was a false one. A run on the base branch
must be able to detect what just merged, or it is theatre.

**Under a CI checkout that fabricates a merge commit** — `actions/checkout`
resolves a pull request to a merge of the topic into the base tip — the merge-base
of that HEAD with the base *is* the base tip, silently becoming the comparison
this design rejects. Detected, not tolerated: when HEAD is a merge whose one
parent is the base tip, the working revision is the *other* parent.

**Acquiring the base branch is a step, not an assumption.** A GitLab merge-request
pipeline fetches only the merge-request ref; the base branch is not truncated, it
is **absent**, at any depth. `GIT_DEPTH: 0` does not fix it. The tool fetches the
base ref itself when it is missing, and when it cannot, the three causes are
distinguished and named separately, because they have three different fixes:
a shallow repository (`--is-shallow-repository`), a ref that is absent, and
histories that are genuinely unrelated. "Name the depth" would have sent a person
to fix the wrong thing.

**`--against <ref>`** uses that revision **directly**, with no merge-base. It is
the escape for a deliberate comparison — against a release tag, say — and it is
documented as unsuitable for a CI default, because `--against origin/master` is
exactly the neighbour-blaming comparison, one copy-paste away.

## Compiling the two revisions

**Both sides are compiled, not parsed.** Source compatibility needs resolved
types: a field removed from an imported message is invisible without resolution.

**The previous revision is compiled with its own dependencies** — its own
`stele.yaml` and `stele.lock`, materialised whole. Compiling yesterday's protos
against today's pins either fails outright or attributes another repository's
change to this one.

Three states the first revision did not cover:

- **A manifest with no lock.** Resolving it fresh would resolve each `ref:` to
  today's tip — precisely the thing the paragraph above forbids — and would do it
  silently. The run fails naming the revision instead.
- **A lock pinning a commit that no longer exists.** A dependency that squash-
  merges rewrites the commit its consumers pinned. The historical lock cannot be
  re-resolved, because it lives in a commit in history. **This is an open problem
  and is not solved here** — see "Open questions". Inventing a graceful
  degradation would produce a check that is green for a reason nobody can state.
- **No manifest at all** — the commit that introduced `stele`, or any revision
  predating adoption. Nothing to compare; the run says so and exits zero.

**Cost, stated accurately.** The cache is keyed by commit, so the previous
revision's pins are cold exactly when a dependency moved — the interesting case —
and cold in every case on a CI runner with no warm cache. Two closures are fetched,
resolved and compiled. This is not "nearly free", which is what the first revision
claimed.

## Scope, and the hole it leaves

**Owned modules are compared**, as `stele lint` scopes itself. But scoping to
owned files leaves a producer action with a consumer consequence invisible, and
that must not be silent:

**Bumping a dependency pin can break your consumers while your own files are
byte-identical.** A consumer resolves your manifest transitively; if you move a
dependency to a revision where a message lost a field, every consumer that
compiles through you breaks, and a comparison of owned files finds nothing.

So the producer run also compares the **resolved closure reachable from owned
modules' imports** and reports changes there. It is not the consumer side — it
answers "did what I re-export change", not "did what I import change" — but it
closes the hole rather than leaving it unnamed.

## Findings and their identity

**A breaking finding carries an explicit subject, supplied by the comparison
engine.** This diverges from lint, where `Finding.Subject` is stamped from the
position and a rule cannot set it, and the divergence has a reason: that
constraint exists so a rule running in another process — which can only return a
line and a column — is not second-class. Breaking detection has no plugin host in
this slice, and the engine holds both descriptor sets, so it knows the name of a
removed field exactly.

Without this, the design does not work at all: a removed field has no position in
the current revision, so anchoring to the surviving message would make the subject
the *message*, and one permission would license the removal of every field of that
message, forever.

**File-level subjects live in their own namespace.** A removed file has no full
name; its subject is its import path, marked as such so it can never collide with
a declaration's name.

**Positions.** A finding anchors at the site of the change where one exists, and
at the nearest surviving declaration otherwise, with the removed element named in
the message. Where nothing survives, the finding carries the path and no line.

## Migrations are declared as moves, not permitted as breakages

A package rename, a file split, or a module-root rename changes the import path or
full name of everything inside it, so every declaration reads as removed. Under a
permission-per-change valve that is hundreds of entries, each needing a reason —
which is how a valve dies of its own weight, and how one blanket entry ends up
licensing a genuine removal hidden in the migration.

So a move is **declared and verified**, not permitted:

    breaking:
      base: master
      moves:
        - from: example.orders.v1
          to: example.ordering.v1

The tool then checks that every declaration that left `from` exists under `to`
with an identical shape. A lossless move produces **no findings at all**. Anything
that did not survive the move is reported as what it is — a removal — inside a
migration that claimed to be lossless.

This is an assertion the tool verifies, not a licence it grants, which is the
difference that makes it safe to write once for a whole package.

## The valve

**Permission is for one specific change**, in the manifest, where it is reviewed
by the people it binds:

    breaking:
      allow:
        - rule: break/field_removed
          subject: example.orders.v1.Order.deprecated_eta
          reason: read by no consumer, checked across the fleet

`reason` is required: a permission with no stated reason cannot be told from a
workaround six months later.

**A permission matches the change, not just the declaration.** Every rule declares
a discriminant, and the permission matches on it: for a type change, the pair of
types; for a move into a oneof, which oneof. Without this, `int32 → int64` and
`int32 → string` are the same permission, and a reviewer who approved the first
has approved the second.

**A stale permission is reported and is not fatal.** The first revision made it an
error, on the argument that a stale permission is a standing licence while a stale
baseline entry is a paid debt. That argument does not survive: to use a stale
permission for a removed field, the field must first be **resurrected** under the
same full name and removed again. A stale baseline entry silently absorbs a
recurrence on a *live* declaration, from one bad edit. By risk the baseline is the
more dangerous of the two, and the first revision gave two structurally identical
objects opposite verdicts on the strength of what they were called.

Making it fatal also broke the design's own premise. On a base branch whose run is
meaningful, a permission goes stale one merge later; but every branch that merges
the base in inherits the line, finds nothing, and goes red for somebody else's
change — the neighbour-blaming failure, arriving by another door.

**"No permanent licences" is enforced off the merge path.** `stele breaking
--audit` reports stale and long-lived permissions and is meant for a scheduled
job that reddens alone and blocks nobody. `--prune` deletes stale entries and
leaves a reviewable diff.

**Severity is not configurable, and the valve for immature contracts is
maturity, not a dial.** A repository with no consumers yet, renaming fields daily,
would accumulate thirty permissions in a week and then delete the CI job — moving
the off switch from a reviewed file into CI, which is the *invisible* place, and
that is the opposite of the argument that made lint's severity configurable.

The valve is the contract's own statement about itself: **packages whose version
is a prerelease — `v1alpha1`, `v1beta2` — are exempt from `break/*`**, because AIP
says in as many words that such packages promise no compatibility. It is
declarative, it lives in the contract, it is reviewed, and it agrees with the AIP
ledger this tool already maintains. Graduating to `v1` is the moment protection
begins, which is the moment it should.

## Failure behaviour

Silence is forbidden: every way of failing to compare is an error naming itself,
never an empty report, because an empty report is indistinguishable from a clean
one. The named cases are the three git states above, the three previous-revision
states, and a rule that crashes — which fails the run whatever else was found.

Adding `breaking:` to a manifest is itself a compatibility event: the parser
rejects unknown keys by design, so an older `stele` fails on a manifest that
adopts this. It is named in the changelog on those terms.

## Evidence

1. **Paired fixtures, one per rule, in both directions** — one proving the rule
   fires, one proving it stays silent on a legal change of the same shape. A rule
   with only the first half is not known to detect what it claims.
2. **The type matrix**, with the corpus and the cross-type equality specified
   above.
3. **A measurement over open work before the fleet enables it.** The claim that
   this "arrives green by construction" is false: it proves there is no historical
   debt, and findings on day one come from **unmerged branches**. A branch three
   weeks old carrying reviewed removals gets fatal findings, and rebasing does not
   help. So the detector runs against every open merge request across the fleet,
   and the counts are published before it is switched on — the same discipline
   lint was held to.
4. **A replay over real history, with its own coverage stated first.** Most
   historical revisions predate adoption and have no manifest, so they are outside
   the comparison by rule. The number of *comparable* merge pairs is measured and
   published **before** the replay result, because "it fired nowhere" otherwise
   has a third explanation the first revision did not account for: almost nothing
   was compared.

## Open questions

- **A dead pin in a historical lock.** When a dependency squash-merges, the commit
  a historical lock pinned is gone, and that revision can never be compiled again.
  Every later run in that repository fails for a reason unrelated to the change
  under review, and the recovery the error text suggests — re-resolve with
  `--update` — is impossible for a lock in history. Candidates: fall back to the
  nearest comparable ancestor and say so; permit a recorded substitution; treat an
  unreachable historical pin as a named skip rather than a failure. None is chosen
  here.
- **Maintenance branches.** With one `breaking.base`, a long-lived `release/1.x`
  has a merge-base that does not move for months, so its permissions never go
  stale and the branch reads every divergence from the base as a breakage. Whether
  the base is configurable per branch is unresolved.
- **Generated output is out of scope, and it is breaking by definition.** Changing
  a generation target's plugin version or options changes the bytes consumers
  link against. This design compares descriptors only, which is a narrowing the
  roadmap's own category list does not make.

## Consumer side, inherited and deferred

The comparison engine is unchanged; only the previous revision differs — the
commit pinned in `stele.lock` against the commit an update would take. It gains
what the producer side cannot have: the import closure already knows which files
this repository actually uses, so the report can say "two of these reach you, and
here is what imports them".

It also closes a hole the producer side does not claim to close: a merge-base
comparison protects the base branch, but a consumer pinned to a commit in the
middle of your history is unaffected by the base branch's opinion. A breaking
change merged and later reverted still happened, for them.

## What the first revision got wrong

Recorded so it is not re-derived. Every item was found by adversarial review, none
by the author.

- The base-branch comparison was degenerate: merge-base with itself, so no rule
  could ever fire and the only possible signal was a false one.
- A stale permission was fatal, which reddened every branch that merged the base
  in — the exact failure the merge-base decision was made to avoid.
- The asymmetry justifying that — permission as standing licence, baseline as paid
  debt — was a rationalisation; by risk the baseline is the more dangerous object.
- The subject of a finding was to be derived from the position, which cannot name
  a removed field and would have licensed the removal of every field of a message.
- Shallow depth was named as the cause of a missing base branch; the cause is the
  refspec, and depth was the wrong fix to send a person to.
- A fabricated PR merge commit silently degraded the comparison to the base tip.
- "Green everywhere by construction" confused an absence of historical debt with
  an absence of findings, and stood in place of a measurement.
- Refusing configurable severity moved the off switch into CI, which the lint
  design had already identified as the invisible place.
- The type matrix's oracle was unspecified, would have called `float` and
  `fixed32` compatible, and the document pre-committed to believing it.
- The JSON blind zone was presented as covered by the source category; three
  concrete breakages pass green through it.
- Dependency pin bumps were left invisible by the owned-modules scope.
- Migrations had no mechanism, so a package rename meant hundreds of permissions.
