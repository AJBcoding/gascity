#!/usr/bin/env bash
#
# test-push-worktree-guard.sh — unit tests for the .githooks/pre-push gate:
# the wrong-worktree guard and verdict header (gas-4pz), the free-disk
# preflight (gas-wnq / gas-9nx), and affected-package scoping (gas-qnav).
# One harness for the one hook, against the REAL .githooks/pre-push copied
# into real temp repos pushing to real bare remotes:
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
#   3. Free-disk preflight: refuses up front, with the number, instead of
#      letting the fan-out die partway through as an inscrutable build error;
#      fails open when the disk cannot be measured.
#   4. Affected-package scoping: the hook asks scripts/ci-static-select which
#      packages the pushed range affects and hands that set to the suite.
#      Narrowing is only sound when the tree the suite tests IS the tree
#      being pushed, so every other shape — a new remote branch, a HEAD
#      merely contained in the pushed ref, a multi-ref push, an unusable
#      selector, an empty answer — falls back to the full suite. The gate
#      must fail toward more testing, never less.
#   5. Runner inventory: the suite the gate execs (scripts/test-local-parallel)
#      must build its fast-mode job inventory from the handed set, and mode
#      full — the batch/landing gate — must ignore it entirely.
#
# A stub Makefile stands in for the real one (its test-fast-parallel target
# echoes SUITE-RAN and the scope it was handed), so tests can assert whether
# and with what scope the suite was reached without actually running it.
# No network, no bd, no models.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"
HOOK="$REPO_ROOT/.githooks/pre-push"
SELECTOR="$REPO_ROOT/scripts/ci-static-select"
RUNNER="$REPO_ROOT/scripts/test-local-parallel"
OWNERSHIP_GUARD="$REPO_ROOT/scripts/push-ownership-guard.sh"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 — $2"; fail=$((fail + 1)); }

# Deterministic, hermetic git identity for the temp repos.
export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="author@example.com"
export GIT_COMMITTER_NAME="Test Pusher" GIT_COMMITTER_EMAIL="pusher@example.com"
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
unset GIT_DIR GIT_WORK_TREE 2>/dev/null || true
# The scoping scenarios hold a standalone temp module: keep the outer repo's
# Go environment out.
export GOFLAGS='' GOENV=off GOWORK=off

# The hook also carries a free-disk preflight (gas-9nx). Every scenario below
# except the disk ones is about other behavior, and would otherwise take a
# real verdict from however much space the host happens to have — a low-disk CI
# box would fail the whole file for the wrong reason. Disable it by default so
# these stay hermetic; the disk scenarios set the floor explicitly.
export GC_PREPUSH_MIN_FREE_GIB=0

# The hook also sources scripts/push-ownership-guard.sh (ga-fip9ps.1) before
# every non-deletion push. These temp repos have no bead behind them, so the
# guard is neutralized exactly the way its own header sanctions for hermetic
# harnesses against synthetic repos: POG_DISABLE=1 short-circuits
# assert_bead_still_claimed, and the install helpers copy the guard script so
# the hook's `source` finds it. The guard's own behavior has its own harness
# (scripts/test-push-ownership-guard.sh); here it must only never be the
# reason an unrelated scenario's push succeeds or fails.
export POG_DISABLE=1

# ---------------------------------------------------------------------------
# Repo/remote helpers (mirrors scripts/test-push-ownership-guard.sh's
# harness on main; kept standalone because that harness is not on every
# deploy lineage).
#
# Physical paths (pwd -P) on purpose: macOS TMPDIR lives under the /var ->
# /private/var symlink, git resolves hook working directories physically, and
# `go list` reports physical package directories. A logical temp path makes
# the selector judge every package "outside the repository" and fall back to
# the full suite, which would make the scoping tests pass vacuously. Real
# worktrees under $HOME are not symlinked, so this matches production.
# ---------------------------------------------------------------------------

new_bare_remote() {
    local d
    d="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/gc-pwg-remote.XXXXXX")" && pwd -P)"
    git init -q --bare -b main "$d"
    printf '%s' "$d"
}

remote_sha() {
    git -C "$1" rev-parse --verify -q "$2" 2>/dev/null || true
}

