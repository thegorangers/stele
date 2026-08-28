# Breaking-change detection

Status: design, approved for planning. Not implemented.

## What this is for

`stele` generates code from contracts and pins the contracts it consumes. It
cannot yet answer the question every consumer of those contracts has: **did this
change break me?**

Two people ask it, and they are not the same person:

- The **producer**, changing a contract, asking whether consumers will break.
- The **consumer**, taking a newer revision of a dependency, asking whether
  anything they actually import has changed under them.

One comparison engine answers both. What differs is only where the *previous*
revision comes from. This document specifies the producer side in full and states
what the consumer side inherits; the consumer side is deliberately a later slice,
because the engine is the expensive part and the producer side is the reason a
tool like this gets installed at all.

## What counts as breaking

Two categories, both fatal, distinguished because they fail differently and a
report that conflates them cannot say what will happen:

- **Wire.** Already-serialised bytes stop being read correctly. Field numbers,
  type changes across incompatible encodings, cardinality, movement between
  distinct oneofs, enum value numbers, and — because in gRPC a name is a path on
  the wire — renaming or removing a package, service or method, changing a
  method's request or response type, or changing whether it streams.
- **Source.** The bytes survive; a consumer's code no longer compiles. Field
  renames at a stable number, removals, removal or rename of a message, enum or
  enum value, a changed `go_package`, and moving a field into a oneof.

A third category — JSON and behavioural compatibility — is **not defined**. It is
not "off"; it does not exist. Defining it and defaulting it to off would be a
promise the tool does not keep, and the changes that matter most there (a field
rename) are already source-breaking and already caught.

### The type-compatibility matrix is measured, not written

The paragraph above is a summary written by hand, and this section exists because
hand-written enumerations in this project have been wrong four revisions running.

Wire compatibility between scalar types has places where intuition is wrong:
`int32`, `int64`, `uint32`, `uint64`, `bool` and enums share an encoding while
`sint32` — which looks adjacent — does not; `string` and `bytes` are not
symmetrically interchangeable; `map<k,v>` and a `repeated` entry message are the
same bytes and a different API; moving a field *out of* a oneof is wire-safe and
moving it *in* is wire-safe but source-breaking.

**The matrix is therefore built by a test, not stated in this document.** For each
ordered pair of scalar types, a value is encoded as the first and decoded as the
second; the outcome declares compatibility. This specification fixes the method.
Where the summary above disagrees with the measurement, the measurement is right
and the summary is a defect.

## Producer side: the mechanism

    stele breaking [--against <ref>]

**The previous revision defaults to the merge-base** of `HEAD` and the base branch
named by `breaking.base` in the manifest — never the tip of that branch. Comparing
against the tip makes a neighbour's merged breaking change fail your unrelated
branch, which trains a team to switch the check off. Where several people or
agents work one repository at once, this is not hypothetical.

If no base is configured and none can be derived, the run fails naming that. It
never guesses a branch name.

**Both sides are compiled, not parsed.** Source compatibility needs resolved
types: a field removed from an imported message is invisible without resolution.
`internal/compile` already produces a `FileDescriptorSet`.

**The previous revision is compiled with its own dependencies.** It had its own
`stele.yaml` and `stele.lock`. Compiling yesterday's protos against today's pins
either fails outright or attributes another repository's change to this one. The
whole revision is materialised — manifest and lock included — and resolved on its
own terms. The fetch cache makes this nearly free, since those commits are
already pinned and usually already present.

**Only modules this repository owns are compared**, exactly as `stele lint`
scopes itself. A dependency changing beneath you is the consumer side's question,
and the producer side does not pretend to answer it.

**Files are matched by import path, not by path on disk.** Renaming a file is a
removal plus an addition, and for anyone who imports it that is a break; it is
reported as one.

**Positions.** A finding anchors to the site of the change in the current
revision. A removed field has no position in the current revision at all, and
removals are a large share of all findings, so the rule is stated rather than
left to the implementation: the anchor is the nearest surviving declaration — the
message the field left — and the removed element is named in the message text.
When an entire file is gone, the finding carries the path and no line.

**Rule ids live in the `break/` namespace**, one per kind of change, and are a
public contract on the terms `RELEASING.md` already sets for `stele/` and `aip/`.

