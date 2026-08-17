package workclose

import (
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/workstamp"
)

// AuditStore is one already-open authoritative store and its canonical ref.
type AuditStore struct {
	StoreRef  string
	Store     beads.Store
	OpenError error
}

// AuditFinding is one task-like exact-shipped record whose durable
// landing stamp cannot be verified.
type AuditFinding struct {
	BeadID     string   `json:"bead_id"`
	Status     string   `json:"status"`
	Violations []string `json:"violations"`
}

// AuditGroup is the complete result for one canonical physical store.
type AuditGroup struct {
	StoreRef string         `json:"store_ref"`
	Findings []AuditFinding `json:"findings"`
	Error    string         `json:"error,omitempty"`
}

// AuditReport keeps usable findings even when another authoritative leg fails.
type AuditReport struct {
	Complete bool         `json:"complete"`
	Groups   []AuditGroup `json:"groups"`
	Errors   []string     `json:"errors,omitempty"`
}

// Clean reports a complete audit with no invalid shipped records.
func (r AuditReport) Clean() bool {
	if !r.Complete {
		return false
	}
	for _, group := range r.Groups {
		if len(group.Findings) != 0 {
			return false
		}
	}
	return true
}

// AuditShipped reads every supplied authoritative store without mutation. It
// intentionally matches only the exact raw outcome "shipped", task-like rows,
// across both active and closed statuses; migration cleanup must not normalize malformed data
// or broaden the work-record vocabulary behind the operator's back.
func AuditShipped(evidence workstamp.EvidenceReader, stores []AuditStore) AuditReport {
	ordered := append([]AuditStore(nil), stores...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].StoreRef < ordered[j].StoreRef })
	report := AuditReport{Complete: true, Groups: make([]AuditGroup, 0, len(ordered))}
	for _, source := range ordered {
		group := AuditGroup{StoreRef: source.StoreRef, Findings: []AuditFinding{}}
		if source.OpenError != nil || source.Store == nil {
			group.Error = "authoritative store is unavailable"
			if source.OpenError != nil {
				group.Error += ": " + source.OpenError.Error()
			}
			report.Complete = false
			report.Groups = append(report.Groups, group)
			continue
		}
		rows, err := source.Store.List(beads.ListQuery{AllowScan: true, IncludeClosed: true, Live: true})
		for _, bead := range rows {
			if bead.Metadata[beadmeta.WorkOutcomeMetadataKey] != beadmeta.WorkOutcomeShipped || !AppliesTo(bead) {
				continue
			}
			status := strings.TrimSpace(bead.Status)
			violations := workstamp.ValidateLandingEvidence(evidence, source.StoreRef, bead)
			if len(violations) != 0 {
				group.Findings = append(group.Findings, AuditFinding{BeadID: bead.ID, Status: status, Violations: violations})
			}
		}
		sort.Slice(group.Findings, func(i, j int) bool { return group.Findings[i].BeadID < group.Findings[j].BeadID })
		if err != nil {
			group.Error = err.Error()
			report.Complete = false
		}
		report.Groups = append(report.Groups, group)
	}
	return report
}
