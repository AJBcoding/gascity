package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// --- finding 2: bug-type beads are as worker-claimable as tasks ---

// TestIsWorkRecordGatedBeadGatesBugBeads pins finding 2 of gastownhall/gascity#5037:
// the gate keyed on Type != "task", so bug beads — claimed, worked, and closed
// by workers exactly like tasks — bypassed the work-record contract silently.
// Both silent rows in that issue's six-close table were type bug.
func TestIsWorkRecordGatedBeadGatesBugBeads(t *testing.T) {
	tests := []struct {
		name string
		bead beads.Bead
		want bool
	}{
		{name: "bug bead is gated", bead: beads.Bead{Type: "bug"}, want: true},
		{name: "task bead stays gated", bead: beads.Bead{Type: "task"}, want: true},
		{name: "empty type stays gated", bead: beads.Bead{}, want: true},
		{
			name: "control-kind bug bead stays exempt",
			bead: beads.Bead{Type: "bug", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindRun}},
			want: false,
		},
		{name: "convoy bead stays exempt", bead: beads.Bead{Type: "convoy"}, want: false},
		{name: "message bead stays exempt", bead: beads.Bead{Type: "message"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWorkRecordGatedBead(tc.bead); got != tc.want {
				t.Fatalf("isWorkRecordGatedBead = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEvaluateWorkRecordCloseGateBlocksShippedBugWithoutCommit proves the
// widened gate actually arms for the new bead class end to end, not just in
// the predicate: a bug bead closed as shipped with no commit must be blocked
// under enforcement, exactly as the same task bead already was.
func TestEvaluateWorkRecordCloseGateBlocksShippedBugWithoutCommit(t *testing.T) {
	preFetched := map[string]beads.Bead{
		"wr-bug-shipped": {ID: "wr-bug-shipped", Type: "bug", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}},
	}
	var stderr strings.Builder
	block := evaluateWorkRecordCloseGate([]string{"close", "wr-bug-shipped"}, panicOnGetStore{}, preFetched, t.TempDir(), true, &stderr)
	if !block {
		t.Fatalf("expected block=true for a shipped bug bead with no commit, got false; stderr=%s", stderr.String())
	}
}

// --- finding 3: reachability must resolve against a stable repo root ---

// TestWorkRecordRepoDirPrefersStableScopeRoot pins finding 3: gc.work_dir is a
// polecat worktree that is deleted at cleanup, so preferring it made every
// post-cleanup reachability probe fail closed on a missing directory — a false
// negative that blocks a legitimately delivered close. The rig root is stable,
// and worktrees share one object store, so it answers the same question.
func TestWorkRecordRepoDirPrefersStableScopeRoot(t *testing.T) {
	bead := beads.Bead{Metadata: map[string]string{beadmeta.WorkDirMetadataKey: "/tmp/pruned-polecat-worktree"}}
	if got := workRecordRepoDir(bead, "/rigs/gascity"); got != "/rigs/gascity" {
		t.Errorf("workRecordRepoDir = %q, want the stable scope root /rigs/gascity", got)
	}
}

// TestWorkRecordRepoDirFallsBackToWorkDir keeps the worktree usable when no
// scope root is known — the gate should still answer rather than fail closed.
func TestWorkRecordRepoDirFallsBackToWorkDir(t *testing.T) {
	bead := beads.Bead{Metadata: map[string]string{beadmeta.WorkDirMetadataKey: "/worktrees/polecat-1"}}
	if got := workRecordRepoDir(bead, "  "); got != "/worktrees/polecat-1" {
		t.Errorf("workRecordRepoDir = %q, want the work_dir fallback /worktrees/polecat-1", got)
	}
}

// TestWorkRecordRepoDirEmptyWhenNeitherKnown pins the fail-closed direction:
// with no repo to consult, gitCommitReachableOnBranch reads "" as not
// reachable rather than probing the process's cwd.
func TestWorkRecordRepoDirEmptyWhenNeitherKnown(t *testing.T) {
	if got := workRecordRepoDir(beads.Bead{}, ""); got != "" {
		t.Errorf("workRecordRepoDir = %q, want empty", got)
	}
}

// TestEvaluateWorkRecordCloseGateAllowsShippedCloseAfterWorktreePruned is the
// regression the gas-dr5 review confirmed with a concrete deputy-close
// scenario: the work landed, the commit is reachable on its branch in the rig
// repo, but the polecat worktree named by gc.work_dir has been reaped. Before
// the fix the git probe ran in a directory that no longer exists, errored, and
// the fail-closed reading blocked a delivered close.
func TestEvaluateWorkRecordCloseGateAllowsShippedCloseAfterWorktreePruned(t *testing.T) {
	repo := t.TempDir()
	mustGit(t, repo, "init", "-b", "work-branch")
	mustGit(t, repo, "config", "user.email", "test@test.com")
	mustGit(t, repo, "config", "user.name", "Test")
	mustGit(t, repo, "commit", "--allow-empty", "-m", "delivered work")
	// Read the SHA from the loose ref instead of shelling out for rev-parse:
	// the repository resource census budgets subprocess call sites, and a
	// fresh single-commit repo has not packed its refs.
	shaBytes, err := os.ReadFile(filepath.Join(repo, ".git", "refs", "heads", "work-branch"))
	if err != nil {
		t.Fatalf("reading work-branch ref: %v", err)
	}
	sha := strings.TrimSpace(string(shaBytes))

	prunedWorktree := filepath.Join(t.TempDir(), "reaped-polecat-worktree")
	preFetched := map[string]beads.Bead{
		"wr-delivered": {
			ID: "wr-delivered", Type: "task", Status: "in_progress",
			Metadata: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  sha,
				beadmeta.WorkBranchMetadataKey:  "work-branch",
				beadmeta.WorkDirMetadataKey:     prunedWorktree,
			},
		},
	}

	var stderr strings.Builder
	block := evaluateWorkRecordCloseGate([]string{"close", "wr-delivered"}, panicOnGetStore{}, preFetched, repo, true, &stderr)
	if block {
		t.Fatalf("delivered close blocked after its worktree was pruned; the probe must resolve against the stable rig root. stderr=%s", stderr.String())
	}
}
