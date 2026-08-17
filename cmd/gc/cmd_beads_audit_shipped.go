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
	"Replay known durable delivery evidence with: gc landing stamp <gcl-event-id>",
	"If the work did not ship, reclassify the outcome to the truthful non-shipped value before closing it.",
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
	sources, sourceErrs := shippedAuditSources(cityPath, cfg)
	evidence, closeEvidence, evidenceErr := openWorkRecordEvidenceJournal(cityPath, cfg)
	if evidenceErr == nil {
		defer closeEvidence()
	}
	report := workclose.AuditShipped(evidence, sources)
	report.Errors = append(report.Errors, sourceErrs...)
	if evidenceErr != nil {
		report.Errors = append(report.Errors, "event journal: "+evidenceErr.Error())
	}
	if len(report.Errors) != 0 {
		report.Complete = false
	}
	return renderShippedAudit(report, format, stdout, stderr)
}

func shippedAuditSources(cityPath string, cfg *config.City) ([]workclose.AuditStore, []string) {
	byRef := make(map[string]workclose.AuditStore)
	cityRef := "city:" + loadedCityName(cfg, cityPath)
	cityStore, cityErr := openStoreAtForCity(cityPath, cityPath)
	byRef[cityRef] = workclose.AuditStore{StoreRef: cityRef, Store: cityStore, OpenError: cityErr}

	rigStores := make(map[string]beads.Store)
	for _, rig := range cfg.Rigs {
		ref := "rig:" + strings.TrimSpace(rig.Name)
		if strings.TrimSpace(rig.Name) == "" {
			continue
		}
		if strings.TrimSpace(rig.Path) == "" {
			byRef[ref] = workclose.AuditStore{StoreRef: ref, OpenError: fmt.Errorf("rig has no path binding")}
			continue
		}
		store, err := openStoreAtForCity(resolveStoreScopeRoot(cityPath, rig.Path), cityPath)
		byRef[ref] = workclose.AuditStore{StoreRef: ref, Store: store, OpenError: err}
		if err == nil {
			rigStores[rig.Name] = store
		}
	}

	var errs []string
	topology := cliResidencyTopology(cityPath, cfg, cityStore, rigStores)
	plan, err := storeref.Plan(storeref.Census{}, topology)
	if err != nil {
		errs = append(errs, "authoritative store topology: "+err.Error())
		for _, binding := range topology.Bindings {
			ref := string(binding.Leg.Ref)
			byRef[ref] = workclose.AuditStore{StoreRef: ref, Store: binding.Leg.Store, OpenError: err}
		}
	} else {
		storeref.EachLeg(plan, func(leg storeref.Leg, _ storeref.Role, _ storeref.ErrPolicy) {
			ref := censusRef(cfg, leg.Ref, censusRefScoped)
			if _, exists := byRef[ref]; !exists {
				byRef[ref] = workclose.AuditStore{StoreRef: ref, Store: leg.Store}
			}
		})
	}
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	out := make([]workclose.AuditStore, 0, len(refs))
	for _, ref := range refs {
		out = append(out, byRef[ref])
	}
	return out, errs
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
		_ = json.NewEncoder(stdout).Encode(shippedAuditJSON{SchemaVersion: shippedAuditSchemaVersion, Complete: report.Complete, Clean: report.Clean(), Groups: report.Groups, Errors: report.Errors, Remediation: shippedAuditRemediation})
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
