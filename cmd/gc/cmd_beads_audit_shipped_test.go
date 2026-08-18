package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/workclose"
)

func TestBeadsAuditShippedCommandIsRegistered(t *testing.T) {
	cmd := newBeadsCmd(&bytes.Buffer{}, &bytes.Buffer{})
	child, _, err := cmd.Find([]string{"audit-shipped"})
	if err != nil || child == cmd || child.Name() != "audit-shipped" {
		t.Fatalf("audit-shipped command missing: child=%v err=%v", child, err)
	}
}

func TestRenderShippedAuditProvidesOnlyExplicitRemediationChoices(t *testing.T) {
	report := workclose.AuditReport{Complete: true, Groups: []workclose.AuditGroup{{
		StoreRef: "city:test", Findings: []workclose.AuditFinding{{BeadID: "ga-bad", Status: "closed", Violations: []string{"missing landing stamp"}}},
	}}}
	var out bytes.Buffer
	if code := renderShippedAudit(report, "text", &out, &out); code == 0 {
		t.Fatal("invalid shipped record returned success")
	}
	got := out.String()
	for _, want := range []string{"city:test", "ga-bad", "gc landing stamp --event <gcl-event-id>", "reclassify the outcome"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "automatically") {
		t.Fatalf("audit implied a rewrite: %s", got)
	}
}

func TestShippedAuditLandingStampRemediationResolvesThroughCobra(t *testing.T) {
	const eventID = "gcl-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	args := shippedAuditLandingStampArgs(eventID)
	root := newRootCmd(io.Discard, io.Discard)
	resolved, remaining, err := root.Find(args)
	if err != nil {
		t.Fatalf("resolve remediation argv: %v", err)
	}
	if got := resolved.CommandPath(); got != "gc landing stamp" {
		t.Fatalf("resolved command = %q, want gc landing stamp", got)
	}
	if err := resolved.ParseFlags(remaining); err != nil {
		t.Fatalf("parse remediation flags: %v", err)
	}
	if got, err := resolved.Flags().GetString("event"); err != nil || got != eventID {
		t.Fatalf("--event = %q err=%v, want %q", got, err, eventID)
	}
}

type shippedAuditErrorWriter struct{ err error }

func (w shippedAuditErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRenderShippedAuditJSONReportsEncoderFailure(t *testing.T) {
	var stderr bytes.Buffer
	code := renderShippedAudit(workclose.AuditReport{Complete: true}, "json", shippedAuditErrorWriter{err: errors.New("disk full")}, &stderr)
	if code == 0 {
		t.Fatal("JSON encoder failure returned success")
	}
	if !strings.Contains(stderr.String(), "disk full") {
		t.Fatalf("stderr = %q, want encoder error", stderr.String())
	}
}

type shippedAuditCloseSpy struct {
	*beads.MemStore
	closes int
	err    error
}

func (s *shippedAuditCloseSpy) CloseStore() error {
	s.closes++
	return s.err
}

func TestShippedAuditOwnedStoresCloseOnceAndSurfaceErrors(t *testing.T) {
	owned := &shippedAuditCloseSpy{MemStore: beads.NewMemStore(), err: errors.New("close failed")}
	notOwned := &shippedAuditCloseSpy{MemStore: beads.NewMemStore()}
	sources := shippedAuditSourceSet{
		owned:  []shippedAuditOwnedStore{{label: "city:test", store: owned}, {label: "rig:alias", store: owned}},
		stores: []workclose.AuditStore{{StoreRef: "class:graph", Store: notOwned}},
	}
	errs := sources.closeOwned()
	if owned.closes != 1 {
		t.Fatalf("owned CloseStore calls = %d, want 1", owned.closes)
	}
	if notOwned.closes != 0 {
		t.Fatalf("topology-owned CloseStore calls = %d, want 0", notOwned.closes)
	}
	if got := strings.Join(errs, " "); !strings.Contains(got, "city:test") || !strings.Contains(got, "close failed") {
		t.Fatalf("close errors = %q, want canonical ref and cause", got)
	}
}

func TestShippedAuditSourcesUseAuthoritativeCityRigAndSplitClassStores(t *testing.T) {
	clearGCEnv(t)
	resetCLIStorageRoutes(t)
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "alpha")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cityTOML := fmt.Sprintf("[workspace]\nname = \"audit-city\"\n[beads]\nprovider = \"bd\"\n\n[[rigs]]\nname = \"alpha\"\npath = %q\n", rigPath)
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	seedAuditFileStore(t, cityPath, "city-row")
	seedAuditFileStore(t, rigPath, "rig-row")

	classStore := beads.NewMemStore()
	mustCreateAuditRow(t, classStore, "class-row")
	routeStores := make(map[coordclass.Class]beads.Store)
	for _, class := range coordclass.Classes() {
		if class.IsInfrastructure() {
			routeStores[class] = classStore
		}
	}
	entry := cliStorageRoutesEntryFor(filepath.Clean(cityPath))
	entry.once.Do(func() { entry.routes = &storageRoutes{stores: routeStores, binding: "infra"} })

	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityPath, "city.toml"))
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	// The ambient provider conflicts with every physical store marker. An audit
	// that reuses ordinary command-context resolution attempts bd and misses the
	// real file ledgers; authoritative resolution must still census all three.
	t.Setenv("GC_BEADS", "bd")
	sources := shippedAuditSources(cityPath, cfg)
	t.Cleanup(func() { _ = sources.closeOwned() })
	if len(sources.errors) != 0 {
		t.Fatalf("source errors = %v", sources.errors)
	}
	seen := map[string]string{}
	for _, source := range sources.stores {
		rows, listErr := source.Store.List(beads.ListQuery{AllowScan: true, IncludeClosed: true, Live: true})
		if listErr != nil {
			t.Fatalf("list %s: %v", source.StoreRef, listErr)
		}
		for _, row := range rows {
			seen[row.Title] = source.StoreRef
		}
	}
	for _, id := range []string{"city-row", "rig-row", "class-row"} {
		if seen[id] == "" {
			t.Errorf("authoritative census missed %s; rows=%v", id, seen)
		}
	}
	if !strings.HasPrefix(seen["city-row"], "city:") || !strings.HasPrefix(seen["rig-row"], "rig:") || !strings.HasPrefix(seen["class-row"], "class:") {
		t.Fatalf("canonical refs = %v, want city/rig/class grouping", seen)
	}
}

