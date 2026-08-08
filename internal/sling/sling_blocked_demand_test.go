package sling

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// errDepListUnavailable models a transient dependency-read outage.
var errDepListUnavailable = errors.New("dep store unavailable")

// blockedDemandEnv builds a plain-bead sling environment: an unsuspended
// single-session target and a fresh open bead in a MemStore.
func blockedDemandEnv(t *testing.T) (SlingDeps, config.Agent, string) {
	t.Helper()
	cfg := &config.City{Workspace: config.Workspace{Name: "test"}}
	deps := testDeps(cfg, runtime.NewFake(), newFakeRunner().run)
	b, err := deps.Store.Create(beads.Bead{Title: "work", Type: "task", Status: "open"})
	if err != nil {
		t.Fatalf("creating work bead: %v", err)
	}
	return deps, config.Agent{Name: "worker", MaxActiveSessions: intPtr(1)}, b.ID
}

// addBlocker creates a dependency target with the given status and links
// beadID to it with depType, i.e. beadID depends on (is blocked by) the new
// bead. It returns the blocker's ID. MemStore.Create forces Status "open", so
// a closed blocker is closed explicitly after creation.
func addBlocker(t *testing.T, deps SlingDeps, beadID, depType, status string) string {
	t.Helper()
	blocker, err := deps.Store.Create(beads.Bead{Title: "blocker", Type: "task"})
	if err != nil {
		t.Fatalf("creating blocker bead: %v", err)
	}
	if status == "closed" {
		if err := deps.Store.Close(blocker.ID); err != nil {
			t.Fatalf("closing blocker %s: %v", blocker.ID, err)
		}
	}
	if err := deps.Store.DepAdd(beadID, blocker.ID, depType); err != nil {
		t.Fatalf("adding %s dep %s -> %s: %v", depType, beadID, blocker.ID, err)
	}
	return blocker.ID
}