install_hook() {
    local repo="$1"
    mkdir -p "$repo/.githooks" "$repo/scripts"
    cp "$HOOK" "$repo/.githooks/pre-push"
    chmod +x "$repo/.githooks/pre-push"
    cp "$OWNERSHIP_GUARD" "$repo/scripts/push-ownership-guard.sh"
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

install_scope_hook() {
    local repo="$1"
    mkdir -p "$repo/.githooks" "$repo/scripts"
    cp "$HOOK" "$repo/.githooks/pre-push"
    chmod +x "$repo/.githooks/pre-push"
    cp "$OWNERSHIP_GUARD" "$repo/scripts/push-ownership-guard.sh"
    cp "$SELECTOR" "$repo/scripts/ci-static-select"
    chmod +x "$repo/scripts/ci-static-select"
    # Echo the scope instead of running a suite, so tests can assert what the
    # gate WOULD run. $(LOCAL_TEST_PACKAGES) is a make expansion reading the
    # exported environment, not a shell expansion.
    # shellcheck disable=SC2016
    printf 'test-fast-parallel:\n\t@echo "SUITE-RAN SCOPE=[$(LOCAL_TEST_PACKAGES)]"\n' > "$repo/Makefile"
    git -C "$repo" config core.hooksPath .githooks
}

# setup_scope_scenario: bare remote + work clone holding a small real Go
# module so the selector can walk a real build-input graph:
#   base  — depended on by mid (stands in for internal/citylayout)
#   mid   — imports base
#   leaf  — imported by nothing
# Branch wb exists on both sides, so pushes to it have a base to diff.
# Echoes "<remote-dir> <work-dir>" on one line.
setup_scope_scenario() {
    local remote work
    remote="$(new_bare_remote)"
    work="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/gc-pwg-scope.XXXXXX")" && pwd -P)"
    git clone -q "$remote" "$work" 2>/dev/null
    git -C "$work" config commit.gpgsign false
    install_scope_hook "$work"
    printf 'module example.com/prepush\n\ngo 1.23\n' > "$work/go.mod"
    mkdir -p "$work/base" "$work/mid" "$work/leaf"
    printf 'package base\n\nfunc Value() int { return 1 }\n' > "$work/base/base.go"
    printf 'package mid\n\nimport "example.com/prepush/base"\n\nfunc Value() int { return base.Value() }\n' > "$work/mid/mid.go"
    printf 'package leaf\n\nfunc Value() int { return 1 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt base
    git -C "$work" push -q --no-verify origin main
    git -C "$work" checkout -q -b wb
    git -C "$work" push -q --no-verify origin wb
    printf '%s %s' "$remote" "$work"
}

# scope_of extracts the scope the stub suite was handed.
scope_of() { sed -n 's/.*SUITE-RAN SCOPE=\[\(.*\)\].*/\1/p' <<<"$1" | tail -1; }

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
# Free-disk preflight (gas-wnq / gas-9nx). The sharded suite compiles multi-GiB
# test binaries per job; started on a full disk it dies partway through as an
# inscrutable build error, or parks the agent running it at a prompt it cannot
# get past. These pin that it refuses first, with the number, and that it never
# becomes the reason a push cannot happen.
# ---------------------------------------------------------------------------

test_disk_preflight_blocks_push_below_floor() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q wb
    # A floor no host can satisfy, so the scenario is about the refusal, not
    # about this machine's actual free space.
    out="$(cd "$work" && GC_PREPUSH_MIN_FREE_GIB=999999 git push origin wb 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] \
        && [[ -z "$(remote_sha "$remote" "refs/heads/wb")" ]] \
        && grep -q "REFUSING" <<<"$out" \
        && grep -q "999999 GiB" <<<"$out" \
        && grep -q -- "--no-verify" <<<"$out" \
        && ! grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "disk/blocks-push-below-floor (refused before the suite, names the floor + bypass)"
    else
        record_fail "disk/blocks-push-below-floor" "expected refusal naming the floor with no SUITE-RAN, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

