package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A persistent named crew seat with no prompt_template must NOT be handed the
// ephemeral graph-worker prompt. That prompt's startup protocol is
// `gc hook --claim --drain-ack --json`, and gc hook answers
// {action: drain, reason: no_work} whenever a hook is merely EMPTY. A pool
// worker acking that is correct (nothing to do, go home); a persistent seat
// acking it agrees to exit, so the controller stamps drain_at and tears the
// runtime down seconds after it started.
//
// Diagnosed 2026-09-11 on city anthony, seat CIPcodes/codex2 (session
// az-wisp-kxio0e, state_reason=drain-ack-stop-pending). gas-tlm8.
//
// The non-v2 branch of the fallback already guards the worker prompt behind
// "is this a pool agent?"; the formula_v2 branch dropped that guard and gave
// graph-worker.md to every template-less agent.
func TestDoPrimeNamedSessionWithoutTemplateDoesNotGetDrainAckWorkerPrompt(t *testing.T) {
	dir := t.TempDir()
	if err := materializeBuiltinPrompts(dir); err != nil {
		t.Fatalf("materializeBuiltinPrompts: %v", err)
	}
	t.Setenv("GC_CITY", "")
	t.Setenv("GC_CITY_PATH", "")
	t.Setenv("GC_CITY_ROOT", "")
	t.Setenv("GC_DIR", "")
	t.Setenv("GC_RIG", "")
	t.Setenv("GC_RIG_ROOT", "")

	// A crew seat, not a pool: max_active_sessions = 1, no [agent.pool],
	// and claimed by a [[named_session]] — the shape of CIPcodes/codex2.
	tomlContent := `[workspace]
name = "test-city"

[daemon]
formula_v2 = true

[[agent]]
name = "crew"
start_command = "echo"
max_active_sessions = 1

[[named_session]]
template = "crew"
mode = "always"
`
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(tomlContent+builtinImportsTOML("core")), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBuiltinImportsLock(t, dir, "core")

	t.Setenv("GC_CITY_PATH", dir)

	var stdout, stderr bytes.Buffer
	if code := doPrime([]string{"crew"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doPrime = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()

	if strings.Contains(out, "--drain-ack") {
		t.Fatalf("persistent crew seat was primed with a drain-acking worker prompt;\n"+
			"an empty hook will make it acknowledge drain and exit:\n%s", out)
	}
	if strings.Contains(out, "# Graph Worker") {
		t.Fatalf("persistent crew seat got the ephemeral Graph Worker prompt:\n%s", out)
	}
}

// writeCrewCityTOML writes a city whose single agent is a persistent crew seat
// claimed by a [[named_session]]. extra is appended to the [[agent]] block.
func writeCrewCityTOML(t *testing.T, dir, extra string) {
	t.Helper()
	if err := materializeBuiltinPrompts(dir); err != nil {
		t.Fatalf("materializeBuiltinPrompts: %v", err)
	}
	tomlContent := `[workspace]
name = "test-city"

[daemon]
formula_v2 = true

[[agent]]
name = "crew"
start_command = "echo"
max_active_sessions = 1
` + extra + `
[[named_session]]
template = "crew"
mode = "always"
`
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), []byte(tomlContent+builtinImportsTOML("core")), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBuiltinImportsLock(t, dir, "core")
}

// enterCrewCity points gc prime at dir without os.Chdir. The chdir dance in
// neighboring prime tests is process-global state, and the repository's
// resource census tracks cwd use precisely to keep it from spreading;
// GC_CITY_PATH is sufficient to resolve the city here.
func enterCrewCity(t *testing.T, dir string) {
	t.Helper()
	for _, k := range []string{"GC_CITY", "GC_CITY_ROOT", "GC_DIR", "GC_RIG", "GC_RIG_ROOT"} {
		t.Setenv(k, "")
	}
	t.Setenv("GC_CITY_PATH", dir)
}

// --strict exists to surface debugging mistakes rather than silently falling
// back. A [[named_session]] with no prompt_template is exactly that mistake:
// the seat is persistent by declaration but inherits a generic run-once
// prompt, which is what let CIPcodes/codex2 ship for days without one. gas-tlm8.
func TestDoPrimeStrictFailsForNamedSessionWithoutPromptTemplate(t *testing.T) {
	dir := t.TempDir()
	writeCrewCityTOML(t, dir, "")
	enterCrewCity(t, dir)

	var stdout, stderr bytes.Buffer
	code := doPrimeWithMode([]string{"crew"}, &stdout, &stderr, false, true)
	if code == 0 {
		t.Fatalf("doPrime --strict = 0, want non-zero for a named session with no prompt_template;\nstdout:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "prompt_template") {
		t.Fatalf("strict error should name the missing prompt_template; got: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "crew") {
		t.Fatalf("strict error should name the offending session; got: %s", stderr.String())
	}
}

// The guard must not fire for a named session that HAS a prompt template —
// otherwise every correctly-configured crew seat fails --strict.
func TestDoPrimeStrictPassesForNamedSessionWithPromptTemplate(t *testing.T) {
	dir := t.TempDir()
	writeCrewCityTOML(t, dir, "prompt_template = \"agents/crew/prompt.template.md\"\n")
	if err := os.MkdirAll(filepath.Join(dir, "agents", "crew"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "crew", "prompt.template.md"),
		[]byte("# Crew\n\nYou are a persistent crew member.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	enterCrewCity(t, dir)

	var stdout, stderr bytes.Buffer
	if code := doPrimeWithMode([]string{"crew"}, &stdout, &stderr, false, true); code != 0 {
		t.Fatalf("doPrime --strict = %d, want 0 for a named session WITH a template; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "persistent crew member") {
		t.Fatalf("expected the crew template to be rendered; got:\n%s", stdout.String())
	}
}