// TestSlingReportsBlockedRoutedBead covers gas-9wc: `gc sling` on a bead with
// an unclosed blocking dependency reported plain success and stamped a correct
// gc.routed_to, yet the work was never dispatched and no session ever spawned.
//
// The two halves disagreed with nothing reporting the disagreement. The
// consumer of that metadata — the pool demand query, both the native
// collectOpenUnassignedRoutedWork read and the shell
// `bd ready --metadata-field gc.routed_to=$route --unassigned` — drops any bead
// with an unclosed ready-blocking dependency (#4395, gc-ft31x). sling never
// evaluated that predicate, so a blocked bead carried perfect routing metadata
// that its only consumer could not see, and the failure was indistinguishable
// from success: both the sling's own output and the bead's metadata read
// "dispatched".
//
// finalize is the correct chokepoint because its beadID argument is by
// construction the bead that receives gc.routed_to on every demand-driven path
// — the source bead on formula attach, the wisp root on a standalone formula,
// the bead itself on a plain route (see the routing comment above the attach
// path's finalize call).
func TestSlingReportsBlockedRoutedBead(t *testing.T) {
	t.Run("unclosed blocker is reported", func(t *testing.T) {
		deps, a, beadID := blockedDemandEnv(t)
		blockerID := addBlocker(t, deps, beadID, "blocks", "open")

		result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
		if err != nil {
			t.Fatalf("DoSling: %v", err)
		}
		if len(result.BlockedBy) != 1 || result.BlockedBy[0] != blockerID {
			t.Fatalf("BlockedBy = %v, want [%s] — a blocked bead routed with no signal is the gas-9wc silent failure", result.BlockedBy, blockerID)
		}
	})

	t.Run("routing still succeeds", func(t *testing.T) {
		// The report is a warning, not a refusal: pre-routing a bead whose
		// blocker is about to close is legitimate, and the demand query picks
		// it up as soon as it becomes ready. Refusing would break that.
		deps, a, beadID := blockedDemandEnv(t)
		addBlocker(t, deps, beadID, "blocks", "open")

		result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
		if err != nil {
			t.Fatalf("DoSling must not refuse a blocked bead: %v", err)
		}
		if result.BeadID != beadID {
			t.Errorf("BeadID = %q, want %q", result.BeadID, beadID)
		}
	})

	t.Run("every ready-blocking dep type is reported", func(t *testing.T) {
		// Must match beads.IsReadyBlockingDependencyType exactly; drifting from
		// it would reintroduce the silent case for the types left out.
		for _, depType := range []string{"blocks", "waits-for", "conditional-blocks"} {
			t.Run(depType, func(t *testing.T) {
				deps, a, beadID := blockedDemandEnv(t)
				blockerID := addBlocker(t, deps, beadID, depType, "open")

				result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
				if err != nil {
					t.Fatalf("DoSling: %v", err)
				}
				if len(result.BlockedBy) != 1 || result.BlockedBy[0] != blockerID {
					t.Errorf("BlockedBy = %v, want [%s] for dep type %q", result.BlockedBy, blockerID, depType)
				}
			})
		}
	})

	t.Run("closed blocker is silent", func(t *testing.T) {
		deps, a, beadID := blockedDemandEnv(t)
		addBlocker(t, deps, beadID, "blocks", "closed")

		result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
		if err != nil {
			t.Fatalf("DoSling: %v", err)
		}
		if len(result.BlockedBy) != 0 {
			t.Errorf("BlockedBy = %v, want empty — a closed blocker leaves the bead ready", result.BlockedBy)
		}
	})

	t.Run("informational dep types are silent", func(t *testing.T) {
		// parent-child, relates-to and tracks never gate Ready(), so reporting
		// them would be a false alarm on ordinary molecule structure.
		for _, depType := range []string{"parent-child", "relates-to", "tracks"} {
			t.Run(depType, func(t *testing.T) {
				deps, a, beadID := blockedDemandEnv(t)
				addBlocker(t, deps, beadID, depType, "open")

				result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
				if err != nil {
					t.Fatalf("DoSling: %v", err)
				}
				if len(result.BlockedBy) != 0 {
					t.Errorf("BlockedBy = %v, want empty for non-ready-blocking dep type %q", result.BlockedBy, depType)
				}
			})
		}
	})

	t.Run("unblocked bead is silent", func(t *testing.T) {
		deps, a, beadID := blockedDemandEnv(t)

		result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
		if err != nil {
			t.Fatalf("DoSling: %v", err)
		}
		if len(result.BlockedBy) != 0 {
			t.Errorf("BlockedBy = %v, want empty for a bead with no dependencies", result.BlockedBy)
		}
	})

	t.Run("multiple blockers are all reported", func(t *testing.T) {
		deps, a, beadID := blockedDemandEnv(t)
		first := addBlocker(t, deps, beadID, "blocks", "open")
		second := addBlocker(t, deps, beadID, "waits-for", "open")
		addBlocker(t, deps, beadID, "blocks", "closed")

		result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
		if err != nil {
			t.Fatalf("DoSling: %v", err)
		}
		got := strings.Join(result.BlockedBy, ",")
		if len(result.BlockedBy) != 2 || !strings.Contains(got, first) || !strings.Contains(got, second) {
			t.Errorf("BlockedBy = %v, want exactly the two unclosed blockers [%s %s]", result.BlockedBy, first, second)
		}
	})

	t.Run("force does not silence the report", func(t *testing.T) {
		// --force means "route it anyway", not "hide the reason nothing will
		// pick it up". Suppressing here would restore the silent failure for
		// exactly the operators most likely to hit it.
		deps, a, beadID := blockedDemandEnv(t)
		blockerID := addBlocker(t, deps, beadID, "blocks", "open")

		opts := testOpts(a, beadID)
		opts.Force = true
		result, err := DoSling(opts, deps, deps.Store)
		if err != nil {
			t.Fatalf("DoSling --force: %v", err)
		}
		if len(result.BlockedBy) != 1 || result.BlockedBy[0] != blockerID {
			t.Errorf("BlockedBy = %v, want [%s] under --force", result.BlockedBy, blockerID)
		}
	})
}

