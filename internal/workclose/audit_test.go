package workclose

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

type listErrorStore struct {
	beads.Store
	err error
}

func (s listErrorStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	rows, _ := s.Store.List(query)
	return rows, s.err
}

func TestAuditShippedGroupsCanonicalStoresAndReportsPartialErrors(t *testing.T) {
	const eventID = "gcl-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	commit, landed := strings.Repeat("a", 40), strings.Repeat("b", 40)
	valid := beads.Bead{ID: "ga-valid", Type: "task", Status: "closed", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
		beadmeta.WorkCommitMetadataKey:  commit, beadmeta.DeliveryStateMetadataKey: "landed",
		beadmeta.DeliveryEventIDMetadataKey: eventID, beadmeta.DeliverySourceCommitMetadataKey: commit,
		beadmeta.DeliveryLandedSHAMetadataKey: landed,
	}}
	invalid := beads.Bead{ID: "ga-invalid", Type: "task", Status: "open", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
	}}
	partial := beads.Bead{ID: "ga-partial", Type: "task", Status: "closed", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
	}}
	ignored := beads.Bead{ID: "ga-ignored", Type: "task", Status: "in_progress", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
	}}
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID: eventID, ObservedLandedSHA: landed,
		WorkRecords: []events.DeliveryWorkRecordRef{{StoreRef: "city:test", BeadID: valid.ID, WorkCommit: commit}},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{Type: events.DeliveryLanded, Subject: eventID, Payload: payload})

	report := AuditShipped(journal, []AuditStore{
		{StoreRef: "city:test", Store: beads.NewMemStoreFrom(1, []beads.Bead{valid, invalid, ignored}, nil)},
		{StoreRef: "rig:alpha", Store: listErrorStore{Store: beads.NewMemStoreFrom(1, []beads.Bead{partial}, nil), err: errors.New("rig offline")}},
	})
	if report.Complete || report.Clean() {
		t.Fatalf("report complete/clean = %v/%v, want false/false: %+v", report.Complete, report.Clean(), report)
	}
	if len(report.Groups) != 2 || report.Groups[0].StoreRef != "city:test" || report.Groups[1].StoreRef != "rig:alpha" {
		t.Fatalf("groups = %+v, want stable canonical city/rig groups", report.Groups)
	}
	if got := report.Groups[0].Findings; len(got) != 2 || got[0].BeadID != ignored.ID || got[1].BeadID != invalid.ID || len(got[0].Violations) == 0 {
		t.Fatalf("city findings = %+v, want invalid non-closed and closed/open exact-shipped tasks", got)
	}
	if report.Groups[1].Error == "" || !strings.Contains(report.Groups[1].Error, "rig offline") {
		t.Fatalf("rig partial error = %q", report.Groups[1].Error)
	}
	if got := report.Groups[1].Findings; len(got) != 1 || got[0].BeadID != partial.ID {
		t.Fatalf("rig partial findings = %+v, want usable row retained alongside error", got)
	}
}
