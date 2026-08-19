package herdr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── herdr error typing on the stderr path (gas-ao8z) ─────────────────────────
//
// herdr reports a rejected verb by writing the SAME {"error":{code,message}}
// envelope its success path uses — but to stderr, with a nonzero exit. Verified
// against herdr 0.7.5: `herdr --session <live> agent get <missing>` exits 1 with
// an empty stdout and stderr of exactly
//
//	{"error":{"code":"agent_not_found","message":"agent target … not found"},"id":"cli:agent:get"}
//
// runFailure formatted those bytes with %s, so no *herdrError ever reached the
// chain, herdrErrorCode returned "", and every code comparison in the package
// silently failed to match — the pane-busy retry ladder, the agent_name_taken
// storm guard, the timeout and agent_not_found branches were all dead on the one
// path a real herdr actually rejects on. (The fake herdr the package tests
// against emits its errors on stdout with exit 0, which is why the stdout path
// was the only one under test.)
//
// runFailure is the seam both run and runRaw funnel exec failures through, so
// these pin the classification without spawning a subprocess (the cancellation
// cases below are the exception: a killed process is the thing under test).
// *exec.ExitError is constructed directly; its ProcessState is nil, which
// os.ProcessState renders as "<nil>" rather than panicking, so only the code,
// the raw stderr and the unwrap chain are asserted — never the exit-status text.

// exitWithStderr is a nonzero herdr exit carrying stderr, exactly as
// exec.Cmd.Output reports one.
func exitWithStderr(stderr string) error { return &exec.ExitError{Stderr: []byte(stderr)} }

// paneBusyStderr is herdr's agent_pane_busy rejection in its wire shape: one
// envelope line, an "id" field the decoder ignores, a trailing newline.
const paneBusyStderr = `{"error":{"code":"agent_pane_busy","message":"pane %5 is not an available shell"},"id":"cli:agent:start"}` + "\n"

// transportStderr is the real transport failure, verified against herdr 0.7.5
// with a session whose socket does not exist: no envelope at all, and its
// raw-text message is the only diagnostic there is.
const transportStderr = "Error: Os { code: 2, kind: NotFound, message: \"No such file or directory\" }\n"

// The defect itself: a rejection delivered on stderr must expose its code, or
// every herdrErrorCode branch in the package is unreachable in production.
func TestRunFailureTypesStderrEnvelope(t *testing.T) {
	err := runFailure(context.Background(), []string{"agent", "start", "witness"}, exitWithStderr(paneBusyStderr))
	if got := herdrErrorCode(err); got != "agent_pane_busy" {
		t.Errorf("herdrErrorCode = %q; want agent_pane_busy — the code branches are dead without it", got)
	}
	// The typed error is recoverable whole, not just its code.
	var he *herdrError
	if !errors.As(err, &he) || he.Message != "pane %5 is not an available shell" {
		t.Errorf("errors.As(*herdrError) = %v, %+v; want herdr's own message", errors.As(err, &he), he)
	}
	// A log reader must still see herdr's own words and the verb that failed.
	if !strings.Contains(err.Error(), `"code":"agent_pane_busy"`) {
		t.Errorf("message lost the raw stderr body: %v", err)
	}
	if !strings.Contains(err.Error(), "agent start witness") {
		t.Errorf("message lost the failing verb: %v", err)
	}
	// The exit status survives too: operators read it, and dropping the
	// ExitError would trade one lost diagnostic for another.
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("errors.As(*exec.ExitError) = false; the exit status was dropped: %v", err)
	}
}

// The message text is a contract of its own, and typing the stderr path must
// not renegotiate it. Every failed verb has always rendered as
// fmt.Sprintf("herdr %v: %s", args, ee.Stderr) — herdr's stderr byte for byte,
// surrounding whitespace and trailing newline included. Structured detail
// belongs in the unwrap chain, where machine consumers read it; the string is
// what a human reads in a log, so it stays put. A substring assertion permits
// exactly the drift this pins (a prefix inserted ahead of the old body still
// contains it), so these compare whole strings.
//
// Both stderr shapes are here for a second reason: the exit status must be
// recoverable from EITHER. Preserving *exec.ExitError only when the envelope
// happens to parse makes the diagnostic depend on herdr's output shape.
func TestRunFailurePreservesLegacyErrorText(t *testing.T) {
	args := []string{"agent", "start", "witness"}
	tests := []struct {
		name     string
		stderr   string
		want     string
		wantCode string
	}{{
		name:     "typed envelope on stderr",
		stderr:   paneBusyStderr,
		want:     "herdr [agent start witness]: " + paneBusyStderr,
		wantCode: "agent_pane_busy",
	}, {
		name:   "plain transport text on stderr",
		stderr: transportStderr,
		want:   "herdr [agent start witness]: " + transportStderr,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runFailure(context.Background(), args, exitWithStderr(tt.stderr))
			if got := err.Error(); got != tt.want {
				t.Errorf("error text changed\n got: %q\nwant: %q", got, tt.want)
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Errorf("errors.As(*exec.ExitError) = false; the exit status is unrecoverable on this path: %v", err)
			}
			if got := herdrErrorCode(err); got != tt.wantCode {
				t.Errorf("herdrErrorCode = %q; want %q", got, tt.wantCode)
			}
		})
	}
}

