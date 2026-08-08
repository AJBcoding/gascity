package beads

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// persistSQLiteSequenceFloorAtLeast serializes the final floor re-read and
// atomic replacement across processes. The lock target is the directory that
// holds graph.seqfloor.
//
// The two more obvious targets are both unusable. graph.seqfloor itself is
// replaced by rename, so it cannot safely carry its own lock. The database file
// looks stable, but the SQLite driver already holds a lock on that inode:
// modernc.org/sqlite's darwin VFS locks the database with flock(2)
// (libc.Xflock, lib/sqlite_darwin_arm64.go). flock ownership belongs to the open
// file description rather than the process, so opening the database a second
// time here produced an independent description that blocked against the
// driver's own lock and deadlocked the process against itself — invisibly on
// Linux, whose default unix VFS takes fcntl byte-range locks that do not contend
// with flock.
//
// The directory is a stable inode that nothing else locks, and using it keeps the
// original constraint that motivated the database inode: no additional
// persistent object appears in the Graph namespace.
func persistSQLiteSequenceFloorAtLeast(floorPath string, requested int64) (persisted int64, returnErr error) {
	directory, err := os.Open(filepath.Dir(floorPath))
	if err != nil {
		return 0, fmt.Errorf("opening Graph directory for sequence-floor lock: %w", err)
	}
	observeSQLiteSequenceFloorBoundary("sequence-floor-lock-open")
	locked := false
	defer func() {
		if locked {
			observeSQLiteSequenceFloorBoundary("sequence-floor-lock-release-before")
			if err := syscall.Flock(int(directory.Fd()), syscall.LOCK_UN); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("unlocking SQLite sequence floor: %w", err))
			} else {
				observeSQLiteSequenceFloorBoundary("sequence-floor-lock-release-after")
			}
		}
		observeSQLiteSequenceFloorBoundary("sequence-floor-lock-close-before")
		if err := directory.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("closing SQLite sequence-floor lock descriptor: %w", err))
		} else {
			observeSQLiteSequenceFloorBoundary("sequence-floor-lock-close-after")
		}
	}()
	if err := syscall.Flock(int(directory.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("locking SQLite sequence floor: %w", err)
	}
	locked = true
	observeSQLiteSequenceFloorBoundary("sequence-floor-lock-held")

	current, err := readSQLiteSequenceFloor(floorPath)
	if err != nil {
		return 0, err
	}
	if current > requested {
		requested = current
	}
	if err := writeSQLiteSequenceFloor(floorPath, requested); err != nil {
		return 0, err
	}
	return requested, nil
}
