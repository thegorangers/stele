# Breaking-change detection

Status: design, third revision. Not implemented.

Two earlier revisions were reviewed adversarially and did not survive. The core —
compare two compiled revisions in two categories — has never been challenged in
four reviews. Everything fatal was in one place: **how the previous revision is
obtained**, which the first two revisions specified without measuring anything.

The third revision replaces that speculation with measurement. Where a number
appears below it was measured on a real fleet of fourteen repositories on
2026-08-28, and the method is named so it can be re-run when it goes stale. Two
findings that adversarial review called fatal were **refuted by that
measurement**, which is recorded rather than quietly dropped: a reviewer's
verdict is data, not judgement.

## What this is for

`stele` generates code from contracts and pins the contracts it consumes. It
cannot yet answer the question every consumer has: **did this change break me?**

The **producer** asks it of their own change; the **consumer** asks it of a
dependency that moved. One comparison engine answers both and only the source of
the previous revision differs. This document specifies the producer side.

## What counts as breaking

- **Wire.** Already-serialised bytes stop being read correctly: field numbers,
  incompatible type changes, cardinality, movement between distinct oneofs, enum
  value numbers, and — because in gRPC a name is a path on the wire — renaming or
  removing a package, service or method, changing a method's request or response
  type, or whether it streams.
- **Source.** The bytes survive; a consumer's code stops compiling: field renames
  at a stable number, removals, removal or rename of a message, enum or enum
  value, a changed `go_package`, a renamed oneof with its members intact. Moving
  a field into or out of a oneof is Source only when the oneof on both sides of
  the move is a singleton — the case a generated wrapper type appears or
  disappears around, but nothing else on the wire shifts; when either side has
  other members remaining, those siblings' `case` values move and the change is
  Wire instead.

### The blind zone, stated because silence reads as safety

JSON and behavioural compatibility is out of scope, and that costs real coverage.
Three breakages pass green. They are named here and printed in every report's
footer:

- **`json_name` changed** — same field name, same number; every `protojson` and
  transcoding consumer breaks.
- **`int32` → `int64`** — wire-compatible, so this design calls it safe, but
  `protojson` serialises 64-bit integers as **strings**: `5` becomes `"5"`.
- **`google.api.http` changed** — pure behaviour, invisible to both categories,
  and in a fleet with transcoding the most likely real breakage of the three.

A oneof renamed with its members intact used to belong on this list — `oneof
pick {...}` becoming `oneof choose {...}` changes the generated wrapper type
and getter, so every Go consumer stops compiling, but no descriptor index
shifts, because a oneof's name is not on the wire, only each member's
`oneof_index` is. It no longer does: `break/oneof_renamed` pairs a removed
oneof with an added one in the same message when their member field-number
sets are identical — that set is disjoint from every sibling oneof's by
construction, so the match is exact, not a shape guess — and reports it as
Source.

A report saying "no breaking changes" means none *in the two categories defined
here*, and says so in those words.

### The type matrix is measured, and the oracle is specified

Hand-written enumerations in this project have been wrong four revisions running,
so the matrix is built by a test. "Encode as one, decode as the other" is not
enough: it measures the wire *type* and would call `float` and `fixed32`
compatible, because both are four bytes and every decode succeeds while `1.0`
reads back as `1065353216`.

- **A corpus, not a sample.** Zero, one, minus one, the boundaries of every width,
  values overflowing a narrower type, invalid UTF-8 for `string`/`bytes`. A pair
  is compatible only if the whole corpus agrees.
- **Cross-type equality is defined.** Numeric types compare numerically — which
  rejects `float`/`fixed32`. `string` and `bytes` compare as byte sequences, each
  direction separately.
- **Disagreement halts.** Where measurement and prose disagree, neither wins
  automatically: the run fails and a human resolves it against the protobuf
  encoding specification.

The matrix covers ordered pairs of scalars. Every other hazard — `map` versus a
repeated entry, oneof movement, cardinality — is covered by paired fixtures, one
per rule, in both directions.

## Acquiring the previous revision

**On a topic branch**, the previous revision is the merge-base of the working
revision and the base branch named by `breaking.base`. Never the tip: comparing
against the tip makes a neighbour's merged change fail an unrelated branch.

