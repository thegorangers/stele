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

**Permission is for one specific change**, in the manifest, where the people it
binds review it:

    breaking:
      allow:
        - rule: break/field_type_changed
          subject: example.orders.v1.Order.total
          change: int32 -> int64
          reason: widening; no consumer stores this in a 32-bit field

`reason` is required: a permission with no stated reason cannot be told from a
workaround six months later.

**`change` is the discriminant, and it is not universal.** Where a rule has an
attribute beyond its subject — the pair of types, the destination oneof, the
direction of a cardinality change — `change` is required, and a permission without
it is refused rather than treated as matching anything. Earlier drafts stated a
universal rule and then showed an example without the field, which is the blanket
permission the mechanism exists to forbid. **Removals have no discriminant**: the
subject is the whole of the change, and `change` is refused there. Each rule's
discriminant spelling is part of the public contract on the terms `RELEASING.md`
sets for rule ids, because it lands in somebody's manifest.

**A stale permission is reported and is not fatal.** An earlier revision made it an
error, arguing that a stale permission is a standing licence while a stale
baseline entry is a paid debt. That does not survive: to use a stale permission for
a removed field the field must first be **resurrected** under the same full name
and removed again, while a stale baseline entry absorbs a recurrence on a *live*
declaration from one bad edit. By risk the baseline is the more dangerous object,
and the argument gave two structurally identical things opposite verdicts on the
strength of what they were called.

Making it fatal also broke the design's own premise: every branch that merged the
base in would inherit the line, find nothing, and go red for somebody else's
change — the neighbour-blaming failure arriving by another door.

**"No permanent licences" is enforced off the merge path.** `stele breaking
--audit` reports stale permissions and moves and is meant for a scheduled job that
reddens alone and blocks nobody; `--prune` deletes stale entries and leaves a
reviewable diff. Note the limit: the manifest records no dates, so `--audit` can
report *stale*, not *long-lived*, and no claim about age is made.

## An unresolved question: repositories with no consumers yet

Severity is not configurable, deliberately: "breaks consumers, but only a warning"
is `allow_failure: true` with more steps.

That leaves a service in early development — contract present, no consumers,
several field renames a day — with a choice between thirty permissions a week and
deleting the CI job, which moves the off switch into CI, the *invisible* place,
which is the opposite of the argument that made lint's severity configurable.

A previous revision answered this by exempting prerelease packages
(`v1alpha1`, `v1beta2`) and claiming agreement with the AIP ledger this tool
maintains. **The claim was false**: the ledger records AIP-181 as `undecidable`
because "a stability level is a claim about a release process, not about a file",
and AIP-180 and AIP-185 likewise. The ledger is right — a package named beta with
a hundred consumers is exactly its point. The exemption was also inert where it
mattered, since on the graduation commit every declaration still lives in the
prerelease package and every finding about it would be exempt.

The exemption is withdrawn and nothing replaces it. **This is an open question,
and enabling the check on a repository in early development is not recommended
until it is answered.** A candidate worth measuring first: whether a repository
has consumers is a fact, not a claim — the fleet's locks say who pins whom.

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
