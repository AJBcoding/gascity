package session

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestEnsureSessionAliasAvailableReleasesDeadHolders(t *testing.T) {
	mk := func(t *testing.T, store beads.Store, alias string, extra map[string]string) beads.Bead {
		t.Helper()
		md := map[string]string{"alias": alias, "gc.kind": "session"}
		for k, v := range extra {
			md[k] = v
		}
		b, err := store.Create(beads.Bead{
			Title:    alias,
			Type:     BeadType,
			Labels:   []string{LabelSession},
			Metadata: md,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("drained holder releases the alias", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.pool-slot-1", map[string]string{
			"state":    string(StateDrained),
			"drain_at": "2026-07-29T00:00:00Z",
		})

		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.pool-slot-1", "gcs-session-new", ""); err != nil {
			t.Fatalf("drained holder must release the alias, got: %v", err)
		}
	})

	t.Run("canceled drain live holder still blocks", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.pool-slot-2", map[string]string{
			"drain_at": "2026-07-29T11:06:16Z",
		})

		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.pool-slot-2", "gcs-session-new", ""); err == nil {
			t.Fatal("live holder with a drain_at stamp must keep reserving its alias")
		}
	})

	t.Run("runtime missing holder releases the alias", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.pool-slot-3", map[string]string{
			"sleep_reason": LifecycleReasonRuntimeMissing,
		})

		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.pool-slot-3", "gcs-session-new", ""); err != nil {
			t.Fatalf("runtime-missing holder must release the alias, got: %v", err)
		}
	})

	t.Run("live open holder still blocks", func(t *testing.T) {
		store := beads.NewMemStore()
		mk(t, store, "rig/gc.pool-slot-4", nil)

		if err := ensureSessionAliasAvailable(store, nil, "rig/gc.pool-slot-4", "gcs-session-new", ""); err == nil {
			t.Fatal("live open holder must still block the alias")
		}
	})
}
