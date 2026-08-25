#!/usr/bin/env bash
# test-check-census-owner-liveness.sh — exit-contract tests for
# scripts/check-census-owner-liveness.sh (gas-xraq).
#
# The script under test is the worked example for the three-valued check exit
# convention (engdocs/contributors/check-exit-code-conventions.md):
#
#   0 — ran, clean:      every census row's owner_bead resolved
#   1 — ran, finding:    at least one dangling owner_bead, full coverage
#   2 — could not run:   a dependency, query, parse, or scope failed
#
# These tests exist because the interesting cases are the ones that used to be
# indistinguishable. Before the conversion every could-not-run path exited 1 —
# the same code a caller reads as a finding — except the three that never
# reached an explicit exit at all and surfaced `bd`'s 127, `jq`'s 5, or a bare
# `set -e` abort with no diagnostic.
#
# The case that motivated the bead is `skip_only_warning_cannot_run`: the
# underlying doctor check reports "I found nothing" and "I could not read that
# scope's bead store" with the same status=warning, and the wrapper used to map
# the second one to exit 0 with the words "nothing to do".
#
# Hermetic: PATH is rebuilt from scratch per case, holding only symlinks to the
# coreutils the script genuinely uses plus purpose-built `gc`/`bd` stubs. That
# is what makes "the tool is missing" a case we can actually test — dropping a
# real directory from PATH cannot, because jq lives in /usr/bin here.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$TEST_DIR/check-census-owner-liveness.sh"
# Resolved once, and invoked by absolute path below: the hermetic PATH each
# case runs under deliberately contains no interpreter.
BASH_BIN="$(command -v bash)"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 — $2"; fail=$((fail + 1)); }

# Coreutils the script under test actually invokes. Everything else it uses is
# a bash builtin — deliberately, so the hermetic PATH stays this short.
REAL_TOOLS=(sed sort cat)

# new_bindir [--no-jq] [--no-gc] [--no-bd]: a PATH directory holding only what
# the case wants visible. Prints its path.
new_bindir() {
    local no_jq=0 no_gc=0 no_bd=0 d t src arg
    for arg in "$@"; do
        case "$arg" in
            --no-jq) no_jq=1 ;;
            --no-gc) no_gc=1 ;;
            --no-bd) no_bd=1 ;;
            *) echo "new_bindir: unknown arg $arg" >&2; return 1 ;;
        esac
    done

    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-ccol-test.XXXXXX")"
    for t in "${REAL_TOOLS[@]}"; do
        src="$(command -v "$t")" || { echo "new_bindir: $t not found" >&2; return 1; }
        ln -s "$src" "$d/$t"
    done
    if [ "$no_jq" -eq 0 ]; then
        src="$(command -v jq)" || { echo "new_bindir: jq not found" >&2; return 1; }
        ln -s "$src" "$d/jq"
    fi

    # Stubs are /bin/sh scripts, not `env bash` ones: the hermetic PATH has no
    # bash in it, and a shebang that had to look one up would fail as ENOENT
    # and read like the stub itself was missing.
    if [ "$no_gc" -eq 0 ]; then
        cat > "$d/gc" <<'GC_STUB'
#!/bin/sh
# Only `gc doctor --json` is reached by the script under test.
if [ "${1:-}" = "doctor" ]; then
    printf '%s' "${STUB_DOCTOR_OUT-}"
    exit "${STUB_DOCTOR_EXIT:-0}"
fi
echo "stub gc: unexpected args: $*" >&2
exit 127
GC_STUB
        chmod +x "$d/gc"
    fi

    if [ "$no_bd" -eq 0 ]; then
        cat > "$d/bd" <<'BD_STUB'
#!/bin/sh
case "${1:-}" in
    list)
        printf '%s' "${STUB_BD_LIST_OUT-[]}"
        exit "${STUB_BD_LIST_EXIT:-0}"
        ;;
    create)
        printf '%s' "${STUB_BD_CREATE_OUT-stub-alert-1}"
        exit "${STUB_BD_CREATE_EXIT:-0}"
        ;;
esac
echo "stub bd: unexpected args: $*" >&2
exit 127
BD_STUB
        chmod +x "$d/bd"
    fi

    printf '%s' "$d"
}

