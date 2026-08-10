package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestSpawnDiskPreflightRefusesBelowFloor(t *testing.T) {
	var log strings.Builder
	probe := func(string) (int64, error) { return 300 << 20, nil } // 300 MiB

	err := checkSpawnDiskPreflight("/work/dir", 1<<30, 2<<30, probe, &log)
	if err == nil {
		t.Fatal("checkSpawnDiskPreflight = nil below the floor; want a refusal")
	}
	// The operator's first question is "how full, and where?". An error that
	// omits the numbers sends them back to df, which is the diagnosis loop
	// this preflight exists to remove (gas-wnq).
	for _, want := range []string{"0.3 GiB", "1.0 GiB", "/work/dir", "GC_SESSION_MIN_FREE_BYTES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestSpawnDiskPreflightAllowsAboveFloor(t *testing.T) {
	var log strings.Builder
	probe := func(string) (int64, error) { return 40 << 30, nil } // 40 GiB

	if err := checkSpawnDiskPreflight("/work/dir", 1<<30, 2<<30, probe, &log); err != nil {
		t.Fatalf("checkSpawnDiskPreflight = %v with 40 GiB free; want nil", err)
	}
	if log.String() != "" {
		t.Errorf("healthy disk logged %q; want silence", log.String())
	}
}

func TestSpawnDiskPreflightWarnsButProceedsInTheBand(t *testing.T) {
	var log strings.Builder
	probe := func(string) (int64, error) { return 3 << 30, nil } // 3 GiB: under warn, over floor

	if err := checkSpawnDiskPreflight("/work/dir", 1<<30, 5<<30, probe, &log); err != nil {
		t.Fatalf("checkSpawnDiskPreflight = %v above the floor; want nil (warn only)", err)
	}
	if !strings.Contains(log.String(), "WARN") {
		t.Errorf("log = %q; want a WARN line so the pressure is visible before it is fatal", log.String())
	}
}

// TestSpawnDiskPreflightFailsOpenOnProbeError pins the fail-open contract this
// codebase already applies to its other disk preflights: a probe that cannot
// answer must never block a spawn. Refusing on a broken probe would convert a
// diagnostic into an outage across every session the city starts.
func TestSpawnDiskPreflightFailsOpenOnProbeError(t *testing.T) {
	var log strings.Builder
	probe := func(string) (int64, error) { return -1, errors.New("statfs boom") }

	if err := checkSpawnDiskPreflight("/work/dir", 1<<30, 2<<30, probe, &log); err != nil {
		t.Fatalf("checkSpawnDiskPreflight = %v on probe failure; want nil (fail open)", err)
	}
	if !strings.Contains(log.String(), "fail-open") {
		t.Errorf("log = %q; want the fail-open path to say so rather than pass silently", log.String())
	}
}

// TestSpawnDiskPreflightDisabledByZeroFloor keeps the documented escape hatch:
// the check must be switchable off entirely, and a nil probe (no production
// wiring) must behave as "not configured" rather than panic.
func TestSpawnDiskPreflightDisabledByZeroFloor(t *testing.T) {
	var log strings.Builder
	probed := false
	probe := func(string) (int64, error) { probed = true; return 0, nil }

	if err := checkSpawnDiskPreflight("/work/dir", 0, 2<<30, probe, &log); err != nil {
		t.Fatalf("checkSpawnDiskPreflight = %v with the check disabled; want nil", err)
	}
	if probed {
		t.Error("probe ran with the check disabled; a zero floor must short-circuit")
	}
	if err := checkSpawnDiskPreflight("/work/dir", 1<<30, 2<<30, nil, &log); err != nil {
		t.Fatalf("checkSpawnDiskPreflight = %v with no probe wired; want nil", err)
	}
}

// TestManagerCheckSpawnDiskUsesCityPathWhenWorkDirEmpty pins the fallback: a
// spec with no WorkDir must still be measured somewhere real, not against "",
// which statfs would reject and the fail-open path would then wave through.
func TestManagerCheckSpawnDiskUsesCityPathWhenWorkDirEmpty(t *testing.T) {
	var probed string
	m := &Manager{
		cityPath:         "/city",
		diskFreeBytes:    func(p string) (int64, error) { probed = p; return 40 << 30, nil },
		diskMinFreeBytes: 1 << 30,
		diskStderr:       &strings.Builder{},
	}
	if err := m.checkSpawnDisk(""); err != nil {
		t.Fatalf("checkSpawnDisk = %v; want nil", err)
	}
	if probed != "/city" {
		t.Errorf("probed %q; want the city path as the fallback measurement point", probed)
	}
}

// TestManagerCheckSpawnDiskUnconfiguredIsNoop guards every existing caller:
// a Manager built without WithDiskPreflight must behave exactly as before.
func TestManagerCheckSpawnDiskUnconfiguredIsNoop(t *testing.T) {
	m := &Manager{cityPath: "/city"}
	if err := m.checkSpawnDisk("/work"); err != nil {
		t.Fatalf("checkSpawnDisk = %v on an unconfigured Manager; want nil", err)
	}
}

// armFullDisk points an already-built Manager's spawn preflight at a disk with
// nothing left on it. Arming after construction is the point: the session is
// created and suspended while the disk is healthy, so what the test exercises
// is the wake, not the create.
func armFullDisk(m *Manager) {
	m.diskFreeBytes = func(string) (int64, error) { return 100 << 20, nil } // 100 MiB
	m.diskMinFreeBytes = 2 << 30
	m.diskWarnFreeBytes = 8 << 30
	m.diskStderr = &strings.Builder{}
}

// armHealthyDisk is armFullDisk's counterpart: the preflight is wired and live,
// but the disk has room. It exists to prove the wake refusals below are caused
// by the disk and not merely by the preflight being wired at all.
func armHealthyDisk(m *Manager) *strings.Builder {
	log := &strings.Builder{}
	m.diskFreeBytes = func(string) (int64, error) { return 40 << 30, nil } // 40 GiB
	m.diskMinFreeBytes = 2 << 30
	m.diskWarnFreeBytes = 8 << 30
	m.diskStderr = log
	return log
}

// TestStartRefusesToWakeOntoAFullDisk is the wake-path half of gas-9nx. gas-wnq
// guarded creation only, so Manager.Start would resume a suspended session onto
// a full disk and reproduce the original incident: the agent runs until
// something inside it hits ENOSPC, then parks at a prompt while holding a pool
// slot. The refusal must happen before any runtime is spawned.
func TestStartRefusesToWakeOntoAFullDisk(t *testing.T) {
	mgr, sp, info := seedSuspendedResumeTarget(t)
	armFullDisk(mgr)

	err := mgr.Start(context.Background(), info.ID, BuildResumeCommand(info), runtime.Config{WorkDir: info.WorkDir})
	if err == nil {
		t.Fatal("Start woke a session onto a full disk; want a refusal")
	}
	if !strings.Contains(err.Error(), "refusing to start session") {
		t.Fatalf("Start error = %v; want the disk preflight refusal", err)
	}
	if hasEventPrefix(sp.events, "start:") {
		t.Fatalf("a runtime was spawned despite the refusal; events = %v", sp.events)
	}
}

// TestStartRuntimeOnlyRefusesToRespawnOntoAFullDisk is the reconciler respawn
// bridge counterpart. This is the path that matters most for the incident: the
// reconciler respawns unattended, so an unguarded spawn here re-creates the
// silent stall with no human in the loop.
func TestStartRuntimeOnlyRefusesToRespawnOntoAFullDisk(t *testing.T) {
	mgr, sp, info := seedSuspendedResumeTarget(t)
	armFullDisk(mgr)

	err := mgr.StartRuntimeOnly(context.Background(), info.ID, BuildResumeCommand(info), runtime.Config{WorkDir: info.WorkDir})
	if err == nil {
		t.Fatal("StartRuntimeOnly respawned onto a full disk; want a refusal")
	}
	if !strings.Contains(err.Error(), "refusing to start session") {
		t.Fatalf("StartRuntimeOnly error = %v; want the disk preflight refusal", err)
	}
	if hasEventPrefix(sp.events, "start:") {
		t.Fatalf("a runtime was spawned despite the refusal; events = %v", sp.events)
	}
}

// TestWakePathsProceedWhenTheDiskHasHeadroom proves the guard does not
// over-refuse. Without this, a preflight that rejected everything would satisfy
// the two refusal tests above and quietly stop the city from waking anything.
func TestWakePathsProceedWhenTheDiskHasHeadroom(t *testing.T) {
	t.Run("Start", func(t *testing.T) {
		mgr, sp, info := seedSuspendedResumeTarget(t)
		log := armHealthyDisk(mgr)

		if err := mgr.Start(context.Background(), info.ID, BuildResumeCommand(info), runtime.Config{WorkDir: info.WorkDir}); err != nil {
			t.Fatalf("Start with 40 GiB free: %v", err)
		}
		if !hasEventPrefix(sp.events, "start:") {
			t.Fatalf("no runtime spawned on a healthy disk; events = %v", sp.events)
		}
		if log.String() != "" {
			t.Errorf("healthy disk logged %q; want silence", log.String())
		}
	})

	t.Run("StartRuntimeOnly", func(t *testing.T) {
		mgr, sp, info := seedSuspendedResumeTarget(t)
		log := armHealthyDisk(mgr)

		if err := mgr.StartRuntimeOnly(context.Background(), info.ID, BuildResumeCommand(info), runtime.Config{WorkDir: info.WorkDir}); err != nil {
			t.Fatalf("StartRuntimeOnly with 40 GiB free: %v", err)
		}
		if !hasEventPrefix(sp.events, "start:") {
			t.Fatalf("no runtime spawned on a healthy disk; events = %v", sp.events)
		}
		if log.String() != "" {
			t.Errorf("healthy disk logged %q; want silence", log.String())
		}
	})
}

// TestCreateStillRefusesBeforeReservingAnything pins the property gas-wnq built
// the early check for, which the chokepoint must not quietly replace. The
// chokepoint alone would refuse only at the spawn — after the bead and the name
// reservations exist — leaving a half-created session behind on a full disk.
// createStarted must still refuse before any of that.
func TestCreateStillRefusesBeforeReservingAnything(t *testing.T) {
	store := beads.NewMemStore()
	sp := &orphanScanProvider{Fake: runtime.NewFake()}
	mgr := NewManagerWithOptions(store, sp,
		WithDiskPreflight(func(string) (int64, error) { return 100 << 20, nil }, 2<<30, 8<<30, &strings.Builder{}))

	_, err := mgr.CreateSession(context.Background(), CreateOptions{
		Template: "helper", Command: "claude", WorkDir: t.TempDir(), Provider: "claude",
		ExtraMeta: map[string]string{"session_origin": "manual"},
	})
	if err == nil {
		t.Fatal("CreateSession succeeded on a full disk; want a refusal")
	}
	if !strings.Contains(err.Error(), "refusing to start session") {
		t.Fatalf("CreateSession error = %v; want the disk preflight refusal", err)
	}
	if hasEventPrefix(sp.events, "start:") {
		t.Fatalf("a runtime was spawned despite the refusal; events = %v", sp.events)
	}
	// The whole point of refusing early: nothing was reserved to clean up.
	all, listErr := store.List(beads.ListQuery{IncludeClosed: true, AllowScan: true})
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	for _, b := range all {
		if b.Type == "session" {
			t.Fatalf("refusal left a session bead behind (%s); the check must run "+
				"before any bead or name reservation exists", b.ID)
		}
	}
}

// TestFullDiskWarnIsNotLoggedTwiceForOneStart keeps the operator-facing output
// honest. createStarted checks the floor early and the chokepoint checks again
// at the spawn; if both also emitted the warn band, every create under pressure
// would print the same WARN twice and read as two separate events.
func TestFullDiskWarnIsNotLoggedTwiceForOneStart(t *testing.T) {
	log := &strings.Builder{}
	store := beads.NewMemStore()
	sp := &orphanScanProvider{Fake: runtime.NewFake()}
	mgr := NewManagerWithOptions(store, sp,
		// 3 GiB free: above the 2 GiB floor, inside the 8 GiB warn band.
		WithDiskPreflight(func(string) (int64, error) { return 3 << 30, nil }, 2<<30, 8<<30, log))

	if _, err := mgr.CreateSession(context.Background(), CreateOptions{
		Template: "helper", Command: "claude", WorkDir: t.TempDir(), Provider: "claude",
		ExtraMeta: map[string]string{"session_origin": "manual"},
	}); err != nil {
		t.Fatalf("CreateSession in the warn band: %v", err)
	}
	if got := strings.Count(log.String(), "WARN"); got != 1 {
		t.Fatalf("one create logged %d WARN lines; want exactly 1\nlog: %s", got, log.String())
	}
}
