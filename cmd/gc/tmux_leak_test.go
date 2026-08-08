package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The dolt leak guard fails the cmd/gc package when a test orphans a dolt
// sql-server. Nothing did the same for tmux, so on 2026-08-08 the box carried
// 22 leaked `tmux -u -L test-city new-session -d -s mayor` servers — one per
// run — and every one of those runs had reported ok (gas-iio, gas-1fb).
//
// These tests pin the tmux equivalent. The hard constraint is scope: a server
// this run started must fail the package, and a server belonging to another
// checkout or a live sibling run must never be reported or signaled. Reaping
// is by PID only — the test socket lives under a temp root that is deleted
// before cleanup finishes, so `tmux -L <name> kill-server` fails against an
// unlinked inode.

func tmuxSocket(root, name string) string {
	return filepath.Join(root, "tmux-"+strconv.Itoa(os.Getuid()), name)
}

func tmuxServer(pid int, socketPath string) tmuxServerInfo {
	return tmuxServerInfo{
		PID:        pid,
		SocketPath: socketPath,
		Argv:       []string{"tmux", "-u", "-L", filepath.Base(socketPath), "new-session", "-d", "-s", "mayor"},
	}
}

func TestSnapshotTmuxServersForSocketRootsKeepsOnlyThisRunsServers(t *testing.T) {
	ourRoot := filepath.Join(t.TempDir(), "tmux")
	siblingRoot := filepath.Join(t.TempDir(), "tmux")

	ours := tmuxServer(101, tmuxSocket(ourRoot, "test-city"))
	sibling := tmuxServer(202, tmuxSocket(siblingRoot, "test-city"))
	personal := tmuxServer(303, "/private/tmp/tmux-501/default")

	got, err := snapshotTmuxServersForSocketRoots(func() ([]tmuxServerInfo, error) {
		return []tmuxServerInfo{ours, sibling, personal}, nil
	}, []string{ourRoot})
	if err != nil {
		t.Fatalf("snapshotTmuxServersForSocketRoots: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("snapshot = %#v, want only this run's server", got)
	}
	if _, ok := got[ours.PID]; !ok {
		t.Fatalf("snapshot missing this run's pid %d: %#v", ours.PID, got)
	}
	if _, ok := got[sibling.PID]; ok {
		t.Fatalf("snapshot included a sibling run's server (pid %d) — it must never be touched", sibling.PID)
	}
	if _, ok := got[personal.PID]; ok {
		t.Fatalf("snapshot included the operator's own tmux server (pid %d)", personal.PID)
	}
}

func TestSnapshotTmuxServersForSocketRootsWithoutRootsScansNothing(t *testing.T) {
	// No configured socket root means the run never isolated TMUX_TMPDIR, so
	// there is no way to tell our servers from the operator's. Degrade to a
	// no-op rather than to a box-wide sweep.
	got, err := snapshotTmuxServersForSocketRoots(func() ([]tmuxServerInfo, error) {
		return []tmuxServerInfo{tmuxServer(101, "/private/tmp/tmux-501/default")}, nil
	}, nil)
	if err != nil {
		t.Fatalf("snapshotTmuxServersForSocketRoots: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("snapshot = %#v, want empty when no socket root is configured", got)
	}
}

func TestSnapshotTmuxServersForSocketRootsSurfacesEnumerationError(t *testing.T) {
	want := errors.New("ps blew up")
	_, err := snapshotTmuxServersForSocketRoots(func() ([]tmuxServerInfo, error) {
		return nil, want
	}, []string{t.TempDir()})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the enumeration error surfaced", err)
	}
}

func TestDiffProcessSnapshotsReportsOnlyNewPIDsInPIDOrder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	initial := map[int]tmuxServerInfo{7: tmuxServer(7, tmuxSocket(root, "pre-existing"))}
	final := map[int]tmuxServerInfo{
		7:  tmuxServer(7, tmuxSocket(root, "pre-existing")),
		42: tmuxServer(42, tmuxSocket(root, "test-city")),
		9:  tmuxServer(9, tmuxSocket(root, "gctest-abc")),
	}

	leaked := diffProcessSnapshots(initial, final)

	if len(leaked) != 2 {
		t.Fatalf("leaked = %#v, want the two servers that appeared during the run", leaked)
	}
	if leaked[0].PID != 9 || leaked[1].PID != 42 {
		t.Fatalf("leaked pids = %d,%d, want 9,42 in pid order", leaked[0].PID, leaked[1].PID)
	}
}

