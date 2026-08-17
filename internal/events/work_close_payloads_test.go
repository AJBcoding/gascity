package events

import (
	"encoding/json"
	"errors"
	"testing"
)

type bestEffortOnlyProvider struct{ Provider }

type unreadableAcknowledgedProvider struct{ *Fake }

func (p unreadableAcknowledgedProvider) List(Filter) ([]Event, error) {
	return nil, errors.New("readback unavailable")
}

func TestRecordWorkCloseWarnOnlyUseRequiresAcknowledgedUniqueReadback(t *testing.T) {
	provider := NewFake()
	payload := WorkCloseWarnOnlyUsedPayload{Route: "gc.bd", StoreRef: "city:test", BeadID: "ga-1"}
	first, err := RecordWorkCloseWarnOnlyUse(provider, payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecordWorkCloseWarnOnlyUse(provider, payload)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second == "" || first == second {
		t.Fatalf("usage identities = %q, %q; want two non-empty unique ids", first, second)
	}
	eventsFound, err := provider.List(Filter{Type: WorkCloseWarnOnlyUsed, Subject: payload.BeadID})
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsFound) != 2 {
		t.Fatalf("events = %d, want 2", len(eventsFound))
	}
	var decoded WorkCloseWarnOnlyUsedPayload
	if err := json.Unmarshal(eventsFound[0].Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.UsageID != first {
		t.Fatalf("readback usage_id = %q, want %q", decoded.UsageID, first)
	}
}

func TestRecordWorkCloseWarnOnlyUseRejectsUnconfirmedPersistence(t *testing.T) {
	payload := WorkCloseWarnOnlyUsedPayload{Route: "api", StoreRef: "rig:alpha", BeadID: "ga-2"}
	for _, test := range []struct {
		name     string
		provider Provider
	}{
		{name: "nil provider"},
		{name: "best effort only", provider: bestEffortOnlyProvider{Provider: NewFake()}},
		{name: "append failure", provider: NewFailFake()},
		{name: "readback failure", provider: unreadableAcknowledgedProvider{Fake: NewFake()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if usageID, err := RecordWorkCloseWarnOnlyUse(test.provider, payload); err == nil {
				t.Fatalf("RecordWorkCloseWarnOnlyUse = %q, nil; want refusal", usageID)
			}
		})
	}
}
