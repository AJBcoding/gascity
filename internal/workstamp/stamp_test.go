package workstamp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

const (
	testLandingEventID = "gcl-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testWorkCommitA    = "1111111111111111111111111111111111111111"
	testWorkCommitB    = "2222222222222222222222222222222222222222"
	testLandedSHA      = "3333333333333333333333333333333333333333"
)

type mapResolver struct {
	stores map[string]beads.Store
	calls  []string
}

type dropFirstStampJournal struct {
	*events.Fake
	drop bool
}

type staleOnFirstUpdateStore struct {
	beads.Store
	writer beads.ConditionalWriter
	raced  bool
}

func (s *staleOnFirstUpdateStore) UpdateIfMatch(id string, revision int64, opts beads.UpdateOpts) error {
	if !s.raced {
		s.raced = true
		if err := s.Update(id, beads.UpdateOpts{Metadata: beads.StringMap{"concurrent": "write"}}); err != nil {
			return err
		}
	}
	return s.writer.UpdateIfMatch(id, revision, opts)
}

func (s *staleOnFirstUpdateStore) CloseIfMatch(id string, revision int64) error {
	return s.writer.CloseIfMatch(id, revision)
}

func (s *staleOnFirstUpdateStore) DeleteIfMatch(id string, revision int64) error {
	return s.writer.DeleteIfMatch(id, revision)
}

func (s *staleOnFirstUpdateStore) CompareAndSetMetadataKey(id, key, expected, next string) (bool, error) {
	return s.writer.CompareAndSetMetadataKey(id, key, expected, next)
}

func (j *dropFirstStampJournal) Record(event events.Event) {
	if event.Type == events.DeliveryWorkStamped && j.drop {
		j.drop = false
		return
	}
	j.Fake.Record(event)
}

func (r *mapResolver) Resolve(_ context.Context, storeRef string) (beads.Store, error) {
	r.calls = append(r.calls, storeRef)
	return r.stores[storeRef], nil
}

