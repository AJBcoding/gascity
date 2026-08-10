package tmuxtest

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// LeakSettleEnv overrides the leak guard's settle window in milliseconds.
	// Zero restores single-scan behavior, which is what the unit tests want.
	LeakSettleEnv = "GC_TEST_TMUX_LEAK_SETTLE_MS"

	// leakProbeHoldCommand keeps a probe server alive long enough for the test
	// that spawned it to observe it. Every probe registers a reaper, so the
	// duration only bounds the damage if a reaper is ever skipped.
	leakProbeHoldCommand = "sleep 120"

	// leakProbeTerminateGrace bounds the wait for a SIGTERMed server to exit
	// before it is killed outright.
	leakProbeTerminateGrace = 2 * time.Second

	// leakProbePollInterval is the gap between liveness checks while waiting
	// for a signaled server to exit.
	leakProbePollInterval = 20 * time.Millisecond
)

// DisableLeakSettleWindow makes the tmux leak guard scan once instead of
// waiting for servers that are still shutting down.
func DisableLeakSettleWindow(tb testing.TB) {
	tb.Helper()
	tb.Setenv(LeakSettleEnv, "0")
}

// StartDetachedSessionOnSocketPath starts a detached tmux server whose socket
// is the explicit path socketPath. Addressing the socket by path rather than by
// name keeps the server inside the caller's own root by construction, so it can
// never resolve to the operator's default server. The combined output is
// returned alongside the error so callers can report why tmux refused.
func StartDetachedSessionOnSocketPath(socketPath, session string) ([]byte, error) {
	return exec.Command("tmux", "-S", socketPath, "new-session", "-d", "-s", session, leakProbeHoldCommand).CombinedOutput()
}

// StartDetachedSessionOnSocketName starts a detached tmux server on the named
// socket, the way the fixtures start one. It resolves under the process's
// TMUX_TMPDIR, which is what makes it discoverable by a guard armed with that
// same root.
func StartDetachedSessionOnSocketName(socketName, session string) ([]byte, error) {
	return exec.Command("tmux", "-L", socketName, "new-session", "-d", "-s", session, leakProbeHoldCommand).CombinedOutput()
}

// KillServerOnSocketPath tears down the server listening on socketPath. A
// socket with no server behind it reports an error, which callers whose server
// may never have started treat as the expected case.
func KillServerOnSocketPath(socketPath string) error {
	return killTestSocketPath(socketPath)
}

// ServerPID asks the server on socketPath for its own PID. A socket with no
// server behind it fails here, which is how stale inodes are filtered out.
func ServerPID(socketPath string) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxGuardCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "-S", socketPath, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// ServerArgv reports the server's command line for a leak report. It is
// diagnostic only — scoping never depends on it — so a failure yields nil
// rather than dropping the leak.
func ServerArgv(pid int) []string {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxGuardCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

// TerminateAndWait ends a test-spawned tmux server by PID, escalating to
// SIGKILL if it outlives the grace period.
//
// By PID and never by socket name: no bare kill-server, and the operator's
// default server is never a candidate. A non-positive PID is a no-op, so a
// caller that failed to resolve one can register this unconditionally.
func TerminateAndWait(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(leakProbeTerminateGrace)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(leakProbePollInterval)
	}
	if pidAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// pidAlive reports whether pid still exists. Signal 0 performs the permission
// and existence checks without delivering anything.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
