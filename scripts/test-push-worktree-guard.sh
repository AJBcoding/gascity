#!/usr/bin/env bash
#
# test-push-worktree-guard.sh — unit tests for the pre-push wrong-worktree
# guard and verdict header (gas-4pz). One repo with many worktrees means a
# push can run from a directory parked on a different branch than the one
# being pushed; the hook tests the CHECKED-OUT tree, so its verdict was
# silently attributed to the pushed ref (a fork-local failure on the parked
# deploy branch got filed as "origin/main is RED"). Two behaviors under test,
# against the REAL .githooks/pre-push copied into real temp repos pushing to
# real bare remotes:
#
#   1. Wrong-worktree guard: when the suite would run (Go sources changed)
#      and the tested HEAD is not contained in (ancestor of or equal to) any
#      ref the push is sending, the hook refuses loudly — naming the checked-
#      out branch, the pushed refs, and the --no-verify bypass — instead of
#      producing a verdict about the wrong tree. Pushes whose HEAD is an
#      ancestor of the pushed ref, stack pushes that include the tested tip,
#      deletion-only pushes, and non-Go pushes are all left alone.
#   2. Verdict header: when the suite does run, the hook names the branch,
#      SHA, and worktree it is testing plus the refs being pushed, so a
#      misattributed verdict is visible in the output instead of invisible.
#
# A stub Makefile stands in for the real one (its test-fast-parallel target
# prints SUITE-RAN), so tests can assert whether the suite was reached
# without actually running it. No network, no bd, no models.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"
HOOK="$REPO_ROOT/.githooks/pre-push"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 — $2"; fail=$((fail + 1)); }

# Deterministic, hermetic git identity for the temp repos.
export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="author@example.com"
export GIT_COMMITTER_NAME="Test Pusher" GIT_COMMITTER_EMAIL="pusher@example.com"
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
unset GIT_DIR GIT_WORK_TREE 2>/dev/null || true

# ---------------------------------------------------------------------------
# Repo/remote helpers (mirrors scripts/test-push-ownership-guard.sh's
# harness on main; kept standalone because that harness is not on every
# deploy lineage).
# ---------------------------------------------------------------------------

new_bare_remote() {
    local d
    d="$(mktemp -d "${TMPDIR:-/tmp}/gc-pwg-remote.XXXXXX")"
    git init -q --bare -b main "$d"
    printf '%s' "$d"
}

remote_sha() {
    git -C "$1" rev-parse --verify -q "$2" 2>/dev/null || true
}

install_hook() {
    local repo="$1"
    mkdir -p "$repo/.githooks"
    cp "$HOOK" "$repo/.githooks/pre-push"
    chmod +x "$repo/.githooks/pre-push"
    printf 'test-fast-parallel:\n\t@echo SUITE-RAN\n' > "$repo/Makefile"
    git -C "$repo" config core.hooksPath .githooks
}

commit_file() { # commit_file <repo> <path> <message>
    local repo="$1" path="$2" msg="$3"
    printf '%s\n' "$msg" >> "$repo/$path"
    git -C "$repo" add -A
    git -C "$repo" commit -qm "$msg"
}

# setup_scenario: bare remote + work clone with the real hook installed.
# Branches in the clone:
#   main    — base commit (a.go + f.txt), pushed to the remote
#   wb      — main + one *.go commit; NOT on the remote (bead-work shape)
#   parked  — main + one *.txt commit; diverged sibling standing in for the
#             repo root parked on a deploy branch
# Echoes "<remote-dir> <work-dir>" on one line.
setup_scenario() {
    local remote work
    remote="$(new_bare_remote)"
    work="$(mktemp -d "${TMPDIR:-/tmp}/gc-pwg-work.XXXXXX")"
    git clone -q "$remote" "$work" 2>/dev/null
    git -C "$work" config commit.gpgsign false
    install_hook "$work"
    printf 'package main\n' > "$work/a.go"
    commit_file "$work" f.txt base
    git -C "$work" push -q --no-verify origin main
    git -C "$work" checkout -q -b wb
    printf 'package wb\n' > "$work/w.go"
    commit_file "$work" f.txt work
    git -C "$work" checkout -q main
    git -C "$work" checkout -q -b parked
    commit_file "$work" p.txt parked-only
    printf '%s %s' "$remote" "$work"
}

# ---------------------------------------------------------------------------
# Wrong-worktree guard.
# ---------------------------------------------------------------------------

test_blocks_push_from_parked_worktree() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    # HEAD=parked, pushing wb: parked is neither ancestor of nor equal to wb.
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] \
        && [[ -z "$(remote_sha "$remote" "refs/heads/wb")" ]] \
        && grep -q "parked" <<<"$out" \
        && grep -q "refs/heads/wb" <<<"$out" \
        && grep -q -- "--no-verify" <<<"$out" \
        && ! grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "guard/blocks-push-from-parked-worktree (rejected before the suite, names both sides + bypass)"
    else
        record_fail "guard/blocks-push-from-parked-worktree" "expected rejected push naming parked+refs/heads/wb+--no-verify with no SUITE-RAN, got rc=$rc remote_sha=$(remote_sha "$remote" "refs/heads/wb") output: $out"
    fi
    rm -rf "$remote" "$work"
}

