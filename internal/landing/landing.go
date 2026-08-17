// Package landing verifies exact authoritative remote-ref observations and
// records typed, idempotent evidence of them. It does not publish code or
// mutate work state.
package landing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gastownhall/gascity/internal/events"
)

const (
	// MaxReceiptBytes is the maximum encoded receipt size accepted by a CLI
	// adapter before JSON decoding.
	MaxReceiptBytes = 64 << 10
	// MaxWorkBeadIDs bounds the work identifiers carried by one landing event.
	MaxWorkBeadIDs = 256
	// MaxWorkRecords bounds the store-scoped work identities in a v2 event.
	MaxWorkRecords = 256

	maxIDBytes         = 256
	maxRepositoryBytes = 4096
	maxRemoteBytes     = 128
	maxTargetRefBytes  = 512
	maxPathBytes       = 4096
	maxActorBytes      = 256
	maxStoreNameBytes  = 255
)

var (
	remoteNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	gitSHAPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Pattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	workTokenPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// WorkRecordRef binds a source commit to one exact local city or rig store.
type WorkRecordRef struct {
	StoreRef   string
	BeadID     string
	WorkCommit string
}

// RecordRequest contains the bounded caller assertions needed to verify one
// authoritative target ref and construct its durable receipt.
type RecordRequest struct {
	ReceiptVersion        string
	WorkflowID            string
	IntegrationAttemptID  string
	RepositoryPath        string
	Repository            string
	Remote                string
	TargetRef             string
	ExpectedTargetSHA     string
	ApprovedCandidateSHA  string
	ExpectedLandedSHA     string
	PublicationMode       string
	IntegrationResultPath string
	IntegrationResultHash string
	WorkBeadIDs           []string
	WorkRecords           []WorkRecordRef
	Actor                 string
}

// RemoteObservation is the credential-free repository identity and exact SHA
// returned by an authoritative remote lookup.
type RemoteObservation struct {
	Repository string
	SHA        string
}

// RemoteObserver performs the remote lookup. Implementations must observe the
// exact supplied ref rather than a local or remote-tracking ref.
type RemoteObserver interface {
	Observe(context.Context, string, string, string) (RemoteObservation, error)
}

// EventJournal is the minimum strict read-after-write boundary needed by the
// service. Record itself is best-effort, so success requires a subsequent List.
type EventJournal interface {
	Record(events.Event)
	List(events.Filter) ([]events.Event, error)
}

// Result identifies the verified observation and whether it already existed.
type Result struct {
	EventID           string `json:"event_id"`
	ObservedLandedSHA string `json:"observed_landed_sha"`
	AlreadyRecorded   bool   `json:"already_recorded"`
}

// Service verifies observations and records typed landing evidence.
type Service struct {
	Observer RemoteObserver
	Journal  EventJournal
	Now      func() time.Time
}

// Record validates the receipt, independently observes the authoritative
// remote ref, and confirms a deterministic typed event is readable.
func (s *Service) Record(ctx context.Context, request RecordRequest) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	if s == nil || s.Observer == nil {
		return Result{}, fmt.Errorf("landing: remote observer is required")
	}
	if s.Journal == nil {
		return Result{}, fmt.Errorf("landing: event journal is required")
	}

	observation, err := s.Observer.Observe(ctx, request.RepositoryPath, request.Remote, request.TargetRef)
	if err != nil {
		return Result{}, fmt.Errorf("landing: observing authoritative remote ref: %w", err)
	}
	if observation.Repository != request.Repository {
		return Result{}, fmt.Errorf("landing: observed repository %q does not match receipt repository %q", observation.Repository, request.Repository)
	}
	if observation.SHA != request.ExpectedLandedSHA {
		return Result{}, fmt.Errorf("landing: observed SHA %q does not match expected landed SHA %q", observation.SHA, request.ExpectedLandedSHA)
	}
	if request.PublicationMode == "direct" && request.ApprovedCandidateSHA != request.ExpectedLandedSHA {
		return Result{}, fmt.Errorf("landing: direct publication candidate %q does not match expected landed SHA %q", request.ApprovedCandidateSHA, request.ExpectedLandedSHA)
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	payload := events.DeliveryLandedPayload{
		WorkflowID:            request.WorkflowID,
		IntegrationAttemptID:  request.IntegrationAttemptID,
		Repository:            observation.Repository,
		Remote:                request.Remote,
		TargetRef:             request.TargetRef,
		ExpectedTargetSHA:     request.ExpectedTargetSHA,
		ApprovedCandidateSHA:  request.ApprovedCandidateSHA,
		ObservedLandedSHA:     observation.SHA,
		PublicationMode:       request.PublicationMode,
		IntegrationResultPath: request.IntegrationResultPath,
		IntegrationResultHash: request.IntegrationResultHash,
		WorkBeadIDs:           append([]string(nil), request.WorkBeadIDs...),
		WorkRecords:           deliveryWorkRecords(request.WorkRecords),
		VerifiedAt:            now().UTC().Format(time.RFC3339Nano),
	}
	eventID, err := deterministicEventID(payload)
	if err != nil {
		return Result{}, err
	}
	payload.EventID = eventID

	exists, err := eventWithIDAlreadyExists(s.Journal, eventID)
	if err != nil {
		return Result{}, err
	}
	if exists {
		return Result{EventID: eventID, ObservedLandedSHA: observation.SHA, AlreadyRecorded: true}, nil
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("landing: encoding event payload: %w", err)
	}
	s.Journal.Record(events.Event{
		Type:    events.DeliveryLanded,
		Actor:   request.Actor,
		Subject: eventID,
		RunID:   request.WorkflowID,
		Payload: raw,
	})

	exists, err = eventWithIDAlreadyExists(s.Journal, eventID)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		return Result{}, fmt.Errorf("landing: recorded event %q could not be read back", eventID)
	}
	return Result{EventID: eventID, ObservedLandedSHA: observation.SHA}, nil
}

