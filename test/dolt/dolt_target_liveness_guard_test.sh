#!/bin/sh
# Regression test: maintenance scripts must SKIP (exit 0) on a city with no live
# Dolt server, and must not be fooled by leftover Dolt-shaped artifacts.
#
# Scripts under test:
#   internal/bootstrap/packs/core/assets/scripts/dolt-target.sh  (core: reaper, jsonl-export)
#   examples/bd/dolt/assets/scripts/runtime.sh                   (bd/dolt: mol-dog-*, gc dolt compact)
#
# Background: a city that migrated off Dolt (e.g. to the MySQL metadata backend)
# keeps Dolt-shaped debris — an empty .beads/dolt/ tree, a stale
# dolt-provider-state.json. core_city_has_dolt_target() treated those artifacts
# as evidence of a live target, so its clean "skip" was unreachable; execution
# fell through to the real port resolver, which correctly found no server and
# exited 78 — producing exactly the recurring OrderFailed the guard exists to
# prevent. Artifacts outlive the server they evidence, so an artifact test fails
# OPEN on precisely the cities the guard is meant to protect.
set -u

HERE=$(unset CDPATH; cd -- "$(dirname "$0")" && pwd)
REPO=$(unset CDPATH; cd -- "$HERE/../.." && pwd)
CORE_SCRIPTS="$REPO/internal/bootstrap/packs/core/assets/scripts"
DOLT_SCRIPTS="$REPO/examples/bd/dolt/assets/scripts"
DOLT_TARGET="$CORE_SCRIPTS/dolt-target.sh"
DOLT_RUNTIME="$DOLT_SCRIPTS/runtime.sh"
[ -f "$DOLT_TARGET" ] || { echo "FAIL: not found: $DOLT_TARGET"; exit 1; }
[ -f "$DOLT_RUNTIME" ] || { echo "FAIL: not found: $DOLT_RUNTIME"; exit 1; }

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
no() { fail=$((fail + 1)); printf 'FAIL - %s\n' "$1"; }

TMPROOT=$(mktemp -d) || exit 1
trap 'rm -rf "$TMPROOT"' EXIT

# make_city NAME [husk] — a city with no live Dolt server. "husk" adds the
# leftover artifacts a post-migration city carries.
make_city() {
  _city="$TMPROOT/$1"
  mkdir -p "$_city/.gc/runtime/packs/dolt"
  if [ "${2:-}" = "husk" ]; then
    # Exactly the debris measured on a migrated city: an EMPTY .beads/dolt/.dolt
    # tree and a provider-state file naming a server that is long gone.
    mkdir -p "$_city/.beads/dolt/.dolt"
    printf '{"running": true, "pid": 999999, "port": 65535, "data_dir": "%s"}\n' \
      "$_city/.beads/dolt" > "$_city/.gc/runtime/packs/dolt/dolt-provider-state.json"
  fi
  printf '%s' "$_city"
}

# A hermetic environment. `env -i` matters: an agent/CI shell exports GC_CITY,
# GC_CITY_RUNTIME_DIR and friends, and GC_CITY_RUNTIME_DIR alone is enough to
# point these scripts at the REAL city's state files and silently invalidate
# every assertion below.
clean_env() { env -i PATH="$PATH" HOME="$TMPROOT" "$@"; }

# run_dolt_target CITY — source dolt-target.sh as a maintenance script does.
# SCRIPT_DIR mirrors how reaper.sh/jsonl-export.sh source it, and lets the
# in-repo port_resolve.sh fallback resolve.
run_dolt_target() {
  clean_env GC_CITY_PATH="$1" SCRIPT_DIR="$CORE_SCRIPTS" \
    sh -c '. "$0"' "$DOLT_TARGET" 2>&1
}

# --- 1. THE BUG: husk artifacts must not be mistaken for a live target -------
city=$(make_city husk husk)
out=$(run_dolt_target "$city"); rc=$?
if [ "$rc" -eq 0 ]; then
  ok "husk city: exits 0 (skip), not 78"
else
  no "husk city: exits 0 (skip), not 78 — got exit $rc: $out"
fi
case "$out" in
  *"no managed dolt target"*) ok "husk city: prints the skip reason" ;;
  *) no "husk city: prints the skip reason — got: $out" ;;
esac

# --- 1b. THE LINE NOT TO CROSS: a city that IS configured for Dolt but whose
#         managed state is corrupt must still fail LOUDLY, not skip -----------
# "No Dolt target" and "Dolt target that is broken right now" are different
# conditions. Only the first may skip. If a corrupt dolt-state.json skipped,
# it would silently disable maintenance on a real Dolt city forever.
city=$(make_city corrupt)
printf 'not-json\n' > "$city/.gc/runtime/packs/dolt/dolt-state.json"
out=$(run_dolt_target "$city"); rc=$?
if [ "$rc" -eq 78 ]; then
  ok "corrupt managed state: still exits 78 (does not silently skip)"
