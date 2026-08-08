package main

import (
	"os"
	"strconv"
)

const (
	// sessionDiskDefaultMinFreeBytes is the floor below which starting a
	// session is refused (2 GiB). A session is not a small allocation: it
	// gets a worktree, and whatever it then runs writes build caches and
	// test output. Below a couple of GiB those writes hit ENOSPC partway
	// through, which is the silent stall this floor exists to prevent
	// (gas-wnq). It sits above the managed-Dolt floor (500 MiB) because
	// Dolt only needs room for its own store, not for a build.
	sessionDiskDefaultMinFreeBytes = 2 << 30 // 2 GiB

	// sessionDiskDefaultWarnFreeBytes is the soft floor (8 GiB): enough
	// headroom that a full Go test suite plus cache growth still fits, so
	// crossing it is worth saying out loud while there is still time to
	// reclaim space.
	sessionDiskDefaultWarnFreeBytes = 8 << 30 // 8 GiB
)

// sessionDiskFreeBytesFunc is the production free-space probe for the session
// spawn preflight. It deliberately reuses the managed-Dolt probe: one statfs
// implementation, already correct about APFS purgeable space (f_bavail, not
// f_bfree), serves both preflights.
var sessionDiskFreeBytesFunc = doltContainerFreeBytesFunc

// sessionDiskMinFreeBytes returns the spawn floor from
// GC_SESSION_MIN_FREE_BYTES, defaulting to 2 GiB. Zero disables the check.
func sessionDiskMinFreeBytes() int64 {
	return sessionDiskBytesEnv("GC_SESSION_MIN_FREE_BYTES", sessionDiskDefaultMinFreeBytes)
}

// sessionDiskWarnFreeBytes returns the spawn warning threshold from
// GC_SESSION_WARN_FREE_BYTES, defaulting to 8 GiB. Zero disables the warning.
func sessionDiskWarnFreeBytes() int64 {
	return sessionDiskBytesEnv("GC_SESSION_WARN_FREE_BYTES", sessionDiskDefaultWarnFreeBytes)
}

// sessionDiskBytesEnv reads a non-negative byte count from the environment,
// falling back to def when unset or unparseable.
func sessionDiskBytesEnv(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return def
}
