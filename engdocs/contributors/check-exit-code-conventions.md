---
title: Check Exit Code Conventions
description: The three-valued exit contract for check scripts — 0 ran-clean, 1 ran-finding, 2 could-not-run — what each code obliges the check to prove, and how a caller must escalate a 2 differently from a 1.
---

## Why this exists

Our health signals conflate failure with inability. A check that cannot reach
its data returns the same thing as a check that ran and passed, so a green
board can mean "everything is fine" or it can mean "nothing was measured", and
nothing in the output distinguishes them.

Observed repeatedly on 2026-08-24: `gc doctor` reported a stale order-firing
finding that order history contradicted; a wasted-work finding pointed at a
session that no longer existed; and `bd ready --metadata-field gc.routed_to=...`
with a malformed flag returned 0 rows, which reads as "no demand" but meant
"the query was wrong" — it wanted `key=value`.

The fix is not more checks. It is making every check say which of three things
happened.

## Where check scripts live

| Location | What lives there | Gated by |
|---|---|---|
| `scripts/check-*.sh` | Repo-level gates and order wrappers for this repo | `scripts/test-check-*.sh` self-test, wrapped by a `scripts/*_test.go` so `go test ./scripts/...` runs it |
| the city's `orders/assets/*.sh` | City patrol and sweep scripts, outside this repo | the city's own gates |
| `packs/*/doctor/check-*.sh` | Per-pack doctor probes shipped with a pack | the pack |

The convention below applies to all three. Only the first is enforced by this
repo's tests. A new check script belongs in `scripts/`, is named
`check-<subject>.sh`, and lands with a `scripts/test-check-<subject>.sh` that
demonstrates each exit path it can produce.

## The three codes

| Code | Meaning | What the check must be able to prove |
|---|---|---|
| `0` | **Ran, clean.** The guarded condition holds. | Every input was read, every dependency answered. A `0` is an assertion about reality, not about the check's own luck. |
| `1` | **Ran, finding.** The guarded condition is violated. | The same coverage a `0` requires. `1` asserts a *complete* verdict — "these are the violations" — so a partial scan cannot report it. |
| `2` | **Could not run.** A dependency, query, parse, or scope failed. | Nothing. That is the point: `2` is the code that claims nothing about the subject under test. |

Two rules fall out of the table and are worth stating separately, because both
have been got wrong in this repo:

- **Absence of evidence is not evidence of absence.** A claimed bead with no
  heartbeat is stale, not fresh. An empty query result is only a zero if the
  query answered; an unanswered query is a `2`.
- **Coverage dominates the verdict.** A run that skipped a scope exits `2` even
  when it also found a violation, because `1` asserts a complete count. Do the
  side effects first (file the alert, write the row), then exit `2`: the
  finding is never lost, the exit code just stops overstating it.

## How a caller must treat a 2

This is the half that gets skipped. A fail-closed result reported as a normal
failure just moves the ambiguity up a level — the board goes red, someone
investigates the *subject*, finds it healthy, and marks the check flaky.

| | `1` — finding | `2` — could not run |
|---|---|---|
| Who it routes to | whoever owns the **subject** (the ledger, the branch, the config) | whoever owns the **instrument** (the missing tool, the broken query, the unreachable store) |
| What the message says | "found N violations" | "**not measured** — <what failed>". Never "check failed". |
| Retry | pointless; re-measuring re-finds it | the only code worth retrying |
| Auto-remediation | may run | **must not run.** A remediation triggered by an unanswered query is how live work gets reset. |
| In an N-check summary | counts as a fail | the summary reads **"incomplete"**, never "N-1 passed" |

Concretely, for a patrol or order wrapping a check:

```bash
scripts/check-<subject>.sh
rc=$?
case "$rc" in
  0) ;;                                   # nothing to do
  1) escalate_finding   "check-<subject> found violations" ;;
  *) escalate_cannot_run "check-<subject> did not run (rc=$rc)" ;;
esac
```

Note `*)`, not `2)`. **Callers test "not 0 and not 1", never `== 2`.** Some
checks reserve higher codes for narrower could-not-run cases —
`scripts/check-unlanded-branches.sh` exits `3` when its positive control fails,
meaning the instrument proved itself broken and refused to report at all. A
caller that matches `2` exactly sends that `3` down the `1` branch and reports
a broken ancestry test as a landing-queue finding. Anything `>= 2` is
could-not-run.

## Writing a check that honours the contract

```bash
set -euo pipefail

readonly EXIT_CLEAN=0
readonly EXIT_FINDING=1
readonly EXIT_CANNOT_RUN=2

fail_closed() {
    printf '<name>: fail-closed: %s\n' "$*" >&2
    exit "$EXIT_CANNOT_RUN"
}

# Default-deny: an outcome nobody classified is a could-not-run, never a
# verdict. Without this, an unguarded failure surfaces bash's own status —
# a tool's 127, jq's 5, a bare set -e 1 — and a caller reading 1 as "finding"
# acts on a measurement that never happened. Deliberately NOT `set -E`: the
# trap must stay out of command substitutions so a query already guarded by
# `|| fail_closed` reports once, not twice.
# shellcheck disable=SC2329  # invoked indirectly by the ERR trap
on_unhandled_error() { fail_closed "unhandled failure at line ${1:-?} (rc=${2:-?})"; }
trap 'on_unhandled_error "$LINENO" "$?"' ERR

for tool in gc bd jq; do
    command -v "$tool" >/dev/null 2>&1 || fail_closed "$tool not found in PATH"
done
```

