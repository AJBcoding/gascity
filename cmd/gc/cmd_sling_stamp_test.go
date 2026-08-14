package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestSlingBranchTargetFlagsStampRoutedBead wires the whole CLI chain for
// gas-jyi5: cobra flag parse → RunE → cmdSlingWithJSON → SlingOpts →
// DoSling's pre-route stamp. The two flag values are deliberately distinct
// so a swapped branch/target anywhere in the positional threading fails
// loudly instead of stamping the wrong key.
func TestSlingBranchTargetFlagsStampRoutedBead(t *testing.T) {
	configureIsolatedRuntimeEnv(t)
	t.Setenv("GC_BEADS", "file")

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "frontend")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(rig): %v", err)
	}
	if err := ensureScopedFileStoreLayout(cityDir); err != nil {
		t.Fatalf("ensureScopedFileStoreLayout: %v", err)
	}
	if err := ensurePersistedScopeLocalFileStore(cityDir); err != nil {
		t.Fatalf("ensurePersistedScopeLocalFileStore(city): %v", err)
	}
	if err := ensurePersistedScopeLocalFileStore(rigDir); err != nil {
		t.Fatalf("ensurePersistedScopeLocalFileStore(rig): %v", err)
	}
	cityToml := `[workspace]
name = "demo"

[[rigs]]
name = "frontend"
path = "frontend"
prefix = "FE"

[[agent]]
name = "worker"
dir = "frontend"
`
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityToml), 0o644); err != nil {
		t.Fatalf("WriteFile(city.toml): %v", err)
	}
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	// --force and --no-convoy mirror TestCmdSlingUsesRigScopedFileStoreForBuiltInRouting:
	// the inline-created bead carries the store's default "gc" prefix while
	// living in the FE rig store, which trips the cross-rig prefix guard
	// unless forced. The stamp path under test is force-independent.
	var stdout, stderr bytes.Buffer
	cmd := newSlingCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"frontend/worker", "ship feature", "--force", "--no-convoy", "--branch", "polecat/FE-9", "--target", "release/1.2"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v; stderr: %s", err, stderr.String())
	}

	rigStore, err := openStoreAtForCity(rigDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity(rig): %v", err)
	}
	rigBeads, err := rigStore.List(beads.ListQuery{AllowScan: true, Sort: beads.SortCreatedAsc})
	if err != nil {
		t.Fatalf("rigStore.List: %v", err)
	}
	if len(rigBeads) != 1 {
		t.Fatalf("rig store bead count = %d, want 1: %#v", len(rigBeads), rigBeads)
	}
	got := rigBeads[0]
	if got.Metadata["branch"] != "polecat/FE-9" {
		t.Errorf("metadata.branch = %q, want polecat/FE-9", got.Metadata["branch"])
	}
	if got.Metadata["target"] != "release/1.2" {
		t.Errorf("metadata.target = %q, want release/1.2", got.Metadata["target"])
	}
}

// TestSlingBranchFlagRejectedWithFormula: a formula launch has no subject
// bead, so --branch/--target with --formula must fail at argument
// validation — before any city or store resolution.
func TestSlingBranchFlagRejectedWithFormula(t *testing.T) {
	var stderr bytes.Buffer
	cmd := newSlingCmd(&bytes.Buffer{}, &stderr)
	cmd.SetArgs([]string{"mayor", "code-review", "--formula", "--branch", "polecat/FE-9"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for --formula with --branch")
	}
	if !strings.Contains(stderr.String(), "--branch/--target") {
		t.Errorf("stderr = %q, want to mention --branch/--target", stderr.String())
	}
}
