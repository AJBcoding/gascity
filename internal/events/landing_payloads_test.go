package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDeliveryLandedPayloadIsRegistered(t *testing.T) {
	sample, ok := LookupPayload(DeliveryLanded)
	if !ok {
		t.Fatal("delivery.landed payload is not registered")
	}
	if _, ok := sample.(DeliveryLandedPayload); !ok {
		t.Fatalf("payload type = %T", sample)
	}

	raw, err := json.Marshal(DeliveryLandedPayload{
		EventID:               "gcl-abc",
		WorkflowID:            "run-1",
		IntegrationAttemptID:  "attempt-1",
		Repository:            "https://example.invalid/acme/repo.git",
		Remote:                "origin",
		TargetRef:             "refs/heads/main",
		ExpectedTargetSHA:     strings.Repeat("a", 40),
		ApprovedCandidateSHA:  strings.Repeat("b", 40),
		ObservedLandedSHA:     strings.Repeat("b", 40),
		PublicationMode:       "direct",
		IntegrationResultPath: "/tmp/result.md",
		IntegrationResultHash: "sha256:" + strings.Repeat("c", 64),
		WorkBeadIDs:           []string{"gc-1"},
		VerifiedAt:            "2026-08-16T20:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{
		"event_id",
		"workflow_id",
		"integration_attempt_id",
		"repository",
		"remote",
		"target_ref",
		"expected_target_sha",
		"approved_candidate_sha",
		"observed_landed_sha",
		"publication_mode",
		"integration_result_path",
		"integration_result_hash",
		"work_bead_ids",
		"verified_at",
	} {
		if !bytes.Contains(raw, []byte(`"`+key+`"`)) {
			t.Errorf("missing %s in %s", key, raw)
		}
	}
}
