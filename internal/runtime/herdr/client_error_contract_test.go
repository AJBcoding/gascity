package herdr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFailingHerdr writes a stub herdr that reports a failure the way the real
// binary does — exit status 1, empty stdout, payload on stderr — and returns a
// client bound to it. Measured against herdr 0.7.5 on 2026-08-10: `agent get`,
// `pane process-info` and `agent start` all report this same shape, so the
// envelope-on-stdout path run() decodes is never taken for a herdr-reported
// failure.
func fakeFailingHerdr(t *testing.T, stderr string) *client {
	t.Helper()
	script := filepath.Join(t.TempDir(), "herdr")
	fake := `#!/bin/sh
printf '%s' '` + stderr + `' >&2
exit 1
`
	if err := os.WriteFile(script, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	c := newClient("gctest-errcontract", "")
	c.bin = script
	return c
}

const paneBusyEnvelope = `{"error":{"code":"agent_pane_busy","message":"agent target pane w1:p1 is not an available shell"},"id":"cli:agent:start"}`

// run must recover the typed *herdrError from herdr's real failure shape.
// Without it herdrErrorCode returns "" for every herdr-reported error and each
// caller that branches on a code silently takes its failure path: Start's
// agent_pane_busy retry breaks on attempt 0 rather than backing off
// (provider.go), and resolveAgentNameTaken never adopts a live holder or reaps
// a stale one (agent_name_taken.go).
func TestRunRecoversErrorCodeFromStderrEnvelope(t *testing.T) {
	c := fakeFailingHerdr(t, paneBusyEnvelope)

	_, err := c.run(context.Background(), "agent", "start", "kindsmoke")
	if err == nil {
		t.Fatal("run = nil error; want the herdr-reported failure")
	}
	if got := herdrErrorCode(err); got != "agent_pane_busy" {
		t.Errorf("herdrErrorCode = %q; want %q (err: %v)", got, "agent_pane_busy", err)
	}
	// The operator-facing text must survive the typing: these errors are read
	// out of test logs and gate output.
	if msg := err.Error(); !strings.Contains(msg, "not an available shell") {
		t.Errorf("err = %q; want it to retain the herdr message", msg)
	}
}

// runRaw carries the identical stderr branch and the same obligation.
func TestRunRawRecoversErrorCodeFromStderrEnvelope(t *testing.T) {
	c := fakeFailingHerdr(t, paneBusyEnvelope)

	_, err := c.runRaw(context.Background(), "agent", "start", "kindsmoke")
	if err == nil {
		t.Fatal("runRaw = nil error; want the herdr-reported failure")
	}
	if got := herdrErrorCode(err); got != "agent_pane_busy" {
		t.Errorf("herdrErrorCode = %q; want %q (err: %v)", got, "agent_pane_busy", err)
	}
}

// Not every failure carries an envelope: an unknown verb exits 2 with a plain
// usage string on stderr (measured against 0.7.5). That must stay an untyped
// error with its text intact — decoding it as a herdr error would invent a
// code, and dropping the text would hide the real complaint.
func TestRunLeavesNonEnvelopeStderrUntyped(t *testing.T) {
	c := fakeFailingHerdr(t, "unknown command: agent frobnicate\nrun 'herdr --help' for usage")

	_, err := c.run(context.Background(), "agent", "frobnicate")
	if err == nil {
		t.Fatal("run = nil error; want the transport failure")
	}
	if got := herdrErrorCode(err); got != "" {
		t.Errorf("herdrErrorCode = %q; want \"\" for a non-envelope failure", got)
	}
	if msg := err.Error(); !strings.Contains(msg, "unknown command") {
		t.Errorf("err = %q; want it to retain the stderr text", msg)
	}
}
