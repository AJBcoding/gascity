#!/usr/bin/env bash
# close-audit-sweep — ladder rung 4: spool records become beads (gas-2w4).
#
# The rung-3 on_close auditor (az-sh66) fires after every close, decides fast
# whether the close is provably delivered, and spools anything it cannot prove
# to <work_dir>/.beads/audit/close-spool.jsonl. Nothing read those spools. The
# findings reached humans only when a witness patrol happened to notice one and
# sent mail, which is one-at-a-time while the generator keeps running.
#
# This sweeper closes that loop: it re-verifies each spooled record and files a
# bead for the ones that are still unproven.
#
# RE-VERIFICATION IS THE WHOLE DESIGN. Spool records are point-in-time
# assertions made microseconds after a close, and the common benign shape is
# "committed, closed, pushed twenty seconds later". In the live spools this was
# written against, 2 of 3 "not-on-publication-remote" records described work
# that was published by the time anyone looked. Filing straight from the spool
# would have been ~67% false positives — and a watchman that cries wolf twice
# for every real catch is one agents learn to wave through (the beads#4960
# reach-for---force class). So every record is re-probed against live git and
# live bead state, and only what still fails is filed.
#
# Runs as an exec order (no LLM, no agent, no wisp): every decision here is a
# git containment probe or a metadata read. Exits 0 on EVERY path — a sweeper
# that breaks the controller is a worse bug than the one it reports.
#
# DURABILITY SEMANTICS ARE SHARED, AND MUST NOT DRIFT. Publication remotes are
# configured remotes that can actually publish away from this repository. A URL
# that resolves back into this same repository (including relative spellings and
# loopback network spellings) or a missing local path confers no durability: a
# blanket --remotes probe reads single-copy work as published on exactly the rig
# that guards gc. That rule now has three implementations: the gc gate
# (cmd/gc/work_record_gate.go gitPublicationRemotes), the rung-3 auditor
# (.beads/hooks/on_close), and this file. They are one semantic. If one changes,
# all three change — drift between them is the failure this ladder exists to
# prevent.
#
# KNOWN LIMIT — the spool is append-only, so every record is re-probed on every
# sweep and the cost grows with lifetime closes, not with open exposure (~2 `gc
# bd` calls per record; the first live sweep over 5 records took ~37s). It is
# deliberately NOT compacted here: the rung-3 auditor appends to the same file
# from close hooks, and a rewrite-and-rename that races an append drops a
# finding — the one failure a data-loss watchman may never have. Compaction
# needs the auditor to rotate, or a separate resolved-ledger. Tracked
# separately; revisit before the spools reach the low hundreds.
#
# Written for bash 3.2 (macOS /bin/bash) as well as 5.x: no associative arrays,
# no empty-array expansion under set -u.
set -uo pipefail   # deliberately NOT -e: no probe failure may abort the sweep

__SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$__SCRIPT_DIR/_bd_trace.sh" ]; then
    # shellcheck disable=SC1091
    . "$__SCRIPT_DIR/_bd_trace.sh" "close-audit-sweep"
fi

ROOT="${GC_CLOSE_AUDIT_ROOT:-${GT_TOWN_ROOT:-$PWD}}"
DRY_RUN="${GC_CLOSE_AUDIT_DRY_RUN:-}"
MAXDEPTH="${GC_CLOSE_AUDIT_MAXDEPTH:-9}"
# Records younger than this are left for the next sweep. The auditor fires
# microseconds after the close, but the honest sequence is commit → close →
# push, and the push lands seconds to minutes later. Filing inside that window
# reports work that is about to be published. The grace period is what keeps
# the benign shape out of the bead tracker entirely.
MIN_AGE_MIN="${GC_CLOSE_AUDIT_MIN_AGE_MIN:-30}"

command -v jq >/dev/null 2>&1 || exit 0
command -v git >/dev/null 2>&1 || exit 0

FILED=0
RESOLVED=0
DEDUPED=0
UNKNOWN=0
TOOSOON=0

# cutoff_ts — the newest spool timestamp old enough to act on, as an ISO-8601
# UTC string. ISO-8601 UTC sorts lexicographically, so callers string-compare.
# Empty output disables the check rather than filing everything.
cutoff_ts() {
    date -u -d "$MIN_AGE_MIN minutes ago" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null && return 0
    date -u -v-"${MIN_AGE_MIN}"M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null && return 0
    printf ''
}
CUTOFF=$(cutoff_ts)

