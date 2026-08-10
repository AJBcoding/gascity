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
	s := NewBdStore("/city/skips-message-beads", runner)

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
	s := NewBdStore("/city/latches-off-after-repeated-failures", runner)
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
	s := NewBdStore("/city/transient-failure-does-not-latch", runner)
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

// TestReadyProjectionCapabilityOutlivesStoreRebuild guards gas-x5k4. The az-3ie
// latch above is documented to hold "for the life of the process", but it lived
// on the BdStore instance — and the control-dispatcher path rebuilds that
// instance on every drain sweep (controlReadyCacheFor reuses a primed snapshot
// only for controlReadyCacheTTL, then opens a brand-new store), so the
// consecutive-failure count restarted at zero before it could ever reach the
// threshold. On an external-MySQL rig, where `bd sql` can never succeed, that
// meant two doomed subprocesses (`bd version`, then `bd sql`) plus a recorded
// problem on every sweep for the life of the dispatcher.
//
// Whether `bd sql` can serve this projection is a property of the store
// directory's backend, not of one short-lived store object, so both the version
// probe and the latch must outlive the rebuild.
func TestReadyProjectionCapabilityOutlivesStoreRebuild(t *testing.T) {
	const dir = "/city/capability-outlives-store-rebuild"
	var sqlCalls, versionCalls int
	runner := func(_, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case joined == "bd version":
			versionCalls++
			return []byte("bd version 1.1.0 (dev)\n"), nil
		case len(args) > 0 && args[0] == "sql":
			sqlCalls++
			return nil, fmt.Errorf("exit status 1: Error: 'bd sql' is not yet supported in embedded mode")
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	items := []Bead{{ID: "gas-task", Type: "task", Status: "open"}}

	// Ten drain sweeps, each with the freshly-opened store the control-ready
	// cache builds once its TTL has lapsed.
	for i := 0; i < 10; i++ {
		out, err := NewBdStore(dir, runner).enrichReadyProjectionForCache(items)
		if got := out[0].IsBlocked; got != nil {
			t.Fatalf("sweep %d: IsBlocked = &%v, want nil (projection unavailable)", i+1, *got)
		}
		if i >= bdReadyProjectionMaxFailures && err != nil {
			t.Fatalf("sweep %d still returned an error after the gate should have latched: %v", i+1, err)
		}
	}
	if sqlCalls != bdReadyProjectionMaxFailures {
		t.Errorf("bd sql called %d times across 10 rebuilt stores, want %d — a rebuilt store must inherit the latch, not restart the count",
			sqlCalls, bdReadyProjectionMaxFailures)
	}
	if versionCalls != 1 {
		t.Errorf("bd version probed %d times across 10 rebuilt stores, want 1 — the version gate answers for the directory, not for the store object",
			versionCalls)
	}
}

// TestReadyProjectionLatchRestoresUsableCacheOnRebuild pins the payoff of
// gas-x5k4, one layer up from the latch itself. A projection failure joins
// PrimeActive's partialErr, and PrimeActive publishes that as primePartialErr —
// which CachedReady treats as "cache not trustworthy", returning ok=false. So
// on a rig where `bd sql` can never succeed, every control-dispatcher sweep
// built a whole cache and then could not use it, falling back to the live
// `bd ready` query it was built to avoid. Once the gate latches, the prime must
// come back clean and the cache must serve.
func TestReadyProjectionLatchRestoresUsableCacheOnRebuild(t *testing.T) {
	const dir = "/city/latch-restores-usable-cache"
	runner := func(_, _ string, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, fmt.Errorf("empty bd command")
		}
		switch args[0] {
		case "version":
			return []byte("bd version 1.1.0 (dev)\n"), nil
		case "sql":
			return nil, fmt.Errorf("exit status 1: Error: 'bd sql' is not yet supported in embedded mode")
		case "list":
			if strings.Contains(strings.Join(args, " "), "--status=open") {
				return []byte(`[{"id":"gas-ready","title":"ready","status":"open","issue_type":"task","created_at":"2026-01-01T00:00:00Z","labels":["task"],"metadata":{}}]`), nil
			}
			return []byte(`[]`), nil
		}
		return []byte(`[]`), nil
	}

	var servedAt int
	for i := 1; i <= 6; i++ {
		cache := NewCachingStoreForTest(NewBdStore(dir, runner), nil)
		if err := cache.PrimeActive(); err != nil {
			t.Fatalf("sweep %d: PrimeActive: %v", i, err)
		}
		if _, ok := cache.CachedReady(); ok {
			servedAt = i
			break
		}
	}
	if servedAt == 0 {
		t.Fatal("cache never became usable across 6 rebuilt stores — every sweep still pays a full prime and then falls back to a live bd ready query")
	}
	if servedAt > bdReadyProjectionMaxFailures+1 {
		t.Errorf("cache first served on sweep %d, want by sweep %d — the latch must stop poisoning the prime as soon as it engages",
			servedAt, bdReadyProjectionMaxFailures+1)
	}
}