// Stderr is shared with anything else herdr (or a wrapper) prints. The envelope
// must still be found beside log lines, and noise alone must never be mistaken
// for one.
func TestRunFailureStderrNoise(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{"envelope only", paneBusyStderr, "agent_pane_busy"},
		{"envelope after a log line", "WARN reconnecting to session socket\n" + paneBusyStderr, "agent_pane_busy"},
		{"envelope before trailing noise", paneBusyStderr + "WARN socket closed\n", "agent_pane_busy"},
		{"transport failure, no envelope", transportStderr, ""},
		{"truncated envelope", `{"error":{"code":"agent_pane_busy","mess`, ""},
		{"brace-led non-JSON", "{not json at all\n", ""},
		{"success envelope on stderr", `{"result":{"agent":{"name":"witness"}}}` + "\n", ""},
		{"envelope with no code", `{"error":{"message":"something went wrong"}}` + "\n", ""},
		{"empty line noise", "\n\n   \n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runFailure(context.Background(), []string{"agent", "get", "witness"}, exitWithStderr(tt.stderr))
			if got := herdrErrorCode(err); got != tt.want {
				t.Errorf("herdrErrorCode = %q; want %q", got, tt.want)
			}
			// Typed or not, the stderr body is never dropped.
			if body := strings.TrimSpace(tt.stderr); body != "" && !strings.Contains(err.Error(), body) {
				t.Errorf("message dropped the stderr body\n got: %v\nwant containing: %s", err, body)
			}
		})
	}
}

// A failure with no stderr at all — a spawn failure, or a killed process — is
// still wrapped, so context cancellation and exec.ErrNotFound stay matchable.
func TestRunFailureWithoutStderrWrapsCause(t *testing.T) {
	err := runFailure(context.Background(), []string{"agent", "get", "witness"}, exec.ErrNotFound)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("errors.Is(exec.ErrNotFound) = false; want the cause preserved: %v", err)
	}
	if got := herdrErrorCode(err); got != "" {
		t.Errorf("herdrErrorCode = %q for a non-herdr failure; want empty", got)
	}
}

// ── cancellation of an in-flight verb ────────────────────────────────────────
//
// A spawn failure carries the context cause on its own; a verb canceled AFTER
// the process started does not. os/exec's Cmd.Wait sees the killed process
// first and deliberately prefers its *exec.ExitError over the context watcher's
// cause, so the error reaching runFailure says "signal: killed" and nothing in
// its chain answers errors.Is(err, context.Canceled). Callers that bound a
// Start on a deadline (the reconciler's startup_timeout, a drain, an operator's
// ctrl-C) then read their own cancellation as a herdr refusal.
//
// The fake herdr below is a script that never answers, run through the client's
// own exec seam — the package's established fake-herdr pattern — so this is a
// real process killed by a real cancellation, not a stubbed error value.

// blockingHerdr is a herdr that never answers, so a verb issued against it is
// still in flight when the context dies. `exec` replaces the shell with the
// sleep on purpose: a forked child would inherit Output's stdout pipe and keep
// Wait blocked for the whole sleep even after CommandContext killed the shell.
func blockingHerdr(t *testing.T) *client {
	t.Helper()
	script := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &client{session: "gctest-cancel", bin: script}
}

func TestRunCancelledInFlightStaysMatchable(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *client) error
	}{{
		name: "run",
		call: func(ctx context.Context, c *client) error {
			_, err := c.run(ctx, "agent", "start", "witness")
			return err
		},
	}, {
		name: "runRaw",
		call: func(ctx context.Context, c *client) error { _, err := c.runRaw(ctx, "pane", "read", "%5"); return err },
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Long enough that the verb is certainly in flight, short enough
			// that the test does not wait on it. Canceling early instead would
			// only exercise the spawn-time path, which already worked.
			timer := time.AfterFunc(150*time.Millisecond, cancel)
			defer timer.Stop()

			err := tt.call(ctx, blockingHerdr(t))
			if err == nil {
				t.Fatal("a canceled verb returned no error at all")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(context.Canceled) = false for a verb canceled mid-command; got %v", err)
			}
		})
	}
}

// The same guarantee pinned without a process, on exactly the error shape
// os/exec hands back for a killed command. The exit status survives alongside
// the cause: one diagnostic is not traded for the other.
func TestRunFailureKeepsContextCauseUnderExitError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runFailure(ctx, []string{"agent", "start", "witness"}, &exec.ExitError{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(context.Canceled) = false; the cause was lost under the exit error: %v", err)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Errorf("errors.As(*exec.ExitError) = false; the exit status was dropped: %v", err)
	}
}

// isAgentNotFound's typed branch (client.go) is one of the five that only ever
// saw "" before: herdr's own agent_not_found arrives on stderr, and the
// substring fallback that carried this case is exactly what the typed check was
// meant to replace.
func TestIsAgentNotFoundTypedOnStderrPath(t *testing.T) {
	const notFound = `{"error":{"code":"agent_not_found","message":"agent target witness not found"},"id":"cli:agent:get"}` + "\n"
	if !isAgentNotFound(runFailure(context.Background(), []string{"agent", "get", "witness"}, exitWithStderr(notFound))) {
		t.Error("isAgentNotFound = false for herdr's agent_not_found on stderr")
	}
	if isAgentNotFound(runFailure(context.Background(), []string{"agent", "get", "witness"}, exitWithStderr(paneBusyStderr))) {
		t.Error("isAgentNotFound = true for agent_pane_busy; the codes must not blur")
	}
}
