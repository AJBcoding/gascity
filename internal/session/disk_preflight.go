package session

import (
	"fmt"
	"io"
	"os"
)

const spawnDiskGiB = float64(1 << 30)

// DiskFreeBytesFunc probes the bytes available to an unprivileged process in
// the filesystem containing path. It mirrors the seam the store-maintenance
// loop already takes (internal/supervisor.MaintenanceDeps.DiskFreeBytes) so
// both preflights share one production implementation and one fake in tests.
type DiskFreeBytesFunc func(path string) (int64, error)

// checkSpawnDisk applies the Manager's configured disk floors to dir. It is a
// no-op on a Manager that was never given a probe.
func (m *Manager) checkSpawnDisk(dir string) error {
	if dir == "" {
		dir = m.cityPath
	}
	stderr := m.diskStderr
	if stderr == nil {
		stderr = os.Stderr
	}
	return checkSpawnDiskPreflight(dir, m.diskMinFreeBytes, m.diskWarnFreeBytes, m.diskFreeBytes, stderr)
}

// checkSpawnDiskPreflight reports whether a session may be started on a host
// with this much free disk. It returns a non-nil error when free space is
// below minFree, which callers must surface instead of spawning.
//
// A session that starts on a full disk does not fail cleanly: it runs until
// something inside it — a build cache write, a test suite, a git push — hits
// ENOSPC, and the agent parks at a prompt it cannot get past. From outside,
// that is indistinguishable from a hung session, and it holds a pool slot the
// whole time (gas-wnq). Refusing up front converts that silent stall into one
// legible error naming the shortfall.
//
// Fails open by design, matching every other disk preflight here: a zero
// floor disables the check, a nil probe means production never wired one, and
// a probe error proceeds with a logged note. A preflight that cannot measure
// the disk must never be the reason a city stops starting sessions.
func checkSpawnDiskPreflight(dir string, minFree, warnFree int64, probe DiskFreeBytesFunc, stderr io.Writer) error {
	if minFree <= 0 || probe == nil {
		return nil
	}
	free, err := probe(dir)
	if err != nil {
		fmt.Fprintf(stderr, "session: disk pre-flight probe failed (fail-open): %v\n", err) //nolint:errcheck
		return nil
	}
	if free < minFree {
		return fmt.Errorf(
			"refusing to start session: %.1f GiB free is below the floor %.1f GiB on %s; "+
				"free disk space or set GC_SESSION_MIN_FREE_BYTES=0 to disable",
			float64(free)/spawnDiskGiB, float64(minFree)/spawnDiskGiB, dir)
	}
	if warnFree > 0 && free < warnFree {
		fmt.Fprintf(stderr, //nolint:errcheck
			"session: disk WARN — %.1f GiB free (warn %.1f GiB) on %s; starting anyway\n",
			float64(free)/spawnDiskGiB, float64(warnFree)/spawnDiskGiB, dir)
	}
	return nil
}
