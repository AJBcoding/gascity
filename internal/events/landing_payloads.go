package events

// DeliveryLandedPayload is the typed, provider-neutral record of an exact
// authoritative target ref observation. It is evidence of observation only;
// recording it does not publish code or mutate work state.
type DeliveryLandedPayload struct {
	EventID               string   `json:"event_id" doc:"Deterministic landing receipt identifier."`
	WorkflowID            string   `json:"workflow_id"`
	IntegrationAttemptID  string   `json:"integration_attempt_id"`
	Repository            string   `json:"repository" doc:"Credential-free authoritative remote identity."`
	Remote                string   `json:"remote"`
	TargetRef             string   `json:"target_ref"`
	ExpectedTargetSHA     string   `json:"expected_target_sha"`
	ApprovedCandidateSHA  string   `json:"approved_candidate_sha"`
	ObservedLandedSHA     string   `json:"observed_landed_sha"`
	PublicationMode       string   `json:"publication_mode" enum:"direct,pull_request"`
	IntegrationResultPath string   `json:"integration_result_path"`
	IntegrationResultHash string   `json:"integration_result_hash"`
	WorkBeadIDs           []string `json:"work_bead_ids" maxItems:"256"`
	VerifiedAt            string   `json:"verified_at" format:"date-time"`
}

// IsEventPayload marks DeliveryLandedPayload as a Payload variant.
func (DeliveryLandedPayload) IsEventPayload() {}

func init() {
	RegisterPayload(DeliveryLanded, DeliveryLandedPayload{})
}
