#!/usr/bin/env bash
set -euo pipefail

version="${1:-}"
repo_root="${2:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
removal_version="v1.6.0"

if [[ ! "$version" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
	echo "check-shipped-close-graduation: invalid release version '$version' (want vX.Y.Z)" >&2
	exit 2
fi

major="${BASH_REMATCH[1]}"
minor="${BASH_REMATCH[2]}"
if (( major < 1 || (major == 1 && minor < 6) )); then
	echo "check-shipped-close-graduation: OK ($version precedes $removal_version)"
	exit 0
fi

declare -a stale=()
check_artifact() {
	local label="$1" path="$2" pattern="$3"
	if [[ -f "$repo_root/$path" ]] && grep -qF "$pattern" "$repo_root/$path"; then
		stale+=("$label:$path")
	fi
}

check_artifact config internal/config/config.go shipped_close_warn_only
check_artifact consumer cmd/gc/work_record_gate.go ShippedCloseWarnOnly
check_artifact consumer internal/api/work_close_policy.go ShippedCloseWarnOnly
check_artifact notice internal/rollout/flag_beads_shipped_close_warn_only.go NoticeCompatibilityModeActive
check_artifact event internal/events/events.go work.close.warn_only.used
check_artifact event internal/events/work_close_payloads.go WorkCloseWarnOnlyUsedPayload
check_artifact docs docs/reference/config.md shipped_close_warn_only

if (( ${#stale[@]} != 0 )); then
	echo "ERROR: $version reached shipped-close warn-only removal boundary $removal_version with migration artifacts still present:" >&2
	printf '  - %s\n' "${stale[@]}" >&2
	echo "Remove the config, consumer, notice, event, and generated docs together before releasing." >&2
	exit 1
fi

echo "check-shipped-close-graduation: OK ($version is graduated)"
