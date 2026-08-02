package beads

import (
	"fmt"
	"strings"
	"testing"
)

// TestEnrichReadyProjectionForCacheSkipsMessageBeads guards the cache-reconcile
// convergence fix: message (mail) beads are never dependency-blocked ready work,
// and bd's denormalized is_blocked column flaps NULL<->false for ephemeral mail
// wisps. Feeding them through the ready projection makes the CachingStore
// reconciler re-emit bead.updated for them every cycle (an event flood that
// starved gc-hook work queries). The enrichment must leave their IsBlocked nil
// so the reconcile diff converges, while still enriching real work beads.
func TestEnrichReadyProjectionForCacheSkipsMessageBeads(t *testing.T) {
	runner := func(_, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case joined == "bd version":
			return []byte("bd version 1.1.0\n"), nil
		case len(args) > 0 && args[0] == "sql":
			// bd reports both ids as not-blocked; a buggy enrichment would
			// stamp IsBlocked=&false on the message bead too.
			return []byte(`[{"id":"mc-wisp-mail","is_blocked":false},{"id":"gcg-task","is_blocked":false}]`), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	s := NewBdStore("/city", runner)

	items := []Bead{
		{ID: "mc-wisp-mail", Type: "message", Status: "open"}, // ephemeral mail, IsBlocked nil
		{ID: "gcg-task", Type: "task", Status: "open"},        // real work, IsBlocked nil
	}
	out, err := s.enrichReadyProjectionForCache(items)
	if err != nil {
		t.Fatalf("enrichReadyProjectionForCache: %v", err)
	}

	byID := make(map[string]Bead, len(out))
	for _, b := range out {
		byID[b.ID] = b
	}
	if got := byID["mc-wisp-mail"].IsBlocked; got != nil {
		t.Errorf("message bead IsBlocked = &%v, want nil (must be skipped so the reconcile diff converges)", *got)
	}
	if got := byID["gcg-task"].IsBlocked; got == nil || *got {
		t.Errorf("task bead IsBlocked = %v, want &false (real work must still be enriched)", got)
	}
}

// TestReadyProjectionLatchesOffAfterRepeatedFailures guards az-3ie: the version
// gate alone is not sufficient. `bd sql` runs against SQLite or Dolt, so on a
// MySQL-backed city a bd new enough to HAVE the subcommand still answers
// "'bd sql' is not yet supported in embedded mode". Version-gating only meant
// every cache prime and reconcile tick shelled out, failed, and recorded a
// problem for the life of the process. After a bounded number of consecutive
// failures the gate must latch off and stop shelling out entirely.
func TestReadyProjectionLatchesOffAfterRepeatedFailures(t *testing.T) {
	var sqlCalls int
	runner := func(_, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case joined == "bd version":
			return []byte("bd version 1.1.0 (dev)\n"), nil
		case len(args) > 0 && args[0] == "sql":
			sqlCalls++
			return nil, fmt.Errorf("exit status 1: Error: 'bd sql' is not yet supported in embedded mode")
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	s := NewBdStore("/city", runner)
	items := []Bead{{ID: "py-task", Type: "task", Status: "open"}}

	// Ten ticks stand in for "forever". Only the first few may shell out.
	for i := 0; i < 10; i++ {
		out, err := s.enrichReadyProjectionForCache(items)
		if got := out[0].IsBlocked; got != nil {
			t.Fatalf("tick %d: IsBlocked = &%v, want nil (projection unavailable)", i+1, *got)
		}
		if i >= bdReadyProjectionMaxFailures && err != nil {
			t.Fatalf("tick %d still returned an error after the gate should have latched: %v", i+1, err)
		}
	}
	if sqlCalls != bdReadyProjectionMaxFailures {
		t.Errorf("bd sql called %d times across 10 ticks, want %d — the gate must latch, not retry every tick",
			sqlCalls, bdReadyProjectionMaxFailures)
	}
}

// TestReadyProjectionTransientFailureDoesNotLatch guards the other half: a
// one-off blip must not permanently disable the projection for the process.
func TestReadyProjectionTransientFailureDoesNotLatch(t *testing.T) {
	var sqlCalls int
	runner := func(_, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case joined == "bd version":
			return []byte("bd version 1.1.0 (dev)\n"), nil
		case len(args) > 0 && args[0] == "sql":
			sqlCalls++
			if sqlCalls%2 == 0 { // succeed, fail, succeed, fail … (never 3 in a row)
				return nil, fmt.Errorf("transient: database is locked")
			}
			return []byte(`[{"id":"py-task","is_blocked":false}]`), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	s := NewBdStore("/city", runner)
	items := []Bead{{ID: "py-task", Type: "task", Status: "open"}}

	for i := 0; i < 10; i++ {
		_, _ = s.enrichReadyProjectionForCache(items)
	}
	if sqlCalls != 10 {
		t.Errorf("bd sql called %d times across 10 ticks, want 10 — alternating failures are transient and must not latch the gate off", sqlCalls)
	}
	out, err := s.enrichReadyProjectionForCache(items)
	if err != nil {
		t.Fatalf("projection still failing after recovery: %v", err)
	}
	if got := out[0].IsBlocked; got == nil || *got {
		t.Errorf("IsBlocked = %v, want &false — enrichment must resume once bd recovers", got)
	}
}
