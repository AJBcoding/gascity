package sling

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Direct-claim evidence: a bead in_progress with a different actor must refuse
// dispatch. This is the claim-gate from gas-cnx6 — before it, a re-sling of a
// claimed bead warned and proceeded, double-routing in-flight work (nux vs
// rictus on gas-git0).
func TestCheckInFlightDispatchRefusesClaimedBead(t *testing.T) {
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "BL-1", Type: "task", Status: "in_progress", Assignee: "other-worker"},
	}, nil)
	deps := SlingDeps{Store: store}

	err := CheckInFlightDispatch(store, "BL-1", config.Agent{Name: "worker"}, deps)
	var conflict *InFlightConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("CheckInFlightDispatch = %v, want *InFlightConflictError", err)
	}
	if conflict.BeadID != "BL-1" || conflict.Assignee != "other-worker" {
		t.Errorf("conflict = %+v, want BeadID=BL-1 Assignee=other-worker", conflict)
	}
	for _, want := range []string{"BL-1", "other-worker", "--force"} {
		if !strings.Contains(conflict.Error(), want) {
			t.Errorf("error %q does not mention %q", conflict.Error(), want)
		}
	}
}

// Non-conflicting shapes must pass: the target's own claim (idempotent repair),
// an open bead directed at another actor (existing warn-and-proceed contract),
// and an assignee-less in_progress bead (workflow-launch promotion sets
// in_progress with no assignee — see PromoteWorkflowLaunchBead).
func TestCheckInFlightDispatchAllowsNonConflictingStates(t *testing.T) {
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "OWN-1", Type: "task", Status: "in_progress", Assignee: "worker"},
		{ID: "OPEN-1", Type: "task", Status: "open", Assignee: "other-worker"},
		{ID: "PROMO-1", Type: "task", Status: "in_progress"},
	}, nil)
	deps := SlingDeps{Store: store}
	a := config.Agent{Name: "worker"}

	for _, id := range []string{"OWN-1", "OPEN-1", "PROMO-1"} {
		if err := CheckInFlightDispatch(store, id, a, deps); err != nil {
			t.Errorf("CheckInFlightDispatch(%s) = %v, want nil", id, err)
		}
	}
}

// The mayor's exact case from gas-cnx6: a convoy stays open/unowned after its
// child is claimed, so nothing gated a second pour. Dispatching the convoy
// while any tracked child is in flight with another actor must refuse.
func TestCheckInFlightDispatchRefusesConvoyWithInFlightChild(t *testing.T) {
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "CV-1", Type: "convoy", Status: "open"},
		{ID: "BL-1", Type: "task", Status: "in_progress", Assignee: "other-worker"},
	}, []beads.Dep{
		{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"},
	})
	deps := SlingDeps{Store: store}

	err := CheckInFlightDispatch(store, "CV-1", config.Agent{Name: "worker"}, deps)
	var conflict *InFlightConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("CheckInFlightDispatch = %v, want *InFlightConflictError", err)
	}
	if conflict.BeadID != "CV-1" || conflict.ChildID != "BL-1" || conflict.Assignee != "other-worker" {
		t.Errorf("conflict = %+v, want BeadID=CV-1 ChildID=BL-1 Assignee=other-worker", conflict)
	}
}

// A convoy whose children are open, claimed by the dispatch target itself, or
// closed carries no foreign in-flight work — dispatch proceeds.
func TestCheckInFlightDispatchAllowsConvoyWithoutForeignClaims(t *testing.T) {
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "CV-1", Type: "convoy", Status: "open"},
		{ID: "BL-1", Type: "task", Status: "open"},
		{ID: "BL-2", Type: "task", Status: "in_progress", Assignee: "worker"},
		{ID: "BL-3", Type: "task", Status: "closed", Assignee: "other-worker"},
	}, []beads.Dep{
		{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"},
		{IssueID: "CV-1", DependsOnID: "BL-2", Type: "tracks"},
		{IssueID: "CV-1", DependsOnID: "BL-3", Type: "tracks"},
	})
	deps := SlingDeps{Store: store}

	if err := CheckInFlightDispatch(store, "CV-1", config.Agent{Name: "worker"}, deps); err != nil {
		t.Errorf("CheckInFlightDispatch = %v, want nil", err)
	}
}

