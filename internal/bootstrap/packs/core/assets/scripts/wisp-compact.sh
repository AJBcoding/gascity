#!/usr/bin/env bash
# wisp-compact — TTL-based cleanup of expired ephemeral beads.
#
# Wisps are short-lived work items (heartbeats, pings, patrols) that
# accumulate and bloat the database. This script applies retention policy:
# - Closed wisps past TTL → deleted (Dolt AS OF preserves history)
# - Non-closed wisps past TTL → promoted to permanent (stuck detection),
#   except unread mail, which is skipped rather than promoted (gas-6uf3)
# - Wisps with comments or "keep" label → promoted (proven value)
#
# Sweeps every store: the HQ/city store plus each rig's, since `gc bd` resolves
# to HQ unless given --rig (gas-0urx).
#
# TTL by wisp_type label:
#   heartbeat, ping: 6h
#   patrol, gc_report: 24h
#   recovery, error, escalation: 7d
#   default (untyped): 24h
#
# Runs as an exec order (no LLM, no agent, no wisp).
set -euo pipefail

# Trace bd invocations to $GC_BD_TRACE when set (no-op otherwise).
__SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
. "$__SCRIPT_DIR/_bd_trace.sh" "wisp-compact"

CITY="${GC_CITY:-.}"

NOW=$(date +%s)
PROMOTED=0
DELETED=0
SKIPPED=0

# Build the list of scopes to sweep: HQ (the empty scope, a bare `gc bd`) plus
# every non-HQ rig. `gc bd list` without --rig resolves to the HQ/city store
# only, so every wisp living in a rig store was invisible to this order —
# measured 2026-08-12, 8 patrol wisps leaked 19h-475h across 5 rigs while the
# order reported success every hour (gas-0urx). A leaked in_progress wisp also
# makes its owner look like it is holding work forever, which reads to the
# deacon's queue-starvation check as a starving queue.
#
# The HQ entry is excluded from the rig list: `gc rig list` reports the city
# root as an hq=true pseudo-rig that `gc --rig <cityName>` cannot resolve, and
# its store is exactly what the bare scope already sweeps. Same scope-list
# shape as renudge-stale-human-gates.sh and orphan-sweep.sh.
RIG_NAMES=""
RIGS_JSON=$(gc rig list --json 2>/dev/null) || RIGS_JSON=""
if [ -n "$RIGS_JSON" ]; then
    RIG_NAMES=$(echo "$RIGS_JSON" | jq -r '(.rigs // [])[] | select(.hq != true) | .name' 2>/dev/null) || RIG_NAMES=""
fi
# The leading empty line is the HQ scope. Command substitution strips trailing
# newlines, so a city with no rigs yields exactly one (empty) scope instead of
# sweeping HQ twice.
SCOPES=$(printf '\n%s' "$RIG_NAMES")