// getErrorStore fails Get for one bead and delegates everything else, modeling
// a dependency row whose target this store cannot resolve — the shape of a
// cross-store dependency.
type getErrorStore struct {
	beads.Store
	failFor string
	err     error
}

func (s getErrorStore) Get(id string) (beads.Bead, error) {
	if id == s.failFor {
		return beads.Bead{}, s.err
	}
	return s.Store.Get(id)
}

// TestSlingUnresolvableBlockerIsNotCounted pins the deliberate asymmetry in
// UnclosedBlockers: an unreadable dependency TARGET is not counted as blocking,
// while an unreadable dependency LIST is reported (see
// TestSlingBlockedProbeFailureIsSurfaced).
//
// A cross-store dependency row legitimately points at a bead absent from this
// store, and the demand-side reader skips it for the same reason
// (cachedBeadReady only counts a target whose status it actually resolved).
// Counting it here would emit a false "nothing will spawn" on routes that
// dispatch perfectly well — the opposite failure, and just as corrosive to
// trust in the warning.
func TestSlingUnresolvableBlockerIsNotCounted(t *testing.T) {
	deps, a, beadID := blockedDemandEnv(t)
	blockerID := addBlocker(t, deps, beadID, "blocks", "open")
	deps.Store = getErrorStore{Store: deps.Store, failFor: blockerID, err: errDepListUnavailable}

	result, err := DoSling(testOpts(a, beadID), deps, deps.Store)
	if err != nil {
		t.Fatalf("DoSling: %v", err)
	}
	if len(result.BlockedBy) != 0 {
		t.Errorf("BlockedBy = %v, want empty — an unresolvable target must not be reported as blocking", result.BlockedBy)
	}
}

// depListErrorStore fails DepList for one bead and delegates everything else,
// modeling a transient backing-store outage on the blocked-dependency probe.
type depListErrorStore struct {
	beads.Store
	failFor string
	err     error
}

func (s depListErrorStore) DepList(id, direction string) ([]beads.Dep, error) {
	if id == s.failFor {
		return nil, s.err
	}
	return s.Store.DepList(id, direction)
}

// TestSlingBlockedProbeFailureIsSurfaced pins the probe's failure mode: a
// dependency read that cannot complete must not silently claim the bead is
// unblocked, and must not fail the sling either. It degrades to a visible
// warning, so an unreadable dependency graph can never masquerade as "ready".
//
// --force is what makes this path reachable. Without it DetectCycle reads the
// same edges first in preflight and hard-fails, which is its own correct
// behavior; --force skips cycle detection but not this probe, so the probe's
// own degradation is exercised here rather than shadowed by the cycle check.
func TestSlingBlockedProbeFailureIsSurfaced(t *testing.T) {
	deps, a, beadID := blockedDemandEnv(t)
	deps.Store = depListErrorStore{Store: deps.Store, failFor: beadID, err: errDepListUnavailable}

	opts := testOpts(a, beadID)
	opts.Force = true
	result, err := DoSling(opts, deps, deps.Store)
	if err != nil {
		t.Fatalf("a failed dependency probe must not fail the sling: %v", err)
	}
	if len(result.BlockedBy) != 0 {
		t.Errorf("BlockedBy = %v, want empty when the probe could not complete", result.BlockedBy)
	}
	var found bool
	for _, w := range result.BeadWarnings {
		if strings.Contains(w, beadID) && strings.Contains(w, "dependencies") {
			found = true
		}
	}
	if !found {
		t.Errorf("BeadWarnings = %v, want a warning naming %s — a swallowed probe error reads as 'not blocked'", result.BeadWarnings, beadID)
	}
}