test_disk_preflight_zero_floor_disables() {
    local remote work out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q wb
    out="$(cd "$work" && GC_PREPUSH_MIN_FREE_GIB=0 git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && grep -q "SUITE-RAN" <<<"$out" && ! grep -q "REFUSING" <<<"$out"; then
        record_pass "disk/zero-floor-disables (documented escape hatch works)"
    else
        record_fail "disk/zero-floor-disables" "expected the suite to run with the check disabled, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# Regression test for a real bug in the first cut of this check: the hook runs
# under `set -euo pipefail`, so `free_kib="$(df ... | awk ...)"` with a df that
# cannot stat the worktree aborted the hook and REFUSED the push — inverting the
# fail-open contract. A preflight that cannot measure the disk must never be why
# a push stops working.
test_disk_preflight_fails_open_when_df_unreadable() {
    local remote work stub out rc
    read -r remote work <<<"$(setup_scenario)"
    git -C "$work" checkout -q wb
    stub="$(mktemp -d "${TMPDIR:-/tmp}/gc-pwg-stub.XXXXXX")"
    printf '#!/bin/sh\nexit 1\n' > "$stub/df"
    chmod +x "$stub/df"
    out="$(cd "$work" && PATH="$stub:$PATH" GC_PREPUSH_MIN_FREE_GIB=10 git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] \
        && grep -q "SUITE-RAN" <<<"$out" \
        && grep -q "fail-open" <<<"$out" \
        && ! grep -q "REFUSING" <<<"$out"; then
        record_pass "disk/fails-open-when-df-unreadable (unmeasurable disk does not block the push)"
    else
        record_fail "disk/fails-open-when-df-unreadable" "expected fail-open reaching the suite, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work" "$stub"
}

test_disk_preflight_precedes_suite_exec() {
    local disk_line exec_line
    disk_line="$(grep -n "GC_PREPUSH_MIN_FREE_GIB" "$HOOK" 2>/dev/null | head -1 | cut -d: -f1)"
    exec_line="$(grep -n "exec make test-fast-parallel" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    if [[ -n "$disk_line" && -n "$exec_line" && "$disk_line" -lt "$exec_line" ]]; then
        record_pass "wiring/disk-preflight-precedes-suite-exec"
    else
        record_fail "wiring/disk-preflight-precedes-suite-exec" "disk_line=$disk_line exec_line=$exec_line (the disk check must run before the suite it is protecting)"
    fi
}

# ---------------------------------------------------------------------------
# Affected-package scoping (gas-qnav): scoping the pushed range.
# ---------------------------------------------------------------------------

test_leaf_change_scopes_to_its_package() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt leaf-change
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./leaf" ]]; then
        record_pass "scope/leaf-change-scopes-to-its-package (./leaf)"
    else
        record_fail "scope/leaf-change-scopes-to-its-package" "expected scope './leaf', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

# The constraint that makes scoping safe to ship: a change to a
# widely-depended-on package must select its dependents too, so the affected
# set is large exactly when it should be.
test_widely_depended_change_selects_dependents() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    printf 'package base\n\nfunc Value() int { return 2 }\n' > "$work/base/base.go"
    commit_file "$work" doc.txt base-change
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./base ./mid" ]]; then
        record_pass "scope/widely-depended-change-selects-dependents (./base ./mid)"
    else
        record_fail "scope/widely-depended-change-selects-dependents" "expected scope './base ./mid', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

# ---------------------------------------------------------------------------
# Affected-package scoping: fallbacks — every one of these must run the full
# suite, never an empty or partial scope.
# ---------------------------------------------------------------------------

test_new_remote_branch_runs_full_suite() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    git -C "$work" checkout -q -b fresh
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt fresh-change
    # No base on the remote to diff against.
    out="$(cd "$work" && git push origin fresh 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./..." ]]; then
        record_pass "fallback/new-remote-branch-runs-full-suite"
    else
        record_fail "fallback/new-remote-branch-runs-full-suite" "expected full scope './...', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

# HEAD contained in the pushed ref is allowed by the wrong-worktree guard, but
# the tested tree is then NOT the pushed tip, so a scope computed from the
# pushed range would not describe what the suite runs.
test_ancestor_head_runs_full_suite() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt leaf-change
    git -C "$work" checkout -q HEAD~1
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./..." ]]; then
        record_pass "fallback/ancestor-head-runs-full-suite"
    else
        record_fail "fallback/ancestor-head-runs-full-suite" "expected full scope './...', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

# A stack push sends more than the tested tree; one scope cannot describe it.
test_multi_ref_push_runs_full_suite() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt leaf-change
    git -C "$work" checkout -q -b wb2
    printf 'package mid\n\nimport "example.com/prepush/base"\n\nfunc Value() int { return base.Value() + 1 }\n' > "$work/mid/mid.go"
    commit_file "$work" doc.txt mid-change
    git -C "$work" push -q --no-verify origin wb2
    commit_file "$work" doc.txt more
    out="$(cd "$work" && git push origin wb wb2 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./..." ]]; then
        record_pass "fallback/multi-ref-push-runs-full-suite"
    else
        record_fail "fallback/multi-ref-push-runs-full-suite" "expected full scope './...', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

