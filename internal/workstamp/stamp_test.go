package workstamp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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

type inFlightEvidenceReader struct {
	events        []events.Event
	listCalls     int
	inFlightCalls int
}

func (r *inFlightEvidenceReader) List(events.Filter) ([]events.Event, error) {
	r.listCalls++
	return nil, nil
}

func (r *inFlightEvidenceReader) ListInFlight(filter events.Filter) ([]events.Event, error) {
	r.inFlightCalls++
	return events.ApplyFilter(r.events, filter), nil
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
	return workStoreWithMetadata(t, beads.StringMap{"gc.work_commit": workCommit, "gc.work_base_commit": "base-unchanged"})
}

func workStoreWithMetadata(t *testing.T, metadata beads.StringMap) *beads.MemStore {
	t.Helper()
	store := beads.NewMemStore()
	store.HonorExplicitIDs = true
	if _, err := store.Create(beads.Bead{
		ID:       "gc-same",
		Title:    "source work",
		Status:   "in_progress",
		Metadata: metadata,
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

func TestStampTreatsIntegrationReadyAnchorAsStampable(t *testing.T) {
	alpha := workStoreWithMetadata(t, beads.StringMap{
		"gc.work_commit":      testWorkCommitA,
		"gc.work_base_commit": "base-unchanged",
		"gc.delivery_state":   "integration_ready",
	})
	beta := workStore(t, testWorkCommitB)
	service := Service{Journal: landingJournal(t), Stores: &mapResolver{stores: map[string]beads.Store{
		"rig:alpha": alpha,
		"rig:beta":  beta,
	}}}

	result, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stamped != 2 || result.AlreadyStamped != 0 || len(result.Conflicts) != 0 {
		t.Fatalf("result = %#v", result)
	}
	bead, err := alpha.Get("gc-same")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"gc.delivery_state":         "landed",
		"gc.delivery_event_id":      testLandingEventID,
		"gc.delivery_repository":    "https://example.invalid/acme/repo.git",
		"gc.delivery_target_ref":    "refs/heads/main",
		"gc.delivery_landed_sha":    testLandedSHA,
		"gc.delivery_verified_at":   "2026-08-16T21:00:00Z",
		"gc.delivery_source_commit": testWorkCommitA,
	}
	for key, value := range want {
		if bead.Metadata[key] != value {
			t.Errorf("%s = %q, want %q", key, bead.Metadata[key], value)
		}
	}
	if bead.Metadata["gc.work_commit"] != testWorkCommitA || bead.Metadata["gc.work_base_commit"] != "base-unchanged" {
		t.Errorf("source provenance changed: %#v", bead.Metadata)
	}
	if bead.Status != "open" {
		t.Errorf("status = %q, want open", bead.Status)
	}
}

func TestStampReplayOverIntegrationReadyAnchorIsIdempotent(t *testing.T) {
	alpha := workStoreWithMetadata(t, beads.StringMap{
		"gc.work_commit":      testWorkCommitA,
		"gc.work_base_commit": "base-unchanged",
		"gc.delivery_state":   "integration_ready",
	})
	beta := workStore(t, testWorkCommitB)
	service := Service{Journal: landingJournal(t), Stores: &mapResolver{stores: map[string]beads.Store{
		"rig:alpha": alpha,
		"rig:beta":  beta,
	}}}

	if _, err := service.Stamp(context.Background(), testLandingEventID); err != nil {
		t.Fatal(err)
	}
	replay, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Stamped != 0 || replay.AlreadyStamped != 2 || len(replay.Conflicts) != 0 {
		t.Fatalf("replay result = %#v", replay)
	}
	stampedEvents, err := service.Journal.List(events.Filter{Type: events.DeliveryWorkStamped})
	if err != nil {
		t.Fatal(err)
	}
	if len(stampedEvents) != 2 {
		t.Fatalf("replay duplicated work-stamped events: %d", len(stampedEvents))
	}
}

func TestStampStillConflictsOnDeliveryMetadataFromAnotherEvent(t *testing.T) {
	for name, metadata := range map[string]beads.StringMap{
		"different event id": {
			"gc.work_commit":       testWorkCommitA,
			"gc.work_base_commit":  "base-unchanged",
			"gc.delivery_state":    "landed",
			"gc.delivery_event_id": "gcl-" + strings.Repeat("f", 64),
		},
		"different landed sha beside pre-landing state": {
			"gc.work_commit":         testWorkCommitA,
			"gc.work_base_commit":    "base-unchanged",
			"gc.delivery_state":      "integration_ready",
			"gc.delivery_landed_sha": "4444444444444444444444444444444444444444",
		},
	} {
		t.Run(name, func(t *testing.T) {
			alpha := workStoreWithMetadata(t, metadata)
			beta := workStore(t, testWorkCommitB)
			journal := landingJournal(t)
			service := Service{Journal: journal, Stores: &mapResolver{stores: map[string]beads.Store{
				"rig:alpha": alpha,
				"rig:beta":  beta,
			}}}
			before, err := alpha.Get("gc-same")
			if err != nil {
				t.Fatal(err)
			}

			result, err := service.Stamp(context.Background(), testLandingEventID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Stamped != 1 || result.AlreadyStamped != 0 || len(result.Conflicts) != 1 {
				t.Fatalf("result = %#v", result)
			}
			if result.Conflicts[0].StoreRef != "rig:alpha" || result.Conflicts[0].Code != "delivery_metadata_mismatch" {
				t.Fatalf("conflict = %#v", result.Conflicts[0])
			}
			after, err := alpha.Get("gc-same")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after.Metadata, before.Metadata) {
				t.Fatalf("conflicting evidence was overwritten: %#v", after.Metadata)
			}
			stampEvents, err := journal.List(events.Filter{Type: events.DeliveryWorkStamped})
			if err != nil {
				t.Fatal(err)
			}
			if len(stampEvents) != 1 {
				t.Fatalf("stamp events = %d, want only beta", len(stampEvents))
			}
		})
	}
}

func TestStampConflictsOnUnrecognizedDeliveryStateValue(t *testing.T) {
	alpha := workStoreWithMetadata(t, beads.StringMap{
		"gc.work_commit":      testWorkCommitA,
		"gc.work_base_commit": "base-unchanged",
		"gc.delivery_state":   "ready-to-integrate",
	})
	beta := workStore(t, testWorkCommitB)
	service := Service{Journal: landingJournal(t), Stores: &mapResolver{stores: map[string]beads.Store{
		"rig:alpha": alpha,
		"rig:beta":  beta,
	}}}

	result, err := service.Stamp(context.Background(), testLandingEventID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stamped != 1 || len(result.Conflicts) != 1 || result.Conflicts[0].Code != "delivery_metadata_mismatch" {
		t.Fatalf("result = %#v", result)
	}
	bead, err := alpha.Get("gc-same")
	if err != nil {
		t.Fatal(err)
	}
	if bead.Metadata["gc.delivery_state"] != "ready-to-integrate" {
		t.Fatalf("unrecognized delivery state was overwritten: %#v", bead.Metadata)
	}
}

func TestHasDeliveryMetadata(t *testing.T) {
	for name, tc := range map[string]struct {
		metadata beads.StringMap
		want     bool
	}{
		"no metadata":        {metadata: beads.StringMap{}, want: false},
		"non-delivery keys":  {metadata: beads.StringMap{"gc.work_commit": testWorkCommitA, "gc.work_base_commit": "base-unchanged"}, want: false},
		"delivery state key": {metadata: beads.StringMap{"gc.delivery_state": "integration_ready"}, want: true},
		"delivery event key": {metadata: beads.StringMap{"gc.work_commit": testWorkCommitA, "gc.delivery_event_id": testLandingEventID}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := hasDeliveryMetadata(tc.metadata); got != tc.want {
				t.Fatalf("hasDeliveryMetadata(%#v) = %t, want %t", tc.metadata, got, tc.want)
			}
		})
	}
}

func TestValidateLandingEvidence(t *testing.T) {
	baseMetadata := beads.StringMap{
		"gc.work_outcome":           "shipped",
		"gc.work_commit":            testWorkCommitA,
		"gc.delivery_state":         "landed",
		"gc.delivery_event_id":      testLandingEventID,
		"gc.delivery_source_commit": testWorkCommitA,
		"gc.delivery_landed_sha":    testLandedSHA,
	}
	matchingRecord := events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA}
	tests := []struct {
		name       string
		mutate     func(beads.StringMap)
		journal    func(t *testing.T) EvidenceReader
		wantDetail string
	}{
		{
			name:       "missing event ID",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_event_id"] = "" },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_event_id=gcl-<64 lowercase hex>",
		},
		{
			name:       "malformed event ID",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_event_id"] = "gcl-not-hex" },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_event_id=gcl-<64 lowercase hex>",
		},
		{
			name:       "missing delivery state",
			mutate:     func(metadata beads.StringMap) { delete(metadata, "gc.delivery_state") },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_state=landed",
		},
		{
			name:       "missing source commit",
			mutate:     func(metadata beads.StringMap) { delete(metadata, "gc.delivery_source_commit") },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_source_commit must equal gc.work_commit",
		},
		{
			name:       "missing landed SHA",
			mutate:     func(metadata beads.StringMap) { delete(metadata, "gc.delivery_landed_sha") },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_landed_sha",
		},
		{
			name:       "source commit mismatch",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_source_commit"] = testWorkCommitB },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_source_commit must equal gc.work_commit",
		},
		{
			name:       "landed SHA mismatch",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_landed_sha"] = testWorkCommitB },
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "gc.delivery_landed_sha does not match",
		},
		{
			name:       "unreadable event",
			mutate:     func(beads.StringMap) {},
			journal:    func(*testing.T) EvidenceReader { return events.NewFailFake() },
			wantDetail: "is unreadable",
		},
		{
			name:   "event bound to another bead",
			mutate: func(beads.StringMap) {},
			journal: func(t *testing.T) EvidenceReader {
				return landingEvidenceJournal(t, events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "other", WorkCommit: testWorkCommitA})
			},
			wantDetail: "is not bound to store \"rig:alpha\" bead \"gc-same\"",
		},
		{
			name:   "event bound to another store",
			mutate: func(beads.StringMap) {},
			journal: func(t *testing.T) EvidenceReader {
				return landingEvidenceJournal(t, events.DeliveryWorkRecordRef{StoreRef: "rig:beta", BeadID: "gc-same", WorkCommit: testWorkCommitA})
			},
			wantDetail: "is not bound to store \"rig:alpha\" bead \"gc-same\"",
		},
		{
			name:       "valid replay",
			mutate:     func(beads.StringMap) {},
			journal:    func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantDetail: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := make(beads.StringMap, len(baseMetadata))
			for key, value := range baseMetadata {
				metadata[key] = value
			}
			tc.mutate(metadata)
			violations := ValidateLandingEvidence(tc.journal(t), "rig:alpha", beads.Bead{ID: "gc-same", Metadata: metadata})
			if tc.wantDetail == "" {
				if len(violations) != 0 {
					t.Fatalf("violations = %v, want none", violations)
				}
				return
			}
			if len(violations) != 1 || !strings.Contains(violations[0], tc.wantDetail) {
				t.Fatalf("violations = %v, want detail %q", violations, tc.wantDetail)
			}
		})
	}
}

