package herdr

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// ── startup-dialog handling for the herdr provider (gas-vs0e) ────────────────
//
// herdr omitted DialogProvider from its first cut and degraded to no-op, so an
// agent launched into a directory it has not seen sat on a workspace-trust
// modal while herdr reported agent_status idle AND interactive_ready true AND a
// quiescent state_change_seq. The next keystroke answered the modal: measured
// on BOTH TUIs with production argv, the startup turn selected "1. Yes, I trust
// this folder", was consumed, and `agent prompt --wait` reported CONFIRMED
// because dismissing the dialog is itself a state change.
//
// Every polecat gets a fresh worktree, which is by definition such a directory.
// --dangerously-skip-permissions does NOT suppress the dialog; that was
// re-measured with the real argv precisely because "we launch with permissions
// bypassed" is the obvious objection.

// Start must clear the modal, and must do it BEFORE delivering anything.
// Ordering is the whole point: dismissing after delivery is what we already had
// — the payload answers the dialog and then there is nothing left to dismiss.
// Both TUIs, because both were reproduced. Their wordings share no phrase, so a
// pass on one is no evidence about the other -- which is exactly the assumption
// that cost an approved diff earlier in this bead's history.
func TestStartDismissesWorkspaceTrustDialogBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag string
	}{
		{"claude", "trust_dialog"},
		{"codex", "trust_dialog_codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, state := newFakeHerdrProvider(t)
			listenHerdrSocket(t, p)
			setState(t, state, tc.flag)

			cfg := runtime.Config{
				Command:      tc.name,
				Nudge:        "Run gc hook --claim --json now.",
				ProcessNames: []string{tc.name}, // what ShouldAcceptStartupDialogs infers from
			}
			if err := p.Start(context.Background(), "gastown__witness", cfg); err != nil {
				t.Fatalf("Start: %v", err)
			}

			calls := fakeCalls(t, state)
			enter := strings.Index(calls, "pane send-keys %5 Enter")
			if enter < 0 {
				t.Fatalf("Start never answered the workspace-trust modal:\n%s", calls)
			}
			prompt := strings.Index(calls, "agent prompt")
			if prompt >= 0 && enter > prompt {
				t.Fatalf("modal was answered AFTER the first delivery, so the payload hit the dialog:\n%s", calls)
			}
		})
	}
}

// The dialog handling must not fire on a session that has no dialog. A provider
// that sends a speculative Enter into every fresh pane would submit whatever is
// in the composer, which is the same class of harm from the other direction.
func TestStartSendsNoKeysWhenNoDialogIsPresent(t *testing.T) {
	p, state := newFakeHerdrProvider(t)
	listenHerdrSocket(t, p)
	// No trust_dialog flag: pane read returns an ordinary empty composer.

	cfg := runtime.Config{
		Command:      "claude",
		Nudge:        "Run gc hook --claim --json now.",
		ProcessNames: []string{"claude"},
	}
	if err := p.Start(context.Background(), "gastown__witness", cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if calls := fakeCalls(t, state); strings.Contains(calls, "send-keys") {
		t.Fatalf("keys were sent to a pane showing no dialog:\n%s", calls)
	}
}

// DismissKnownDialogs is the reconciler's entry point for an ALREADY-RUNNING
// session: Start clears dialogs on the way up, but a modal can appear later (a
// rate-limit prompt mid-session, an MCP server added to the project) and
// nothing else would clear it.
func TestDismissKnownDialogsClearsAModalOnALiveSession(t *testing.T) {
	p, state := newFakeHerdrProvider(t)
	listenHerdrSocket(t, p)

	if err := p.Start(context.Background(), "gastown__witness", runtime.Config{Command: "claude"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The modal appears only now, after the session is up.
	setState(t, state, "trust_dialog")

	if err := p.DismissKnownDialogs(context.Background(), "gastown__witness", 5*time.Second); err != nil {
		t.Fatalf("DismissKnownDialogs: %v", err)
	}
	if calls := fakeCalls(t, state); !strings.Contains(calls, "pane send-keys %5 Enter") {
		t.Fatalf("DismissKnownDialogs did not answer the modal:\n%s", calls)
	}
}
