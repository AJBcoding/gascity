package herdr

import (
	"errors"
	"testing"
	"time"
)

// errProbeFailed stands in for a herdr transport failure during a pane probe.
var errProbeFailed = errors.New("herdr transport down")

// Live herdr does NOT keep a dead agent registered: seconds after a real
// SIGHUP, `agent get` reports the agent absent, so ObserveLiveness's verdict is
// the empty one and no agent_status survives to be recorded. Attribution has to
// come from the surviving evidence instead — the sidecar pane binding Start
// persisted. resolveBinding is where that binding is proven stale, and its
// confirmed-gone branch is the death transition.
//
// The discrimination matters more than the recording. A session that was never
// started has no binding and returns earlier, so the benign steady state (an
// idle session polled repeatedly, the agent_not_found path responsible for
// thousands of lines in herdr's log) can never reach the death branch.

// deathOps builds paneLookupOps whose every closure is populated, so a wiring
// mistake surfaces as a failed expectation rather than a nil-closure panic.
func deathOps(bound string, probe paneProbe, probeErr error, noted *[]string) paneLookupOps {
	return paneLookupOps{
		getAgent:     func() (agentInfo, bool, error) { return agentInfo{}, false, nil },
		boundPane:    func() string { return bound },
		boundMode:    func() string { return bindModeAgent },
		boundAge:     func() time.Duration { return time.Hour },
		probePane:    func(string) (paneProbe, error) { return probe, probeErr },
		reapPane:     func(string) {},
		clearBinding: func() {},
		noteDeath:    func(paneID string) { *noted = append(*noted, paneID) },
	}
}

// TestResolveBindingNotesConfirmedPaneDeath pins the one branch that is a real
// death: a session that HAD a bound pane, and whose pane herdr now confirms
// gone.
func TestResolveBindingNotesConfirmedPaneDeath(t *testing.T) {
	var noted []string
	_, _, _ = resolveBinding(deathOps("232", paneProbe{}, nil, &noted))

	if len(noted) != 1 || noted[0] != "232" {
		t.Errorf("noteDeath calls = %v; want exactly [232] for a bound pane herdr confirms gone", noted)
	}
}

// TestResolveBindingDoesNotNoteNonDeaths keeps the evidence set clean. Each of
// these reaches resolveBinding constantly in normal operation, and recording
// any of them would bury the real deaths in false ones.
func TestResolveBindingDoesNotNoteNonDeaths(t *testing.T) {
	cases := []struct {
		what     string
		bound    string
		probe    paneProbe
		probeErr error
	}{
		{"never started: no binding to go stale", "", paneProbe{}, nil},
		{"pane alive and busy", "232", paneProbe{Exists: true, Busy: true}, nil},
		{"probe transport failure proves nothing", "232", paneProbe{}, errProbeFailed},
	}
	for _, c := range cases {
		var noted []string
		_, _, _ = resolveBinding(deathOps(c.bound, c.probe, c.probeErr, &noted))
		if len(noted) != 0 {
			t.Errorf("%s: noteDeath called with %v; want no call", c.what, noted)
		}
	}
}

// TestResolveBindingDoesNotNoteReapedExit excludes the agent-exited-to-shell
// branch. That pane still exists; the agent completed and returned to its
// shell, which is what a polecat finishing its work looks like. In the observed
// production window normal completions outnumbered real deaths roughly 34:1, so
// recording them would swamp the signal this exists to isolate.
func TestResolveBindingDoesNotNoteReapedExit(t *testing.T) {
	var noted []string
	ops := deathOps("232", paneProbe{Exists: true, Busy: false}, nil, &noted)
	_, _, _ = resolveBinding(ops)

	if len(noted) != 0 {
		t.Errorf("noteDeath called with %v for a completed agent reaped back to its shell; want no call", noted)
	}
}