**On the base branch**, the previous revision is HEAD's **first parent** — what
the branch looked like before the change that landed. The first revision used
merge-base here too, which on the base branch is HEAD itself: nothing compared,
no rule able to fire, and the only signal the job could ever emit was a false one.

Review called the first-parent rule unsound, on the ground that a fast-forward or
rebase merge lands several commits and only the last is compared. **Measured, and
refuted for this fleet**: all fourteen projects are configured `merge_method =
merge`; none is `ff` or `rebase_merge`; every merge request lands as one merge
commit whose first parent is the base before it; and since adoption every
first-parent commit in all fourteen repositories is a merge commit. Method:
`glab api projects/:id` for the setting, and `git rev-list --first-parent
--parents` over each base branch for the reality, because the setting and the
history can disagree.

**The rule's real exposure is different from the one predicted, and is
documented rather than assumed away:** a direct multi-commit push to the base
branch bypasses it, and there is a precedent in the fleet — one repository took
97 linear commits in a single push. Branch protection permits it for maintainers.
A change arriving that way is compared only against the last commit of the push.

**Under a CI checkout that fabricates a merge commit** — `actions/checkout`
resolves a pull request to a merge of the topic into the base tip — the merge-base
of that HEAD with the base *is* the base tip, silently becoming the rejected
comparison. The test is **structural, not an equality with the tip**: a parent
that is an ancestor of the base identifies the base side, and the working
revision is the other parent. Testing equality with the tip fails whenever the
base moved between checkout and run, which on an active repository is normal.

Two consequences of that test are stated because they are not obvious:

- **A human's merge of the base into a topic branch has the same shape.** The
  tool takes the topic parent, so the merge commit itself is not compared — and a
  conflict resolution that drops a field is the most natural way to introduce a
  breakage by hand. This is a known gap of the topic-branch path; the base-branch
  run catches it when the branch lands.
- **When both parents are ancestors of the base** — a re-merge, or a merge of two
  already-landed branches — there is nothing outside the base to compare. The run
  says so and exits zero rather than reporting an empty comparison as clean.

**Acquiring the base branch is a step, not an assumption.** A GitLab merge-request
pipeline fetches only the merge-request ref: the base branch is not truncated, it
is **absent**, at any depth, and `GIT_DEPTH: 0` does not fix it. Depth still
matters separately — `HEAD^` and an old merge-base can both be outside a shallow
clone — so the causes are distinguished before they are reported, and
`--is-shallow-repository` is consulted *first*, because a shallow clone otherwise
reports as "unrelated histories" and sends a person to fix the wrong thing.

The first commit of a repository has no first parent: nothing to compare, exit
zero.

**`--against <ref>`** uses that revision directly, with no merge-base. It is the
escape for a deliberate comparison — against a release tag — and is documented as
unsuitable for a CI default, because `--against origin/master` is exactly the
neighbour-blaming comparison, one copy-paste away.

### The local git driver is new work

`internal/source` clones into a cache and **deletes `.git`** from every entry, so
no existing machinery can compute `merge-base`, `rev-parse HEAD^`, or read a tree
from history. Nothing in the tool today accepts an existing repository. This
design therefore requires a local git driver — `rev-parse`, `merge-base`,
`cat-file`, `rev-list`, shallow detection, and materialising a historical tree —
with its own error taxonomy. Earlier revisions specified the behaviour and did not
notice the component.

It is bound by two rules, because `stele` otherwise writes only generated code and
the lock:

- **Fetching the base ref writes into `refs/stele/*` and never touches
  `FETCH_HEAD`** or the user's remote-tracking refs.
- **`breaking.base` names a remote-tracking ref** — a stale local branch would
  silently place the comparison in the past. A repository with no remote, or with
  several and no choice recorded, is an error naming the ambiguity.

## Compiling the two revisions

**Both sides are compiled, not parsed.** Source compatibility needs resolved
types: a field removed from an imported message is invisible without resolution.

**The previous revision is compiled with its own dependencies** — its own
`stele.yaml` and `stele.lock`, materialised whole. Compiling yesterday's protos
against today's pins either fails outright or attributes another repository's
change to this one.

