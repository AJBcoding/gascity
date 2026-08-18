package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/rollout"
)

// bdContractStore is a store that answers the ONE question the bounded
// beads.shipped_close_warn_only mode turns on — "is this target written
// through the pinned bd contract that cannot revision-fence a close?" — the
// way a bd-backed store does, while keeping an in-memory write path.
//
// A real *beads.BdStore would need a bd binary and a live subprocess for a
// question that is pure store identity, and the write it fronts is not what
// this test is about. The faithfulness of the double is pinned separately in
// internal/beads (TestStoreUsesBDContract).
type bdContractStore struct{ *beads.MemStore }

func (bdContractStore) UsesBDContract() bool { return true }

// seedPolicyInvalidWorkBead pins a work bead that violates the work-record
// close contract in the plainest possible way: it carries no typed
// gc.work_outcome at all. Nothing about it is a fencing problem, so nothing
// about it is a bd-compatibility problem.
func seedPolicyInvalidWorkBead(t *testing.T, mem *beads.MemStore, id string) string {
	t.Helper()
	mem.HonorExplicitIDs = true
	created, err := mem.Create(beads.Bead{ID: id, Title: "unstamped work"})
	if err != nil {
		t.Fatalf("seeding %s: %v", id, err)
	}
	if created.ID != id {
		t.Fatalf("fixture store minted %q instead of the pinned %q", created.ID, id)
	}
	return id
}

// TestShippedCloseWarnOnlyRelaxesOnlyTheBDBackedTarget is the mixed-topology
// case: one city, one city-level bd-backed store, one rig-level FileStore, and
// the city-global beads.shipped_close_warn_only set true.
//
// The setting exists for one deficiency — the pinned bd cannot revision-fence
// a managed close — so it may relax only the target written through that
// contract. The FileStore rig fences natively and has no compatibility need,
// so it must keep STRICT shipped-close enforcement even while the flag is on.
// Asserting only the city arm would pass with the rule still resolved
// city-globally, which is exactly the bug.
func TestShippedCloseWarnOnlyRelaxesOnlyTheBDBackedTarget(t *testing.T) {
	st := newFakeState(t)
	st.cfg.Workspace.Prefix = "cty"
	st.cfg.Rigs = []config.Rig{{Name: "filerig", Path: filepath.Join(t.TempDir(), "filerig"), Prefix: "fr"}}

	cityMem := beads.NewMemStore()
	st.cityBeadStore = bdContractStore{MemStore: cityMem}
	rigStore, err := beads.OpenFileStore(fsys.OSFS{}, filepath.Join(t.TempDir(), "rig-beads.json"))
	if err != nil {
		t.Fatalf("opening rig file store: %v", err)
	}
	st.stores = map[string]beads.Store{"filerig": rigStore}

	cityID := seedPolicyInvalidWorkBead(t, cityMem, "cty-1")
	rigID := seedPolicyInvalidWorkBead(t, rigStore.MemStore, "fr-1")

	s := newServer(rolloutProviderState{
		fakeState: st,
		flags:     rollout.ForTest(rollout.WithShippedCloseWarnOnly(true)),
	}, false)

	if _, err := s.humaHandleBeadClose(context.Background(), &BeadCloseInput{ID: cityID}); err != nil {
		t.Fatalf("bd-backed city close refused while warn-only is on: %v", err)
	}

	_, err = s.humaHandleBeadClose(context.Background(), &BeadCloseInput{ID: rigID})
	if err == nil {
		t.Fatal("the FileStore rig lost strict shipped-close enforcement to a bd-compatibility flag it has no need of")
	}
	if !strings.Contains(err.Error(), "work-record close refused") {
		t.Fatalf("FileStore rig close error = %v, want the work-record refusal", err)
	}
	closed, getErr := rigStore.Get(rigID)
	if getErr != nil {
		t.Fatalf("re-reading %s: %v", rigID, getErr)
	}
	if closed.Status == "closed" {
		t.Fatal("the refused close still mutated the FileStore rig")
	}

	// The scoping narrows WHICH closes warn, not what warning costs: the arm
	// that legitimately warns still emits the durable use record.
	used, listErr := st.eventProv.List(events.Filter{Type: "work.close.warn_only.used"})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(used) != 1 {
		t.Fatalf("warn-only telemetry events = %d, want exactly one (the bd-backed city close)", len(used))
	}
	if used[0].Subject != cityID {
		t.Fatalf("warn-only telemetry subject = %q, want the bd-backed %q", used[0].Subject, cityID)
	}
}

// TestShippedCloseEnforcedEverywhereWithoutTheFlag pins the other half of the
// rule: with the compatibility setting off, both providers refuse. The scoping
// added for the mixed topology cannot become a new way to relax anything.
func TestShippedCloseEnforcedEverywhereWithoutTheFlag(t *testing.T) {
	for _, warnOnly := range []bool{false, true} {
		name := "flag off"
		if warnOnly {
			name = "flag on, both targets bd-backed"
		}
		t.Run(name, func(t *testing.T) {
			st := newFakeState(t)
			cityMem := beads.NewMemStore()
			rigMem := beads.NewMemStore()
			// Both stores answer the same way, so the only variable is the flag.
			if warnOnly {
				st.cityBeadStore = bdContractStore{MemStore: cityMem}
				st.stores = map[string]beads.Store{"myrig": bdContractStore{MemStore: rigMem}}
			} else {
				st.cityBeadStore = bdContractStore{MemStore: cityMem}
				st.stores = map[string]beads.Store{"myrig": rigMem}
			}
			cityID := seedPolicyInvalidWorkBead(t, cityMem, "cty-2")
			rigID := seedPolicyInvalidWorkBead(t, rigMem, "rig-2")

			s := newServer(rolloutProviderState{
				fakeState: st,
				flags:     rollout.ForTest(rollout.WithShippedCloseWarnOnly(warnOnly)),
			}, false)

			for _, id := range []string{cityID, rigID} {
				_, err := s.humaHandleBeadClose(context.Background(), &BeadCloseInput{ID: id})
				if warnOnly && err != nil {
					t.Fatalf("bd-backed close of %s refused while warn-only is on: %v", id, err)
				}
				if !warnOnly && err == nil {
					t.Fatalf("close of %s accepted with the compatibility setting off", id)
				}
			}
		})
	}
}
