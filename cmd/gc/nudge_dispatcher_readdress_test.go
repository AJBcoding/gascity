package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/session"
)

// Regression coverage for gas-wvap: a queued nudge is fenced to the
// (session_id, epoch) captured at enqueue, so when a pool session recycles,
// the item can never be claimed by the slot's live successor — it rots
// never-attempted until expiry while the successor idles beside a routed
// queue it was never poked to read. The dispatcher now re-addresses pending
// items whose fenced session is no longer open, and only those: a fence to a
// session that is still open stays intact so live siblings cannot steal each
// other's directed nudges.

func loadQueuedNudgeByID(t *testing.T, cityPath, id string) queuedNudge {
	t.Helper()
	state, err := nudgequeue.LoadState(cityPath)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	for _, it := range append(append([]queuedNudge{}, state.Pending...), state.InFlight...) {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("nudge %s not found in pending or in-flight", id)
	return queuedNudge{}
}

func TestReaddressOrphanedQueuedNudgesClearsDeadFenceOnly(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	dir := t.TempDir()
	now := time.Now().Add(-time.Minute)
	dead := newQueuedNudgeWithOptions("rig/pool", "check for assigned work", "session", now,
		queuedNudgeOptions{SessionID: "az-dead", ContinuationEpoch: "1"})
	live := newQueuedNudgeWithOptions("rig/pool", "for the live fence", "session", now,
		queuedNudgeOptions{SessionID: "az-live", ContinuationEpoch: "2"})
	plain := newQueuedNudge("rig/pool", "already agent-addressed", now)
	for _, item := range []queuedNudge{dead, live, plain} {
		if err := enqueueQueuedNudge(dir, item); err != nil {
			t.Fatalf("enqueueQueuedNudge: %v", err)
		}
	}

	n, err := readdressOrphanedQueuedNudges(dir, map[string]bool{"az-live": true})
	if err != nil {
		t.Fatalf("readdressOrphanedQueuedNudges: %v", err)
	}
	if n != 1 {
		t.Fatalf("readdressed = %d, want 1 (only the dead fence)", n)
	}

	got := loadQueuedNudgeByID(t, dir, dead.ID)
	if got.SessionID != "" || got.ContinuationEpoch != "" {
		t.Fatalf("dead fence not cleared: session_id=%q epoch=%q", got.SessionID, got.ContinuationEpoch)
	}
	if got.ReaddressedFrom != "az-dead" {
		t.Fatalf("ReaddressedFrom = %q, want az-dead (audit trail)", got.ReaddressedFrom)
	}
	if l := loadQueuedNudgeByID(t, dir, live.ID); l.SessionID != "az-live" || l.ReaddressedFrom != "" {
		t.Fatalf("live fence must stay intact: session_id=%q readdressed_from=%q", l.SessionID, l.ReaddressedFrom)
	}
	if p := loadQueuedNudgeByID(t, dir, plain.ID); p.ReaddressedFrom != "" {
		t.Fatalf("agent-addressed item must be untouched: readdressed_from=%q", p.ReaddressedFrom)
	}
}

func TestReaddressedOrphanIsClaimableByTheSlotSuccessor(t *testing.T) {
	successor := nudgeTarget{
		agent:             config.Agent{Name: "pool"},
		sessionID:         "az-new",
		continuationEpoch: "1",
	}
	item := newQueuedNudgeWithOptions(successor.agent.QualifiedName(), "check for assigned work", "session",
		time.Now(), queuedNudgeOptions{SessionID: "az-dead", ContinuationEpoch: "1"})
	if queuedNudgeClaimableForTarget(successor, item) {
		t.Fatal("fenced item must not be claimable by the successor before re-address")
	}
	item.ReaddressedFrom = item.SessionID
	item.SessionID, item.ContinuationEpoch = "", ""
	if !queuedNudgeClaimableForTarget(successor, item) {
		t.Fatal("re-addressed item must be claimable by the slot's live successor")
	}
}

func TestDispatchAllQueuedNudgesReaddressesOrphanedFence(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	dir := t.TempDir()
	item := newQueuedNudgeWithOptions("rig/pool", "check for assigned work", "session",
		time.Now().Add(-time.Minute), queuedNudgeOptions{SessionID: "az-dead", ContinuationEpoch: "1"})
	if err := enqueueQueuedNudge(dir, item); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	snap := newSessionBeadSnapshotFromInfos([]session.Info{{ID: "az-new", AgentName: "rig/pool"}})
	// Delivery itself is not under test (nil provider; target resolution may
	// skip) — the assertion is that the dispatcher's re-address pass persisted
	// before any delivery attempt.
	if _, err := dispatchAllQueuedNudges(dir, supervisorCfg(), nil, nil, nil, snap); err != nil {
		t.Logf("dispatchAllQueuedNudges (delivery not under test): %v", err)
	}

	got := loadQueuedNudgeByID(t, dir, item.ID)
	if got.SessionID != "" || got.ReaddressedFrom != "az-dead" {
		t.Fatalf("dispatcher did not re-address the orphaned fence: session_id=%q readdressed_from=%q",
			got.SessionID, got.ReaddressedFrom)
	}
}