A manifest with **no lock** fails the run: resolving it fresh would silently
resolve each `ref:` to today's tip, which is the thing that paragraph forbids. A
revision with **no manifest** — the commit that introduced `stele`, or anything
before adoption — has nothing to compare and exits zero.

### The dead pin: measured, and downgraded

The second revision recorded as an open problem, and review called fatal, that a
dependency's squash-merge rewrites the commit its consumers pinned, making the
historical lock permanently uncompilable.

**Measured: 23 distinct pins across every historical lock in the fleet, 0 dead.**
Every internal pin is not merely reachable but an ancestor of its dependency's
current base branch; both external pins resolve. Method: every `stele.lock` blob
on every ref (1862 blob versions), pins checked against the forge API and against
a fresh clone.

The mechanism matters more than the count, and it is why the reviewer's reasoning
was wrong rather than merely unlucky: **every internal dependency is pinned
`ref: master`**, so a resolved pin is always a commit already on the dependency's
base branch, and squash-merge rewrites topic-branch commits, not the base branch.
The failure cannot arise from squash at all.

It remains possible under two named conditions, and only these: a dependency
pinned to a ref other than its base branch, or a force-push to a dependency's base
branch. Reported as what it is when it happens; not designed around.

### Cost, and the shortcut that makes it moot

Two closures fetched, resolved and compiled is not free. The cache is keyed by
commit, so the previous revision's pins are cold exactly when a dependency moved,
and cold always on a CI runner with no warm cache. Fetching an unadvertised commit
falls back to fetching all heads and tags with no depth limit, and that fallback
is the *ordinary* path for the older side, not an edge.

**But the ordinary commit pays none of it.** If the git trees of the owned module
roots and the `stele.lock` blob are identical between the two revisions, there is
nothing to compare and the run exits immediately. **Measured at 84.7% of base-branch
commits fleet-wide** (786 commits, full history; 82.4% over the most recent 60 per
repository; range 52% to 99%). Roughly one commit in six pays the full comparison.

One narrowing is available and worth taking: the previous side never supplies a
position, so it does not need source info. `internal/compile` hard-codes
`SourceInfoStandard` with no option, so this is a change there, not a setting.

## Scope, and the hole it leaves

**Owned modules are compared**, as `stele lint` scopes itself. One repository in
the measured fleet owns no proto modules at all — it is a pure consumer, and the
producer comparison there is empty by construction, which the run says rather than
reporting as clean.

Scoping to owned files leaves a producer action with a consumer consequence
invisible: **bumping a dependency pin can break your consumers while your own
files are byte-identical**, because a consumer resolves your manifest
transitively. So the producer run also compares the **resolved closure reachable
from owned modules' imports** and reports changes there. It answers "did what I
re-export change", not "did what I import change", and says so.

## Findings and their identity

**A breaking finding carries an explicit subject supplied by the comparison
engine.** This diverges from lint, where `Finding.Subject` is stamped from the
position and a rule cannot set it. That constraint exists so a rule in another
process — which can only return a line and a column — is not second-class;
breaking detection has no plugin host in this slice, and the engine holds both
descriptor sets, so it knows a removed field's name exactly.

Without this the design does not work at all: a removed field has no position in
the current revision, so anchoring to the surviving message would make the subject
the *message*, and one permission would license the removal of every field of that
message forever.

**Subjects are typed, because two namespaces meet here.** A declaration's subject
is its full name; a file's is its import path, written `file:api/orders/v1/order.proto`.
An untagged string is a full name, and a path that looks like a name cannot be
mistaken for one.

**Breaking findings do not enter `stele.baseline`.** The baseline is keyed on a
position-derived subject and is designed for debt that is being paid down; a
breaking finding is neither. Admitting them would also reopen the laundering this
design refuses: a regenerated baseline would absorb a breakage silently.

**Positions.** A finding anchors at the site of the change where one exists, at
the nearest surviving declaration otherwise with the removed element named in the
message, and carries a path with no line where nothing survives.

## Migrations: a rename map, not a permission

