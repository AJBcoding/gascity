#!/usr/bin/env bash
# close-merge-sweep — ladder rung 5: the merge ledger (gas-6g10).
#
# Rung 4 (close-audit-sweep) answers "is this work DURABLE?" — is the commit on
# a publication remote, so a worktree prune cannot destroy it. That question has
# a true green for work that is published, safe forever, and in the tree nothing
# runs from. gas-cj7 (3938a3f84) and gas-2w4 (85ee50baa) are both that shape:
# published on ajb, closed, and on no mainline. gas-2w4 is the close-audit
# sweeper itself, so the fix for invisible closes was invisible for weeks.
#
# This rung asks the other question: DID IT LAND WHERE IT WAS AIMED?
#
# TWO CLAIMS, NEVER CONFLATED. This is the whole design, not a refinement.
#   PROVEN   — the refinery merged it and stamped merged_target + merged_sha.
#              The claim is "merged into X at SHA". Re-probing it catches a
#              force-push or history rewrite that silently un-lands merged work.
#   INTENDED — everyone else. The claim is only "targeted X, landing unproven".
#              97% of closes are this shape (measured: 7 of 252 closed beads
#              carried merged_target), because most beads are closed by the
#              agent that did the work rather than merged by the refinery.
# Reporting an intended target as though it were proven is exactly the failure
# that let a deployed binary sit 418 commits behind, built from an unmerged
# branch, while 98 beads read closed. Landed-on-a-branch, merged-upstream, and
# running-on-this-host are three unrelated facts.
#
# THIS RUNG NEVER BLOCKS A CLOSE, BY DESIGN. Extending the close gate from "is
# it published" to "is it merged" would hold every polecat's bead open behind a
# refinery queue it does not control, converting one stalled refinery into a
# city-wide stall — and failing closed hardest on the rigs whose refineries are
# already struggling. The polecat's job IS finished at publication; merging is
# the refinery's. So this is a read-only sweep that files a bead, never a gate.
#
# ANCESTRY IS MEASURED AGAINST THE REMOTE REF, NEVER THE BARE LOCAL BRANCH.
# A local branch name can sit hundreds of commits behind the ref everyone
# actually pushes to — measured 2026-08-11: local integration/deploy-20260804
# was 400+ commits behind ajb/integration/deploy-20260804. Testing ancestry
# against that stale local would report every genuinely merged bead as
# unmerged: a fully inverted, entirely convincing false alarm. When no
# remote-tracking ref exists we say NOTHING rather than judge against a local
# ref we cannot trust.
#
# Runs as an exec order (no LLM, no agent, no wisp): every decision is a git
# containment probe or a metadata read. Exits 0 on EVERY path — a sweeper that
# breaks the controller is a worse bug than the one it reports.
#
# Written for bash 3.2 (macOS /bin/bash) as well as 5.x.
set -uo pipefail   # deliberately NOT -e: no probe failure may abort the sweep

__SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$__SCRIPT_DIR/_bd_trace.sh" ]; then
    # shellcheck disable=SC1091
    . "$__SCRIPT_DIR/_bd_trace.sh" "close-merge-sweep"
fi

ROOT="${GC_MERGE_LEDGER_ROOT:-${GT_TOWN_ROOT:-$PWD}}"
DRY_RUN="${GC_MERGE_LEDGER_DRY_RUN:-}"
LIMIT="${GC_MERGE_LEDGER_LIMIT:-200}"
# A close is not a merge request. The refinery picks work up on its own cadence,
# so a bead closed minutes ago is expected to be unmerged; filing inside that
# window reports the pipeline working. Default is deliberately much longer than
# rung 4's publication grace, which only has to outlast a push.
MIN_AGE_HOURS="${GC_MERGE_LEDGER_MIN_AGE_HOURS:-24}"

command -v jq >/dev/null 2>&1 || exit 0
command -v git >/dev/null 2>&1 || exit 0
command -v gc >/dev/null 2>&1 || exit 0

FILED=0
LANDED=0
UNPROVEN=0
UNKNOWN=0
TOOSOON=0
DEDUPED=0

# cutoff_ts — the newest close timestamp old enough to act on, ISO-8601 UTC.
# ISO-8601 UTC sorts lexicographically, so callers string-compare.
cutoff_ts() {
    date -u -d "$MIN_AGE_HOURS hours ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null && return 0
    date -u -v-"${MIN_AGE_HOURS}"H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null && return 0
    printf ''
}
CUTOFF="$(cutoff_ts)"

