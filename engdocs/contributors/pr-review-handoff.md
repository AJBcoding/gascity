---
title: PR Review Handoff Notes
description: How to tell whose court an open PR is in, why GitHub's own state fields cannot tell you, the @-mention convention that keeps an answered review visible, and the squash/post-merge review scope rule.
---

# PR Review Handoff Notes

## Whose court is this PR in?

**Decide it from the newest human event in the timeline, never from a state
field.** Compare the newest entry across *both* `issues/N/comments` and
`pulls/N/reviews` against our own newest commit or comment. If our activity is
newer, the ball is with the reviewer, whatever any status field says.

```bash
gh api repos/O/R/pulls/N/reviews   --jq '.[-1] | "\(.submitted_at) \(.user.login) \(.state)"'
gh api repos/O/R/issues/N/comments --jq '.[-1] | "\(.created_at) \(.user.login)"'
gh api repos/O/R/pulls/N/commits   --jq '.[-1].commit.author.date'
```

`reviewDecision`, `reviews`, and `requested_reviewers` are **not** oracles for
this. Each fails, and they fail in opposite directions, so neither a "reviewed"
nor an "unreviewed" reading can be trusted on its own:

- **Sticky `CHANGES_REQUESTED` makes an answered review look unanswered.**
  GitHub holds that decision until the reviewer submits a *new* review. Pushing
  fixes and replying does not clear it. On 2026-08-09/10, PRs #5116, #5119 and
  #5120 had all been fixed and answered, still read `CHANGES_REQUESTED`, and a
  hold retest re-derived "ball is in our court" from that field and dispatched
  three agents to redo finished work.
- **A review posted as plain issue comments makes an active review look
  absent.** A reviewer who writes findings as ordinary comments instead of a
  formal review leaves `reviews: 0`, `reviewDecision: ""`, and
  `requested_reviewers: []` — every state field says "nobody has reviewed
  this" while real findings sit in the timeline. PR #5103 was filed citywide as
  "blocked on review, not ours to unblock" and sat ~29h while the ball was
  actually ours; it gated four other beads.

Both directions are live in this repo. Reading the timeline costs one extra API
call and is immune to both.

## Always end a review response with an @-mention ping

**An @-mention comment is the only notification channel available to this
account.** Make it the last step of answering any review.

After a `CHANGES_REQUESTED` review, the PR leaves the reviewer's queue. Pushing
fixes and replying does not put it back — only a re-request does, and this
account cannot re-request. Both mechanisms fail:

```bash
# 404; requested_reviewers is still [] afterward
gh api -X POST repos/O/R/pulls/N/requested_reviewers -f 'reviewers[]=<login>'

# GraphQL: <user> does not have the correct permissions to execute
# `RequestReviewsByLogin` (requestReviewsByLogin)
gh pr edit N --repo O/R --add-reviewer <login>
```

So an answered review is invisible to the reviewer *and* reads as unanswered to
us. Both halves point the same wrong way, which is why nothing in the loop
notices on its own. Worth stating in the ping that the sticky decision is not
outstanding work on our side, so the reviewer does not read it as ours.

**Hazard: `gh pr edit --add-reviewer` exits 0 when it fails.** The permission
error goes to stderr and the exit status is still 0. Do not write a checker
around its exit code — it will report success forever. Verify by re-reading the
list instead, and give the read a positive control so an empty result cannot be
a silent failure:

```bash
# The real check: did the reviewer actually get re-requested?
gh api repos/O/R/pulls/N --jq '[.requested_reviewers[]?.login]'
# Positive control in the same window, so an empty list above cannot be a
# silent API failure masquerading as "nobody is requested".
gh api repos/O/R/pulls/N --jq '.number'
```

Note the asymmetry that makes this confusing to diagnose: **reading**
`requested_reviewers` works fine and returns `[]`. It is the **write** that is
refused. The empty list *is* the symptom, not an error you can catch.

The underlying permission is an operator-level ask, tracked on `gas-9f29`; until
it changes, the ping convention is the whole mitigation.

## Squash and post-merge review scope

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

## See also

- [Hold and blocked label conventions](hold-label-conventions.md) — when a PR
  wait is genuinely `hold:external` versus work still in our court. A hold
  whose premise has expired is indistinguishable from a valid one until
  somebody re-measures, so pair any `hold:external` on a PR with the timeline
  test above.