// TestClassifyLandingEvidenceSeparatesUnreadableFromInvalid pins the typed
// violation surface: evidence-read failures (infrastructure) classify as
// unreadable, everything else — absent, malformed, or foreign-bound evidence —
// classifies as invalid, and the messages stay identical to
// ValidateLandingEvidence so both surfaces describe one refusal.
func TestClassifyLandingEvidenceSeparatesUnreadableFromInvalid(t *testing.T) {
	baseMetadata := beads.StringMap{
		"gc.work_outcome":           "shipped",
		"gc.work_commit":            testWorkCommitA,
		"gc.delivery_state":         "landed",
		"gc.delivery_event_id":      testLandingEventID,
		"gc.delivery_source_commit": testWorkCommitA,
		"gc.delivery_landed_sha":    testLandedSHA,
	}
	matchingRecord := events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA}
	tests := []struct {
		name      string
		mutate    func(beads.StringMap)
		journal   func(t *testing.T) EvidenceReader
		wantClass EvidenceClass
	}{
		{
			name:      "journal read failure",
			mutate:    func(beads.StringMap) {},
			journal:   func(*testing.T) EvidenceReader { return events.NewFailFake() },
			wantClass: EvidenceUnreadable,
		},
		{
			name:      "nil journal",
			mutate:    func(beads.StringMap) {},
			journal:   func(*testing.T) EvidenceReader { return nil },
			wantClass: EvidenceUnreadable,
		},
		{
			name:      "absent landing event",
			mutate:    func(beads.StringMap) {},
			journal:   func(*testing.T) EvidenceReader { return events.NewFake() },
			wantClass: EvidenceInvalid,
		},
		{
			name:      "metadata precheck failure",
			mutate:    func(metadata beads.StringMap) { delete(metadata, "gc.delivery_state") },
			journal:   func(t *testing.T) EvidenceReader { return landingEvidenceJournal(t, matchingRecord) },
			wantClass: EvidenceInvalid,
		},
		{
			name:   "event bound to another bead",
			mutate: func(beads.StringMap) {},
			journal: func(t *testing.T) EvidenceReader {
				return landingEvidenceJournal(t, events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "other", WorkCommit: testWorkCommitA})
			},
			wantClass: EvidenceInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := make(beads.StringMap, len(baseMetadata))
			for key, value := range baseMetadata {
				metadata[key] = value
			}
			tc.mutate(metadata)
			journal := tc.journal(t)
			bead := beads.Bead{ID: "gc-same", Metadata: metadata}
			classified := ClassifyLandingEvidence(journal, "rig:alpha", bead)
			if len(classified) != 1 || classified[0].Class != tc.wantClass {
				t.Fatalf("classified = %#v, want one violation of class %q", classified, tc.wantClass)
			}
			flat := ValidateLandingEvidence(journal, "rig:alpha", bead)
			if len(flat) != 1 || flat[0] != classified[0].Message {
				t.Fatalf("ValidateLandingEvidence = %v, want the classified message %q", flat, classified[0].Message)
			}
		})
	}
}

