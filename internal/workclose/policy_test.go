package workclose

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

func TestWorkClosePolicyPreservesControlAndNonShippedBehavior(t *testing.T) {
	p := WorkClosePolicy{}
	for name, bead := range map[string]beads.Bead{
		"control":  {Status: "closed", Type: "task", Metadata: beads.StringMap{beadmeta.KindMetadataKey: "step"}},
		"non-task": {Status: "closed", Type: "convoy"},
		"no-op":    {Status: "closed", Type: "task", Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := p.Evaluate(Request{Current: bead, ProspectiveStatus: bead.Status, ProspectiveMetadata: bead.Metadata, StoreRef: "city:test"}); len(got) != 0 {
				t.Fatalf("Evaluate() = %v, want no violations", got)
			}
		})
	}
}

func TestWorkClosePolicyShippedRequiresDurableEvidence(t *testing.T) {
	sha := strings.Repeat("a", 40)
	bead := beads.Bead{ID: "ga-1", Status: "closed", Type: "task", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
		beadmeta.WorkCommitMetadataKey:  sha,
		beadmeta.WorkBranchMetadataKey:  "main",
	}}
	p := WorkClosePolicy{CommitReachable: func(_, _ string) bool { return true }}
	got := strings.Join(p.Evaluate(Request{Current: bead, ProspectiveStatus: bead.Status, ProspectiveMetadata: bead.Metadata, StoreRef: "city:test"}), "\n")
	if !strings.Contains(got, beadmeta.DeliveryStateMetadataKey) {
		t.Fatalf("Evaluate() = %q, want delivery evidence violation", got)
	}
}

func TestWorkClosePolicyShippedFailsClosedWhenEventReaderUnavailable(t *testing.T) {
	commit := strings.Repeat("a", 40)
	bead := beads.Bead{ID: "ga-1", Status: "closed", Type: "task", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey:          beadmeta.WorkOutcomeShipped,
		beadmeta.WorkCommitMetadataKey:           commit,
		beadmeta.WorkBranchMetadataKey:           "main",
		beadmeta.DeliveryStateMetadataKey:        "landed",
		beadmeta.DeliveryEventIDMetadataKey:      "gcl-" + strings.Repeat("b", 64),
		beadmeta.DeliverySourceCommitMetadataKey: commit,
		beadmeta.DeliveryLandedSHAMetadataKey:    strings.Repeat("c", 40),
	}}
	p := WorkClosePolicy{CommitReachable: func(_, _ string) bool { return true }}
	got := strings.Join(p.Evaluate(Request{Current: bead, ProspectiveStatus: bead.Status, ProspectiveMetadata: bead.Metadata, StoreRef: "city:test"}), "\n")
	if !strings.Contains(got, "event journal is unavailable") {
		t.Fatalf("Evaluate() = %q, want unreadable event refusal", got)
	}
}
