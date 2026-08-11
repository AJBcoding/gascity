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
# ANCESTRY IS NOT EFFECT, SO THE VERDICT IS A THREE-RUNG LADDER. "Is this SHA
# reachable" is never the question; "is this CHANGE in effect" is. Cherry-pick,
# rebase, squash-merge and hand-porting all put a change into effect while
# leaving the recorded SHA unreachable, so a bare ancestry probe manufactures
# confident false alarms — five of them during this bead's own design, from four
# independent directions. A watchman whose third alarm is wrong gets ignored,
# and then the real merge gap goes unread too. So we report ONLY when all three
# rungs miss, and each rung asks a DIFFERENT question, because re-running one
# method twice feels rigorous and learns nothing:
#   1. ANCESTRY     — is the commit reachable from the target? Cheap, and right
#                     whenever it fires. Answers "did this COMMIT land".
#   2. PATCH-ID     — does any commit on the target carry the same diff? Catches
#                     cherry-pick and rebase, where the bytes survive and the
#                     SHA does not.
#   3. FINGERPRINT  — do the distinctive identifiers this commit INTRODUCES
#                     exist at the target? Catches the port and the squash,
#                     where even the diff is different. gas-cj7's own
#                     remediation is this shape: upstream #4768 had rewritten
#                     the function signature, so the fix had to be re-authored
#                     and its patch-id differs forever. An ancestry+patch-id
#                     ledger would report it missing forever, on work that is
#                     in effect.
# A fingerprint is DERIVED from the commit rather than stamped by hand: the
# identifiers on its added lines that appear nowhere in its parent tree. Hand-
# picked fingerprints fail silently in the safe-looking direction when someone
# chooses a common word or a line that later gets reformatted, and 13 of 29
# gate-scoped closes carry no work record to stamp one on anyway. A stamped
# gc.work_fingerprint is honoured when present; nothing depends on it existing.
#
# WHAT WE CANNOT READ, WE DO NOT JUDGE. A commit this repo has never fetched
# and a target with no remote-tracking ref are both UNPROBEABLE, not unmerged.
# Reporting either as a merge gap is an inverted alarm about work that may be
# perfectly landed somewhere this clone cannot see.
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

# Rungs 2 and 3 run only after rung 1 misses, which is the rare case, so these
# bounds exist to cap the tail rather than to tune throughput. The patch-id scan
# walks only commits on the target that touch the same paths, so the limit is
# reached only by files with very long histories.
PATCH_SCAN_LIMIT="${GC_MERGE_LEDGER_PATCH_SCAN:-400}"
FINGERPRINTS_WANTED="${GC_MERGE_LEDGER_FINGERPRINTS:-3}"
TOKEN_CANDIDATES="${GC_MERGE_LEDGER_TOKEN_CANDIDATES:-12}"
# Shorter tokens are words, not identifiers: "function", "return" and "context"
# all appear in every tree, so a fingerprint built from them reports landed for
# any change at all — a false green, the one direction this sweep must not fail.
MIN_TOKEN_LEN="${GC_MERGE_LEDGER_MIN_TOKEN_LEN:-12}"

command -v jq >/dev/null 2>&1 || exit 0
command -v git >/dev/null 2>&1 || exit 0
command -v gc >/dev/null 2>&1 || exit 0

FILED=0
LANDED=0
EQUIV=0
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

# commit_readable — true when $2 names a commit object this clone actually has.
# A commit published on a remote nobody here has fetched is unreadable, and
# every probe below would report it absent from a target it may well be on.
commit_readable() {
    git -C "$1" cat-file -e "${2}^{commit}" 2>/dev/null
}

# patch_id_of — the stable patch-id of a commit's diff, empty when it has none
# (merge commits, and commits whose diff is empty). --stable is required: the
# default is unstable across git versions, so two clones would disagree.
patch_id_of() {
    git -C "$1" diff-tree -p --no-commit-id "$2" 2>/dev/null |
        git -C "$1" patch-id --stable 2>/dev/null | awk '{print $1}'
}