**Not built.** This section describes a design, not behaviour the tool has. It was
planned as part of the valve and taken out: a revision's descriptors are
immutable and a field whose type moved still carries the old fully-qualified
name, so renaming index keys does not produce the property the mechanism exists
for — a lossless rename producing no findings. It needs a feasibility probe
rather than a task list. Until then a repository facing a package rename holds
the rule at `warning`.


A package rename changes the full name of everything inside it, so every
declaration reads as removed. Permitting that one declaration at a time is
hundreds of entries, and one blanket entry would license a genuine removal hidden
among them.

A move is therefore **an identity map applied before the comparison, not a
suppression after it**:

    breaking:
      base: master
      moves:
        - from: example.orders.v1
          to: example.ordering.v1

The previous revision's declarations are renamed through the map, and then the
ordinary comparison runs. **A move suppresses nothing.** A lossless rename
produces no findings because after renaming there is nothing to report; a rename
that also dropped a field reports that field, by its new name, as an ordinary
removal. Laundering is impossible by construction rather than by a check that has
to be got right.

That framing settles what "identical shape" would otherwise have to enumerate:
there is no shape comparison. The consequences are stated:

- **`go_package` follows the package.** A `go_package` change explained by a
  declared move is part of that move; one that is not explained is a finding.
- **`to` must be a module this repository owns.** Pointing a move at a dependency
  would make a pinned third party the authority on whether your declarations still
  exist.
- **A move is refused, not resolved, when it is ambiguous**: two entries with the
  same `from`, a cycle, or a `from` that still exists in the current revision
  while claiming to have moved.
- **File-path moves take the same form** over `file:` subjects. A file *split* —
  one file becoming two in the same package — is not expressible, is named as
  unsupported, and reports as ordinary removals and additions.
- **A move that matches nothing is stale**, reported by `--audit` and removed by
  `--prune`, on the same terms as a permission. Nothing else retires it.

## The valve

**Every rule is on, at error severity, until the manifest says otherwise.**
Protection arrives by itself; switching a rule off is a line in a diff, reviewed
by the people it binds.

    breaking:
      rules:
        - id: break/field_type_changed
          severity: off
        - id: break/field_renamed
          severity: warning

The vocabulary is `error | warning | off`, and it is deliberately the one
`lint.rules` already uses. Two spellings of one question would be two sets of
mistakes to make, and the question is the same: what does this repository do
about this rule.

**Lowering a rule requires a `reason`, on the same terms a permission does.**
Without this the mechanism is inverted: approving one change would be gated on a
stated reason while switching the rule off *for every future change, for ever*
would be free. The larger concession is the one that needs the sentence.

**Every lowered rule is named in the report, on every run.** This is the shape
`unpinned: true` already has for plugins — not usable by accident, and said out
loud rather than left in a file somebody has to go and read. A repository may
protect nothing; it may not do so quietly.

The run's exit status is non-zero when a finding stands at `error`.

An earlier draft of this section also said that a rule which failed to run fails
the run whatever severity it was configured at, on the principle that `off`
silences a rule's findings and not its failures. The principle is right and the
sentence was struck, because the engine has no notion of a rule failing: rules
run inline over a descriptor diff, with no per-rule dispatch and no plugin
boundary, and nothing in the tree recovers a panic — so a rule that breaks exits
non-zero as a crash rather than being swallowed into a clean-looking report. The
sentence promised a mechanism that does not exist and was not needed. It becomes
needed the day breaking rules gain a host the way lint rules have one, and that
is the day to write it.

**An earlier revision refused per-rule configuration**, arguing that "breaks
consumers, but only a warning" is `allow_failure: true` with more steps. That
argument does not survive contact with the alternative. The choice a repository
actually faces is not between full protection and configured protection — it is
between configured protection and **deleting the CI job**, which is the invisible
off switch this project's lint design already identified and rejected. A
repository that two rules out of twenty inconvenience will switch off all twenty.
Partial protection is worth more than none, and the same document argues exactly
this for `lint.rules` a few sections away; refusing it here was an inconsistency,
not a principle.

**Two honest qualifications, because this decision was taken ahead of its
measurement.** Everything else here was measured before it was chosen; this was
not, and the measurement that would inform it — the shadow period and the count
over open work, in Evidence below — is scheduled after. So:

