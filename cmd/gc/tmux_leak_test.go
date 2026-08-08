package main

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/test/tmuxtest"
)

// tmuxLeakTestSocket builds a socket path in the layout tmux actually uses
// under TMUX_TMPDIR: <root>/tmux-<uid>/<socket name>.
func tmuxLeakTestSocket(root, name string) string {
	return filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}

func TestSnapshotTmuxServersForSocketRootKeepsOnlyThisRunsServers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tmux")
	mine := TmuxProcInfo{
		PID:        2001,
		SocketPath: tmuxLeakTestSocket(root, "test-city"),
		Argv:       []string{"tmux", "-u", "-L", "test-city", "new-session", "-d", "-s", "mayor"},
	}
	// Same socket name, different run root: another checkout's server.
	other := TmuxProcInfo{
		PID:        2002,
		SocketPath: tmuxLeakTestSocket(filepath.Join(t.TempDir(), "tmux"), "test-city"),
		Argv:       []string{"tmux", "-u", "-L", "test-city", "new-session", "-d", "-s", "mayor"},
	}
	enumerate := func() ([]TmuxProcInfo, error) {
		return []TmuxProcInfo{mine, other}, nil
	}

	got, err := snapshotTmuxServersForSocketRoot(enumerate, root)
	if err != nil {
		t.Fatalf("snapshotTmuxServersForSocketRoot: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("snapshot = %#v, want only the server under %s", got, root)
	}
	if _, ok := got[mine.PID]; !ok {
		t.Fatalf("snapshot = %#v, want PID %d", got, mine.PID)
	}
}

func TestSnapshotTmuxServersForEmptyRootIsEmpty(t *testing.T) {
	enumerate := func() ([]TmuxProcInfo, error) {
		return []TmuxProcInfo{{PID: 2001, SocketPath: "/tmp/whatever/tmux-0/test-city"}}, nil
	}

	got, err := snapshotTmuxServersForSocketRoot(enumerate, "")
	if err != nil {
		t.Fatalf("snapshotTmuxServersForSocketRoot: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("snapshot = %#v, want empty when the run has no tmux socket root", got)
	}
}

func TestDiffTmuxServerSnapshotsReportsOnlyNewServers(t *testing.T) {
	initial := map[int]TmuxProcInfo{2000: {PID: 2000}}
	final := map[int]TmuxProcInfo{
		2000: {PID: 2000},
		2003: {PID: 2003},
		2001: {PID: 2001},
	}

	got := diffTmuxServerSnapshots(initial, final)

	if len(got) != 2 {
		t.Fatalf("diff length = %d, want 2: %#v", len(got), got)
	}
	if got[0].PID != 2001 || got[1].PID != 2003 {
		t.Fatalf("diff PIDs = [%d %d], want [2001 2003]", got[0].PID, got[1].PID)
	}
}

func TestWriteTmuxLeakReportShowsPIDSocketAndArgv(t *testing.T) {
	var buf bytes.Buffer

	writeTmuxLeakReport(&buf, []TmuxProcInfo{{
		PID:        2001,
		SocketPath: "/tmp/gct1234/tmux/tmux-501/test-city",
		Argv:       []string{"tmux", "-u", "-L", "test-city", "new-session", "-d", "-s", "mayor"},
	}})

	report := buf.String()
	for _, want := range []string{"pid=2001", "/tmp/gct1234/tmux/tmux-501/test-city", "-L test-city"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report = %q, want it to contain %q", report, want)
		}
	}
}

