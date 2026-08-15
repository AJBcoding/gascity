package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	slingcore "github.com/gastownhall/gascity/internal/sling"
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

type recordingRouteMutationStore struct {
	beads.Store
	updates     []beads.UpdateOpts
	setMetadata []string
	updateErr   error
}

func (s *recordingRouteMutationStore) Update(id string, opts beads.UpdateOpts) error {
	s.updates = append(s.updates, cloneRouteUpdateOpts(opts))
	if s.updateErr != nil {
		return s.updateErr
	}
	return s.Store.Update(id, opts)
}

func (s *recordingRouteMutationStore) SetMetadata(id, key, value string) error {
	s.setMetadata = append(s.setMetadata, key+"="+value)
	return s.Store.SetMetadata(id, key, value)
}

func cloneRouteUpdateOpts(opts beads.UpdateOpts) beads.UpdateOpts {
	cloned := opts
	if opts.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(opts.Metadata))
		for k, v := range opts.Metadata {
			cloned.Metadata[k] = v
		}
	}
	return cloned
}

func TestCliBeadRouterBuiltInRouteAppliesRouteContractAtomically(t *testing.T) {
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "alpha", Path: "/alpha"}},
		Agents: []config.Agent{{
			Name:              "refinery",
			Dir:               "alpha",
			MaxActiveSessions: intPtr(1),
		}},
		NamedSessions: []config.NamedSession{{
			Template: "refinery",
			Dir:      "alpha",
			Mode:     "always",
		}},
	}
	mem := beads.NewMemStore()
	bead, err := mem.Create(beads.Bead{Title: "route atomically", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	store := &recordingRouteMutationStore{Store: mem}
	deps := slingDeps{CityName: "test-city", CityPath: "/city", Cfg: cfg, Store: store}
	router := cliBeadRouter{deps: &deps}

	err = router.Route(context.Background(), slingcore.RouteRequest{
		BeadID:   bead.ID,
		Target:   "alpha/refinery",
		Assignee: "alpha/refinery",
		Metadata: map[string]string{
			"branch": "polecat/FE-9",
			"target": "release/1.2",
		},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(store.setMetadata) != 0 {
		t.Fatalf("SetMetadata calls = %v, want none; routing contract must be one Update", store.setMetadata)
	}
	if len(store.updates) != 1 {
		t.Fatalf("Update calls = %d, want 1", len(store.updates))
	}
	update := store.updates[0]
	if update.Assignee == nil || *update.Assignee != "alpha/refinery" {
		t.Fatalf("Update.Assignee = %v, want alpha/refinery", update.Assignee)
	}
	for key, want := range map[string]string{
		beadmeta.RoutedToMetadataKey: "alpha/refinery",
		"branch":                     "polecat/FE-9",
		"target":                     "release/1.2",
	} {
		if got := update.Metadata[key]; got != want {
			t.Fatalf("Update.Metadata[%q] = %q, want %q; full update=%#v", key, got, want, update.Metadata)
		}
	}
	got, getErr := store.Get(bead.ID)
	if getErr != nil {
		t.Fatalf("Get(%s): %v", bead.ID, getErr)
	}
	if got.Assignee != "alpha/refinery" {
		t.Fatalf("Assignee = %q, want alpha/refinery", got.Assignee)
	}
	for key, want := range map[string]string{
		beadmeta.RoutedToMetadataKey: "alpha/refinery",
		"branch":                     "polecat/FE-9",
		"target":                     "release/1.2",
	} {
		if got := got.Metadata[key]; got != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestCliBeadRouterBuiltInRouteUpdateFailureLeavesNoPartialState(t *testing.T) {
	mem := beads.NewMemStore()
	bead, err := mem.Create(beads.Bead{Title: "route atomically", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updateErr := errors.New("update failed")
	store := &recordingRouteMutationStore{Store: mem, updateErr: updateErr}
	deps := slingDeps{CityName: "test-city", CityPath: "/city", Cfg: &config.City{Workspace: config.Workspace{Name: "test-city"}}, Store: store}
	router := cliBeadRouter{deps: &deps}

	err = router.Route(context.Background(), slingcore.RouteRequest{
		BeadID:   bead.ID,
		Target:   "refinery",
		Assignee: "refinery",
		Metadata: map[string]string{
			"branch": "polecat/FE-9",
			"target": "release/1.2",
		},
	})

	if !errors.Is(err, updateErr) {
		t.Fatalf("Route error = %v, want %v", err, updateErr)
	}
	if len(store.setMetadata) != 0 {
		t.Fatalf("SetMetadata calls = %v, want none before failing Update", store.setMetadata)
	}
	got, getErr := store.Get(bead.ID)
	if getErr != nil {
		t.Fatalf("Get(%s): %v", bead.ID, getErr)
	}
	if got.Assignee != "" {
		t.Fatalf("Assignee = %q, want unchanged empty", got.Assignee)
	}
	for _, key := range []string{beadmeta.RoutedToMetadataKey, "branch", "target"} {
		if got.Metadata[key] != "" {
			t.Fatalf("metadata[%q] = %q, want unset after failed atomic route", key, got.Metadata[key])
		}
	}
}
