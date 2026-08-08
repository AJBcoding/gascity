package sling

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/beads"
)

// BlockerQuerier reads the dependency edges and dependency-target statuses
// needed to evaluate the readiness predicate the pool demand query applies.
// beads.Store satisfies it.
type BlockerQuerier interface {
	DepLister
	BeadQuerier
}

// UnclosedBlockers returns the IDs of beadID's unclosed ready-blocking
// dependencies, in dependency-row order, or nil when the bead is ready.
//
// This is the sling-side evaluation of the predicate the other end of the
// route applies. The pool demand query — the shell
// `bd ready --metadata-field gc.routed_to=$route --unassigned` and its native
// twin collectOpenUnassignedRoutedWork — hides any bead with an unclosed
// ready-blocking dependency (#4395, gc-ft31x). A blocked bead can therefore
// carry correct gc.routed_to metadata that its only consumer never sees, so
// sling has to evaluate the same predicate to report the disagreement.
//
// Dependency-type selection is beads.IsReadyBlockingDependencyType:
// parent-child and the informational relation types never gate readiness.
// Status interpretation matches the native reader in cachedBeadReady — any
// non-closed target blocks, and a target this store cannot resolve is not
// counted, because a cross-store dependency row is legitimately absent here
// and the demand-side reader skips it for the same reason.
func UnclosedBlockers(beadID string, q BlockerQuerier) ([]string, error) {
	if q == nil {
		return nil, nil
	}
	deps, err := q.DepList(beadID, "down")
	if err != nil {
		return nil, fmt.Errorf("reading dependencies of %s: %w", beadID, err)
	}
	var blockers []string
	for _, d := range deps {
		if !beads.IsReadyBlockingDependencyType(d.Type) {
			continue
		}
		target, err := q.Get(d.DependsOnID)
		if err != nil {
			continue
		}
		if target.Status != "closed" {
			blockers = append(blockers, d.DependsOnID)
		}
	}
	return blockers, nil
}
