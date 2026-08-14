package sling

import (
	"context"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// routeStampSetup builds a sling against a worker pool with a fresh open
// bead, mirroring orderClaimedPoolHandoffSetup but without the claimed
// state. Callers set Branch/TargetBranch on the returned opts.
func routeStampSetup(t *testing.T) (SlingOpts, SlingDeps, beads.Bead) {
	t.Helper()
	runner := newFakeRunner()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test"},
		Rigs:      []config.Rig{{Name: "myrig", Path: "/myrig", Prefix: "gc"}},
	}
	a := config.Agent{Name: "polecat", Dir: "myrig", MaxActiveSessions: intPtr(2)}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)
	bead, err := deps.Store.Create(beads.Bead{Title: "landed work", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	opts := SlingOpts{Target: a, BeadOrFormula: bead.ID, NoFormula: true}
	return opts, deps, bead
}

// TestDoSling_StampsBranchAndTargetMetadata is the core test for gas-jyi5:
// a bead routed to a merge-queue consumer is only visible to that consumer's
// find-work query when it carries metadata.branch (required) and
// metadata.target (optional) — the branch-handoff contract read by e.g.
// mol-refinery-patrol. `gc sling --branch/--target` must stamp both keys on
// the routed bead so one sling produces a fully contract-visible bead
// instead of requiring a separate, forgettable `gc bd update` first.
func TestDoSling_StampsBranchAndTargetMetadata(t *testing.T) {
	opts, deps, bead := routeStampSetup(t)
	opts.Branch = "polecat/gc-101"
	opts.TargetBranch = "integration/deploy"

	if _, err := DoSling(opts, deps, nil); err != nil {
		t.Fatalf("DoSling --branch --target: %v", err)
	}
	got, err := deps.Store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", bead.ID, err)
	}
	if got.Metadata["branch"] != "polecat/gc-101" {
		t.Errorf("metadata.branch = %q, want polecat/gc-101", got.Metadata["branch"])
	}
	if got.Metadata["target"] != "integration/deploy" {
		t.Errorf("metadata.target = %q, want integration/deploy", got.Metadata["target"])
	}
}

// TestDoSling_StampsBranchWithoutTarget covers the contract's optionality:
// metadata.target defaults downstream (consumer template target_branch), so
// --branch alone must stamp only branch and leave target absent rather than
// writing an empty value that would shadow the default.
func TestDoSling_StampsBranchWithoutTarget(t *testing.T) {
	opts, deps, bead := routeStampSetup(t)
	opts.Branch = "polecat/gc-102"

	if _, err := DoSling(opts, deps, nil); err != nil {
		t.Fatalf("DoSling --branch: %v", err)
	}
	got, err := deps.Store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", bead.ID, err)
	}
	if got.Metadata["branch"] != "polecat/gc-102" {
		t.Errorf("metadata.branch = %q, want polecat/gc-102", got.Metadata["branch"])
	}
	if _, present := got.Metadata["target"]; present {
		t.Errorf("metadata.target present (%q), want absent when TargetBranch is unset", got.Metadata["target"])
	}
}

// TestDoSling_BranchRejectedWithFormulaLaunch guards the flags from the
// standalone-formula hazard that bit --reassign (see
// TestDoSling_ReassignFormula_DoesNotReopenCollidingBead): on an IsFormula
// launch BeadOrFormula is a formula NAME, so stamping would write metadata
// onto any bead whose ID collides with that name. There is no subject work
// bead on a formula launch — reject loudly instead of guessing.
func TestDoSling_BranchRejectedWithFormulaLaunch(t *testing.T) {
	runner := newFakeRunner()
	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}}
	a := config.Agent{Name: "mayor", MaxActiveSessions: intPtr(1)}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)
	// Seed a bead whose ID collides with the formula name; it must stay
	// untouched.
	deps.Store = seededStore("code-review")

	opts := SlingOpts{Target: a, BeadOrFormula: "code-review", IsFormula: true, Branch: "polecat/gc-103"}
	_, err := DoSling(opts, deps, nil)
	if err == nil {
		t.Fatal("DoSling --formula --branch succeeded, want rejection (no subject bead exists on a formula launch)")
	}
	if !strings.Contains(err.Error(), "--formula") {
		t.Errorf("error = %q, want mention of --formula", err)
	}
	got, getErr := deps.Store.Get("code-review")
	if getErr != nil {
		t.Fatalf("store.Get(code-review): %v", getErr)
	}
	if _, present := got.Metadata["branch"]; present {
		t.Errorf("colliding bead gained metadata.branch = %q, want untouched", got.Metadata["branch"])
	}
}

