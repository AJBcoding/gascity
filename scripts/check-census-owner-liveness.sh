#!/usr/bin/env bash
# check-census-owner-liveness.sh
#
# Order wrapper for the "census-owner-liveness" gc doctor check (ga-kr3glv.1,
# decision doc ga-kr3glv secs 2, 13). The resource-census ledger
# (test/test-resources.toml) anchors every row on an owner_bead, but nothing
# else in the pipeline notices when that bead stops resolving -- it happened
# once already (ga-c1slhq, same-day, <24h).
#
# Runs `gc doctor --json`, looks for dangling owner_bead findings from the
# census-owner-liveness check, and files one alert bead per distinct
# dangling owner_bead -- deduped against existing open alerts so a
# persistent condition doesn't spam a fresh bead on every cron tick.
#
# Detection only: this script never repairs the ledger or the bead store.
# Intended trigger: a cron order running every few hours (see the close-out
# notes on ga-kr3glv.1 for the order.toml to deploy).
#
# EXIT CODES -- three-valued, per engdocs/contributors/check-exit-code-conventions.md:
#
#   0  ran, clean.   Every scope was read and every owner_bead resolved.
#   1  ran, finding. Full coverage, and at least one dangling owner_bead;
#                    one alert bead filed per distinct owner (deduped).
#   2  could not run. A dependency, query, parse, or scope failed. This run
#                    did not measure everything it claims to measure. Escalate
#                    the instrument, not the ledger -- see "how a caller must
#                    treat 2" in the convention doc.
#
# Why the conversion (gas-xraq): every could-not-run path used to exit 1, the
# same code a caller reads as a finding, and three of them never reached an
# explicit exit at all -- surfacing `bd`'s 127, `jq`'s 5, or a bare `set -e`
# abort with no diagnostic. Worse, the underlying doctor check reports "no
# dangling owner_beads" and "I could not open that scope's bead store" with
# the same status=warning, and this wrapper mapped the second one to exit 0
# with the words "nothing to do". That is a green board for a ledger nobody
# looked at.
#
# COVERAGE DOMINATES THE VERDICT. A run that skipped a scope exits 2 even when
# it also found a dangling owner, because exit 1 asserts a complete count and
# a partial scan cannot support one. The alert beads are still filed first, so
# the finding is never lost -- only the exit code stops overstating it.
#
# Security note: bead IDs and row text read from the ledger/doctor output
# are untrusted data, not trusted shell fragments. Every bd/jq invocation
# below passes that data as a quoted argv element or through `jq -n --arg`
# / a heredoc -- never through `sh -c`/`eval` string interpolation. The
# per-owner detail filter is a bash `case` over quoted patterns rather than
# an interpolated regex, so a bead id carrying regex metacharacters cannot
# widen its own match.

set -euo pipefail

readonly EXIT_CLEAN=0
readonly EXIT_FINDING=1
readonly EXIT_CANNOT_RUN=2

routed_to="${CENSUS_OWNER_LIVENESS_ROUTED_TO:-gascity/architect}"
alert_label="source:census-owner-liveness-patrol"

note() { printf 'check-census-owner-liveness: %s\n' "$*"; }
warn() { printf 'check-census-owner-liveness: %s\n' "$*" >&2; }

fail_closed() {
    printf 'check-census-owner-liveness: fail-closed: %s\n' "$*" >&2
    exit "$EXIT_CANNOT_RUN"
}

# Default-deny, the blast-radius shape applied inside one script: an outcome
# nobody classified resolves to could-not-run, never to a verdict. Without this
# an unguarded failure surfaces bash's own status -- 127, 5, or a bare 1 -- and
# a caller reading 1 as "finding" acts on a measurement that never happened.
#
# Line and status are passed as arguments rather than read inside the handler,
# so both describe the command that actually failed. Deliberately NOT `set -E`:
# the trap must stay out of command substitutions so that a query already
# guarded by `|| fail_closed` reports once, not twice.
# shellcheck disable=SC2329  # invoked indirectly by the ERR trap below
on_unhandled_error() {
    fail_closed "unhandled failure at line ${1:-?} (rc=${2:-?})"
}
trap 'on_unhandled_error "$LINENO" "$?"' ERR

# Dependency preflight. `bd` is checked here rather than at first use because
# filing alerts is part of this check's contract: reporting a clean ledger
# while unable to file is the same partial-capability green the exit convention
# exists to remove.
for tool in gc bd jq; do
    command -v "$tool" >/dev/null 2>&1 || fail_closed "$tool not found in PATH"
done

# gc doctor exits nonzero when unrelated BLOCKING checks fail; the
# census-owner-liveness check is advisory-only, so capture the JSON regardless
# of exit code and validate it parses before trusting it. The exit code is kept
# only to make the fail-closed diagnostics specific.
doctor_rc=0
doctor_json="$(gc doctor --json 2>/dev/null)" || doctor_rc=$?

[ -n "$doctor_json" ] ||
    fail_closed "gc doctor --json produced no output (gc exited $doctor_rc)"

printf '%s' "$doctor_json" | jq -e . >/dev/null 2>&1 ||
    fail_closed "gc doctor --json output is not valid JSON (gc exited $doctor_rc)"

check_json="$(printf '%s' "$doctor_json" | jq -c '
  [ .results[]? | select(.name == "census-owner-liveness") ] | .[0] // empty
')" || fail_closed "jq failed reading the census-owner-liveness result"

