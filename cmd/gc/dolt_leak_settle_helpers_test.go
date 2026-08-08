package main

import (
	"os"
	"strconv"
	"time"
)

const (
	// doltLeakSettleDefaultWindow bounds how long the guard waits for
	// in-flight dolt shutdowns to finish before it calls the survivors a
	// leak. Long enough to cover a shutdown storm on a loaded host, short
	// enough that a genuinely orphaned server is still reported promptly.
	doltLeakSettleDefaultWindow = 10 * time.Second

	// doltLeakSettleInterval is the gap between re-scans inside the window.
	doltLeakSettleInterval = 250 * time.Millisecond
)

// doltLeakSettleWindow returns the settle window, overridable with
// GC_TEST_DOLT_LEAK_SETTLE_MS. Zero restores the old single-scan behavior.
func doltLeakSettleWindow() time.Duration {
	if v := os.Getenv("GC_TEST_DOLT_LEAK_SETTLE_MS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return doltLeakSettleDefaultWindow
}

// settleDoltLeaks re-scans for dolt processes that appeared during the test
// run and returns only those that are still alive once the set stops
// shrinking, or once the window expires.
//
// A single scan taken the instant the run ends cannot distinguish a server the
// tests orphaned from one that is still executing its own shutdown. Under
// concurrency the latter is routine, and treating it as a leak failed the push
// gate on runs where every test passed (gas-tl8). Waiting while the set is
// shrinking tells the two apart: shutdowns converge to empty, orphans do not.
//
// Returns as soon as a scan comes back clean, so a healthy run pays one scan
// and no delay. While anything is still alive it keeps scanning to the
// deadline rather than concluding early from a set that merely looks stable:
// a server three seconds into its own shutdown is indistinguishable from an
// orphan for the first several scans, and calling it a leak then is the exact
// false failure this fixes. Waiting the window out costs time only on runs
// that were going to fail anyway.
func settleDoltLeaks(scan func() (map[int]DoltProcInfo, error), initial map[int]DoltProcInfo, window, interval time.Duration) ([]DoltProcInfo, error) {
	if interval <= 0 {
		interval = doltLeakSettleInterval
	}
	return settleLeaks(scan, initial, window, interval, diffDoltProcessSnapshots)
}

// settleLeaks is the settle loop shared by the dolt and tmux halves of the
// leak guard (gas-1fb). Both ask the same question of different process
// types — "is this survivor orphaned, or still shutting down?" — so the
// waiting policy documented on settleDoltLeaks lives here once, and each
// caller supplies its own scan and diff.
func settleLeaks[T any](
	scan func() (map[int]T, error),
	initial map[int]T,
	window, interval time.Duration,
	diff func(initial, final map[int]T) []T,
) ([]T, error) {
	final, err := scan()
	if err != nil {
		return nil, err
	}
	leaked := diff(initial, final)
	if len(leaked) == 0 || window <= 0 {
		return leaked, nil
	}

	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		final, err = scan()
		if err != nil {
			return nil, err
		}
		next := diff(initial, final)
		if len(next) == 0 {
			return next, nil
		}
		leaked = next
	}
	return leaked, nil
}