# expect <label> <bindir> <want-exit> [want-substring]: run the real script
# with <bindir> as the entire PATH and compare the exit code, optionally also
# requiring a substring in the combined output.
#
# Cases pass stub fixtures as an environment prefix on this function. Bash
# keeps such assignments in the shell after a *function* returns, so clear them
# here — otherwise one case's doctor output silently becomes the next case's
# input and a green result proves nothing about the case that printed it.
expect() {
    local label="$1" bindir="$2" want="$3" want_out="${4:-}" out ec
    out=$(PATH="$bindir" "$BASH_BIN" "$SCRIPT" 2>&1)
    ec=$?

    if [ "$ec" -ne "$want" ]; then
        record_fail "$label" "exit=$ec, expected $want
$out"
    elif [ -n "$want_out" ] && [[ "$out" != *"$want_out"* ]]; then
        record_fail "$label" "exit ok, but output lacks '$want_out'
$out"
    else
        record_pass "$label"
    fi

    rm -rf "$bindir"
    unset STUB_DOCTOR_OUT STUB_DOCTOR_EXIT \
          STUB_BD_LIST_OUT STUB_BD_LIST_EXIT \
          STUB_BD_CREATE_OUT STUB_BD_CREATE_EXIT
}

# --- fixtures --------------------------------------------------------------
#
# Detail strings mirror cmd/gc/doctor_census_owner_liveness.go verbatim:
#   finding — "%s: dangling owner_bead=%s rows=[%s]"
#   skip    — "%s skipped: %s"

doctor_ok() {
    printf '{"results":[{"name":"census-owner-liveness","status":"ok","details":[]}]}'
}

# status=warning carrying ONLY skips. The check measured nothing in that scope;
# reporting it as a pass is the silent success this convention exists to kill.
doctor_warning_skip_only() {
    printf '{"results":[{"name":"census-owner-liveness","status":"warning","details":["rig kit skipped: opening bead store: dial tcp 127.0.0.1:3306: connection refused"]}]}'
}

doctor_warning_dangling() {
    printf '{"results":[{"name":"census-owner-liveness","status":"warning","details":["city: dangling owner_bead=ga-dead1 rows=[debt: scope=internal/api resource=coverage]"]}]}'
}

# A detail line whose bead id runs to end-of-line. The doctor format happens to
# put " rows=[" after the id today, but the wrapper must not be load-bearing on
# that: the pre-conversion `grep -F "...=$id "` found nothing here and aborted
# under `set -e` with no diagnostic.
doctor_warning_dangling_no_trailing_space() {
    printf '{"results":[{"name":"census-owner-liveness","status":"warning","details":["city: dangling owner_bead=ga-dead1"]}]}'
}

# Both a real finding and an unreadable scope. Coverage was incomplete, so the
# run cannot assert "exactly one dangling owner" — but the finding it did see
# must still be filed.
doctor_warning_dangling_and_skip() {
    printf '{"results":[{"name":"census-owner-liveness","status":"warning","details":["city: dangling owner_bead=ga-dead1 rows=[debt: scope=internal/api resource=coverage]","rig kit skipped: opening bead store: dial tcp 127.0.0.1:3306: connection refused"]}]}'
}

doctor_error_status() {
    printf '{"results":[{"name":"census-owner-liveness","status":"error","details":["loading resource-census ledger: permission denied"]}]}'
}

doctor_missing_check() {
    printf '{"results":[{"name":"some-other-check","status":"ok","details":[]}]}'
}

# --- exit 0: ran, clean ----------------------------------------------------

test_clean_ledger_passes() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_ok)" \
        expect "clean ledger exits 0" "$d" 0
}

# --- exit 1: ran, finding --------------------------------------------------

test_dangling_owner_is_a_finding() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_warning_dangling)" \
        expect "dangling owner_bead exits 1" "$d" 1 "filed stub-alert-1"
}

