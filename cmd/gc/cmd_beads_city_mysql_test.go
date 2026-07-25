package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

func writeForkShapedMySQLMetadata(t *testing.T, scopeRoot, database string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(scopeRoot, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"backend":"mysql","database":"beads.db","mysql_host":"127.0.0.1","mysql_port":"3306","mysql_user":"root","mysql_database":%q}`, database)
	if err := os.WriteFile(filepath.Join(scopeRoot, ".beads", "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readScopeMetadataMap(t *testing.T, scopeRoot string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(scopeRoot, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	return meta
}

func TestDoBeadsCityUseMySQLConvertsForkShapedCityAndRigScopes(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := filepath.Join(t.TempDir(), "kit")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`[workspace]
name = "test-city"

[[rigs]]
name = "kit"
path = %q
prefix = "kit"
`, rigDir)
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	writeForkShapedMySQLMetadata(t, cityDir, "anthony_beads")
	writeForkShapedMySQLMetadata(t, rigDir, "kit_beads")

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMySQL(fsys.OSFS{}, cityDir, cityMySQLOptions{
		DSN: "root@tcp(127.0.0.1:3306)/",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doBeadsCityUseMySQL() = %d, stderr = %s", code, stderr.String())
	}

	for scope, wantDB := range map[string]string{cityDir: "anthony_beads", rigDir: "kit_beads"} {
		meta := readScopeMetadataMap(t, scope)
		if got := meta["backend"]; got != "mysql" {
			t.Fatalf("%s backend = %v, want mysql", scope, got)
		}
		if got := meta["mysql_dsn"]; got != "root@tcp(127.0.0.1:3306)/" {
			t.Fatalf("%s mysql_dsn = %v, want DSN", scope, got)
		}
		if got := meta["mysql_database"]; got != wantDB {
			t.Fatalf("%s mysql_database = %v, want %q", scope, got, wantDB)
		}
		for _, legacy := range []string{"mysql_host", "mysql_port", "mysql_user", "mysql_password"} {
			if _, ok := meta[legacy]; ok {
				t.Fatalf("%s metadata retains fork-legacy key %q", scope, legacy)
			}
		}
	}
}

func TestDoBeadsCityUseMySQLDryRunWritesNothing(t *testing.T) {
	cityDir := t.TempDir()
	writeForkShapedMySQLMetadata(t, cityDir, "anthony_beads")
	before := readScopeMetadataMap(t, cityDir)

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMySQL(fsys.OSFS{}, cityDir, cityMySQLOptions{
		DSN:    "root@tcp(127.0.0.1:3306)/",
		DryRun: true,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doBeadsCityUseMySQL() = %d, stderr = %s", code, stderr.String())
	}

	after := readScopeMetadataMap(t, cityDir)
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("dry-run mutated metadata: before=%v after=%v", before, after)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("anthony_beads")) {
		t.Fatalf("dry-run output should list the planned database, got: %s", stdout.String())
	}
}

func TestDoBeadsCityUseMySQLRequiresDSN(t *testing.T) {
	cityDir := t.TempDir()
	writeForkShapedMySQLMetadata(t, cityDir, "anthony_beads")

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMySQL(fsys.OSFS{}, cityDir, cityMySQLOptions{}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("doBeadsCityUseMySQL() = 0 without --dsn, want failure")
	}
}

func TestDoBeadsCityUseMySQLFailsWhenScopeHasNoDatabase(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"backend":"dolt","database":"dolt","dolt_mode":"server"}`
	if err := os.WriteFile(filepath.Join(cityDir, ".beads", "metadata.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := doBeadsCityUseMySQL(fsys.OSFS{}, cityDir, cityMySQLOptions{
		DSN: "root@tcp(127.0.0.1:3306)/",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("doBeadsCityUseMySQL() = 0 for scope without any database name, want failure")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("database")) {
		t.Fatalf("stderr should mention the missing database, got: %s", stderr.String())
	}
}
