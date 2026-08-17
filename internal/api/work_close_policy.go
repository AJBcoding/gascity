package api

import (
	"encoding/json"
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

func (s *Server) enforceResolvedWorkClose(current, prospective beads.Bead, ref storeref.StoreRef) error {
	repoDir := strings.TrimSpace(prospective.Metadata[beadmeta.WorkDirMetadataKey])
	if repoDir == "" {
		repoDir = s.state.CityPath()
	}
	policy := workclose.WorkClosePolicy{
		Evidence: s.state.EventProvider(),
		CommitReachable: func(commit, branch string) bool {
			return apiGitCommitReachable(repoDir, commit, branch)
		},
	}
	violations := policy.Evaluate(workclose.Request{
		Current:             current,
		ProspectiveType:     prospective.Type,
		ProspectiveStatus:   prospective.Status,
		ProspectiveMetadata: prospective.Metadata,
		StoreRef:            s.canonicalWorkCloseStoreRef(ref),
	})
	mode := "enforced"
	if s.bootFlags.ShippedCloseWarnOnly() {
		mode = "warn-only"
	}
	if mode == "warn-only" {
		payload, _ := json.Marshal(events.WorkCloseWarnOnlyUsedPayload{
			Route: "api", StoreRef: s.canonicalWorkCloseStoreRef(ref), BeadID: prospective.ID,
			ViolationCount: len(violations), RemovalVersion: rollout.ShippedCloseWarnOnlyRemovalVersion,
		})
		s.state.EventProvider().Record(events.Event{Type: events.WorkCloseWarnOnlyUsed, Actor: "gc.workclose", Subject: prospective.ID, Payload: payload})
	}
	if len(violations) == 0 {
		return nil
	}
	message := strings.Join(violations, "; ")
	log.Printf("api: work-record gate (%s): close of %s: %s", mode, prospective.ID, message)
	if mode == "enforced" {
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
