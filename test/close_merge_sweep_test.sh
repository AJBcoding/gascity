#!/usr/bin/env bash
# Tests for the rung-5 merge ledger (gas-6g10).
#
# The ledger separates two claims that wear the same symptom: work that is
# PROVEN merged (refinery stamped merged_target + merged_sha) and work that only
# has an INTENDED target (the rig's default_branch). Conflating them is what let
# gas-cj7 and gas-2w4 read closed-and-green while sitting on no mainline.
#
# The trap these tests exist to pin: ancestry must be probed against the REMOTE
# ref, never the bare local branch name. On 2026-08-11 the local
# integration/deploy-20260804 was 400+ commits behind its remote, and judging
# against it would report every merged bead as unmerged.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SWEEP="$ROOT/internal/bootstrap/packs/core/assets/scripts/close-merge-sweep.sh"

PASSED=0
FAILED=0
pass() { printf 'PASS %s\n' "$1"; PASSED=$((PASSED + 1)); }
fail() { printf 'FAIL %s\n' "$1" >&2; FAILED=$((FAILED + 1)); }

[ -x "$SWEEP" ] || { echo "FATAL: $SWEEP missing or not executable" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "SKIP: jq required" >&2; exit 0; }

# A repo with an "origin" that is a real separate directory, so the sweeper's
# self-remote filter keeps it as a publication remote.
make_repo() {
    local dir="$1"
    mkdir -p "$dir/upstream" "$dir/work"
    git init -q --bare "$dir/upstream/repo.git"
    git init -q -b main "$dir/work"
    git -C "$dir/work" config user.email t@t.co
    git -C "$dir/work" config user.name t
    echo seed >"$dir/work/f.txt"
    git -C "$dir/work" add f.txt
    git -C "$dir/work" commit -qm seed
    git -C "$dir/work" remote add origin "$dir/upstream/repo.git"
    git -C "$dir/work" push -q origin main
    git -C "$dir/work" fetch -q origin
}

# Fake gc: `bd list --status=closed` returns $BEADS_JSON, `rig list` returns a
# rig whose default_branch is $RIG_TARGET, `bd create` logs, `bd list --status=open`
# returns whatever has been filed so far (for the dedupe path).
#
# `rig list` MUST emit the real wire shape — an OBJECT with a .rigs[] field,
# mirroring RigListJSON in cmd/gc/cmd_rig.go — never a bare array. This fake
# emitting a bare array is what let the resolver's `.[]` parse pass every test
# here while aborting against the real CLI in production (gas-c8es).
# $GC_RIG_LIST_RAW, when set, replaces the response verbatim so tests can
# exercise the resolution-failure path.
make_fake_gc() {
    local bin="$1"
    mkdir -p "$bin"
    cat >"$bin/gc" <<'FAKE'
#!/usr/bin/env bash
case "$1 $2" in
  "bd list")
    case "$*" in
      *--status=open*) cat "$GC_FILED_JSON" 2>/dev/null || echo '[]' ;;
      *)               cat "$GC_BEADS_JSON" 2>/dev/null || echo '[]' ;;
    esac
    ;;
  "bd create")
    printf '%s\n' "$*" >>"$GC_CREATE_LOG"
    ;;
  "rig list")
    if [ -n "${GC_RIG_LIST_RAW:-}" ]; then
      printf '%s\n' "$GC_RIG_LIST_RAW"
    else
      printf '{"schema_version":"1","city_path":"/city","city_name":"t","rigs":[{"name":"r","path":"%s","prefix":"r","default_branch":"%s","hq":false,"suspended":false,"running":false,"beads":"initialized"}],"summary":{"total":1,"suspended":0,"running":0}}\n' "$GC_RIG_PATH" "$GC_RIG_TARGET"
    fi
    ;;
esac
exit 0
FAKE
    chmod +x "$bin/gc"
}

setup() {
    TMP=$(mktemp -d)
    BIN="$TMP/bin"
    make_fake_gc "$BIN"
    make_repo "$TMP"
    REPO="$TMP/work"
    : >"$TMP/create.log"
    echo '[]' >"$TMP/filed.json"
}

