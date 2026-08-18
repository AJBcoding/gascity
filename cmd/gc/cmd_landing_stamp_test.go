package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/workstamp"
)

const landingStampEventID = "gcl-dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

type cliStampResolver struct {
	stores map[string]beads.Store
	calls  []string
}

func (r *cliStampResolver) Resolve(_ context.Context, storeRef string) (beads.Store, error) {
	r.calls = append(r.calls, storeRef)
	return r.stores[storeRef], nil
}

func seedCLIStampStore(t *testing.T, commit string) *beads.MemStore {
	t.Helper()
	store := beads.NewMemStore()
	store.HonorExplicitIDs = true
	if _, err := store.Create(beads.Bead{
		ID:       "gc-same",
		Title:    "work",
		Metadata: beads.StringMap{"gc.work_commit": commit},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func seedCLIStampJournal(t *testing.T) *events.Fake {
	t.Helper()
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           landingStampEventID,
		Repository:        "https://example.invalid/repo.git",
		TargetRef:         "refs/heads/main",
		ObservedLandedSHA: strings.Repeat("c", 40),
		VerifiedAt:        "2026-08-16T22:00:00Z",
		WorkRecords: []events.DeliveryWorkRecordRef{
			{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: strings.Repeat("a", 40)},
			{StoreRef: "rig:beta", BeadID: "gc-same", WorkCommit: strings.Repeat("b", 40)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{Type: events.DeliveryLanded, Subject: landingStampEventID, Payload: payload})
	return journal
}

func installLandingStampCLIDependencies(
	t *testing.T, provider events.Provider, resolver workstamp.StoreResolver,
) {
	t.Helper()
	previousProvider := landingOpenEventsProvider
	previousResolver := landingNewStampResolver
	landingOpenEventsProvider = func(io.Writer, string) (events.Provider, int) { return provider, 0 }
	landingNewStampResolver = func(io.Writer) (workstamp.StoreResolver, error) { return resolver, nil }
	t.Cleanup(func() {
		landingOpenEventsProvider = previousProvider
		landingNewStampResolver = previousResolver
	})
}

func TestLandingStampCLIStampsExactStoresAndReportsReplay(t *testing.T) {
	alpha := seedCLIStampStore(t, strings.Repeat("a", 40))
	beta := seedCLIStampStore(t, strings.Repeat("b", 40))
	resolver := &cliStampResolver{stores: map[string]beads.Store{"rig:alpha": alpha, "rig:beta": beta}}
	provider := seedCLIStampJournal(t)
	installLandingStampCLIDependencies(t, provider, resolver)

	stdout, stderr, err := runLandingCLI(t, "landing", "stamp", "--event", landingStampEventID, "--json")
	if err != nil {
		t.Fatalf("Execute: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	var first landingStampJSONResult
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatalf("JSON %q: %v", stdout, err)
	}
	if first.SchemaVersion != "1" || !first.OK || first.EventID != landingStampEventID ||
		first.Stamped != 2 || first.AlreadyStamped != 0 || len(first.Conflicts) != 0 {
		t.Fatalf("first = %#v", first)
	}
	if strings.Join(resolver.calls, ",") != "rig:alpha,rig:beta" {
		t.Fatalf("resolved stores = %v", resolver.calls)
	}

	stdout, stderr, err = runLandingCLI(t, "landing", "stamp", "--event", landingStampEventID, "--json")
	if err != nil {
		t.Fatalf("replay Execute: %v stderr=%q", err, stderr)
	}
	var replay landingStampJSONResult
	if err := json.Unmarshal([]byte(stdout), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Stamped != 0 || replay.AlreadyStamped != 2 || len(replay.Conflicts) != 0 {
		t.Fatalf("replay = %#v", replay)
	}
}

func TestLandingStampCLIConflictEmitsOneFailureJSONAndExitsNonzero(t *testing.T) {
	alpha := seedCLIStampStore(t, strings.Repeat("f", 40))
	beta := seedCLIStampStore(t, strings.Repeat("b", 40))
	resolver := &cliStampResolver{stores: map[string]beads.Store{"rig:alpha": alpha, "rig:beta": beta}}
	installLandingStampCLIDependencies(t, seedCLIStampJournal(t), resolver)

	stdout, stderr, err := runLandingCLI(t, "landing", "stamp", "--event", landingStampEventID, "--json")
	if err == nil {
		t.Fatal("Execute error = nil")
	}
	if stderr != "" {
		t.Fatalf("JSON failure wrote stderr: %q", stderr)
	}
	if strings.Count(strings.TrimSpace(stdout), "\n") != 0 {
		t.Fatalf("stdout contains more than one JSON value: %q", stdout)
	}
	var result landingStampJSONResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("JSON %q: %v", stdout, decodeErr)
	}
	if result.OK || result.Stamped != 1 || len(result.Conflicts) != 1 ||
		result.Conflicts[0].Code != "work_commit_mismatch" {
		t.Fatalf("result = %#v", result)
	}
}

func TestLandingStampCLIRejectsNonExactEventIDBeforeOpeningStores(t *testing.T) {
	previousResolver := landingNewStampResolver
	landingNewStampResolver = func(io.Writer) (workstamp.StoreResolver, error) {
		t.Fatal("resolver called for invalid event ID")
		return nil, nil
	}
	t.Cleanup(func() { landingNewStampResolver = previousResolver })

	stdout, stderr, err := runLandingCLI(t, "landing", "stamp", "--event", "gcl-dddd", "--json")
	if err == nil {
		t.Fatal("Execute error = nil")
	}
	if stderr != "" {
		t.Fatalf("JSON failure wrote stderr: %q", stderr)
	}
	var result landingStampJSONResult
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("JSON %q: %v", stdout, decodeErr)
	}
	if result.OK || result.Error != "--event must be an exact gcl- ID" {
		t.Fatalf("result = %#v", result)
	}
}

func TestLandingStampCLIUsesStockFileBackedRigStores(t *testing.T) {
	configureIsolatedRuntimeEnv(t)
	t.Setenv("GC_BEADS", "file")
	cityDir := t.TempDir()
	alphaDir := filepath.Join(cityDir, "alpha")
	betaDir := filepath.Join(cityDir, "beta")
	for _, dir := range []string{alphaDir, betaDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeProviderAwareTestCity(t, cityDir, `[workspace]
name = "stamp-city"
[[rigs]]
name = "alpha"
prefix = "A"
[[rigs]]
name = "beta"
prefix = "B"
`+builtinImportsTOML("core"))
	writeBuiltinImportsLock(t, cityDir, "core")
	if err := os.WriteFile(filepath.Join(cityDir, ".gc", "site.toml"), []byte(`workspace_name = "stamp-city"
[[rig]]
name = "alpha"
path = "alpha"
[[rig]]
name = "beta"
path = "beta"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureScopedFileStoreLayout(cityDir); err != nil {
		t.Fatal(err)
	}
	for dir, commit := range map[string]string{
		alphaDir: strings.Repeat("a", 40),
		betaDir:  strings.Repeat("b", 40),
	} {
		if err := ensurePersistedScopeLocalFileStore(dir); err != nil {
			t.Fatal(err)
		}
		store, err := openScopeLocalFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		store.HonorExplicitIDs = true
		if _, err := store.Create(beads.Bead{
			ID: "gc-same", Title: "work", Metadata: beads.StringMap{"gc.work_commit": commit},
		}); err != nil {
			t.Fatal(err)
		}
	}
	chdirProviderAwareTest(t, cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)
	provider := seedCLIStampJournal(t)
	previousProvider := landingOpenEventsProvider
	previousResolver := landingNewStampResolver
	landingOpenEventsProvider = func(io.Writer, string) (events.Provider, int) { return provider, 0 }
	landingNewStampResolver = newCityLandingStampResolver
	t.Cleanup(func() {
		landingOpenEventsProvider = previousProvider
		landingNewStampResolver = previousResolver
	})

	stdout, stderr, err := runLandingCLI(t, "landing", "stamp", "--event", landingStampEventID, "--json")
	if err != nil {
		t.Fatalf("Execute: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	var result landingStampJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Stamped != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, dir := range []string{alphaDir, betaDir} {
		store, err := openScopeLocalFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		bead, err := store.Get("gc-same")
		if err != nil {
			t.Fatal(err)
		}
		if bead.Metadata["gc.delivery_event_id"] != landingStampEventID {
			t.Fatalf("%s delivery metadata = %#v", dir, bead.Metadata)
		}
	}
}

func TestCityLandingStampResolverStampsAuthoritativeClassRow(t *testing.T) {
	cityPath, classStore := foreignProviderCity(t)
	commit := strings.Repeat("a", 40)
	bead := classResidentWorkShapedBead(t, classStore, "gc-classstamp", "class work")
	if err := classStore.Update(bead.ID, beads.UpdateOpts{
		Metadata: beads.StringMap{"gc.work_commit": commit},
	}); err != nil {
		t.Fatal(err)
	}
	chdirProviderAwareTest(t, cityPath)
	t.Setenv("GC_CITY_PATH", cityPath)

	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           landingStampEventID,
		Repository:        "https://example.invalid/repo.git",
		TargetRef:         "refs/heads/main",
		ObservedLandedSHA: strings.Repeat("c", 40),
		VerifiedAt:        "2026-08-16T22:00:00Z",
		WorkRecords: []events.DeliveryWorkRecordRef{{
			StoreRef: "class:gmnos", BeadID: bead.ID, WorkCommit: commit,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := events.NewFake()
	provider.Record(events.Event{Type: events.DeliveryLanded, Subject: landingStampEventID, Payload: payload})
	previousProvider := landingOpenEventsProvider
	previousResolver := landingNewStampResolver
	landingOpenEventsProvider = func(io.Writer, string) (events.Provider, int) { return provider, 0 }
	landingNewStampResolver = newCityLandingStampResolver
	t.Cleanup(func() {
		landingOpenEventsProvider = previousProvider
		landingNewStampResolver = previousResolver
	})

	stdout, stderr, err := runLandingCLI(t, "landing", "stamp", "--event", landingStampEventID, "--json")
	if err != nil {
		t.Fatalf("Execute: %v stdout=%q stderr=%q", err, stdout, stderr)
	}
	after, err := classStore.Get(bead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Metadata["gc.delivery_event_id"]; got != landingStampEventID {
		t.Fatalf("authoritative class delivery event = %q, want %q", got, landingStampEventID)
	}
	if violations := workstamp.ValidateLandingEvidence(provider, "class:gmnos", after); len(violations) != 0 {
		t.Fatalf("class landing evidence violations = %v", violations)
	}
}

// A class ref is read back from the TOPOLOGY the census spelled it with, never
// from a second reading of the routes. The two halves a re-derivation gets
// separately — one class's store, and a ref rebuilt from every class the routes
// carry — are pinned here as one answer: the served ref resolves to the binding
// itself, and a ref this city serves no binding for is refused by name.
func TestCityLandingStampResolverAnswersClassRefsFromTheTopology(t *testing.T) {
	cityPath, classStore := foreignProviderCity(t)
	chdirProviderAwareTest(t, cityPath)
	t.Setenv("GC_CITY_PATH", cityPath)

	resolver, err := newCityLandingStampResolver(io.Discard)
	if err != nil {
		t.Fatalf("building the resolver: %v", err)
	}
	store, err := resolver.Resolve(context.Background(), "class:gmnos")
	if err != nil {
		t.Fatalf("resolving the ref this city serves: %v", err)
	}
	if store != classStore {
		t.Fatalf("class:gmnos resolved to %T, want the binding the routes opened", store)
	}
	if _, err := resolver.Resolve(context.Background(), "class:g"); err == nil {
		t.Fatal("a class ref naming no binding this city serves resolved to a store")
	} else if !strings.Contains(err.Error(), "class:gmnos") {
		t.Fatalf("the refusal must name what this city does serve: %v", err)
	}
}
