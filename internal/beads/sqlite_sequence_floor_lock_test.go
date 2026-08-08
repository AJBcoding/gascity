package beads

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// sequenceFloorPersistDeadline bounds how long the serialized floor write may
// take when nothing else contends for it. The critical section is a sidecar
// read plus an atomic rewrite, so anything beyond this is a lock that will
// never be granted rather than a slow disk.
const sequenceFloorPersistDeadline = 20 * time.Second

// TestSetSequenceFloorDoesNotDeadlockAgainstTheStoresOwnConnection is the
// regression test for gas-bsj. persistSQLiteSequenceFloorAtLeast used to take
// its cross-process flock on the SQLite database file itself. flock(2) locks
// belong to the open file description, not the process, so a second
// independent os.Open of that inode contends with the store's own live
// connection rather than being re-entrant.
//
// That is invisible on Linux, where SQLite serializes with POSIX fcntl
// byte-range locks and never flocks the database. macOS builds SQLite with
// SQLITE_ENABLE_LOCKING_STYLE and selects an flock-based VFS, so the store's
// own open connection holds an flock on the database file and the blocking
// LOCK_EX below never returns — the package timed out at 20m instead of
// failing.
//
// The lock target must therefore be a file SQLite does not own.
func TestSetSequenceFloorDoesNotDeadlockAgainstTheStoresOwnConnection(t *testing.T) {
	dir := t.TempDir()
	opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	store := opened.(*SQLiteStore)
	t.Cleanup(func() { _ = store.CloseStore() })

	// Buffered so the writer never blocks on send if this test has already
	// failed the deadline and stopped receiving.
	done := make(chan error, 1)
	go func() { done <- store.SetSequenceFloor(41) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetSequenceFloor: %v", err)
		}
	case <-time.After(sequenceFloorPersistDeadline):
		t.Fatalf("SetSequenceFloor blocked for %s with no other process contending; "+
			"the floor lock is contending with the store's own SQLite connection",
			sequenceFloorPersistDeadline)
	}
}

// TestSequenceFloorLockStillExcludesConcurrentHolders guards the other side of
// the fix: moving the lock off the database inode must not quietly turn it into
// a no-op. While the floor lock is held, an independent acquisition of the same
// lock must be refused.
//
// The Linux-only cross-process test covers the same contract with real
// subprocesses; this one runs everywhere, which matters because the deadlock it
// partners with was macOS-only.
func TestSequenceFloorLockStillExcludesConcurrentHolders(t *testing.T) {
	dir := t.TempDir()
	opened, err := OpenSQLiteStore(dir, WithSQLiteStoreIDPrefix("gcg"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	store := opened.(*SQLiteStore)
	t.Cleanup(func() { _ = store.CloseStore() })

	// Probe from inside the critical section, at the exact point the floor lock
	// is held: an independent descriptor on the same lock file must be refused.
	// The probe only records what it saw — asserting here would risk calling
	// t.Error from the writer goroutine after this test had already returned.
	previous := observeSQLiteSequenceFloorBoundary
	t.Cleanup(func() { observeSQLiteSequenceFloorBoundary = previous })
	var (
		probed      bool
		probeErr    error
		uncontended bool
	)
	lockPath := filepath.Join(dir, sqliteGraphSequenceFloorFilename) + sqliteSequenceFloorLockSuffix
	observeSQLiteSequenceFloorBoundary = func(reached string) {
		if reached != "sequence-floor-lock-held" {
			return
		}
		probed = true
		contender, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if openErr != nil {
			probeErr = openErr
			return
		}
		defer contender.Close() //nolint:errcheck // probe descriptor
		if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			uncontended = true
			_ = syscall.Flock(int(contender.Fd()), syscall.LOCK_UN)
		}
	}

	done := make(chan error, 1)
	go func() { done <- store.SetSequenceFloor(7) }()

	// Receiving from done happens after the probe ran, so its writes are
	// visible here without further synchronization.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetSequenceFloor: %v", err)
		}
	case <-time.After(sequenceFloorPersistDeadline):
		t.Fatalf("SetSequenceFloor blocked for %s; the floor lock never became available",
			sequenceFloorPersistDeadline)
	}
	if !probed {
		t.Fatal("never observed the sequence-floor-lock-held boundary; the probe did not run")
	}
	if probeErr != nil {
		t.Fatalf("open sequence-floor lock file: %v", probeErr)
	}
	if uncontended {
		t.Error("acquired the sequence-floor lock while it was already held; " +
			"the lock no longer excludes concurrent holders")
	}
}
