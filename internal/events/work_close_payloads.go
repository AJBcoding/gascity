package events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// WorkCloseWarnOnlyUsedPayload records each use of the bounded warn-only close
// path so operators can prove it is unused before deleting the compatibility
// setting at its removal floor.
type WorkCloseWarnOnlyUsedPayload struct {
	UsageID        string `json:"usage_id"`
	Route          string `json:"route"`
	StoreRef       string `json:"store_ref"`
	BeadID         string `json:"bead_id"`
	ViolationCount int    `json:"violation_count"`
	RemovalVersion string `json:"removal_version"`
}

type acknowledgedBatchAppender interface {
	AppendBatch([]Event) error
}

// RecordWorkCloseWarnOnlyUse durably appends one compatibility-use event and
// reads its unique typed identity back before returning success. Best-effort
// recorders are deliberately rejected: warn-only is available only when its
// use can be proven later.
func RecordWorkCloseWarnOnlyUse(provider Provider, payload WorkCloseWarnOnlyUsedPayload) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("event provider is unavailable")
	}
	appender, ok := provider.(acknowledgedBatchAppender)
	if !ok {
		return "", fmt.Errorf("event provider does not support acknowledged append")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("allocate usage identity: %w", err)
	}
	payload.UsageID = "gcw-" + hex.EncodeToString(random)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode warn-only usage: %w", err)
	}
	if err := appender.AppendBatch([]Event{{Type: WorkCloseWarnOnlyUsed, Actor: "gc.workclose", Subject: payload.BeadID, Payload: encoded}}); err != nil {
		return "", fmt.Errorf("append warn-only usage: %w", err)
	}
	found, err := provider.List(Filter{Type: WorkCloseWarnOnlyUsed, Subject: payload.BeadID})
	if err != nil {
		return "", fmt.Errorf("read back warn-only usage %s: %w", payload.UsageID, err)
	}
	for _, event := range found {
		var recorded WorkCloseWarnOnlyUsedPayload
		if json.Unmarshal(event.Payload, &recorded) == nil && recorded.UsageID == payload.UsageID {
			return payload.UsageID, nil
		}
	}
	return "", fmt.Errorf("read back warn-only usage %s: event not found", payload.UsageID)
}

// IsEventPayload marks WorkCloseWarnOnlyUsedPayload as an events.Payload variant.
func (WorkCloseWarnOnlyUsedPayload) IsEventPayload() {}

func init() {
	RegisterPayload(WorkCloseWarnOnlyUsed, WorkCloseWarnOnlyUsedPayload{})
}