while IFS= read -r scope; do
    RIG_ARG1=""
    RIG_ARG2=""
    if [ -n "$scope" ]; then
        RIG_ARG1="--rig"
        RIG_ARG2="$scope"
    fi

    # Get all ephemeral beads in this scope. --include-infra is required:
    # wisps are infrastructure beads and `gc bd list` hides those by default, so
    # without it the `.ephemeral` filter below matched nothing in ANY store,
    # HQ included (measured: 97 wisps present, 0 returned). Every write below
    # repeats the scope args — a bare `gc bd update/delete` would resolve to
    # the HQ store and act on the wrong bead, or on none.
    #
    # Best-effort per scope: a read failure (suspended rig, unreachable store)
    # skips that scope rather than aborting, so one bad store cannot strand
    # the sweep for all the others.
    ALL=$(gc bd list ${RIG_ARG1:+"$RIG_ARG1" "$RIG_ARG2"} --include-infra --json --all -n 0 2>/dev/null) || continue
    EPHEMERALS=$(echo "$ALL" | jq '[.[] | select(.ephemeral == true)]' 2>/dev/null) || continue

    if [ -z "$EPHEMERALS" ] || [ "$EPHEMERALS" = "[]" ]; then
        continue
    fi

    # Process each ephemeral bead. Capturing jq output into BEADS first
    # (instead of piping into the loop) preserves the original pipefail
    # fail-loud on jq error AND keeps PROMOTED/DELETED/SKIPPED in the parent
    # shell so they survive to the summary echo below. EPHEMERALS is
    # pre-validated as a non-empty array just above, so BEADS is guaranteed
    # non-empty here.
    BEADS=$(echo "$EPHEMERALS" | jq -c '.[]' 2>/dev/null)
    while IFS= read -r bead; do
        id=$(echo "$bead" | jq -r '.id')
        status=$(echo "$bead" | jq -r '.status')
        issue_type=$(echo "$bead" | jq -r '.issue_type // ""')
        updated_at=$(echo "$bead" | jq -r '.updated_at // .created_at')
        comment_count=$(echo "$bead" | jq -r '.comment_count // 0')
        labels=$(echo "$bead" | jq -r '.labels // [] | .[]' 2>/dev/null)

        # Determine TTL from wisp_type label.
        TTL_SECONDS=$((24 * 3600))  # default: 24h
        for label in $labels; do
            case "$label" in
                wisp_type:heartbeat|wisp_type:ping) TTL_SECONDS=$((6 * 3600)) ;;
                wisp_type:patrol|wisp_type:gc_report) TTL_SECONDS=$((24 * 3600)) ;;
                wisp_type:recovery|wisp_type:error|wisp_type:escalation) TTL_SECONDS=$((7 * 24 * 3600)) ;;
                keep) TTL_SECONDS=0 ;;  # force promote
            esac
        done

        # Calculate age. bd emits RFC3339 timestamps with a trailing 'Z'; the
        # second BSD `date -ju -f` fallback handles that explicitly and forces
        # UTC semantics to match GNU `date -d`. The third layout supports older
        # no-Z timestamps without interpreting them in the local timezone.
        BEAD_TS=$(date -d "$updated_at" +%s 2>/dev/null || \
                  date -ju -f "%Y-%m-%dT%H:%M:%SZ" "$updated_at" +%s 2>/dev/null || \
                  date -ju -f "%Y-%m-%dT%H:%M:%S" "$updated_at" +%s 2>/dev/null) || continue
        AGE=$((NOW - BEAD_TS))

        # Skip if within TTL (unless force-promote via keep label).
        if [ "$TTL_SECONDS" -gt 0 ] && [ "$AGE" -lt "$TTL_SECONDS" ]; then
            SKIPPED=$((SKIPPED + 1))
            continue
        fi

        # Comments or a keep label mean someone engaged with this wisp: it has
        # proven value and is promoted regardless of type or status.
        PROVEN=0
        if [ "$comment_count" -gt 0 ] || echo "$labels" | grep -q '^keep$'; then
            PROVEN=1
        fi

        # Stuck detection promotes a non-closed wisp past TTL so that a stalled
        # molecule stays visible. Mail is not stalled work: an unread message
        # past TTL is a retention question, not a stuck agent, and promoting it
        # turns the mail spool into permanent beads. Measured on the first
        # correctly-scoped sweep of the HQ store: 37 unread message wisps
        # (oldest 48 days) would have been promoted in one pass. Leave them
        # untouched — closed mail past TTL is still deleted below, which is what
        # bounds the spool. Unread-mail retention is gas-6uf3.
        if [ "$PROVEN" -eq 0 ] && [ "$status" != "closed" ] && [ "$issue_type" = "message" ]; then
            SKIPPED=$((SKIPPED + 1))
            continue
        fi

        # Promote if proven value, or non-closed (stuck detection).
        if [ "$PROVEN" -eq 1 ] || [ "$status" != "closed" ]; then
            if [ "$status" != "closed" ]; then
                REASON="open past TTL (stuck detection)"
            else
                REASON="proven value"
            fi
            gc bd update "$id" ${RIG_ARG1:+"$RIG_ARG1" "$RIG_ARG2"} --persistent 2>/dev/null || true
            gc bd comment "$id" ${RIG_ARG1:+"$RIG_ARG1" "$RIG_ARG2"} "Promoted from wisp: $REASON" 2>/dev/null || true
            PROMOTED=$((PROMOTED + 1))
            continue
        fi

        # Closed + past TTL + no special attributes → delete.
        gc bd delete "$id" ${RIG_ARG1:+"$RIG_ARG1" "$RIG_ARG2"} --force 2>/dev/null || true
        DELETED=$((DELETED + 1))
    done <<< "$BEADS"
done <<< "$SCOPES"

TOTAL=$((PROMOTED + DELETED))
if [ "$TOTAL" -gt 0 ]; then
    echo "wisp-compact: promoted=$PROMOTED deleted=$DELETED skipped=$SKIPPED"
fi