# Run the sweep with a bead set. $1 = beads JSON, $2 = rig default_branch.
run_sweep() {
    local beads="$1" target="$2"
    printf '%s' "$beads" >"$TMP/beads.json"
    GC_MERGE_LEDGER_ROOT="$REPO" \
    GC_MERGE_LEDGER_MIN_AGE_HOURS=0 \
    GC_BEADS_JSON="$TMP/beads.json" \
    GC_FILED_JSON="$TMP/filed.json" \
    GC_CREATE_LOG="$TMP/create.log" \
    GC_RIG_PATH="$REPO" \
    GC_RIG_TARGET="$target" \
    PATH="$BIN:$PATH" \
        bash "$SWEEP" 2>&1
}

remove_origin_publication() {
    git -C "$REPO" remote remove origin
    git -C "$REPO" update-ref -d refs/remotes/origin/main 2>/dev/null || true
}

expect_no_publication_ref_unknown() {
    local label="$1" out="$2"
    [ ! -s "$TMP/create.log" ] ||
        { fail "$label: filed using a non-publication remote ref: $(cat "$TMP/create.log")"; return 1; }
    grep -q 'unknown=1' <<<"$out" ||
        { fail "$label: non-publication remote ref should be UNPROBEABLE, not landed: $out"; return 1; }
    grep -q 'landed=0' <<<"$out" ||
        { fail "$label: non-publication remote ref was counted as landed: $out"; return 1; }
    return 0
}

# move_target_on — put an unrelated commit on the current branch. Without this,
# a branch cut from main and cherry-picked straight back lands on the SAME
# parent with the same tree, author and (within the same second) committer date,
# so git reproduces a byte-identical commit object. Ancestry then answers and
# the rung under test never runs.
move_target_on() {
    printf 'unrelated\n' >>"$1/other.txt"
    git -C "$1" add other.txt && git -C "$1" commit -qm unrelated
}

bead() { # id commit [merged_target] [merged_sha]
    jq -nc --arg id "$1" --arg c "$2" --arg mt "${3:-}" --arg ms "${4:-}" \
      '[{id:$id, closed_at:"2000-01-01T00:00:00Z", metadata:(
          {"gc.work_outcome":"shipped","gc.work_commit":$c}
          + (if $mt == "" then {} else {"merged_target":$mt} end)
          + (if $ms == "" then {} else {"merged_sha":$ms} end))}]'
}

# --- a commit that IS on the remote target must be silent ------------------
test_landed_work_files_nothing() {
    setup
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    local out; out=$(run_sweep "$(bead b-1 "$sha")" main)
    grep -q 'landed=1' <<<"$out" || { fail "landed work not counted as landed: $out"; return; }
    [ ! -s "$TMP/create.log" ] || { fail "filed a bead for work that is on the target"; return; }
    pass "work already on the target files nothing"
}

# --- the core gas-6g10 shape: published, durable, never merged -------------
test_unmerged_work_files_a_merge_gap() {
    setup
    git -C "$REPO" checkout -q -b feature
    echo more >>"$REPO/f.txt"; git -C "$REPO" commit -qam feature
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" push -q origin feature   # published, but not on main
    git -C "$REPO" fetch -q origin

    local out; out=$(run_sweep "$(bead b-2 "$sha")" main)
    grep -q 'unproven=1' <<<"$out" || { fail "unmerged work not counted: $out"; return; }
    grep -q 'merge-gap' "$TMP/create.log" || { fail "no merge-gap bead filed: $(cat "$TMP/create.log")"; return; }
    grep -q 'intended, NOT a proven merge' "$TMP/create.log" ||
        { fail "filed bead does not mark the target as intended rather than proven"; return; }
    pass "published-but-unmerged work files a merge-gap naming the target as intended"
}

# --- a proven merge that stops being true is the loud case -----------------
test_retracted_merge_is_reported_distinctly() {
    setup
    git -C "$REPO" checkout -q -b gone
    echo x >>"$REPO/f.txt"; git -C "$REPO" commit -qam dropped
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" checkout -q main    # the commit never reaches main

    local out; out=$(run_sweep "$(bead b-3 "$sha" main "$sha")" main)
    grep -q 'merge-retracted' "$TMP/create.log" ||
        { fail "a proven merge that no longer holds was not reported: $(cat "$TMP/create.log")"; return; }
    grep -q 'merge-gap' "$TMP/create.log" &&
        { fail "a retracted PROVEN merge was misreported as an intended-target gap"; return; }
    pass "a proven merge that no longer holds is reported distinctly from a merge gap"
}

