# AIP breadth, and the ledger that keeps it honest

`stele lint` ships eight rules. The direction asked for is AIP breadth. AIP is a
large external corpus, so the first question is not "which rules next" but "how
does this repository hold a list of an external corpus without the list rotting".
This document answers that, and it is measurement first because every number
below changes what the design has to be.

> **Overruled, and where.** This document was written before any rule existed
> and it recommended, in §6, that AIP rules be a profile a manifest opts into.
> That recommendation was **overruled by the owner** when the first rules
> landed. §6 is left standing as written, with the decision and its reasoning
> recorded in §10: the argument that beat it is this document's own §9, "a
> profile that nobody enables is a profile that ships dead". §3's estimates have
> been re-measured with the rules themselves, and one of them was wrong by
> nearly a factor of two.

Nothing here writes a rule. What it lands is the inventory: `internal/aip`, a
derived snapshot of upstream, a ledger of decisions, and a test that fails when
the two disagree.

## Why not simply write AIP rules

This repository has a documented failure history with hand-written lists of
facts: four consecutive design revisions died because an enumeration typed by a
person drifted from the thing it enumerated, and looked complete while it
drifted. The fix, each time, was to derive the list.

"The AIPs we implement" is that list again, one size larger. AIP is edited
upstream; it grows; an AIP can change state under a number that stays the same.
A hardcoded subset would be wrong within months and would read as complete for
years. So the subset is not written down at all: what is written down is a
decision per AIP, checked against a mechanically derived list of every AIP
upstream carries.

## 1. The corpus, measured

The corpus is `github.com/aip-dev/google.aip.dev`. Each AIP is a markdown file
under `aip/<scope>/` with YAML front matter carrying `id` and `state`.

```
go run ./internal/aip/aipsync          # derives internal/aip/index.tsv
go run ./internal/aip/aipsync -check   # fails if it is not what upstream says
```

The snapshot in this repository is upstream at `23e176e733`, and every number
below is `awk` over it, not a reading.

```
$ awk -F'\t' 'NR>4 {n++} END {print n}' internal/aip/index.tsv
118
$ awk -F'\t' 'NR>4 {s[$3]++} END {for (k in s) print k, s[k]}' internal/aip/index.tsv
approved 101
draft 14
reviewing 3
$ awk -F'\t' 'NR>4 {s[$2]++} END {for (k in s) print k, s[k]}' internal/aip/index.tsv
general 73
client-libraries 11
auth 10
firebase 9
apps 6
aog 5
cloud 4
```

**118 AIPs. No withdrawn, rejected or replaced ones today** — AIP-1 defines
those states and the corpus does not currently use them, which is a fact about
today and not a property to rely on; `Record.State` carries whatever word
upstream writes.

### The split: what is about protobuf design at all

Derived, not guessed, in two steps.

**Scope**, from the directory: 45 of the 118 are vendor sets — Actions on Google,
Workspace, Firebase, Google Cloud and gcloud, credential guidance, and the
client-library generator rules. They are guidance for platforms and generated
clients, not for a `.proto` file. That leaves **73 general AIPs**.

**Proto content**, from a mechanical prior — whether the body contains a fenced
` ```proto ` block:

```
$ awk -F'\t' 'NR>4 && $2=="general" {print $5}' internal/aip/index.tsv | sort | uniq -c
     22 no
     51 yes