Then guard every external call — `x="$(cmd ...)" || fail_closed "..."` — and
validate what comes back, not just the exit status. The three that bite:

- an empty result from a command that exited `0`,
- output that is not the shape you parsed it as (`jq 'if type == "array" then length else empty end'`, then reject an empty answer),
- an unfamiliar enum value. Handle the cases you know and make the `*)` branch
  a `fail_closed`, not a pass.

## Worked example: `check-census-owner-liveness.sh`

Converted in `gas-xraq`. It wraps the `census-owner-liveness` doctor check,
which reports "no dangling owner_beads" and "I could not open that scope's bead
store" with the *same* `status=warning`. The wrapper mapped the second one to
exit `0` with the words "nothing to do".

Exit codes before and after, on identical `gc doctor --json` fixtures:

| Fixture | before | after |
|---|---|---|
| clean ledger | `0` | `0` |
| dangling `owner_bead` found | `0` | `1` |
| scope skipped, no finding | `0` | `2` |
| doctor `status=error` | `0` | `2` |
| unparseable doctor JSON | `1` | `2` |
| `gc`, `bd`, or `jq` absent | `1` / `127` | `2` |
| dedupe query failed | `1` (silent abort) | `2` |
| dedupe query returned garbage | `5` (jq's own code) | `2` |

Four genuinely different states shared exit `0`, and the *only* nonzero code
the script produced on purpose meant "could not run" — the inverse of the
convention.

The three paths, demonstrated (`gc`/`bd` stubbed so the fixtures drive it):

```console
$ DOCTOR='{"results":[{"name":"census-owner-liveness","status":"ok","details":[]}]}' \
    scripts/check-census-owner-liveness.sh; echo "exit=$?"
check-census-owner-liveness: no dangling owner_bead references; every scope read (status=ok)
exit=0

$ DOCTOR='{"results":[{"name":"census-owner-liveness","status":"warning","details":["city: dangling owner_bead=ga-dead1 rows=[debt: scope=internal/api resource=coverage]"]}]}' \
    scripts/check-census-owner-liveness.sh; echo "exit=$?"
check-census-owner-liveness: filed demo-alert-1 for dangling owner_bead=ga-dead1
check-census-owner-liveness: done (1 alert(s) created); dangling owner_bead reference(s) found across all scopes
exit=1

$ DOCTOR='{"results":[{"name":"census-owner-liveness","status":"warning","details":["rig kit skipped: opening bead store: dial tcp 127.0.0.1:3306: connection refused"]}]}' \
    scripts/check-census-owner-liveness.sh; echo "exit=$?"
check-census-owner-liveness: coverage incomplete -- these scopes were not measured:
  rig kit skipped: opening bead store: dial tcp 127.0.0.1:3306: connection refused
check-census-owner-liveness: fail-closed: census-owner-liveness did not read every scope; this is a could-not-run, not a clean ledger
exit=2
```

Every path above is pinned by `scripts/test-check-census-owner-liveness.sh`
(16 cases, hermetic — PATH is rebuilt per case from symlinked coreutils plus
`gc`/`bd` stubs, which is what makes "the tool is missing" testable at all).

**This conversion changed no patrol's report.** The script had no caller: no
order file, no Makefile target, no cron entry — its own release gate
(`release-gates/ga-joodpj-census-owner-liveness-gate.md`) records "No in-repo
order file was added, matching the non-goal". Converting a check that *is*
wired to a patrol is a different job: say so on the bead and stop, rather than
shipping a silent change to what the board shows.

## Default-deny is the shape we want

The exit convention is one instance of a broader rule: an unclassified outcome
should resolve to refused, not permitted. `tdupu/mathcity`'s
`assets/mctl/blast_radius.toml` states it at the registry level — an operation
absent from the file resolves to `gate='unclassified'`, refused rather than
permitted, so adding a mutation without classifying it fails *safe*.

Our work-record close gate is currently the inverse: warn-by-omission. A close
with no `gc.work_outcome` is warned about, not refused. **Do not re-arm that
gate on the strength of this page.** It is noted here only so that whoever does
arm it starts from default-deny rather than rediscovering the argument.

## See also

- [Hold and Blocked Label Conventions](hold-label-conventions.md) — the same
  "one canonical vocabulary, no ad hoc values" discipline, for bead labels
- [Release Gate Criteria Conventions](release-gate-criteria-conventions.md) —
  what a "Tests pass" sign-off must cite
- `scripts/check-unlanded-branches.sh` — the prior art here: `2` for a missing
  `git` or an unresolvable lane, `3` for a failed positive control
