package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestCreateStartedMaterializesWorkDir pins the session-create path as the
// owner of work_dir materialization (gas-oax3).
//
// Neither the runtime providers nor the Manager used to create an agent's work
// dir: the directory existed only as a side effect of somebody having RESOLVED
// it earlier in a reconcile tick. That made an ordinary metadata question mint
// directories, and it left the spawn chain depending on resolution order. The
// create path is where a concrete session is about to run in the directory, so
// it is where the directory is owed.
func TestCreateStartedMaterializesWorkDir(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	mgr := NewManagerWithOptions(store, sp)

	workDir := filepath.Join(t.TempDir(), "agent-home", "nested")
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: work dir must not exist yet, stat err = %v", err)
	}

	info, err := mgr.CreateSession(context.Background(), CreateOptions{
		Template: "helper",
		Title:    "materialize",
		Command:  "claude",
		WorkDir:  workDir,
		Provider: "claude",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !sp.IsRunning(info.SessionName) {
		t.Fatal("runtime session not started")
	}

	fi, err := os.Stat(workDir)
	if err != nil {
		t.Fatalf("work dir not materialized by the create path: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("work dir %q is not a directory", workDir)
	}
}

// TestCreateStartedMaterializesBeforeDiskPreflight pins the ORDER of the two
// steps, which is load-bearing and easy to swap back by accident.
//
// checkSpawnDiskPreflight fails open on a probe error. If the work dir were
// still missing when the probe ran, the probe would error, the floor would be
// silently skipped, and precisely the sessions that have never run before
// would be the ones allowed onto a full disk — reopening gas-wnq. So the probe
// must observe a directory that already exists.
func TestCreateStartedMaterializesBeforeDiskPreflight(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()

	workDir := filepath.Join(t.TempDir(), "agent-home", "nested")

	existedAtProbe := false
	probed := ""
	probe := func(p string) (int64, error) {
		probed = p
		fi, err := os.Stat(p)
		existedAtProbe = err == nil && fi.IsDir()
		return 40 << 30, nil
	}
	mgr := NewManagerWithOptions(store, sp,
		WithDiskPreflight(probe, 1<<30, 0, os.Stderr))

	if _, err := mgr.CreateSession(context.Background(), CreateOptions{
		Template: "helper",
		Title:    "ordering",
		Command:  "claude",
		WorkDir:  workDir,
		Provider: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if probed != workDir {
		t.Fatalf("disk pre-flight probed %q, want the session work dir %q", probed, workDir)
	}
	if !existedAtProbe {
		t.Fatal("disk pre-flight ran before the work dir was materialized: the probe would error and fail open, silently skipping the free-space floor (gas-wnq)")
	}
}

// uncreatableWorkDir returns a path that MkdirAll cannot create, by putting a
// regular FILE where a parent directory would have to be (ENOTDIR). That is
// deterministic on any host, unlike relying on a permission-denied path.
func uncreatableWorkDir(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return filepath.Join(blocker, "work")
}

// TestCreateStartedFailsClosedOnUncreatableWorkDir pins the create path's error
// policy: an operator asked for this session, so a cwd that cannot be created
// is an immediate error and nothing is left half-created.
func TestCreateStartedFailsClosedOnUncreatableWorkDir(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	mgr := NewManagerWithOptions(store, sp)

	_, err := mgr.CreateSession(context.Background(), CreateOptions{
		Template: "helper",
		Title:    "bad cwd",
		Command:  "claude",
		WorkDir:  uncreatableWorkDir(t),
		Provider: "claude",
	})
	if err == nil {
		t.Fatal("CreateSession succeeded with an uncreatable work dir; the create path must fail closed")
	}
}

// TestMaterializeWorkDirBestEffortFailsOpen pins the OPPOSITE policy for the
// reconciler's start bridges. Refusing to wake a session because its recorded
// work_dir cannot be created on this host turns a path problem into "agents
// mysteriously stopped waking"; the provider Start raises the real, legible
// error instead. Regression: making this fail closed broke
// TestReconcileSessionBeads_SyncsGCDirWithWorkDirOverride, whose session
// carries the unmaterializable work_dir "/instance/worktree".
func TestMaterializeWorkDirBestEffortFailsOpen(t *testing.T) {
	var logged strings.Builder
	materializeWorkDirBestEffort(uncreatableWorkDir(t), &logged)

	if logged.Len() == 0 {
		t.Fatal("best-effort materialization swallowed the failure silently; it must report on stderr")
	}
	if !strings.Contains(logged.String(), "fail-open") {
		t.Errorf("log %q should mark itself fail-open so an operator knows the start proceeded anyway", logged.String())
	}
}

// TestCreateBeadOnlyDoesNotMaterializeWorkDir is the other half of the
// contract: a bead-only create spawns nothing, so it must not touch the disk.
// Materializing here would reintroduce the husk vector for every start-pending
// session the reconciler never gets around to starting.
func TestCreateBeadOnlyDoesNotMaterializeWorkDir(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	mgr := NewManagerWithOptions(store, sp)

	workDir := filepath.Join(t.TempDir(), "agent-home", "never-started")

	if _, err := mgr.CreateSession(context.Background(), CreateOptions{
		BeadOnly: true,
		Template: "helper",
		Title:    "queued",
		Command:  "claude",
		WorkDir:  workDir,
		Provider: "claude",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("bead-only create materialized %q (stat err = %v); it spawns nothing and must leave no husk", workDir, err)
	}
}