```

That prior is recorded in the index and is *not* used as the answer, because it
is wrong in both directions: AIP-121 (resource-oriented design) and AIP-130
(methods) are entirely about proto APIs and carry no proto block, while AIP-4222
(routing headers) carries proto and is about client libraries. The prior is a
reading aid for triage. The ledger is the answer.

## 2. Static decidability

A rule is handed one linked file. It can see the descriptor and nothing else: no
previous revision, no service config, no knowledge of what a string means. Of
the 73 general AIPs, the ledger classifies:

| disposition | count | what it means |
| --- | --- | --- |
| implemented | 6 | rules exist today (3 when this was written; §10 added AIP-135, AIP-142 and AIP-158) |
| candidate | 23 | decidable from a descriptor, no rule yet |
| declined | 12 | deliberately no rule (10 state no requirement about a file; 2 were measured and rejected) |
| undecidable | 32 | states a requirement about protos that no descriptor can decide |

So **29 of 73 general AIPs are statically decidable in whole or in part**, and
**32 are not**. That ratio is the honest headline of this whole direction: AIP
breadth does not mean 118 rules, and a tool claiming AIP coverage without saying
so is claiming something it cannot do.

**Decidable — three examples.**

- *AIP-126, enumerations.* The zero value, its name, the prefix, the casing.
  Implemented already, by three rules that predate this document and were chosen
  against a fleet rather than against AIP.
- *AIP-158, pagination.* A `ListX` request carries `page_size` and `page_token`;
  the response carries `next_page_token`. Every term is a field name and a type
  in the descriptor.
- *AIP-135, delete.* A method named `DeleteX` returns `google.protobuf.Empty` (or
  the soft-deleted resource). Names and types, both present.

**Decidable with caveats — three examples.**

- *AIP-148, standard fields.* "A field named `create_time` is a `Timestamp`" is
  decidable. "This resource should have had a `create_time`" is not. The rule can
  only police the fields that exist.
- *AIP-216, states.* "A field named `state` is an enum named for its resource" is
  decidable; that a resource needs a state is not.
- *AIP-123, resource types.* Decidable *once* `google.api.resource` is present —
  which is a caveat with teeth, and section 4 is about it.

**Not decidable — three examples, and they are the interesting ones.**

- *AIP-121, resource-oriented design.* Whether a set of messages is a resource
  model is the whole judgement. A descriptor shows messages and methods, never
  what they represent.
- *AIP-122, resource names.* A resource name is a `string`. So is everything
  else. Nothing in a descriptor separates `books/1/pages/2` from a free-text
  note, which is why the fleet's 316 identifier fields can be neither confirmed
  nor refuted by a rule.
- *AIP-180, backwards compatibility.* Decidable, but not from *one* file: it
  compares two revisions. That is breaking-change detection, which the roadmap
  already keeps as separate work, and the ledger says so rather than pretending
  the AIP is unimplementable.

## 3. What the fleet would trigger

Measured over the same corpus the eight built-in rules were measured against:
**35 hand-written `.proto` files across 13 services** of a private fleet. As in
the roadmap, that corpus is not committed and cannot be — it is an
organisation's private repositories, and `internal/hygiene` forbids
organisation-specific identifiers with no exemptions.

These counts come from a throwaway parser over the source text, not from the
rules, because the rules do not exist yet. They are the triage numbers that
decide *which* rules to write. The standard the roadmap sets — each rule
re-measured with the rule itself before it ships — is unchanged.

| candidate check | findings | files |
| --- | --- | --- |
| AIP-203, every field carries `field_behavior` | 1225 / 1237 fields | 25/35 |
| AIP-192, every top-level declaration is documented | 372 | 28/35 |
| AIP-127, every method carries `google.api.http` | 142 / 142 methods | 35/35 |
| AIP-123, `google.api.resource` on resource messages | every message | 35/35 |
| AIP-142, a `Timestamp` field is named `*_time` | 111 | 19/35 |
| AIP-131, `GetX` returns `X` | 25 | 12/35 |
| AIP-158, `List` response carries `next_page_token` | 15 | 8/35 |
| AIP-158, `List` request carries `page_size` | 8 | 6/35 |
| AIP-135, `DeleteX` returns `Empty` | 8 | 5/35 |
| AIP-140, field names are `lower_snake_case` | 0 | 0/35 |
| AIP-133/134, `CreateX` takes `CreateXRequest` | 0 | 0/35 |
| AIP-231/233/234/235, batch method shapes | 0 | 0/35 |
| AIP-151, long-running operation shape | 0 | 0/35 |

Read the top of that table as the finding it is: **the four broadest AIP checks
fire on every file in the fleet, and AIP-142 fires on nineteen.** The 111
AIP-142 findings are all one thing — the fleet writes `created_at` where AIP
writes `create_time` — and a rule that renames 111 fields across 19 files on the
day it is switched on is a rule that gets switched off, exactly as the roadmap
records for `rpc_response_named_for_method`. The bottom of the table is the
starting set: AIP-135, AIP-158 and the batch and create shapes cost between zero
and eight fixes.

**None of this makes those rules wrong.** It makes them a profile a repository
opts into, which is section 5.

### Re-measured, with the rules

The counts above came from a throwaway parser. Five rules have since been
written and run over the fleet as rules, and the fleet has been re-enumerated at
**59 hand-written files**, deduplicated by import path across every service
rather than the 35 of one export set. This is the standard the roadmap sets, and
what follows is why it is the standard:

| rule | findings | files |
| --- | --- | --- |
| `aip/135_delete_returns_empty` | 7 | 4/59 |
| `aip/142_timestamp_field_time_suffix` | 111 | 19/59 |
| `aip/158_list_request_page_size` | 14 | 8/59 |
| `aip/158_list_request_page_token` | 15 | 8/59 |
| `aip/158_list_response_next_page_token` | 15 | 8/59 |
| `aip/148_standard_field_type` (written, not shipped) | 0 | 0/59 |
| `aip/161_field_mask_type` (written, not shipped) | 0 | 0/59 |

Two disagree with the estimate, and in opposite directions. AIP-158's
`page_size` was estimated at 8 and measures **14**. AIP-135 was estimated at 8
and measures **7**, because the rule accepts the soft-delete shape and
`google.longrunning.Operation` and the estimate accepted neither. An estimate is
not a conservative version of a measurement; it is a different number.

AIP-142's 111 reproduced exactly, and it is the one that mattered most: it is
the rule that forced the roll-up in §10.

## 4. Rule identity: `aip/<number>_<name>`

Existing ids are `stele/<name>` and the `stele` namespace is reserved for
built-ins. AIP rules get their own namespace, `aip`, and their names carry the
number:

```
aip/135_delete_returns_empty
aip/158_list_request_page_size
aip/158_list_response_next_page_token
```

Three alternatives were considered.

- **`aip/135` alone.** Rejected, and this is the decision that matters. One AIP
  implies several checks — AIP-158 alone is at least three — and an id is
  permanent once released. `aip/158` would have to become either a bag of
  unrelated findings that cannot be configured or ignored separately, or a name
  that has to be split later; splitting is a rename, and under `RELEASING.md` a
  rename does not fail loudly, it silently stops matching the ignore list that
  named it. The number alone buys tidiness now and forecloses the only future
  that is actually likely.
- **`aip/delete_returns_empty`, no number.** Rejected. The number is the one
  identity this guidance has that we do not own, it is what a reader searches
  for, and it is the join with the ledger. Dropping it means keeping the mapping
  somewhere else, which is the drift this whole document is about.
- **`stele/…` for everything.** Rejected. The namespace is what makes the
  profile a group without a second group list to maintain: "the AIP profile" is
  exactly the rules whose id begins `aip/`, derived rather than enumerated.

Reserving the `aip` namespace is itself a change and it should land before the
first `aip/` rule does, alongside the existing reservation of `stele`. **It has
landed**, as `rule.NamespaceAIP` and `rule.Reserved`, in the commit before the
first rule. It was free then and breaking later: a third-party rule that claims `aip/135_…` first is a
rule this tool would then have to either displace or work around.

The number is a prefix rather than a suffix so that ids sort into AIP order, and
the name after it is the same lower_snake_case as a built-in — an id should read
the same whoever wrote it.

## 5. The ledger that cannot rot

Two files, one derived and one written.

**`internal/aip/index.tsv` — derived.** Written by `internal/aip/aipsync` out of
a clone of upstream. One line per AIP: id, scope, state, category, whether the
body carries proto, title. It records the upstream commit it came from, so "what
upstream said when we last looked" is a fact in the repository rather than a
memory. It is never edited by hand, and the parser refuses rather than shrinks:
a file with no front matter, no id, no state or no heading is an error naming the
file, because a shorter inventory looks exactly like a complete one.

**`internal/aip/ledger.yaml` — written.** One entry per AIP, in four states:

- `implemented`, which must name rule ids;
- `candidate` — decidable, no rule yet, with what the rule would check and what
  it was measured at;
- `declined` — deliberately no rule, with why;
- `undecidable` — states a requirement no descriptor can decide, with what the
  missing knowledge is.

A fifth word, `untriaged`, exists so that "I have read this and I am not ready"
can be written down. It always fails.

**The gate**, `TestEveryAIPIsClassified`, is hermetic and runs in the ordinary
test job on every change. It fails when an AIP in the snapshot has no entry or an
untriaged one, naming the number *and the title* — the fix is to read an AIP and
form an opinion, and a failure that says only "AIP-149" sends the reader to a
browser first. It fails in the other direction too, on an entry for an AIP the
snapshot does not carry.

**The refresh**, `aipsync -check`, needs the network and runs in its own CI job
beside parity, on the same reasoning the parity split already uses: the
classification question is hermetic and must stay that way, and only "has
upstream moved" needs a clone.

And **the drift that has actually bitten this repository four times** is closed
by `TestImplementedAIPsNameLoadedRules`: the rule ids an entry claims are checked
against `lint.Builtin()`, not against a second list. A rule renamed or dropped
fails here rather than in somebody's reading of the docs. The reverse direction
is reported as a log line rather than an error: a built-in claimed by no AIP is
not a fault, since the eight were chosen against a fleet rather than against a
corpus. As it happens all eight are claimed today — by AIP-126, AIP-184 and
AIP-191 — which is a coincidence worth not enshrining as a rule.

### When upstream moves

- **An AIP is added.** It appears in the snapshot on the next refresh, and the
  hermetic test fails until it is classified. `aipsync` deliberately does not
  write a placeholder entry, not even `untriaged`: a generated placeholder is a
  green build with an unread AIP behind it.
- **An AIP changes state** — approved to withdrawn. The snapshot changes, the
  networked check fails, and the diff is in the review. The ledger keeps its
  entry: what we decided about AIP-142 does not stop being true because AIP-142
  was withdrawn, and deleting it would lose the reasoning. If a rule implements
  it, whether to keep the rule is a decision, and a decision should be made in a
  review.
- **An AIP is renumbered or removed.** It leaves the snapshot, and the hermetic
  test fails on an orphaned entry. This is the case worth the noise: a rule named
  `aip/158_…` after a renumbered AIP would cite guidance it was not written
  against.

The refresh is deliberately manual. A scheduled job that opened a pull request
would be fine; a job that regenerated the snapshot on `main` would not, because
the snapshot changing *is* the signal.

## 6. Adoption, and what `severity` does not give

> The conclusion of this section — that AIP rules must not be built-ins — was
> overruled. It is left as written, because a document that quietly agrees with
> whatever was decided last is not evidence of anything. §10 says what shipped
> and why this reasoning did not survive.

The fleet's contracts were not written to AIP, and the numbers in section 3 say
what that means: four checks would redden every file on day one.

**What `severity` gives.** `error`, `warning`, `off`, per rule, in `stele.yaml`,
plus an `ignore` list of import paths per rule and globally. `warning` is a real
adoption tool — it reports without failing — and it is in a file reviewed with
the contracts rather than in `allow_failure: true` on a CI job.

**What it does not give, said plainly.**

- *It does not protect new code.* `severity: warning` on AIP-142 means 111
  existing findings do not fail the build — and neither does the 112th, written
  tomorrow. A baseline is the mechanism that separates those two, it does not
  exist, and the roadmap already names it as the next thing this milestone
  needs. **Every profile rule that fires broadly is worth less than it looks
  until the baseline lands**, and that is an argument for landing the baseline
  before the broad rules, not after. *(It has since landed; §11.)*
- *It does not group.* Turning on twenty AIP rules today means twenty lines.
  The namespace makes the group derivable, and configuration should be able to
  name it; that is a small change to `internal/config` and it is not in this
  slice.
- *Nothing here is opt-out.* Every built-in runs unless configured otherwise, and
  the roadmap defends that: a rule that shipped switched off would ship dead.

Which forces the one decision this section exists for. **AIP rules must not be
built-ins.** If they were, adding them would redden every repository that
upgrades — a breaking change under `RELEASING.md`, taken repeatedly, for
guidance the repository never said it wanted. They are a profile a manifest opts
into by name, and within the profile `severity` and `ignore` work exactly as they
do for built-ins. That is a genuine asymmetry with the eight built-ins, and the
justification is the measurement: the built-ins were chosen because they cost a
fleet almost nothing, and these were measured and do not.

## 7. What the annotations cost

Roughly half the decidable AIPs are decidable only because of an annotation:
`google/api/resource.proto` for AIP-123 and AIP-122, `google/api/annotations.proto`
for AIP-127, `google/rpc/status.proto` for AIP-193, `google/longrunning/operations.proto`
for AIP-151. The measured fleet imports exactly one file from that set —
`google/api/field_behavior.proto`, in 2 of 35 files.

Three costs, in increasing order of unpleasantness.

1. **A dependency for the consumer.** Every repository adopting those rules
   declares `googleapis` in its manifest and pays for it in its vendored export.
   That is ordinary, and this tool's dependency handling is the part with the
   most evidence behind it.
2. **A dependency for the rule.** A rule reading `google.api.resource` needs the
   extension *type*, not only the name. The published `rule` interface hands a
   rule a linked `protoreflect.FileDescriptor`, so the extension is readable when
   the file being linted imports the annotation.
3. **The trap.** When a file does *not* import the annotation, the extension is
   simply absent — and "absent because the author did not annotate" is
   indistinguishable, at the descriptor, from "absent because this file is not
   linked against the annotation at all". A naive AIP-127 rule reports every file
   in an unannotated repository as missing HTTP bindings, which is exactly the
   35/35 line in section 3, and it would report the same thing about a repository
   that had annotated everything but whose descriptor set was assembled without
   the import. **A rule in this namespace must therefore check the import before
   it checks the annotation**, and report the missing import as its own finding
   with its own fix. That is a constraint on every annotation rule, and it is
   written here rather than discovered three rules in.

## 8. Deliberately not in this slice

- ~~**Any `aip/` rule.**~~ Five landed; see §10.
- ~~**Reserving the `aip` namespace in `rule`.**~~ Landed, before the first rule.
- **Profile selection in the manifest.** `lint.profiles` is sketched above and
  not designed; it is a change to a schema that is already released.
- ~~**The baseline.**~~ Landed, before the broad rules and for the reason §6
  gives. See §11.
- **Breaking-change detection.** AIP-180 is classified `undecidable` for a rule
  handed one file, which is a statement about the rule interface, not about the
  AIP.

## 9. Arguments against this, recorded

- **118 dispositions is a lot of prose to maintain.** True, and the mitigation is
  that 45 of them are one sentence about a vendor scope and will never be read
  again. The test does not check that a reason is *good*, only that it exists —
  a reason can rot into a sentence nobody believes, and no test catches that.
  What the test does catch is silence, which is the failure that actually
  happened here four times.
- **The snapshot is a copy of upstream, and copies drift.** It does — that is
  what `-check` is for — but the drift is loud rather than silent, and it is a CI
  failure with a diff, which is the strongest form available without depending on
  the network in every test run.
- **Pinning to a commit means we lint against guidance that may be months old.**
  Yes, deliberately: the alternative is a tool whose output changes overnight
  because somebody else edited a markdown file, which is exactly the argument
  `stele` already makes for pinning rule plugins.
- **A profile that nobody enables is a profile that ships dead** — the roadmap's
  own objection to rules that default to off, turned around and pointed at
  section 6. The honest answer is that it is a weaker position than the built-ins
  have, and it is accepted because the measured alternative is reddening every
  repository in a fleet on upgrade. If no repository enables the profile, that is
  evidence about the profile and it should be recorded, not explained away.

  *This is the argument that won.* See §10.
- **A roll-up is a place for findings to hide.** True. One line saying "111" is
  a line somebody can look at every day without ever reading what is behind it,
  and that is a real failure mode rather than a hypothetical: it is how
  `allow_failure: true` on a CI job behaves, only tidier. The defence is that
  the alternative measured worse — 111 printed lines are scrolled past rather
  than read — and that the count is on the line, so a number that grows is
  visible where a wall of identical lines is not. It is a defence, not a proof.

## 10. What shipped, and the decision that overruled section 6

Section 6 concluded that AIP rules must not be built-ins. The owner overruled
it. The rules are **on for every repository, and they warn**.

**Why the conclusion was wrong.** Section 6's argument was sound about the cost
and wrong about the alternative. It weighed "reddens every repository on
upgrade" against "a profile a manifest opts into", and did not weigh the third
option that §9 had already named as the profile's own fatal objection: a profile
nobody enables ships dead. The value of this guidance is entirely that it
reaches somebody who was not already looking for it — a repository that knows
enough about AIP to type `profiles: [aip]` is the one that needs the rules
least. And "reddens every repository" was never a property of the rules; it was
a property of shipping them at `error`. A warning reddens nothing.

**What that leaves, and it is the real problem.** 111 warnings from one rule.
One line per finding is not a signal: it is a wall a reader learns to scroll
past, and a rule whose output is scrolled past is off in every way but the
configuration. That is the same failure as the profile nobody enables, wearing a
different colour.

**So the output is rolled up.** A rule reporting more than `SummaryThreshold`
warnings prints one line — the count, the number of files, and the exact command
that prints the detail — instead of one line each:

```
stele: aip/142_timestamp_field_time_suffix: 111 warnings in 19 files; see `stele lint --rule aip/142_timestamp_field_time_suffix`
```

Four decisions inside that, and each was a real fork.

1. **What decides is severity, not namespace.** Errors are never rolled up: an
   error is the build failing now, and a failure that will not say what failed
   is not one anybody can act on. A warning is information the reader may act on
   later, and a count is enough to decide with. This answers "does summarising
   apply to `stele/` rules too" by mechanism rather than by special case — a
   `stele/` rule a repository has lowered to `warning` while it works through
   two hundred findings is rolled up too, and an `aip/` rule raised to `error`
   prints in full, which is exactly what raising it asked for. The alternative,
   "roll up the `aip` namespace", is a second rule about rules, and the second
   one drifts from the first. It also gets the `stele/` case wrong in both
   directions at once.
2. **There is a threshold, and it is five.** The number is not tuned and nothing
   rests on its value; what matters is that below it the detail is cheaper than
   the round trip to fetch it. Five findings are ten lines, because each carries
   its fix.
3. **The detail is one command, and the roll-up line names it.** A summary
   pointing at a flag the reader has to go and look up is a summary they do not
   follow. `--rule ID` narrows the run and prints everything it finds;
   `--all-findings` does it for every rule. A `--rule` naming a rule nothing
   carries is refused, and one the manifest switches `off` says so rather than
   printing an empty report that reads as a clean one.
4. **`severity` and `ignore` are untouched.** Rolling up happens when the report
   is rendered. It changes no count, no exit status and no configuration, and
   the run's own summary line still counts every warning including the rolled-up
   ones — so nothing is hidden, only indented differently.

**Which rules shipped, and the criterion that is not in section 3.** Five,
across three AIPs: AIP-135, AIP-142, and AIP-158's three fields. What kept the
others out was not only volume.

*A summary line that reads the same on every run of every repository teaches
nothing.* "142 of 142 methods carry no `google.api.http`" is not a finding about
142 methods; it is one bit of information about the repository — it has not
adopted the annotation — and printing it every run turns it into furniture. The
four broadest checks in §3 are all of that shape, and §7 gives the second reason
they cannot ship anyway: the rule cannot tell an unannotated file from a
descriptor set assembled without the import. The rules that shipped are the ones
whose count *falls as the work is done*, which is what makes the line worth
reading a second time.

The two rules written, measured at 0 and not shipped, are the other half of the
standard: a rule id is permanent, and 0 findings is not evidence for one.

**Is this a breaking change?** Yes, and the changelog records it as one. No
build fails that did not fail before, but `RELEASING.md` is written around the
bytes this tool writes, and every lint run's output changes. Under `0.x` that
bumps the minor. The alternative reading — that a warning cannot be breaking
because the exit status is unchanged — was rejected for the reason the policy
already gives about new built-in rules: the consumer's diff is the same size
either way, and "when in doubt, go up".

**What is still true from section 6.** `severity: warning` still does not
protect new code. The 112th `_at` field, written tomorrow, is warned about
exactly as the 111 existing ones are, and the reader cannot tell them apart. A
baseline is the mechanism that separates the two, it does not exist, and the
roll-up makes it *more* pressing rather than less: it makes a large standing
count comfortable to live with. That is the strongest argument against what
shipped here, and it is recorded rather than answered. *§11 answers it.*

## 11. The baseline, and what it does to sections 6 and 10

The objection recorded twice above is now met. `stele lint --update-baseline`
writes `stele.baseline`, a generated file committed and read in review beside
`stele.lock`; the findings it names cost nothing, and every other finding costs
what its severity says. The 112th `_at` field fails a run the 111 do not.

**Why the identity is a declaration and not a position.** (file, rule, line) is
the obvious answer, and it is the one that fails: inserting a line above a
finding moves every finding below it, so the file goes stale on an edit that
changed nothing it is about, people regenerate it without reading it, and a
regeneration nobody reads carries the new findings in with the old. An entry is
(import path, rule, subject, count), and the subject is the full name of the
declaration — `example.v1.Order.created_at`. It survives reformatting and
reordering and stops being the same name exactly when the declaration stops
being the same declaration.

**The engine derives it, and that is a constraint from §7's neighbourhood
rather than a convenience.** `rule.WireFinding` carries a line, a column, a
message and a fix, and nothing else, for the reasons `rule/wire.go` gives. An
identity that required a rule to name its own subject would work for the rules
that ship here and not for the ones that do not, and one interface that means
two things depending on which side of a process boundary a rule sits on is not
one interface.

**What this changes about §6.** Nothing about the asymmetry: `aip/` rules still
default to `warning` and `stele/` rules still default to `error`, and the
measurement that justified it is unchanged. What changes is the sentence that
followed it. "Every profile rule that fires broadly is worth less than it looks
until the baseline lands" was true and is no longer: a repository that wants
AIP-142 enforced can raise it to `error`, baseline the 111, and be protected
from the 112th on the same afternoon. The argument for landing the baseline
*before* the broad rules stands as it was written, and it is why this came
next.

**What it does not change.** A baseline is not evidence for a rule. The
standard §10 sets — a rule id is permanent, and 0 findings is not evidence for
one — is about whether a rule should exist, and a mechanism for living with a
rule's findings says nothing about that. The four broadest candidates in §3
still have not shipped, and the reason is still the one given there: a rule
that cannot tell an unannotated file from a descriptor set assembled without
the import reports the same count on every run of every repository, and a
baseline would hold that count as comfortably as it holds a real one.
