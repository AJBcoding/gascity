package main

import (
	"errors"
	"testing"
	"time"
)

// The leak guard exists to catch dolt servers a test genuinely orphaned. It
// used to decide that from a single scan taken the instant the run finished,
// which cannot tell an orphan apart from a server that is still mid-shutdown.
// Under load those look identical, so a fully passing run failed the push gate
// three times in a row on timing alone (gas-tl8). These tests pin the settle
// window that separates the two.

func settleFixture(t *testing.T, scans [][]int) func() (map[int]DoltProcInfo, error) {
	t.Helper()
	call := 0
	return func() (map[int]DoltProcInfo, error) {
		snap := map[int]DoltProcInfo{}
		idx := call
		if idx >= len(scans) {
			idx = len(scans) - 1 // hold the final state; the guard may scan past the script
		}
		if idx >= 0 {
			for _, pid := range scans[idx] {
				snap[pid] = DoltProcInfo{PID: pid, Argv: []string{"dolt", "sql-server"}}
			}
		}
		call++
		return snap, nil
	}
}

func TestSettleDoltLeaksClearsWhenShutdownsFinish(t *testing.T) {
	// Three servers winding down: gone by the third scan. Nothing leaked.
	scan := settleFixture(t, [][]int{{1, 2, 3}, {2, 3}, {}})

	leaked, err := settleDoltLeaks(scan, map[int]DoltProcInfo{}, time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("settleDoltLeaks: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("leaked = %v; want none once shutdown completes", leaked)
	}
}

func TestSettleDoltLeaksReportsStableSet(t *testing.T) {
	// A genuinely orphaned server never goes away. The guard must still fail:
	// widening the window must not blind it to real leaks.
	scan := settleFixture(t, [][]int{{7}, {7}, {7}, {7}, {7}, {7}, {7}, {7}})

	leaked, err := settleDoltLeaks(scan, map[int]DoltProcInfo{}, 40*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("settleDoltLeaks: %v", err)
	}
	if len(leaked) != 1 || leaked[0].PID != 7 {
		t.Fatalf("leaked = %v; want the stable orphan pid 7", leaked)
	}
}

func TestSettleDoltLeaksIgnoresPreexistingProcesses(t *testing.T) {
	// A server that was already running before the tests started is not ours.
	initial := map[int]DoltProcInfo{9: {PID: 9, Argv: []string{"dolt", "sql-server"}}}
	scan := settleFixture(t, [][]int{{9}, {9}, {9}})

	leaked, err := settleDoltLeaks(scan, initial, 20*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatalf("settleDoltLeaks: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("leaked = %v; want none — pid 9 predates the run", leaked)
	}
}

func TestSettleDoltLeaksReturnsFirstCleanScanImmediately(t *testing.T) {
	// The common case is no leak at all. That must cost one scan and no
	// waiting, or every green run pays the settle window.
	calls := 0
	scan := func() (map[int]DoltProcInfo, error) {
		calls++
		return map[int]DoltProcInfo{}, nil
	}

	leaked, err := settleDoltLeaks(scan, map[int]DoltProcInfo{}, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("settleDoltLeaks: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("leaked = %v; want none", leaked)
	}
	if calls != 1 {
		t.Errorf("scanned %d times on a clean run; want exactly 1", calls)
	}
}

func TestSettleDoltLeaksPropagatesScanError(t *testing.T) {
	scan := func() (map[int]DoltProcInfo, error) { return nil, errors.New("ps exploded") }

	if _, err := settleDoltLeaks(scan, map[int]DoltProcInfo{}, 20*time.Millisecond, time.Millisecond); err == nil {
		t.Fatal("settleDoltLeaks = nil error on a failing scan; want the error surfaced")
	}
}