func validateRequest(request RecordRequest) error {
	scalars := []struct {
		name  string
		value string
		max   int
	}{
		{"workflow_id", request.WorkflowID, maxIDBytes},
		{"integration_attempt_id", request.IntegrationAttemptID, maxIDBytes},
		{"repository_path", request.RepositoryPath, maxPathBytes},
		{"repository", request.Repository, maxRepositoryBytes},
		{"remote", request.Remote, maxRemoteBytes},
		{"target_ref", request.TargetRef, maxTargetRefBytes},
		{"expected_target_sha", request.ExpectedTargetSHA, 40},
		{"approved_candidate_sha", request.ApprovedCandidateSHA, 40},
		{"expected_landed_sha", request.ExpectedLandedSHA, 40},
		{"publication_mode", request.PublicationMode, maxIDBytes},
		{"integration_result_path", request.IntegrationResultPath, maxPathBytes},
		{"integration_result_hash", request.IntegrationResultHash, 71},
		{"actor", request.Actor, maxActorBytes},
	}
	for _, scalar := range scalars {
		if err := validateScalar(scalar.name, scalar.value, scalar.max); err != nil {
			return err
		}
	}

	if !filepath.IsAbs(request.RepositoryPath) {
		return fmt.Errorf("landing: repository_path must be absolute")
	}
	if !filepath.IsAbs(request.IntegrationResultPath) {
		return fmt.Errorf("landing: integration_result_path must be absolute")
	}
	if !remoteNamePattern.MatchString(request.Remote) {
		return fmt.Errorf("landing: remote is not a safe Git remote name")
	}
	if !strings.HasPrefix(request.TargetRef, "refs/heads/") || request.TargetRef == "refs/heads/" {
		return fmt.Errorf("landing: target_ref must name a branch under refs/heads/")
	}
	for name, value := range map[string]string{
		"expected_target_sha":    request.ExpectedTargetSHA,
		"approved_candidate_sha": request.ApprovedCandidateSHA,
		"expected_landed_sha":    request.ExpectedLandedSHA,
	} {
		if !gitSHAPattern.MatchString(value) {
			return fmt.Errorf("landing: %s must be a lowercase 40-character Git SHA", name)
		}
	}
	if !sha256Pattern.MatchString(request.IntegrationResultHash) {
		return fmt.Errorf("landing: integration_result_hash must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	if request.PublicationMode != "direct" && request.PublicationMode != "pull_request" {
		return fmt.Errorf("landing: publication_mode must be direct or pull_request")
	}
	if request.ReceiptVersion == "2" {
		if len(request.WorkBeadIDs) != 0 {
			return fmt.Errorf("landing: v2 receipt must use work_records instead of work_bead_ids")
		}
		if err := validateWorkRecords(request.WorkRecords); err != nil {
			return err
		}
	} else {
		if request.ReceiptVersion != "" && request.ReceiptVersion != "1" {
			return fmt.Errorf("landing: unsupported receipt schema_version %q", request.ReceiptVersion)
		}
		if len(request.WorkRecords) != 0 {
			return fmt.Errorf("landing: v1 receipt must not contain work_records")
		}
		if err := validateWorkBeadIDs(request.WorkBeadIDs); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkBeadIDs(workBeadIDs []string) error {
	if len(workBeadIDs) == 0 {
		return fmt.Errorf("landing: work_bead_ids must not be empty")
	}
	if len(workBeadIDs) > MaxWorkBeadIDs {
		return fmt.Errorf("landing: work_bead_ids exceeds maximum of %d", MaxWorkBeadIDs)
	}
	seen := make(map[string]struct{}, len(workBeadIDs))
	for index, workID := range workBeadIDs {
		if err := validateScalar(fmt.Sprintf("work_bead_ids[%d]", index), workID, maxIDBytes); err != nil {
			return err
		}
		if _, exists := seen[workID]; exists {
			return fmt.Errorf("landing: duplicate work bead ID %q", workID)
		}
		seen[workID] = struct{}{}
	}
	return nil
}

func validateWorkRecords(workRecords []WorkRecordRef) error {
	if len(workRecords) == 0 {
		return fmt.Errorf("landing: work_records must not be empty")
	}
	if len(workRecords) > MaxWorkRecords {
		return fmt.Errorf("landing: work_records exceeds maximum of %d", MaxWorkRecords)
	}
	seen := make(map[string]struct{}, len(workRecords))
	for index, record := range workRecords {
		if err := validateStoreRef(record.StoreRef); err != nil {
			return fmt.Errorf("landing: work_records[%d].store_ref: %w", index, err)
		}
		if err := validateScalar(fmt.Sprintf("work_records[%d].bead_id", index), record.BeadID, maxIDBytes); err != nil {
			return err
		}
		if !workTokenPattern.MatchString(record.BeadID) {
			return fmt.Errorf("landing: work_records[%d].bead_id is not a safe bead ID", index)
		}
		if !gitSHAPattern.MatchString(record.WorkCommit) {
			return fmt.Errorf("landing: work_records[%d].work_commit must be a lowercase 40-character Git SHA", index)
		}
		key := record.StoreRef + "\x00" + record.BeadID
		if _, exists := seen[key]; exists {
			return fmt.Errorf("landing: duplicate work record %q in %q", record.BeadID, record.StoreRef)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateStoreRef(storeRef string) error {
	kind, name, ok := strings.Cut(storeRef, ":")
	if !ok || (kind != "city" && kind != "rig") || len(name) > maxStoreNameBytes || !workTokenPattern.MatchString(name) {
		return fmt.Errorf("must be canonical city:<name> or rig:<name>")
	}
	return nil
}

func deliveryWorkRecords(records []WorkRecordRef) []events.DeliveryWorkRecordRef {
	if len(records) == 0 {
		return nil
	}
	result := make([]events.DeliveryWorkRecordRef, len(records))
	for index, record := range records {
		result[index] = events.DeliveryWorkRecordRef{
			StoreRef:   record.StoreRef,
			BeadID:     record.BeadID,
			WorkCommit: record.WorkCommit,
		}
	}
	return result
}

func validateScalar(name, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("landing: %s is required", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("landing: %s must be valid UTF-8", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("landing: %s must not contain leading or trailing whitespace", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("landing: %s exceeds %d bytes", name, maxBytes)
	}
	return nil
}

type canonicalLandingReceipt struct {
	WorkflowID            string                         `json:"workflow_id"`
	IntegrationAttemptID  string                         `json:"integration_attempt_id"`
	Repository            string                         `json:"repository"`
	Remote                string                         `json:"remote"`
	TargetRef             string                         `json:"target_ref"`
	ExpectedTargetSHA     string                         `json:"expected_target_sha"`
	ApprovedCandidateSHA  string                         `json:"approved_candidate_sha"`
	ObservedLandedSHA     string                         `json:"observed_landed_sha"`
	PublicationMode       string                         `json:"publication_mode"`
	IntegrationResultPath string                         `json:"integration_result_path"`
	IntegrationResultHash string                         `json:"integration_result_hash"`
	WorkBeadIDs           []string                       `json:"work_bead_ids,omitempty"`
	WorkRecords           []events.DeliveryWorkRecordRef `json:"work_records,omitempty"`
}

func deterministicEventID(payload events.DeliveryLandedPayload) (string, error) {
	canonical := canonicalLandingReceipt{
		WorkflowID:            payload.WorkflowID,
		IntegrationAttemptID:  payload.IntegrationAttemptID,
		Repository:            payload.Repository,
		Remote:                payload.Remote,
		TargetRef:             payload.TargetRef,
		ExpectedTargetSHA:     payload.ExpectedTargetSHA,
		ApprovedCandidateSHA:  payload.ApprovedCandidateSHA,
		ObservedLandedSHA:     payload.ObservedLandedSHA,
		PublicationMode:       payload.PublicationMode,
		IntegrationResultPath: payload.IntegrationResultPath,
		IntegrationResultHash: payload.IntegrationResultHash,
		WorkBeadIDs:           payload.WorkBeadIDs,
		WorkRecords:           payload.WorkRecords,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("landing: encoding canonical receipt: %w", err)
	}
	digest := sha256.Sum256(raw)
	return "gcl-" + hex.EncodeToString(digest[:]), nil
}

func eventWithIDAlreadyExists(journal EventJournal, eventID string) (bool, error) {
	matches, err := journal.List(events.Filter{Type: events.DeliveryLanded, Subject: eventID})
	if err != nil {
		return false, fmt.Errorf("landing: reading event journal: %w", err)
	}
	for _, event := range matches {
		decoded, registered, err := events.DecodePayload(event.Type, event.Payload)
		if err != nil {
			return false, fmt.Errorf("landing: decoding recorded event %q: %w", eventID, err)
		}
		if !registered {
			continue
		}
		payload, ok := decoded.(events.DeliveryLandedPayload)
		if ok && payload.EventID == eventID {
			return true, nil
		}
	}
	return false, nil
}
