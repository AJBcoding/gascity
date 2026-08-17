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
			if got := p.Evaluate(Request{Current: bead, ProspectiveType: bead.Type, ProspectiveStatus: bead.Status, ProspectiveMetadata: bead.Metadata, StoreRef: "city:test"}); len(got) != 0 {
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
	got := strings.Join(p.Evaluate(Request{Current: bead, ProspectiveType: bead.Type, ProspectiveStatus: bead.Status, ProspectiveMetadata: bead.Metadata, StoreRef: "city:test"}), "\n")
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
	got := strings.Join(p.Evaluate(Request{Current: bead, ProspectiveType: bead.Type, ProspectiveStatus: bead.Status, ProspectiveMetadata: bead.Metadata, StoreRef: "city:test"}), "\n")
	if !strings.Contains(got, "event journal is unavailable") {
		t.Fatalf("Evaluate() = %q, want unreadable event refusal", got)
	}
}

func TestWorkClosePolicyScopesCurrentOrProspectiveType(t *testing.T) {
	shipped := beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}
	task := "task"
	convoy := "convoy"
	p := WorkClosePolicy{}
	for _, tc := range []struct {
		name    string
		current beads.Bead
		opts    beads.UpdateOpts
	}{
		{
			name:    "atomic non-task to task close",
			current: beads.Bead{ID: "ga-1", Type: "convoy", Status: "open"},
			opts:    beads.UpdateOpts{Type: &task, Status: stringPointer("closed"), Metadata: shipped},
		},
		{
			name:    "current task remains scoped when becoming non-task",
			current: beads.Bead{ID: "ga-2", Type: "task", Status: "open"},
			opts:    beads.UpdateOpts{Type: &convoy, Status: stringPointer("closed"), Metadata: shipped},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prospective := ProjectUpdate(tc.current, tc.opts)
			if prospective.Type == tc.current.Type {
				t.Fatalf("ProjectUpdate type = %q, want projected type", prospective.Type)
			}
			got := p.Evaluate(Request{
				Current:             tc.current,
				ProspectiveType:     prospective.Type,
				ProspectiveStatus:   prospective.Status,
				ProspectiveMetadata: prospective.Metadata,
				StoreRef:            "city:test",
			})
			if len(got) == 0 {
				t.Fatal("Evaluate allowed shipped current-or-prospective work record without evidence")
			}
		})
	}
}

func stringPointer(value string) *string { return &value }
