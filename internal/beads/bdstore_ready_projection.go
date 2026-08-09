package beads

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gastownhall/gascity/internal/deps"
)

const bdReadyProjectionMinVersion = "1.0.5"

// readyProjectionCapability is one store directory's verdict on whether
// `bd sql` can serve the ready projection there: the memoized version-gate
// answer plus the consecutive-failure latch.
//
// The verdict is deliberately scoped to the directory and the process rather
// than to a BdStore instance (gas-x5k4). Whether bd can answer `bd sql` is a
// property of that directory's beads backend, and short-lived stores are the
// norm on the hot paths: the control dispatcher opens a brand-new store on
// every drain sweep (cmd/gc's controlReadyCacheFor reuses a primed snapshot
// only for controlReadyCacheTTL). An instance-scoped latch therefore never
// survived long enough to reach its threshold, and each sweep re-paid a doomed
// `bd version` plus `bd sql` on MySQL-backed rigs — forever.
type readyProjectionCapability struct {
	mu       sync.Mutex
	checked  bool
	enabled  bool
	failures int
}

// readyProjectionCapabilities memoizes one capability per store directory. The
// registry mutex covers only the map lookup, never a subprocess call, so stores
// for different rigs probe concurrently while stores sharing a directory
// serialize behind that directory's own mutex and probe bd just once between
// them. Keys are store directories, so the map is bounded by the number of rigs
// a process opens.
//
// One directory therefore means one verdict for the whole process: in
// production that is exactly right (a directory has one beads backend), but a
// test that stubs `bd version` or `bd sql` must give its store its own
// directory rather than sharing a fixture path with its neighbors.
var readyProjectionCapabilities = struct {
	mu    sync.Mutex
	byDir map[string]*readyProjectionCapability
}{byDir: make(map[string]*readyProjectionCapability)}

// readyProjectionCapabilityFor returns dir's capability record, creating it on
// first use.
func readyProjectionCapabilityFor(dir string) *readyProjectionCapability {
	readyProjectionCapabilities.mu.Lock()
	defer readyProjectionCapabilities.mu.Unlock()
	capability, ok := readyProjectionCapabilities.byDir[dir]
	if !ok {
		capability = &readyProjectionCapability{}
		readyProjectionCapabilities.byDir[dir] = capability
	}
	return capability
}

type bdReadyProjectionRow struct {
	ID        string       `json:"id"`
	IsBlocked optionalBool `json:"is_blocked"`
}

func (s *BdStore) enrichReadyProjectionForCache(items []Bead) ([]Bead, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		// Message (mail) beads are never dependency-blocked ready work, and
		// bd's denormalized is_blocked column flaps NULL<->false for ephemeral
		// mail wisps. Enriching them makes the CachingStore reconciler re-emit
		// bead.updated for every open mail bead on every cycle (an event flood
		// that starves gc-hook work queries). Leave their IsBlocked at bd's nil
		// fallback so the reconcile diff converges.
		if item.ID == "" || item.Status == "closed" || item.IsBlocked != nil || item.Type == "message" {
			continue
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return items, nil
	}
	enabled, err := s.bdReadyProjectionEnabled()
	if err != nil {
		return items, err
	}
	if !enabled {
		return items, nil
	}

	projection, err := s.fetchReadyProjection(ids)
	if err != nil {
		s.noteReadyProjectionFailure()
		return items, err
	}
	s.noteReadyProjectionSuccess()
	enriched := make([]Bead, len(items))
	copy(enriched, items)
	for i := range enriched {
		if enriched[i].ID == "" || enriched[i].Status == "closed" || enriched[i].IsBlocked != nil || enriched[i].Type == "message" {
			continue
		}
		blocked, ok := projection[enriched[i].ID]
		if !ok {
			continue
		}
		enriched[i].IsBlocked = cloneBoolPtr(&blocked)
	}
	return enriched, nil
}