# --- THE TRAP: a stale local ref must never be used to judge --------------
test_stale_local_ref_is_never_judged_against() {
    setup
    # Advance the remote well past the local branch, then delete the
    # remote-tracking ref so ONLY a stale local "main" remains.
    git -C "$REPO" checkout -q -b ahead
    echo y >>"$REPO/f.txt"; git -C "$REPO" commit -qam ahead
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" push -q origin ahead:main
    git -C "$REPO" update-ref -d refs/remotes/origin/main

    local out; out=$(run_sweep "$(bead b-4 "$sha")" main)
    [ ! -s "$TMP/create.log" ] ||
        { fail "judged against a stale local ref and filed a false alarm: $(cat "$TMP/create.log")"; return; }
    grep -q 'unknown=1' <<<"$out" ||
        { fail "no remote ref should count as UNPROBEABLE, not unmerged: $out"; return; }
    pass "with no remote-tracking ref the sweep stays quiet instead of judging a stale local"
}

# --- non-publication remote refs must never prove a target ----------------
test_relative_self_remote_ref_is_not_landing_evidence() {
    setup
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    remove_origin_publication
    git -C "$REPO" remote add dot-self .
    git -C "$REPO" fetch -q dot-self

    local out; out=$(run_sweep "$(bead b-self-rel "$sha")" main)
    expect_no_publication_ref_unknown "relative self remote" "$out" || return
    pass "a relative self remote-tracking ref is not landing evidence"
}

test_loopback_scp_remote_ref_is_not_landing_evidence() {
    setup
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    remove_origin_publication
    git -C "$REPO" remote add loopback "git@localhost:$REPO"
    git -C "$REPO" update-ref refs/remotes/loopback/main "$sha"

    local out; out=$(run_sweep "$(bead b-self-scp "$sha")" main)
    expect_no_publication_ref_unknown "loopback scp remote" "$out" || return
    pass "a loopback scp remote-tracking ref is not landing evidence"
}

test_missing_local_remote_ref_is_not_landing_evidence() {
    setup
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    remove_origin_publication
    git -C "$REPO" remote add missing "$TMP/missing.git"
    git -C "$REPO" update-ref refs/remotes/missing/main "$sha"

    local out; out=$(run_sweep "$(bead b-missing-local "$sha")" main)
    expect_no_publication_ref_unknown "missing local remote" "$out" || return
    pass "a stale ref from a missing local remote is not landing evidence"
}

# --- read-only: the sweep may never mutate the repo or block --------------
test_sweep_is_read_only_and_always_exits_zero() {
    setup
    local before after out rc
    before=$(git -C "$REPO" rev-parse HEAD)
    out=$(run_sweep '[{"id":"b-5","closed_at":"2000-01-01T00:00:00Z","metadata":{"gc.work_outcome":"shipped","gc.work_commit":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}}]' main)
    rc=$?
    after=$(git -C "$REPO" rev-parse HEAD)
    [ "$before" = "$after" ] || { fail "the sweep moved HEAD"; return; }
    [ "$rc" -eq 0 ] || { fail "sweep exited $rc; a sweeper must never break the controller"; return; }
    pass "sweep is read-only and exits 0 even on an unresolvable commit"
}

