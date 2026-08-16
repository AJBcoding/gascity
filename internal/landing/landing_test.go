package landing

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
)

type fakeObserver struct {
	observation    RemoteObservation
	err            error
	calls          int
	order          *[]string
	repositoryPath string
	remote         string
	targetRef      string
}

func (f *fakeObserver) Observe(_ context.Context, repositoryPath, remote, targetRef string) (RemoteObservation, error) {
	f.calls++
	f.repositoryPath = repositoryPath
	f.remote = remote
	f.targetRef = targetRef
	if f.order != nil {
		*f.order = append(*f.order, "observe")
	}
	return f.observation, f.err
}

type fakeJournal struct {
	events  []events.Event
	drop    bool
	listErr error
	records int
	order   *[]string
}

func (f *fakeJournal) Record(event events.Event) {
	f.records++
	if f.order != nil {
		*f.order = append(*f.order, "record")
	}
	if !f.drop {
		f.events = append(f.events, event)
	}
}

func (f *fakeJournal) List(filter events.Filter) ([]events.Event, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return events.ApplyFilter(f.events, filter), nil
}

func validRequest(t *testing.T) RecordRequest {
	t.Helper()
	root := t.TempDir()
	return RecordRequest{
		WorkflowID:            "build-1",
		IntegrationAttemptID:  "attempt-1",
		RepositoryPath:        root,
		Repository:            "https://example.invalid/acme/repo.git",
		Remote:                "origin",
		TargetRef:             "refs/heads/main",
		ExpectedTargetSHA:     shaA,
		ApprovedCandidateSHA:  shaB,
		ExpectedLandedSHA:     shaB,
		PublicationMode:       "direct",
		IntegrationResultPath: filepath.Join(root, "integration-result.md"),
		IntegrationResultHash: "sha256:" + strings.Repeat("c", 64),
		WorkBeadIDs:           []string{"gc-a", "gc-b"},
		Actor:                 "gc.publisher",
	}
}

func serviceFor(request RecordRequest, journal *fakeJournal) (*Service, *fakeObserver) {
	observer := &fakeObserver{observation: RemoteObservation{
		Repository: request.Repository,
		SHA:        request.ExpectedLandedSHA,
	}}
	return &Service{
		Observer: observer,
		Journal:  journal,
		Now:      func() time.Time { return time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC) },
	}, observer
}

func TestRecordRejectsMalformedOrOversizedReceiptBeforeObservation(t *testing.T) {
	tooManyWorkIDs := make([]string, MaxWorkBeadIDs+1)
	for i := range tooManyWorkIDs {
		tooManyWorkIDs[i] = "gc-work-" + strings.Repeat("x", i%10+1)
	}

	tests := []struct {
		name   string
		mutate func(*RecordRequest)
	}{
		{name: "empty required scalar", mutate: func(r *RecordRequest) { r.WorkflowID = "" }},
		{name: "invalid utf8", mutate: func(r *RecordRequest) { r.WorkflowID = string([]byte{0xff}) }},
		{name: "hidden whitespace", mutate: func(r *RecordRequest) { r.WorkflowID = " build-1" }},
		{name: "unsafe remote", mutate: func(r *RecordRequest) { r.Remote = "-upload-pack=bad" }},
		{name: "non-head target", mutate: func(r *RecordRequest) { r.TargetRef = "refs/tags/v1" }},
		{name: "malformed target sha", mutate: func(r *RecordRequest) { r.ExpectedTargetSHA = "ABC" }},
		{name: "malformed candidate sha", mutate: func(r *RecordRequest) { r.ApprovedCandidateSHA = strings.Repeat("A", 40) }},
		{name: "malformed landed sha", mutate: func(r *RecordRequest) { r.ExpectedLandedSHA = "deadbeef" }},
		{name: "malformed result hash", mutate: func(r *RecordRequest) { r.IntegrationResultHash = strings.Repeat("c", 64) }},
		{name: "relative repository path", mutate: func(r *RecordRequest) { r.RepositoryPath = "repo" }},
		{name: "relative result path", mutate: func(r *RecordRequest) { r.IntegrationResultPath = "result.md" }},
		{name: "duplicate work id", mutate: func(r *RecordRequest) { r.WorkBeadIDs = []string{"gc-a", "gc-a"} }},
		{name: "empty work id", mutate: func(r *RecordRequest) { r.WorkBeadIDs = []string{"gc-a", ""} }},
		{name: "too many work ids", mutate: func(r *RecordRequest) { r.WorkBeadIDs = tooManyWorkIDs }},
		{name: "oversized scalar", mutate: func(r *RecordRequest) { r.WorkflowID = strings.Repeat("x", 257) }},
		{name: "unsupported mode", mutate: func(r *RecordRequest) { r.PublicationMode = "magic" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := validRequest(t)
			tt.mutate(&request)
			journal := &fakeJournal{}
			service, observer := serviceFor(request, journal)

			if _, err := service.Record(context.Background(), request); err == nil {
				t.Fatal("Record error = nil")
			}
			if observer.calls != 0 {
				t.Fatalf("observer calls = %d, want 0", observer.calls)
			}
			if journal.records != 0 {
				t.Fatalf("journal records = %d, want 0", journal.records)
			}
		})
	}
}

