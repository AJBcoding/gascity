package herdr

import (
	"context"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
)

// TestStartRetriesPaneBusyRejectedOnStderr is the end-to-end guard for the
// push-gate failure: herdr rejects `agent start` with agent_pane_busy when the
// freshly placed pane has not yet reached a prompt its own detector recognizes,
// and Start is supposed to back off, re-wait for the shell, and retry.
//
// It did not. herdr emits that rejection on stderr with a non-zero exit, which
// run() surfaced as an untyped error, so herdrErrorCode returned "" and the
// retry loop broke on its first pass. Measured in the gctest-kind server log
// across 2026-08-09/10: 70 `agent start` requests, 70 completions — a 1:1 ratio
// over 23 rejections, i.e. not one retry was ever issued. Each rejection failed
// the whole package in under a second and took the next pusher's gate with it.
//
// The fake rejects the first launch exactly as herdr does (stderr + exit 1) and
// accepts the second, so a Start that succeeds proves the retry fired.
func TestStartRetriesPaneBusyRejectedOnStderr(t *testing.T) {
	p, state := newFakeHerdrProvider(t)
	listenHerdrSocket(t, p)
	setState(t, state, "pane_busy_once")

	cfg := runtime.Config{
		Command: "claude",
		Env:     map[string]string{"GC_SESSION_ID": "sess-1"},
	}
	if err := p.Start(context.Background(), "gastown__witness", cfg); err != nil {
		t.Fatalf("Start = %v; want the agent_pane_busy rejection absorbed by the retry", err)
	}

	calls := fakeCalls(t, state)
	if n := strings.Count(calls, "agent start "); n != 2 {
		t.Errorf("agent start issued %d time(s); want 2 (rejection + one retry):\n%s", n, calls)
	}
}