# --- a bead too fresh to judge is left alone ------------------------------
test_recent_close_is_left_for_the_next_sweep() {
    setup
    git -C "$REPO" checkout -q -b later
    echo z >>"$REPO/f.txt"; git -C "$REPO" commit -qam later
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    printf '%s' "$(jq -nc --arg c "$sha" '[{id:"b-6",closed_at:"2999-01-01T00:00:00Z",metadata:{"gc.work_outcome":"shipped","gc.work_commit":$c}}]')" >"$TMP/beads.json"
    local out
    out=$(GC_MERGE_LEDGER_ROOT="$REPO" GC_MERGE_LEDGER_MIN_AGE_HOURS=24 \
        GC_BEADS_JSON="$TMP/beads.json" GC_FILED_JSON="$TMP/filed.json" \
        GC_CREATE_LOG="$TMP/create.log" GC_RIG_PATH="$REPO" GC_RIG_TARGET=main \
        PATH="$BIN:$PATH" bash "$SWEEP" 2>&1)
    grep -q 'toosoon=1' <<<"$out" || { fail "a fresh close was judged rather than deferred: $out"; return; }
    [ ! -s "$TMP/create.log" ] || { fail "filed on a close inside the grace period"; return; }
    pass "a close inside the grace period is left for the next sweep"
}

# --- rung 2: a cherry-pick lands the same diff under a different SHA -------
# The mayor's own false positive (py-zwqw): the fix WAS on main, re-applied
# under a different SHA, so ancestry said "missing" on work that was in effect
# the whole time. An ancestry-only ledger files a P1 about a regression that
# does not exist.
test_cherry_picked_work_is_resolved_by_patch_id() {
    setup
    git -C "$REPO" checkout -q -b feature
    printf 'distinctiveCherryIdentifier = 1\n' >>"$REPO/f.txt"
    git -C "$REPO" commit -qam original
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" push -q origin feature      # published, never merged
    git -C "$REPO" checkout -q main
    move_target_on "$REPO" || { fail "setup: could not advance main"; return; }
    # Same diff, different SHA. A silently failed cherry-pick would leave main
    # genuinely missing the work and the test would "pass" for the wrong reason.
    git -C "$REPO" cherry-pick "$sha" >/dev/null 2>&1 ||
        { fail "setup: cherry-pick onto main failed"; return; }
    [ "$(git -C "$REPO" rev-parse HEAD)" != "$sha" ] ||
        { fail "setup: cherry-pick reproduced the same SHA, so ancestry would answer"; return; }
    git -C "$REPO" push -q origin main
    git -C "$REPO" fetch -q origin

    local out; out=$(run_sweep "$(bead b-7 "$sha")" main)
    [ ! -s "$TMP/create.log" ] ||
        { fail "filed on work that landed under a different SHA: $(cat "$TMP/create.log")"; return; }
    grep -q 'patch-id' <<<"$out" ||
        { fail "cherry-picked work was not resolved by the patch-id rung: $out"; return; }
    pass "a cherry-pick is resolved by patch-id, not reported as a merge gap"
}

# --- rung 3: a PORT changes the diff but keeps the identifier --------------
# gas-cj7's own remediation: upstream #4768 had rewritten the function's
# signature, so the fix could not be cherry-picked and was re-authored. Its
# patch-id differs forever. An ancestry+patch-id ledger reports it missing
# forever, on work that is in effect.
test_ported_work_is_resolved_by_symbol_fingerprint() {
    setup
    git -C "$REPO" checkout -q -b feature
    printf 'package gate\n\nfunc workRecordRepoForCommit(commit string) string {\n\treturn commit\n}\n' >"$REPO/gate.go"
    git -C "$REPO" add gate.go
    git -C "$REPO" commit -qm original
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" push -q origin feature
    git -C "$REPO" checkout -q main
    # The port: same distinguishing identifier, genuinely different diff.
    printf 'package gate\n\n// reworked for the new signature\nfunc workRecordRepoForCommit(commit string, repo string) (string, error) {\n\treturn repo, nil\n}\n' >"$REPO/gate.go"
    git -C "$REPO" add gate.go
    git -C "$REPO" commit -qm port
    git -C "$REPO" push -q origin main
    git -C "$REPO" fetch -q origin

    local out; out=$(run_sweep "$(bead b-8 "$sha")" main)
    [ ! -s "$TMP/create.log" ] ||
        { fail "filed on a port whose identifier is present on the target: $(cat "$TMP/create.log")"; return; }
    grep -q 'fingerprint' <<<"$out" ||
        { fail "ported work was not resolved by the fingerprint rung: $out"; return; }
    pass "a re-authored port is resolved by symbol fingerprint, not reported as a merge gap"
}

