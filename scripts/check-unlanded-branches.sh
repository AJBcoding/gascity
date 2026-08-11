#!/usr/bin/env bash
# check-unlanded-branches.sh — derive the TRUE landing queue from git, not from bead
# bookkeeping, and name the closed beads whose branch never actually landed.
#
# Why this exists (gas-jyi5): the refinery finds work with
#   gc bd list --rig=$GC_RIG --assignee=$GC_AGENT --status=open,in_progress \
#     --exclude-type=epic --has-metadata-key=branch
# That query is correct, and mol-polecat-work's handoff (:20 "NEVER CLOSE THE WORK
# BEAD", :561 reassign to the refinery) is the matching producer. But an agent that
# closes its bead at PUBLICATION instead of handing off removes its branch from that
# queue permanently, and a publication-close is indistinguishable from a merge-close
# without reading the prose. Git is the one signal an agent cannot skip: a branch
# either is or is not an ancestor of the lane.
#
# This reports. It never merges, never mutates a bead, and never deletes a branch —
# of five stranded branches triaged by hand on 2026-08-10, two were NOT landable
# (an abandoned twin, and one needing a 50-file cascade analysis first), so the
# verdict has to stay with a human or an agent. Discovery from git, verdict on the bead.
#
# Usage: scripts/check-unlanded-branches.sh [remote] [lane-branch]
set -euo pipefail

REMOTE="${1:-ajb}"
LANE="${2:-integration/deploy-20260804}"
# Branch tips older than this are history, not a landing queue (see the filter below).
MAX_AGE_DAYS="${MAX_AGE_DAYS:-21}"
NOW_EPOCH="$(date +%s)"

command -v git >/dev/null || { echo "git not found" >&2; exit 2; }

# Authoritative lane tip: ls-remote, never a local ref. A stale local branch reads
# hundreds of commits behind and inverts every ancestry answer below.
LANE_SHA="$(git ls-remote "$REMOTE" "refs/heads/$LANE" | awk '{print $1}')"
[ -n "$LANE_SHA" ] || { echo "could not resolve $REMOTE/$LANE via ls-remote" >&2; exit 2; }

echo "lane:   $REMOTE/$LANE @ ${LANE_SHA:0:9}"

# One fetch, then every ancestry test is local. Fetching per-branch would be 300+
# round trips.
git fetch -q "$REMOTE" "refs/heads/*:refs/remotes/$REMOTE/*" --prune 2>/dev/null || true
git fetch -q "$REMOTE" "refs/heads/$LANE" 2>/dev/null || true

# POSITIVE CONTROL: a branch known to be merged must classify as merged. Without
# this, a broken ancestry test reports every branch as unlanded and the output reads
# like a catastrophe instead of a bug in this script.
control_ok="SKIPPED (control branch absent)"
if git rev-parse -q --verify "refs/remotes/$REMOTE/polecat/gas-qnav^{commit}" >/dev/null 2>&1; then
  if git merge-base --is-ancestor "refs/remotes/$REMOTE/polecat/gas-qnav" "$LANE_SHA" 2>/dev/null; then
    control_ok="PASS (a known-merged branch reads as merged)"
  else
    echo "CONTROL FAILED: polecat/gas-qnav is known-merged but reads unmerged. Ancestry test is broken; refusing to report." >&2
    exit 3
  fi
fi
echo "control: $control_ok"
echo

merged=0; unlanded=0; stale=0
declare -a rows=()