// TestDoSling_DryRunSkipsStamp: dry-run previews and must not mutate — the
// same contract every other pre-route mutation (reassign reopen, inline bead
// creation) already honors.
func TestDoSling_DryRunSkipsStamp(t *testing.T) {
	opts, deps, bead := routeStampSetup(t)
	opts.Branch = "polecat/gc-104"
	opts.DryRun = true

	if _, err := DoSling(opts, deps, nil); err != nil {
		t.Fatalf("DoSling --dry-run --branch: %v", err)
	}
	got, err := deps.Store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", bead.ID, err)
	}
	if _, present := got.Metadata["branch"]; present {
		t.Errorf("dry-run stamped metadata.branch = %q, want no mutation", got.Metadata["branch"])
	}
}

// TestDoSling_StampFailureAbortsRoute: stamping is fail-closed. If the
// contract metadata cannot be written, routing anyway would reproduce the
// exact defect the flags exist to fix — a bead handed to a consumer whose
// queue filter can never see it. The route must not execute. (--force skips
// the existing-bead validation, so the stamp is the first store touch and
// its error must surface.)
func TestDoSling_StampFailureAbortsRoute(t *testing.T) {
	runner := newFakeRunner()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test"},
		Rigs:      []config.Rig{{Name: "myrig", Path: "/myrig", Prefix: "gc"}},
	}
	a := config.Agent{Name: "polecat", Dir: "myrig", MaxActiveSessions: intPtr(2)}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)

	opts := SlingOpts{Target: a, BeadOrFormula: "gc-missing", NoFormula: true, Force: true, Branch: "polecat/gc-105"}
	_, err := DoSling(opts, deps, nil)
	if err == nil {
		t.Fatal("DoSling --force --branch on a missing bead succeeded, want stamp failure")
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner executed %d command(s) after stamp failure, want 0 (route must not run half-stamped)", len(runner.calls))
	}
}

// TestDoSling_StampsBranchOnAlreadyRoutedBead is the regression test for the
// stamp-vs-idempotency ordering. The flags' primary repair scenario is a bead
// an earlier sling already routed WITHOUT the contract stamp — the exact
// invisible-to-its-consumer state gas-jyi5 documents. Re-slinging it to the
// same target with --branch hits the idempotent short-circuit (already
// routed, no double dispatch), and if the stamp ran after that check the one
// command an operator would use to repair the bead would silently do
// nothing. The stamp must land even when routing short-circuits.
func TestDoSling_StampsBranchOnAlreadyRoutedBead(t *testing.T) {
	opts, deps, bead := routeStampSetup(t)
	// Simulate the earlier, stampless sling: routed to this pool, unclaimed.
	if err := deps.Store.SetMetadata(bead.ID, "gc.routed_to", "myrig/polecat"); err != nil {
		t.Fatalf("SetMetadata(gc.routed_to): %v", err)
	}
	opts.Branch = "polecat/gc-108"
	opts.NoConvoy = true

	// Pass the store as querier: the idempotency check reads bead state
	// through it, and a nil querier can never report Idempotent (which
	// would let this test pass without exercising the short-circuit).
	result, err := DoSling(opts, deps, deps.Store)
	if err != nil {
		t.Fatalf("DoSling --branch on already-routed bead: %v", err)
	}
	if !result.Idempotent {
		t.Fatalf("result.Idempotent = false, want true — the fixture must actually trigger the short-circuit for this regression test to mean anything (result=%+v)", result)
	}
	got, getErr := deps.Store.Get(bead.ID)
	if getErr != nil {
		t.Fatalf("store.Get(%s): %v", bead.ID, getErr)
	}
	if got.Metadata["branch"] != "polecat/gc-108" {
		t.Errorf("metadata.branch = %q, want polecat/gc-108 (stamp must land even when routing is idempotent)", got.Metadata["branch"])
	}
}