- The day-one cost of *not* having `warning` is smaller than it first looks. A
  breaking finding is not a stock of pre-existing debt the way a lint finding is:
  it exists only because somebody is breaking a contract now. What reddens on
  the first day is unmerged branches, which is a transient the shadow period
  already absorbs. `warning` is a permanent mechanism, and the problem it is
  most often justified by is temporary.
- The producer configures a risk the *consumer* bears. A lint finding is about
  your file and costs you; a breaking finding says somebody else's build stops
  working. That asymmetry is the one real argument against per-rule severity,
  and it is not answered here — it is accepted, in exchange for the alternative
  being an invisible switch in CI. Naming it is the least this document owes.

A candidate that would resolve it, recorded rather than adopted: whether a
repository has consumers is a fact the fleet's locks record, not a claim its
manifest makes. Defaults derived from who pins whom would tighten by themselves
the day a first consumer arrives. That needs a measurement this project has not
made, and it belongs with the others.

**Permission for one specific change** is the finer tool, for a repository that
keeps a rule at `error` and has approved one exception to it:

    breaking:
      allow:
        - rule: break/field_type_changed
          subject: example.orders.v1.Order.total
          change: int32 -> int64
          reason: widening; no consumer stores this in a 32-bit field

`reason` is required: a permission with no stated reason cannot be told from a
workaround six months later. Without this layer, a repository needing one
exception has to switch the whole rule off, which is a far larger concession than
the change it wanted to make.

**`change` is the discriminant, and it is not universal.** Where a rule has an
attribute beyond its subject — the pair of types, the destination oneof, the
direction of a cardinality change — `change` is required, and a permission without
it is refused rather than treated as matching anything. **Removals have no
discriminant**: the subject is the whole of the change, and `change` is refused
there. Each rule's discriminant spelling is part of the public contract on the
terms `RELEASING.md` sets for rule ids, because it lands in somebody's manifest.

**A stale permission is reported and is not fatal.** To use a stale permission for
a removed field the field must first be **resurrected** under the same full name
and removed again. Making it fatal would also break the design's own premise:
every branch that merged the base in would inherit the line, find nothing, and go
red for somebody else's change — the neighbour-blaming failure arriving by another
door.

**The posture is reported, which is less than measured and should not be dressed
as more.** `stele breaking --audit` reports stale permissions and what this repository has lowered — counting both `severity` and a rule
whose `ignore` list covers everything it would otherwise check, because an audit
that can be zeroed by a mechanism it does not count is worse than none.

It is a report about **one repository**, read by the people who wrote the lines it
is about. This design has no aggregator and does not pretend to one: an earlier
revision claimed a component it did not have and was caught doing it. And it is
blind in the direction that matters most — a repository that would have deleted
the CI job does not run this either. `--prune` deletes stale
entries and leaves a reviewable diff. Note the limit: the manifest records no
dates, so `--audit` reports *stale*, not *long-lived*.

## Repositories with no consumers yet: answered, and what it cost

Per-rule severity answers this. A service in early development — contract
present, no consumers, several field renames a day — writes three reviewed lines
with a stated reason, and they are named in every report until it removes them.
The section is kept because the reasoning that was used to refuse that answer is
worth not re-deriving.

The refusal ran: severity is not configurable, so such a service faces a choice
between thirty permissions a week and deleting the CI job — which moves the off
switch into CI, the *invisible* place, and is the opposite of the argument that
made lint's severity configurable. The second half of that sentence is what
eventually overturned the first.

A previous revision answered it differently, by exempting prerelease packages
(`v1alpha1`, `v1beta2`) and claiming agreement with the AIP ledger this tool
maintains. **The claim was false**: the ledger records AIP-181 as `undecidable`
because "a stability level is a claim about a release process, not about a file",
and AIP-180 and AIP-185 likewise. The ledger is right — a package named beta with
a hundred consumers is exactly its point. The exemption was also inert where it
mattered: on the graduation commit every declaration still lives in the
prerelease package, so every finding about it would be exempt. It is withdrawn
and is not coming back.