## Adoption, and the valve

**There is no baseline, and none is needed.** This is a consequence of comparing
against a merge-base rather than against history: on the day the check is enabled
there is nothing behind it to compare, so there is no backlog. Where lint arrived
with 205 findings across the fleet, this arrives green everywhere by
construction.

**Severity is not configurable.** For lint, severity is the adoption mechanism and
that is right, because a stylistic rule can legitimately not apply. "Breaks
consumers, but only a warning" is `allow_failure: true` with more steps — the
failure mode the lint design already names as how protection dies.

Legitimate breaking changes exist, so the valve is **permission for one specific
change, not for a rule**:

    breaking:
      base: master
      allow:
        - rule: break/field_removed
          subject: example.orders.v1.Order.deprecated_eta
          reason: read by no consumer, checked across the fleet

`reason` is required. A permission with no stated reason is the thing that rots:
six months later nobody can tell a decision from a workaround.

**Permissions expire without dates.** A date is something people move. Once the
change is merged, the base already contains the new shape, the comparison has
nothing to find, and the entry matches nothing — it is stale by the same
mechanism the lint baseline already uses, with no new concept.

**A stale permission is an error, which is the opposite of the baseline's
default, deliberately.** A stale baseline entry is the record of a debt already
paid; it is harmless, and failing on it would punish the person who fixed
something. A stale permission is a *standing licence to break* a named
declaration that nobody intends to break any more. It is harmful in exactly the
way this whole mechanism exists to prevent.

The cost is real and is stated rather than hidden: **after the merge, the base
branch goes red until the line is deleted.** The trade is an hour of red against
a permanent standing permission, and it is softened by the report naming the
exact lines and by `stele breaking --prune`, which deletes them and leaves a
reviewable diff.

**Limit, the same one the baseline has:** a permission addresses a (rule, subject)
pair, so two breakages of one rule against one declaration are not distinguished.

## Failure behaviour

Silence is forbidden. Every way of failing to compare is an error naming itself,
never an empty report, because an empty report is indistinguishable from a clean
one and that is how a check like this dies without anyone noticing.

- **A shallow clone.** GitLab CI clones to depth 20 by default, and a merge-base
  with the base branch may not exist in such a clone. Degrading to a comparison
  against the branch tip is tempting and is exactly the neighbour-blaming failure
  this design rejects. The run fails, naming the depth and what to set.
- **The previous revision does not compile** — a pin gone from its origin, or no
  network. An error, never "no breaking changes found".
- **The previous revision has no manifest.** This is the commit that introduces
  `stele`. There is nothing to compare; the run says so and exits zero. A
  legitimate case, not a failure.

## Evidence

Three layers. The third matters more than the first two.

1. **Paired fixtures, one per rule, in both directions.** One proves the rule
   fires on the breaking change; the other proves it stays silent on a legal
   change of the same shape. A rule with only the first half is not known to
   detect what it claims.
2. **The type matrix**, generated by encoding and decoding as specified above.
3. **A replay over the fleet's real history.** Every merged change across the
   thirteen contract-owning repositories is replayed through the detector. Both
   outcomes are informative: firing nowhere means either the fleet never broke a
   contract — separately checkable — or the detector is dead, and that is worth
   knowing before it is switched on rather than after. Firing somewhere yields a
   list of breakages that already happened and already cost somebody time, which
   is the only evidence of value that cannot be manufactured.

This third layer is the project's own rule about acceptance: a library with no
consumer is unproven however many reviews it passed. Here the consumer is the
history.

## Consumer side, inherited and deferred

The consumer side reuses the comparison engine unchanged and changes only where
the previous revision comes from: the commit pinned in `stele.lock` against the
commit an update would take. Two things it gains for free that the producer side
cannot have — the import closure already knows which files this repository
actually uses, so the report can say "two of these breakages reach you, and here
is what imports them" rather than counting changes in a dependency.

It also closes a hole the producer side leaves open and does not claim to close:
a merge-base comparison protects the base branch, but a consumer pinned to a
commit in the middle of your history is unaffected by the base branch's opinion.
A breaking change merged and later reverted still happened, for them.
