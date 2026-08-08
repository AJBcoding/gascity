package beads

import (
	"testing"
	"time"
)

// sequenceFloorLockTimeout bounds the regression assertion below. The critical
// section reads one small file and renames another, so any wait beyond this is a
// lock that will never be granted rather than a slow machine.
const sequenceFloorLockTimeout = 30 * time.Second

// TestSetSequenceFloorDoesNotBlockOnTheOpenDatabase pins the floor lock's target
// away from the SQLite database file itself.
//
// The database inode looks like the obvious lock target, but the driver already
// owns a lock on it: modernc.org/sqlite's darwin VFS locks the database with
// flock(2) (libc.Xflock, lib/sqlite_darwin_arm64.go). flock ownership belongs to
// the open file description rather than the process, so opening the same path a
// second time inside the same process yields an independent description that
// blocks against the driver's own lock — the process deadlocks against itself.
// Linux never showed it because the default unix VFS there takes fcntl
// byte-range locks, which do not contend with flock, so CI stayed green while
// every macOS run hung.
//
// The bound matters as much as the assertion. The original defect was an
// unbounded LOCK_EX, so the failure surfaced as the whole package burning its
// 20m timeout with dozens of tests parked in "chan receive" — the normal state of
// a t.Parallel() test awaiting its turn — which reads as a parallelism deadlock
// and hides the single stuck syscall that actually caused it.
func TestSetSequenceFloorDoesNotBlockOnTheOpenDatabase(t *testing.T) {
	dir := t.TempDir()
	opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	store := opened.(*SQLiteStore)
	t.Cleanup(func() { _ = store.CloseStore() })

	// Off the test goroutine: a regression blocks forever, and the point is to
	// fail fast and legibly instead of hanging the package.
	done := make(chan error, 1)
	go func() { done <- store.SetSequenceFloor(41) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetSequenceFloor on an open store: %v", err)
		}
	case <-time.After(sequenceFloorLockTimeout):
		t.Fatalf("SetSequenceFloor blocked for %s on an open store: the floor lock is contending with the SQLite driver's own lock on the database file", sequenceFloorLockTimeout)
	}
}

// TestSetSequenceFloorSerializesConcurrentWriters keeps the property the lock
// exists for. Whatever the target, concurrent floor writers must not interleave
// their read-modify-write, and the highest requested floor must win.
func TestSetSequenceFloorSerializesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	store := opened.(*SQLiteStore)
	t.Cleanup(func() { _ = store.CloseStore() })

	const writers = 8
	errs := make(chan error, writers)
	for i := range writers {
		go func() { errs <- store.SetSequenceFloor(int64(10 * (i + 1))) }()
	}
	for range writers {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("concurrent SetSequenceFloor: %v", err)
			}
		case <-time.After(sequenceFloorLockTimeout):
			t.Fatalf("concurrent SetSequenceFloor blocked for %s", sequenceFloorLockTimeout)
		}
	}

	floor, err := store.SequenceFloor()
	if err != nil {
		t.Fatalf("SequenceFloor: %v", err)
	}
	if floor != 10*writers {
		t.Fatalf("SequenceFloor = %d, want the highest requested floor %d", floor, 10*writers)
	}
}