func tmuxSettleFixture(t *testing.T, root string, scans [][]int) func() (map[int]tmuxServerInfo, error) {
	t.Helper()
	call := 0
	return func() (map[int]tmuxServerInfo, error) {
		idx := call
		if idx >= len(scans) {
			idx = len(scans) - 1 // hold the final state; the guard may scan past the script
		}
		snap := map[int]tmuxServerInfo{}
		if idx >= 0 {
			for _, pid := range scans[idx] {
				snap[pid] = tmuxServer(pid, tmuxSocket(root, "test-city"))
			}
		}
		call++
		return snap, nil
	}
}

func TestSettleTmuxLeaksClearsWhenShutdownsFinish(t *testing.T) {
	// A tmux server that is still tearing down is indistinguishable from an
	// orphan at any single instant — the same property that made the dolt
	// guard fail three green runs in a row (gas-tl8). Without a settle window
	// this guard would reintroduce that false-failure class on a new axis.
	root := filepath.Join(t.TempDir(), "tmux")
	scan := tmuxSettleFixture(t, root, [][]int{{1, 2}, {2}, {}})

	leaked, err := settleLeaks(scan, map[int]tmuxServerInfo{}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("settleLeaks: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("leaked = %#v, want none once the shutdowns finish", leaked)
	}
}

func TestSettleTmuxLeaksReportsServersThatOutliveTheWindow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	scan := tmuxSettleFixture(t, root, [][]int{{1, 2}, {2}})

	leaked, err := settleLeaks(scan, map[int]tmuxServerInfo{}, 30*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("settleLeaks: %v", err)
	}
	if len(leaked) != 1 || leaked[0].PID != 2 {
		t.Fatalf("leaked = %#v, want the one server still alive at the deadline", leaked)
	}
}

func TestSettleTmuxLeaksSurfacesScanError(t *testing.T) {
	want := errors.New("tmux gone")
	scan := func() (map[int]tmuxServerInfo, error) { return nil, want }
	if _, err := settleLeaks(scan, map[int]tmuxServerInfo{}, 20*time.Millisecond, time.Millisecond); !errors.Is(err, want) {
		t.Fatalf("err = %v, want the scan error surfaced", err)
	}
}

func TestWriteTmuxLeakReportNamesPIDSocketAndArgv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	socketPath := tmuxSocket(root, "test-city")
	var sb strings.Builder

	writeTmuxLeakReport(&sb, []tmuxServerInfo{tmuxServer(4242, socketPath)})

	report := sb.String()
	for _, want := range []string{"pid=4242", socketPath, "new-session -d -s mayor"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report = %q, want it to contain %q so the spawn site is traceable", report, want)
		}
	}
}

func TestReapTmuxLeakServersSignalsOnlyTheLeakedPIDs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	leaked := []tmuxServerInfo{tmuxServer(11, tmuxSocket(root, "test-city"))}

	var signaled []int
	errs := reapTmuxLeakServersWithKiller(leaked, func(pid int, _ syscall.Signal) error {
		signaled = append(signaled, pid)
		return nil
	})

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	for _, pid := range signaled {
		if pid != 11 {
			t.Fatalf("signaled pid %d, want only the leaked pid 11", pid)
		}
	}
	if len(signaled) == 0 {
		t.Fatal("reaper signaled nothing; the leaked server would survive")
	}
}

func TestReapTmuxLeakServersTreatsAlreadyGoneAsSuccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	leaked := []tmuxServerInfo{tmuxServer(11, tmuxSocket(root, "test-city"))}

	errs := reapTmuxLeakServersWithKiller(leaked, func(int, syscall.Signal) error {
		return syscall.ESRCH
	})

	if len(errs) != 0 {
		t.Fatalf("errs = %v, want ESRCH treated as already reaped", errs)
	}
}

type fakeTestingM struct {
	code int
	ran  bool
}

func (m *fakeTestingM) Run() int {
	m.ran = true
	return m.code
}

