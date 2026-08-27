package herdr

import (
	"context"
	"strconv"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// Optional capability interfaces herdr supports natively. (Relaunch,
// ProcessTableScanner and InterruptBoundaryWait are deliberately omitted from
// the first cut — the reconciler degrades gracefully when a provider lacks
// them: Relaunch falls back to Stop+Start, the others to no-op/default
// behavior.) SessionEventProvider is implemented in events.go over the socket
// API's events.subscribe stream.
//
// DialogProvider was in that omitted list and MUST NOT go back into it.
// "Degrades gracefully to no-op" is true of Relaunch; for dialogs it means the
// modal stays up and the next keystroke answers it. tmux and exec have called
// runtime.AcceptStartupDialogs all along, so when a city moved to herdr every
// agent silently lost startup-dialog handling — for months, with no signal,
// because an omitted capability looks identical to a capability that had
// nothing to do (gas-vs0e).
var (
	_ runtime.IdleWaitProvider       = (*Provider)(nil)
	_ runtime.ImmediateNudgeProvider = (*Provider)(nil)
	// DialogProvider lets the reconciler clear a modal on an already-running
	// session; Start clears them on the way up. See provider.go.
	_ runtime.DialogProvider = (*Provider)(nil)
	// LivenessObserver lets the reconciler read aliveness from herdr's own
	// agent-status instead of the host process-table walk (see
	// provider.go ObserveLiveness).
	_ runtime.LivenessObserver = (*Provider)(nil)
	// SessionEventProvider is implemented in events.go over the socket API's
	// events.subscribe stream (see #4217 herdr-first-class).
	_ runtime.SessionEventProvider = (*Provider)(nil)
)

// dismissDialogsOnPane clears known startup dialogs on paneID, best-effort.
//
// It hands runtime.AcceptStartupDialogs herdr's own two primitives — `pane
// read --source visible` to see the screen and `pane send-keys` to answer —
// which is the same contract tmux satisfies with its own pair. The dialog
// catalog (workspace trust, bypass-permissions, MCP trust, hook review,
// theme picker, rate limit) is therefore shared rather than reimplemented
// here: a renderer change is one fix in dialog.go for every provider.
//
// Best-effort by design. A session with no dialog is unaffected
// (AcceptStartupDialogs is documented idempotent), and a transport failure
// must not fail a Start whose session is otherwise live.
func (p *Provider) dismissDialogsOnPane(ctx context.Context, paneID string, timeout time.Duration) {
	_ = runtime.AcceptStartupDialogsWithTimeout(ctx, timeout,
		func(lines int) (string, error) { return p.c.paneRead(ctx, paneID, "visible", lines) },
		func(keys ...string) error { return p.c.sendKeys(ctx, paneID, keys...) },
	)
}

// DismissKnownDialogs implements runtime.DialogProvider for an ALREADY-RUNNING
// session, which is the reconciler's entry point — Start clears dialogs on the
// way up, but a modal can appear later (a rate-limit prompt mid-session, an MCP
// server added to the project) and nothing else would clear it.
//
// Resolves through the pane binding rather than the registry name so it still
// works for a session whose herdr name was cleared, matching every other
// pane-addressed verb here.
func (p *Provider) DismissKnownDialogs(ctx context.Context, name string, timeout time.Duration) error {
	pane, err := p.paneID(ctx, name)
	if err != nil {
		return err
	}
	if pane == "" {
		return runtime.ErrSessionNotFound
	}
	p.dismissDialogsOnPane(ctx, pane, timeout)
	return nil
}

// idleWaitOutcome is the legible verdict of one `agent wait --until idle`
// probe. The interface-facing WaitForIdle keeps its proceed-either-way
// contract, but internal callers (Start's startup delivery) need to see WHY a
// wait did not confirm: the live city measured 0 ok / 8 timeout / 2 error
// over 20h with every verdict silently discarded (gas-90h).
type idleWaitOutcome string

const (
	idleWaitReached idleWaitOutcome = "idle"     // herdr observed the agent idle
	idleWaitTimeout idleWaitOutcome = "timeout"  // bound elapsed without idle
	idleWaitNoAgent idleWaitOutcome = "no_agent" // no registered agent (raw shell pane)
	idleWaitError   idleWaitOutcome = "error"    // transport or unexpected herdr error
)

// waitForIdleOutcome blocks until herdr reports the agent idle or the timeout
// elapses, via herdr's native `agent wait --until idle` (the ≥0.7.5 flag
// spelling) — vs the pane-polling tmux does — and returns the verdict
// distinctly instead of discarding it.
func (p *Provider) waitForIdleOutcome(ctx context.Context, name string, timeout time.Duration) idleWaitOutcome {
	ms := int(timeout / time.Millisecond)
	if ms < 1 {
		ms = 1
	}
	_, err := p.c.run(ctx, "agent", "wait", herdrAgentName(name), "--until", "idle", "--timeout", strconv.Itoa(ms))
	switch {
	case err == nil:
		return idleWaitReached
	case herdrErrorCode(err) == "timeout":
		return idleWaitTimeout
	case isAgentNotFound(err):
		return idleWaitNoAgent
	default:
		return idleWaitError
	}
}

// WaitForIdle blocks until herdr reports the agent idle or the timeout
// elapses. Either outcome (idle reached or timed out) means the caller may
// proceed — as does an unregistered session (raw shell panes have no agent to
// wait on) — so only context cancellation surfaces as an error; the timeout
// is a hard bound.
func (p *Provider) WaitForIdle(ctx context.Context, name string, timeout time.Duration) error {
	_ = p.waitForIdleOutcome(ctx, name, timeout)
	return ctx.Err()
}

// NudgeNow injects input immediately. herdr's send/run already deliver without a
// wait-idle heuristic, so this is the same delivery path as Nudge.
func (p *Provider) NudgeNow(name string, content []runtime.ContentBlock) error {
	return p.Nudge(name, content)
}