// Pour evidence, the gas-yudf shape: a live molecule/workflow root whose only
// link to the work bead is gc.input_convoy_id -> a live tracking convoy. Every
// pre-existing probe (CollectAttachedBeads, CheckNoMoleculeChildren) misses it
// because the root is neither a child of the bead nor named in its metadata.
// Dispatching the bead to a different actor must refuse — this is the re-sling
// half of the gas-cnx6 race. An unrouted root (no routed_to, no assignee) is
// still dispatch machinery driving the same work and also refuses.
func TestCheckInFlightDispatchRefusesLiveForeignRootOverTrackingConvoy(t *testing.T) {
	newStore := func(rootMeta map[string]string, rootAssignee string) *beads.MemStore {
		meta := map[string]string{beadmeta.InputConvoyIDMetadataKey: "CV-1"}
		for k, v := range rootMeta {
			meta[k] = v
		}
		return beads.NewMemStoreFrom(0, []beads.Bead{
			{ID: "BL-1", Type: "task", Status: "open"},
			{ID: "CV-1", Type: "convoy", Status: "open"},
			{ID: "MOL-1", Type: "molecule", Status: "open", Assignee: rootAssignee, Metadata: meta},
		}, []beads.Dep{
			{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"},
		})
	}

	t.Run("routed to another actor", func(t *testing.T) {
		store := newStore(map[string]string{beadmeta.RoutedToMetadataKey: "other-session"}, "")
		err := CheckInFlightDispatch(store, "BL-1", config.Agent{Name: "worker"}, SlingDeps{Store: store})
		var conflict *InFlightConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("CheckInFlightDispatch = %v, want *InFlightConflictError", err)
		}
		if conflict.BeadID != "BL-1" || conflict.RootID != "MOL-1" || conflict.ConvoyID != "CV-1" || conflict.RootTarget != "other-session" {
			t.Errorf("conflict = %+v, want BeadID=BL-1 RootID=MOL-1 ConvoyID=CV-1 RootTarget=other-session", conflict)
		}
	})

	t.Run("assigned to another actor", func(t *testing.T) {
		store := newStore(nil, "other-session")
		err := CheckInFlightDispatch(store, "BL-1", config.Agent{Name: "worker"}, SlingDeps{Store: store})
		var conflict *InFlightConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("CheckInFlightDispatch = %v, want *InFlightConflictError", err)
		}
	})

	t.Run("unrouted root", func(t *testing.T) {
		store := newStore(nil, "")
		err := CheckInFlightDispatch(store, "BL-1", config.Agent{Name: "worker"}, SlingDeps{Store: store})
		var conflict *InFlightConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("CheckInFlightDispatch = %v, want *InFlightConflictError", err)
		}
	})
}

