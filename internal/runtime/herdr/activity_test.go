package herdr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── GetLastActivity against a fake herdr ─────────────────────────────────────
//
// herdr exposes no wall-clock activity timestamp — only agent_status and the
// globally-monotonic state_change_seq stamped at the agent's last status
// transition (gas-s04b measured a working agent's seq frozen across 12s of
// active output). GetLastActivity therefore derives activity by observation:
// a working agent is active NOW; an at-rest agent's activity is the first
// time this rest epoch (identified by seq) was observed, persisted in the
// sidecar so cross-process readers age it consistently.
//
// The fake herdr script serves `agent get` from two state files (status, seq)
// so tests can walk an agent through transitions without a live herdr.

var activitySession int64

// newFakeActivityProvider builds a Provider whose client shells out to a fake
// herdr serving `agent get` from state files. Returns the provider, a session
// name, and the state dir. Write "status" and "seq" files to register the
// agent; remove "status" to make it absent; create "transport_error" to make
// the fake fail.
func newFakeActivityProvider(t *testing.T) (*Provider, string, string) {
	t.Helper()
	session := fmt.Sprintf("gctest-act-%d-%d", os.Getpid(), atomic.AddInt64(&activitySession, 1))
	state := t.TempDir()
	script := filepath.Join(t.TempDir(), "herdr")
	fake := `#!/bin/sh
STATE='` + state + `'
shift 2
printf '%s\n' "$*" >> "$STATE/calls.log"
case "$1_$2" in
agent_get)
  if [ -e "$STATE/transport_error" ]; then
    echo "herdr: transport boom" >&2
    exit 1
  fi
  if [ ! -e "$STATE/status" ]; then
    printf '%s' '{"error":{"code":"agent_not_found","message":"agent target not found"}}'
  else
    S=$(cat "$STATE/status"); Q=$(cat "$STATE/seq")
    printf '%s' '{"result":{"agent":{"name":"'"$3"'","pane_id":"%5","tab_id":"t1","workspace_id":"w1","agent_status":"'"$S"'","state_change_seq":'"$Q"'}}}'
  fi ;;
*)
  printf '%s' '{"result":{}}' ;;
esac
`
	if err := os.WriteFile(script, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	p := New(session, t.TempDir(), t.TempDir(), time.Second, time.Second)
	p.c.bin = script
	return p, session, state
}

func setAgentState(t *testing.T, state, status string, seq int64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "seq"), []byte(fmt.Sprintf("%d", seq)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A working agent is active by definition: the verdict is "now", regardless
// of how long ago its last status transition bumped the seq. This is the
// guard that keeps auto-suspend from stopping a session mid-turn (its seq
// freezes for the whole turn) and reads a hung-looping agent as active —
// hung-vs-productive is the witness's judgment, not Go's.
func TestGetLastActivityWorkingReportsNow(t *testing.T) {
	p, name, state := newFakeActivityProvider(t)
	setAgentState(t, state, "working", 41)

	before := time.Now().Add(-time.Second)
	got, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("GetLastActivity: %v", err)
	}
	if got.IsZero() {
		t.Fatal("working agent reported zero activity; want approximately now")
	}
	if got.Before(before) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("working agent activity = %v; want approximately now", got)
	}
}

// An at-rest agent's activity is the first observation of its current rest
// epoch: the stamp persists across calls while the seq is unchanged (so it
// ages), and a seq change — a status transition happened since — re-stamps.
func TestGetLastActivityAtRestStampsObservationAndAges(t *testing.T) {
	p, name, state := newFakeActivityProvider(t)
	setAgentState(t, state, "done", 7)

	first, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("first GetLastActivity: %v", err)
	}
	if first.IsZero() {
		t.Fatal("at-rest agent reported zero activity on first observation; want a stamp")
	}

	second, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("second GetLastActivity: %v", err)
	}
	if !second.Equal(first) {
		t.Fatalf("same rest epoch returned a moving stamp: first=%v second=%v; want the persisted stamp so idleness ages", first, second)
	}

	setAgentState(t, state, "idle", 9) // a transition happened since the stamp
	third, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("third GetLastActivity: %v", err)
	}
	if !third.After(first) {
		t.Fatalf("new rest epoch did not advance the stamp: first=%v third=%v", first, third)
	}
}

