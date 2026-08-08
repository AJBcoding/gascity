package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/fsys"
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

	got, err := scopeUsesExternalServerBackendForInit(cityPath, rigPath)
	if err != nil {
		t.Fatalf("scopeUsesExternalServerBackendForInit: %v", err)
	}
	if !got {
		t.Fatal("scopeUsesExternalServerBackendForInit = false for a fresh rig on a mysql city, want true")
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

func TestInheritedExternalScopeMetadataDeclinesPostgresCity(t *testing.T) {
	// Postgres scopes keep today's behavior: gc writes no derived metadata,
	// because the per-scope PG database and credentials are not gc's to mint.
	city := contract.MetadataState{
		Database:         "beads.db",
		Backend:          "postgres",
		PostgresHost:     "db.example.test",
		PostgresPort:     "5432",
		PostgresUser:     "beads",
		PostgresDatabase: "hq_beads",
	}
	if _, ok := inheritedExternalScopeMetadata(city, "gas"); ok {
		t.Fatal("inheritedExternalScopeMetadata ok = true for postgres; want no derived write")
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

// writePostgresBackendCity materializes a city whose ledger is Postgres-backed.
func writePostgresBackendCity(t *testing.T, cityPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"test-city\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := `{"database":"beads.db","backend":"postgres","postgres_host":"db.example.test",` +
		`"postgres_port":"5432","postgres_user":"beads","postgres_database":"hq_beads"}`
	if err := os.WriteFile(scopeMetadataJSONPath(cityPath), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFreshPostgresRigInheritsCityBindingRatherThanNone pins what a fresh rig
// on a Postgres city actually resolves to, because it is easy to read the
// declining arm of inheritedExternalScopeMetadata and conclude such a rig is
// left with no store binding at all. It is not: writing no metadata.json is
// precisely what makes the scope uninitialized, and an uninitialized scope
// inherits the city's binding by definition (bd_env.go's !ok arm).
//
// So the Postgres gap is NOT "bd has nothing to open" — it is that the rig
// shares the city's database instead of getting its own, where a MySQL rig
// gets <prefix>_beads (gas-4cu). Whether Postgres should match that is a
// design decision; silently failing the add on this path is not the fix, and
// would break the supported inherited-city Postgres rig.
func TestFreshPostgresRigInheritsCityBindingRatherThanNone(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	cityPath := t.TempDir()
	writePostgresBackendCity(t, cityPath)
	rigPath := t.TempDir() // brand new rig: no .beads at all

	// gc correctly declines to invent a per-rig binding...
	if err := ensureInheritedExternalScopeMetadata(fsys.OSFS{}, cityPath, rigPath, "gas"); err != nil {
		t.Fatalf("ensureInheritedExternalScopeMetadata on a postgres city: %v", err)
	}
	if _, ok, err := contract.LoadMetadataState(fsys.OSFS{}, scopeMetadataJSONPath(rigPath)); err != nil {
		t.Fatalf("LoadMetadataState: %v", err)
	} else if ok {
		t.Error("a metadata.json was derived for a postgres rig; gc has no credential to mint that database")
	}

	// ...and the rig still resolves a store: the city's.
	meta, ok, err := externalBackendMetadataForScope(cityPath, rigPath)
	if err != nil {
		t.Fatalf("externalBackendMetadataForScope: %v", err)
	}
	if !ok {
		t.Fatal("a fresh rig on a postgres city resolved no backend; it must inherit the city's")
	}
	if meta.Backend != "postgres" {
		t.Errorf("Backend = %q, want postgres", meta.Backend)
	}
	if meta.PostgresDatabase != "hq_beads" {
		t.Errorf("PostgresDatabase = %q, want the city's hq_beads (the shared-database gap, gas-c2t)", meta.PostgresDatabase)
	}
}