# same_patch_on_target — LADDER RUNG 2. Does any commit on the target ref carry
# this commit's diff under a different SHA? Only commits touching the same paths
# are candidates, which keeps this bounded on a fast-moving lane.
same_patch_on_target() {
    local repo="$1" probe="$2" ref="$3" want cand line
    local -a paths=()

    want="$(patch_id_of "$repo" "$probe")"
    [ -n "$want" ] || return 1

    while IFS= read -r line; do
        [ -n "$line" ] && paths+=("$line")
    done <<EOF
$(git -C "$repo" show --format= --name-only "$probe" 2>/dev/null)
EOF
    # bash 3.2 errors on "${paths[@]}" for an empty array under set -u.
    [ "${#paths[@]}" -gt 0 ] || return 1

    for cand in $(git -C "$repo" log --format=%H --max-count="$PATCH_SCAN_LIMIT" \
                      "$ref" -- "${paths[@]}" 2>/dev/null); do
        [ "$(patch_id_of "$repo" "$cand")" = "$want" ] && return 0
    done
    return 1
}

# introduced_identifiers — the distinctive identifiers a commit ADDS: tokens on
# its added lines that appear nowhere in its parent tree. The parent check is
# what makes a token distinguishing — a token the commit merely mentions proves
# nothing about the commit when found on a target.
introduced_identifiers() {
    local repo="$1" commit="$2" want="$3" parent tok found=0
    parent="$(git -C "$repo" rev-parse --verify --quiet "${commit}^" 2>/dev/null)"
    for tok in $(git -C "$repo" show --format= --unified=0 "$commit" 2>/dev/null |
                     grep '^+' | grep -v '^+++' | cut -c2- |
                     grep -oE "[A-Za-z_][A-Za-z0-9_-]{$((MIN_TOKEN_LEN - 1)),}" |
                     sort -u | awk '{ print length($0), $0 }' | sort -rn |
                     head -n "$TOKEN_CANDIDATES" | cut -d' ' -f2-); do
        if [ -n "$parent" ] && git -C "$repo" grep -q -F -e "$tok" "$parent" -- 2>/dev/null; then
            continue   # pre-existing: it distinguishes nothing
        fi
        printf '%s\n' "$tok"
        found=$((found + 1))
        [ "$found" -ge "$want" ] && return 0
    done
    return 0
}

