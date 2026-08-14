package main

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// suspendedRigNamedTestCfg builds the gas-eere fixture: a rig suspended via
// suspended_on_start, a pack-shaped backing agent bound to it (Dir = rig
// name), and an always-mode named session over that agent.
func suspendedRigNamedTestCfg(t *testing.T, suspended bool) *config.City {
	t.Helper()
	return &config.City{
		Workspace:     config.Workspace{Name: "test-city"},
		Rigs:          []config.Rig{{Name: "kit", Path: t.TempDir(), SuspendedOnStart: suspended}},
		Agents:        []config.Agent{{Name: "witness", Dir: "kit", StartCommand: "true"}},
		NamedSessions: []config.NamedSession{{Template: "witness", Dir: "kit", Mode: "always"}},
	}
}

// TestReconcileSessionBeads_NamedSessionOnSuspendedRigDrains covers gas-eere:
// rig suspension must gate pack-agent (named-session) runtimes. The
// desired-state build already excludes a suspended rig's named sessions, but
// the reconciler's preserve path (preserveConfiguredNamedSessionBeadInfo) used
// to re-adopt the live session every tick purely because its configured spec
// exists, so the "suspended" drain below it was unreachable and the session
// ran forever on a suspended rig. A live named session whose backing agent is
// effectively suspended must flow to the suspend-drain instead — with the
// #3630 confirmation window intact.
func TestReconcileSessionBeads_NamedSessionOnSuspendedRigDrains(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = suspendedRigNamedTestCfg(t, true)
	sessionName := config.NamedSessionRuntimeName("test-city", env.cfg.Workspace, "kit/witness")
	_ = env.sp.Start(context.Background(), sessionName, runtime.Config{Command: "true"})
	session := env.createSessionBead(sessionName, "kit/witness")
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "kit/witness",
		namedSessionModeMetadata:     "always",
		"state":                      "active",
		"last_woke_at":               env.clk.Now().UTC().Format(time.RFC3339),
	})
	// desiredState deliberately stays empty: buildDesiredState gates
	// suspended-rig agents out, so the reconciler sees a live session with no
	// desired entry — exactly the shape the preserve leak kept alive.

	// Tick 1: suspend-class drains are confirm-buffered (#3630) — defer.
	env.reconcile([]beads.Bead{session})
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("tick 1 must defer inside the #3630 confirmation window, got drain reason=%q", ds.reason)
	}

	// Tick 2: confirmed — the drain must begin, classified "suspended".
	env.reconcile([]beads.Bead{session})
	ds := env.dt.get(session.ID)
	if ds == nil {
		t.Fatal("named session on a suspended rig must be suspend-drained; preserve must not keep it desired (gas-eere)")
	}
	if ds.reason != "suspended" {
		t.Fatalf("drain reason = %q, want %q", ds.reason, "suspended")
	}
}

// TestReconcileSessionBeads_NamedSessionOnActiveRigPreserved is the control
// for the gas-eere gate: the same fixture with the rig NOT suspended must keep
// the existing preserve behavior — the live named session stays running and is
// never suspend-drained, even though it has no desired-state entry this tick.
func TestReconcileSessionBeads_NamedSessionOnActiveRigPreserved(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = suspendedRigNamedTestCfg(t, false)
	sessionName := config.NamedSessionRuntimeName("test-city", env.cfg.Workspace, "kit/witness")
	_ = env.sp.Start(context.Background(), sessionName, runtime.Config{Command: "true"})
	session := env.createSessionBead(sessionName, "kit/witness")
	env.setSessionMetadata(&session, map[string]string{
		namedSessionMetadataKey:      "true",
		namedSessionIdentityMetadata: "kit/witness",
		namedSessionModeMetadata:     "always",
		"state":                      "active",
		"last_woke_at":               env.clk.Now().UTC().Format(time.RFC3339),
	})

	// Two ticks — enough to clear the #3630 window if the gate misfired.
	env.reconcile([]beads.Bead{session})
	env.reconcile([]beads.Bead{session})
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("named session on an unsuspended rig must be preserved, got drain reason=%q", ds.reason)
	}
	if !env.sp.IsRunning(sessionName) {
		t.Fatalf("named session %q must stay running when its rig is not suspended", sessionName)
	}
}
