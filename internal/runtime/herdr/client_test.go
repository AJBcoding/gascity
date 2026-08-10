package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ── herdr error-envelope decoding ────────────────────────────────────────────
//
// herdr reports a structured failure as {"error":{"code":…,"message":…}} on
// STDERR with a non-zero exit — not on stdout with exit 0. Measured against
// herdr 0.7.5:
//
//	$ herdr --session s agent start bogus --kind claude --pane w9:p9
//	exit=1
//	stderr: {"error":{"code":"agent_pane_not_found","message":"…"}}
//
// Every code-based recovery in the provider (the agent_pane_busy retry loop in
// Start, resolveAgentNameTaken's adopt/reap) branches on herdrErrorCode, so an
// error that loses its *herdrError on that path silently disables all of them.

// newFakeBinClient builds a client whose herdr binary is a script emitting the
// given stdout/stderr and exit status, so run/runRaw can be exercised against
// the real process contract (exit status + stream) rather than a stdout-only
// stand-in.
func newFakeBinClient(t *testing.T, stdout, stderr string, exit int) *client {
	t.Helper()
	script := filepath.Join(t.TempDir(), "herdr")
	fake := "#!/bin/sh\n" +
		"printf '%s' " + shSingleQuote(stdout) + "\n" +
		"printf '%s' " + shSingleQuote(stderr) + " >&2\n" +
		"exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(script, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newClient("gctest-clienterr", "")
	c.bin = script
	return c
}

// shSingleQuote wraps s for safe use as a single-quoted /bin/sh word.
func shSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// A herdr-reported error on stderr must still arrive as a typed *herdrError, or
// the pane-busy retry and name-taken adoption never fire. This is the exact
// shape that failed the push gate: one agent start, rejected in under a second,
// with three retries silently skipped.
func TestRunSurfacesHerdrErrorCodeFromStderrEnvelope(t *testing.T) {
	const envelope = `{"error":{"code":"agent_pane_busy","message":"agent target pane w1:p1 is not an available shell"},"id":"cli:agent:start"}`
	c := newFakeBinClient(t, "", envelope, 1)

	_, err := c.run(context.Background(), "agent", "start", "kindsmoke")
	if err == nil {
		t.Fatal("run against a failing herdr = nil error; want the herdr rejection")
	}
	if got := herdrErrorCode(err); got != "agent_pane_busy" {
		t.Errorf("herdrErrorCode = %q; want %q (error: %v)", got, "agent_pane_busy", err)
	}
	if !strings.Contains(err.Error(), "not an available shell") {
		t.Errorf("error text lost herdr's message: %v", err)
	}
}

// runRaw carries the same contract for the text-returning ops (pane read).
func TestRunRawSurfacesHerdrErrorCodeFromStderrEnvelope(t *testing.T) {
	const envelope = `{"error":{"code":"pane_not_found","message":"pane w1:p1 not found"}}`
	c := newFakeBinClient(t, "", envelope, 1)

	_, err := c.runRaw(context.Background(), "pane", "read", "--pane", "w1:p1")
	if err == nil {
		t.Fatal("runRaw against a failing herdr = nil error; want the herdr rejection")
	}
	if got := herdrErrorCode(err); got != "pane_not_found" {
		t.Errorf("herdrErrorCode = %q; want %q (error: %v)", got, "pane_not_found", err)
	}
}

// A transport failure is not an envelope: herdr writes plain text (e.g.
// `Error: Os { code: 2, kind: NotFound … }` when no server owns the socket).
// That tail must survive verbatim for diagnosis, and must not be mistaken for a
// coded rejection.
func TestRunKeepsNonEnvelopeStderrText(t *testing.T) {
	const plain = `Error: Os { code: 2, kind: NotFound, message: "No such file or directory" }`
	c := newFakeBinClient(t, "", plain, 1)

	_, err := c.run(context.Background(), "agent", "list")
	if err == nil {
		t.Fatal("run against a failing herdr = nil error; want the transport failure")
	}
	if got := herdrErrorCode(err); got != "" {
		t.Errorf("herdrErrorCode = %q; want \"\" for a non-envelope failure", got)
	}
	if !strings.Contains(err.Error(), "No such file or directory") {
		t.Errorf("error text lost the stderr tail: %v", err)
	}
}

// The pre-existing stdout-envelope path (exit 0, error in the payload) keeps
// working — the fake herdr scripts across this package model that shape.
func TestRunStillSurfacesStdoutEnvelopeError(t *testing.T) {
	const envelope = `{"error":{"code":"agent_name_taken","message":"agent kindsmoke already exists"}}`
	c := newFakeBinClient(t, envelope, "", 0)

	_, err := c.run(context.Background(), "agent", "start", "kindsmoke")
	if err == nil {
		t.Fatal("run with an error envelope on stdout = nil error; want the herdr rejection")
	}
	if got := herdrErrorCode(err); got != "agent_name_taken" {
		t.Errorf("herdrErrorCode = %q; want %q", got, "agent_name_taken")
	}
}