while read -r sha ref; do
  branch="${ref#refs/heads/}"
  case "$branch" in
    "$LANE"|main|master|HEAD) continue ;;
  esac
  # Need the object locally to test ancestry; a branch pushed after our fetch is
  # reported rather than silently skipped.
  if ! git rev-parse -q --verify "${sha}^{commit}" >/dev/null 2>&1; then
    rows+=("UNKNOWN-OBJECT|$branch|${sha:0:9}|not fetched; re-run")
    continue
  fi
  if git merge-base --is-ancestor "$sha" "$LANE_SHA" 2>/dev/null; then
    merged=$((merged+1))
    continue
  fi
  unlanded=$((unlanded+1))

  # RELEVANCE FILTER. Ancestry alone called 286 of 318 branches "unlanded" on the
  # first run — release/v1.0.0, safekeep/*, quinn/acceptance-*, tutorials. That is a
  # graveyard, not a queue, and a 286-row report is read as noise and then ignored,
  # which is worse than no report. A branch is a landing CANDIDATE only if its tip is
  # recent; everything older is history that was never coming to this lane.
  tip_epoch="$(git log -1 --format=%ct "$sha" 2>/dev/null || echo 0)"
  age_days=$(( (NOW_EPOCH - tip_epoch) / 86400 ))
  if [ "$tip_epoch" -eq 0 ] || [ "$age_days" -gt "$MAX_AGE_DAYS" ]; then
    stale=$((stale+1))
    continue
  fi

  bead="$(printf '%s' "$branch" | grep -oE 'gas-[a-z0-9]+' | head -1 || true)"
  if [ -z "$bead" ]; then
    rows+=("NO-BEAD|$branch|${sha:0:9}|${age_days}d old, no gas-* id in name")
    continue
  fi

  json="$(gc bd show "$bead" --json 2>/dev/null || true)"
  if [ -z "$json" ] || [ "$json" = "[]" ]; then
    rows+=("BEAD-MISSING|$branch|${sha:0:9}|$bead not found")
    continue
  fi
  status="$(printf '%s' "$json"   | jq -r '.[0].status // "?"')"
  assignee="$(printf '%s' "$json" | jq -r '.[0].assignee // ""')"
  reason="$(printf '%s' "$json"   | jq -r '(.[0].close_reason // .[0].resolution // "") | gsub("[\n\r]+";" ") | .[0:100]')"

  case "$status" in
    closed)
      # Ancestry says unlanded. If the close reason CLAIMS a merge, the two
      # disagree — which is either a squash/rebase landing (ancestry is blind to
      # those) or a false close. Separated because the remedies are opposite.
      if printf '%s' "$reason" | grep -qiE 'merg|landed|already_merged'; then
        rows+=("CLOSED-CLAIMS-MERGED|$branch|${sha:0:9}|$bead — ancestry disagrees with: $reason")
      else
        rows+=("CLOSED-BUT-UNLANDED|$branch|${sha:0:9}|$bead — $reason")
      fi
      ;;
    open|in_progress)
      if printf '%s' "$assignee" | grep -q 'refinery'; then
        rows+=("QUEUED|$branch|${sha:0:9}|$bead assigned to $assignee — refinery should take it")
      else
        rows+=("IN-FLIGHT|$branch|${sha:0:9}|$bead $status, assignee=${assignee:-NONE}")
      fi
      ;;
    *)
      rows+=("STATUS-$status|$branch|${sha:0:9}|$bead assignee=${assignee:-NONE}") ;;
  esac
done < <(git ls-remote --heads "$REMOTE")

printf '%-22s %-52s %-10s %s\n' "CLASS" "BRANCH" "TIP" "DETAIL"
printf '%-22s %-52s %-10s %s\n' "----------------------" "----------------------------------------------------" "----------" "------"
if [ "${#rows[@]}" -gt 0 ]; then
  printf '%s\n' "${rows[@]}" | sort | while IFS='|' read -r class branch tip detail; do
    printf '%-22s %-52s %-10s %s\n' "$class" "$branch" "$tip" "$detail"
  done
fi

echo
echo "merged into the lane:            $merged"
echo "NOT on the lane:                 $unlanded"
echo "  ...of those, tip > ${MAX_AGE_DAYS}d old:   $stale  (history, filtered out)"
echo "  ...recent enough to matter:    $(( unlanded - stale ))"
echo
echo "by class:"
if [ "${#rows[@]}" -gt 0 ]; then
  printf '%s\n' "${rows[@]}" | cut -d'|' -f1 | sort | uniq -c | sort -rn | sed 's/^/  /'
fi
echo
echo "CLOSED-BUT-UNLANDED is the gas-jyi5 defect class: the bead says done, git says the"
echo "branch never landed. CLOSED-CLAIMS-MERGED needs a human — ancestry is blind to a"
echo "squash or rebase landing, so those are not automatically wrong."