test_no_verify_bypasses_guard() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    out="$(cd "$work" && git push --no-verify origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && [[ -n "$(remote_sha "$remote" "refs/heads/wb")" ]]; then
        record_pass "guard/no-verify-bypasses (push succeeded from the wrong worktree)"
    else
        record_fail "guard/no-verify-bypasses" "expected successful push, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

test_allows_push_from_own_worktree() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q wb
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && [[ -n "$(remote_sha "$remote" "refs/heads/wb")" ]] && grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "guard/allows-push-from-own-worktree (suite ran)"
    else
        record_fail "guard/allows-push-from-own-worktree" "expected successful push reaching the suite, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# The tested HEAD being an ancestor of the pushed ref is allowed: the verdict
# is about code the push contains (it can under-test new commits, but it can
# never attribute a failure from a tree the push does not contain — the
# false-P0 mode this guard exists to stop).
test_allows_push_when_head_is_ancestor_of_pushed_ref() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q main
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && [[ -n "$(remote_sha "$remote" "refs/heads/wb")" ]] && grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "guard/allows-ancestor-head (HEAD=main contained in pushed wb)"
    else
        record_fail "guard/allows-ancestor-head" "expected successful push reaching the suite, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# A stack push that includes the tested tip is legitimate: the tree under
# test IS one of the refs being sent, and every other ref in the stack is
# contained in it.
test_allows_stack_push_from_tip() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q -B stack-a main
    printf 'package aa\n' > "$work/aa.go"
    commit_file "$work" f.txt stack-a
    git -C "$work" checkout -q -b stack-b
    printf 'package bb\n' > "$work/bb.go"
    commit_file "$work" f.txt stack-b
    out="$(cd "$work" && git push origin stack-a stack-b 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] \
        && [[ -n "$(remote_sha "$remote" "refs/heads/stack-a")" ]] \
        && [[ -n "$(remote_sha "$remote" "refs/heads/stack-b")" ]]; then
        record_pass "guard/allows-stack-push-from-tip (HEAD=stack-b is one of the pushed refs)"
    else
        record_fail "guard/allows-stack-push-from-tip" "expected both refs pushed, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

test_deletion_push_skips_gate() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" push -q --no-verify origin wb   # seed the remote branch
    # HEAD=parked; a deletion-only push has nothing to test and must pass.
    out="$(cd "$work" && git push origin :wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && [[ -z "$(remote_sha "$remote" "refs/heads/wb")" ]]; then
        record_pass "guard/deletion-push-skips-gate (delete allowed from parked worktree)"
    else
        record_fail "guard/deletion-push-skips-gate" "expected successful deletion, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# No Go changes in the pushed range → no suite, no verdict, nothing to
# misattribute: the guard must not block doc-only pushes from a parked
# worktree.
test_docs_only_push_from_parked_worktree_allowed() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" push -q --no-verify origin wb   # wb now exists on the remote
    git -C "$work" checkout -q wb
    commit_file "$work" doc.txt docs-only
    git -C "$work" checkout -q parked
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && ! grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "guard/docs-only-push-from-parked-allowed (no suite, no guard)"
    else
        record_fail "guard/docs-only-push-from-parked-allowed" "expected successful push with no suite run, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# ---------------------------------------------------------------------------
# Verdict header.
# ---------------------------------------------------------------------------

test_verdict_header_names_tested_head_and_pushed_refs() {
    local remote work out rc short
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q wb
    short="$(git -C "$work" rev-parse --short HEAD)"
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] \
        && grep -q "pre-push: testing wb@$short" <<<"$out" \
        && grep -q "refs/heads/wb@$short" <<<"$out"; then
        record_pass "header/names-tested-head-and-pushed-refs (wb@$short)"
    else
        record_fail "header/names-tested-head-and-pushed-refs" "expected 'pre-push: testing wb@$short' and 'refs/heads/wb@$short', got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# ---------------------------------------------------------------------------
# Static wiring — the guard and header must sit between the go_changed
# early-exit and the suite exec, so they run exactly when a verdict exists.
# ---------------------------------------------------------------------------

test_guard_and_header_precede_suite_exec() {
    local guard_line header_line exec_line
    guard_line="$(grep -n "merge-base --is-ancestor" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    header_line="$(grep -n "pre-push: testing" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    exec_line="$(grep -n "exec make test-fast-parallel" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    if [[ -n "$guard_line" && -n "$header_line" && -n "$exec_line" \
        && "$guard_line" -lt "$header_line" && "$header_line" -lt "$exec_line" ]]; then
        record_pass "wiring/guard-and-header-precede-suite-exec"
    else
        record_fail "wiring/guard-and-header-precede-suite-exec" "guard_line=$guard_line header_line=$header_line exec_line=$exec_line (guard must precede header, header must precede exec make)"
    fi
}

# ---------------------------------------------------------------------------
# Runner
# ---------------------------------------------------------------------------

run_all() {
    test_blocks_push_from_parked_worktree
    test_no_verify_bypasses_guard
    test_allows_push_from_own_worktree
    test_allows_push_when_head_is_ancestor_of_pushed_ref
    test_allows_stack_push_from_tip
    test_deletion_push_skips_gate
    test_docs_only_push_from_parked_worktree_allowed
    test_verdict_header_names_tested_head_and_pushed_refs
    test_guard_and_header_precede_suite_exec

    echo
    echo "pass=$pass fail=$fail"
    [[ $fail -eq 0 ]]
}

run_all