# common_dir <path> — absolute, symlink-resolved git common dir, or empty. This
# is the identity used for self-remote detection: a repo root and any of its
# worktrees resolve to the same common dir.
common_dir() {
    local d="$1" out
    [ -n "$d" ] || return 0
    case "$d" in -*) return 0 ;; esac
    out=$(git -C "$d" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
    if [ -z "$out" ]; then
        # git < 2.31 has no --path-format; fall back to a manual resolve.
        out=$(cd "$d" 2>/dev/null && git rev-parse --git-common-dir 2>/dev/null) || return 0
        [ -n "$out" ] || return 0
        case "$out" in
            /*) ;;
            *) out=$(cd "$d" 2>/dev/null && cd "$out" 2>/dev/null && pwd -P) || return 0 ;;
        esac
    fi
    ( cd "$out" 2>/dev/null && pwd -P ) 2>/dev/null || printf '%s' "$out"
}

repo_url_base() {
    local repo="$1" out
    out=$(git -C "$repo" rev-parse --path-format=absolute --show-toplevel 2>/dev/null)
    if [ -n "$out" ]; then
        ( cd "$out" 2>/dev/null && pwd -P ) 2>/dev/null || printf '%s' "$out"
        return 0
    fi
    common_dir "$repo"
}

is_loopback_host() {
    local host
    host=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
    host="${host#[}"
    host="${host%]}"
    case "$host" in
        localhost|localhost.localdomain|127.*|::1|0:0:0:0:0:0:0:1) return 0 ;;
    esac
    return 1
}

# remote_local_path <repo> <url> — prints a local filesystem path and returns 0
# when the URL addresses this host, returns 1 when it names another host, and
# returns 2 when it is local-shaped but not usable as publication evidence.
remote_local_path() {
    local repo="$1" url="$2" rest hostpart host path base colon slash
    [ -n "$url" ] || return 2
    case "$url" in
        file://*)
            rest="${url#file://}"
            case "$rest" in
                /*) path="$rest" ;;
                *)
                    hostpart="${rest%%/*}"
                    if [ "$hostpart" = "$rest" ]; then
                        return 2
                    fi
                    if ! is_loopback_host "$hostpart"; then
                        return 1
                    fi
                    path="/${rest#*/}"
                    ;;
            esac
            [ -n "$path" ] || return 2
            printf '%s' "$path"
            return 0
            ;;
        *://*)
            rest="${url#*://}"
            hostpart="${rest%%/*}"
            if [ "$hostpart" = "$rest" ]; then
                return 2
            fi
            path="/${rest#*/}"
            host="${hostpart#*@}"
            case "$host" in
                \[*\]*) host="${host#\[}"; host="${host%%\]*}" ;;
                *) host="${host%%:*}" ;;
            esac
            if is_loopback_host "$host"; then
                [ -n "$path" ] || return 2
                printf '%s' "$path"
                return 0
            fi
            return 1
            ;;
    esac

    colon="${url%%:*}"
    if [ "$colon" != "$url" ]; then
        slash="${url%%/*}"
        if [ "$slash" = "$url" ] || [ ${#colon} -lt ${#slash} ]; then
            host="${colon#*@}"
            if is_loopback_host "$host"; then
                path="${url#*:}"
                case "$path" in
                    /*) printf '%s' "$path"; return 0 ;;
                    *) return 2 ;;
                esac
            fi
            return 1
        fi
    fi

    case "$url" in
        /*) path="$url" ;;
        *)
            base=$(repo_url_base "$repo")
            [ -n "$base" ] || return 2
            path="$base/$url"
            ;;
    esac
    printf '%s' "$path"
    return 0
}

is_publication_remote() {
    local repo="$1" self="$2" url="$3" path rc c
    path=$(remote_local_path "$repo" "$url")
    rc=$?
    case "$rc" in
        0) ;;
        1) return 0 ;;  # off-host
        *) return 1 ;;
    esac
    [ -n "$path" ] || return 1
    c=$(common_dir "$path")
    if [ -n "$c" ] && [ -n "$self" ] && [ "$c" = "$self" ]; then
        return 1
    fi
    [ -e "$path" ] || return 1
    return 0
}

configured_remote_count() {
    git -C "$1" remote 2>/dev/null | awk 'NF { n++ } END { print n + 0 }'
}

# publication_globs <repo> — one `--glob=refs/remotes/<name>/*` per remote that
# can actually confer durability, newline separated. Empty output means the repo
# has nowhere to publish.
publication_globs() {
    local repo="$1" self remotes name url
    self=$(common_dir "$repo")
    remotes=$(git -C "$repo" remote 2>/dev/null) || return 1
    while IFS= read -r name; do
        [ -n "$name" ] || continue
        case "$name" in -*) continue ;; esac
        url=$(git -C "$repo" remote get-url "$name" 2>/dev/null)
        if [ -z "$url" ]; then
            continue
        fi
        if is_publication_remote "$repo" "$self" "$url"; then
            printf -- '--glob=refs/remotes/%s/*\n' "$name"
        fi
    done <<EOF
$remotes
EOF
}

# is_published <repo> <sha> — 0 = published (or nowhere to publish),
# 1 = on no publication remote, 2 = could not probe.
is_published() {
    local repo="$1" sha="$2" configured globs out
    [ -n "$repo" ] && [ -n "$sha" ] || return 2
    case "$sha" in -*) return 2 ;; esac
    git -C "$repo" rev-parse --git-dir >/dev/null 2>&1 || return 2
    git -C "$repo" rev-parse --verify --quiet "${sha}^{commit}" >/dev/null 2>&1 || return 2
    configured=$(configured_remote_count "$repo") || return 2
    globs=$(publication_globs "$repo") || return 2
    if [ -z "$globs" ]; then
        # No configured remotes at all ⇒ local-only rig ⇒ durability not
        # applicable. Configured remotes that all filter out are a durability
        # misconfiguration: the work is not published anywhere.
        [ "$configured" = "0" ] && return 0
        return 1
    fi
    set --
    while IFS= read -r g; do
        [ -n "$g" ] && set -- "$@" "$g"
    done <<EOF
$globs
EOF
    out=$(git -C "$repo" rev-list --max-count=1 "$sha" --not "$@" 2>/dev/null) || return 2
    [ -z "$out" ] && return 0
    return 1
}

# already_filed <work_dir> <id> — 0 when a bead already carries this record's
# marker. Searches closed beads too: re-filing a record whose audit bead someone
# already triaged is the duplicate-noise failure this sweeper must not create.
already_filed() {
    local work_dir="$1" id="$2" out
    out=$( cd "$work_dir" 2>/dev/null && gc bd list --all \
             --metadata-field "gc.close_audit_source=$id" \
             --json --limit 1 2>/dev/null )
    [ -n "$out" ] || return 1
    [ "$(printf '%s' "$out" | jq -r 'length' 2>/dev/null || echo 0)" != "0" ]
}

# sweep_record <id> <commit> <branch> <work_dir> <reason> <ts>
sweep_record() {
    local id="$1" commit="$2" branch="$3" work_dir="$4" reason="$5" ts="$6"
    local show status live_commit probe_commit title desc rc

    [ -n "$id" ] || return 0
    [ -n "$work_dir" ] && [ -d "$work_dir" ] || work_dir="$ROOT"

    # Too fresh to judge: the push may still be in flight. Leave it spooled.
    if [ -n "$CUTOFF" ] && [ -n "$ts" ] && [ "$ts" ">" "$CUTOFF" ]; then
        TOOSOON=$((TOOSOON + 1)); return 0
    fi

    # ── Re-verify against LIVE bead state, not the spooled snapshot ──────────
    show=$( cd "$work_dir" 2>/dev/null && gc bd show "$id" --json 2>/dev/null )
    if [ -z "$show" ]; then
        # Cannot read the bead — say nothing rather than file on a read failure.
        UNKNOWN=$((UNKNOWN + 1)); return 0
    fi
    status=$(printf '%s' "$show" | jq -r '.[0].status // ""' 2>/dev/null)
    if [ -n "$status" ] && [ "$status" != "closed" ]; then
        # Reopened since the spool: the close is no longer asserting delivery.
        RESOLVED=$((RESOLVED + 1)); return 0
    fi
    live_commit=$(printf '%s' "$show" | jq -r '.[0].metadata["gc.work_commit"] // ""' 2>/dev/null)

    # A corrected work record supersedes the spooled one.
    probe_commit="$live_commit"
    [ -n "$probe_commit" ] || probe_commit="$commit"
    [ "$probe_commit" = "null" ] && probe_commit=""

    if [ -n "$probe_commit" ]; then
        is_published "$work_dir" "$probe_commit"
        rc=$?
        case "$rc" in
            0) RESOLVED=$((RESOLVED + 1)); return 0 ;;   # published after the close
            2) UNKNOWN=$((UNKNOWN + 1)); return 0 ;;     # unprobeable: stay quiet
        esac
        title="Close-publication: $id closed as shipped but its commit is on no publication remote"
        desc="The rung-3 close auditor spooled this close at $ts and a re-probe at sweep time still cannot find the work on any publication remote.

  bead:      $id
  commit:    $probe_commit
  branch:    ${branch:-<unstamped>}
  work_dir:  $work_dir
  spooled:   $reason

The commit exists in the local object database and on no remote that can
publish it off this host. Any worktree prune or branch GC destroys it. Remotes
whose URL resolves back into this same repository were excluded — they confer
no durability (gas-6tc).

To resolve: push the branch carrying $probe_commit to a real remote, then close
this bead. If the work was superseded, correct the record with
  gc bd update $id --set-metadata gc.work_outcome=abandoned"
    else
        # outcome=shipped with no commit anywhere: an unfalsifiable delivery
        # claim. py-1tlv is this exact shape.
        title="Close-publication: $id closed as shipped with no commit recorded"
        desc="The rung-3 close auditor spooled this close at $ts and the bead still
records no gc.work_commit, so the delivery claim cannot be checked at all.

  bead:      $id
  branch:    ${branch:-<unstamped>}
  work_dir:  $work_dir
  spooled:   $reason

A close carrying gc.work_outcome=shipped with no commit asserts delivery that
no oracle can confirm or refute. Either the work landed and the record is
incomplete, or it never landed and the deliverable is gone.

To resolve: stamp the delivering commit with
  gc bd update $id --set-metadata gc.work_commit=<sha>
or correct the outcome to no-op/blocked/abandoned if nothing shipped."
    fi

    if already_filed "$work_dir" "$id"; then
        DEDUPED=$((DEDUPED + 1)); return 0
    fi

    if [ -n "$DRY_RUN" ]; then
        printf 'close-audit-sweep: WOULD FILE for %s (%s)\n' "$id" "$reason"
        FILED=$((FILED + 1)); return 0
    fi

    if ( cd "$work_dir" 2>/dev/null && gc bd create "$title" \
            --type bug -p 1 \
            -d "$desc" \
            --metadata "$(jq -nc --arg src "$id" --arg reason "$reason" --arg ts "$ts" \
                '{"gc.close_audit_source":$src,"gc.close_audit_reason":$reason,"gc.close_audit_spooled_at":$ts}')" \
            >/dev/null 2>&1 ); then
        FILED=$((FILED + 1))
        printf 'close-audit-sweep: filed for %s (%s)\n' "$id" "$reason"
    else
        UNKNOWN=$((UNKNOWN + 1))
    fi
}

# ── Collect spools, then loop in this shell so the counters survive ─────────
SPOOLS=$(mktemp) || exit 0
trap 'rm -f "$SPOOLS" "$RECORDS" 2>/dev/null' EXIT
RECORDS=$(mktemp) || exit 0

find "$ROOT" -maxdepth "$MAXDEPTH" -type d -name .git -prune -o \
     -type f -path '*/.beads/audit/close-spool.jsonl' -print > "$SPOOLS" 2>/dev/null

while IFS= read -r spool; do
    [ -n "$spool" ] || continue
    # fromjson? drops malformed lines silently: a torn write must not stop the
    # sweep over every other record.
    #
    # Fields are joined on US (\037), NOT tab: tab is IFS *whitespace*, so bash
    # collapses a run of them into one delimiter and an empty field (the common
    # `missing-commit` record has one) silently shifts every later column.
    jq -R -r 'fromjson? | [.id//"", .commit//"", .branch//"", .work_dir//"", .reason//"", .ts//""] | join("\u001f")' \
        "$spool" 2>/dev/null >> "$RECORDS"
done < "$SPOOLS"

while IFS=$'\037' read -r r_id r_commit r_branch r_work_dir r_reason r_ts; do
    sweep_record "$r_id" "$r_commit" "$r_branch" "$r_work_dir" "$r_reason" "$r_ts"
done < "$RECORDS"

printf 'close-audit-sweep: filed=%d resolved=%d already-filed=%d too-fresh=%d unprobeable=%d\n' \
    "$FILED" "$RESOLVED" "$DEDUPED" "$TOOSOON" "$UNKNOWN"

exit 0