func TestRecordObservesRemoteBeforeEmittingTypedEvent(t *testing.T) {
	request := validRequest(t)
	var order []string
	journal := &fakeJournal{order: &order}
	service, observer := serviceFor(request, journal)
	observer.order = &order

	result, err := service.Record(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(order, ","), "observe,record"; got != want {
		t.Fatalf("call order = %q, want %q", got, want)
	}
	if observer.repositoryPath != request.RepositoryPath || observer.remote != request.Remote || observer.targetRef != request.TargetRef {
		t.Fatalf("observer arguments = (%q, %q, %q)", observer.repositoryPath, observer.remote, observer.targetRef)
	}
	if len(journal.events) != 1 {
		t.Fatalf("events = %d, want 1", len(journal.events))
	}
	event := journal.events[0]
	if event.Type != events.DeliveryLanded || event.Subject != result.EventID || event.RunID != request.WorkflowID {
		t.Fatalf("event envelope = %#v", event)
	}
	decoded, ok, err := events.DecodePayload(event.Type, event.Payload)
	if err != nil || !ok {
		t.Fatalf("DecodePayload ok=%v err=%v", ok, err)
	}
	payload := decoded.(events.DeliveryLandedPayload)
	if payload.EventID != result.EventID || payload.ObservedLandedSHA != shaB || payload.VerifiedAt != "2026-08-16T20:00:00Z" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestRecordDirectModeRequiresCandidateAtRemote(t *testing.T) {
	request := validRequest(t)
	request.ApprovedCandidateSHA = shaC
	journal := &fakeJournal{}
	service, observer := serviceFor(request, journal)

	if _, err := service.Record(context.Background(), request); err == nil {
		t.Fatal("Record error = nil")
	}
	if observer.calls != 1 {
		t.Fatalf("observer calls = %d, want 1", observer.calls)
	}
	if journal.records != 0 {
		t.Fatalf("journal records = %d, want 0", journal.records)
	}
}

func TestRecordPullRequestModeAllowsExactServerGeneratedLanding(t *testing.T) {
	request := validRequest(t)
	request.PublicationMode = "pull_request"
	request.ExpectedLandedSHA = shaC
	journal := &fakeJournal{}
	service, _ := serviceFor(request, journal)

	result, err := service.Record(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservedLandedSHA != shaC || len(journal.events) != 1 {
		t.Fatalf("result=%#v events=%d", result, len(journal.events))
	}
}

func TestRecordRejectsObservedTargetMismatchWithoutEvent(t *testing.T) {
	request := validRequest(t)
	journal := &fakeJournal{}
	service, observer := serviceFor(request, journal)
	observer.observation.SHA = shaC

	if _, err := service.Record(context.Background(), request); err == nil {
		t.Fatal("Record error = nil")
	}
	if journal.records != 0 {
		t.Fatalf("journal records = %d, want 0", journal.records)
	}
}

func TestRecordRejectsObservedRepositoryMismatchWithoutEvent(t *testing.T) {
	request := validRequest(t)
	journal := &fakeJournal{}
	service, observer := serviceFor(request, journal)
	observer.observation.Repository = "https://example.invalid/other/repo.git"

	if _, err := service.Record(context.Background(), request); err == nil {
		t.Fatal("Record error = nil")
	}
	if journal.records != 0 {
		t.Fatalf("journal records = %d, want 0", journal.records)
	}
}

func TestRecordSequentialReplayReobservesAndDoesNotAppend(t *testing.T) {
	request := validRequest(t)
	journal := &fakeJournal{}
	service, observer := serviceFor(request, journal)

	first, err := service.Record(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Record(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.EventID != second.EventID || !second.AlreadyRecorded {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if observer.calls != 2 {
		t.Fatalf("observer calls = %d, want 2", observer.calls)
	}
	if journal.records != 1 || len(journal.events) != 1 {
		t.Fatalf("records=%d events=%d, want 1", journal.records, len(journal.events))
	}
}

func TestRecordFailsWhenRecordedEventCannotBeReadBack(t *testing.T) {
	request := validRequest(t)
	journal := &fakeJournal{drop: true}
	service, _ := serviceFor(request, journal)

	if _, err := service.Record(context.Background(), request); err == nil {
		t.Fatal("Record error = nil")
	}
	if journal.records != 1 {
		t.Fatalf("journal records = %d, want 1 attempted record", journal.records)
	}
}

func TestRecordEventIDIsStableAcrossActorAndClockChanges(t *testing.T) {
	firstRequest := validRequest(t)
	firstService, _ := serviceFor(firstRequest, &fakeJournal{})
	first, err := firstService.Record(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}

	secondRequest := firstRequest
	secondRequest.Actor = "different-actor"
	secondJournal := &fakeJournal{}
	secondService, _ := serviceFor(secondRequest, secondJournal)
	secondService.Now = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	second, err := secondService.Record(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}

	if first.EventID != second.EventID {
		t.Fatalf("event IDs differ: %q != %q", first.EventID, second.EventID)
	}
	if !strings.HasPrefix(first.EventID, "gcl-") || len(first.EventID) != len("gcl-")+64 {
		t.Fatalf("event ID = %q", first.EventID)
	}
}

func TestRecordPropagatesObserverAndJournalReadErrors(t *testing.T) {
	request := validRequest(t)
	observerFailure := errors.New("remote unavailable")
	journal := &fakeJournal{}
	service, observer := serviceFor(request, journal)
	observer.err = observerFailure
	if _, err := service.Record(context.Background(), request); !errors.Is(err, observerFailure) {
		t.Fatalf("observer error = %v", err)
	}

	journalFailure := errors.New("journal unavailable")
	service, _ = serviceFor(request, &fakeJournal{listErr: journalFailure})
	if _, err := service.Record(context.Background(), request); !errors.Is(err, journalFailure) {
		t.Fatalf("journal error = %v", err)
	}
}

func TestRecordedPayloadIsValidJSON(t *testing.T) {
	request := validRequest(t)
	journal := &fakeJournal{}
	service, _ := serviceFor(request, journal)
	if _, err := service.Record(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(journal.events[0].Payload) {
		t.Fatalf("payload = %q", journal.events[0].Payload)
	}
}