# --- all three rungs miss: the finding must quote CONTENT evidence ---------
test_absent_work_files_quoting_the_content_evidence() {
    setup
    git -C "$REPO" checkout -q -b feature
    printf 'package gate\n\nfunc neverLandedAnywhereIdentifier() {}\n' >"$REPO/gate.go"
    git -C "$REPO" add gate.go
    git -C "$REPO" commit -qm original
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" push -q origin feature
    git -C "$REPO" checkout -q main

    local out; out=$(run_sweep "$(bead b-9 "$sha")" main)
    grep -q 'merge-gap' "$TMP/create.log" ||
        { fail "genuinely absent work was not filed: $(cat "$TMP/create.log")"; return; }
    grep -q 'neverLandedAnywhereIdentifier' "$TMP/create.log" ||
        { fail "finding does not quote the content evidence a reader can check"; return; }
    pass "when all three rungs miss the finding quotes the absent identifier, not just the SHA"
}

# --- the fingerprint must DISTINGUISH, or the ledger goes quiet on real gaps -
# A commit that merely mentions an identifier the target already has proves
# nothing by that identifier's presence. Counting it turns the fingerprint rung
# into a false GREEN — silence on exactly the unmerged work this bead exists to
# surface, which is the failure direction nobody would notice.
test_preexisting_identifier_is_not_evidence_of_landing() {
    setup
    printf 'package gate\n\nfunc alreadyPresentEverywhere() {}\n' >"$REPO/gate.go"
    git -C "$REPO" add gate.go
    git -C "$REPO" commit -qm base
    git -C "$REPO" push -q origin main
    git -C "$REPO" fetch -q origin

    git -C "$REPO" checkout -q -b feature
    printf 'func brandNewNeverLanded() { alreadyPresentEverywhere() }\n' >>"$REPO/gate.go"
    git -C "$REPO" commit -qam newwork
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" push -q origin feature
    git -C "$REPO" checkout -q main    # the new work never reaches main

    run_sweep "$(bead b-12 "$sha")" main >/dev/null
    grep -q 'merge-gap' "$TMP/create.log" ||
        { fail "an identifier the target already had was taken as proof the work landed"; return; }
    grep -q 'brandNewNeverLanded' "$TMP/create.log" ||
        { fail "the finding cites no identifier the commit actually introduced"; return; }
    pass "an identifier the target already had is not counted as evidence the work landed"
}

# --- a commit this repo cannot read is UNPROBEABLE, exactly like a missing ref
test_unresolvable_commit_is_unprobeable_not_unmerged() {
    setup
    local out
    out=$(run_sweep '[{"id":"b-10","closed_at":"2000-01-01T00:00:00Z","metadata":{"gc.work_outcome":"shipped","gc.work_commit":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}}]' main)
    [ ! -s "$TMP/create.log" ] ||
        { fail "judged a commit it cannot read and filed a false alarm: $(cat "$TMP/create.log")"; return; }
    grep -q 'unknown=1' <<<"$out" ||
        { fail "an unreadable commit should count as UNPROBEABLE, not unmerged: $out"; return; }
    pass "a commit this repo cannot read is left unjudged rather than reported missing"
}

# --- a rewritten target must not turn every proven merge into a retraction -
# The ladder has to cover the PROVEN claim too. Rebasing or filtering the
# target branch changes every SHA on it while keeping the content, so an
# ancestry-only probe reports each proven merge as retracted work.
test_rewritten_target_does_not_manufacture_retractions() {
    setup
    git -C "$REPO" checkout -q -b feature
    printf 'package gate\n\nfunc provenMergeIdentifier() {}\n' >"$REPO/gate.go"
    git -C "$REPO" add gate.go
    git -C "$REPO" commit -qm merged
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    git -C "$REPO" checkout -q main
    move_target_on "$REPO" || { fail "setup: could not advance main"; return; }
    # The same content, arriving under a rewritten history.
    git -C "$REPO" cherry-pick "$sha" >/dev/null 2>&1 ||
        { fail "setup: cherry-pick onto main failed"; return; }
    [ "$(git -C "$REPO" rev-parse HEAD)" != "$sha" ] ||
        { fail "setup: cherry-pick reproduced the same SHA, so ancestry would answer"; return; }
    git -C "$REPO" push -q origin main
    git -C "$REPO" fetch -q origin

    local out; out=$(run_sweep "$(bead b-11 "$sha" main "$sha")" main)
    [ ! -s "$TMP/create.log" ] ||
        { fail "reported a retraction for content still present on the target: $(cat "$TMP/create.log")"; return; }
    grep -q 'equiv=1' <<<"$out" ||
        { fail "a proven merge surviving a rewrite was not resolved by the ladder: $out"; return; }
    pass "a rewritten target does not manufacture merge-retracted findings"
}