// The refused-city branch. A plan the resolver declines to build must still
// leave an audit that NAMES every binding it could not reach — dropping them
// would report a clean city — and it must name them with no handle: the audit
// never reads a source carrying an open error, so passing the store anyway
// would take a probe out of a plan the resolver had just refused.
func TestShippedAuditSourcesNameEveryBindingARefusedPlanCouldNotReach(t *testing.T) {
	clearGCEnv(t)
	resetCLIStorageRoutes(t)
	resetCLIResidencyBindings()
	t.Cleanup(resetCLIResidencyBindings)
	cityPath := t.TempDir()
	cityTOML := "[workspace]\nname = \"audit-city\"\n[beads]\nprovider = \"bd\"\n"
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	seedAuditFileStore(t, cityPath, "city-row")
	entry := cliStorageRoutesEntryFor(filepath.Clean(cityPath))
	entry.once.Do(func() { entry.routes = refusingStorageRoutes("infra", errStorageRefusedForTest{}) })

	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityPath, "city.toml"))
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	t.Setenv("GC_BEADS", "bd")
	sources := shippedAuditSources(cityPath, cfg)
	t.Cleanup(func() { _ = sources.closeOwned() })
	if !strings.Contains(strings.Join(sources.errors, "; "), "authoritative store topology") {
		t.Fatalf("a refused plan reported errors %v, want the topology failure", sources.errors)
	}
	found := false
	for _, source := range sources.stores {
		if !strings.HasPrefix(source.StoreRef, "class:") {
			continue
		}
		found = true
		if source.OpenError == nil {
			t.Errorf("%s was reported as readable on a refused city", source.StoreRef)
		}
		if source.Store != nil {
			t.Errorf("%s was handed a store handle out of a plan the resolver refused", source.StoreRef)
		}
	}
	if !found {
		t.Fatalf("the refused binding was dropped from the audit: %+v", sources.stores)
	}
}

func seedAuditFileStore(t *testing.T, root, id string) {
	t.Helper()
	store, err := beads.OpenFileStore(fsys.OSFS{}, filepath.Join(root, ".gc", "beads.json"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	mustCreateAuditRow(t, store, id)
}

func mustCreateAuditRow(t *testing.T, store beads.Store, id string) {
	t.Helper()
	if _, err := store.Create(beads.Bead{ID: id, Title: id, Type: "task", Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
}
