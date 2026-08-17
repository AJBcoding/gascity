package events

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDeliveryWorkStampedPayloadIsRegisteredAndRoundTrips(t *testing.T) {
	want := DeliveryWorkStampedPayload{
		EventID:        "gws-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		LandingEventID: "gcl-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		StoreRef:       "rig:alpha",
		BeadID:         "gc-123",
		WorkCommit:     "cccccccccccccccccccccccccccccccccccccccc",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	decoded, registered, err := DecodePayload(DeliveryWorkStamped, raw)
	if err != nil || !registered {
		t.Fatalf("DecodePayload registered=%v err=%v", registered, err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %#v, want %#v", decoded, want)
	}
}