# --- the target must resolve from the REAL rig-list wire shape -------------
# The production defect (gas-c8es): the resolver iterated `gc rig list --json`
# as a bare array while the CLI returns an object with a .rigs[] field. jq
# aborted, its stderr was discarded, DEFAULT_BRANCH came out empty, and every
# close without its own gc.work_target fell to UNKNOWN — observed live as
# 'unknown=19 target=<unresolved>'. The sweeper built to catch invisible
# closes was itself inert. The fake above emits the real object shape so the
# parse cannot silently drift again; this test pins the visible symptom.
test_target_resolves_from_the_real_rig_list_shape() {
    setup
    local sha; sha=$(git -C "$REPO" rev-parse HEAD)
    local out; out=$(run_sweep "$(bead b-13 "$sha")" main)
    grep -q 'target=main' <<<"$out" ||
        { fail "the rig-list object shape did not resolve the target: $out"; return; }
    grep -q 'unknown=0' <<<"$out" ||
        { fail "records fell to unknown despite a resolvable target: $out"; return; }
    pass "the intended target resolves from the real rig-list object shape"
}

# --- a FAILED resolution must be loud, and distinct from an empty config ----
# Discarding the resolver's failure is what hid gas-c8es: an aborted parse
# read exactly like "no target configured" and the sweep quietly judged
# nothing. A failure to resolve must say so, and the summary must label it
# distinctly from a config that genuinely names no target — while still
# honoring the exit-0 contract.
test_failed_target_resolution_is_loud_not_silent() {
    setup
    local sha out rc; sha=$(git -C "$REPO" rev-parse HEAD)
    export GC_RIG_LIST_RAW='not json at all'
    out=$(run_sweep "$(bead b-14 "$sha")" main)
    rc=$?
    unset GC_RIG_LIST_RAW
    [ "$rc" -eq 0 ] ||
        { fail "resolution failure broke the exit-0 contract (rc=$rc)"; return; }
    grep -q 'WARNING: intended-target resolution failed' <<<"$out" ||
        { fail "a failed resolution said nothing: $out"; return; }
    grep -q 'target=<resolution-failed>' <<<"$out" ||
        { fail "summary does not distinguish failed resolution from empty config: $out"; return; }
    [ ! -s "$TMP/create.log" ] ||
        { fail "filed findings while the target was unresolvable: $(cat "$TMP/create.log")"; return; }
    pass "a failed target resolution warns, labels the summary distinctly, and exits 0"
}

test_landed_work_files_nothing
test_unmerged_work_files_a_merge_gap
test_retracted_merge_is_reported_distinctly
test_stale_local_ref_is_never_judged_against
test_relative_self_remote_ref_is_not_landing_evidence
test_loopback_scp_remote_ref_is_not_landing_evidence
test_missing_local_remote_ref_is_not_landing_evidence
test_sweep_is_read_only_and_always_exits_zero
test_recent_close_is_left_for_the_next_sweep
test_cherry_picked_work_is_resolved_by_patch_id
test_ported_work_is_resolved_by_symbol_fingerprint
test_absent_work_files_quoting_the_content_evidence
test_preexisting_identifier_is_not_evidence_of_landing
test_unresolvable_commit_is_unprobeable_not_unmerged
test_rewritten_target_does_not_manufacture_retractions
test_target_resolves_from_the_real_rig_list_shape
test_failed_target_resolution_is_loud_not_silent

printf '\n%d passed, %d failed\n' "$PASSED" "$FAILED"
[ "$FAILED" -eq 0 ]
