#!/usr/bin/env bash
# Guards the local test runner's environment isolation (gas-2ypy).
#
# scripts/test-local-parallel goes to real trouble to control what a test job
# sees: `env -i` with a hand-curated allowlist, so a developer's shell cannot
# change what a gate measures. It then ran the job through `bash -lc`, and a
# LOGIN shell sources /etc/profile, which on macOS runs path_helper and
# REPLACES PATH with the bare system default. The carefully passed
# PATH="${PATH}" was discarded by the next process in the chain.
#
# That is not a style question. internal/worker/adapters/zcode forwards
# os.Getenv("PATH") into the adapter subprocess it drives, so the stripped PATH
# reached the child, the harness could not start a turn, and eleven tests failed
# — deterministically, at any CPU budget, and never standalone. Those eleven
# were read as flaky-under-load for days and kept the merge gate permanently
# red, which is what made every merge in the city impossible.
#
# Measured 2026-08-22, same tree, same host, same load, only the wrapper varying:
#
#   go test ./internal/worker/adapters/zcode          ok   7.08s
#   bash -c  'go test ./internal/worker/adapters/zcode'   ok   6.72s
#   bash -lc 'go test ./internal/worker/adapters/zcode'   FAIL 11 tests
#   env -i <allowlist> bash -lc '...'                     FAIL 11 tests
#   env -i <allowlist> bash -c  '...'                     ok   6.55s
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
RUNNER="$ROOT/scripts/test-local-parallel"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# Only lines the runner EXECUTES are examined. The comment at the invocation
# site has to keep naming `bash -lc` — that is the whole explanation of why the
# rule exists — and a guard that cannot tell prose from code would forbid its
# own rationale.
runnable_lines() {
    grep -vE '^[[:space:]]*#' "$RUNNER"
}

# The regression anchor. A login shell re-imports the developer's profile that
# `env -i` just finished excluding, so the two cannot both be right.
test_the_runner_does_not_start_jobs_in_a_login_shell() {
    local hits
    hits=$(runnable_lines | grep -n 'bash -lc\|bash -l ' || true)
    [[ -z "$hits" ]] ||
        fail "the runner starts jobs in a LOGIN shell, which re-sources the profile and discards the PATH the env -i allowlist passed (gas-2ypy):
$hits"
}

# `env -i` naming PATH explicitly is what makes the login shell a contradiction
# rather than a redundancy: the allowlist has an opinion about PATH, so nothing
# downstream may overwrite it.
test_the_allowlist_still_passes_path_explicitly() {
    grep -q 'PATH="\${PATH}"' "$RUNNER" ||
        fail "the runner no longer passes PATH through its env -i allowlist; if that is deliberate, this test and its reasoning need rewriting"
}

# WHY the anchor above matters, verified against the platform rather than
# asserted from memory. If a future platform stops resetting PATH in login
# shells, the rationale for the anchor has changed and someone should know that
# rather than find a rule with no reason behind it.
test_a_login_shell_really_does_discard_a_passed_path() {
    local sentinel passed_c passed_lc
    sentinel="/gas-2ypy-sentinel-$$"

    passed_c=$(env -i PATH="$sentinel:/usr/bin:/bin" bash -c 'printf %s "$PATH"' 2>/dev/null || true)
    passed_lc=$(env -i PATH="$sentinel:/usr/bin:/bin" bash -lc 'printf %s "$PATH"' 2>/dev/null || true)

    case "$passed_c" in
        "$sentinel"*) ;;
        *) fail "a non-login shell did not preserve the passed PATH; the runner's isolation cannot work at all: got '$passed_c'" ;;
    esac

    case "$passed_lc" in
        "$sentinel"*)
            echo "note: on this platform a login shell PRESERVES the passed PATH."
            echo "      gas-2ypy's mechanism does not reproduce here, so the anchor above is"
            echo "      guarding against a hazard this host does not currently exhibit."
            ;;
        *)
            # Precisely: path_helper does not erase the passed PATH, it DEMOTES
            # it below the system defaults, and entries that came from a shell
            # rc rather than the login profile (homebrew's node@22, nvm) are
            # absent entirely. Either effect changes which binary a child
            # resolves, which is all the zcode adapter needed to break.
            echo "confirmed: a login shell does not preserve the passed PATH's precedence here."
            echo "           got: $passed_lc"
            echo "           that is the gas-2ypy mechanism, so the anchor is load-bearing."
            ;;
    esac
}

test_the_runner_does_not_start_jobs_in_a_login_shell
test_the_allowlist_still_passes_path_explicitly
test_a_login_shell_really_does_discard_a_passed_path

echo "local test runner environment guards passed"
