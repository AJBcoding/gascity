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
	"time"

	"github.com/gastownhall/gascity/internal/pathutil"
)

const (
	// tmuxLeakCommandTimeout caps each tmux/ps invocation the sweep makes, so
	// one wedged server cannot stall the end of the test run.
	tmuxLeakCommandTimeout = 2 * time.Second

	// tmuxLeakSettleDefaultWindow bounds how long the guard waits for tmux
	// servers that are still shutting down. Shorter than the dolt window: a
	// tmux server exits as soon as its last session dies, so a server still
	// alive after this long is orphaned rather than busy.
	tmuxLeakSettleDefaultWindow = 5 * time.Second

	// tmuxLeakSettleInterval is the gap between re-scans inside the window.
	tmuxLeakSettleInterval = 250 * time.Millisecond

	// tmuxLeakSettleEnv overrides the settle window in milliseconds. Zero
	// restores single-scan behavior, which is what the unit tests want.
	tmuxLeakSettleEnv = "GC_TEST_TMUX_LEAK_SETTLE_MS"
)

// TmuxProcInfo describes one live tmux server discovered through its socket.
// SocketPath is the scoping key: it is the only field that ties a server to
// the run that spawned it, because two concurrent cmd/gc runs spawn servers
// with byte-identical argv (`tmux -L test-city ...`) and tmux servers reparent
// away from the test binary, so neither argv nor ancestry can tell them apart.
type TmuxProcInfo struct {
	PID        int
	SocketPath string
	Argv       []string
}

// tmuxLeakSettleWindow returns the settle window, overridable with
// GC_TEST_TMUX_LEAK_SETTLE_MS. Zero restores the old single-scan behavior.
func tmuxLeakSettleWindow() time.Duration {
	if v := os.Getenv(tmuxLeakSettleEnv); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return tmuxLeakSettleDefaultWindow
}

// discoverTmuxServersForSocketRoot returns the live tmux servers whose socket
// lives under socketRoot, which is this run's TMUX_TMPDIR.
//
// Enumeration starts from the socket rather than from the process table on
// purpose. Scanning processes would surface every tmux server on the box —
// including the operator's own and other checkouts' — and nothing in a tmux
// server's argv says which socket root it belongs to. Globbing this run's
// private root inverts that: a server can only be seen if it is reachable
// through a socket this run owns, so a foreign server is never a candidate for
// the report or the reap.
//
// A socket with no live server behind it (a crashed run's leftover inode)
// resolves to no PID and is skipped, so stale files never fabricate a leak.
func discoverTmuxServersForSocketRoot(socketRoot string) ([]TmuxProcInfo, error) {
	socketRoot = strings.TrimSpace(socketRoot)
	if socketRoot == "" {
		return nil, nil
	}
	// tmux places a -L socket at <TMUX_TMPDIR>/tmux-<uid>/<name>.
	sockets, err := filepath.Glob(filepath.Join(socketRoot, "tmux-*", "*"))
	if err != nil {
		return nil, fmt.Errorf("globbing tmux sockets under %q: %w", socketRoot, err)
	}
	out := make([]TmuxProcInfo, 0, len(sockets))
	for _, socket := range sockets {
		info, err := os.Lstat(socket)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			continue
		}
		pid, ok := tmuxServerPID(socket)
		if !ok {
			continue
		}
		out = append(out, TmuxProcInfo{
			PID:        pid,
			SocketPath: socket,
			Argv:       tmuxServerArgv(pid),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// tmuxServerPID asks the server on socketPath for its own PID. A socket with
// no server behind it fails here, which is how stale inodes are filtered out.
func tmuxServerPID(socketPath string) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxLeakCommandTimeout)
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

// tmuxServerArgv reports the server's command line for the leak report. It is
// diagnostic only — scoping never depends on it — so a failure yields nil
// rather than dropping the leak.
func tmuxServerArgv(pid int) []string {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxLeakCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

// snapshotTmuxServersForSocketRoot maps PID to server for every enumerated
// server whose socket is inside socketRoot. The containment check repeats the
// scoping the enumerator already applies: it is the invariant that keeps a
// foreign server out of the reap set even if enumeration is ever widened or a
// symlink escapes the glob. An empty root disables the sweep entirely.
func snapshotTmuxServersForSocketRoot(enumerate func() ([]TmuxProcInfo, error), socketRoot string) (map[int]TmuxProcInfo, error) {
	procs, err := enumerate()
	if err != nil {
		return nil, err
	}
	out := make(map[int]TmuxProcInfo, len(procs))
	for _, proc := range procs {
		if socketRoot == "" || !pathutil.PathWithin(socketRoot, proc.SocketPath) {
			continue
		}
		out[proc.PID] = proc
	}
	return out, nil
}

// diffTmuxServerSnapshots returns the servers present at the end of the run
// that were not there at the start, ordered by PID.
func diffTmuxServerSnapshots(initial, final map[int]TmuxProcInfo) []TmuxProcInfo {
	leaked := make([]TmuxProcInfo, 0, len(final))
	for pid, proc := range final {
		if _, ok := initial[pid]; ok {
			continue
		}
		leaked = append(leaked, proc)
	}
	sort.Slice(leaked, func(i, j int) bool { return leaked[i].PID < leaked[j].PID })
	return leaked
}

// settleTmuxLeaks re-scans until the set of new servers empties or the window
// expires, so a server still tearing down when the run ends is not called a
// leak. Same reasoning as settleDoltLeaks (gas-tl8).
func settleTmuxLeaks(scan func() (map[int]TmuxProcInfo, error), initial map[int]TmuxProcInfo, window, interval time.Duration) ([]TmuxProcInfo, error) {
	if interval <= 0 {
		interval = tmuxLeakSettleInterval
	}
	return settleLeaks(scan, initial, window, interval, diffTmuxServerSnapshots)
}

// writeTmuxLeakReport prints each leaked server with the PID to trace, the
// socket that proved it belongs to this run, and its argv.
func writeTmuxLeakReport(w io.Writer, leaked []TmuxProcInfo) {
	for _, proc := range leaked {
		fmt.Fprintf(w, "  pid=%d socket=%s argv=%q\n", proc.PID, proc.SocketPath, strings.Join(proc.Argv, " ")) //nolint:errcheck
	}
}

// reapTmuxLeakProcesses kills the leaked servers by PID.
//
// By PID and never by socket name: the fixtures point TMUX_TMPDIR inside their
// own temp root, so by the time the sweep runs that tree can already be gone
// and `tmux -L <name> kill-server` fails against an unlinked inode. Reaping the
// diff by PID also keeps the repo's tmux-safety rule intact — no bare
// kill-server, and the operator's default server is never a candidate.
func reapTmuxLeakProcesses(leaked []TmuxProcInfo) {
	pids := make([]int, 0, len(leaked))
	for _, proc := range leaked {
		pids = append(pids, proc.PID)
	}
	_ = reapLeakPIDsWithKiller(pids, killProcess)
}