func TestClassifyLandingEvidenceReportsNoViolationForValidReplay(t *testing.T) {
	metadata := beads.StringMap{
		"gc.work_outcome":           "shipped",
		"gc.work_commit":            testWorkCommitA,
		"gc.delivery_state":         "landed",
		"gc.delivery_event_id":      testLandingEventID,
		"gc.delivery_source_commit": testWorkCommitA,
		"gc.delivery_landed_sha":    testLandedSHA,
	}
	journal := landingEvidenceJournal(t, events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA})
	classified := ClassifyLandingEvidence(journal, "rig:alpha", beads.Bead{ID: "gc-same", Metadata: metadata})
	if len(classified) != 0 {
		t.Fatalf("classified = %#v, want none", classified)
	}
}

func TestValidateLandingEvidenceRejectsNonCanonicalMetadata(t *testing.T) {
	baseMetadata := beads.StringMap{
		"gc.work_outcome":           "shipped",
		"gc.work_commit":            testWorkCommitA,
		"gc.delivery_state":         "landed",
		"gc.delivery_event_id":      testLandingEventID,
		"gc.delivery_source_commit": testWorkCommitA,
		"gc.delivery_landed_sha":    testLandedSHA,
	}
	tests := []struct {
		name       string
		mutate     func(beads.StringMap)
		record     events.DeliveryWorkRecordRef
		landedSHA  string
		wantDetail string
	}{
		{
			name: "uppercase work commit",
			mutate: func(metadata beads.StringMap) {
				metadata["gc.work_commit"] = strings.ToUpper(strings.Repeat("a", 40))
				metadata["gc.delivery_source_commit"] = strings.ToUpper(strings.Repeat("a", 40))
			},
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: strings.ToUpper(strings.Repeat("a", 40))},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.work_commit",
		},
		{
			name: "non-hex work commit",
			mutate: func(metadata beads.StringMap) {
				metadata["gc.work_commit"] = strings.Repeat("g", 40)
				metadata["gc.delivery_source_commit"] = strings.Repeat("g", 40)
			},
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: strings.Repeat("g", 40)},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.work_commit",
		},
		{
			name: "padded work commit",
			mutate: func(metadata beads.StringMap) {
				metadata["gc.work_commit"] = " " + testWorkCommitA + " "
				metadata["gc.delivery_source_commit"] = " " + testWorkCommitA + " "
			},
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.work_commit",
		},
		{
			name:       "padded delivery state",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_state"] = " landed " },
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.delivery_state",
		},
		{
			name:       "padded event ID",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_event_id"] = " " + testLandingEventID + " " },
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.delivery_event_id",
		},
		{
			name:       "padded source commit",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_source_commit"] = " " + testWorkCommitA + " " },
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.delivery_source_commit",
		},
		{
			name: "uppercase landed SHA",
			mutate: func(metadata beads.StringMap) {
				metadata["gc.delivery_landed_sha"] = strings.ToUpper(strings.Repeat("a", 40))
			},
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  strings.ToUpper(strings.Repeat("a", 40)),
			wantDetail: "gc.delivery_landed_sha",
		},
		{
			name:       "non-hex landed SHA",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_landed_sha"] = strings.Repeat("g", 40) },
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  strings.Repeat("g", 40),
			wantDetail: "gc.delivery_landed_sha",
		},
		{
			name:       "padded landed SHA",
			mutate:     func(metadata beads.StringMap) { metadata["gc.delivery_landed_sha"] = " " + testLandedSHA + " " },
			record:     events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA},
			landedSHA:  testLandedSHA,
			wantDetail: "gc.delivery_landed_sha",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := make(beads.StringMap, len(baseMetadata))
			for key, value := range baseMetadata {
				metadata[key] = value
			}
			tc.mutate(metadata)
			journal := landingEvidenceJournalWith(t, testLandingEventID, tc.landedSHA, tc.record)
			violations := ValidateLandingEvidence(journal, "rig:alpha", beads.Bead{ID: "gc-same", Metadata: metadata})
			if len(violations) != 1 || !strings.Contains(violations[0], tc.wantDetail) {
				t.Fatalf("violations = %v, want detail %q", violations, tc.wantDetail)
			}
		})
	}
}