func TestSettleTmuxLeaksIgnoresServerThatExitsInsideWindow(t *testing.T) {
	// gas-tl8's lesson applied to tmux: a server still tearing down when the
	// run ends is not a leak. Only survivors of the settle window are.
	leaked := TmuxProcInfo{PID: 2001, SocketPath: "/tmp/root/tmux-501/test-city"}
	var scans int
	scan := func() (map[int]TmuxProcInfo, error) {
		scans++
		if scans == 1 {
			return map[int]TmuxProcInfo{leaked.PID: leaked}, nil
		}
		return nil, nil
	}

	got, err := settleTmuxLeaks(scan, nil, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("settleTmuxLeaks: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("settled leaks = %#v, want none once the server exits inside the window", got)
	}
}

func TestSettleTmuxLeaksReportsServerThatOutlivesWindow(t *testing.T) {
	leaked := TmuxProcInfo{PID: 2001, SocketPath: "/tmp/root/tmux-501/test-city"}
	scan := func() (map[int]TmuxProcInfo, error) {
		return map[int]TmuxProcInfo{leaked.PID: leaked}, nil
	}

	got, err := settleTmuxLeaks(scan, nil, 10*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("settleTmuxLeaks: %v", err)
	}

	if len(got) != 1 || got[0].PID != leaked.PID {
		t.Fatalf("settled leaks = %#v, want PID %d", got, leaked.PID)
	}
}

func TestLeakGuardFailsAndReapsLeakedTmuxServer(t *testing.T) {
	t.Setenv(tmuxLeakSettleEnv, "0")
	socketRoot := filepath.Join(t.TempDir(), "tmux")
	leaked := TmuxProcInfo{
		PID:        2001,
		SocketPath: tmuxLeakTestSocket(socketRoot, "test-city"),
		Argv:       []string{"tmux", "-u", "-L", "test-city", "new-session", "-d", "-s", "mayor"},
	}
	var scans int
	var reaped []TmuxProcInfo

	g := newDoltLeakGuardedTestingM(nil, filepath.Join(t.TempDir(), "gct-current"))
	g.guardTmuxSocketRoot(socketRoot)
	g.tmuxEnumerate = func() ([]TmuxProcInfo, error) {
		scans++
		if scans == 1 {
			return nil, nil
		}
		return []TmuxProcInfo{leaked}, nil
	}
	g.tmuxReap = func(procs []TmuxProcInfo) { reaped = append(reaped, procs...) }

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) { return nil, nil },
		func(string) bool { return false },
		func() {},
		func() {},
		func([]DoltProcInfo) {},
	)

	if code != 1 {
		t.Fatalf("guard returned code %d, want 1 for a leaked tmux server", code)
	}
	if len(reaped) != 1 || reaped[0].PID != leaked.PID {
		t.Fatalf("reaped = %#v, want only PID %d through the injected reaper", reaped, leaked.PID)
	}
}

func TestLeakGuardNeverTouchesTmuxServerOutsideRunRoot(t *testing.T) {
	// Acceptance: a server belonging to another checkout or to the operator's
	// own tmux is never reported and never reaped.
	t.Setenv(tmuxLeakSettleEnv, "0")
	socketRoot := filepath.Join(t.TempDir(), "tmux")
	foreign := TmuxProcInfo{
		PID:        2002,
		SocketPath: tmuxLeakTestSocket(filepath.Join(t.TempDir(), "tmux"), "test-city"),
		Argv:       []string{"tmux", "-u", "-L", "test-city", "new-session", "-d", "-s", "mayor"},
	}
	var scans int
	var reaped []TmuxProcInfo

	g := newDoltLeakGuardedTestingM(nil, filepath.Join(t.TempDir(), "gct-current"))
	g.guardTmuxSocketRoot(socketRoot)
	// The foreign server must appear only after the run: a server present in
	// both snapshots is excluded by the diff no matter what the socket-root
	// scoping does, which would make this test pass without testing it.
	g.tmuxEnumerate = func() ([]TmuxProcInfo, error) {
		scans++
		if scans == 1 {
			return nil, nil
		}
		return []TmuxProcInfo{foreign}, nil
	}
	g.tmuxReap = func(procs []TmuxProcInfo) { reaped = append(reaped, procs...) }

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) { return nil, nil },
		func(string) bool { return false },
		func() {},
		func() {},
		func([]DoltProcInfo) {},
	)

	if code != 0 {
		t.Fatalf("guard returned code %d, want 0: a foreign tmux server is not this run's leak", code)
	}
	if len(reaped) != 0 {
		t.Fatalf("reaped = %#v, want nothing outside %s", reaped, socketRoot)
	}
}

