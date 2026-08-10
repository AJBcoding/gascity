package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── MySQL backend wiring (external-server lane, mirrors the PG slice) ──

func writeMySQLScopeFixture(t *testing.T, scopeRoot string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(scopeRoot, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"database":"beads.db","backend":"mysql","mysql_dsn":"root@tcp(127.0.0.1:3306)/","mysql_database":"anthony_beads"}`
	if err := os.WriteFile(filepath.Join(scopeRoot, ".beads", "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExternalBackendMetadataForScopeMySQL(t *testing.T) {
	cityPath := t.TempDir()
	scopeRoot := t.TempDir()
	writeMySQLScopeFixture(t, scopeRoot)

	meta, ok, err := externalBackendMetadataForScope(cityPath, scopeRoot)
	if err != nil {
		t.Fatalf("externalBackendMetadataForScope: %v", err)
	}
	if !ok {
		t.Fatal("externalBackendMetadataForScope ok = false, want true for mysql scope")
	}
	if meta.Backend != "mysql" || meta.MySQLDatabase != "anthony_beads" {
		t.Fatalf("meta = %+v, want mysql backend with anthony_beads", meta)
	}
}

func TestExternalBackendMetadataForScopeDoltIsNotExternal(t *testing.T) {
	cityPath := t.TempDir()
	scopeRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(scopeRoot, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`
	if err := os.WriteFile(filepath.Join(scopeRoot, ".beads", "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := externalBackendMetadataForScope(cityPath, scopeRoot)
	if err != nil {
		t.Fatalf("externalBackendMetadataForScope: %v", err)
	}
	if ok {
		t.Fatal("externalBackendMetadataForScope ok = true for dolt scope, want false")
	}
}

func TestApplyResolvedScopeMySQLEnvClearsProjectionsAndProjectsNothing(t *testing.T) {
	// The postgres keys this fixture used to carry were removed when postgres
	// was retired (gas-1oou, 2026-08-10). They stopped being gc's projections,
	// so they are no longer gc's to clear — the same "foreign residue, left
	// alone" rule this change applied to postgres_* metadata keys. Upstream's
	// TestOSSProjectsNoUnregisteredBackendEnv holds the stronger line that
	// makes this safe: source may not NAME an env var for a backend gc does
	// not implement, so there is no postgres projection left to leak.
	// GC_DOLT_* / BEADS_DOLT_* below keep the actual assertion intact: gc
	// clears its OWN projected namespace and projects nothing for mysql.
	env := map[string]string{
		"GC_BEADS_BACKEND":       "doltlite",
		"BEADS_BACKEND":          "doltlite",
		"BEADS_DOLT_SERVER_HOST": "127.0.0.1",
		"UNRELATED":              "keep",
	}
	applyResolvedScopeMySQLEnv(env)

	if got := env["UNRELATED"]; got != "keep" {
		t.Fatalf("UNRELATED = %q, want preserved", got)
	}
	for _, key := range []string{
		"GC_BEADS_BACKEND", "BEADS_BACKEND", "BEADS_DOLT_SERVER_HOST",
	} {
		if val, ok := env[key]; ok && val != "" {
			t.Errorf("env[%q] = %q, want cleared for mysql scope", key, val)
		}
	}
	for key := range env {
		if key != "UNRELATED" && env[key] != "" {
			t.Errorf("env[%q] = %q, want no projection for mysql scope (bd self-configures)", key, env[key])
		}
	}
}

func TestScopeUsesExternalServerBackendForInitMySQL(t *testing.T) {
	cityPath := t.TempDir()
	writeMySQLScopeFixture(t, cityPath)

	usesExternal, err := scopeSkipsManagedDoltForInit(cityPath, cityPath)
	if err != nil {
		t.Fatalf("scopeSkipsManagedDoltForInit: %v", err)
	}
	if !usesExternal {
		t.Fatal("scopeSkipsManagedDoltForInit = false for mysql scope, want true")
	}
}

func TestPublishManagedDoltRuntimeStateSkipsWhenBackendIsMySQL(t *testing.T) {
	cityPath := t.TempDir()
	writeMySQLScopeFixture(t, cityPath)
	if err := writeDoltRuntimeStateFile(providerManagedDoltStatePath(cityPath), doltRuntimeState{
		Running:   true,
		PID:       os.Getpid(),
		Port:      33123,
		DataDir:   filepath.Join(cityPath, ".beads", "dolt"),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	if err := publishManagedDoltRuntimeState(cityPath); err != nil {
		t.Fatalf("publishManagedDoltRuntimeState: %v", err)
	}
	if _, err := os.Stat(managedDoltStatePath(cityPath)); !os.IsNotExist(err) {
		t.Fatalf("published managed Dolt state should not exist for a mysql city, stat err = %v", err)
	}
}
