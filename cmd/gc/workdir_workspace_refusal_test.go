package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// packPolecatWorkDir is the path poolWorkDirCity's pack-shaped agent resolves
// to under the city's worktrees root.
func packPolecatWorkDir(cityDir string) string {
	return filepath.Join(cityDir, ".gc", "worktrees", "myrig", "polecats", "polecat")
}

func stampWorkDir(t *testing.T, cityDir string, cfg *config.City, stderr io.Writer) string {
	t.Helper()
	bp := newAgentBuildParams("test-city", cityDir, cfg, runtime.NewFake(), time.Now().UTC(), nil, stderr)
	return poolTriggerWorkDir(bp, &cfg.Agents[0], "myrig/polecat", SessionRequest{WorkBeadID: "gas-1"})
}

// TestPoolTriggerWorkDirRefusesHuskUnderWorktreesRoot guards gas-tvn5. The
// stamp is what the ledger and the next session read (gas-u1f6); a value
// naming a plain directory under the worktrees root hands that session a
// workspace with no git, no code, and no .beads (py-3xe5). Binding nothing is
// the safe degradation.
func TestPoolTriggerWorkDirRefusesHuskUnderWorktreesRoot(t *testing.T) {
	cityDir, cfg := poolWorkDirCity(t)
	husk := packPolecatWorkDir(cityDir)
	if err := os.MkdirAll(husk, 0o755); err != nil {
		t.Fatalf("MkdirAll(husk): %v", err)
	}

	var stderr bytes.Buffer
	got := stampWorkDir(t, cityDir, cfg, &stderr)

	if got != "" {
		t.Fatalf("poolTriggerWorkDir = %q, want \"\" (a plain dir under the worktrees root is not a workspace)", got)
	}
	if !strings.Contains(stderr.String(), husk) {
		t.Errorf("refusal was silent: stderr = %q, want it to name %q", stderr.String(), husk)
	}
}

// A real linked worktree — .git is a pointer file — is the shape the lane
// expects and must stamp unchanged.
func TestPoolTriggerWorkDirStampsLinkedWorktree(t *testing.T) {
	cityDir, cfg := poolWorkDirCity(t)
	work := packPolecatWorkDir(cityDir)
	gitdir := filepath.Join(cityDir, "myrig", ".git", "worktrees", "polecat")
	for _, dir := range []string{work, gitdir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(.git): %v", err)
	}

	if got := stampWorkDir(t, cityDir, cfg, io.Discard); got != work {
		t.Fatalf("poolTriggerWorkDir = %q, want %q (linked worktree)", got, work)
	}
}

// The worktree is created by the pack's pre_start hook ("git worktree add"),
// which runs after work_dir resolution. A path that does not exist yet must
// still stamp, or a polecat's first session would bind no work_dir at all.
func TestPoolTriggerWorkDirStampsNotYetCreatedWorktree(t *testing.T) {
	cityDir, cfg := poolWorkDirCity(t)

	want := packPolecatWorkDir(cityDir)
	if got := stampWorkDir(t, cityDir, cfg, io.Discard); got != want {
		t.Fatalf("poolTriggerWorkDir = %q, want %q (pre_start creates the worktree)", got, want)
	}
}

// TestPoolTriggerWorkDirStampsNonWorktreeShapes is the regression gas-tvn5 was
// split out to protect. The dispatch originally asked for a blanket ".git must
// be a FILE" rule; measured against the live city that refuses the mayor and
// every witness (agent dirs, no .git at all) and kit/anthony (an external plain
// repo, .git is a directory). All three are legitimate and must keep stamping.
func TestPoolTriggerWorkDirStampsNonWorktreeShapes(t *testing.T) {
	external := filepath.Join(t.TempDir(), "kit")
	if err := os.MkdirAll(filepath.Join(external, ".git"), 0o755); err != nil {
		t.Fatalf("MkdirAll(external repo): %v", err)
	}

	for _, tc := range []struct {
		name    string
		workDir string
		want    func(cityDir string) string
		mkdir   bool
	}{
		{
			name:    "mayor agent dir (no .git)",
			workDir: ".gc/agents/mayor",
			want:    func(c string) string { return filepath.Join(c, ".gc", "agents", "mayor") },
			mkdir:   true,
		},
		{
			name:    "witness agent dir (no .git)",
			workDir: ".gc/agents/{{.Rig}}/witness",
			want:    func(c string) string { return filepath.Join(c, ".gc", "agents", "myrig", "witness") },
			mkdir:   true,
		},
		{
			name:    "external plain repo (.git dir)",
			workDir: external,
			want:    func(string) string { return external },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cityDir, cfg := poolWorkDirCity(t)
			cfg.Agents[0].WorkDir = tc.workDir

			want := tc.want(cityDir)
			if tc.mkdir {
				if err := os.MkdirAll(want, 0o755); err != nil {
					t.Fatalf("MkdirAll(%s): %v", want, err)
				}
			}

			var stderr bytes.Buffer
			got := stampWorkDir(t, cityDir, cfg, &stderr)
			if got != want {
				t.Fatalf("poolTriggerWorkDir = %q, want %q (%s must keep stamping); stderr=%q",
					got, want, tc.name, stderr.String())
			}
		})
	}
}