func workStore(t *testing.T, workCommit string) *beads.MemStore {
	t.Helper()
	store := beads.NewMemStore()
	store.HonorExplicitIDs = true
	if _, err := store.Create(beads.Bead{
		ID:       "gc-same",
		Title:    "source work",
		Status:   "in_progress",
		Metadata: beads.StringMap{"gc.work_commit": workCommit, "gc.work_base_commit": "base-unchanged"},
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func landingJournal(t *testing.T) *events.Fake {
	t.Helper()
	verifiedAt := time.Date(2026, 8, 16, 21, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           testLandingEventID,
		Repository:        "https://example.invalid/acme/repo.git",
		TargetRef:         "refs/heads/main",
		ObservedLandedSHA: testLandedSHA,
		WorkRecords: []events.DeliveryWorkRecordRef{
			{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			{StoreRef: "rig:beta", BeadID: "gc-same", WorkCommit: testWorkCommitB},
		},
		VerifiedAt: verifiedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{
		Type:    events.DeliveryLanded,
		Subject: testLandingEventID,
		Payload: payload,
	})
	return journal
}

func TestStampUsesOnlyBoundStoresAndPreservesSourceProvenance(t *testing.T) {
	alpha := workStore(t, testWorkCommitA)
	beta := workStore(t, testWorkCommitB)
	resolver := &mapResolver{stores: map[string]beads.Store{
		"rig:alpha": alpha,
		"rig:beta":  beta,
	}}
	service := Service{Journal: landingJournal(t), Stores: resolver}

	result, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stamped != 2 || result.AlreadyStamped != 0 || len(result.Conflicts) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(resolver.calls, []string{"rig:alpha", "rig:beta"}) {
		t.Fatalf("resolved stores = %v", resolver.calls)
	}

	for storeRef, store := range map[string]*beads.MemStore{"rig:alpha": alpha, "rig:beta": beta} {
		bead, err := store.Get("gc-same")
		if err != nil {
			t.Fatal(err)
		}
		wantSource := map[string]string{"rig:alpha": testWorkCommitA, "rig:beta": testWorkCommitB}[storeRef]
		want := map[string]string{
			"gc.delivery_state":         "landed",
			"gc.delivery_event_id":      testLandingEventID,
			"gc.delivery_repository":    "https://example.invalid/acme/repo.git",
			"gc.delivery_target_ref":    "refs/heads/main",
			"gc.delivery_landed_sha":    testLandedSHA,
			"gc.delivery_verified_at":   "2026-08-16T21:00:00Z",
			"gc.delivery_source_commit": wantSource,
		}
		for key, value := range want {
			if bead.Metadata[key] != value {
				t.Errorf("%s %s = %q, want %q", storeRef, key, bead.Metadata[key], value)
			}
		}
		if bead.Metadata["gc.work_commit"] != wantSource || bead.Metadata["gc.work_base_commit"] != "base-unchanged" {
			t.Errorf("%s source provenance changed: %#v", storeRef, bead.Metadata)
		}
		if bead.Status != "open" {
			t.Errorf("%s status = %q, want open", storeRef, bead.Status)
		}
	}
	stampedEvents, err := service.Journal.List(events.Filter{Type: events.DeliveryWorkStamped})
	if err != nil {
		t.Fatal(err)
	}
	if len(stampedEvents) != 2 {
		t.Fatalf("work-stamped events = %d, want 2", len(stampedEvents))
	}

	replay, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Stamped != 0 || replay.AlreadyStamped != 2 || len(replay.Conflicts) != 0 {
		t.Fatalf("replay result = %#v", replay)
	}
	stampedEvents, err = service.Journal.List(events.Filter{Type: events.DeliveryWorkStamped})
	if err != nil {
		t.Fatal(err)
	}
	if len(stampedEvents) != 2 {
		t.Fatalf("replay duplicated work-stamped events: %d", len(stampedEvents))
	}
}

func TestStampWorkCommitMismatchLeavesThatRecordUntouched(t *testing.T) {
	alpha := workStore(t, "9999999999999999999999999999999999999999")
	beta := workStore(t, testWorkCommitB)
	resolver := &mapResolver{stores: map[string]beads.Store{"rig:alpha": alpha, "rig:beta": beta}}
	journal := landingJournal(t)
	service := Service{Journal: journal, Stores: resolver}

	result, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stamped != 1 || result.AlreadyStamped != 0 || len(result.Conflicts) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Conflicts[0].StoreRef != "rig:alpha" || result.Conflicts[0].Code != "work_commit_mismatch" {
		t.Fatalf("conflict = %#v", result.Conflicts[0])
	}
	untouched, err := alpha.Get("gc-same")
	if err != nil {
		t.Fatal(err)
	}
	if hasDeliveryMetadata(untouched.Metadata) {
		t.Fatalf("mismatched bead was stamped: %#v", untouched.Metadata)
	}
	stampEvents, err := journal.List(events.Filter{Type: events.DeliveryWorkStamped})
	if err != nil {
		t.Fatal(err)
	}
	if len(stampEvents) != 1 {
		t.Fatalf("stamp events = %d, want only beta", len(stampEvents))
	}
}

func TestStampReplayRepairsMetadataWhoseStampEventWasNotRecorded(t *testing.T) {
	alpha := workStore(t, testWorkCommitA)
	beta := workStore(t, testWorkCommitB)
	journal := &dropFirstStampJournal{Fake: landingJournal(t), drop: true}
	service := Service{
		Journal: journal,
		Stores: &mapResolver{stores: map[string]beads.Store{
			"rig:alpha": alpha,
			"rig:beta":  beta,
		}},
	}

	first, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stamped != 1 || len(first.Conflicts) != 1 || first.Conflicts[0].Code != "stamp_event_unreadable" {
		t.Fatalf("first result = %#v", first)
	}

	second, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Stamped != 1 || second.AlreadyStamped != 1 || len(second.Conflicts) != 0 {
		t.Fatalf("repair result = %#v", second)
	}

	third, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if third.Stamped != 0 || third.AlreadyStamped != 2 || len(third.Conflicts) != 0 {
		t.Fatalf("settled replay result = %#v", third)
	}
}

func TestStampStaleRevisionRaceLeavesNoFalseLandedMetadata(t *testing.T) {
	backing := workStore(t, testWorkCommitA)
	racing := &staleOnFirstUpdateStore{Store: backing, writer: backing}
	beta := workStore(t, testWorkCommitB)
	journal := landingJournal(t)
	service := Service{
		Journal: journal,
		Stores: &mapResolver{stores: map[string]beads.Store{
			"rig:alpha": racing,
			"rig:beta":  beta,
		}},
	}

	result, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stamped != 1 || len(result.Conflicts) != 1 || result.Conflicts[0].Code != "conditional_write_failed" {
		t.Fatalf("result = %#v", result)
	}
	alpha, err := backing.Get("gc-same")
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Metadata["concurrent"] != "write" || hasDeliveryMetadata(alpha.Metadata) {
		t.Fatalf("stale race left false delivery metadata: %#v", alpha.Metadata)
	}
}
