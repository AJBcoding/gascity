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
)

// enforceResolvedWorkClose applies the work-record close policy to the exact
// row, physical store ref, and STORE this plane resolved for the mutation.
//
// store is the target, not a convenience: the bounded shipped-close warn-only
// setting relaxes only a close written through the pinned bd contract, and on a
// mixed-provider city that is a per-store fact. Handlers pass the store they
// are about to write through, so the decision never has to re-derive a provider
// from config or from the bead's ID prefix.
func (s *Server) enforceResolvedWorkClose(current, prospective beads.Bead, ref storeref.StoreRef, store beads.Store) error {
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
	violations := policy.EvaluateDetailed(workclose.Request{
		Current:             current,
		ProspectiveType:     prospective.Type,
		ProspectiveStatus:   prospective.Status,
		ProspectiveMetadata: prospective.Metadata,
		StoreRef:            storeRef,
	})
	mode := "enforced"
	if !workclose.Enforce(s.bootFlags.ShippedCloseWarnOnly(), workclose.CloseTarget{
		BDStoreContract: beads.StoreUsesBDContract(store),
	}) {
		mode = "warn-only"
	}
	if mode == "warn-only" {
		_, telemetryErr := events.RecordWorkCloseWarnOnlyUse(s.state.EventProvider(), events.WorkCloseWarnOnlyUsedPayload{
			Route: "api", StoreRef: storeRef, BeadID: prospective.ID,
			ViolationCount: len(violations.Violations), RemovalVersion: rollout.ShippedCloseWarnOnlyRemovalVersion,
		})
		if telemetryErr != nil {
			log.Printf("api: work-record gate (enforced): close of %s refused: warn-only compatibility telemetry could not be confirmed: %v", prospective.ID, telemetryErr)
			return apierr.ConflictWrongState.Msg("conflict: work-record close refused: warn-only compatibility telemetry is unavailable")
		}
	}
	if len(violations.Violations) == 0 {
		return nil
	}
	message := strings.Join(violations.Violations, "; ")
	log.Printf("api: work-record gate (%s): close of %s: %s", mode, prospective.ID, message)
	if mode == "enforced" {
		if violations.LandingEvidenceUnreadable() {
			return apierr.ServiceUnavailable.Msg("work-record close refused: " + message)
		}
		return apierr.ConflictWrongState.Msg("conflict: work-record close refused: " + message)
	}
	return nil
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