func TestValidateLandingEvidenceReadsInFlightLandingEvent(t *testing.T) {
	metadata := beads.StringMap{
		"gc.work_outcome":           "shipped",
		"gc.work_commit":            testWorkCommitA,
		"gc.delivery_state":         "landed",
		"gc.delivery_event_id":      testLandingEventID,
		"gc.delivery_source_commit": testWorkCommitA,
		"gc.delivery_landed_sha":    testLandedSHA,
	}
	journal := landingEvidenceJournal(t, events.DeliveryWorkRecordRef{StoreRef: "rig:alpha", BeadID: "gc-same", WorkCommit: testWorkCommitA})
	stored, err := journal.List(events.Filter{Type: events.DeliveryLanded, Subject: testLandingEventID})
	if err != nil {
		t.Fatal(err)
	}
	reader := &inFlightEvidenceReader{events: stored}
	violations := ValidateLandingEvidence(reader, "rig:alpha", beads.Bead{ID: "gc-same", Metadata: metadata})
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
	if reader.listCalls != 0 || reader.inFlightCalls != 1 {
		t.Fatalf("reader calls = List:%d ListInFlight:%d, want List:0 ListInFlight:1", reader.listCalls, reader.inFlightCalls)
	}
}

func landingEvidenceJournal(t *testing.T, record events.DeliveryWorkRecordRef) *events.Fake {
	return landingEvidenceJournalWith(t, testLandingEventID, testLandedSHA, record)
}

func landingEvidenceJournalWith(t *testing.T, eventID, observedLandedSHA string, record events.DeliveryWorkRecordRef) *events.Fake {
	t.Helper()
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           eventID,
		ObservedLandedSHA: observedLandedSHA,
		WorkRecords:       []events.DeliveryWorkRecordRef{record},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{Type: events.DeliveryLanded, Subject: eventID, Payload: payload})
	return journal
}