test_missing_selector_runs_full_suite() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    rm -f "$work/scripts/ci-static-select"
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt leaf-change
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./..." ]]; then
        record_pass "fallback/missing-selector-runs-full-suite"
    else
        record_fail "fallback/missing-selector-runs-full-suite" "expected full scope './...', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

# A broken package graph makes the affected set uncomputable. The selector
# reports ./... for that, and the gate must honor it rather than narrow.
test_uncomputable_scope_runs_full_suite() {
    local remote work out rc scope
    read -r remote work <<<"$(setup_scope_scenario)"
    printf 'package broken\n\nimport _ "example.com/prepush/missing"\n' > "$work/leaf/broken.go"
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt broken-graph
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    scope="$(scope_of "$out")"
    if [[ $rc -eq 0 ]] && [[ "$scope" == "./..." ]]; then
        record_pass "fallback/uncomputable-scope-runs-full-suite"
    else
        record_fail "fallback/uncomputable-scope-runs-full-suite" "expected full scope './...', got rc=$rc scope='$scope' output: $out"
    fi
    rm -rf "$remote" "$work"
}

# ---------------------------------------------------------------------------
# Affected-package scoping: behavior the scoping must not disturb.
# ---------------------------------------------------------------------------

test_docs_only_push_still_skips_the_suite() {
    local remote work out rc
    read -r remote work <<<"$(setup_scope_scenario)"
    commit_file "$work" doc.txt docs-only
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -eq 0 ]] && ! grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "unchanged/docs-only-push-still-skips-the-suite"
    else
        record_fail "unchanged/docs-only-push-still-skips-the-suite" "expected no suite, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

test_wrong_worktree_guard_still_rejects() {
    local remote work out rc
    read -r remote work <<<"$(setup_scope_scenario)"
    printf 'package leaf\n\nfunc Value() int { return 2 }\n' > "$work/leaf/leaf.go"
    commit_file "$work" doc.txt leaf-change
    git -C "$work" checkout -q -b parked main
    commit_file "$work" p.txt parked-only
    out="$(cd "$work" && git push origin wb 2>&1)"; rc=$?
    if [[ $rc -ne 0 ]] \
        && grep -q "REFUSING" <<<"$out" \
        && ! grep -q "SUITE-RAN" <<<"$out"; then
        record_pass "unchanged/wrong-worktree-guard-still-rejects"
    else
        record_fail "unchanged/wrong-worktree-guard-still-rejects" "expected rejection before the suite, got rc=$rc output: $out"
    fi
    rm -rf "$remote" "$work"
}

# ---------------------------------------------------------------------------
# Affected-package scoping: static wiring — the scope must be resolved after
# the guard (so a refused push never pays for a go list) and before the suite
# exec.
# ---------------------------------------------------------------------------