# publication_remotes — remotes that can confer durability: every configured
# remote except those whose URL resolves back into this same repository
# (gas-6tc). Shared semantic with the gc gate and rung 4; if one changes, all
# change.
publication_remotes() {
    local repo="$1" name url self
    self="$(git -C "$repo" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
    for name in $(git -C "$repo" remote 2>/dev/null); do
        case "$name" in -*) continue ;; esac
        url="$(git -C "$repo" remote get-url "$name" 2>/dev/null)"
        if [ -n "$url" ] && [ -n "$self" ] && [ -d "$url" ]; then
            case "$(cd "$url" 2>/dev/null && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" in
                "$self") continue ;;
            esac
        fi
        printf '%s\n' "$name"
    done
}

# resolve_target_ref — the ref to test ancestry against for branch $2 in repo $1.
# Prints a ref and returns 0, or prints nothing and returns 1 when only a local
# ref exists. Returning 1 means UNPROBEABLE, not unmerged: see the header.
resolve_target_ref() {
    local repo="$1" branch="$2" remote ref
    [ -n "$branch" ] || return 1
    for remote in $(publication_remotes "$repo"); do
        ref="refs/remotes/$remote/$branch"
        if git -C "$repo" show-ref --verify --quiet "$ref"; then
            printf '%s' "$ref"
            return 0
        fi
    done
    return 1
}

is_ancestor() {
    git -C "$1" merge-base --is-ancestor "$2" "$3" 2>/dev/null
}

already_filed() {
    local repo="$1" id="$2" out
    out=$( cd "$repo" 2>/dev/null && gc bd list --status=open --json 2>/dev/null |
        jq -c --arg src "$id" '[.[] | select(.metadata["gc.merge_ledger_source"] == $src)]' 2>/dev/null )
    [ -n "$out" ] && [ "$(printf '%s' "$out" | jq -r 'length' 2>/dev/null || echo 0)" != "0" ]
}

file_finding() {
    local repo="$1" id="$2" kind="$3" title="$4" desc="$5"
    if already_filed "$repo" "$id"; then
        DEDUPED=$((DEDUPED + 1)); return 0
    fi
    if [ -n "$DRY_RUN" ]; then
        printf 'close-merge-sweep: WOULD FILE for %s (%s)\n' "$id" "$kind"
        FILED=$((FILED + 1)); return 0
    fi
    if ( cd "$repo" 2>/dev/null && gc bd create "$title" \
            --type bug -p 2 \
            -d "$desc" \
            --metadata "$(jq -nc --arg src "$id" --arg kind "$kind" \
                '{"gc.merge_ledger_source":$src,"gc.merge_ledger_kind":$kind}')" \
            >/dev/null 2>&1 ); then
        FILED=$((FILED + 1))
        printf 'close-merge-sweep: filed for %s (%s)\n' "$id" "$kind"
    else
        UNKNOWN=$((UNKNOWN + 1))
    fi
}

