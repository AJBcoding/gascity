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
			// The verdict is WHICH OPTION the fake recorded as confirmed, not
			// which keys arrived. Claude's current layout puts "No, exit"
			// first and pre-selected, so a position-based dismissal confirms
			// the decline and kills the agent while still sending an Enter
			// that a key-only assertion would happily accept (gas-193q).
			if hasState(t, state, "trust_declined") {
				t.Fatalf("%s: the dismissal confirmed the DECLINE option, which exits the agent:\n%s", tc.name, calls)
			}
			if !hasState(t, state, "trust_answered") {
				t.Fatalf("%s: Start never confirmed the trust option:\n%s", tc.name, calls)
			}
			enter := strings.LastIndex(calls, "pane send-keys %5 Enter")
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

// TestStartDismissesATrustModalRaisedAFTERTheReadyPrompt pins the second
// dialog pass.
//
// Codex draws its input prompt and raises its trust modal a beat later,
// measured live 2026-09-04: prompt at t+0.0s, modal at t+1.5s. The startup
// dialog sequence reads a visible prompt as "nothing left to dismiss" and
// returns, so on that ordering the first pass finishes before the modal
// exists. tmux has re-run acceptStartupDialogs after readiness since it was
// written; herdr ran only the first pass, and the result was an intermittent
// codex spawn that sat on an unanswered modal forever.
//
// The modal must be cleared, and cleared BEFORE the first turn is delivered —
// otherwise the payload answers it, which is the gas-vs0e defect returning by
// a different route.
func TestStartDismissesATrustModalRaisedAFTERTheReadyPrompt(t *testing.T) {
	p, state := newFakeHerdrProvider(t)
	listenHerdrSocket(t, p)
	setState(t, state, "trust_dialog_late")

	cfg := runtime.Config{
		Command:      "codex",
		Nudge:        "Run gc hook --claim --json now.",
		ProcessNames: []string{"codex"},
	}
	if err := p.Start(context.Background(), "gastown__witness", cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}

	calls := fakeCalls(t, state)
	if hasState(t, state, "trust_declined") {
		t.Fatalf("the late modal was answered with the DECLINE option:\n%s", calls)
	}
	if !hasState(t, state, "trust_answered") {
		t.Fatalf("a trust modal raised after the ready prompt was never dismissed:\n%s", calls)
	}
	enter := strings.LastIndex(calls, "pane send-keys %5 Enter")
	prompt := strings.Index(calls, "agent prompt")
	if prompt >= 0 && enter > prompt {
		t.Fatalf("the late modal was answered AFTER delivery, so the payload hit the dialog:\n%s", calls)
	}
}
