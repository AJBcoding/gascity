package api

import (
	"log"
	"os/exec"
	"strings"

	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/rollout"
	"github.com/gastownhall/gascity/internal/storeref"
	"github.com/gastownhall/gascity/internal/workclose"
	"github.com/gastownhall/gascity/internal/workstamp"
)

func (s *Server) enforceResolvedWorkClose(current, prospective beads.Bead, ref storeref.StoreRef) error {
	storeRef := s.canonicalWorkCloseStoreRef(ref)
	repoDir := strings.TrimSpace(prospective.Metadata[beadmeta.WorkDirMetadataKey])
	if repoDir == "" {
		repoDir = s.state.CityPath()
	}
	evidence := s.state.EventProvider()
	policy := workclose.WorkClosePolicy{
		Evidence: evidence,
		CommitReachable: func(commit, branch string) bool {
			return apiGitCommitReachable(repoDir, commit, branch)
		},
	}
	violations := policy.Evaluate(workclose.Request{
		Current:             current,
		ProspectiveType:     prospective.Type,
		ProspectiveStatus:   prospective.Status,
		ProspectiveMetadata: prospective.Metadata,
		StoreRef:            storeRef,
	})
	mode := "enforced"
	if s.bootFlags.ShippedCloseWarnOnly() {
		mode = "warn-only"
	}
	if mode == "warn-only" {
		_, telemetryErr := events.RecordWorkCloseWarnOnlyUse(s.state.EventProvider(), events.WorkCloseWarnOnlyUsedPayload{
			Route: "api", StoreRef: storeRef, BeadID: prospective.ID,
			ViolationCount: len(violations), RemovalVersion: rollout.ShippedCloseWarnOnlyRemovalVersion,
		})
		if telemetryErr != nil {
			log.Printf("api: work-record gate (enforced): close of %s refused: warn-only compatibility telemetry could not be confirmed: %v", prospective.ID, telemetryErr)
			return apierr.ConflictWrongState.Msg("conflict: work-record close refused: warn-only compatibility telemetry is unavailable")
		}
	}
	if len(violations) == 0 {
		return nil
	}
	message := strings.Join(violations, "; ")
	log.Printf("api: work-record gate (%s): close of %s: %s", mode, prospective.ID, message)
	if mode == "enforced" {
		if landingEvidenceUnreadable(evidence, storeRef, prospective) {
			return apierr.ServiceUnavailable.Msg("work-record close refused: " + message)
		}
		return apierr.ConflictWrongState.Msg("conflict: work-record close refused: " + message)
	}
	return nil
}

// landingEvidenceUnreadable reports whether a refused close stems from landing
// evidence the server could not read (an infrastructure failure) rather than
// from the record's own state, so the transport can answer 503 instead of a
// client-state 409. It re-runs the typed evidence classification only for
// prospective shipped work, mirroring the policy's evidence gate; the close
// stays refused either way.
func landingEvidenceUnreadable(evidence workstamp.EvidenceReader, storeRef string, prospective beads.Bead) bool {
	if prospective.Metadata[beadmeta.WorkOutcomeMetadataKey] != beadmeta.WorkOutcomeShipped {
		return false
	}
	for _, violation := range workstamp.ClassifyLandingEvidence(evidence, storeRef, prospective) {
		if violation.Class == workstamp.EvidenceUnreadable {
			return true
		}
	}
	return false
}

func (s *Server) canonicalWorkCloseStoreRef(ref storeref.StoreRef) string {
	if ref != storeref.WorkRef {
		return string(ref)
	}
	name := strings.TrimSpace(s.state.CityName())
	if name == "" {
		name = "city"
	}
	return "city:" + name
}

func apiGitCommitReachable(repoDir, commit, branch string) bool {
	if strings.TrimSpace(repoDir) == "" || commit == "" || branch == "" || strings.HasPrefix(commit, "-") || strings.HasPrefix(branch, "-") {
		return false
	}
	return exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", commit, branch).Run() == nil
}
