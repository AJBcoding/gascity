package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

const continuationCompensationCandidate = `[{"id":"work-1","status":"open","metadata":{"gc.routed_to":"worker","gc.root_bead_id":"root-1","gc.continuation_group":"body"}}]`

type continuationCompensationRecorder struct {
	output string

	claimed                      []string
	primaryReleased              []string
	continuationAssigned         []string
	continuationReleased         []string
	continuationReleaseAssignees []string
	claimReleasedEvents          []hookClaimReleaseRecord
	compensationFailures         []hookClaimCompensationFailureRecord
	assignFailures               map[string]error
	releaseOutcomes              map[string]bool
	releaseFailures              map[string]error
	requireReleaseAssignee       string
	failIfPrimaryClaimed         bool
	continuationSiblings         []beads.Bead
}

func newContinuationCompensationRecorder() *continuationCompensationRecorder {
	return &continuationCompensationRecorder{
		output:          continuationCompensationCandidate,
		assignFailures:  map[string]error{},
		releaseOutcomes: map[string]bool{},
		releaseFailures: map[string]error{},
		continuationSiblings: []beads.Bead{
			continuationSibling("work-1"),
			continuationSibling("sib-1"),
			continuationSibling("sib-2"),
		},
	}
}

func continuationSibling(id string) beads.Bead {
	return beads.Bead{
		ID:     id,
		Status: "open",
		Metadata: map[string]string{
			"gc.routed_to":          "worker",
			"gc.root_bead_id":       "root-1",
			"gc.continuation_group": "body",
		},
	}
}

func (r *continuationCompensationRecorder) ops(t *testing.T) hookClaimOps {
	t.Helper()
	return hookClaimOps{
		Runner: func(string, string) (string, error) { return r.output, nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			if r.failIfPrimaryClaimed {
				t.Fatalf("primary claim called for adopted assignment %s", beadID)
			}
			r.claimed = append(r.claimed, beadID)
			return beads.Bead{
				ID:       beadID,
				Status:   "in_progress",
				Assignee: assignee,
				Metadata: map[string]string{
					"gc.routed_to":          "worker",
					"gc.root_bead_id":       "root-1",
					"gc.continuation_group": "body",
				},
			}, true, nil
		},
		ListContinuation: func(context.Context, string, []string, string, string) ([]beads.Bead, error) {
			return slices.Clone(r.continuationSiblings), nil
		},
		AssignContinuation: func(_ context.Context, _ string, _ []string, beadID, _ string) error {
			if err := r.assignFailures[beadID]; err != nil {
				return err
			}
			r.continuationAssigned = append(r.continuationAssigned, beadID)
			return nil
		},
		Release: func(_ context.Context, _ string, _ []string, beadID, _ string) (bool, error) {
			r.primaryReleased = append(r.primaryReleased, beadID)
			return true, nil
		},
		ReleaseContinuationAssignment: func(_ context.Context, _ string, _ []string, beadID, assignee string) (bool, error) {
			r.continuationReleased = append(r.continuationReleased, beadID)
			r.continuationReleaseAssignees = append(r.continuationReleaseAssignees, assignee)
			if err := r.releaseFailures[beadID]; err != nil {
				return false, err
			}
			if r.requireReleaseAssignee != "" && assignee != r.requireReleaseAssignee {
				return false, nil
			}
			if released, ok := r.releaseOutcomes[beadID]; ok {
				return released, nil
			}
			return true, nil
		},
		EmitClaimReleased: func(rec hookClaimReleaseRecord) {
			r.claimReleasedEvents = append(r.claimReleasedEvents, rec)
		},
		EmitClaimCompensationFailed: func(rec hookClaimCompensationFailureRecord) {
			r.compensationFailures = append(r.compensationFailures, rec)
		},
		PublishRunMap:     func(string, string, ...string) error { return nil },
		StampWorkMeta:     func(context.Context, string, []string, string, string, map[string]string) error { return nil },
		ReadWorkMeta:      func(context.Context, string, []string, string, string) (beads.Bead, error) { return beads.Bead{}, nil },
		ResolveWorkBranch: func(string) string { return "" },
	}
}

func continuationCompensationOptions() hookClaimOptions {
	return hookClaimOptions{
		Assignee:           "worker-1",
		IdentityCandidates: []string{"worker-1"},
		RouteTargets:       []string{"worker"},
		JSON:               true,
	}
}

func TestHookClaimPreassignFailureBeforeSiblingAssignmentReleasesMintedPrimary(t *testing.T) {
	rec := newContinuationCompensationRecorder()
	rec.assignFailures["sib-1"] = errors.New("assign failed")

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", continuationCompensationOptions(), rec.ops(t), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no delivered claim", stdout.String())
	}
	if got := strings.Join(rec.primaryReleased, ","); got != "work-1" {
		t.Fatalf("primary releases = %v, want [work-1]", rec.primaryReleased)
	}
	if len(rec.continuationReleased) != 0 {
		t.Fatalf("continuation releases = %v, want none before any sibling was assigned", rec.continuationReleased)
	}
	if !strings.Contains(stderr.String(), "assigning sib-1") {
		t.Fatalf("stderr = %q, want failed sibling id", stderr.String())
	}
}

