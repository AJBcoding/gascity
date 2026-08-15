---
title: Community Fix Viability — 30-Day Discord and GitHub Review
description: Ranked Gas City reliability fixes and adjacent tools found through community reports and public repositories, verified on 2026-08-15.
---

This review mines the prior 30 days of the Gas Town Hall `#gas-city` and
`#troubleshooting` channels for contributors who reported comparable resource,
delivery, worktree, and orchestration failures or linked public fixes. Their
public Gas City, Gas City Packs, Beads, Herdr, and adjacent agent repositories
and upstream pull requests were checked against the local failure set.

Status, mergeability, test counts, repository activity, and licensing notes are
snapshots from **2026-08-15**. Recheck them before implementation.

## Verdict

The highest return is a small reliability bundle, not the large experimental
workspace or runtime branches:

1. Remove self-amplifying status-line, Boot, and duplicate patrol work.
2. Make Herdr and Codex delivery observable instead of trusting a successful
   transport return.
3. Stop standing alias conflicts from creating an event and process storm.
4. Add read-only operational visibility before attempting architectural
   replacements.

The three Gas City Packs changes are the cleanest first batch. The core Gas
City fixes map more directly to stranded agents and resource exhaustion but
require manual ports because their branches conflict with current main.

## Contributors and public work reviewed