# sweep_bead — one closed bead, one verdict.
sweep_bead() {
    local repo="$1" id="$2" commit="$3" closed_at="$4" merged_target="$5" merged_sha="$6" target="$7"
    local ref claim probe

    [ -n "$id" ] && [ -n "$commit" ] || return 0

    if [ -n "$CUTOFF" ] && [ -n "$closed_at" ] && [ "$closed_at" ">" "$CUTOFF" ]; then
        TOOSOON=$((TOOSOON + 1)); return 0
    fi

    if [ -n "$merged_target" ]; then
        claim="proven"
        probe="${merged_sha:-$commit}"
        target="$merged_target"
    else
        claim="intended"
        probe="$commit"
    fi

    [ -n "$target" ] || { UNKNOWN=$((UNKNOWN + 1)); return 0; }

    if ! ref="$(resolve_target_ref "$repo" "$target")"; then
        # Only a local ref, or none. A local branch can be hundreds of commits
        # stale; judging against it manufactures false alarms. Stay quiet.
        UNKNOWN=$((UNKNOWN + 1)); return 0
    fi

    if is_ancestor "$repo" "$probe" "$ref"; then
        LANDED=$((LANDED + 1)); return 0
    fi

    if [ "$claim" = "proven" ]; then
        UNPROVEN=$((UNPROVEN + 1))
        file_finding "$repo" "$id" "merge-retracted" \
            "Merge ledger: $id was merged to $target at $probe, and $probe is no longer on $ref" \
            "This bead recorded a PROVEN merge — the refinery stamped merged_target and
merged_sha — but the commit is no longer an ancestor of the target.

  bead:           $id
  merged_target:  $target
  merged_sha:     $probe
  probed ref:     $ref

A proven merge that stops being true means the target branch was force-pushed
or its history rewritten, and merged work was dropped in the process. This is
the loud case: the ledger is not guessing here, it is reporting that a fact it
recorded has been retracted.

To resolve: confirm whether $probe is still wanted on $target. If it is,
re-merge it. If it was deliberately dropped, correct the record with
  gc bd update $id --unset-metadata merged_target --unset-metadata merged_sha"
        return 0
    fi

    UNPROVEN=$((UNPROVEN + 1))
    file_finding "$repo" "$id" "merge-gap" \
        "Merge ledger: $id closed as shipped but $commit never landed on $target" \
        "This bead closed asserting delivery, and its commit is published and durable —
nothing is at risk of being lost. What is missing is EFFECT: the work is not in
the tree anything runs from.

  bead:        $id
  commit:      $commit
  target:      $target  (intended, NOT a proven merge)
  probed ref:  $ref
  closed:      $closed_at

This is an INTENDED-target claim, not a proven one: no refinery merge was ever
recorded for this bead, so the target is the rig's configured default_branch
rather than a branch anything confirmed the work reached. Read it as 'targeted
$target, landing unproven' — never as 'failed to merge'.

The close was correct and should NOT be reopened. A polecat's job finishes at
publication; merging is the refinery's. This is a handoff that stalled, not a
close that lied.

To resolve: route $commit to the refinery for merge to $target, or — if it was
superseded, or landed somewhere else on purpose — record that with
  gc bd update $id --set-metadata merged_target=<branch> --set-metadata merged_sha=<sha>"
}

REPO="$ROOT"
git -C "$REPO" rev-parse --git-dir >/dev/null 2>&1 || exit 0

# Resolve the rig's configured mainline. This is the INTENDED target for every
# close that carries no proven merge, and it is read from config rather than
# guessed: a wrong default_branch aims the whole pipeline at a branch nobody
# pushes to, and the refinery then correctly finds nothing to do, forever
# (measured: gascity sat pointed at feat/mysql-first-class-backend, 459 commits
# behind the lane, which is most of why only 7 of 252 closes carried a merge).
DEFAULT_BRANCH="${GC_MERGE_LEDGER_TARGET:-}"
if [ -z "$DEFAULT_BRANCH" ]; then
    DEFAULT_BRANCH=$( cd "$REPO" 2>/dev/null && gc rig list --json 2>/dev/null |
        jq -r --arg root "$REPO" '
            [ .[] | select((.path // "") as $p | $root | startswith($p)) ]
            | sort_by((.path // "") | length) | last | .default_branch // empty
        ' 2>/dev/null )
fi

RECORDS=$( cd "$REPO" 2>/dev/null && gc bd list --status=closed --limit="$LIMIT" --json 2>/dev/null |
    jq -r '
        .[]
        | select((.metadata["gc.work_outcome"] // "") == "shipped")
        | select((.metadata["gc.work_commit"] // "") != "")
        | [ .id,
            .metadata["gc.work_commit"],
            (.closed_at // .updated_at // ""),
            (.metadata["merged_target"] // ""),
            (.metadata["merged_sha"] // ""),
            (.metadata["gc.work_target"] // "")
          ] | @tsv
    ' 2>/dev/null )

while IFS="$(printf '\t')" read -r id commit closed_at merged_target merged_sha work_target; do
    [ -n "${id:-}" ] || continue
    # gc.work_target is the target frozen at close, when it is present; the
    # config value is the fallback for the closes that predate that stamp.
    sweep_bead "$REPO" "$id" "$commit" "$closed_at" "$merged_target" "$merged_sha" \
        "${work_target:-$DEFAULT_BRANCH}"
done <<EOF
$RECORDS
EOF

printf 'close-merge-sweep: landed=%d unproven=%d filed=%d deduped=%d toosoon=%d unknown=%d target=%s\n' \
    "$LANDED" "$UNPROVEN" "$FILED" "$DEDUPED" "$TOOSOON" "$UNKNOWN" "${DEFAULT_BRANCH:-<unresolved>}"
exit 0