func TestLeakGuardWithoutTmuxSocketRootSkipsTmuxSweep(t *testing.T) {
	g := newDoltLeakGuardedTestingM(nil, filepath.Join(t.TempDir(), "gct-current"))
	enumerated := false
	g.tmuxEnumerate = func() ([]TmuxProcInfo, error) {
		enumerated = true
		return nil, nil
	}

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) { return nil, nil },
		func(string) bool { return false },
		func() {},
		func() {},
		func([]DoltProcInfo) {},
	)

	if code != 0 {
		t.Fatalf("guard returned code %d, want 0", code)
	}
	if enumerated {
		t.Fatal("tmux enumeration ran without a tmux socket root; want the sweep disabled")
	}
}

func TestDiscoverTmuxServersForSocketRootWithoutRootIsNoop(t *testing.T) {
	got, err := discoverTmuxServersForSocketRoot("")
	if err != nil {
		t.Fatalf("discoverTmuxServersForSocketRoot(%q): %v", "", err)
	}
	if len(got) != 0 {
		t.Fatalf("discovered = %#v, want none for an empty socket root", got)
	}
}

func TestDiscoverTmuxServersForSocketRootIgnoresStaleSocket(t *testing.T) {
	root := shortSocketTempDir(t, "gctmuxstale")
	socket := tmuxLeakTestSocket(root, "test-city")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}
	// A real socket inode with no server behind it — what a crashed tmux
	// leaves on disk. Keep the file after Close so the glob still finds it.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatalf("creating stale socket: %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("closing stale socket: %v", err)
	}

	got, err := discoverTmuxServersForSocketRoot(root)
	if err != nil {
		t.Fatalf("discoverTmuxServersForSocketRoot: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("discovered = %#v, want none for a socket with no live server", got)
	}
}

// TestDiscoverTmuxServersForSocketRootFindsLiveServer exercises the real
// socket-to-PID resolution the guard depends on. The injected-enumerator tests
// above cannot cover it: they stub out exactly the step that talks to tmux.
func TestDiscoverTmuxServersForSocketRootFindsLiveServer(t *testing.T) {
	tmuxtest.RequireTmux(t)
	root := shortSocketTempDir(t, "gctmuxlive")
	socket := tmuxLeakTestSocket(root, "gctest-leakguard")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}

	// -S pins the socket path, so this server is discoverable without
	// mutating TMUX_TMPDIR for the whole test process.
	start := exec.Command("tmux", "-S", socket, "new-session", "-d", "-s", "probe", "sleep 120")
	if out, err := start.CombinedOutput(); err != nil {
		t.Skipf("starting isolated tmux server: %v: %s", err, out)
	}
	// Register the reaper from the PID resolved at spawn time, before any
	// assertion can abort the test: a cleanup that depended on the assertions
	// passing would leak a server whenever this test fails.
	spawnedPID, ok := tmuxServerPID(socket)
	registerTmuxTestServerReaper(t, spawnedPID)
	if !ok {
		t.Fatalf("resolving pid of the server on %s", socket)
	}

	got, err := discoverTmuxServersForSocketRoot(root)
	if err != nil {
		t.Fatalf("discoverTmuxServersForSocketRoot: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("discovered = %#v, want exactly the server on %s", got, socket)
	}
	if got[0].PID != spawnedPID {
		t.Fatalf("discovered PID = %d, want the spawned server pid %d", got[0].PID, spawnedPID)
	}
	if !pidAlive(got[0].PID) {
		t.Fatalf("discovered PID %d is not alive", got[0].PID)
	}
	assertSameTestPath(t, got[0].SocketPath, socket)
	if len(got[0].Argv) == 0 || !strings.Contains(strings.Join(got[0].Argv, " "), "tmux") {
		t.Fatalf("discovered argv = %q, want the tmux server command line", got[0].Argv)
	}
}

