package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/workclose"
)

// mixedProviderCity writes a bd-backed city that holds one file-backed rig, and
// returns (cityDir, rigDir). It is the topology beads.shipped_close_warn_only
// was never scoped for: the city needs the compatibility mode because its
// pinned bd cannot revision-fence a close, and the rig needs nothing at all.
func mixedProviderCity(t *testing.T) (string, string) {
	t.Helper()
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "mixed"

[beads]
provider = "bd"
shipped_close_warn_only = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rigDir := filepath.Join(cityDir, "filerig")
	if err := os.MkdirAll(filepath.Join(rigDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".gc", "beads.json"), []byte("{\"seq\":0,\"beads\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cityDir, rigDir
}

// TestWorkRecordCloseTargetScopesWarnOnlyToTheBDBackedScope is the scope half of
// the mixed-topology rule: one city-global setting, two scopes, two answers.
func TestWorkRecordCloseTargetScopesWarnOnlyToTheBDBackedScope(t *testing.T) {
	cityDir, rigDir := mixedProviderCity(t)
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}

	cityTarget := workRecordCloseTargetForScope(cityDir, cityDir)
	if !cityTarget.BDStoreContract {
		t.Fatalf("city scope target = %+v, want the bd store contract", cityTarget)
	}
	rigTarget := workRecordCloseTargetForScope(rigDir, cityDir)
	if rigTarget.BDStoreContract {
		t.Fatalf("file-backed rig target = %+v, want a natively fence-capable target", rigTarget)
	}

	if workRecordEnforceEnabled(cfg, cityTarget) {
		t.Fatal("the bd-backed city scope must still get the bounded warn-only compatibility mode")
	}
	if !workRecordEnforceEnabled(cfg, rigTarget) {
		t.Fatal("the file-backed rig lost strict shipped-close enforcement to a bd-compatibility flag it has no need of")
	}
}

// TestRunWorkRecordCloseGateKeepsFileBackedRigStrictUnderWarnOnly drives the
// same topology through the gate itself rather than the decision, so a close
// that violates the work-record contract in a way fencing has nothing to do
// with — no typed gc.work_outcome at all — is refused in the rig and warned
// through in the city.
func TestRunWorkRecordCloseGateKeepsFileBackedRigStrictUnderWarnOnly(t *testing.T) {
	cityDir, rigDir := mixedProviderCity(t)
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}
	unstamped := beads.Bead{ID: "wr-unstamped", Type: "task", Status: "open"}
	preFetched := map[string]beads.Bead{unstamped.ID: unstamped}

	var cityErr strings.Builder
	if runWorkRecordCloseGate([]string{"close", unstamped.ID}, cityDir, cityDir, cfg,
		panicOnGetStore{}, preFetched, workRecordCloseTargetForScope(cityDir, cityDir), &cityErr) {
		t.Fatalf("bd-backed city close blocked while warn-only is on; stderr=%q", cityErr.String())
	}
	if !strings.Contains(cityErr.String(), "work-record gate (warn-only)") {
		t.Fatalf("city stderr = %q, want the warn-only diagnosis", cityErr.String())
	}

	var rigErr strings.Builder
	if !runWorkRecordCloseGate([]string{"close", unstamped.ID}, rigDir, cityDir, cfg,
		panicOnGetStore{}, preFetched, workRecordCloseTargetForScope(rigDir, cityDir), &rigErr) {
		t.Fatalf("file-backed rig close proceeded under a bd-only compatibility mode; stderr=%q", rigErr.String())
	}
	if !strings.Contains(rigErr.String(), "work-record gate (enforced)") {
		t.Fatalf("rig stderr = %q, want the enforced diagnosis", rigErr.String())
	}
}

// TestWorkRecordCloseTargetEnforcesEverywhereWithoutTheFlag pins the other half:
// with the setting absent the mixed topology is uniformly strict, so the scoping
// added here cannot be read as a new way to relax anything.
func TestWorkRecordCloseTargetEnforcesEverywhereWithoutTheFlag(t *testing.T) {
	cityDir, rigDir := mixedProviderCity(t)
	warnOnly := false
	for name, cfg := range map[string]*config.City{
		"unset": {},
		"false": {Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}},
	} {
		t.Run(name, func(t *testing.T) {
			for scopeName, scope := range map[string]string{"city": cityDir, "rig": rigDir} {
				if !workRecordEnforceEnabled(cfg, workRecordCloseTargetForScope(scope, cityDir)) {
					t.Fatalf("%s scope is not enforced with shipped_close_warn_only %s", scopeName, name)
				}
			}
		})
	}
}

// TestResolvedWorkCloseTelemetryUnchangedForTheBDScope proves the warn-only
// route that legitimately keeps warning still emits, and still fails closed
// when the emission cannot be read back — the target scoping narrows WHICH
// closes warn, not what warning costs.
func TestResolvedWorkCloseTelemetryUnchangedForTheBDScope(t *testing.T) {
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}
	current := beads.Bead{ID: "wr-unstamped", Type: "task", Status: "open"}
	prospective := current
	prospective.Status = "closed"

	journal := events.NewFake()
	var stderr strings.Builder
	if evaluateResolvedWorkCloseWithProvider(current, prospective, t.TempDir(), "city:mixed", cfg, bdCloseTarget, journal, &stderr) {
		t.Fatalf("bd-target warn-only close blocked after confirmed telemetry; stderr=%q", stderr.String())
	}
	got, err := journal.List(events.Filter{Type: "work.close.warn_only.used", Subject: current.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("warn-only telemetry events = %d, want exactly one", len(got))
	}

	var nativeErr strings.Builder
	if !evaluateResolvedWorkCloseWithProvider(current, prospective, t.TempDir(), "city:mixed", cfg, workclose.CloseTarget{}, events.NewFake(), &nativeErr) {
		t.Fatalf("native-target close was relaxed by the bd compatibility mode; stderr=%q", nativeErr.String())
	}
}

// TestClassBindingCloseTargetIsNativeWhenTheBindingIs pins the by-id class door
// arm: its writes are in-process fenced writes against the binding store, so
// the target follows that store rather than the city's provider.
func TestClassBindingCloseTargetIsNativeWhenTheBindingIs(t *testing.T) {
	if got := workRecordCloseTargetForStore(beads.NewMemStore()); got.BDStoreContract {
		t.Fatalf("native binding target = %+v, want a fence-capable target", got)
	}
	if got := workRecordCloseTargetForStore(nil); got.BDStoreContract {
		t.Fatalf("unresolvable store target = %+v, want the strict answer", got)
	}
	wrapped := wrapStoreWithBeadPolicies(beads.NewMemStore(), &config.City{})
	if got := workRecordCloseTargetForStore(wrapped); got.BDStoreContract {
		t.Fatalf("policy-wrapped native store target = %+v, want a fence-capable target", got)
	}
}
