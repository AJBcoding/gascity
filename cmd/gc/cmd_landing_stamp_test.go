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
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/storeref"
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

// The end-to-end row, over a city whose binding is really open: the served ref
// resolves to that binding, and a ref this city serves no binding for is refused
// by name.
//
// It is the WHOLE SPLIT, which is the one arrangement where the topology's ref
// and a ref rebuilt from the raw route keys agree — so it pins the happy path
// and the refusal sentence, and it pins no mismatch. The two rows that do are
// below: the class sets part company only when routes.stores carries a class the
// bindings do not, or when the bindings are more than one.
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

// landingStampResolverForCity builds the resolver directly over a city path.
//
// The class arm reads r.cityPath and r.cfg and nothing else — no city name, no
// rig path, no opened scope store — so a test of that arm alone skips the city
// discovery and config load newCityLandingStampResolver performs, and the
// fixture is the seeded route map rather than a directory tree.
func landingStampResolverForCity(cityPath string) *cityLandingStampResolver {
	return &cityLandingStampResolver{
		cityPath: cityPath,
		cityName: "test-city",
		cfg:      residencyTestConfig(),
		stores:   map[string]beads.Store{},
	}
}

// The route map and the binding set derived FROM it are different sets, and the
// ref a class arm answers has to come from the second.
//
// residencyBindingsFromRoutes walks coordclass.Classes() and skips every class
// that is not infrastructure. storage_boot's openStorageRoutes keys routes.stores
// straight off planned.AssignedClasses.Classes(), with no such filter. So a route
// for ClassWork — the one non-infrastructure class — sits in routes.stores and in
// no binding, and the two derivations then spell DIFFERENT REFS over the same
// routes: "class:gmnos" from the topology against "class:gmnosw" rebuilt from the
// raw keys.
//
// Both directions are pinned, because a ref rebuilt from the keys gets both
// wrong: it refuses the ref the census records under, and it answers a ref no
// binding on this city is spelled with.
func TestCityLandingStampResolverClassRefIgnoresANonInfrastructureRouteKey(t *testing.T) {
	cityPath := t.TempDir()
	binding, workRouted := beads.NewMemStore(), beads.NewMemStore()
	routes := &storageRoutes{stores: map[coordclass.Class]beads.Store{coordclass.ClassWork: workRouted}}
	for _, class := range infrastructureClasses() {
		routes.stores[class] = binding
	}
	seedRoutes(t, cityPath, routes)

	// The constructor half, stated first: one binding, and the ref it is spelled
	// with. The resolver's answer below has to be that ref's leg.
	bindings, refused := residencyBindingsFromRoutes(routes)
	if refused != nil {
		t.Fatalf("a healthy binding reported a refusal: %v", refused)
	}
	if len(bindings) != 1 {
		t.Fatalf("got %d bindings, want the one store every infrastructure class routes to", len(bindings))
	}
	served := bindings[0].Leg.Ref

	// The raw-key half: the ref a rebuild over routes.stores mints. The fixture
	// exercises the mismatch only if the two disagree, so that is asserted.
	keys := make([]coordclass.Class, 0, len(routes.stores))
	for class := range routes.stores {
		keys = append(keys, class)
	}
	rebuilt := storeref.ClassRef(keys)
	if rebuilt == served {
		t.Fatalf("both derivations spell %q over this fixture, so it exercises no mismatch", served)
	}

	resolver := landingStampResolverForCity(cityPath)
	store, err := resolver.Resolve(context.Background(), string(served))
	if err != nil {
		t.Fatalf("resolving %q, the ref this city's binding is spelled with: %v", served, err)
	}
	if store != binding {
		t.Fatalf("%q resolved to %T, want the binding the routes opened", served, store)
	}
	if got, err := resolver.Resolve(context.Background(), string(rebuilt)); err == nil {
		t.Fatalf("%q — the ref a rebuild over the raw route keys mints — resolved to %T, but no binding on this city is spelled with it", rebuilt, got)
	}
}

// The store a class ref names is the leg of the binding THAT REF spells, not
// whatever the graph class happens to route to.
//
// On a fan-out the two are different stores. Graph has a binding to itself here,
// and the other four infrastructure classes share ONE physical store — the
// aliased binding, whose ref is what four classes' beads are recorded under. An
// arm that took its store from graphClassBinding hands back the graph engine for
// that ref, and stamping landing evidence through it writes the durable record
// into a store that does not hold the bead.
func TestCityLandingStampResolverClassRefNamesTheAliasedBindingNotTheGraphStore(t *testing.T) {
	cityPath := t.TempDir()
	graphOnly, shared := beads.NewMemStore(), beads.NewMemStore()
	routes := &storageRoutes{stores: map[coordclass.Class]beads.Store{coordclass.ClassGraph: graphOnly}}
	for _, class := range infrastructureClasses() {
		if class != coordclass.ClassGraph {
			routes.stores[class] = shared
		}
	}
	seedRoutes(t, cityPath, routes)

	bindings, _ := residencyBindingsFromRoutes(routes)
	refs := make([]storeref.StoreRef, 0, len(bindings))
	for _, b := range bindings {
		refs = append(refs, b.Leg.Ref)
	}
	if len(refs) != 2 || refs[0] != "class:g" || refs[1] != "class:mnos" {
		t.Fatalf("the fan-out is spelled %v, want [class:g class:mnos]", refs)
	}

	resolver := landingStampResolverForCity(cityPath)
	store, err := resolver.Resolve(context.Background(), "class:mnos")
	if err != nil {
		t.Fatalf("resolving class:mnos, the ref the aliased binding is spelled with: %v", err)
	}
	if store == graphOnly {
		t.Fatal("class:mnos resolved to the GRAPH class's store: the ref names one binding and the store is another's")
	}
	if store != shared {
		t.Fatalf("class:mnos resolved to %T, want the one store its four classes share", store)
	}
	// The control: the graph binding still answers to its OWN ref, so the rows
	// above assert which leg came back rather than a resolver that lost one.
	if graphStore, err := resolver.Resolve(context.Background(), "class:g"); err != nil || graphStore != graphOnly {
		t.Fatalf("class:g resolved to (%T, %v), want the graph binding and no error", graphStore, err)
	}
}
