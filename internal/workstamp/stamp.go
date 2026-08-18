// Package workstamp applies replayable delivery metadata to exact source work
// records named by a typed delivery.landed event.
package workstamp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// StoreResolver opens one exact canonical city or rig work store.
type StoreResolver interface {
	Resolve(ctx context.Context, storeRef string) (beads.Store, error)
}

// EventJournal is the read/write event boundary used by the stamping service.
type EventJournal interface {
	events.Recorder
	List(filter events.Filter) ([]events.Event, error)
}

// EvidenceReader reads durable delivery evidence without permitting mutation.
type EvidenceReader interface {
	List(filter events.Filter) ([]events.Event, error)
}

var (
	deliveryEventIDPattern = regexp.MustCompile(`^gcl-[0-9a-f]{64}$`)
	gitSHAPattern          = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ValidateLandingEvidence verifies that a bead's landed delivery metadata is
// corroborated by exactly one durable delivery.landed event. storeRef must be
// the caller's already-canonical physical store identity.
func ValidateLandingEvidence(reader EvidenceReader, storeRef string, bead beads.Bead) []string {
	metadata := bead.Metadata
	workCommit := metadata[beadmeta.WorkCommitMetadataKey]
	if !gitSHAPattern.MatchString(workCommit) {
		return []string{fmt.Sprintf("%s=shipped requires %s to be a lowercase 40-character Git SHA", beadmeta.WorkOutcomeMetadataKey, beadmeta.WorkCommitMetadataKey)}
	}
	if metadata[beadmeta.DeliveryStateMetadataKey] != beadmeta.DeliveryStateLanded {
		return []string{fmt.Sprintf("%s=shipped requires %s=%s", beadmeta.WorkOutcomeMetadataKey, beadmeta.DeliveryStateMetadataKey, beadmeta.DeliveryStateLanded)}
	}
	eventID := metadata[beadmeta.DeliveryEventIDMetadataKey]
	if !deliveryEventIDPattern.MatchString(eventID) {
		return []string{fmt.Sprintf("%s=shipped requires %s=gcl-<64 lowercase hex>", beadmeta.WorkOutcomeMetadataKey, beadmeta.DeliveryEventIDMetadataKey)}
	}
	if metadata[beadmeta.DeliverySourceCommitMetadataKey] != workCommit {
		return []string{fmt.Sprintf("%s must equal %s", beadmeta.DeliverySourceCommitMetadataKey, beadmeta.WorkCommitMetadataKey)}
	}
	landedSHA := metadata[beadmeta.DeliveryLandedSHAMetadataKey]
	if !gitSHAPattern.MatchString(landedSHA) {
		return []string{fmt.Sprintf("%s=shipped requires %s to be a lowercase 40-character Git SHA", beadmeta.WorkOutcomeMetadataKey, beadmeta.DeliveryLandedSHAMetadataKey)}
	}
	if strings.TrimSpace(storeRef) == "" {
		return []string{"landing evidence requires an explicit canonical store ref"}
	}
	if reader == nil {
		return []string{"landing event is unreadable: event journal is unavailable"}
	}
	matches, err := listLandingEvidence(reader, events.Filter{Type: events.DeliveryLanded, Subject: eventID})
	if err != nil {
		return []string{fmt.Sprintf("landing event %q is unreadable: %v", eventID, err)}
	}
	if len(matches) != 1 {
		return []string{fmt.Sprintf("expected exactly one landing event %q, found %d", eventID, len(matches))}
	}
	decoded, registered, err := events.DecodePayload(matches[0].Type, matches[0].Payload)
	if err != nil || !registered {
		if err != nil {
			return []string{fmt.Sprintf("landing event %q is unreadable: %v", eventID, err)}
		}
		return []string{fmt.Sprintf("landing event %q is unreadable: payload is not registered", eventID)}
	}
	landing, ok := decoded.(events.DeliveryLandedPayload)
	if !ok || landing.EventID != eventID {
		return []string{fmt.Sprintf("landing event %q is not valid v2 delivery evidence", eventID)}
	}
	if landing.ObservedLandedSHA != landedSHA {
		return []string{fmt.Sprintf("%s does not match landing event observed landed SHA", beadmeta.DeliveryLandedSHAMetadataKey)}
	}
	for _, record := range landing.WorkRecords {
		if record.StoreRef == storeRef && record.BeadID == bead.ID && record.WorkCommit == workCommit {
			return nil
		}
	}
	return []string{fmt.Sprintf("landing event %q is not bound to store %q bead %q commit %s", eventID, storeRef, bead.ID, workCommit)}
}

func listLandingEvidence(reader EvidenceReader, filter events.Filter) ([]events.Event, error) {
	if inFlight, ok := reader.(events.InFlightProvider); ok {
		return inFlight.ListInFlight(filter)
	}
	return reader.List(filter)
}

// Conflict is a deterministic refusal to stamp one bound source record.
type Conflict struct {
	StoreRef string `json:"store_ref"`
	BeadID   string `json:"bead_id"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// Result reports bounded per-record progress. Cross-store partial progress is
// intentionally replayable and is not described as a transaction.
type Result struct {
	Stamped        int        `json:"stamped"`
	AlreadyStamped int        `json:"already_stamped"`
	Conflicts      []Conflict `json:"conflicts"`
}

// Service stamps source work through explicitly resolved stores only.
type Service struct {
	Journal EventJournal
	Stores  StoreResolver
}

// Stamp loads one durable landing event and applies its delivery evidence to
// each exact source bead with one revision-fenced metadata update.
func (s Service) Stamp(ctx context.Context, eventID string) (Result, error) {
	if s.Journal == nil || s.Stores == nil {
		return Result{}, fmt.Errorf("workstamp: service dependencies are required")
	}
	matches, err := s.Journal.List(events.Filter{Type: events.DeliveryLanded, Subject: eventID})
	if err != nil {
		return Result{}, fmt.Errorf("workstamp: reading landing event: %w", err)
	}
	if len(matches) != 1 {
		return Result{}, fmt.Errorf("workstamp: expected exactly one landing event %q, found %d", eventID, len(matches))
	}
	decoded, registered, err := events.DecodePayload(matches[0].Type, matches[0].Payload)
	if err != nil {
		return Result{}, fmt.Errorf("workstamp: decoding landing event %q: %w", eventID, err)
	}
	if !registered {
		return Result{}, fmt.Errorf("workstamp: landing event payload is not registered")
	}
	landing, ok := decoded.(events.DeliveryLandedPayload)
	if !ok || landing.EventID != eventID || len(landing.WorkRecords) == 0 {
		return Result{}, fmt.Errorf("workstamp: landing event %q has no valid v2 work records", eventID)
	}

	result := Result{Conflicts: make([]Conflict, 0)}
	for _, record := range landing.WorkRecords {
		store, resolveErr := s.Stores.Resolve(ctx, record.StoreRef)
		if resolveErr != nil {
			return result, fmt.Errorf("workstamp: resolving store %q: %w", record.StoreRef, resolveErr)
		}
		if store == nil {
			return result, fmt.Errorf("workstamp: resolving store %q: resolver returned no store", record.StoreRef)
		}
		bead, getErr := store.Get(record.BeadID)
		if getErr != nil {
			result.Conflicts = append(result.Conflicts, conflict(record, "bead_unreadable", getErr.Error()))
			continue
		}
		if bead.Metadata[beadmeta.WorkCommitMetadataKey] != record.WorkCommit {
			result.Conflicts = append(result.Conflicts, conflict(record, "work_commit_mismatch", "current gc.work_commit differs from landing evidence"))
			continue
		}
		writer, ok := exactConditionalWriter(store)
		if !ok {
			result.Conflicts = append(result.Conflicts, conflict(record, "conditional_write_unsupported", "store does not support revision-fenced updates"))
			continue
		}
		patch := beads.StringMap{
			beadmeta.DeliveryStateMetadataKey:        beadmeta.DeliveryStateLanded,
			beadmeta.DeliveryEventIDMetadataKey:      landing.EventID,
			beadmeta.DeliveryRepositoryMetadataKey:   landing.Repository,
			beadmeta.DeliveryTargetRefMetadataKey:    landing.TargetRef,
			beadmeta.DeliveryLandedSHAMetadataKey:    landing.ObservedLandedSHA,
			beadmeta.DeliveryVerifiedAtMetadataKey:   landing.VerifiedAt,
			beadmeta.DeliverySourceCommitMetadataKey: record.WorkCommit,
		}
		stampPayload, payloadErr := newStampPayload(landing.EventID, record)
		if payloadErr != nil {
			return result, payloadErr
		}
		stampEventExists, eventErr := exactStampEventExists(s.Journal, stampPayload)
		if eventErr != nil {
			return result, eventErr
		}
		if metadataContains(bead.Metadata, patch) && stampEventExists {
			result.AlreadyStamped++
			continue
		}
		if !metadataContains(bead.Metadata, patch) && hasConflictingDeliveryMetadata(bead.Metadata, patch) {
			result.Conflicts = append(result.Conflicts, conflict(record, "delivery_metadata_mismatch", "existing delivery metadata differs from landing evidence"))
			continue
		}
		beforeStatus := bead.Status
		beforeBase := bead.Metadata[beadmeta.WorkBaseCommitMetadataKey]
		if !metadataContains(bead.Metadata, patch) {
			if updateErr := writer.UpdateIfMatch(record.BeadID, bead.Revision, beads.UpdateOpts{Metadata: patch}); updateErr != nil {
				result.Conflicts = append(result.Conflicts, conflict(record, "conditional_write_failed", updateErr.Error()))
				continue
			}
		}
		updated, readErr := store.Get(record.BeadID)
		if readErr != nil {
			result.Conflicts = append(result.Conflicts, conflict(record, "readback_failed", readErr.Error()))
			continue
		}
		if updated.Status != beforeStatus || updated.Metadata[beadmeta.WorkCommitMetadataKey] != record.WorkCommit ||
			updated.Metadata[beadmeta.WorkBaseCommitMetadataKey] != beforeBase || !metadataContains(updated.Metadata, patch) {
			result.Conflicts = append(result.Conflicts, conflict(record, "readback_mismatch", "stamped metadata or source provenance did not read back exactly"))
			continue
		}
		if !stampEventExists {
			raw, marshalErr := json.Marshal(stampPayload)
			if marshalErr != nil {
				return result, fmt.Errorf("workstamp: encoding stamp event: %w", marshalErr)
			}
			s.Journal.Record(events.Event{
				Type:    events.DeliveryWorkStamped,
				Actor:   "gc.workstamp",
				Subject: stampPayload.EventID,
				Payload: raw,
			})
			recorded, verifyErr := exactStampEventExists(s.Journal, stampPayload)
			if verifyErr != nil {
				return result, verifyErr
			}
			if !recorded {
				result.Conflicts = append(result.Conflicts, conflict(record, "stamp_event_unreadable", "stamp event did not read back"))
				continue
			}
		}
		result.Stamped++
	}
	return result, nil
}

// exactConditionalWriter follows only wrapper-declared resolution targets.
// Stamping is intrinsically revision-fenced, so it checks the store's actual
// capability independent of the optional conditional-writes rollout mode.
func exactConditionalWriter(store beads.Store) (beads.ConditionalWriter, bool) {
	for range 16 {
		if writer, ok := beads.ConditionalWriterFor(store); ok {
			return writer, true
		}
		targeter, ok := store.(beads.ConditionalWritesResolveTargeter)
		if !ok {
			return nil, false
		}
		next := targeter.ConditionalWritesResolveTarget()
		if next == nil {
			return nil, false
		}
		store = next
	}
	return nil, false
}

func newStampPayload(landingEventID string, record events.DeliveryWorkRecordRef) (events.DeliveryWorkStampedPayload, error) {
	identity := struct {
		LandingEventID string `json:"landing_event_id"`
		StoreRef       string `json:"store_ref"`
		BeadID         string `json:"bead_id"`
		WorkCommit     string `json:"work_commit"`
	}{landingEventID, record.StoreRef, record.BeadID, record.WorkCommit}
	raw, err := json.Marshal(identity)
	if err != nil {
		return events.DeliveryWorkStampedPayload{}, fmt.Errorf("workstamp: encoding stamp identity: %w", err)
	}
	digest := sha256.Sum256(raw)
	return events.DeliveryWorkStampedPayload{
		EventID:        "gws-" + hex.EncodeToString(digest[:]),
		LandingEventID: landingEventID,
		StoreRef:       record.StoreRef,
		BeadID:         record.BeadID,
		WorkCommit:     record.WorkCommit,
	}, nil
}

func exactStampEventExists(journal EventJournal, want events.DeliveryWorkStampedPayload) (bool, error) {
	matches, err := journal.List(events.Filter{Type: events.DeliveryWorkStamped, Subject: want.EventID})
	if err != nil {
		return false, fmt.Errorf("workstamp: reading stamp event %q: %w", want.EventID, err)
	}
	if len(matches) == 0 {
		return false, nil
	}
	if len(matches) != 1 {
		return false, fmt.Errorf("workstamp: expected at most one stamp event %q, found %d", want.EventID, len(matches))
	}
	decoded, registered, err := events.DecodePayload(matches[0].Type, matches[0].Payload)
	if err != nil {
		return false, fmt.Errorf("workstamp: decoding stamp event %q: %w", want.EventID, err)
	}
	payload, ok := decoded.(events.DeliveryWorkStampedPayload)
	if !registered || !ok || !reflect.DeepEqual(payload, want) {
		return false, fmt.Errorf("workstamp: stamp event %q disagrees with expected identity", want.EventID)
	}
	return true, nil
}

func conflict(record events.DeliveryWorkRecordRef, code, message string) Conflict {
	return Conflict{StoreRef: record.StoreRef, BeadID: record.BeadID, Code: code, Message: message}
}

func metadataContains(metadata beads.StringMap, want beads.StringMap) bool {
	for key, value := range want {
		if metadata[key] != value {
			return false
		}
	}
	return true
}

func hasDeliveryMetadata(metadata beads.StringMap) bool {
	for key := range metadata {
		if strings.HasPrefix(key, beadmeta.DeliveryMetadataPrefix) {
			return true
		}
	}
	return false
}

// hasConflictingDeliveryMetadata reports whether metadata carries delivery
// evidence that contradicts the landing patch. The recognized pre-landing
// lifecycle marker gc.delivery_state=integration_ready is stampable, not
// conflicting: implementation workflows record it on the still-open source
// anchor before landing, and stamping overwrites it with the landed state.
// Every other divergent gc.delivery_* value is evidence from a different
// landing event and must not be overwritten.
func hasConflictingDeliveryMetadata(metadata, patch beads.StringMap) bool {
	for key, value := range metadata {
		if !strings.HasPrefix(key, beadmeta.DeliveryMetadataPrefix) || value == patch[key] {
			continue
		}
		if key == beadmeta.DeliveryStateMetadataKey && value == beadmeta.DeliveryStateIntegrationReady {
			continue
		}
		return true
	}
	return false
}
