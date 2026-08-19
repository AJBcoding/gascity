package herdr

import (
	"errors"
	"fmt"
	"testing"
)

// wrapTaken builds an error shaped exactly like client.run's output for a
// herdr agent_name_taken rejection: the typed *herdrError wrapped with %w
// under an outer context string.
func wrapTaken() error {
	return fmt.Errorf("herdr [agent start x]: %w", &herdrError{
		Code:    "agent_name_taken",
		Message: `agent name x is already used; candidates: pane_id=w3F:pW status=Unknown`,
	})
}

func TestHerdrErrorCodeExtractsWrappedCode(t *testing.T) {
	if got := herdrErrorCode(wrapTaken()); got != "agent_name_taken" {
		t.Errorf("herdrErrorCode = %q; want agent_name_taken", got)
	}
	if got := herdrErrorCode(errors.New("plain transport failure")); got != "" {
		t.Errorf("herdrErrorCode(plain) = %q; want empty", got)
	}
	if got := herdrErrorCode(nil); got != "" {
		t.Errorf("herdrErrorCode(nil) = %q; want empty", got)
	}
}

// A successful start passes straight through, untouched and unadopted.
func TestResolveAgentNameTakenSuccessPassesThrough(t *testing.T) {
	want := agentInfo{Name: "x", PaneID: "w1:pA"}
	got, adopted, err := resolveAgentNameTaken(want, nil, agentStartOps{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != want {
		t.Errorf("got %+v; want %+v", got, want)
	}
	if adopted {
		t.Error("adopted=true for a fresh successful start; want false")
	}
}

// A non-taken error is surfaced verbatim — recovery must not swallow real
// failures (e.g. openpty tab_create_failed, transport errors).
func TestResolveAgentNameTakenNonTakenErrorSurfaces(t *testing.T) {
	boom := errors.New("herdr [agent start x]: some_other_failure: nope")
	called := false
	_, adopted, err := resolveAgentNameTaken(agentInfo{}, boom, agentStartOps{
		getAgent: func() (agentInfo, bool, error) { called = true; return agentInfo{}, false, nil },
	})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v; want the original non-taken error", err)
	}
	if adopted {
		t.Error("adopted=true on a non-taken error; want false")
	}
	if called {
		t.Error("getAgent was called for a non-taken error; recovery must not engage")
	}
}

// The holder disposition — all three verdicts the pane probe can return, in
// one place, because the difference between them is the whole safety argument.
//
// The reap is destructive: closePane kills whatever turn the holder's pane is
// running. So it is owed a determinate "this holder is dead", and a probe that
// FAILED has not delivered one. The closure feeding paneAlive used to collapse
// a failed probe into "not alive", which reaped on a transport blip; that was
// unreachable dead code until the stderr-path error typing (gas-ao8z) made
// agent_name_taken resolvable at all, so it landed with this fix or not at all.
//
// Asserting the returned error cannot catch that regression — the original
// agent_name_taken comes back whether or not the pane was closed on the way.
// The absence of the closePane and retryStart calls is the real assertion.
func TestResolveAgentNameTakenHolderDisposition(t *testing.T) {
	existing := agentInfo{Name: "x", PaneID: "w3F:pW", TabID: "w3F:tE"}
	fresh := agentInfo{Name: "x", PaneID: "w3F:pNEW"}
	probeFailed := errors.New("herdr [pane process-info --pane w3F:pW]: exit status 1: socket hiccup")

	tests := []struct {
		name        string
		alive       bool
		probeErr    error
		wantInfo    agentInfo
		wantAdopted bool
		wantOrigErr bool   // the original agent_name_taken is surfaced
		wantReaped  string // pane passed to closePane; "" means it must NOT be called
		wantRetries int
	}{{
		// Live holder → adopt it: no new pane, no retry, and adopted=true so
		// Start skips re-priming an agent that is already working. The
		// storm-breaker.
		name: "live holder is adopted", alive: true,
		wantInfo: existing, wantAdopted: true,
	}, {
		// Confirmed-dead holder → reap the stale pane, then start once more.
		// Bounded: exactly one retry, never a loop. A retried start is a fresh
		// agent, not an adoption, so adopted=false and Start still primes it.
		name: "confirmed-dead holder is reaped and retried once", alive: false,
		wantInfo: fresh, wantReaped: existing.PaneID, wantRetries: 1,
	}, {
		// Probe failed → the holder was never condemned. Surface the original
		// error and touch nothing: a transport blip must not cost a live agent
		// its pane and its in-flight turn.
		name: "uninspectable holder is surfaced, never reaped", alive: false, probeErr: probeFailed,
		wantOrigErr: true,
	}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := wrapTaken()
			var reaped string
			retries := 0
			got, adopted, err := resolveAgentNameTaken(agentInfo{}, orig, agentStartOps{
				getAgent:   func() (agentInfo, bool, error) { return existing, true, nil },
				paneAlive:  func(string) (bool, error) { return tt.alive, tt.probeErr },
				closePane:  func(paneID string) error { reaped = paneID; return nil },
				retryStart: func() (agentInfo, error) { retries++; return fresh, nil },
			})
			switch {
			case tt.wantOrigErr && !errors.Is(err, orig):
				t.Errorf("err = %v; want the original agent_name_taken surfaced", err)
			case !tt.wantOrigErr && err != nil:
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.wantInfo {
				t.Errorf("info = %+v; want %+v", got, tt.wantInfo)
			}
			if adopted != tt.wantAdopted {
				t.Errorf("adopted = %v; want %v", adopted, tt.wantAdopted)
			}
			// The two destructive calls. Proving they did NOT happen is the
			// point of the uninspectable case.
			if reaped != tt.wantReaped {
				t.Errorf("closePane called with %q; want %q (empty = must not be called at all)", reaped, tt.wantReaped)
			}
			if retries != tt.wantRetries {
				t.Errorf("retryStart called %d times; want %d", retries, tt.wantRetries)
			}
		})
	}
}

// agent_name_taken but the holder can't be inspected (getAgent errors or
// reports absent) → surface the original error rather than guessing.
func TestResolveAgentNameTakenUninspectableHolderSurfacesOriginal(t *testing.T) {
	orig := wrapTaken()
	_, adopted, err := resolveAgentNameTaken(agentInfo{}, orig, agentStartOps{
		getAgent: func() (agentInfo, bool, error) { return agentInfo{}, false, nil },
	})
	if !errors.Is(err, orig) {
		t.Errorf("err = %v; want the original agent_name_taken error when the holder is uninspectable", err)
	}
	if adopted {
		t.Error("adopted=true when the holder is uninspectable; want false")
	}
}