else
  no "corrupt managed state: still exits 78 — got exit $rc: $out"
fi

# --- 2. Regression: a city with no Dolt evidence at all still skips ----------
city=$(make_city bare)
out=$(run_dolt_target "$city"); rc=$?
if [ "$rc" -eq 0 ]; then
  ok "bare city: exits 0 (skip)"
else
  no "bare city: exits 0 (skip) — got exit $rc: $out"
fi

# --- 3. Regression: an explicit GC_DOLT_PORT is still honored as an operator
#        seed, and must NOT be skipped ---------------------------------------
city=$(make_city seeded)
out=$(clean_env GC_DOLT_PORT=13306 \
  GC_CITY_PATH="$city" SCRIPT_DIR="$CORE_SCRIPTS" \
  sh -c '. "$0"' "$DOLT_TARGET" 2>&1)
case "$out" in
  *"no managed dolt target"*) no "GC_DOLT_PORT seed: must not skip — got: $out" ;;
  *) ok "GC_DOLT_PORT seed: not skipped" ;;
esac

# --- 4. compact/run.sh's chain: runtime.sh must be able to defer the port
#        decision to a caller that resolves it itself ------------------------
# `gc dolt compact` re-resolves the port for every branch of its own guard and
# skips cleanly when there is no live runtime — but it only reaches that guard
# if sourcing runtime.sh does not exit 78 first.
city=$(make_city optin husk)
out=$(clean_env GC_CITY_PATH="$city" GC_PACK_DIR="$REPO/examples/bd/dolt" \
  GC_DOLT_PORT_OPTIONAL=1 \
  sh -c '. "$0"; echo "REACHED:[${GC_DOLT_PORT:-}]"' "$DOLT_RUNTIME" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then
  ok "runtime.sh: GC_DOLT_PORT_OPTIONAL=1 does not exit 78"
else
  no "runtime.sh: GC_DOLT_PORT_OPTIONAL=1 does not exit 78 — got exit $rc: $out"
fi
case "$out" in
  *"REACHED:[]"*) ok "runtime.sh: caller reached with an empty port to handle" ;;
  *) no "runtime.sh: caller reached with an empty port to handle — got: $out" ;;
esac

# --- 5. Regression: WITHOUT the opt-in, runtime.sh still fails hard, so
#        interactive commands (gc dolt sql) keep their loud error -------------
city=$(make_city strict husk)
out=$(clean_env GC_CITY_PATH="$city" GC_PACK_DIR="$REPO/examples/bd/dolt" \
  sh -c '. "$0"; echo REACHED' "$DOLT_RUNTIME" 2>&1); rc=$?
if [ "$rc" -eq 78 ]; then
  ok "runtime.sh: without opt-in, still exits 78 (interactive callers unchanged)"
else
  no "runtime.sh: without opt-in, still exits 78 — got exit $rc: $out"
fi

# --- 6. THE REGRESSION GUARD THAT MATTERS: a city with a genuinely LIVE
#        runtime must still be treated as having a target ---------------------
# The guard now decides on liveness, so this is what proves the change did not
# silently turn every real Dolt city into a no-op. Uses an actual listening
# socket and a real live PID rather than a stub, because managed_runtime_port
# cross-checks the state file against the process and the socket.
if command -v python3 >/dev/null 2>&1; then
  city=$(make_city live)
  mkdir -p "$city/.beads/dolt"
  portfile="$TMPROOT/live.port"
  python3 -c '
import socket, sys, time
s = socket.socket(); s.bind(("127.0.0.1", 0)); s.listen(1)
open(sys.argv[1], "w").write(str(s.getsockname()[1]))
time.sleep(30)
' "$portfile" &
  listener_pid=$!
  i=0
  while [ ! -s "$portfile" ] && [ "$i" -lt 50 ]; do i=$((i + 1)); sleep 0.1; done
  if [ -s "$portfile" ]; then
    printf '{"running": true, "pid": %s, "port": %s, "data_dir": "%s"}\n' \
      "$listener_pid" "$(cat "$portfile")" "$city/.beads/dolt" \
      > "$city/.gc/runtime/packs/dolt/dolt-state.json"
    out=$(run_dolt_target "$city")
    case "$out" in
      *"no managed dolt target"*) no "live runtime: must NOT skip — got: $out" ;;
      *) ok "live runtime: correctly detected as a target (not skipped)" ;;
    esac
  else
    no "live runtime: could not start test listener"
  fi
  kill "$listener_pid" 2>/dev/null
  wait "$listener_pid" 2>/dev/null
else
  printf 'skip - live runtime case (python3 unavailable)\n'
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