// TestTmuxLeakGuardCatchesRealLeakedServer is the acceptance case: a tmux
// server left running under this run's socket root fails the package.
func TestTmuxLeakGuardCatchesRealLeakedServer(t *testing.T) {
	tmuxtest.RequireTmux(t)
	t.Setenv(tmuxLeakSettleEnv, "0")
	socketRoot := shortSocketTempDir(t, "gctmuxguard")
	socket := tmuxLeakTestSocket(socketRoot, "gctest-leaked")
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatalf("creating socket dir: %v", err)
	}

	var reaped []TmuxProcInfo
	var spawnedPID int
	g := newDoltLeakGuardedTestingM(nil, filepath.Join(t.TempDir(), "gct-current"))
	g.guardTmuxSocketRoot(socketRoot)
	// Record instead of reaping, so the assertions below can prove the guard
	// selected the right PID. The reaper registered at spawn time is what
	// actually kills the server.
	g.tmuxReap = func(procs []TmuxProcInfo) { reaped = append(reaped, procs...) }

	code := g.runWith(
		func() int {
			// Stand in for a test that forgets to tear its server down.
			start := exec.Command("tmux", "-S", socket, "new-session", "-d", "-s", "leaked", "sleep 120")
			if out, err := start.CombinedOutput(); err != nil {
				t.Skipf("starting isolated tmux server: %v: %s", err, out)
			}
			spawnedPID, _ = tmuxServerPID(socket)
			registerTmuxTestServerReaper(t, spawnedPID)
			return 0
		},
		func() ([]DoltProcInfo, error) { return nil, nil },
		func(string) bool { return false },
		func() {},
		func() {},
		func([]DoltProcInfo) {},
	)

	if code != 1 {
		t.Fatalf("guard returned code %d, want 1: a leaked tmux server must fail the package", code)
	}
	if len(reaped) != 1 {
		t.Fatalf("reaped = %#v, want exactly the leaked server", reaped)
	}
	if reaped[0].PID != spawnedPID {
		t.Fatalf("reaped PID = %d, want the leaked server pid %d", reaped[0].PID, spawnedPID)
	}
	assertSameTestPath(t, reaped[0].SocketPath, socket)
}

// TestTmuxLeakGuardWatchesTheProcessSocketRoot pins the production wiring:
// TestMain arms the guard with the same socket root it puts in TMUX_TMPDIR, so
// a server started the way the fixtures start one — with -L, not an explicit
// -S path — lands where the guard globs. The tests above all choose the socket
// path themselves and so cannot catch a wiring drift between the two.
func TestTmuxLeakGuardWatchesTheProcessSocketRoot(t *testing.T) {
	tmuxtest.RequireTmux(t)
	socketRoot := os.Getenv("TMUX_TMPDIR")
	if socketRoot == "" {
		t.Fatal("TMUX_TMPDIR is empty; TestMain must point cmd/gc tests at an isolated tmux socket root")
	}

	socketName := fmt.Sprintf("gctest-wiring-%d", os.Getpid())
	start := exec.Command("tmux", "-L", socketName, "new-session", "-d", "-s", "probe", "sleep 120")
	if out, err := start.CombinedOutput(); err != nil {
		t.Skipf("starting tmux server on the process socket root: %v: %s", err, out)
	}
	spawnedPID, _ := tmuxServerPID(tmuxLeakTestSocket(socketRoot, socketName))
	registerTmuxTestServerReaper(t, spawnedPID)

	got, err := discoverTmuxServersForSocketRoot(socketRoot)
	if err != nil {
		t.Fatalf("discoverTmuxServersForSocketRoot: %v", err)
	}

	found := false
	for _, proc := range got {
		if proc.PID == spawnedPID {
			found = true
		}
	}
	if !found {
		t.Fatalf("discovered = %#v, want the -L %s server (pid %d) under %s", got, socketName, spawnedPID, socketRoot)
	}
}

// registerTmuxTestServerReaper guarantees a test-spawned tmux server dies with
// the test, by PID, whether or not the assertions that follow pass.
func registerTmuxTestServerReaper(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() {
		if pid <= 0 {
			return
		}
		// By PID only: never a bare kill-server, never the default socket.
		_ = syscall.Kill(pid, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for pidAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if pidAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}
