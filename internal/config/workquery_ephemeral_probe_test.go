package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The assigned-work probes loop over three candidate identities
// ($GC_SESSION_ID, $GC_SESSION_NAME, $GC_ALIAS). The ephemeral half of each
// iteration used to run
//
//	bd query --json 'ephemeral=true AND status=<X>' --limit=0 | jq --arg id "$id" …
//
// inside that loop. The bd query never references $id — only the downstream jq
// filter does — so it is loop-invariant and was re-exec'd once per identity,
// each time an unlimited full ephemeral scan.
//
// Measured on kit/jeeves 2026-08-05: `gc hook` spawned 33 bd subprocesses for
// 11 distinct queries, and every bd exec costs a flat ~0.13s (process startup +
// connection) regardless of query shape. The identity loop alone accounted for
// 14 of those execs. See kit-021.
//
// These tests pin the runtime property — how many times bd actually runs — not
// the generated text, so they stay valid however the dedupe is expressed.

// runShellCountingBd runs shellCmd with a fake bd on PATH that appends its
// arguments to a log file and prints an empty JSON array, then returns the log.
// It mirrors runLifecycleHookCommand's BD_LOG idiom, but returns the full
// invocation log so callers can count calls rather than assert on stdout.
func runShellCountingBd(t *testing.T, shellCmd string, env map[string]string) string {
	t.Helper()

	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "bd.log")
	bdScript := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
printf '[]'
`
	if err := os.WriteFile(filepath.Join(tmp, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Env = []string{
		"PATH=" + tmp + ":" + os.Getenv("PATH"),
		"BD_LOG=" + logPath,
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if _, err := cmd.Output(); err != nil {
		t.Fatalf("run shell with counting bd: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	return string(data)
}

func countBdCallsMatching(log, substr string) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.TrimSpace(line) != "" && strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// threeDistinctIdentities drives the probe loop through all three candidate
// identities: every one is non-empty and distinct, and the fake bd reports no
// work, so nothing short-circuits and the loop runs to completion.
func threeDistinctIdentities() map[string]string {
	return map[string]string{
		"GC_SESSION_ID":   "sess-abc123",
		"GC_SESSION_NAME": "hello-world/worker",
		"GC_ALIAS":        "hello-world--worker",
	}
}

func TestAssignedInProgressQueryRunsEphemeralProbeOnce(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the ephemeral probe filters with jq")
	}
	a := Agent{Name: "worker", Dir: "hello-world"}
	log := runShellCountingBd(t, a.EffectiveAssignedInProgressQuery(), threeDistinctIdentities())

	// The per-identity assignee lookup must still run once per identity —
	// that query genuinely varies with $id.
	if got := countBdCallsMatching(log, "--status in_progress --assignee="); got != 3 {
		t.Errorf("per-identity assignee lookups = %d, want 3 (one per candidate identity)", got)
	}
	// The ephemeral scan does not vary with $id and must not repeat.
	if got := countBdCallsMatching(log, "ephemeral=true AND status=in_progress"); got != 1 {
		t.Errorf("ephemeral in_progress scans = %d, want 1 (loop-invariant, must not re-run per identity)\nbd log:\n%s", got, log)
	}
}

func TestAssignedReadyQueryRunsEphemeralProbeOnce(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the ephemeral probe filters with jq")
	}
	a := Agent{Name: "worker", Dir: "hello-world"}
	log := runShellCountingBd(t, a.EffectiveAssignedReadyQuery(), threeDistinctIdentities())

	if got := countBdCallsMatching(log, "ready --assignee="); got != 3 {
		t.Errorf("per-identity ready lookups = %d, want 3 (one per candidate identity)", got)
	}
	if got := countBdCallsMatching(log, "ephemeral=true AND status=open"); got != 1 {
		t.Errorf("ephemeral open scans = %d, want 1 (loop-invariant, must not re-run per identity)\nbd log:\n%s", got, log)
	}
}

// The dedupe must not make the probe eager. When the first identity already has
// in-progress work the loop exits before any ephemeral fallback is needed, and
// hoisting the scan to the top of the script would add a subprocess to the
// fast path that the original never paid.
func TestAssignedInProgressQuerySkipsEphemeralProbeWhenFirstIdentityHasWork(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the ephemeral probe filters with jq")
	}
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "bd.log")
	// Report work for the assignee lookup, nothing for anything else.
	bdScript := `#!/bin/sh
printf '%s\n' "$*" >> "$BD_LOG"
case "$*" in
  *"--status in_progress --assignee=sess-abc123"*) printf '[{"id":"work-1"}]' ;;
  *) printf '[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(tmp, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	a := Agent{Name: "worker", Dir: "hello-world"}
	cmd := exec.Command("sh", "-c", a.EffectiveAssignedInProgressQuery())
	cmd.Env = []string{"PATH=" + tmp + ":" + os.Getenv("PATH"), "BD_LOG=" + logPath}
	for k, v := range threeDistinctIdentities() {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run shell with counting bd: %v", err)
	}
	if !strings.Contains(string(out), "work-1") {
		t.Fatalf("query did not return the first identity's work: %q", out)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	if got := countBdCallsMatching(string(data), "ephemeral=true AND status=in_progress"); got != 0 {
		t.Errorf("ephemeral scans = %d, want 0 (first identity had work; probe must stay lazy)\nbd log:\n%s", got, data)
	}
}

// The full work query runs the assigned tiers and then the pool-demand tier in
// one script. Both reach for the same unlimited `ephemeral=true AND status=open`
// scan: the assigned-ready probe through the __gc_eph_open memo, the
// pool-demand tier through its own re-scan in legacyEphemeralPoolDemandShell.
// That second scan is a byte-identical query costing a flat ~0.16s, and
// `gc hook` runs this whole script once per store. See kit-r0e2.
//
// The memo is READ across the $( ) subshell that wraps the pool-demand call —
// it is populated in the parent scope by the earlier assigned-ready probe. A
// write inside that subshell would not persist, so this test also pins the
// ordering that makes the reuse pay.
func TestWorkQueryRunsEphemeralOpenScanOnce(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available; the ephemeral probe filters with jq")
	}
	a := Agent{Name: "worker", Dir: "hello-world"}
	log := runShellCountingBd(t, a.EffectiveWorkQuery(), threeDistinctIdentities())

	if got := countBdCallsMatching(log, "ephemeral=true AND status=open"); got != 1 {
		t.Errorf("ephemeral open scans = %d, want 1 (the pool-demand tier must reuse the memo, not re-scan)\nbd log:\n%s", got, log)
	}
	// The pool-demand tier must still actually run — a passing count of 1 is
	// only meaningful if the tier was reached rather than skipped.
	if got := countBdCallsMatching(log, "gc.routed_to"); got == 0 {
		t.Errorf("pool-demand tier did not run (no routed_to query); the origin gate or target may be wrong\nbd log:\n%s", log)
	}
}