// TestDoSlingBatch_ContainerRejectsBranchStamp: a convoy is a container —
// its children each carry their own branch, so one --branch value has no
// well-defined recipient. Stamping the container would satisfy nobody's
// contract, and fanning the value out to every child would corrupt N-1 of
// them. Reject with a pointer at the per-convoy tool instead.
func TestDoSlingBatch_ContainerRejectsBranchStamp(t *testing.T) {
	runner := newFakeRunner()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test"},
		Rigs:      []config.Rig{{Name: "myrig", Path: "/myrig", Prefix: "gc"}},
	}
	a := config.Agent{Name: "polecat", Dir: "myrig", MaxActiveSessions: intPtr(2)}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)
	convoy, err := deps.Store.Create(beads.Bead{Title: "batch", Type: "convoy"})
	if err != nil {
		t.Fatalf("Create convoy: %v", err)
	}

	opts := SlingOpts{Target: a, BeadOrFormula: convoy.ID, NoFormula: true, Branch: "polecat/gc-106"}
	memStore, ok := deps.Store.(*beads.MemStore)
	if !ok {
		t.Fatalf("testDeps store is %T, want *beads.MemStore", deps.Store)
	}
	_, err = DoSlingBatch(opts, deps, memStore)
	if err == nil {
		t.Fatal("DoSlingBatch --branch on a convoy succeeded, want container rejection")
	}
	got, getErr := deps.Store.Get(convoy.ID)
	if getErr != nil {
		t.Fatalf("store.Get(%s): %v", convoy.ID, getErr)
	}
	if _, present := got.Metadata["branch"]; present {
		t.Errorf("container gained metadata.branch = %q, want untouched", got.Metadata["branch"])
	}
}

// TestExpandConvoy_ThreadsBranchStampRejection guards the RouteOpts plumbing:
// the CLI's plain-bead convoy path goes through Sling.ExpandConvoy, which
// rebuilds SlingOpts from RouteOpts. If RouteOpts dropped Branch/TargetBranch
// the container rejection above could never fire on that path and the flags
// would be silently ignored — the swallowed-intent failure mode.
func TestExpandConvoy_ThreadsBranchStampRejection(t *testing.T) {
	runner := newFakeRunner()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test"},
		Rigs:      []config.Rig{{Name: "myrig", Path: "/myrig", Prefix: "gc"}},
	}
	a := config.Agent{Name: "polecat", Dir: "myrig", MaxActiveSessions: intPtr(2)}
	deps := testDeps(cfg, runtime.NewFake(), runner.run)
	convoy, err := deps.Store.Create(beads.Bead{Title: "batch", Type: "convoy"})
	if err != nil {
		t.Fatalf("Create convoy: %v", err)
	}
	sl, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	memStore, ok := deps.Store.(*beads.MemStore)
	if !ok {
		t.Fatalf("testDeps store is %T, want *beads.MemStore", deps.Store)
	}
	_, err = sl.ExpandConvoy(context.Background(), convoy.ID, a, RouteOpts{NoFormula: true, Branch: "polecat/gc-107"}, memStore)
	if err == nil {
		t.Fatal("ExpandConvoy with RouteOpts.Branch on a convoy succeeded, want container rejection threaded through")
	}
}