- [`atbrace`](https://github.com/atbrace): Gas City, Gas City Packs, and Beads
  forks; authored the strongest status-line, Boot, alias-conflict, detached
  poller, and Codex-submit candidates in this review.
- [`nonathaj`](https://github.com/nonathaj): Gas City, Gas City Packs, Herdr,
  Herdr Triage, and Herdr Lazy forks; authored the Herdr submission-confirmation
  fix.
- [`sjarmak`](https://github.com/sjarmak): broad Gas City and Packs work,
  including transactional workspace ownership, Herdr reconciliation, bounded
  archive lookup, and city-scoped project-lead patrols.
- [`Wldc4rd`](https://github.com/Wldc4rd): `gc-debug`, Cairn, Bartertown, and
  several Gas City operational fixes.
- [`davidawad`](https://github.com/davidawad): `oil-cop` observability and
  `chwrite` file protection.
- [`phall1`](https://github.com/phall1): Blackbird coordination, Phux terminal
  runtime, and OpenCode worktree tooling.
- [`duncan4123`](https://github.com/duncan4123): experimental PostgreSQL and
  DoltLite Beads backends and larger Gas City plugin/backend branches.

Other Discord identities either had no verifiable public GitHub mapping, no
relevant public repository, or only unpublished operational work. No identity
was assigned from a display-name guess alone.

## Highest-return fixes

| Priority | Candidate | Evidence and fit | Recommendation |
| --- | --- | --- | --- |
| 1 | [`gascity-packs#207`](https://github.com/gastownhall/gascity-packs/pull/207), bound and single-flight `status-line.sh` queries (`atbrace`) | One file, mergeable, 4/4 checks green. The existing script runs `gc hook` and `gc mail check` without a bound when macOS lacks GNU `timeout`, and has no single-flight lock. The author measured a slow concurrent render falling from 28s to 0.12s by serving stale cache. | Port immediately. This is the smallest direct reduction in Mac process and store-query amplification. |
| 2 | [`gascity-packs#219`](https://github.com/gastownhall/gascity-packs/pull/219), deterministic stuck probe before Boot reasoning (`atbrace`) | One file, mergeable, 4/4 checks green. A 24-hour field sample recorded 134 Boot reasoning passes, 601 read-only `gc` calls, zero interventions, and the third-highest token consumption in that city. | Port immediately. It removes automated token and process churn while retaining reasoning for a real stuck signal. |
| 3 | [`gascity#4894`](https://github.com/gastownhall/gascity/pull/4894), confirm Herdr nudge submission (`nonathaj`) | Two files, 350 additions, no failed checks, but conflicting. It targets `herdr agent prompt` reporting success before the TUI is listening, leaving assigned agents idle forever. Current `internal/runtime/herdr/client.go` trusts that successful return and does not model `interactive_ready`. | Manually port against current main with readiness, submit-confirmation, and startup-nudge regressions. |
| 4 | [`gascity#4595`](https://github.com/gastownhall/gascity/pull/4595), back off deferred-singleton alias-conflict writes (`atbrace`) | Two files, 80 additions, no failed checks, but conflicting. In the author's field case, 96% of `bead.updated` events were conflict re-records; the resulting `bd` fork storm drove load to roughly 77 on six cores. The triggering `retryDeferredSingleton` debounce bypass remains in `cmd/gc/session_beads.go`. | Confirm the event signature, then port. With a standing canonical alias conflict, this may be the largest CPU/Dolt win in the set. |
| 5 | [`gascity-packs#267`](https://github.com/gastownhall/gascity-packs/pull/267), make `patrol-project-leads` city-scoped (`sjarmak`) | Four files, 186 additions, mergeable, four successful checks and no failures. The existing order enumerates all project leads but can be materialized once per importing rig, multiplying identical citywide work. | Include with #207 and #219 as one pack batch. It should reduce agent starts, store reads, events, and token burn. |
| 6 | [`gascity#5122`](https://github.com/gastownhall/gascity/pull/5122), confirm Codex submit instead of limiting verification to Claude (`atbrace`) | Six files, 154 additions, conflicting, with 60 successful and four failed checks. It matches the composer-strand class: text is present, Enter was lost, and liveness remains green. `internal/runtime/tmux/tmux.go` currently makes `submitVerifyEligible` true only for Claude. | High-value follow-up, but explain and clear the failed checks before porting. Preserve pane-content safeguards so stale human text is never submitted automatically. |
| 7 | [`gascity#5203`](https://github.com/gastownhall/gascity/pull/5203), bound archived controller-start lookup (`sjarmak`) | Four files, 354 additions, mergeable, 69 successful checks and no failures. A 70-archive field case spent more than 110 seconds of CPU in the fallback used by both `gc doctor` and supervisor order dispatch. | Port if timing confirms archive lookup is material locally. |

## Public tools worth piloting

### Oil Cop

[`davidawad/oil-cop`](https://github.com/davidawad/oil-cop) is the best
immediate observability addition. It is MIT-licensed and read-only, works
through `gc` and `bd` JSON rather than a second database, and exposes queue,
agent, DAG, staleness, and landed-but-not-closed state. Use it to capture a
before/after baseline for the pack and delivery fixes.

The repository claims 102 tests, but no recent GitHub Actions runs were
published at review time. Run its suite locally before relying on it
operationally.

### GC Debug

[`Wldc4rd/gc-debug`](https://github.com/Wldc4rd/gc-debug) packages useful
owner-layer, fork-storm, full-table-scan, CPU, OOM, and Dolt investigation
methods. It is early-stage and GitHub detected no license. Mine its diagnostic
method or obtain licensing permission rather than copying code blindly.

### chwrite

[`davidawad/chwrite`](https://github.com/davidawad/chwrite) is MIT-licensed and
had three consecutive green CI runs at review time. A narrow pilot could
protect selected configuration or generated files, but it modifies Git's
hooks path and is not worktree isolation or OS-level write confinement.

### Blackbird

[`phall1/blackbird`](https://github.com/phall1/blackbird) offers durable
messaging and cooperative file reservations over a local daemon and SQLite.
It is architecturally interesting for collision avoidance, but overlaps Gas
City mail and state, introduces another service, uses a nonstandard license,
and had mixed recent CI. Evaluate it separately rather than adding it to a Gas
City reliability release.

## High-potential work to defer

- [`gascity#5193`](https://github.com/gastownhall/gascity/pull/5193), a single
  transactional owner for agent workspaces, has the strongest long-term
  worktree contract in the scan. It is also a conflicting 34-file change with
  4,486 additions and three failed checks. Mine its invariants and adversarial
  tests; do not treat it as a release candidate.
- [`gascity#4293`](https://github.com/gastownhall/gascity/pull/4293) targets a
  real detached nudge-poller leak, but is a conflicting nine-file change with
  seven failed checks and had not moved since 2026-07-19. Census live pollers
  first; if the leak is present, reimplement identity-verified reaping on
  current main.
- [`beads#4804`](https://github.com/gastownhall/beads/pull/4804) could remove
  steady-state migration-lock serialization paid by every server-mode store
  open. Its field evidence is compelling, but the current diff is eight files
  and 885 additions with no completed successful CI checks. Benchmark and wait
  for a smaller, proven form before touching a deployed Beads fork.
- Sjarmak's larger Herdr event/reconciliation series, Duncan's backend/plugin
  branches, Cairn, Phux, and full Blackbird integration are multi-thousand-line
  or architectural migrations. Keep them as research inputs until the smaller
  reliability work is measured.

## Recommended execution order

1. Establish a read-only baseline with Oil Cop and direct event, process, and
   store measurements.
2. Port `gascity-packs#207`, `#219`, and `#267` as one independently
   revertible pack batch. Compare query concurrency, Boot reasoning rate,
   patrol count, event rate, process count, and token use before and after.
3. Port `gascity#4894` with focused Herdr delivery tests.
4. Query conflict metadata and events; port `gascity#4595` if its trigger is
   active.
5. Resolve `gascity#5122`'s failed checks, then port its Codex confirmation
   behavior without weakening pane-content safety.
6. Time `gc doctor` and order dispatch and census detached pollers before
   deciding whether `#5203` or a current-main reimplementation of `#4293` is
   next.

## Verification limits

The deployed `gc` observed during the review reported only `dev`. Source
inspection therefore proves that the relevant code shapes exist in checked-out
trees, not that every one is present in an exact running binary. Establish the
deployed SHA or reproduce the relevant signature before claiming a production
fix.

PR check totals include successful, failed, skipped, neutral, and pending
checks as reported by GitHub. "No failed checks" does not mean every listed
check completed. Conflicting PRs should be ported deliberately with focused
regressions, not cherry-picked wholesale.