What remains genuinely open is not this question but the one under it: a
repository's own claim about itself is the weakest evidence available, and
whether it has consumers is a **fact** the fleet's locks record. Defaults derived
from who pins whom would need no claim and would tighten by themselves the day a
first consumer arrives. That is a measurement this project has not made.

## Failure behaviour

Silence is forbidden: every way of failing to compare is an error naming itself,
never an empty report, because an empty report is indistinguishable from a clean
one. The named cases are the git states above, the previous-revision states, and a
rule that crashes, which fails the run whatever else was found.

Two compatibility events, named because they are not obvious:

- Adding `breaking:` to a manifest makes it unreadable to an older `stele`: the
  parser rejects unknown keys by design.
- `schema/stele.schema.json` is a second copy of the manifest's shape, held
  against the parser by a test, and must gain the same keys.

## Evidence

1. **Paired fixtures, one per rule, in both directions** — one proving the rule
   fires, one proving it stays silent on a legal change of the same shape. A rule
   with only the first half is not known to detect what it claims.
2. **The type matrix**, with the corpus and cross-type equality above.
3. **A shadow period, replacing the historical replay.** An earlier revision
   proposed replaying the fleet's merged history and publishing the count of
   comparable pairs first. Measured, that plan is empty: **50 comparable pairs out
   of 786 base-branch commits — 6.4% — and none older than five days**, because
   the tool was adopted on 2026-08-23. Publishing the denominator would have made
   the emptiness visible without curing it. Instead the detector runs in CI in
   report-only mode for two weeks, and every firing is classified by hand as true
   or false. That answers the question a replay cannot answer at all: not "did it
   fire" but "was it right".
4. **A measurement over open work before the fleet enables it**, because
   "arrives green by construction" is false — it proves there is no historical
   debt, while findings on day one come from **unmerged branches**, where a
   three-week-old branch carrying reviewed removals gets fatal findings and
   rebasing does not help. The counts are published before it is switched on, as
   lint's were. **With an independent oracle:** the count is produced by the same
   previous-revision logic being validated, so a hand-checked sample of ten to
   fifteen merge requests is checked against it. A measurement that can only
   confirm itself is not evidence.

## Consumer side, inherited and deferred

The engine is unchanged; only the previous revision differs — the commit pinned in
`stele.lock` against the commit an update would take. It gains what the producer
side cannot have: the import closure knows which files this repository actually
uses, so the report can say "two of these reach you, and here is what imports
them".

It also closes a hole the producer side does not claim to close: a merge-base
comparison protects the base branch, but a consumer pinned to a commit in the
middle of your history is unaffected by the base branch's opinion. A breaking
change merged and later reverted still happened, for them.

## What earlier revisions got wrong

Recorded so it is not re-derived. Everything here was found by adversarial review
or by measurement; none of it by the author.

**First revision.** The base-branch comparison was degenerate — merge-base with
itself, so no rule could fire and the only possible signal was false. A stale
permission was fatal, reddening every branch that merged the base in. The
asymmetry justifying that was a rationalisation. The subject was to be derived
from the position, which cannot name a removed field. Shallow depth was named as
the cause of a missing base branch when the cause is the refspec. A fabricated PR
merge commit silently degraded the comparison to the base tip. "Green everywhere
by construction" confused absence of historical debt with absence of findings.
Refusing configurable severity moved the off switch into CI. The type matrix's
oracle was unspecified and would have called `float` and `fixed32` compatible.
The JSON blind zone was presented as covered. Dependency pin bumps were invisible.
Migrations had no mechanism.

**Second revision.** `moves` verified "identical shape", a term it never defined,
and which could have suppressed a real break; it is now a rename map that
suppresses nothing. The prerelease exemption cited a ledger that says the
opposite and was inert on the graduation commit. The permission example omitted
the discriminant the next paragraph required. "The tool fetches the base ref" had
no component behind it. The fabricated-merge test compared against the tip, which
fails whenever the base moves. Cost was understated and the tree shortcut was not
considered.

**Refuted by measurement, and recorded because a reviewer's verdict is data.**
Two findings called fatal did not survive contact with the fleet: the first-parent
rule holds on 14 of 14 repositories, and no historical pin is dead, for a
structural reason — every internal dependency pins its base branch, which
squash-merge does not rewrite.