// Pour evidence that does not conflict: a root routed to the same target
// (same-dispatch duplicates stay with the existing idempotency and root-key
// machinery), a closed root, a root over a terminal convoy, and a root over a
// session-bookkeeping convoy (ready-excluded label) all pass.
func TestCheckInFlightDispatchIgnoresNonConflictingPourEvidence(t *testing.T) {
	cases := []struct {
		name   string
		beads  []beads.Bead
		deps   []beads.Dep
		target string
	}{
		{
			name: "same-target root",
			beads: []beads.Bead{
				{ID: "BL-1", Type: "task", Status: "open"},
				{ID: "CV-1", Type: "convoy", Status: "open"},
				{ID: "MOL-1", Type: "molecule", Status: "open", Metadata: map[string]string{
					beadmeta.InputConvoyIDMetadataKey: "CV-1",
					beadmeta.RoutedToMetadataKey:      "worker",
				}},
			},
			deps:   []beads.Dep{{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"}},
			target: "worker",
		},
		{
			name: "closed root",
			beads: []beads.Bead{
				{ID: "BL-1", Type: "task", Status: "open"},
				{ID: "CV-1", Type: "convoy", Status: "open"},
				{ID: "MOL-1", Type: "molecule", Status: "closed", Metadata: map[string]string{
					beadmeta.InputConvoyIDMetadataKey: "CV-1",
					beadmeta.RoutedToMetadataKey:      "other-session",
				}},
			},
			deps:   []beads.Dep{{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"}},
			target: "worker",
		},
		{
			name: "terminal convoy",
			beads: []beads.Bead{
				{ID: "BL-1", Type: "task", Status: "open"},
				{ID: "CV-1", Type: "convoy", Status: "closed"},
				{ID: "MOL-1", Type: "molecule", Status: "open", Metadata: map[string]string{
					beadmeta.InputConvoyIDMetadataKey: "CV-1",
					beadmeta.RoutedToMetadataKey:      "other-session",
				}},
			},
			deps:   []beads.Dep{{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"}},
			target: "worker",
		},
		{
			name: "ready-excluded bookkeeping convoy",
			beads: []beads.Bead{
				{ID: "BL-1", Type: "task", Status: "open"},
				{ID: "CV-1", Type: "convoy", Status: "open", Labels: []string{"gc:session"}},
				{ID: "MOL-1", Type: "molecule", Status: "open", Metadata: map[string]string{
					beadmeta.InputConvoyIDMetadataKey: "CV-1",
					beadmeta.RoutedToMetadataKey:      "other-session",
				}},
			},
			deps:   []beads.Dep{{IssueID: "CV-1", DependsOnID: "BL-1", Type: "tracks"}},
			target: "worker",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := beads.NewMemStoreFrom(0, tc.beads, tc.deps)
			deps := SlingDeps{Store: store}
			if err := CheckInFlightDispatch(store, "BL-1", config.Agent{Name: tc.target}, deps); err != nil {
				t.Errorf("CheckInFlightDispatch = %v, want nil", err)
			}
		})
	}
}

// A probe failure is not proof the bead is idle. The gate must refuse with the
// probe error rather than proceed — proceeding on an unreadable ledger
// re-opens the double-dispatch window this gate exists to close.
func TestCheckInFlightDispatchFailsClosedOnProbeError(t *testing.T) {
	mem := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "BL-1", Type: "task", Status: "open"},
	}, nil)
	probeErr := errors.New("store unavailable")
	store := depListErrStore{Store: mem, err: probeErr}
	deps := SlingDeps{Store: store}

	err := CheckInFlightDispatch(store, "BL-1", config.Agent{Name: "worker"}, deps)
	if !errors.Is(err, probeErr) {
		t.Fatalf("CheckInFlightDispatch = %v, want probe error surfaced (fail closed)", err)
	}
}

// The gate applies only to real dispatches of existing beads: formula launches
// mint fresh wisps, inline text mints a fresh bead, dry-run mutates nothing,
// and --force / --reassign are the documented takeover escape hatches.
func TestShouldCheckInFlightDispatch(t *testing.T) {
	base := SlingOpts{BeadOrFormula: "BL-1"}
	if !shouldCheckInFlightDispatch(base) {
		t.Error("plain sling: shouldCheckInFlightDispatch = false, want true")
	}
	onFormula := base
	onFormula.OnFormula = "code-review"
	if !shouldCheckInFlightDispatch(onFormula) {
		t.Error("--on sling: shouldCheckInFlightDispatch = false, want true (pours are dispatches)")
	}
	for name, mutate := range map[string]func(*SlingOpts){
		"IsFormula":  func(o *SlingOpts) { o.IsFormula = true },
		"Force":      func(o *SlingOpts) { o.Force = true },
		"Reassign":   func(o *SlingOpts) { o.Reassign = true },
		"DryRun":     func(o *SlingOpts) { o.DryRun = true },
		"InlineText": func(o *SlingOpts) { o.InlineText = true },
	} {
		opts := base
		mutate(&opts)
		if shouldCheckInFlightDispatch(opts) {
			t.Errorf("%s: shouldCheckInFlightDispatch = true, want false", name)
		}
	}
}

// End-to-end through DoSling: a claimed bead refuses with the typed error and
// no routing metadata is written; --force and --reassign still dispatch.
func TestDoSlingInFlightGateEndToEnd(t *testing.T) {
	newDeps := func(t *testing.T) (SlingDeps, string) {
		t.Helper()
		cfg := &config.City{Workspace: config.Workspace{Name: "test"}}
		deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
		b, err := deps.Store.Create(beads.Bead{Title: "claimed work", Type: "task"})
		if err != nil {
			t.Fatal(err)
		}
		// Create normalizes status to open; claim it the way a worker would.
		status := "in_progress"
		assignee := "other-worker"
		if err := deps.Store.Update(b.ID, beads.UpdateOpts{Status: &status, Assignee: &assignee}); err != nil {
			t.Fatal(err)
		}
		return deps, b.ID
	}

	t.Run("refuses claimed bead", func(t *testing.T) {
		deps, beadID := newDeps(t)
		a := config.Agent{Name: "worker", MaxActiveSessions: intPtr(1)}
		_, err := DoSling(SlingOpts{Target: a, BeadOrFormula: beadID, NoConvoy: true}, deps, deps.Store)
		var conflict *InFlightConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("DoSling = %v, want *InFlightConflictError", err)
		}
		b, getErr := deps.Store.Get(beadID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if routed := b.Metadata[beadmeta.RoutedToMetadataKey]; routed != "" {
			t.Errorf("refused dispatch still wrote gc.routed_to=%q", routed)
		}
	})

	t.Run("force bypasses", func(t *testing.T) {
		deps, beadID := newDeps(t)
		a := config.Agent{Name: "worker", MaxActiveSessions: intPtr(1)}
		if _, err := DoSling(SlingOpts{Target: a, BeadOrFormula: beadID, NoConvoy: true, Force: true}, deps, deps.Store); err != nil {
			t.Fatalf("DoSling --force = %v, want nil", err)
		}
	})

	t.Run("reassign bypasses and reopens", func(t *testing.T) {
		deps, beadID := newDeps(t)
		a := config.Agent{Name: "worker", MaxActiveSessions: intPtr(1)}
		if _, err := DoSling(SlingOpts{Target: a, BeadOrFormula: beadID, NoConvoy: true, Reassign: true}, deps, deps.Store); err != nil {
			t.Fatalf("DoSling --reassign = %v, want nil", err)
		}
		b, getErr := deps.Store.Get(beadID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if b.Assignee != "" || b.Status != "open" {
			t.Errorf("reassign left assignee=%q status=%q, want cleared/open", b.Assignee, b.Status)
		}
	})
}

// Batch fan-out: a child covered by live foreign pour evidence fails
// individually while clean siblings still route. The batch loop's status
// filter already excludes in_progress children; this covers the pour-evidence
// half for children that still read open.
func TestDoSlingBatchFailsInFlightChild(t *testing.T) {
	cfg := &config.City{Workspace: config.Workspace{Name: "test"}}
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)

	convoy, err := deps.Store.Create(beads.Bead{Title: "batch", Type: "convoy", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	clean, err := deps.Store.Create(beads.Bead{Title: "clean", Type: "task", Status: "open", ParentID: convoy.ID})
	if err != nil {
		t.Fatal(err)
	}
	covered, err := deps.Store.Create(beads.Bead{Title: "covered", Type: "task", Status: "open", ParentID: convoy.ID})
	if err != nil {
		t.Fatal(err)
	}
	pourConvoy, err := deps.Store.Create(beads.Bead{Title: "pour", Type: "convoy", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Store.DepAdd(pourConvoy.ID, covered.ID, "tracks"); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Store.Create(beads.Bead{
		Title:  "live foreign root",
		Type:   "molecule",
		Status: "open",
		Metadata: map[string]string{
			beadmeta.InputConvoyIDMetadataKey: pourConvoy.ID,
			beadmeta.RoutedToMetadataKey:      "other-session",
		},
	}); err != nil {
		t.Fatal(err)
	}

	a := config.Agent{Name: "worker", MaxActiveSessions: intPtr(1)}
	result, err := DoSlingBatch(SlingOpts{Target: a, BeadOrFormula: convoy.ID, NoConvoy: true, NoFormula: true}, deps, deps.Store)
	// A failed child surfaces as a joined batch error carrying the typed
	// conflict, so the CLI/API boundary can map it; the per-child results
	// below still record the partial routing.
	var conflict *InFlightConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("DoSlingBatch = %v, want joined error carrying *InFlightConflictError", err)
	}
	var cleanRouted, coveredFailed bool
	for _, child := range result.Children {
		switch child.BeadID {
		case clean.ID:
			cleanRouted = child.Routed
		case covered.ID:
			coveredFailed = child.Failed
			if child.Failed && !strings.Contains(child.FailReason, "in flight") {
				t.Errorf("covered child FailReason = %q, want in-flight conflict", child.FailReason)
			}
		}
	}
	if !cleanRouted {
		t.Error("clean child was not routed")
	}
	if !coveredFailed {
		t.Error("covered child did not fail on live foreign pour evidence")
	}
}
