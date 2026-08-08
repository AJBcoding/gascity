package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/gastownhall/gascity/internal/pathutil"
)

const (
	// tmuxLeakSettleDefaultWindow mirrors doltLeakSettleDefaultWindow: long
	// enough to cover a shutdown storm on a loaded host, short enough that a
	// genuinely orphaned server is still reported promptly.
	tmuxLeakSettleDefaultWindow = 10 * time.Second

	// tmuxLeakSettleInterval is the gap between re-scans inside the window.
	tmuxLeakSettleInterval = 250 * time.Millisecond

	// tmuxLeakSettleWindowEnv overrides the window; zero restores a single scan.
	tmuxLeakSettleWindowEnv = "GC_TEST_TMUX_LEAK_SETTLE_MS"

	// tmuxServerPIDTimeout caps each `tmux -S <socket> display-message` probe so
	// a wedged server cannot stall teardown.
	tmuxServerPIDTimeout = 2 * time.Second
)

// tmuxServerInfo identifies one live tmux server owned by this test run. The
// socket path is the scope decision — it is what proves the server is ours —
// and the PID is the only safe way to reap it. Argv is best-effort context for
// the leak report so an operator can trace the spawn site.
type tmuxServerInfo struct {
	PID        int
	SocketPath string
	Argv       []string
}

// leakSettleWindow returns the settle window named by env, or def when unset or
// malformed. Zero is honored: it restores single-scan behavior.
func leakSettleWindow(env string, def time.Duration) time.Duration {
	if v := os.Getenv(env); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return def
}

