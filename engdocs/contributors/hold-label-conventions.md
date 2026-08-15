---
title: Hold and Blocked Label Conventions
description: The canonical hold/blocked label taxonomy for this project's own bd tracker — which label to use, how to set and clear one so both edges stay audited, when to use status or a dependency edge instead, and what happened to the old ad hoc labels.
---

## Why this exists

Before 2026-07-14 this repo's bd tracker had accumulated at least 8 overlapping
ad hoc "hold"-family labels with unclear, possibly inconsistent semantics
(`arch-hold`, `blocked`, `blocked-by-operator`, `blocked-on-external`,
`blocked-on-upstream`, `blocked-prereq`, `human-hold`/`human`, `on-hold`),
alongside the one label that already followed the sanctioned convention,
`hold:mayor`. `ga-tug8ry` audited and consolidated them down to two canonical
values; `ga-tug8ry.2` migrated every live bead onto the result. This page is
the durable reference so nobody reinvents another ad hoc hold label — if
you're about to pause a bead and reach for a new label name, stop and use one
of the two values below instead.

Full rationale and the live census this decision was based on:
`bd show ga-tug8ry.1` (the decision) and `bd show ga-tug8ry.2` (the
migration record, including before/after counts).

## Three orthogonal "not ready" mechanisms

A bead can be "not simply ready to work" for three structurally different
reasons. Pick the mechanism that matches *why* you're pausing it, not just
"it's blocked":

| Mechanism | How to set it | Meaning |
|---|---|---|
| Dependency edge | `bd dep add <a> <b>` | Bead A cannot start until bd-tracked bead B closes. Gates `bd ready`. Computed from real edges, not a manual claim. |
| Bead status | `bd update <id> --status blocked` | "I cannot currently proceed," with no further structure about why or who must act. |
| `hold:<value>` label | `bd set-state <id> hold=<value> --reason "..."` | "I am paused pending a specific actor or condition." Structured, audited (files an event bead), and names the *who*. |

These are orthogonal and combine freely — a bead can be `status=blocked`
**and** `hold:external` at the same time. Use a dependency edge when the
blocker is itself a bd bead; use `status=blocked` when nothing more specific
applies; use `hold:<value>` only when a specific actor or external condition
is the actual reason you're paused.

## Canonical `hold:<value>` values

Only two values are canonical. Don't introduce a third without a new
architecture decision — see `ga-tug8ry.1` for the reasoning that narrowed
the taxonomy to these two.

- **`hold:mayor`** — the required next actor is the mayor. Covers both
  mayor-initiated pauses and automation-escalated-to-mayor cases; both are
  the same operational state ("nothing proceeds until the mayor acts") and
  share one value rather than being split in two.
- **`hold:external`** — the required next actor or condition is outside this
  bd instance's control (an external repo's maintainers, an upstream PR
  merge, etc.). Established by `ga-h7hnpt`.

Set either with the sanctioned command — never with a plain `bd label add`:

```bash
bd set-state <id> hold=mayor --reason "why, and who/what unblocks it"
bd set-state <id> hold=external --reason "why, and who/what unblocks it"
```

`bd set-state` removes any existing label in the `hold:` dimension, adds the
new one, and files an audit event bead. It does **not** touch `status`,
`owner`, or `metadata` — update those separately (or add a dependency edge)
if they also need to change.

### Retesting a hold that waits on an upstream PR review