# fingerprint_on_target — LADDER RUNG 3. Is any of these identifiers present at
# the target ref? Read at the ref with git grep <ref>, never off the working
# tree: the tree is whatever branch someone last checked out, and on this
# machine that is routinely not the branch under discussion. That single
# confusion is what turned a miss into a confident alarm during this design.
fingerprint_on_target() {
    local repo="$1" ref="$2" fingerprints="$3" tok
    [ -n "$fingerprints" ] || return 1
    while IFS= read -r tok; do
        [ -n "$tok" ] || continue
        git -C "$repo" grep -q -F -e "$tok" "$ref" -- 2>/dev/null && return 0
    done <<EOF
$fingerprints
EOF
    return 1
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
    local repo="$1" id="$2" commit="$3" closed_at="$4" merged_target="$5" merged_sha="$6" target="$7" stamped_fp="$8"
    local ref claim probe fingerprints evidence

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

    if ! commit_readable "$repo" "$probe"; then
        # Published somewhere this clone has never fetched, or garbage-collected
        # out of it. Every probe below would say "absent" about a commit we
        # simply cannot read. Unprobeable is not unmerged.
        UNKNOWN=$((UNKNOWN + 1)); return 0
    fi

    if ! ref="$(resolve_target_ref "$repo" "$target")"; then
        # Only a local ref, or none. A local branch can be hundreds of commits
        # stale; judging against it manufactures false alarms. Stay quiet.
        UNKNOWN=$((UNKNOWN + 1)); return 0
    fi

    # THE LADDER. Three different questions, cheapest first; a finding needs all
    # three to miss. Both claims run it: a rebased or filtered target rewrites
    # every SHA on it while keeping the content, so ancestry alone would turn
    # each proven merge into a "merged work was dropped" alarm.
    if is_ancestor "$repo" "$probe" "$ref"; then
        LANDED=$((LANDED + 1)); return 0
    fi

    if same_patch_on_target "$repo" "$probe" "$ref"; then
        EQUIV=$((EQUIV + 1))
        printf 'close-merge-sweep: %s is on %s under a different SHA (patch-id) — not filing\n' "$id" "$target"
        return 0
    fi

    fingerprints="${stamped_fp:-$(introduced_identifiers "$repo" "$probe" "$FINGERPRINTS_WANTED")}"
    if fingerprint_on_target "$repo" "$ref" "$fingerprints"; then
        EQUIV=$((EQUIV + 1))
        printf 'close-merge-sweep: %s is on %s under a different SHA (fingerprint) — not filing\n' "$id" "$target"
        return 0
    fi

    # All three missed. Quote the CONTENT evidence, not the ancestry verdict:
    # "this identifier is absent from X" is checkable in one command, whereas
    # "SHA is not an ancestor" invites the reader to conclude it was never
    # applied — the exact reasoning error that produced this design's five
    # false positives.
    if [ -n "$fingerprints" ]; then
        evidence="Three independent probes agree the change is absent from $ref:
  1. ancestry  — $probe is not reachable from the ref
  2. patch-id  — no commit on the ref touching these paths carries its diff
  3. content   — none of the identifiers this commit introduces appear there:
$(printf '%s\n' "$fingerprints" | sed 's/^/         /')

Check the load-bearing one yourself in a single command:
  git grep -F -e '$(printf '%s' "$fingerprints" | head -n 1)' $ref"
    else
        evidence="Ancestry and patch-id both say absent. The content rung could NOT run: this
commit introduces no identifier distinctive enough to fingerprint (a pure
deletion, a whitespace-only change, or a merge commit). Treat this finding as
weaker than one carrying identifier evidence and confirm by hand — read the
commit's diff and check its subject matter at $ref directly."
    fi

    if [ "$claim" = "proven" ]; then
        UNPROVEN=$((UNPROVEN + 1))
        file_finding "$repo" "$id" "merge-retracted" \
            "Merge ledger: $id was merged to $target at $probe, and $probe is no longer on $ref" \
            "This bead recorded a PROVEN merge — the refinery stamped merged_target and
merged_sha — and the change is no longer present on the target.

  bead:           $id
  merged_target:  $target
  merged_sha:     $probe
  probed ref:     $ref

$evidence

A proven merge that stops being true means the target branch was force-pushed
or its history rewritten, and merged work was dropped in the process. A rewrite
that KEPT the content is not this: rungs 2 and 3 above would have found it, so
this is not simply a changed SHA. This is the loud case: the ledger is not
guessing here, it is reporting that a fact it recorded has been retracted.

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

$evidence

Absence of the FIX is all this reports. Do not read it as 'the bug is present
on $target' — that is a separate claim nobody has checked here, and it can be
false outright when the code the fix touches does not exist on that lineage.

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
            (.metadata["gc.work_target"] // ""),
            (.metadata["gc.work_fingerprint"] // "")
          ] | @tsv
    ' 2>/dev/null )

while IFS="$(printf '\t')" read -r id commit closed_at merged_target merged_sha work_target fingerprint; do
    [ -n "${id:-}" ] || continue
    # gc.work_target is the target frozen at close, when it is present; the
    # config value is the fallback for the closes that predate that stamp.
    # gc.work_fingerprint is optional — the ladder derives one when it is absent,
    # which is nearly always, so nothing here depends on a new close-path stamp.
    sweep_bead "$REPO" "$id" "$commit" "$closed_at" "$merged_target" "$merged_sha" \
        "${work_target:-$DEFAULT_BRANCH}" "${fingerprint:-}"
done <<EOF
$RECORDS
EOF

printf 'close-merge-sweep: landed=%d equiv=%d unproven=%d filed=%d deduped=%d toosoon=%d unknown=%d target=%s\n' \
    "$LANDED" "$EQUIV" "$UNPROVEN" "$FILED" "$DEDUPED" "$TOOSOON" "$UNKNOWN" "${DEFAULT_BRANCH:-<unresolved>}"
exit 0