func TestTmuxLeakGuardFailsTheRunOnALeakedServer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	leaked := tmuxServer(4242, tmuxSocket(root, "test-city"))

	started := false
	enumerate := func() ([]tmuxServerInfo, error) {
		if !started {
			return nil, nil
		}
		return []tmuxServerInfo{leaked}, nil
	}
	inner := &fakeTestingM{code: 0}
	var reaped []tmuxServerInfo
	var report strings.Builder

	g := newTmuxLeakGuardedTestingM(inner, root)
	code := g.runWith(func() int {
		started = true
		return inner.Run()
	}, enumerate, func(servers []tmuxServerInfo) { reaped = append(reaped, servers...) }, &report, 0)

	if code != 1 {
		t.Fatalf("guard returned %d, want 1 — a leaked tmux server must fail the package", code)
	}
	if len(reaped) != 1 || reaped[0].PID != leaked.PID {
		t.Fatalf("reaped = %#v, want only the leaked pid %d", reaped, leaked.PID)
	}
	if !strings.Contains(report.String(), "pid=4242") {
		t.Fatalf("report = %q, want the leaked pid reported", report.String())
	}
}

func TestTmuxLeakGuardIgnoresServersThatPredateTheRun(t *testing.T) {
	// A server already on the socket root when the run started is not this
	// run's leak. Reporting it would fail the package for someone else's
	// residue, and reaping it would kill a live sibling run.
	root := filepath.Join(t.TempDir(), "tmux")
	preExisting := tmuxServer(77, tmuxSocket(root, "test-city"))

	enumerate := func() ([]tmuxServerInfo, error) {
		return []tmuxServerInfo{preExisting}, nil
	}
	inner := &fakeTestingM{code: 0}
	var reaped []tmuxServerInfo
	var report strings.Builder

	g := newTmuxLeakGuardedTestingM(inner, root)
	code := g.runWith(inner.Run, enumerate, func(s []tmuxServerInfo) { reaped = append(reaped, s...) }, &report, 0)

	if code != 0 {
		t.Fatalf("guard returned %d, want 0 — a pre-existing server is not this run's leak", code)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %#v, want nothing; that server belongs to another run", reaped)
	}
}

func TestTmuxLeakGuardKeepsACleanRunGreen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	inner := &fakeTestingM{code: 0}
	var report strings.Builder

	g := newTmuxLeakGuardedTestingM(inner, root)
	code := g.runWith(inner.Run, func() ([]tmuxServerInfo, error) { return nil, nil }, func([]tmuxServerInfo) {}, &report, 0)

	if code != 0 {
		t.Fatalf("guard returned %d, want 0 for a clean run", code)
	}
	if !inner.ran {
		t.Fatal("guard never ran the inner tests")
	}
	if report.String() != "" {
		t.Fatalf("report = %q, want silence on a clean run", report.String())
	}
}

func TestTmuxLeakGuardPreservesAFailingRunsExitCode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	inner := &fakeTestingM{code: 3}
	var report strings.Builder

	g := newTmuxLeakGuardedTestingM(inner, root)
	code := g.runWith(inner.Run, func() ([]tmuxServerInfo, error) { return nil, nil }, func([]tmuxServerInfo) {}, &report, 0)

	if code != 3 {
		t.Fatalf("guard returned %d, want the inner exit code 3 preserved", code)
	}
}

func TestTmuxLeakGuardFailsTheRunWhenTheScanErrors(t *testing.T) {
	// A guard that cannot scan must not report success — that is exactly how
	// the tmux leak class stayed invisible for 22 runs.
	root := filepath.Join(t.TempDir(), "tmux")
	inner := &fakeTestingM{code: 0}
	var report strings.Builder

	g := newTmuxLeakGuardedTestingM(inner, root)
	code := g.runWith(inner.Run, func() ([]tmuxServerInfo, error) {
		return nil, errors.New("ps unavailable")
	}, func([]tmuxServerInfo) {}, &report, 0)

	if code != 1 {
		t.Fatalf("guard returned %d, want 1 when the scan fails", code)
	}
}