// TestReadyProjectionCapabilityIsPerStoreDirectory guards the other half of
// gas-x5k4's fix: the verdict is shared per store directory, so latching a
// MySQL-backed rig off must not disable the projection for a Dolt-backed rig in
// the same process. One `gc` process routinely holds stores for several rigs at
// once (the control dispatcher, the API server) and their backends differ.
func TestReadyProjectionCapabilityIsPerStoreDirectory(t *testing.T) {
	const mysqlDir = "/city/per-directory-mysql"
	const doltDir = "/city/per-directory-dolt"
	newRunner := func(supported bool, sqlCalls *int) CommandRunner {
		return func(_, name string, args ...string) ([]byte, error) {
			joined := name + " " + strings.Join(args, " ")
			switch {
			case joined == "bd version":
				return []byte("bd version 1.1.0 (dev)\n"), nil
			case len(args) > 0 && args[0] == "sql":
				if sqlCalls != nil {
					*sqlCalls++
				}
				if !supported {
					return nil, fmt.Errorf("exit status 1: Error: 'bd sql' is not yet supported in embedded mode")
				}
				return []byte(`[{"id":"gas-task","is_blocked":true}]`), nil
			}
			return nil, fmt.Errorf("unexpected command: %s", joined)
		}
	}
	items := []Bead{{ID: "gas-task", Type: "task", Status: "open"}}

	// Drive the MySQL-backed directory past its latch threshold.
	for i := 0; i <= bdReadyProjectionMaxFailures; i++ {
		_, _ = NewBdStore(mysqlDir, newRunner(false, nil)).enrichReadyProjectionForCache(items)
	}

	var doltSQLCalls int
	out, err := NewBdStore(doltDir, newRunner(true, &doltSQLCalls)).enrichReadyProjectionForCache(items)
	if err != nil {
		t.Fatalf("healthy directory: %v", err)
	}
	if doltSQLCalls != 1 {
		t.Errorf("bd sql called %d times for the healthy directory, want 1 — another directory's latch must not disable it", doltSQLCalls)
	}
	if got := out[0].IsBlocked; got == nil || !*got {
		t.Errorf("IsBlocked = %v, want &true — the healthy directory must still enrich", got)
	}
}

// TestEnrichReadyProjectionForCacheSkipsNudgeBeads guards the same cache
// convergence invariant for durable nudge queue beads. They are transient
// notifications represented as chore beads, not dependency-blocked work.
func TestEnrichReadyProjectionForCacheSkipsNudgeBeads(t *testing.T) {
	runner := func(_, name string, args ...string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case joined == "bd version":
			return []byte("bd version 1.1.0\n"), nil
		case len(args) > 0 && args[0] == "sql":
			return []byte(`[{"id":"gc-wisp-nudge","is_blocked":false},{"id":"gcg-task","is_blocked":false}]`), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", joined)
	}
	s := NewBdStore("/city", runner)

	items := []Bead{
		{ID: "gc-wisp-nudge", Type: "chore", Status: "open", Labels: []string{"gc:nudge"}},
		{ID: "gcg-task", Type: "task", Status: "open"},
	}
	out, err := s.enrichReadyProjectionForCache(items)
	if err != nil {
		t.Fatalf("enrichReadyProjectionForCache: %v", err)
	}

	byID := make(map[string]Bead, len(out))
	for _, b := range out {
		byID[b.ID] = b
	}
	if got := byID["gc-wisp-nudge"].IsBlocked; got != nil {
		t.Errorf("nudge bead IsBlocked = &%v, want nil (must be skipped so the reconcile diff converges)", *got)
	}
	if got := byID["gcg-task"].IsBlocked; got == nil || *got {
		t.Errorf("task bead IsBlocked = %v, want &false (real work must still be enriched)", got)
	}
}
