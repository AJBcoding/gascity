package events

// WorkCloseWarnOnlyUsedPayload records each use of the bounded warn-only close
// path so operators can prove it is unused before deleting the compatibility
// setting at its removal floor.
type WorkCloseWarnOnlyUsedPayload struct {
	Route          string `json:"route"`
	StoreRef       string `json:"store_ref"`
	BeadID         string `json:"bead_id"`
	ViolationCount int    `json:"violation_count"`
	RemovalVersion string `json:"removal_version"`
}

// IsEventPayload marks WorkCloseWarnOnlyUsedPayload as an events.Payload variant.
func (WorkCloseWarnOnlyUsedPayload) IsEventPayload() {}

func init() {
	RegisterPayload(WorkCloseWarnOnlyUsed, WorkCloseWarnOnlyUsedPayload{})
}