func TestHookClaimPreassignFailureAfterSiblingAssignmentCompensatesSiblingAndMintedPrimary(t *testing.T) {
	rec := newContinuationCompensationRecorder()
	rec.assignFailures["sib-2"] = errors.New("assign failed")

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", continuationCompensationOptions(), rec.ops(t), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.Join(rec.continuationAssigned, ","); got != "sib-1" {
		t.Fatalf("assigned siblings = %v, want [sib-1]", rec.continuationAssigned)
	}
	if got := strings.Join(rec.continuationReleased, ","); got != "sib-1" {
		t.Fatalf("continuation releases = %v, want [sib-1]", rec.continuationReleased)
	}
	if got := strings.Join(rec.primaryReleased, ","); got != "work-1" {
		t.Fatalf("primary releases = %v, want [work-1]", rec.primaryReleased)
	}
}

func TestHookClaimPreassignFailureOnAdoptedPrimaryCompensatesSiblingButKeepsPrimary(t *testing.T) {
	rec := newContinuationCompensationRecorder()
	rec.output = `[{"id":"work-1","status":"in_progress","assignee":"worker-1","metadata":{"gc.routed_to":"worker","gc.root_bead_id":"root-1","gc.continuation_group":"body"}}]`
	rec.failIfPrimaryClaimed = true
	rec.assignFailures["sib-2"] = errors.New("assign failed")

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", continuationCompensationOptions(), rec.ops(t), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.Join(rec.continuationReleased, ","); got != "sib-1" {
		t.Fatalf("continuation releases = %v, want [sib-1]", rec.continuationReleased)
	}
	if len(rec.primaryReleased) != 0 {
		t.Fatalf("primary releases = %v, want none for adopted primary", rec.primaryReleased)
	}
}

func TestHookClaimResultDeliveryFailureOnAdoptedPrimaryCompensatesSiblingsWithAssignmentAssignee(t *testing.T) {
	rec := newContinuationCompensationRecorder()
	rec.output = `[{"id":"work-1","status":"in_progress","assignee":"worker-alias","metadata":{"gc.routed_to":"worker","gc.root_bead_id":"root-1","gc.continuation_group":"body"}}]`
	rec.failIfPrimaryClaimed = true
	rec.requireReleaseAssignee = "worker-canonical"
	opts := continuationCompensationOptions()
	opts.Assignee = "worker-canonical"
	opts.IdentityCandidates = []string{"worker-canonical", "worker-alias"}

	var stderr bytes.Buffer
	code := doHookClaim("query", "/rig", opts, rec.ops(t), brokenPipeWriter{}, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.Join(rec.continuationAssigned, ","); got != "sib-1,sib-2" {
		t.Fatalf("assigned siblings = %v, want [sib-1 sib-2]", rec.continuationAssigned)
	}
	if got := strings.Join(rec.continuationReleased, ","); got != "sib-1,sib-2" {
		t.Fatalf("continuation releases = %v, want [sib-1 sib-2]", rec.continuationReleased)
	}
	if got := strings.Join(rec.continuationReleaseAssignees, ","); got != "worker-canonical,worker-canonical" {
		t.Fatalf("continuation release assignees = %v, want worker-canonical for every sibling", rec.continuationReleaseAssignees)
	}
	if len(rec.compensationFailures) != 0 {
		t.Fatalf("compensation failures = %+v, want none after releasing with assignment assignee", rec.compensationFailures)
	}
	if len(rec.primaryReleased) != 0 {
		t.Fatalf("primary releases = %v, want none for adopted primary", rec.primaryReleased)
	}
}

func TestHookClaimResultDeliveryFailureCompensatesPreassignedSiblings(t *testing.T) {
	rec := newContinuationCompensationRecorder()
	rec.continuationSiblings = []beads.Bead{
		continuationSibling("work-1"),
		continuationSibling("sib-1"),
	}

	var stderr bytes.Buffer
	code := doHookClaim("query", "/rig", continuationCompensationOptions(), rec.ops(t), brokenPipeWriter{}, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.Join(rec.continuationAssigned, ","); got != "sib-1" {
		t.Fatalf("assigned siblings = %v, want [sib-1]", rec.continuationAssigned)
	}
	if got := strings.Join(rec.continuationReleased, ","); got != "sib-1" {
		t.Fatalf("continuation releases = %v, want [sib-1]", rec.continuationReleased)
	}
	if got := strings.Join(rec.primaryReleased, ","); got != "work-1" {
		t.Fatalf("primary releases = %v, want [work-1]", rec.primaryReleased)
	}
}

func TestHookClaimContinuationCompensationFailureNamesResidualIDsDurably(t *testing.T) {
	rec := newContinuationCompensationRecorder()
	rec.continuationSiblings = []beads.Bead{
		continuationSibling("work-1"),
		continuationSibling("sib-1"),
		continuationSibling("sib-2"),
		continuationSibling("sib-3"),
	}
	rec.assignFailures["sib-3"] = errors.New("assign failed")
	rec.releaseFailures["sib-2"] = errors.New("release refused")

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", continuationCompensationOptions(), rec.ops(t), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr=%s", code, stderr.String())
	}
	if got := strings.Join(rec.continuationReleased, ","); got != "sib-1,sib-2" {
		t.Fatalf("continuation releases = %v, want [sib-1 sib-2]", rec.continuationReleased)
	}
	if len(rec.compensationFailures) != 1 {
		t.Fatalf("compensation failures = %+v, want one durable diagnostic", rec.compensationFailures)
	}
	if got := strings.Join(rec.compensationFailures[0].ResidualBeadIDs, ","); got != "sib-2" {
		t.Fatalf("residual ids = %q, want sib-2", got)
	}
	if !strings.Contains(stderr.String(), "residual continuation assignments") || !strings.Contains(stderr.String(), "sib-2") {
		t.Fatalf("stderr = %q, want residual sibling diagnostic", stderr.String())
	}
}
