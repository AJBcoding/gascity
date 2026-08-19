package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/storeref"
	"github.com/gastownhall/gascity/internal/workclose"
	"github.com/spf13/cobra"
)

const shippedAuditSchemaVersion = "1"

var shippedAuditRemediation = []string{
	"Replay known durable delivery evidence with: " + shippedAuditLandingStampCommand("<gcl-event-id>"),
	"If the work did not ship, reclassify the outcome to the truthful non-shipped value before closing it.",
}

func shippedAuditLandingStampArgs(eventID string) []string {
	return []string{"landing", "stamp", "--event", eventID}
}

func shippedAuditLandingStampCommand(eventID string) string {
	return "gc " + strings.Join(shippedAuditLandingStampArgs(eventID), " ")
}

func newBeadsAuditShippedCmd(stdout, stderr io.Writer) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "audit-shipped",
		Short: "Audit shipped work records for durable landing evidence",
		Long: `Read every authoritative city, rig, and relocated-class store and report
task-like records (including active and closed rows) whose exact gc.work_outcome is shipped but
whose landing stamp cannot be verified. This command never rewrites records.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cmdBeadsAuditShipped(format, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func cmdBeadsAuditShipped(format string, stdout, stderr io.Writer) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc beads audit-shipped: %v\n", err) //nolint:errcheck
		return 1
	}
	cfg, prov, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityPath, "city.toml"))
	if err != nil {
		fmt.Fprintf(stderr, "gc beads audit-shipped: %v\n", err) //nolint:errcheck
		return 1
	}
	emitLoadCityConfigWarnings(stderr, prov)
	sources := shippedAuditSources(cityPath, cfg)
	evidence, _, evidenceErr := openWorkRecordEvidenceJournal(cityPath, cfg)
	report := workclose.AuditShipped(evidence, sources.stores)
	report.Errors = append(report.Errors, sources.errors...)
	if evidenceErr != nil {
		report.Errors = append(report.Errors, "event journal: "+evidenceErr.Error())
	} else if err := evidence.Close(); err != nil {
		report.Errors = append(report.Errors, "event journal close: "+err.Error())
	}
	report.Errors = append(report.Errors, sources.closeOwned()...)
	if len(report.Errors) != 0 {
		report.Complete = false
	}
	return renderShippedAudit(report, format, stdout, stderr)
}

type shippedAuditOwnedStore struct {
	label string
	store beads.Store
}

type shippedAuditSourceSet struct {
	stores []workclose.AuditStore
	errors []string
	owned  []shippedAuditOwnedStore
}

// closeOwned closes only the city and rig handles this audit opened. Relocated
// class handles belong to the process-wide topology funnel and stay open until
// that owner closes them. Pointer identity prevents aliases from being closed
// twice; any close failure makes the audit honestly partial.
func (s *shippedAuditSourceSet) closeOwned() []string {
	closed := make(map[uintptr]struct{}, len(s.owned))
	var errs []string
	for _, owned := range s.owned {
		if key, ok := storePointerKey(owned.store); ok {
			if _, duplicate := closed[key]; duplicate {
				continue
			}
			closed[key] = struct{}{}
		}
		if err := closeBeadStoreHandle(owned.store); err != nil {
			errs = append(errs, fmt.Sprintf("authoritative store %s close: %v", owned.label, err))
		}
	}
	return errs
}

// shippedAuditSources opens every store this audit must read and names each one
// with the ref its landing evidence is recorded under.
//
// # Why the legs are enumerated rather than walked
//
// The set is a SUPERSET of the resolver's plan, and that is the reason. A rig
// whose store fails to open, and one configured with no path at all, never
// becomes a topology leg — but it is exactly the store an audit has to name as
// unverified, so it is added here carrying its open error and reported as an
// errored group. Reading inside storeref.Walk would drop it silently, which is
// the opposite of what an audit is for. The per-leg error policy is overridden
// for the same reason: every store that could not be read is REPORTED and makes
// the report incomplete, because "what I could not verify" is this command's
// output rather than a degradation of it.
//
// What this function does not do is decide the leg set. It takes the legs the
// resolver names, in the plan the resolver built, and every read then happens in
// workclose.AuditShipped — the domain owner — over the assembled sources.
func shippedAuditSources(cityPath string, cfg *config.City) shippedAuditSourceSet {
	byRef := make(map[string]workclose.AuditStore)
	byIdentity := make(map[uintptr]string)
	result := shippedAuditSourceSet{}
	add := func(ref string, store beads.Store, openErr error, owned bool) {
		if owned && store != nil && openErr == nil {
			result.owned = append(result.owned, shippedAuditOwnedStore{label: ref, store: store})
		}
		if store != nil && openErr == nil {
			if key, ok := storePointerKey(store); ok {
				if _, duplicate := byIdentity[key]; duplicate {
					return
				}
				byIdentity[key] = ref
			}
		}
		byRef[ref] = workclose.AuditStore{StoreRef: ref, Store: store, OpenError: openErr}
	}
	cityRef := "city:" + loadedCityName(cfg, cityPath)
	cityStore, cityErr := openAuthoritativeStoreAtForCityWithConfig(cityPath, cityPath, cfg)
	add(cityRef, cityStore, cityErr, true)

	rigStores := make(map[string]beads.Store)
	for _, rig := range cfg.Rigs {
		ref := "rig:" + strings.TrimSpace(rig.Name)
		if strings.TrimSpace(rig.Name) == "" {
			continue
		}
		if strings.TrimSpace(rig.Path) == "" {
			add(ref, nil, fmt.Errorf("rig has no path binding"), false)
			continue
		}
		store, err := openAuthoritativeStoreAtForCityWithConfig(resolveStoreScopeRoot(cityPath, rig.Path), cityPath, cfg)
		add(ref, store, err, true)
		if err == nil {
			rigStores[rig.Name] = store
		}
	}

	topology := cliResidencyTopology(cityPath, cfg, cityStore, rigStores)
	plan, err := storeref.Plan(storeref.Census{}, topology)
	if err != nil {
		result.errors = append(result.errors, "authoritative store topology: "+err.Error())
		// A plan that refused hands back no leg, so each binding is named with
		// no handle at all. AuditShipped never reads a source carrying an open
		// error, and passing the store anyway would take a probe out of a plan
		// the resolver had just declined to build.
		for _, binding := range topology.Bindings {
			add(string(binding.Leg.Ref), nil, err, false)
		}
	} else {
		storeref.EachLeg(plan, func(leg storeref.Leg, _ storeref.Role, _ storeref.ErrPolicy) { // residency:allow — the audit must also name stores no plan can contain, so its reads cannot run inside Walk
			ref := censusRef(cfg, leg.Ref, censusRefScoped)
			add(ref, leg.Store, nil, false)
		})
	}
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	result.stores = make([]workclose.AuditStore, 0, len(refs))
	for _, ref := range refs {
		result.stores = append(result.stores, byRef[ref])
	}
	return result
}

type shippedAuditJSON struct {
	SchemaVersion string                 `json:"schema_version"`
	Complete      bool                   `json:"complete"`
	Clean         bool                   `json:"clean"`
	Groups        []workclose.AuditGroup `json:"groups"`
	Errors        []string               `json:"errors,omitempty"`
	Remediation   []string               `json:"remediation"`
}

func renderShippedAudit(report workclose.AuditReport, format string, stdout, stderr io.Writer) int {
	if format != "text" && format != "json" {
		fmt.Fprintf(stderr, "gc beads audit-shipped: unsupported format %q (want text or json)\n", format) //nolint:errcheck
		return 1
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(shippedAuditJSON{SchemaVersion: shippedAuditSchemaVersion, Complete: report.Complete, Clean: report.Clean(), Groups: report.Groups, Errors: report.Errors, Remediation: shippedAuditRemediation}); err != nil {
			fmt.Fprintf(stderr, "gc beads audit-shipped: encode json: %v\n", err) //nolint:errcheck
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "Shipped work-record audit: complete=%t clean=%t\n", report.Complete, report.Clean()) //nolint:errcheck
		for _, group := range report.Groups {
			fmt.Fprintf(stdout, "[%s]\n", group.StoreRef) //nolint:errcheck
			for _, finding := range group.Findings {
				fmt.Fprintf(stdout, "  %s (%s): %s\n", finding.BeadID, finding.Status, strings.Join(finding.Violations, "; ")) //nolint:errcheck
			}
			if group.Error != "" {
				fmt.Fprintf(stdout, "  ERROR: %s\n", group.Error) //nolint:errcheck
			}
		}
		for _, err := range report.Errors {
			fmt.Fprintf(stdout, "ERROR: %s\n", err) //nolint:errcheck
		}
		if !report.Clean() {
			fmt.Fprintln(stdout, "Remediation (choose one; this audit performs no rewrites):") //nolint:errcheck
			for _, line := range shippedAuditRemediation {
				fmt.Fprintln(stdout, "  - "+line) //nolint:errcheck
			}
		}
	}
	if report.Clean() {
		return 0
	}
	return 1
}