func (s *BdStore) bdReadyProjectionEnabled() (bool, error) {
	capability := readyProjectionCapabilityFor(s.dir)
	capability.mu.Lock()
	defer capability.mu.Unlock()
	// Probe the bd version once per store directory per process. Operators must
	// restart gc after changing bd versions to re-evaluate ready-projection
	// support.
	if capability.checked {
		return capability.enabled, nil
	}
	out, err := s.runner(s.dir, "bd", "version")
	if err != nil {
		return false, fmt.Errorf("bd ready projection version gate: %w", err)
	}
	version, err := parseBDVersion(string(out))
	if err != nil {
		return false, fmt.Errorf("bd ready projection version gate: %w", err)
	}
	capability.enabled = deps.CompareVersions(version, bdReadyProjectionMinVersion) >= 0
	capability.checked = true
	return capability.enabled, nil
}

// bdReadyProjectionMaxFailures bounds how many consecutive projection failures
// are tolerated before the gate latches off for the rest of the process.
const bdReadyProjectionMaxFailures = 3

// noteReadyProjectionFailure records a failed projection fetch against the
// store's directory and latches its gate off once failures are clearly
// structural rather than transient.
//
// The version gate cannot decide this on its own: `bd sql` runs "against the
// underlying database (SQLite or Dolt)", so on a MySQL-backed city a bd new
// enough to HAVE the subcommand still cannot serve it — embedded mode answers
// "'bd sql' is not yet supported in embedded mode". Version-gating alone meant
// every cache prime and reconcile tick shelled out, failed, and recorded a
// problem forever (az-3ie). Latching after a few consecutive failures reports
// the condition without repeating it for the life of the process, while a
// one-off blip still recovers via noteReadyProjectionSuccess. The projection is
// a cost optimisation, not a correctness gate, so losing it is safe: callers
// already treat a disabled projection as the no-op it is.
//
// The count lives on the directory's readyProjectionCapability, not on this
// store, so a caller that rebuilds its store between ticks still converges on
// the latch instead of restarting the count each time (gas-x5k4).
func (s *BdStore) noteReadyProjectionFailure() {
	capability := readyProjectionCapabilityFor(s.dir)
	capability.mu.Lock()
	defer capability.mu.Unlock()
	capability.failures++
	if capability.failures >= bdReadyProjectionMaxFailures {
		capability.enabled = false
	}
}

// noteReadyProjectionSuccess clears the directory's consecutive-failure count so
// a transient error never accumulates toward the latch across an otherwise
// healthy process.
func (s *BdStore) noteReadyProjectionSuccess() {
	capability := readyProjectionCapabilityFor(s.dir)
	capability.mu.Lock()
	defer capability.mu.Unlock()
	capability.failures = 0
}

func (s *BdStore) fetchReadyProjection(ids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(ids))
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			wanted[id] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return result, nil
	}

	// bd exposes this as an active-row projection: the SQL filters out closed
	// rows so cache prime/reconcile cost stays O(active work) instead of
	// scanning unbounded closed issue/wisp history every cycle. The ids
	// argument is a cache-side allow-list so callers can keep their requested
	// surface bounded. A row that races closed between the list snapshot and
	// this fetch drops out of the projection; the reconciler preserves its last
	// cached is_blocked (preserveCachedReadyProjectionLocked) so the absence
	// does not flap a spurious bead.updated.
	out, err := s.runner(s.dir, "bd", "sql", readyProjectionSQL(), "--json")
	if err != nil {
		return nil, fmt.Errorf("bd sql ready projection: %w", err)
	}
	var rows []bdReadyProjectionRow
	if err := json.Unmarshal(extractJSON(out), &rows); err != nil {
		return nil, fmt.Errorf("bd sql ready projection: parsing JSON: %w", err)
	}
	for _, row := range rows {
		if row.ID == "" || !row.IsBlocked.set {
			continue
		}
		if _, ok := wanted[row.ID]; !ok {
			continue
		}
		result[row.ID] = row.IsBlocked.value
	}
	return result, nil
}

func readyProjectionSQL() string {
	return "select id,is_blocked from issues where status <> 'closed' union all select id,is_blocked from wisps where status <> 'closed'"
}
