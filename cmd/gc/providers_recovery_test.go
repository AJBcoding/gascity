package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadProviderSessionSnapshotAvoidsManagedDoltRecovery is the gas-0v3m
// regression test. loadProviderSessionSnapshot opens its OWN city store
// (openSessionProviderStore) purely to read session-class beads for
// transport/ACP routing, and discards every error it gets back. On a
// bd-backed scope that open used to resolve the bd runtime env with recovery
// ENABLED, so merely CONSTRUCTING a session provider against a scope whose
// health check fails started a detached `dolt sql-server` that this path
// neither owns nor stops. Against a healthy city it was invisible; against
// an unhealthy one it was one unowned server per construction.
//
// This is the same caller shape ga-cdmx6x already converted for scoped reads
// (scopedBdStoreForCity / bdRuntimeEnvWithErrorNoRecovery): a short
// best-effort read must fail fast rather than pay a multi-second
// recovery/health-check/autostart sequence, and every concurrent
// construction attempting that recovery multiplies exactly the load that
// mitigation exists to bound.
//
// The assertion is the spawned server, not wall-clock. Latency is only a
// proxy — the defect is the unowned process — and a timing threshold here
// would redden the gate under exactly the contention this package already
// fights (gas-0vsl).
func TestLoadProviderSessionSnapshotAvoidsManagedDoltRecovery(t *testing.T) {
	cityDir := t.TempDir()
	// Pin the bd provider rather than inheriting GC_BEADS from the ambient
	// session: the managed-dolt path is the condition under test, so it must
	// not depend on how the runner's environment happens to be set.
	content := `[workspace]
name = "demo"

[beads]
provider = "bd"
`
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// Registers a pre-scan now and re-scans in t.Cleanup, reporting any dolt
	// sql-server whose --config lives under cityDir that this test spawned.
	requireNoLeakedDoltAfterForPaths(t, cityDir)

	// Mirrors how the real shared store is constructed at controller/CLI
	// startup: the first call materializes .gc/scripts/gc-beads-bd.sh, which
	// is what makes a later env resolution believe a managed dolt server
	// ought to exist and attempt to start or recover one.
	_ = bdStoreForCity(cityDir, cityDir)

	// Best-effort by contract: a nil snapshot is a valid outcome here. The
	// point is that failing to read must not leave a server behind.
	_ = loadProviderSessionSnapshot(sessionProviderContext{cityPath: cityDir})
}
