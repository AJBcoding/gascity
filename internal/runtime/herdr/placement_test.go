package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlacementFor covers how a runtime session name + reconciler env map to a
// herdr workspace and tab. The wisp cases are the reason this exists: their
// town-qualified, wisp-id runtime name must be lifted into the originating rig
// workspace under a themed tab.
func TestPlacementFor(t *testing.T) {
	tests := []struct {
		name     string
		sessName string
		env      map[string]string
		wantWS   string
		wantTab  string
	}{
		{
			name:     "wisp gets rig workspace and themed tab",
			sessName: "gastown__polecat-gc-wisp-3nvj3yx",
			env:      map[string]string{"GC_RIG": "webapp", "GC_ALIAS": "webapp/gastown.furiosa"},
			wantWS:   "webapp",
			wantTab:  "polecat-furiosa",
		},
		{
			name:     "wisp on mobile with bare alias",
			sessName: "gastown__polecat-gc-wisp-abc123",
			env:      map[string]string{"GC_RIG": "mobile", "GC_ALIAS": "nux"},
			wantWS:   "mobile",
			wantTab:  "polecat-nux",
		},
		{
			name:     "wisp falls back to GC_AGENT when no alias",
			sessName: "gastown__polecat-gc-wisp-abc123",
			env:      map[string]string{"GC_RIG": "webapp", "GC_AGENT": "webapp/gastown.slit"},
			wantWS:   "webapp",
			wantTab:  "polecat-slit",
		},
		{
			name:     "wisp with no alias yet keeps wisp tab but still moves to rig",
			sessName: "gastown__polecat-gc-wisp-abc123",
			env:      map[string]string{"GC_RIG": "webapp"},
			wantWS:   "webapp",
			wantTab:  "polecat-gc-wisp-abc123",
		},
		{
			name:     "alias that is itself the wisp identity is ignored",
			sessName: "gastown__polecat-gc-wisp-abc123",
			env:      map[string]string{"GC_RIG": "webapp", "GC_AGENT": "gastown.polecat-gc-wisp-abc123"},
			wantWS:   "webapp",
			wantTab:  "polecat-gc-wisp-abc123",
		},
		{
			name:     "persistent rig agent unchanged",
			sessName: "webapp--gastown__witness",
			env:      map[string]string{"GC_RIG": "webapp", "GC_ALIAS": "webapp/gastown.witness"},
			wantWS:   "webapp",
			wantTab:  "witness",
		},
		{
			name:     "town-level agent has no rig and stays in town",
			sessName: "gastown__mayor",
			env:      map[string]string{"GC_ALIAS": "gastown.mayor"},
			wantWS:   "gastown",
			wantTab:  "mayor",
		},
		{
			name:     "no env falls back to structural name parsing",
			sessName: "webapp--gastown__refinery",
			env:      nil,
			wantWS:   "webapp",
			wantTab:  "refinery",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotWS, gotTab := placementFor(tt.sessName, tt.env)
			if gotWS != tt.wantWS || gotTab != tt.wantTab {
				t.Errorf("placementFor(%q, %v) = (%q, %q), want (%q, %q)",
					tt.sessName, tt.env, gotWS, gotTab, tt.wantWS, tt.wantTab)
			}
		})
	}
}

// newSingleTabFakeClient builds a client whose herdr script models the ≥0.8.0
// contract — closing a workspace's last tab closes the workspace itself
// (herdr #1760/#1899) — with stateful tabs so the scenario evolves as verbs
// land. Initial state: workspace w1 ("gastown") holding exactly one tab
// t-stale labeled "witness". Returns the client and the state dir.
func newSingleTabFakeClient(t *testing.T) (*client, string) {
	t.Helper()
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "tabs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "tabs", "t-stale"), []byte("witness"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ws_alive"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "herdr")
	fake := `#!/bin/sh
STATE='` + state + `'
shift 2
printf '%s\n' "$*" >> "$STATE/calls.log"
case "$1_$2" in
workspace_list)
  if [ -e "$STATE/ws_alive" ]; then
    printf '%s' '{"result":{"workspaces":[{"workspace_id":"w1","label":"gastown"}]}}'
  else
    printf '%s' '{"result":{"workspaces":[]}}'
  fi ;;
tab_list)
  tabs=''
  for f in "$STATE/tabs"/*; do
    [ -e "$f" ] || continue
    entry='{"tab_id":"'"$(basename "$f")"'","label":"'"$(cat "$f")"'"}'
    tabs="${tabs:+$tabs,}$entry"
  done
  printf '%s' '{"result":{"tabs":['"$tabs"']}}' ;;
tab_close)
  rm -f "$STATE/tabs/$3"
  if [ -z "$(ls -A "$STATE/tabs")" ]; then rm -f "$STATE/ws_alive"; fi
  printf '%s' '{"result":{"type":"tab_closed"}}' ;;
tab_create)
  if [ ! -e "$STATE/ws_alive" ]; then
    printf '%s' '{"error":{"code":"workspace_not_found","message":"workspace w1 not found"}}'
  else
    printf '%s' "$6" > "$STATE/tabs/t-new"
    printf '%s' '{"result":{"tab":{"tab_id":"t-new"},"root_pane":{"pane_id":"%9"}}}'
  fi ;;
*)
  exit 0 ;;
esac
`
	if err := os.WriteFile(script, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newClient("gctest-placement", "")
	c.bin = script
	return c, state
}

// The herdr 0.8.0 upgrade blocker: every workspace passes through the
// single-tab state as agents drain, and restarting that last agent must not
// destroy the workspace out from under its own placement. ensurePlacement has
// to create the replacement tab BEFORE closing the stale one — close-first
// drops the workspace to zero tabs, 0.8.0 closes the workspace with it, and
// the create that follows fails against a dead workspace id.
func TestEnsurePlacementSingleTabWorkspaceSurvives(t *testing.T) {
	c, state := newSingleTabFakeClient(t)

	tabID, paneID, err := c.ensurePlacement(context.Background(), "gastown", "witness", "", nil)
	if err != nil {
		t.Fatalf("ensurePlacement on a single-tab workspace: %v", err)
	}
	if tabID != "t-new" || paneID != "%9" {
		t.Fatalf("ensurePlacement = (%q, %q); want (\"t-new\", \"%%9\")", tabID, paneID)
	}
	if _, err := os.Stat(filepath.Join(state, "ws_alive")); err != nil {
		t.Fatal("workspace died during placement: the last tab was closed before its replacement existed")
	}
	calls, err := os.ReadFile(filepath.Join(state, "calls.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "tab close t-stale") {
		t.Errorf("stale tab was never recycled:\n%s", calls)
	}
	if strings.Contains(string(calls), "tab close t-new") {
		t.Errorf("closed the freshly created tab (stale-close not keyed on TabID):\n%s", calls)
	}
}