// An absent agent (stopped, or a raw shell pane that never registered) falls
// back to the persisted stamp; with none, zero-and-nil means "unknown" per
// the Provider contract.
func TestGetLastActivityAbsentAgent(t *testing.T) {
	p, name, state := newFakeActivityProvider(t)

	got, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("GetLastActivity with no agent and no stamp: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("absent agent with no stamp = %v; want zero (unknown)", got)
	}

	setAgentState(t, state, "done", 3)
	stamp, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("stamping observation: %v", err)
	}
	if err := os.Remove(filepath.Join(state, "status")); err != nil {
		t.Fatal(err)
	}
	got, err = p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("GetLastActivity after agent vanished: %v", err)
	}
	if !got.Equal(stamp) {
		t.Fatalf("absent agent = %v; want the persisted stamp %v", got, stamp)
	}
}

// A transport failure is an error, not a silent zero: the enrichment guard
// (err == nil) must be able to tell "herdr unreachable" from "no activity".
func TestGetLastActivityTransportErrorSurfaces(t *testing.T) {
	p, name, state := newFakeActivityProvider(t)
	if err := os.WriteFile(filepath.Join(state, "transport_error"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetLastActivity(name); err == nil {
		t.Fatal("transport failure returned nil error; want the failure surfaced")
	}
}

// A corrupt stamp self-heals: the next at-rest observation re-seeds it
// rather than erroring forever (an error here would leave the session
// permanently invisible to activity readers — the gas-s04b wedge, again).
func TestGetLastActivityCorruptStampSelfHeals(t *testing.T) {
	p, name, state := newFakeActivityProvider(t)
	setAgentState(t, state, "done", 5)
	if _, err := p.GetLastActivity(name); err != nil {
		t.Fatalf("seeding stamp: %v", err)
	}
	stampPath := filepath.Join(p.metaDir, sanitize(name), sanitize(lastActivityMetaKey))
	if err := os.WriteFile(stampPath, []byte("not a timestamp"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("GetLastActivity over corrupt stamp: %v", err)
	}
	if got.IsZero() {
		t.Fatal("corrupt stamp returned zero; want a re-seeded stamp")
	}
	raw, err := os.ReadFile(stampPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw))); perr != nil {
		t.Fatalf("stamp file not re-seeded after corruption: %q", raw)
	}
}

// The capability flag must match reality: GetLastActivity now returns
// meaningful results.
func TestCapabilitiesReportActivity(t *testing.T) {
	p, _, _ := newFakeActivityProvider(t)
	if !p.Capabilities().CanReportActivity {
		t.Fatal("CanReportActivity = false; herdr derives activity by observation now")
	}
}

// The observation-folding verdict, exhaustively: which statuses read as
// active-now, which age against the persisted baseline, and when the
// baseline is (re)written.
func TestActivityFromObservation(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	prev := now.Add(-42 * time.Minute)

	cases := []struct {
		name        string
		status      string
		seq         int64
		prevStamp   time.Time
		prevSeq     string
		wantStamp   time.Time
		wantPersist bool
	}{
		{"working is now, never persisted", "working", 7, prev, "7", now, false},
		{"unknown fails safe toward active", "unknown", 7, prev, "7", now, false},
		{"empty status fails safe toward active", "", 7, prev, "7", now, false},
		{"future status herdr adds fails safe toward active", "pondering", 7, prev, "7", now, false},
		{"idle first observation stamps", "idle", 7, time.Time{}, "", now, true},
		{"done same epoch returns baseline", "done", 7, prev, "7", prev, false},
		{"DONE with whitespace still matches", "  Done ", 7, prev, "7", prev, false},
		{"blocked ages so stuck-detection sees it", "blocked", 7, prev, "7", prev, false},
		{"terminal status ages", "exited", 7, prev, "7", prev, false},
		{"seq moved: a transition happened, re-stamp", "done", 9, prev, "7", now, true},
		{"baseline lost but seq matches: re-seed", "done", 7, time.Time{}, "7", now, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stamp, persist := activityFromObservation(tc.status, tc.seq, tc.prevStamp, tc.prevSeq, now)
			if !stamp.Equal(tc.wantStamp) {
				t.Errorf("stamp = %v; want %v", stamp, tc.wantStamp)
			}
			if persist != tc.wantPersist {
				t.Errorf("persist = %v; want %v", persist, tc.wantPersist)
			}
		})
	}
}
