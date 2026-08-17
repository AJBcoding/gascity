package events

// DeliveryWorkStampedPayload records one exact source work record whose bead
// metadata agrees with a durable landing event. EventID is deterministic over
// LandingEventID, StoreRef, BeadID, and WorkCommit.
type DeliveryWorkStampedPayload struct {
	EventID        string `json:"event_id"`
	LandingEventID string `json:"landing_event_id"`
	StoreRef       string `json:"store_ref"`
	BeadID         string `json:"bead_id"`
	WorkCommit     string `json:"work_commit"`
}

// IsEventPayload marks DeliveryWorkStampedPayload as a Payload variant.
func (DeliveryWorkStampedPayload) IsEventPayload() {}

func init() {
	RegisterPayload(DeliveryWorkStamped, DeliveryWorkStampedPayload{})
}
