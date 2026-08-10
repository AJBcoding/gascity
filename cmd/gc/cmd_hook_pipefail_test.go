package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellWorkQueryFailsWhenPipelineFirstStageDies guards the fail-open door
// gas-znj closed. A shell pipeline exits with the status of its LAST stage, so a
// piped work_query — the natural shape, e.g. `bd ready --json | jq ...` — used to
// mask a dying bd completely: bd exits 137, jq exits 0 with empty stdout,
// cmd.Output() returns no error, and the hook read the empty candidate list as
// genuine no-work. The work query must run under pipefail so a failing stage
// propagates and the caller can fail loudly instead of standing the agent down
// mid-incident.
func TestShellWorkQueryFailsWhenPipelineFirstStageDies(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantErr string
	}{
		{
			name:    "first stage exits non-zero",
			query:   "false | cat",
			wantErr: "exit status 1",
		},
		{
			// The gas-zaa shape: the producer is killed (rc=137) mid-install
			// while the downstream filter still exits 0 with empty stdout.
			name:    "first stage is killed while the filter succeeds",
			query:   `sh -c 'exit 137' | cat`,
			wantErr: "exit status 137",
		},
		{
			name:    "failing stage in the middle of a longer pipeline",
			query:   "printf '[]' | false | cat",
			wantErr: "exit status 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := shellWorkQueryWithEnv(tt.query, t.TempDir(), nil)
			if err == nil {
				t.Fatalf("shellWorkQueryWithEnv(%q) err = nil (out %q), want the failing stage to surface", tt.query, out)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want it to report %q so the operator sees which stage died", err, tt.wantErr)
			}
		})
	}
}

// TestShellWorkQuerySucceedsForHealthyPipeline pins the other half of the
// contract: pipefail must not make an ordinary successful piped work_query fail.
func TestShellWorkQuerySucceedsForHealthyPipeline(t *testing.T) {
	out, err := shellWorkQueryWithEnv(`printf '[]\n' | cat`, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("shellWorkQueryWithEnv() err = %v, want nil for a healthy pipeline", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("out = %q, want %q", out, "[]")
	}
}

// TestShellWorkQueryRunsCommandStartingWithComment guards the pipefail prelude's
// separator: a work_query whose first line is a comment (or any command relying
// on line structure) must still run, so the prelude cannot be joined to it with
// a bare `;`.
func TestShellWorkQueryRunsCommandStartingWithComment(t *testing.T) {
	out, err := shellWorkQueryWithEnv("# leading comment\nprintf '[]'", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("shellWorkQueryWithEnv() err = %v, want nil", err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("out = %q, want %q", out, "[]")
	}
}

// TestResolveWorkQueryShellSkipsShellsWithoutPipefail pins the portability
// contract: the resolver probes candidates in order and picks the first that
// actually accepts `set -o pipefail`, so a host whose /bin/sh is plain dash gets
// bash/zsh instead of a prelude that would kill every work query — `set` is a
// special builtin, so a shell that rejects pipefail dies on it rather than
// carrying on. `false` stands in for such a shell: it exits non-zero for any
// argv, exactly as the probe must interpret as "no pipefail here".
func TestResolveWorkQueryShellSkipsShellsWithoutPipefail(t *testing.T) {
	got := resolveWorkQueryShellFrom([]string{"false", "gc-absent-shell", "sh"})
	if filepath.Base(got) != "sh" {
		t.Errorf("resolveWorkQueryShellFrom() = %q, want the first pipefail-capable candidate (sh)", got)
	}
}

// TestResolveWorkQueryShellReportsNoPipefailShell covers the last-resort branch:
// when no candidate supports pipefail the resolver reports that plainly instead
// of returning a shell that would abort on the prelude.
func TestResolveWorkQueryShellReportsNoPipefailShell(t *testing.T) {
	if got := resolveWorkQueryShellFrom([]string{"false", "gc-absent-shell"}); got != "" {
		t.Errorf("resolveWorkQueryShellFrom() = %q, want \"\" (no pipefail-capable shell)", got)
	}
}

// TestWorkQueryShellCommandFallsBackWithoutPipefail pins the shape of the
// last-resort invocation: with no pipefail-capable shell the query still runs
// under sh, unprefixed, rather than being wrapped in a prelude that shell would
// reject.
func TestWorkQueryShellCommandFallsBackWithoutPipefail(t *testing.T) {
	name, args := workQueryShellCommandFor("", "bd ready --json | jq -c .")
	if name != "sh" {
		t.Errorf("shell = %q, want %q", name, "sh")
	}
	want := []string{"-c", "bd ready --json | jq -c ."}
	if len(args) != len(want) || args[0] != want[0] || args[1] != want[1] {
		t.Errorf("args = %q, want %q", args, want)
	}
}

// TestHookClaimIsLoudWhenPipedWorkQueryFirstStageDies is the end-to-end
// acceptance for gas-znj, driven through the production runner binding: a piped
// work_query whose first stage dies must terminate `gc hook --claim` with a
// reported error, never with the action=drain/no_work record that tells a live
// agent to stand down.
func TestHookClaimIsLoudWhenPipedWorkQueryFirstStageDies(t *testing.T) {
	clearGCEnv(t)

	dir := t.TempDir()
	query := `sh -c 'exit 137' | cat`
	var stdout, stderr bytes.Buffer
	emitted := 0
	code := claimHookWork(
		// cityPath is new in upstream's signature (class-route resolution).
		// dir is a bare t.TempDir() with no city config, so the route opens
		// empty and the claim proceeds unrouted — which is what this test,
		// about a dying piped work_query, means to exercise.
		dir,
		query,
		dir,
		nil,
		[]hookStore{{dir: dir}},
		hookClaimOptions{Assignee: "polecat-pipefail", JSON: true},
		func(string, error) { emitted++ },
		&stdout,
		&stderr,
	)

	if code != 1 {
		t.Errorf("claimHookWork() = %d, want 1 (terminal failure)", code)
	}
	if strings.Contains(stdout.String(), `"action":"drain"`) || strings.Contains(stdout.String(), hookClaimReasonNoWork) {
		t.Errorf("stdout = %q, want no drain/no_work record for a dying work query", stdout.String())
	}
	if !strings.Contains(stderr.String(), "137") {
		t.Errorf("stderr = %q, want the failing stage's status reported", stderr.String())
	}
	if emitted != 1 {
		t.Errorf("emitFailure called %d times, want 1 (work-query failure surfaced on the event bus)", emitted)
	}
}
