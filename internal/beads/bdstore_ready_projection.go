package beads

import (
	"encoding/json"
	"fmt"

	"github.com/gastownhall/gascity/internal/deps"
)

const bdReadyProjectionMinVersion = "1.0.5"

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
	s.readyProjectionMu.Lock()
	defer s.readyProjectionMu.Unlock()
	// Probe the bd version once per process. Operators must restart gc after
	// changing bd versions to re-evaluate ready-projection support.
	if s.readyProjectionChecked {
		return s.readyProjectionEnabled, nil
	}
	out, err := s.runner(s.dir, "bd", "version")
	if err != nil {
		return false, fmt.Errorf("bd ready projection version gate: %w", err)
	}
	version, err := parseBDVersion(string(out))
	if err != nil {
		return false, fmt.Errorf("bd ready projection version gate: %w", err)
	}
	s.readyProjectionEnabled = deps.CompareVersions(version, bdReadyProjectionMinVersion) >= 0
	s.readyProjectionChecked = true
	return s.readyProjectionEnabled, nil
}

// bdReadyProjectionMaxFailures bounds how many consecutive projection failures
// are tolerated before the gate latches off for the rest of the process.
const bdReadyProjectionMaxFailures = 3

// noteReadyProjectionFailure records a failed projection fetch and latches the
// gate off once failures are clearly structural rather than transient.
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
func (s *BdStore) noteReadyProjectionFailure() {
	s.readyProjectionMu.Lock()
	defer s.readyProjectionMu.Unlock()
	s.readyProjectionFailures++
	if s.readyProjectionFailures >= bdReadyProjectionMaxFailures {
		s.readyProjectionEnabled = false
	}
}

// noteReadyProjectionSuccess clears the consecutive-failure count so a transient
// error never accumulates toward the latch across an otherwise healthy process.
func (s *BdStore) noteReadyProjectionSuccess() {
	s.readyProjectionMu.Lock()
	defer s.readyProjectionMu.Unlock()
	s.readyProjectionFailures = 0
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