test_scope_resolved_between_guard_and_suite() {
    local guard_line scope_line exec_line
    guard_line="$(grep -n "merge-base --is-ancestor" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    scope_line="$(grep -n "list-affected" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    exec_line="$(grep -n "exec make test-fast-parallel" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    if [[ -n "$guard_line" && -n "$scope_line" && -n "$exec_line" \
        && "$guard_line" -lt "$scope_line" && "$scope_line" -lt "$exec_line" ]]; then
        record_pass "wiring/scope-resolved-between-guard-and-suite"
    else
        record_fail "wiring/scope-resolved-between-guard-and-suite" "guard_line=$guard_line scope_line=$scope_line exec_line=$exec_line (guard must precede scope selection, which must precede exec make)"
    fi
}

# The bead-ownership re-check (ga-fip9ps.1) must run before every suite-side
# stage — a push blocked as stale must never pay for a worktree verdict, a
# disk probe, or a go list.
test_ownership_guard_precedes_worktree_guard() {
    local own_line guard_line
    own_line="$(grep -n "push-ownership-guard.sh" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    guard_line="$(grep -n "merge-base --is-ancestor" "$HOOK" 2>/dev/null | tail -1 | cut -d: -f1)"
    if [[ -n "$own_line" && -n "$guard_line" && "$own_line" -lt "$guard_line" ]]; then
        record_pass "wiring/ownership-guard-precedes-worktree-guard"
    else
        record_fail "wiring/ownership-guard-precedes-worktree-guard" "own_line=$own_line guard_line=$guard_line (the ownership re-check must precede the suite-side guards)"
    fi
}

# ---------------------------------------------------------------------------
# Runner inventory — the gate hands scripts/test-local-parallel a package set
# via LOCAL_TEST_PACKAGES; the fast suite must build its job inventory from
# that set, and mode full (the batch/landing gate) must ignore it entirely,
# so whole-repository coverage is still paid once per batch — structurally,
# not by convention.
#
# --print-jobs is a flag rather than an environment variable on purpose: an
# unrecognized flag makes the runner exit on its usage check, so a regression
# here fails in milliseconds. An unrecognized environment variable would be
# ignored and the runner would execute the real multi-hour fan-out instead.
# ---------------------------------------------------------------------------

# runner_jobs <mode> [VAR=value ...] — print the "label::command" inventory
# the canonical runner would execute for mode, without running it. The
# ambient LOCAL_TEST_PACKAGES is dropped first so the unscoped cases stay
# hermetic when this harness itself runs under a scoped gate.
runner_jobs() {
    local mode="$1"; shift
    (cd "$REPO_ROOT" && env -u LOCAL_TEST_PACKAGES CMD_GC_PROCESS_TOTAL=2 "$@" "$RUNNER" --print-jobs "$mode")
}

job_labels() { cut -d: -f1 <<<"$1" | paste -s -d ' ' -; }
job_command() { sed -n "s/^$2:://p" <<<"$1"; }

test_runner_unscoped_fast_runs_whole_module() {
    local out labels command
    out="$(runner_jobs fast)" || { record_fail "runner/unscoped-fast-runs-whole-module" "inventory failed: $out"; return; }
    labels="$(job_labels "$out")"
    command="$(job_command "$out" unit-core)"
    if [[ "$labels" == "fsys-darwin-compile unit-core push-gate-lock-selftest local-concurrency-selftest unit-cmd-gc-1-of-2 unit-cmd-gc-2-of-2" ]] \
        && grep -qF "go list ./..." <<<"$command"; then
        record_pass "runner/unscoped-fast-runs-whole-module"
    else
        record_fail "runner/unscoped-fast-runs-whole-module" "labels='$labels' unit-core='$command' (want the whole-module inventory)"
    fi
}

# The scoped unit-core command must keep the unscoped sweep's -count=1 and
# shared timeout budget: scoping narrows WHAT runs, never HOW it runs. The
# gate-machinery selftests stay in every fast inventory, scoped or not.
test_runner_selection_replaces_whole_module_expansion() {
    local out labels command
    out="$(runner_jobs fast "LOCAL_TEST_PACKAGES=./internal/alpha ./internal/beta")" \
        || { record_fail "runner/selection-replaces-whole-module" "inventory failed: $out"; return; }
    labels="$(job_labels "$out")"
    command="$(job_command "$out" unit-core)"
    if [[ "$labels" == "fsys-darwin-compile unit-core push-gate-lock-selftest local-concurrency-selftest" ]] \
        && grep -qF " ./internal/alpha ./internal/beta" <<<"$command" \
        && grep -qF -- "-count=1" <<<"$command" \
        && grep -qF -- "-timeout" <<<"$command" \
        && ! grep -qF "go list ./..." <<<"$command"; then
        record_pass "runner/selection-replaces-whole-module (cmd/gc shards dropped, unit-core scoped with the shared budget)"
    else
        record_fail "runner/selection-replaces-whole-module" "labels='$labels' unit-core='$command' (want fsys+unit-core+selftests with unit-core testing exactly the selection under -count=1 and the shared timeout)"
    fi
}

test_runner_cmd_gc_selection_keeps_shards_and_scopes_rest() {
    local out labels command
    out="$(runner_jobs fast "LOCAL_TEST_PACKAGES=./cmd/gc ./internal/alpha")" \
        || { record_fail "runner/cmd-gc-selection-keeps-shards" "inventory failed: $out"; return; }
    labels="$(job_labels "$out")"
    command="$(job_command "$out" unit-core)"
    if [[ "$labels" == "fsys-darwin-compile unit-core push-gate-lock-selftest local-concurrency-selftest unit-cmd-gc-1-of-2 unit-cmd-gc-2-of-2" ]] \
        && grep -qF " ./internal/alpha" <<<"$command" \
        && ! grep -qF "./cmd/gc" <<<"$command"; then
        record_pass "runner/cmd-gc-selection-keeps-shards (shards kept, unit-core scoped to the rest)"
    else
        record_fail "runner/cmd-gc-selection-keeps-shards" "labels='$labels' unit-core='$command' (want the full fast inventory with unit-core testing only ./internal/alpha)"
    fi
}

test_runner_cmd_gc_only_selection_drops_unit_core() {
    local out labels
    out="$(runner_jobs fast "LOCAL_TEST_PACKAGES=./cmd/gc")" \
        || { record_fail "runner/cmd-gc-only-drops-unit-core" "inventory failed: $out"; return; }
    labels="$(job_labels "$out")"
    if [[ "$labels" == "fsys-darwin-compile push-gate-lock-selftest local-concurrency-selftest unit-cmd-gc-1-of-2 unit-cmd-gc-2-of-2" ]]; then
        record_pass "runner/cmd-gc-only-drops-unit-core"
    else
        record_fail "runner/cmd-gc-only-drops-unit-core" "labels='$labels' (want the shards and selftests without unit-core)"
    fi
}

test_runner_explicit_whole_module_selection_matches_unscoped() {
    local scoped unscoped
    scoped="$(runner_jobs fast "LOCAL_TEST_PACKAGES=./...")" \
        || { record_fail "runner/whole-module-selection-matches-unscoped" "inventory failed: $scoped"; return; }
    unscoped="$(runner_jobs fast)" \
        || { record_fail "runner/whole-module-selection-matches-unscoped" "inventory failed: $unscoped"; return; }
    if [[ "$scoped" == "$unscoped" ]]; then
        record_pass "runner/whole-module-selection-matches-unscoped (./... is the unscoped inventory)"
    else
        record_fail "runner/whole-module-selection-matches-unscoped" "scoped inventory diverged from unscoped: scoped='$scoped' unscoped='$unscoped'"
    fi
}

test_runner_landing_gate_stays_full_suite() {
    local scoped unscoped
    scoped="$(runner_jobs full "LOCAL_TEST_PACKAGES=./internal/alpha")" \
        || { record_fail "runner/landing-gate-stays-full-suite" "inventory failed: $scoped"; return; }
    unscoped="$(runner_jobs full)" \
        || { record_fail "runner/landing-gate-stays-full-suite" "inventory failed: $unscoped"; return; }
    if [[ "$scoped" == "$unscoped" ]]; then
        record_pass "runner/landing-gate-stays-full-suite (mode full ignores LOCAL_TEST_PACKAGES)"
    else
        record_fail "runner/landing-gate-stays-full-suite" "mode full honored a package selection: scoped='$scoped' unscoped='$unscoped'"
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
    test_disk_preflight_blocks_push_below_floor
    test_disk_preflight_zero_floor_disables
    test_disk_preflight_fails_open_when_df_unreadable
    test_disk_preflight_precedes_suite_exec
    test_leaf_change_scopes_to_its_package
    test_widely_depended_change_selects_dependents
    test_new_remote_branch_runs_full_suite
    test_ancestor_head_runs_full_suite
    test_multi_ref_push_runs_full_suite
    test_missing_selector_runs_full_suite
    test_uncomputable_scope_runs_full_suite
    test_docs_only_push_still_skips_the_suite
    test_wrong_worktree_guard_still_rejects
    test_scope_resolved_between_guard_and_suite
    test_ownership_guard_precedes_worktree_guard
    test_runner_unscoped_fast_runs_whole_module
    test_runner_selection_replaces_whole_module_expansion
    test_runner_cmd_gc_selection_keeps_shards_and_scopes_rest
    test_runner_cmd_gc_only_selection_drops_unit_core
    test_runner_explicit_whole_module_selection_matches_unscoped
    test_runner_landing_gate_stays_full_suite

    echo
    echo "pass=$pass fail=$fail"
    # This file runs without `set -e`, so a mistyped case name above would
    # vanish silently instead of failing. Pin the case count so lost coverage
    # is loud.
    local expected_cases=30
    if [[ $((pass + fail)) -ne $expected_cases ]]; then
        echo "FAIL: recorded $((pass + fail)) cases, want $expected_cases — a case was lost or double-counted"
        return 1
    fi
    [[ $fail -eq 0 ]]
}

run_all
