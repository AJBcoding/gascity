package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

func TestDoctorHelpNamesRawCloseQualificationGate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newDoctorCmd(&stdout, &stderr)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor --help: %v; stderr=%s", err, stderr.String())
	}
	help := strings.ToLower(stdout.String())
	for _, want := range []string{"direct_raw_bd_writes", "production", "gc bd"} {
		if !strings.Contains(help, want) {
			t.Errorf("doctor --help missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestShippedCloseBoundaryAllowsManagedBDProvider(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "bd")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "bd"}}

	check := newShippedCloseBoundaryCheck(cityPath, cfg)
	res := check.Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusOK {
		t.Fatalf("result = %+v, want OK for bd used only behind the managed front door", res)
	}
	joined := strings.Join(append([]string{res.Message}, res.Details...), " ")
	for _, want := range []string{"managed mutation surfaces", "out-of-band", "unsupported"} {
		if !strings.Contains(joined, want) {
			t.Errorf("result text %q missing %q", joined, want)
		}
	}
}

func TestShippedCloseBoundaryBlocksExplicitRawBDWriteEntrypoint(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "bd")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "bd", DirectRawBDWrites: true}}

	check := newShippedCloseBoundaryCheck(cityPath, cfg)
	res := check.Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusError || res.Severity != doctor.SeverityBlocking {
		t.Fatalf("result = status %v severity %v, want error/blocking: %+v", res.Status, res.Severity, res)
	}
	joined := strings.Join(append([]string{res.Message, res.FixHint}, res.Details...), " ")
	for _, want := range []string{"direct_raw_bd_writes", "raw `bd`", "unsupported", "city"} {
		if !strings.Contains(joined, want) {
			t.Errorf("result text %q missing %q", joined, want)
		}
	}

	d := &doctor.Doctor{}
	d.Register(check)
	report := d.RunCollect(&doctor.CheckContext{CityPath: cityPath}, false)
	if report.BlockingFailed != 1 {
		t.Fatalf("BlockingFailed = %d, want 1", report.BlockingFailed)
	}
}

func TestShippedCloseBoundaryCannotBeMaskedByAmbientFileOverride(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "bd")
	t.Setenv("GC_BEADS", "file")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "bd", DirectRawBDWrites: true}}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusError || res.Severity != doctor.SeverityBlocking {
		t.Fatalf("ambient GC_BEADS=file masked configured raw bd entrypoint: %+v", res)
	}
}

func TestShippedCloseBoundaryCannotBeMaskedByFileMarkerAndAmbientFileOverride(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "bd")
	if err := os.MkdirAll(filepath.Join(cityPath, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityPath, ".gc", "beads.json"), []byte("{\"seq\":0,\"beads\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_BEADS", "file")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "bd", DirectRawBDWrites: true}}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusError || res.Severity != doctor.SeverityBlocking {
		t.Fatalf("file marker plus ambient GC_BEADS=file masked configured raw bd entrypoint: %+v", res)
	}
}

func TestShippedCloseBoundaryDoesNotApplyCityBDDefaultToExplicitFileRig(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "file-rig")
	if err := os.MkdirAll(filepath.Join(rigPath, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, ".gc", "beads.json"), []byte("{\"seq\":0,\"beads\":[]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeShippedCloseBoundaryCityConfig(t, cityPath, "bd")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "bd", DirectRawBDWrites: true}}

	if newShippedCloseBoundaryCheck(cityPath, cfg).scopeHasConfiguredOrEffectiveBDPath(rigPath) {
		t.Fatal("explicitly file-backed rig inherited city bd default")
	}
}

func TestShippedCloseBoundaryIncludesAmbientBDOverride(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "file")
	t.Setenv("GC_BEADS", "bd")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "file", DirectRawBDWrites: true}}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusError || res.Severity != doctor.SeverityBlocking {
		t.Fatalf("ambient GC_BEADS=bd raw entrypoint was not qualified: %+v", res)
	}
}

func TestShippedCloseBoundaryWarnsWhenCompatibilityModeDisablesEnforcement(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "bd")
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "bd", ShippedCloseWarnOnly: &warnOnly}}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusWarning || res.Severity != doctor.SeverityAdvisory {
		t.Fatalf("result = status %v severity %v, want warning/advisory: %+v", res.Status, res.Severity, res)
	}
	if !strings.Contains(res.Message, "not production-enforced") {
		t.Fatalf("Message = %q, want production-enforcement qualification", res.Message)
	}
}

func TestShippedCloseBoundaryPassesWithoutRawBDBackedScope(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "file")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "file"}}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusOK {
		t.Fatalf("result = %+v, want OK", res)
	}
	if !strings.Contains(res.Message, "supported managed mutation surfaces") {
		t.Fatalf("Message = %q, want supported-surface qualification", res.Message)
	}
}

func TestShippedCloseBoundaryFindsMixedBDRig(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "legacy")
	if err := os.MkdirAll(filepath.Join(rigPath, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigPath, ".beads", "metadata.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeShippedCloseBoundaryCityConfig(t, cityPath, "file")
	cfg := &config.City{
		Beads: config.BeadsConfig{Provider: "file", DirectRawBDWrites: true},
		Rigs:  []config.Rig{{Name: "legacy", Path: rigPath}},
	}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusError {
		t.Fatalf("result = %+v, want Error", res)
	}
	if got := strings.Join(res.Details, " "); !strings.Contains(got, "rig:legacy") {
		t.Fatalf("Details = %q, want mixed-provider rig scope", got)
	}
}

func TestShippedCloseBoundaryWarnsWhenRawBDDeclarationHasNoBDBackedScope(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "file")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "file", DirectRawBDWrites: true}}

	res := newShippedCloseBoundaryCheck(cityPath, cfg).Run(&doctor.CheckContext{CityPath: cityPath})

	if res.Status != doctor.StatusWarning || res.Severity != doctor.SeverityAdvisory {
		t.Fatalf("result = %+v, want advisory warning for non-applicable declaration", res)
	}
	if !strings.Contains(res.Message, "no bd-backed scope") {
		t.Fatalf("Message = %q, want non-applicable scope explanation", res.Message)
	}
}

func TestShippedCloseBoundaryMetadataAndRegistration(t *testing.T) {
	clearGCEnv(t)
	cityPath := t.TempDir()
	writeShippedCloseBoundaryCityConfig(t, cityPath, "file")
	cfg := &config.City{Beads: config.BeadsConfig{Provider: "file"}}
	check := newShippedCloseBoundaryCheck(cityPath, cfg)
	if check.Name() != "shipped-close-boundary" {
		t.Fatalf("Name() = %q, want shipped-close-boundary", check.Name())
	}
	if check.CanFix() || check.WarmupEligible() {
		t.Fatal("boundary check must be read-only and excluded from fail-open startup warmup")
	}

	for _, candidate := range buildDoctorChecks(cityPath, cfg, nil, buildDoctorChecksOpts{
		SkipCityDoltCheck:    true,
		SkipManagedDoltCheck: true,
		SkipRigDoltChecks:    true,
	}) {
		if candidate.Name() == check.Name() {
			return
		}
	}
	t.Fatal("shipped-close-boundary check not registered")
}

func writeShippedCloseBoundaryCityConfig(t *testing.T, cityPath, provider string) {
	t.Helper()
	contents := "[workspace]\nname = \"test-city\"\n\n[beads]\nprovider = \"" + provider + "\"\n"
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
