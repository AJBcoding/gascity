#!/usr/bin/env bash
# Test: close-audit-sweep — rung 4, spool records become beads (gas-2w4)
#
# The rung-3 on_close auditor spools a record for every close it cannot prove
# delivered. Those records are POINT-IN-TIME: in the live spools this sweeper
# was written against, 2 of 3 "not-on-publication-remote" records described
# work that had been pushed moments after the close. A sweeper that filed
# straight from the spool would have been ~67% false positives, which is how a
# watchman gets trained away. So the contract under test is re-verification.
#
# Acceptance criteria:
#   1. Commit now on a publication remote  → files NOTHING (record went stale)
#   2. Commit on no publication remote     → files exactly one bead
#   3. Commit reachable only via a SELF-REFERENTIAL remote → still files
#      (a remote pointing back at this same repo confers no durability, gas-6tc)
#   4. A bead already filed for that record → files nothing (idempotent)
#   5. Malformed spool lines and bd failures never make the sweeper exit non-zero
#   6. shipped with no commit at all → files one bead (the py-1tlv shape)
#   7. A record younger than the grace period → left for the next sweep

set -uo pipefail   # deliberately NOT -e: each case must run even if one probe fails

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SWEEP="$SCRIPT_DIR/../internal/bootstrap/packs/core/assets/scripts/close-audit-sweep.sh"
FAILED=0

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; FAILED=1; }

if [ ! -f "$SWEEP" ]; then
    printf 'ERROR: close-audit-sweep.sh not found at %s\n' "$SWEEP" >&2
    exit 1
fi
command -v jq >/dev/null 2>&1 || { printf 'SKIP: jq not installed\n'; exit 0; }

# ── Fixture helpers ────────────────────────────────────────────────────────

# make_work_repo <dir> — a repo with one commit, plus a real off-host-shaped
# publication remote (a bare repo at another path) that is NOT pushed to yet.
# Echoes the commit SHA.
make_work_repo() {
    local dir="$1"
    git init -q -b main "$dir" >/dev/null 2>&1
    git -C "$dir" config user.email t@t.invalid
    git -C "$dir" config user.name  t
    echo payload > "$dir/file.txt"
    git -C "$dir" add file.txt
    git -C "$dir" commit -qm "work" >/dev/null 2>&1
    git init -q --bare "$dir.origin.git" >/dev/null 2>&1
    git -C "$dir" remote add origin "$dir.origin.git"
    git -C "$dir" rev-parse HEAD
}

# spool <city_root> <work_dir> <id> <commit> <reason> [ts] — write one spool
# record where the rung-3 auditor writes it. ts defaults to a fixed past
# timestamp so the grace-period check is not wall-clock dependent.
spool() {
    local root="$1" work_dir="$2" id="$3" commit="$4" reason="$5"
    local ts="${6:-2020-01-01T00:00:00Z}"
    mkdir -p "$work_dir/.beads/audit"
    printf '{"ts":"%s","id":"%s","commit":"%s","branch":"main","work_dir":"%s","reason":"%s"}\n' \
        "$ts" "$id" "$commit" "$work_dir" "$reason" >> "$work_dir/.beads/audit/close-spool.jsonl"
    : "$root"
}

# now_ts — an ISO-8601 UTC stamp for "just spooled".
now_ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# install_fake_gc <bindir> <dup_hit> — a `gc` stub that logs every invocation to
# $BD_CALLS. The sweeper routes through `gc bd` (per-rig store resolution), so
# the stub matches on $2. `show` reports the bead still closed with no corrected
# work record; `list` (the dedup probe) returns a hit only when dup_hit=1.
install_fake_gc() {
    local bindir="$1" dup_hit="$2"
    mkdir -p "$bindir"
    cat > "$bindir/gc" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "\$BD_CALLS"
[ "\$1" = "bd" ] || exit 0
case "\$2" in
  show)   printf '[{"id":"%s","status":"closed","metadata":{}}]\n' "\$3" ;;
  list)   if [ "$dup_hit" = "1" ]; then printf '[{"id":"dup-1"}]\n'; else printf '[]\n'; fi ;;
  create) printf 'created\n' ;;
  *) : ;;
esac
exit 0
EOF
    chmod +x "$bindir/gc"
}

# run_sweep <city_root> <dup_hit> — run the sweeper with a stubbed gc. Sets
# RUN_N (count of `gc bd create` calls) and RUN_RC. Deliberately not a command
# substitution: that would run the body in a subshell and strand both globals.
RUN_N=0
RUN_RC=0
run_sweep() {
    local root="$1" dup_hit="${2:-0}" min_age="${3:-0}"
    local bindir="$root/.bin"
    install_fake_gc "$bindir" "$dup_hit"
    export BD_CALLS="$root/bd-calls.log"
    : > "$BD_CALLS"
    PATH="$bindir:$PATH" GC_CLOSE_AUDIT_ROOT="$root" \
        GC_CLOSE_AUDIT_MIN_AGE_MIN="$min_age" "$SWEEP" >"$root/out.log" 2>&1
    RUN_RC=$?
    RUN_N=$(grep -c '^bd create' "$BD_CALLS" 2>/dev/null | tr -d ' ')
}

