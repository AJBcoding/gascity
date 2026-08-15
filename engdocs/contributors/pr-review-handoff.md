# PR Review Handoff Notes

## Squash and Post-Merge Review Scope

When finalizing an adopted PR, the squash title and body must name every
substantive behavior change that lands in the commit. If a maintainer fixup
extends beyond the original PR title, include a short bullet for each added
scope in the squash body so post-merge reviewers, operators, and future bisects
can see the full change.

For PR #1513, the landed commit was titled for the polecat-to-refinery routing
fix, but it also changed two additional runtime behaviors:

- Supervisor-managed cities now keep per-city API routes unavailable until
  startup reconciliation has completed and `CityRuntime.OnStarted` marks the
  city running.
- Session transcript streams now rely on the log watcher loop's immediate first
  read after the caller's initial history load, so writes in that gap are
  reloaded before the stream blocks for later file notifications.

Future review finalization should record comparable bundled changes in the
public review comment or maintainer handoff notes before applying
`status/merge-ready`.

## The fork-PR review loop with gastownhall/gascity

Everything below exists because of one permission fact: the AJBcoding
account has **pull-only** access to gastownhall/gascity (admin:false,
maintain:false, push:false, triage:false, pull:true). Every gc PR is a fork
PR (AJBcoding/gascity → gastownhall/gascity), and this account cannot
approve, merge, auto-merge, or — the part that shapes this whole page —
**request a review**. Both re-request mechanisms fail:

- `POST /repos/gastownhall/gascity/pulls/N/requested_reviewers` → **404**,
  with `requested_reviewers` unchanged (`[]`) afterward.
- `gh pr edit N --add-reviewer <login>` → `GraphQL: AJBcoding does not have
  the correct permissions to execute RequestReviewsByLogin` — and **exits
  0** despite failing (see the hazard section below).

After a reviewer submits CHANGES_REQUESTED, the PR leaves their queue.
Pushing fixes and replying does **not** put it back — only a re-request
does, and we cannot issue one. Left alone, every answered
CHANGES_REQUESTED PR therefore stalls silently in both directions at once:
the reviewer sees nothing (no queue entry, no notification), and our side's
state fields still say the review is unanswered. Three PRs sat exactly like
this from 08-09 to 08-10 (#5116, #5119, #5120). The two sections below are
the working procedure; the permission ask itself is tracked in `gas-hwii`.

## Answering a review: end with an @-mention ping

Answering a review — pushing fix commits, replying to findings, filing
scoped-out beads — **ends with a comment that @-mentions the reviewer**,
naming what changed and that it is ready for re-review:

```
@sjarmak — all three findings addressed in <sha> (or: filed as scoped-out
beads gas-xxxx, gas-yyyy per your suggestion). Ready for re-review.
```

The @-mention is not politeness; it is the **only notification channel this
account has**. A re-review request is impossible (above), and a push or a
plain reply generates no notification for a reviewer whose queue no longer
contains the PR. An answer without an @-mention ping is operationally
unfinished — the reviewer will never learn it exists.

## Whose court is a PR in? The timeline court test

Whose court a PR sits in is decided by **the newest human event in the
`issues/N/comments` + `pulls/N/reviews` timeline, compared against our
newest commit/comment**. Never by `reviewDecision`, `reviews`, or
`requested_reviewers` — every one of those state fields has already
produced a wrong dispatch decision in this repo, in both directions:

- **Sticky CHANGES_REQUESTED makes an ANSWERED review look unanswered.**
  `reviewDecision` stays `CHANGES_REQUESTED` until the reviewer submits a
  *new* formal review — which they will not, because the PR left their
  queue and we cannot re-request. #5116, #5119, and #5120 sat fully
  answered from 08-09 to 08-10 while a mayor hold-retest re-derived
  "still blocked on us" from `reviewDecision` and dispatched three
  polecats to redo work that was already done.
- **Reviews posted as plain issue comments make an ACTIVE review look
  absent.** A reviewer who posts findings as issue comments (as sjarmak
  did twice on #5103) leaves `reviews: 0`, `reviewDecision: ""`, and
  `requested_reviewers: []`. Any check reading review state concludes
  "nobody has reviewed this" while the comment timeline says the
  opposite — #5103 spent 29 hours of a P1 critical path misfiled as an
  unbounded external wait (gas-0wqy).

The test itself — gather the human events, take the newest, compare
against our newest action:

```bash
PR=5116 REPO=gastownhall/gascity
{
  gh api "repos/$REPO/issues/$PR/comments" --paginate \
    --jq '.[] | {at: .created_at,  who: .user.login, kind: "comment"}'
  gh api "repos/$REPO/pulls/$PR/reviews" --paginate \
    --jq '.[] | {at: .submitted_at, who: .user.login, kind: "review"}'
} | jq -s 'sort_by(.at) | last(.[])'
```

(Inline review-thread replies live in `pulls/N/comments`; include that
lane too when threads are in play.) Compare that newest human event
against our newest commit push (`gh pr view N --json commits`) and our
newest comment:

- Newest event is **theirs** (a review or a comment we have not answered)
  → the ball is **ours**. Answer it, ending with the @-mention ping.
- Newest event is **ours** (fix commits plus the @-mention ping) → the
  ball is **theirs**, regardless of what `reviewDecision` still claims.
  A hold-retest must conclude "waiting on reviewer", not "unanswered
  review" — do not dispatch work to redo it.

## Hazard: `gh pr edit --add-reviewer` fails while exiting 0

`gh pr edit N --add-reviewer <login>` prints the GraphQL permission error
to stderr and **exits 0**. Verified live by gastown.slit: the command
reports failure text while `$?` is 0 and `requested_reviewers` stays
empty. Do **not** write a checker, retry loop, or handoff gate around its
exit code — it will report success forever. If some future permission
change makes re-requests possible, verify by reading state back
(`gh pr view N --json reviewRequests`), never by the edit's exit status.

## The standing permission ask

The durable fix is account capability, not procedure: AJBcoding needs
permission to request reviews on gastownhall/gascity (triage-level access
suffices; it grants no code write), or an explicitly agreed re-review
signal with the maintainers (recent mergers: quad341, julianknutsen).
That is an operator/GitHub-org action, not an agent one. The ask is
surfaced as **`gas-hwii`** (`hold:mayor`); the operator records the
decision on that bead either way — granted, alternative signal agreed, or
declined with the @-mention convention made permanent.