[ -n "$check_json" ] ||
    fail_closed "census-owner-liveness check not present in gc doctor output (gc exited $doctor_rc)"

check_status="$(printf '%s' "$check_json" | jq -r '.status // empty')" ||
    fail_closed "jq failed reading the census-owner-liveness status"

# Findings and skips both arrive as status=warning details; discriminate them
# on the literal shapes cmd/gc/doctor_census_owner_liveness.go emits:
#   finding  "<label>: dangling owner_bead=<id> rows=[...]"
#   skip     "<label> skipped: <reason>"
dangling_lines="$(printf '%s' "$check_json" |
    jq -r '.details[]? | select(test("dangling owner_bead="))')" ||
    fail_closed "jq failed reading dangling owner_bead findings"

skip_lines="$(printf '%s' "$check_json" |
    jq -r '.details[]? | select(test(" skipped: "))')" ||
    fail_closed "jq failed reading skipped-scope details"

case "$check_status" in
    ok|warning)
        : # both are interpretable; the details decide the verdict below
        ;;
    error)
        fail_closed "gc doctor reports status=error for census-owner-liveness: $check_json"
        ;;
    "")
        fail_closed "census-owner-liveness result carries no status field"
        ;;
    *)
        # Default-deny on an unfamiliar status: a vocabulary this script does
        # not know is not evidence of health.
        fail_closed "unrecognized census-owner-liveness status '$check_status' -- refusing to interpret"
        ;;
esac

created=0

if [ -n "$dangling_lines" ]; then
    owner_beads="$(printf '%s\n' "$dangling_lines" |
        sed -n 's/.*dangling owner_bead=\([^ ]*\).*/\1/p' | sort -u)" ||
        fail_closed "failed extracting owner_bead ids from doctor details"

    [ -n "$owner_beads" ] ||
        fail_closed "doctor reported dangling owner_bead lines but no id parsed from them"

    while IFS= read -r owner_bead; do
        [ -z "$owner_bead" ] && continue

        existing_json="$(bd list --json --label "$alert_label" --status open \
            --metadata-field "census.owner_bead=${owner_bead}")" ||
            fail_closed "bd list dedupe query failed for owner_bead=$owner_bead"

        # An unanswered dedupe query is not an answer of zero. Reading one as
        # zero is how a broken query files a duplicate alert every cron tick.
        existing_count="$(printf '%s' "$existing_json" |
            jq 'if type == "array" then length else empty end')" ||
            fail_closed "dedupe query for owner_bead=$owner_bead returned unparseable JSON"

        [ -n "$existing_count" ] ||
            fail_closed "dedupe query for owner_bead=$owner_bead did not return an array"

        if [ "$existing_count" -gt 0 ]; then
            note "owner_bead=$owner_bead already has an open alert (${existing_count}), not filing another"
            continue
        fi

        # Literal, quoted matching. The id may end the line or be followed by
        # " rows=[...]"; the wrapper must not be load-bearing on a trailing
        # space the doctor format never promised.
        matching_lines=""
        while IFS= read -r detail; do
            case "$detail" in
                *"dangling owner_bead=$owner_bead "*|*"dangling owner_bead=$owner_bead")
                    matching_lines="${matching_lines:+$matching_lines$'\n'}$detail"
                    ;;
            esac
        done <<< "$dangling_lines"

        [ -n "$matching_lines" ] ||
            fail_closed "no detail line matches parsed owner_bead=$owner_bead"

        metadata="$(jq -n --arg routed_to "$routed_to" --arg owner_bead "$owner_bead" \
            '{"gc.routed_to": $routed_to, "census.owner_bead": $owner_bead}')" ||
            fail_closed "failed building alert metadata for owner_bead=$owner_bead"

        description="$(cat <<EOF
gc doctor census-owner-liveness detected a resource-census ledger row
whose owner_bead no longer resolves in the bead store.

owner_bead: ${owner_bead}

Affected rows:
${matching_lines}

Detection only -- no auto-repair. Re-point the ledger row's owner_bead
through council review (see TESTING.md).
EOF
)"

        new_id="$(bd create \
            --title "resource-census ledger references dangling owner_bead ${owner_bead}" \
            --type task \
            --label "$alert_label" \
            --metadata "$metadata" \
            --description "$description" \
            --silent)" ||
            fail_closed "bd create failed for owner_bead=$owner_bead"

        [ -n "$new_id" ] ||
            fail_closed "bd create returned no bead id for owner_bead=$owner_bead"

        created=$((created + 1))
        note "filed ${new_id} for dangling owner_bead=${owner_bead}"
    done <<< "$owner_beads"
fi

# Verdict. Alerts are already filed, so nothing below can lose a finding.
if [ -n "$skip_lines" ]; then
    warn "coverage incomplete -- these scopes were not measured:"
    printf '%s\n' "$skip_lines" | sed 's/^/  /' >&2
    if [ -n "$dangling_lines" ]; then
        warn "${created} alert(s) filed for the scopes that WERE read; that count is a floor, not a total"
    fi
    fail_closed "census-owner-liveness did not read every scope; this is a could-not-run, not a clean ledger"
fi

if [ -n "$dangling_lines" ]; then
    note "done (${created} alert(s) created); dangling owner_bead reference(s) found across all scopes"
    exit "$EXIT_FINDING"
fi

note "no dangling owner_bead references; every scope read (status=$check_status)"
exit "$EXIT_CLEAN"