When a held bead's external condition is "an upstream PR review", whether
that condition has moved is decided by the **timeline court test** in
[pr-review-handoff.md](pr-review-handoff.md): the newest human event in the
PR's `issues/N/comments` + `pulls/N/reviews` timeline, compared against our
newest commit/comment — **never** by `reviewDecision`, `reviews`, or
`requested_reviewers`. Both directions of the state-field trap have bitten
live hold-retests in this repo: sticky `CHANGES_REQUESTED` made three
answered PRs (#5116/#5119/#5120) look unanswered and re-dispatched finished
work, and a review posted as plain issue comments made an active review on
#5103 look absent for 29 hours (gas-0wqy). The same page records the
@-mention ping convention and the `gh pr edit --add-reviewer`
exit-0-on-failure hazard.

## Clearing a hold

Releasing a hold is the same kind of write as setting one, so it uses the same
command. The value is `none`:

```bash
bd set-state <id> hold=none --reason "what changed, and why it can proceed now"
```

That removes whatever `hold:` label the bead carried, adds `hold:none`, and
files the event bead recording the release:

```
✓ Set hold = none on <id>
  Previous: external
  Event: <id>.2          # "State change: hold → none"
                         # "Changed hold from external to none / Reason: ..."
```

**Never release a hold with a plain label removal:**

```bash
bd update <id> --remove-label hold:external   # WRONG — files no event
```

`bd set-state` files the audit event bead; `--remove-label` does not. The plain
removal is the same mistake as setting a hold with `bd label add`, and it leaves
holds audited on the way *in* and unaudited on the way *out*. The event ledger —
the declared source of truth, of which the label is only a fast lookup cache —
then drifts permanently from that cache, and drifts in one direction only, so
"was this bead ever held, and who released it?" becomes unanswerable from the
ledger alone (`gas-x284`).

### `hold:none` is a release marker, not a third hold value

The two canonical *hold* values are still `hold:mayor` and `hold:external`.
`hold:none` records the **absence** of a hold, so it does not widen that
taxonomy and needs no new architecture decision. Keeping it as a visible marker
rather than a bare deletion is the point: the label cache then agrees with the
event ledger about what happened, which is exactly what the plain removal breaks.

Code must therefore treat `hold:none` as *not held*:

- It stays out of `beadmeta.DispatchHoldLabels`, so a released bead is ordinary
  dispatchable work again. Adding it there would starve released beads the same
  way an unlifted hold does (`gas-kg6`).
- Consumers that match the `hold:` **prefix** instead of that exact set must
  exempt it explicitly. `holdLabelValue` in `cmd/gc/doctor_hold_label_routed_to.go`
  is the live example: without the exemption, `gc doctor --fix` reads `hold:none`
  as a routing target and backfills `gc.routed_to="none"` over the bead's real
  route — corrupting the sole persisted routing key on every released bead.

### Clearing a hold does not change the assignment

`bd set-state` never touches `status`, `owner`, `assignee`, or `metadata` —
setting a hold or clearing one. That is deliberate, and it matches what
`gas-kg6` settled from the other side: a hold changes what dispatch will
**serve**, never who **owns** the bead. The dispatch paths filter held beads;
the assignment stays a real ownership fact throughout, so a parked bead's owner
never goes invisible to the pool or to crash recovery.

Clearing the hold therefore just restores the bead to its existing assignee —
the next `gc hook --claim` from that session serves it again.

One case needs a second, deliberate step. If the assignee is gone (pool workers
mint a new identity on every restart), a cleared bead is owned by a session that
will never ask for it again: no hold filter hides it, but unassigned pool routing
will not pick it up either. Release the assignment explicitly:

```bash
bd set-state <id> hold=none --reason "..."
bd update <id> --status=open --assignee=""
```

## Retired labels

These labels are legacy. If you see one on a live bead, treat it as drift
worth a bug report, not a pattern to follow.

| Legacy label | Replace with | Notes |
|---|---|---|
| `blocked-by-operator` | `hold:mayor` | "Operator" meant the human operator/mayor seat. |
| `blocked-on-upstream` | `hold:mayor` | Means "next step in our own merge pipeline," not an external repo — despite the name, this is not a `hold:external` synonym. |
| `human-hold`, bare `human` | `hold:mayor` | Both named the same "next actor is mayor" state as a bare label. Caution: a bare `human` label can also appear alone for an unrelated reason (a human merge/PR action needed) that is not a hold state at all — check the bead's own context before assuming `human` implies a hold. |
| `blocked-on-external` | `hold:external` | Direct predecessor of `hold:external`; carry forward any `blocker_scope`/`external_blocker`/`external_pr`/`pr`/`repo` metadata unchanged. |
| `blocked` | none — use native `status=blocked` | Redundant with the bead's own `Status` field; keeping both invites drift between them. |
| `arch-hold` | none — owned by the `maintainer-pr-review` pack | Not a generic bd hold; it's that pack's own gate, cleared via `gc maintainer-pr-review clear-hold`. It only looked like one of ours because it lacks the `mpr-` prefix its sibling `mpr-human-hold` carries. |
| `blocked-prereq` | none today; if it recurs, use a dependency edge (prerequisite is a bd bead) or `hold:external` with PR numbers recorded in metadata (prerequisite is bare GitHub PR numbers) | Historical: blocked on specific GitHub PRs merging first, with no corresponding bd bead. |
| `on-hold` | none — already superseded | Any bead needing this should already carry the canonical `hold:mayor`/`hold:external` in its place. |

**Explicitly out of scope — do not migrate these, they mean something
different:**

- `mpr-human-hold` and other `mpr-*` labels — owned end-to-end by the
  `maintainer-pr-review` pack, with its own metadata namespace and its own
  clearing tool. Not a generic bd hold label.
- `build-blocker`, `ci-blocker`, `pre-push-blocker`, `push-blocking`,
  `test-blocker` — a different semantic axis ("pipeline stage X is red
  because of me"), not "I am waiting on decision-maker Y."
- `needs-mayor` / `needs-mayor-decision` — a routing/queue-placement label
  (parallel to `needs-architecture`, `needs-design`, `needs-pm`,
  `ready-to-build`), not a pause-state label. It may legitimately co-occur
  with `hold:mayor`.

## This is a data convention, not SDK behavior

Nothing in this page requires or implies special-casing any role name in Go.
`hold:mayor` and `hold:external` are plain label values in this project's own
bd data, and *which* value to reach for is enforced by convention — this
document, PR review, and `bd set-state`'s dimension semantics — not by SDK
code.

Gas City's "ZERO hardcoded roles" invariant is unaffected. The dispatcher does
read these two values, from the single shared definition in
`internal/beadmeta/hold_labels.go` (`DispatchHoldLabels`), so that a bead
parked on a hold is not handed to a worker as actionable work. That is a check
for the *presence of a label value*, not a branch on a role: no Go code knows
or cares who "mayor" is, and renaming the seat would not change a line of it.
Do not re-spell either literal at a call site — import the constants, so the
label set stays defined exactly once.

Two questions consume that list and they answer oppositely; `DispatchHoldLabels`
documents the split in full. In short: filter on holds when deciding what an
agent should **do** (route-scoped dispatch, and any path serving a bead as
work — `ga-5736js`, `gas-kg6`), never when deciding which sessions still need
to **exist** (pool demand and crash-recovery accounting stay hold-transparent,
or a parked bead's owner goes invisible).

## See also

- `bd show ga-tug8ry.1` — the architecture decision: full live census,
  per-label disposition rationale, and a label-flow diagram.
- `bd show ga-tug8ry.2` — the migration record: before/after counts and the
  beads intentionally skipped (bare `human` used for an unrelated reason).
- [Beads architecture](../architecture/beads.md) — the generic `Label` and
  `Store` mechanism this convention is built on.
