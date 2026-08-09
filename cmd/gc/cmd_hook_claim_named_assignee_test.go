package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Regression coverage for gas-q07: `gc hook --claim` served an OPEN bead that
// had been handed off to a named session (the refinery) back to the generic
// pool, pulling it out of the merge queue and back onto a worker.
//
// The handoff contract in mol-polecat-work's submit-and-exit step is exactly:
//
//	bd update <id> --status=open --assignee=<named session> \
//	  --set-metadata gc.routed_to=""
//
// because "generic pool demand is represented by unassigned work with
// gc.routed_to=<route>, so named-session work should not retain pool-demand
// metadata". A bead in that state is the canonical "belongs to a named session,
// not the pool" shape, and it must be invisible to a pool member of a different
// agent type on both claim paths: the identity-adoption path
// (hookClaimExistingOrAssigned) and the fresh-claim path
// (claimFirstEligibleHookCandidate).
//
// These tests pin the claim-path contract at the seam. The live backstop is
// bd's own atomic claim, which refuses "issue already assigned to ..." — but
// relying on that alone would mean the hook still SERVES the bead and only
// fails at the mutation, so the contract is pinned here in the hook.

const (
	// namedAssigneePolecatSession is the pool worker doing the claiming: a
	// suffixed polecat, i.e. a member of the generic pool.
	namedAssigneePolecatSession = "gastown__polecat-az-wisp-g7x3c"
	// namedAssigneeHolder is the named session the work was handed off to. It
	// is a different agent type, so it shares no identity with the polecat.
	namedAssigneeHolder = "gascity/gastown.refinery"
	// namedAssigneePoolRoute is the generic pool route the polecat serves.
	namedAssigneePoolRoute = "gascity/gastown.polecat"
)

func namedAssigneeClaimOptions() hookClaimOptions {
	return hookClaimOptions{
		Assignee: namedAssigneePolecatSession,
		IdentityCandidates: hookClaimIdentityCandidates(
			namedAssigneePolecatSession,
			"az-wisp-g7x3c",
			"gascity/gastown.rictus",
		),
		RouteTargets: hookClaimRouteTargets(namedAssigneePoolRoute),
		JSON:         true,
	}
}

// namedAssigneeRow renders one work-query row for a bead assigned to a named
// session. routedTo is emitted verbatim so a caller can pin both the cleared
// shape ("") and the stale pre-handoff shape (the pool route).
func namedAssigneeRow(id, status, assignee, routedTo string) string {
	row := map[string]any{
		"id":         id,
		"status":     status,
		"issue_type": "task",
		"assignee":   assignee,
		"metadata": map[string]string{
			beadmeta.RoutedToMetadataKey: routedTo,
		},
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestHookClaimDoesNotServeBeadAssignedToNamedSession is the primary gas-q07
// regression: the exact observed sequence. An open bead handed off to the
// refinery with gc.routed_to cleared must not come back to the polecat pool.
func TestHookClaimDoesNotServeBeadAssignedToNamedSession(t *testing.T) {
	const handedOffID = "gas-tl8"
	runner := func(string, string) (string, error) {
		return `[` + namedAssigneeRow(handedOffID, "open", namedAssigneeHolder, "") + `]`, nil
	}
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, bool, error) {
			t.Fatalf("store.Claim called for %q, which is assigned to %q; a named session's work must never reach the claim mutation",
				id, namedAssigneeHolder)
			return beads.Bead{}, false, nil
		},
	}
	var stdout, stderr bytes.Buffer
	doHookClaim("bd ready --json", "/tmp/work", namedAssigneeClaimOptions(), ops, &stdout, &stderr)

	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Action == "work" {
		t.Fatalf("REGRESSION gas-q07: bead %q assigned to named session %q served as action=work (reason=%q)",
			result.BeadID, namedAssigneeHolder, result.Reason)
	}
}

// TestHookClaimDoesNotServeNamedAssigneeWithStalePoolRoute covers the harder
// arm: the work-query snapshot predates the handoff, so the row still carries
// the pool route in gc.routed_to while the assignee is already the named
// session. Route match alone must not make it claimable.
func TestHookClaimDoesNotServeNamedAssigneeWithStalePoolRoute(t *testing.T) {
	const handedOffID = "gas-tl8"
	runner := func(string, string) (string, error) {
		return `[` + namedAssigneeRow(handedOffID, "open", namedAssigneeHolder, namedAssigneePoolRoute) + `]`, nil
	}
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, bool, error) {
			t.Fatalf("store.Claim called for %q: assignee %q owns it even though gc.routed_to still points at the pool",
				id, namedAssigneeHolder)
			return beads.Bead{}, false, nil
		},
	}
	var stdout, stderr bytes.Buffer
	doHookClaim("bd ready --json", "/tmp/work", namedAssigneeClaimOptions(), ops, &stdout, &stderr)

	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Action == "work" {
		t.Fatalf("REGRESSION gas-q07: bead %q assigned to %q served as action=work on a stale pool route (reason=%q)",
			result.BeadID, namedAssigneeHolder, result.Reason)
	}
}

// TestHookClaimDoesNotResurrectClearedMetadata is the second, independent
// gas-q07 defect. ops.Claim returns a CANONICAL readback (hookClaimWithBdStore
// re-reads the bead with store.Get after the mutation), so the claimed bead's
// metadata is authoritative. Overlaying the work-query snapshot underneath it
// restores keys deleted between the snapshot and the claim — here gc.routed_to.
//
// Scope, measured rather than assumed: the merged map is never written back to
// the store, so this does NOT re-arm the bead's stored routing. The reachable
// harm is the hook ANNOUNCING a stale pool route for a bead whose route was
// cleared, which is why the fix is scoped to the reported route and the merge
// itself is left intact for the partial-projection keys it exists to carry
// (TestDoHookClaimClaimsRoutedUnassignedWork pins that contract).
func TestHookClaimDoesNotResurrectClearedMetadata(t *testing.T) {
	const claimableID = "gas-stale"
	// The snapshot predates the handoff: unassigned and still routed to the pool.
	runner := func(string, string) (string, error) {
		return `[` + namedAssigneeRow(claimableID, "open", "", namedAssigneePoolRoute) + `]`, nil
	}
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			// Canonical readback: gc.routed_to has since been cleared (the key is
			// gone), and an unrelated key survives to prove the merge still
			// carries non-conflicting canonical data.
			return beads.Bead{
				ID:       id,
				Status:   "in_progress",
				Assignee: assignee,
				Type:     "task",
				Metadata: map[string]string{
					beadmeta.WorkBranchMetadataKey: "polecat/gas-stale",
				},
			}, true, nil
		},
	}
	var stdout, stderr bytes.Buffer
	doHookClaim("bd ready --json", "/tmp/work", namedAssigneeClaimOptions(), ops, &stdout, &stderr)

	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\nraw: %s", err, stdout.String())
	}
	if result.Route == namedAssigneePoolRoute {
		t.Fatalf("REGRESSION gas-q07: claim resurrected gc.routed_to=%q from the stale work-query snapshot after it was cleared; route must come from the canonical readback",
			result.Route)
	}
}
