package main

import (
	"path/filepath"
	"testing"
)

// doltProcWithConfigUnder builds a dolt sql-server process record whose
// --config path sits under dir, the shape discoverDoltProcesses reports.
func doltProcWithConfigUnder(pid int, dir string) DoltProcInfo {
	return DoltProcInfo{
		PID: pid,
		Argv: []string{
			"dolt",
			"sql-server",
			"--config",
			filepath.Join(dir, ".gc", "runtime", "packs", "dolt", "dolt-config.yaml"),
		},
	}
}

// TestSnapshotDoltProcessesForConfigRootsSeesRepoRootedServer is the gas-dfn
// regression. The leak guard scopes its before/after snapshots to the private
// temp root, so a test that starts a managed dolt whose data-dir resolves to
// the PACKAGE DIRECTORY instead of t.TempDir() is outside both snapshots: it
// leaks silently, the package still reports ok, and the live server holds the
// lock on $REPO/cmd/gc/.beads/dolt so the next run in that checkout can block.
// Scoping to the temp root AND the package dir makes that class loud.
func TestSnapshotDoltProcessesForConfigRootsSeesRepoRootedServer(t *testing.T) {
	tempRoot := filepath.Join("/tmp", "gc-cmd-test-root")
	repoRoot := filepath.Join("/Users/someone/code/gascity", "cmd", "gc")
	owned := doltProcWithConfigUnder(1001, filepath.Join(tempRoot, "TestOwned", "001"))
	repoRooted := doltProcWithConfigUnder(1002, repoRoot)
	unrelated := doltProcWithConfigUnder(1003, filepath.Join("/tmp", "TestOther", "001"))

	got, err := snapshotDoltProcessesForConfigRoots(func() ([]DoltProcInfo, error) {
		return []DoltProcInfo{owned, repoRooted, unrelated}, nil
	}, tempRoot, repoRoot)
	if err != nil {
		t.Fatalf("snapshotDoltProcessesForConfigRoots: %v", err)
	}
	if _, ok := got[1002]; !ok {
		t.Errorf("snapshot missed the repo-rooted server (PID 1002): %#v — this is the leak that reports ok today", got)
	}
	if _, ok := got[1001]; !ok {
		t.Errorf("snapshot dropped the temp-root server (PID 1001): %#v", got)
	}
	if _, ok := got[1003]; ok {
		t.Errorf("snapshot included an unrelated server (PID 1003): %#v — the guard must not claim processes it does not own", got)
	}
}

// TestSnapshotDoltProcessesForConfigRootsIgnoresEmptyRoots proves a blank root
// never widens the scope to everything. An empty tempRoot (createActiveTestTempRoot
// failed) must not turn the guard into a box-wide reaper of other checkouts'
// dolt servers.
func TestSnapshotDoltProcessesForConfigRootsIgnoresEmptyRoots(t *testing.T) {
	proc := doltProcWithConfigUnder(1001, filepath.Join("/tmp", "elsewhere"))

	got, err := snapshotDoltProcessesForConfigRoots(func() ([]DoltProcInfo, error) {
		return []DoltProcInfo{proc}, nil
	}, "", "")
	if err != nil {
		t.Fatalf("snapshotDoltProcessesForConfigRoots: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("snapshot = %#v with no roots configured, want empty", got)
	}
}

// TestSnapshotDoltProcessesForConfigRootMatchesSingleRootBehavior keeps the
// original single-root entry point honest once it delegates to the multi-root
// form: the existing callers' semantics must not shift.
func TestSnapshotDoltProcessesForConfigRootMatchesSingleRootBehavior(t *testing.T) {
	root := filepath.Join("/tmp", "gc-cmd-test-root")
	owned := doltProcWithConfigUnder(1001, filepath.Join(root, "TestOwned", "001"))
	unowned := doltProcWithConfigUnder(1002, filepath.Join("/tmp", "TestOther", "001"))
	enumerate := func() ([]DoltProcInfo, error) {
		return []DoltProcInfo{owned, unowned}, nil
	}

	single, err := snapshotDoltProcessesForConfigRoot(enumerate, root)
	if err != nil {
		t.Fatalf("snapshotDoltProcessesForConfigRoot: %v", err)
	}
	multi, err := snapshotDoltProcessesForConfigRoots(enumerate, root)
	if err != nil {
		t.Fatalf("snapshotDoltProcessesForConfigRoots: %v", err)
	}
	if len(single) != len(multi) {
		t.Fatalf("single-root snapshot = %#v, multi-root snapshot = %#v, want identical", single, multi)
	}
	for pid := range single {
		if _, ok := multi[pid]; !ok {
			t.Errorf("multi-root snapshot missing PID %d present in single-root", pid)
		}
	}
}