func TestTmuxSocketPathsUnderRootsListsSocketsAndSkipsDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	uidDir := filepath.Join(root, "tmux-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(filepath.Join(uidDir, "some-dir"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	for _, name := range []string{"test-city", "gctest-abc12345"} {
		if err := os.WriteFile(filepath.Join(uidDir, name), nil, 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", name, err)
		}
	}

	got := tmuxSocketPathsUnderRoots([]string{root})

	if len(got) != 2 {
		t.Fatalf("sockets = %v, want the two socket files without the directory", got)
	}
	for _, want := range []string{filepath.Join(uidDir, "gctest-abc12345"), filepath.Join(uidDir, "test-city")} {
		found := false
		for _, path := range got {
			if path == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("sockets = %v, missing %q — a non-gctest socket name must still be enumerated", got, want)
		}
	}
}

func TestTmuxSocketPathsUnderRootsIgnoresMissingRoots(t *testing.T) {
	got := tmuxSocketPathsUnderRoots([]string{filepath.Join(t.TempDir(), "never-created"), ""})
	if len(got) != 0 {
		t.Fatalf("sockets = %v, want none for roots that do not exist", got)
	}
}

// TestDiscoverAndReapRealTmuxServerUnderThisRunsRoot exercises the production
// path end to end against a real tmux server, reproducing the exact leak shape
// from gas-iio: `tmux -u -L test-city new-session -d -s mayor`. Every other
// test here injects an enumerator, which cannot prove that socket discovery and
// PID resolution work against real tmux.
//
// The server is spawned on this test's own socket root under /tmp — short, so
// the socket path stays inside the 104-byte macOS limit — and is reaped by PID,
// never by socket name or a bare kill-server.
func TestDiscoverAndReapRealTmuxServerUnderThisRunsRoot(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	socketRoot, err := os.MkdirTemp("/tmp", "gctmuxleak-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	t.Setenv("TMUX_TMPDIR", socketRoot)
	t.Setenv("TMUX", "")

	spawn := exec.Command("tmux", "-u", "-L", "test-city", "new-session", "-d", "-s", "mayor", "sleep", "300")
	spawn.Env = append(os.Environ(), "TMUX_TMPDIR="+socketRoot, "TMUX=")
	if out, err := spawn.CombinedOutput(); err != nil {
		t.Skipf("tmux would not start a server here (%v): %s", err, out)
	}
	// Reap by PID no matter how the assertions below go, so a failure cannot
	// leave behind the very leak this guard exists to catch.
	var serverPID int
	t.Cleanup(func() {
		if serverPID > 0 {
			_ = killProcess(serverPID, syscall.SIGKILL)
		}
	})

	servers, err := discoverTmuxServersUnderRoots([]string{socketRoot})
	if err != nil {
		t.Fatalf("discoverTmuxServersUnderRoots: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %#v, want the one server this test started", servers)
	}
	serverPID = servers[0].PID
	if serverPID <= 0 {
		t.Fatalf("server pid = %d, want the real server pid; reaping needs it", serverPID)
	}
	if want := tmuxSocket(socketRoot, "test-city"); servers[0].SocketPath != want {
		t.Fatalf("socket = %q, want %q", servers[0].SocketPath, want)
	}
	if argv := strings.Join(servers[0].Argv, " "); !strings.Contains(argv, "tmux") {
		t.Fatalf("argv = %q, want the tmux command line for the leak report", argv)
	}

	// The guard must call this a leak: it appeared after the initial snapshot.
	snapshot, err := snapshotTmuxServersForSocketRoots(
		func() ([]tmuxServerInfo, error) { return discoverTmuxServersUnderRoots([]string{socketRoot}) },
		[]string{socketRoot},
	)
	if err != nil {
		t.Fatalf("snapshotTmuxServersForSocketRoots: %v", err)
	}
	leaked := diffProcessSnapshots(map[int]tmuxServerInfo{}, snapshot)
	if len(leaked) != 1 || leaked[0].PID != serverPID {
		t.Fatalf("leaked = %#v, want the running server reported as a leak", leaked)
	}

	if errs := reapTmuxLeakServersWithKiller(leaked, killProcess); len(errs) != 0 {
		t.Fatalf("reap errors = %v", errs)
	}

	// Reaping by PID must actually remove the server, not merely signal it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		remaining, err := discoverTmuxServersUnderRoots([]string{socketRoot})
		if err == nil && len(remaining) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("tmux server pid %d survived the PID reap", serverPID)
}
