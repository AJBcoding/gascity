package sling

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	convoycore "github.com/gastownhall/gascity/internal/convoy"
)

// InFlightConflictError reports a dispatch refused because the targeted work
// is already in flight with a different actor. Two kinds of evidence produce
// it: a direct claim (the bead, or a tracked child when the target is a
// convoy, is in_progress with a foreign assignee) and pour evidence (a live
// molecule/workflow root references the bead through a live tracking convoy
// via gc.input_convoy_id). Exactly one of ChildID/RootID is set per kind.
type InFlightConflictError struct {
	// BeadID is the bead (or convoy) whose dispatch was refused.
	BeadID string
	// ChildID is the tracked child holding the claim when BeadID is a convoy.
	ChildID string
	// Assignee is the claiming identity for direct-claim evidence.
	Assignee string
	// RootID is the live molecule/workflow root for pour evidence.
	RootID string
	// ConvoyID is the tracking convoy linking RootID to BeadID.
	ConvoyID string
	// RootTarget is the identity RootID is dispatched to; empty when the
	// root is unrouted (a cooked workflow the controller will drive).
	RootTarget string
}

func (e *InFlightConflictError) Error() string {
	if e == nil {
		return "in-flight dispatch conflict"
	}
	if e.ChildID != "" {
		return fmt.Sprintf("refusing to dispatch %s: tracked child %s is already in flight with %q (status in_progress); re-sling with --force to dispatch anyway",
			e.BeadID, e.ChildID, e.Assignee)
	}
	if e.RootID != "" {
		target := e.RootTarget
		if target == "" {
			target = "unrouted"
		}
		return fmt.Sprintf("refusing to dispatch %s: work is already in flight via live root %s (tracking convoy %s, dispatched to %q); close the stale root or re-sling with --force",
			e.BeadID, e.RootID, e.ConvoyID, target)
	}
	return fmt.Sprintf("refusing to dispatch %s: already in flight with %q (status in_progress); re-sling with --force to dispatch anyway",
		e.BeadID, e.Assignee)
}

// shouldCheckInFlightDispatch reports whether the in-flight dispatch gate
// applies. Formula launches and inline text mint fresh beads with nothing to
// conflict, dry-run mutates nothing, and --force / --reassign are the
// documented takeover escape hatches (reassign exists precisely to hand a
// claimed bead to a new actor).
func shouldCheckInFlightDispatch(opts SlingOpts) bool {
	return !opts.IsFormula && !opts.Force && !opts.Reassign && !opts.DryRun && !opts.InlineText
}

// beadInFlightWith reports whether b is claimed by an actor other than
// target: in_progress with a non-empty foreign assignee. An in_progress bead
// with no assignee is a workflow-launch promotion, not a claim, and stays
// dispatchable here; pour evidence covers it when a live root exists.
func beadInFlightWith(b beads.Bead, target string) bool {
	assignee := strings.TrimSpace(b.Assignee)
	return b.Status == "in_progress" && assignee != "" && assignee != target
}

// CheckInFlightDispatch refuses a dispatch that would double-route work
// already in flight with a different actor. A convoy that stays open and
// unowned after its child is claimed gates nothing on its own, so the
// convoy-pour and direct-claim dispatch paths could each reach the same bead;
// this gate makes every dispatch shape consult the live claim state instead
// of persisting ownership that could go stale on a crash.
//
// For a convoy, any live tracked member claimed by another actor refuses the
// dispatch. For a plain bead, its own foreign claim refuses, and so does a
// live molecule/workflow root referencing it through a live tracking convoy
// (gc.input_convoy_id) — a root that is neither a child of the bead nor named
// in its metadata, so the attachment probes cannot see it. Roots dispatched
// to the requesting target are left to the idempotency and root-key
// machinery, which knows how to repair or no-op a same-target re-sling.
//
// Probe failures refuse the dispatch (fail closed): an unreadable ledger is
// not proof the bead is idle, and the write the sling is about to attempt
// goes through the same store.
func CheckInFlightDispatch(q BeadQuerier, beadID string, a config.Agent, deps SlingDeps) error {
	b, ok := BeadFromGetters(beadID, q, deps.Store)
	if !ok {
		// Missing bead is validateExistingBead's error to report.
		return nil
	}
	target := agentutil.RoutedToIdentity(&a)
	if beads.IsContainerType(b.Type) {
		return checkContainerChildrenInFlight(b, target, deps)
	}
	if beadInFlightWith(b, target) {
		return &InFlightConflictError{BeadID: b.ID, Assignee: strings.TrimSpace(b.Assignee)}
	}
	return checkLiveInputConvoyRoots(b.ID, target, deps)
}

func checkContainerChildrenInFlight(container beads.Bead, target string, deps SlingDeps) error {
	if deps.Store == nil {
		return nil
	}
	members, err := convoycore.Members(deps.Store, container.ID, false)
	if err != nil {
		return fmt.Errorf("in-flight dispatch check for %s: %w", container.ID, err)
	}
	for _, member := range members {
		if beadInFlightWith(member, target) {
			return &InFlightConflictError{
				BeadID:   container.ID,
				ChildID:  member.ID,
				Assignee: strings.TrimSpace(member.Assignee),
			}
		}
	}
	return nil
}

// checkLiveInputConvoyRoots surfaces pour evidence for beadID: a live
// molecule/workflow root whose gc.input_convoy_id names a live tracking
// convoy of the bead and that is dispatched to some other actor (or to no
// one — a cooked root the controller will drive is still a competing
// dispatch). Terminal convoys, session/order bookkeeping convoys, terminal
// roots, and non-dispatch beads that merely carry the metadata key are all
// ignored.
func checkLiveInputConvoyRoots(beadID, target string, deps SlingDeps) error {
	if deps.Store == nil {
		return nil
	}
	convoys, err := convoycore.TrackingConvoysForItem(deps.Store, beadID)
	if err != nil {
		return fmt.Errorf("in-flight dispatch check for %s: %w", beadID, err)
	}
	for _, convoy := range convoys {
		if convoycore.IsTerminalStatus(convoy.Status) || beads.HasReadyExcludedLabel(convoy) {
			continue
		}
		roots, err := deps.Store.ListByMetadata(map[string]string{beadmeta.InputConvoyIDMetadataKey: convoy.ID}, 0, beads.WithBothTiers)
		if err != nil {
			return fmt.Errorf("in-flight dispatch check for %s: %w", beadID, err)
		}
		for _, root := range roots {
			if convoycore.IsTerminalStatus(root.Status) {
				continue
			}
			if !IsAttachedRoot(root) && !strings.EqualFold(strings.TrimSpace(root.Metadata[beadmeta.KindMetadataKey]), beadmeta.KindWisp) {
				continue
			}
			rootTarget := strings.TrimSpace(root.Metadata[beadmeta.RoutedToMetadataKey])
			if rootTarget == "" {
				rootTarget = strings.TrimSpace(root.Assignee)
			}
			if rootTarget == target {
				continue
			}
			return &InFlightConflictError{
				BeadID:     beadID,
				RootID:     root.ID,
				ConvoyID:   convoy.ID,
				RootTarget: rootTarget,
			}
		}
	}
	return nil
}