test_dangling_owner_already_alerted_is_still_a_finding() {
    local d; d="$(new_bindir)"
    # An alert bead is already open for this owner: the script files nothing
    # new, but the ledger is still broken, so the verdict is unchanged.
    STUB_DOCTOR_OUT="$(doctor_warning_dangling)" \
    STUB_BD_LIST_OUT='[{"id":"ga-existing-alert"}]' \
        expect "dangling owner_bead with an existing alert still exits 1" "$d" 1
}

test_dangling_owner_without_trailing_space_is_a_finding() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_warning_dangling_no_trailing_space)" \
        expect "dangling owner_bead at end-of-line exits 1 and still files" \
               "$d" 1 "filed stub-alert-1"
}

# --- exit 2: could not run -------------------------------------------------

test_skip_only_warning_cannot_run() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_warning_skip_only)" \
        expect "warning carrying only skips exits 2" "$d" 2
}

test_finding_with_skips_cannot_run_but_still_files() {
    local d; d="$(new_bindir)"
    # Incomplete coverage dominates the verdict, but the alert is still filed:
    # the exit code loses nothing, it just stops overstating what was measured.
    STUB_DOCTOR_OUT="$(doctor_warning_dangling_and_skip)" \
        expect "finding alongside a skipped scope exits 2 and still files" \
               "$d" 2 "filed stub-alert-1"
}

test_error_status_cannot_run() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_error_status)" \
        expect "doctor status=error exits 2" "$d" 2
}

test_missing_gc_fails_closed() {
    local d; d="$(new_bindir --no-gc)"
    expect "gc absent exits 2" "$d" 2
}

test_missing_bd_fails_closed() {
    local d; d="$(new_bindir --no-bd)"
    STUB_DOCTOR_OUT="$(doctor_warning_dangling)" \
        expect "bd absent exits 2" "$d" 2
}

test_missing_jq_fails_closed() {
    local d; d="$(new_bindir --no-jq)"
    STUB_DOCTOR_OUT="$(doctor_ok)" \
        expect "jq absent exits 2" "$d" 2
}

test_doctor_no_output_fails_closed() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="" STUB_DOCTOR_EXIT=1 \
        expect "gc doctor producing no output exits 2" "$d" 2
}

test_unparseable_doctor_json_fails_closed() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT='{"results": [ truncated' \
        expect "unparseable doctor JSON exits 2" "$d" 2
}

test_absent_census_check_fails_closed() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_missing_check)" \
        expect "census check absent from doctor output exits 2" "$d" 2
}

test_dedupe_query_failure_fails_closed() {
    local d; d="$(new_bindir)"
    # The pre-conversion hole: `bd list` fails, `jq length` sees nothing,
    # ${existing_count:-0} reads 0, and the script files a duplicate alert on
    # the strength of a query that never answered.
    STUB_DOCTOR_OUT="$(doctor_warning_dangling)" \
    STUB_BD_LIST_EXIT=1 STUB_BD_LIST_OUT="" \
        expect "failed dedupe query exits 2" "$d" 2
}

test_dedupe_query_returning_garbage_fails_closed() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_warning_dangling)" \
    STUB_BD_LIST_OUT='not json' \
        expect "unparseable dedupe query result exits 2" "$d" 2
}

test_alert_creation_failure_fails_closed() {
    local d; d="$(new_bindir)"
    STUB_DOCTOR_OUT="$(doctor_warning_dangling)" \
    STUB_BD_CREATE_EXIT=1 STUB_BD_CREATE_OUT="" \
        expect "failed alert creation exits 2" "$d" 2
}

test_clean_ledger_passes
test_dangling_owner_is_a_finding
test_dangling_owner_already_alerted_is_still_a_finding
test_dangling_owner_without_trailing_space_is_a_finding
test_skip_only_warning_cannot_run
test_finding_with_skips_cannot_run_but_still_files
test_error_status_cannot_run
test_missing_gc_fails_closed
test_missing_bd_fails_closed
test_missing_jq_fails_closed
test_doctor_no_output_fails_closed
test_unparseable_doctor_json_fails_closed
test_absent_census_check_fails_closed
test_dedupe_query_failure_fails_closed
test_dedupe_query_returning_garbage_fails_closed
test_alert_creation_failure_fails_closed

echo "----"
echo "test-check-census-owner-liveness.sh: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