# ── Case 1: commit is published now — the stale record ─────────────────────
t1=$(mktemp -d)
sha=$(make_work_repo "$t1/work")
git -C "$t1/work" push -q origin main >/dev/null 2>&1
spool "$t1" "$t1/work" "gas-aaa" "$sha" "not-on-publication-remote"
run_sweep "$t1"; n="$RUN_N"
if [ "$n" = "0" ] && [ "$RUN_RC" = "0" ]; then
    pass "published commit files nothing (stale spool record)"
else
    fail "published commit should file nothing, filed $n (rc=$RUN_RC)"
fi

# ── Case 2: commit is on no publication remote — the real exposure ─────────
t2=$(mktemp -d)
sha=$(make_work_repo "$t2/work")   # deliberately not pushed
spool "$t2" "$t2/work" "gas-bbb" "$sha" "not-on-publication-remote"
run_sweep "$t2"; n="$RUN_N"
if [ "$n" = "1" ] && [ "$RUN_RC" = "0" ]; then
    pass "unpublished commit files exactly one bead"
else
    fail "unpublished commit should file 1 bead, filed $n (rc=$RUN_RC)"
fi

# ── Case 3: reachable only through a self-referential remote ───────────────
# herdr-src in the live gascity repo is exactly this: a path remote pointing at
# the repo itself. Fetching it creates refs/remotes/*, so a blanket --remotes
# reads single-copy work as published.
t3=$(mktemp -d)
sha=$(make_work_repo "$t3/work")
git -C "$t3/work" remote add herdr-src "$t3/work"
git -C "$t3/work" fetch -q herdr-src >/dev/null 2>&1
spool "$t3" "$t3/work" "gas-ccc" "$sha" "not-on-publication-remote"
run_sweep "$t3"; n="$RUN_N"
if [ "$n" = "1" ] && [ "$RUN_RC" = "0" ]; then
    pass "self-referential remote confers no durability — still files"
else
    fail "self-ref remote should still file 1 bead, filed $n (rc=$RUN_RC)"
fi

# ── Case 4: idempotence — a bead already exists for this record ────────────
t4=$(mktemp -d)
sha=$(make_work_repo "$t4/work")
spool "$t4" "$t4/work" "gas-ddd" "$sha" "not-on-publication-remote"
run_sweep "$t4" 1; n="$RUN_N"
if [ "$n" = "0" ] && [ "$RUN_RC" = "0" ]; then
    pass "already-filed record files nothing (idempotent)"
else
    fail "already-filed record should file 0, filed $n (rc=$RUN_RC)"
fi

# ── Case 5: garbage in, exit 0 out ─────────────────────────────────────────
t5=$(mktemp -d)
mkdir -p "$t5/work/.beads/audit"
printf 'not json at all\n{"id":"gas-eee"\n' > "$t5/work/.beads/audit/close-spool.jsonl"
run_sweep "$t5"; n="$RUN_N"
if [ "$RUN_RC" = "0" ]; then
    pass "malformed spool lines never fail the sweep (rc=0)"
else
    fail "malformed spool should exit 0, got rc=$RUN_RC"
fi

# ── Case 6: shipped with no commit at all — the py-1tlv shape ──────────────
# Five of the seven live spool records are this: gc.work_outcome=shipped with no
# gc.work_commit, so no oracle can confirm or refute the delivery claim.
t6=$(mktemp -d)
make_work_repo "$t6/work" >/dev/null
spool "$t6" "$t6/work" "gas-fff" "" "missing-commit"
run_sweep "$t6"; n="$RUN_N"
if [ "$n" = "1" ] && [ "$RUN_RC" = "0" ]; then
    pass "shipped-with-no-commit files one bead (unfalsifiable delivery claim)"
else
    fail "missing-commit should file 1 bead, filed $n (rc=$RUN_RC)"
fi

# ── Case 7: grace period — a record spooled just now is left alone ─────────
# The honest sequence is commit → close → push, and the push lands seconds to
# minutes after the auditor spools. Judging inside that window is what produced
# the stale records in case 1.
t7=$(mktemp -d)
sha=$(make_work_repo "$t7/work")   # unpushed, so only the grace period spares it
spool "$t7" "$t7/work" "gas-ggg" "$sha" "not-on-publication-remote" "$(now_ts)"
run_sweep "$t7" 0 30; n="$RUN_N"
if [ "$n" = "0" ] && [ "$RUN_RC" = "0" ]; then
    pass "record younger than the grace period is left for the next sweep"
else
    fail "fresh record should file 0, filed $n (rc=$RUN_RC)"
fi

rm -rf "$t1" "$t2" "$t3" "$t4" "$t5" "$t6" "$t7" \
       "$t1/work.origin.git" "$t2/work.origin.git" "$t3/work.origin.git" \
       "$t4/work.origin.git" "$t6/work.origin.git" "$t7/work.origin.git" 2>/dev/null

exit "$FAILED"