// diffProcessSnapshots returns the entries present in final but not initial, in
// PID order. Both leak guards key their snapshots by PID, so the diff is the
// same operation regardless of what kind of process is being tracked.
func diffProcessSnapshots[T any](initial, final map[int]T) []T {
	pids := make([]int, 0, len(final))
	for pid := range final {
		if _, ok := initial[pid]; ok {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	leaked := make([]T, 0, len(pids))
	for _, pid := range pids {
		leaked = append(leaked, final[pid])
	}
	return leaked
}

// settleLeaks re-scans for processes that appeared during the test run and
// returns only those still alive once a scan comes back clean, or once the
// window expires.
//
// A single scan taken the instant the run ends cannot distinguish a process the
// tests orphaned from one still executing its own shutdown. Under concurrency
// the latter is routine, and treating it as a leak failed the push gate on runs
// where every test passed (gas-tl8). Waiting while the set is shrinking tells
// the two apart: shutdowns converge to empty, orphans do not.
//
// Returns as soon as a scan comes back clean, so a healthy run pays one scan and
// no delay. While anything is still alive it keeps scanning to the deadline
// rather than concluding early from a set that merely looks stable: a server
// three seconds into its own shutdown is indistinguishable from an orphan for
// the first several scans, and calling it a leak then is the exact false failure
// this avoids. Waiting the window out costs time only on runs that were going to
// fail anyway.
func settleLeaks[T any](scan func() (map[int]T, error), initial map[int]T, window, interval time.Duration) ([]T, error) {
	final, err := scan()
	if err != nil {
		return nil, err
	}
	leaked := diffProcessSnapshots(initial, final)
	if len(leaked) == 0 || window <= 0 {
		return leaked, nil
	}
	if interval <= 0 {
		interval = tmuxLeakSettleInterval
	}

	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		final, err = scan()
		if err != nil {
			return nil, err
		}
		next := diffProcessSnapshots(initial, final)
		if len(next) == 0 {
			return next, nil
		}
		leaked = next
	}
	return leaked, nil
}

// tmuxSocketPathsUnderRoots lists the tmux socket files beneath each root's
// tmux-<uid> directory. Every socket name is enumerated, not just the
// "gctest-*" ones tmuxtest.KillAllTestSessions sweeps: the leak that went
// unnoticed for 22 runs sat on a socket named "test-city" (gas-iio).
//
// Roots that do not exist are skipped rather than reported, because a run that
// never spawned tmux never creates them.
func tmuxSocketPathsUnderRoots(roots []string) []string {
	uidDir := "tmux-" + strconv.Itoa(os.Getuid())
	var sockets []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		dir := filepath.Join(filepath.Clean(root), uidDir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			sockets = append(sockets, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(sockets)
	return sockets
}

// discoverTmuxServersUnderRoots finds the live tmux servers listening on this
// run's sockets. It asks each socket for its server PID instead of pattern
// matching the process table, because a tmux server's argv records the socket
// *name* (`-L test-city`) and not the socket root: two checkouts running
// concurrently both show `-L test-city`, and only the root tells them apart.
//
// Degrades to a no-op when tmux is absent, matching discoverDoltProcesses on a
// host without /proc.
func discoverTmuxServersUnderRoots(roots []string) ([]tmuxServerInfo, error) {
	sockets := tmuxSocketPathsUnderRoots(roots)
	if len(sockets) == 0 {
		return nil, nil
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, nil
	}
	argvByPID := tmuxServerArgvByPID()
	var out []tmuxServerInfo
	for _, socket := range sockets {
		pid, ok := tmuxServerPIDForSocket(socket)
		if !ok {
			continue
		}
		out = append(out, tmuxServerInfo{PID: pid, SocketPath: socket, Argv: argvByPID[pid]})
	}
	return out, nil
}

// tmuxServerPIDForSocket returns the PID of the server listening on socket.
// A missing server, a stale socket inode, or a wedged probe all report false —
// all three mean "nothing of ours is running here". display-message never
// starts a server, so probing cannot create the leak it is looking for.
func tmuxServerPIDForSocket(socket string) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxServerPIDTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "-S", socket, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// tmuxServerArgvByPID maps PID to argv for the leak report. Best-effort: an
// empty map just means the report carries PID and socket without argv.
func tmuxServerArgvByPID() map[int][]string {
	lines, err := psLStartCommandLines()
	if err != nil {
		return nil
	}
	out := make(map[int][]string, len(lines))
	for _, line := range lines {
		fields, command := consumeLeadingFields(line, 7)
		if len(fields) != 7 || command == "" {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		out[pid] = strings.Fields(command)
	}
	return out
}

// snapshotTmuxServersForSocketRoots keys the enumerated servers by PID, keeping
// only those whose socket lives under one of roots. With no roots the result is
// empty: a run that never isolated TMUX_TMPDIR cannot tell its own servers from
// the operator's, and the guard must degrade to a no-op rather than to a
// box-wide sweep.
func snapshotTmuxServersForSocketRoots(enumerate func() ([]tmuxServerInfo, error), roots []string) (map[int]tmuxServerInfo, error) {
	servers, err := enumerate()
	if err != nil {
		return nil, err
	}
	out := make(map[int]tmuxServerInfo, len(servers))
	for _, server := range servers {
		for _, root := range roots {
			if root == "" || !pathutil.PathWithin(root, server.SocketPath) {
				continue
			}
			out[server.PID] = server
			break
		}
	}
	return out, nil
}

func writeTmuxLeakReport(w io.Writer, leaked []tmuxServerInfo) {
	for _, server := range leaked {
		fmt.Fprintf(w, "  pid=%d socket=%s argv=%q\n", server.PID, server.SocketPath, strings.Join(server.Argv, " ")) //nolint:errcheck
	}
}

func reapTmuxLeakServers(leaked []tmuxServerInfo) {
	_ = reapTmuxLeakServersWithKiller(leaked, killProcess)
}

// reapTmuxLeakServersWithKiller signals the leaked servers by PID, reusing the
// dolt guard's PID reaper (TERM, settle, KILL; ESRCH means already gone) —
// reaping is resource-agnostic once the scope decision has been made.
//
// PID is not merely convenient here, it is the only thing that works. Tests
// point TMUX_TMPDIR at their own temp root, so once that tree is removed the
// socket is an unlinked inode and `tmux -L test-city kill-server` fails with
// "error connecting to /private/tmp/tmux-501/test-city". A bare
// `tmux kill-server` would target the operator's default server, which the
// repo's tmux-safety rule forbids outright.
func reapTmuxLeakServersWithKiller(leaked []tmuxServerInfo, killFn func(int, syscall.Signal) error) []error {
	pids := make([]int, 0, len(leaked))
	for _, server := range leaked {
		pids = append(pids, server.PID)
	}
	return reapDoltLeakPIDsWithKiller(pids, killFn)
}

// tmuxLeakGuardedTestingM fails the cmd/gc package when a test leaves a tmux
// server running on this run's socket root, and reaps what it reports.
//
// It composes around another testscript.TestingM rather than extending the dolt
// guard: the two watch different resources with different scope rules, and
// keeping them separate means each stays independently testable. Wrap it
// *inside* the socket-root cleanup in TestMain so the sockets still exist when
// the final scan runs.
type tmuxLeakGuardedTestingM struct {
	m           testscript.TestingM
	socketRoots []string
}

func newTmuxLeakGuardedTestingM(m testscript.TestingM, socketRoots ...string) *tmuxLeakGuardedTestingM {
	roots := make([]string, 0, len(socketRoots))
	for _, root := range socketRoots {
		if strings.TrimSpace(root) != "" {
			roots = append(roots, filepath.Clean(root))
		}
	}
	return &tmuxLeakGuardedTestingM{m: m, socketRoots: roots}
}

func (g *tmuxLeakGuardedTestingM) Run() int {
	enumerate := func() ([]tmuxServerInfo, error) {
		return discoverTmuxServersUnderRoots(g.socketRoots)
	}
	return g.runWith(g.m.Run, enumerate, reapTmuxLeakServers, os.Stderr, leakSettleWindow(tmuxLeakSettleWindowEnv, tmuxLeakSettleDefaultWindow))
}

func (g *tmuxLeakGuardedTestingM) runWith(
	runTests func() int,
	enumerate func() ([]tmuxServerInfo, error),
	reapLeaks func([]tmuxServerInfo),
	out io.Writer,
	window time.Duration,
) int {
	scan := func() (map[int]tmuxServerInfo, error) {
		return snapshotTmuxServersForSocketRoots(enumerate, g.socketRoots)
	}

	initial, initialErr := scan()
	if initialErr != nil {
		fmt.Fprintf(out, "cmd/gc test tmux leak guard: initial scan failed: %v\n", initialErr) //nolint:errcheck
	}

	code := runTests()

	// A guard that cannot scan must not report success: a silent guard is how
	// this leak class stayed invisible across 22 runs (gas-iio).
	guardFailed := initialErr != nil
	if initialErr == nil {
		leaked, finalErr := settleLeaks(scan, initial, window, tmuxLeakSettleInterval)
		switch {
		case finalErr != nil:
			fmt.Fprintf(out, "cmd/gc test tmux leak guard: final scan failed: %v\n", finalErr) //nolint:errcheck
			guardFailed = true
		case len(leaked) > 0:
			fmt.Fprintf(out, "cmd/gc test tmux leak guard: leaked %d tmux server(s) under %s\n", len(leaked), strings.Join(g.socketRoots, " ")) //nolint:errcheck
			writeTmuxLeakReport(out, leaked)
			reapLeaks(leaked)
			guardFailed = true
		}
	}

	if guardFailed && code == 0 {
		return 1
	}
	return code
}
