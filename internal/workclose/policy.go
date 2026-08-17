// Package workclose owns the transport-independent work-record close policy.
package workclose

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/workstamp"
)

// EnforceEnvVar selects enforced rather than warn-only close validation.
// Warn-only remains the default until the separately planned migration flips it.
const EnforceEnvVar = "GC_WORK_RECORD_ENFORCE"

// WorkClosePolicy validates a prospective, already-resolved bead state. Store
// resolution and metadata projection belong to the mutation adapter; this type
// deliberately receives the exact row and canonical physical store identity.
type WorkClosePolicy struct { //nolint:revive // design term pinned by the universal close-policy contract
	Evidence        workstamp.EvidenceReader
	CommitReachable func(commit, branch string) bool
}

// Request is the exact authoritative row plus the mutation's prospective
// policy-bearing fields and already-canonical physical store identity.
type Request struct {
	Current             beads.Bead
	ProspectiveType     string
	ProspectiveStatus   string
	ProspectiveMetadata beads.StringMap
	StoreRef            string
}

// Evaluate returns every work-record violation in prospective. Non-work and
// control-plane beads are deliberately outside this policy.
func (p WorkClosePolicy) Evaluate(req Request) []string {
	if !strings.EqualFold(strings.TrimSpace(req.ProspectiveStatus), "closed") {
		return nil
	}
	prospective := req.Current
	prospective.Type = req.ProspectiveType
	prospective.Status = req.ProspectiveStatus
	prospective.Metadata = req.ProspectiveMetadata
	if !AppliesTo(req.Current) && !AppliesTo(prospective) {
		return nil
	}
	violations := ValidateRecord(prospective, p.CommitReachable)
	if prospective.Metadata[beadmeta.WorkOutcomeMetadataKey] == beadmeta.WorkOutcomeShipped {
		violations = append(violations, workstamp.ValidateLandingEvidence(p.Evidence, req.StoreRef, prospective)...)
	}
	return violations
}

// ProjectUpdate applies the object-model fields that can change policy scope
// or evidence. It clones metadata so evaluating a request never mutates the
// authoritative row returned by the store.
func ProjectUpdate(current beads.Bead, opts beads.UpdateOpts) beads.Bead {
	prospective := current
	prospective.Metadata = make(beads.StringMap, len(current.Metadata)+len(opts.Metadata))
	for key, value := range current.Metadata {
		prospective.Metadata[key] = value
	}
	for key, value := range opts.Metadata {
		prospective.Metadata[key] = value
	}
	if opts.Status != nil {
		prospective.Status = *opts.Status
	}
	if opts.Type != nil {
		prospective.Type = *opts.Type
	}
	return prospective
}

// ProjectClose returns the prospective state of the store.Close mutation.
func ProjectClose(current beads.Bead) beads.Bead {
	prospective := ProjectUpdate(current, beads.UpdateOpts{})
	prospective.Status = "closed"
	return prospective
}

// ValidateRecord checks the typed disposition and Git provenance portion of
// the work-record contract. Evaluate adds the durable landing-evidence rule.
func ValidateRecord(prospective beads.Bead, commitReachable func(commit, branch string) bool) []string {
	outcome := prospective.Metadata[beadmeta.WorkOutcomeMetadataKey]
	if outcome == "" {
		return []string{fmt.Sprintf("missing %s (want one of shipped|no-op|blocked|abandoned)", beadmeta.WorkOutcomeMetadataKey)}
	}
	if !ValidOutcome(outcome) {
		return []string{fmt.Sprintf("invalid %s=%q (want one of shipped|no-op|blocked|abandoned)", beadmeta.WorkOutcomeMetadataKey, outcome)}
	}
	if outcome != beadmeta.WorkOutcomeShipped {
		return nil
	}

	commit := strings.TrimSpace(prospective.Metadata[beadmeta.WorkCommitMetadataKey])
	branch := strings.TrimSpace(prospective.Metadata[beadmeta.WorkBranchMetadataKey])
	var violations []string
	if commit == "" {
		violations = append(violations, fmt.Sprintf("%s=shipped requires %s (the commit that satisfied the bead)", beadmeta.WorkOutcomeMetadataKey, beadmeta.WorkCommitMetadataKey))
	}
	if branch == "" {
		violations = append(violations, fmt.Sprintf("%s=shipped requires %s (the branch the commit lives on)", beadmeta.WorkOutcomeMetadataKey, beadmeta.WorkBranchMetadataKey))
	}
	if commit != "" && branch != "" && (commitReachable == nil || !commitReachable(commit, branch)) {
		violations = append(violations, fmt.Sprintf("%s %s is not reachable on %s %s", beadmeta.WorkCommitMetadataKey, commit, beadmeta.WorkBranchMetadataKey, branch))
	}
	return violations
}

// AppliesTo reports whether bead is a worker-claimable unit rather than a
// control/structural record or another non-task object.
func AppliesTo(bead beads.Bead) bool {
	if t := strings.TrimSpace(bead.Type); t != "" && t != "task" {
		return false
	}
	return strings.TrimSpace(bead.Metadata[beadmeta.KindMetadataKey]) == ""
}

// ValidOutcome reports whether v is a typed work-record disposition.
func ValidOutcome(v string) bool {
	switch v {
	case beadmeta.WorkOutcomeShipped, beadmeta.WorkOutcomeNoOp,
		beadmeta.WorkOutcomeBlocked, beadmeta.WorkOutcomeAbandoned:
		return true
	default:
		return false
	}
}

// Enforced parses the explicit migration flag without reading process state.
func Enforced(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
