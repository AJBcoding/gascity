package beads

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// persistSQLiteSequenceFloorAtLeast serializes the final floor re-read and
// atomic replacement across processes. graph.seqfloor is replaced by rename and
// so cannot carry its own lock; the lock therefore lives on a dedicated sibling
// file, matching FileStore's "<path>.lock" convention.
//
// The database inode is deliberately NOT the lock target, even though it is the
// one file guaranteed to exist. flock(2) locks belong to the open file
// description rather than the process, so locking the database contends with
// the store's own live connection instead of being re-entrant. Linux never
// shows this — SQLite serializes there with POSIX fcntl byte-range locks and
// never flocks the database — but macOS builds SQLite with
// SQLITE_ENABLE_LOCKING_STYLE and selects an flock-based VFS, so the store's
// open connection holds an flock on the database and a blocking LOCK_EX here
// deadlocked against it permanently (gas-bsj).
func persistSQLiteSequenceFloorAtLeast(floorPath string, requested int64) (persisted int64, returnErr error) {
	lock, err := os.OpenFile(floorPath+sqliteSequenceFloorLockSuffix, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return 0, fmt.Errorf("opening sequence-floor lock file: %w", err)
	}
	observeSQLiteSequenceFloorBoundary("sequence-floor-lock-open")
	locked := false
	defer func() {
		if locked {
			observeSQLiteSequenceFloorBoundary("sequence-floor-lock-release-before")
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("unlocking SQLite sequence floor: %w", err))
			} else {
				observeSQLiteSequenceFloorBoundary("sequence-floor-lock-release-after")
			}
		}
		observeSQLiteSequenceFloorBoundary("sequence-floor-lock-close-before")
		if err := lock.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("closing SQLite sequence-floor lock descriptor: %w", err))
		} else {
			observeSQLiteSequenceFloorBoundary("sequence-floor-lock-close-after")
		}
	}()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
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
