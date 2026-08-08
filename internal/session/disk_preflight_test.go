package session

import (
	"errors"
	"strings"
	"testing"
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
