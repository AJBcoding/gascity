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
	env := map[string]string{
		"GC_BEADS_BACKEND":        "doltlite",
		"BEADS_BACKEND":           "doltlite",
		"BEADS_DOLT_SERVER_HOST":  "127.0.0.1",
		"BEADS_POSTGRES_HOST":     "db.example.test",
		"BEADS_POSTGRES_PASSWORD": "swordfish",
		"GC_POSTGRES_PASSWORD":    "swordfish",
		"UNRELATED":               "keep",
	}
	applyResolvedScopeMySQLEnv(env)

	if got := env["UNRELATED"]; got != "keep" {
		t.Fatalf("UNRELATED = %q, want preserved", got)
	}
	for _, key := range []string{
		"GC_BEADS_BACKEND", "BEADS_BACKEND",
		"BEADS_POSTGRES_HOST", "BEADS_POSTGRES_PASSWORD", "GC_POSTGRES_PASSWORD",
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

	usesExternal, err := scopeUsesExternalServerBackendForInit(cityPath, cityPath)
	if err != nil {
		t.Fatalf("scopeUsesExternalServerBackendForInit: %v", err)
	}
	if !usesExternal {
		t.Fatal("scopeUsesExternalServerBackendForInit = false for mysql scope, want true")
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
