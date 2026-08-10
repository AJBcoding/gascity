package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
)

// ── gas-4cu: rig add on an external-server (mysql) city ──────────────────
//
// A city whose .beads/metadata.json declares an external server backend owns
// no managed Dolt. Adding a rig to it must derive the new rig's store from the
// city's active backend instead of assuming Dolt, and must never need a Dolt
// server to be reachable.

const (
	testMySQLCityDSN      = "root@tcp(127.0.0.1:3306)/"
	testMySQLCityDatabase = "anthony_beads"
)

// writeExternalBackendCity materializes a bd-contract city whose metadata
// declares the mysql backend, mirroring a real cut-over city on disk.
func writeExternalBackendCity(t *testing.T, cityPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"test-city\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"database":"beads.db","backend":"mysql","mysql_dsn":"` + testMySQLCityDSN + `","mysql_database":"` + testMySQLCityDatabase + `"}`
	if err := os.WriteFile(scopeMetadataJSONPath(cityPath), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExternalBackendMetadataForScopeInheritsCityForUninitializedRig(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	cityPath := t.TempDir()
	writeExternalBackendCity(t, cityPath)
	rigPath := t.TempDir() // brand new rig: no .beads at all

	meta, ok, err := externalBackendMetadataForScope(cityPath, rigPath)
	if err != nil {
		t.Fatalf("externalBackendMetadataForScope: %v", err)
	}
	if !ok {
		t.Fatal("externalBackendMetadataForScope ok = false; a fresh rig under a mysql city inherits the city backend")
	}
	if meta.Backend != "mysql" || meta.MySQLDSN != testMySQLCityDSN {
		t.Fatalf("meta = %+v, want the city's mysql binding", meta)
	}
}

func TestExternalBackendMetadataForScopeUninitializedRigUnderDoltCityIsNotExternal(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scopeMetadataJSONPath(cityPath), []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"server","dolt_database":"hq"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rigPath := t.TempDir()

	_, ok, err := externalBackendMetadataForScope(cityPath, rigPath)
	if err != nil {
		t.Fatalf("externalBackendMetadataForScope: %v", err)
	}
	if ok {
		t.Fatal("externalBackendMetadataForScope ok = true under a dolt city; managed Dolt must stay the default")
	}
}

func TestScopeUsesExternalServerBackendForInitForFreshRigOnMySQLCity(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	cityPath := t.TempDir()
	writeExternalBackendCity(t, cityPath)
	rigPath := t.TempDir()

	got, err := scopeSkipsManagedDoltForInit(cityPath, rigPath)
	if err != nil {
		t.Fatalf("scopeSkipsManagedDoltForInit: %v", err)
	}
	if !got {
		t.Fatal("scopeSkipsManagedDoltForInit = false for a fresh rig on a mysql city, want true")
	}
}

func TestManagedDoltLifecycleOwnedFalseForMySQLCity(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	cityPath := t.TempDir()
	writeExternalBackendCity(t, cityPath)

	owned, err := managedDoltLifecycleOwned(cityPath)
	if err != nil {
		t.Fatalf("managedDoltLifecycleOwned: %v", err)
	}
	if owned {
		t.Fatal("managedDoltLifecycleOwned = true for a mysql city; gc manages no Dolt server there")
	}
}

func TestInheritedExternalScopeMetadataDerivesRigDatabaseFromPrefix(t *testing.T) {
	city := contract.MetadataState{
		Database:      "beads.db",
		Backend:       "mysql",
		MySQLDSN:      testMySQLCityDSN,
		MySQLDatabase: testMySQLCityDatabase,
	}
	got, ok := inheritedExternalScopeMetadata(city, "gas")
	if !ok {
		t.Fatal("inheritedExternalScopeMetadata ok = false, want a derived mysql binding")
	}
	if got.Backend != "mysql" {
		t.Errorf("Backend = %q, want mysql", got.Backend)
	}
	if got.MySQLDatabase != "gas_beads" {
		t.Errorf("MySQLDatabase = %q, want gas_beads", got.MySQLDatabase)
	}
	if got.MySQLDSN != testMySQLCityDSN {
		t.Errorf("MySQLDSN = %q, want the city DSN verbatim", got.MySQLDSN)
	}
	if got.Database != "beads.db" {
		t.Errorf("Database = %q, want beads.db", got.Database)
	}
	if got.DoltDatabase != "" || got.DoltMode != "" {
		t.Errorf("derived state carries dolt fields: %+v", got)
	}
}

func TestInheritedExternalScopeMetadataDeclinesNonMySQLBackend(t *testing.T) {
	// mysql is the only backend gc derives per-scope metadata for. Anything
	// else gets no derived write. "postgres" is the case worth naming: it was
	// a registered external backend that declined here for its own reason
	// (the PG database and credentials were never gc's to mint), and after it
	// was retired (gas-1oou) it must still decline — now because gc does not
	// recognize it at all. Same verdict, so a regression cannot hide behind
	// the change of reason.
	for _, backend := range []string{"postgres", "cockroach", ""} {
		city := contract.MetadataState{
			Database: "beads.db",
			Backend:  backend,
		}
		if _, ok := inheritedExternalScopeMetadata(city, "gas"); ok {
			t.Errorf("inheritedExternalScopeMetadata ok = true for backend %q; want no derived write", backend)
		}
	}
}

func TestInitAndHookDirWritesInheritedMySQLMetadataForFreshRig(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	cityPath := t.TempDir()
	writeExternalBackendCity(t, cityPath)
	rigPath := t.TempDir()

	// No Dolt server is running and no provider script exists: a correct
	// implementation never reaches for either.
	if err := initAndHookDir(cityPath, rigPath, "gas"); err != nil {
		t.Fatalf("initAndHookDir on a mysql city: %v", err)
	}

	raw := readScopeMetadataMap(t, rigPath)
	if raw["backend"] != "mysql" {
		t.Errorf("backend = %v, want mysql", raw["backend"])
	}
	if raw["mysql_database"] != "gas_beads" {
		t.Errorf("mysql_database = %v, want gas_beads", raw["mysql_database"])
	}
	if raw["mysql_dsn"] != testMySQLCityDSN {
		t.Errorf("mysql_dsn = %v, want the city DSN", raw["mysql_dsn"])
	}
	for _, key := range []string{"dolt_database", "dolt_mode"} {
		if v, present := raw[key]; present {
			t.Errorf("metadata carries %s = %v; a mysql rig must have no dolt keys", key, v)
		}
	}
}
