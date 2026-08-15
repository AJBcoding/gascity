package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/pathutil"
)

// reapTestRigName is the single rig name used across the reaper tests. It is a
// const rather than a parameter so the helpers do not carry an argument that
// every caller passes identically.
const reapTestRigName = "mrig"

// initReapRig builds a rig git repository with an initial commit pushed to a
// bare origin, and returns the city path (whose .gc/worktrees/ tree will hold
// per-bead worktrees) and the rig repo root. Because the seed commit is on
// origin/main, worktrees branched from it have no unpushed commits and so pass
// the reaper's git-safety gate absent any liveness signal.
func initReapRig(t *testing.T) (cityPath, rigRoot string) {
	t.Helper()
	base := t.TempDir()
	cityPath = filepath.Join(base, "city")
	rigRoot = filepath.Join(base, "rig")
	remote := filepath.Join(base, "remote.git")

	mustGit(t, "", "init", "--bare", remote)
	mustGit(t, "", "-c", "init.defaultBranch=main", "init", rigRoot)
	if err := os.WriteFile(filepath.Join(rigRoot, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	mustGit(t, rigRoot, "add", ".")
	mustGit(t, rigRoot, "-c", "commit.gpgsign=false", "commit", "-m", "init")
	mustGit(t, rigRoot, "remote", "add", "origin", remote)
	mustGit(t, rigRoot, "push", "-u", "origin", "main")
	return cityPath, rigRoot
}

// addClosedWorktree adds a per-bead worktree nested under an agent-home
// directory (depth-2: .gc/worktrees/<rig>/<agentHome>/<beadID>), matching the
// real do-work layout the reaper must now discover. It branches from HEAD (on
// origin/main), so the tree is clean with no unpushed commits. The worktree's
// creation time is backdated well past the freshness-quarantine default (FR-5)
// so existing callers exercise the liveness/git-safety/borrow-veto gates
// without incidentally tripping quarantine; tests of the quarantine gate
// itself use addClosedWorktreeWithAge directly.
func addClosedWorktree(t *testing.T, rigRoot, cityPath, agentHome, beadID string) string {
	t.Helper()
	return addClosedWorktreeWithAge(t, rigRoot, cityPath, agentHome, beadID, 24*time.Hour)
}

// addClosedWorktreeWithAge is addClosedWorktree with an explicit backdated
// age for the worktree's on-disk creation signal, letting freshness-gate
// tests place a worktree on either side of the quarantine boundary. age == 0
// leaves the real (just-created) mtime in place.
func addClosedWorktreeWithAge(t *testing.T, rigRoot, cityPath, agentHome, beadID string, age time.Duration) string {
	t.Helper()
	wtPath := filepath.Join(cityPath, ".gc", "worktrees", reapTestRigName, agentHome, beadID)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatalf("mkdir worktree parent: %v", err)
	}
	mustGit(t, rigRoot, "worktree", "add", "-b", "wt-"+beadID, wtPath)
	if age > 0 {
		backdateWorktreeGitFile(t, wtPath, age)
	}
	return wtPath
}

// addNestedRegisteredWorktree adds a registered git worktree physically inside
// parentPath/worktrees/<beadID>. This mirrors the production hazard: the inner
// checkout is a real worktree, but the outer checkout can hide it from status
// when "worktrees/" is excluded.
func addNestedRegisteredWorktree(t *testing.T, rigRoot, parentPath, beadID string) string {
	t.Helper()
	wtPath := filepath.Join(parentPath, "worktrees", beadID)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatalf("mkdir nested worktree parent: %v", err)
	}
	mustGit(t, rigRoot, "worktree", "add", "-b", "wt-"+beadID, wtPath)
	backdateWorktreeGitFile(t, wtPath, 24*time.Hour)
	return wtPath
}

// backdateWorktreeGitFile sets the mtime of a worktree's .git pointer file
// (written once by `git worktree add` and not rewritten during normal use)
// back by age, so the reaper's age-computation helper — which uses that
// file's mtime as a creation-time proxy — sees a worktree older than age.
func backdateWorktreeGitFile(t *testing.T, worktreePath string, age time.Duration) {
	t.Helper()
	gitFile := filepath.Join(worktreePath, ".git")
	backdated := time.Now().Add(-age)
	if err := os.Chtimes(gitFile, backdated, backdated); err != nil {
		t.Fatalf("backdate %s: %v", gitFile, err)
	}
}

func mustGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"-c", "core.hooksPath="}, args...)
	cmd := exec.Command("git", fullArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	for _, env := range os.Environ() {
		if key, _, ok := strings.Cut(env, "="); ok && testGitEnvBlacklist[key] {
			continue
		}
		cmd.Env = append(cmd.Env, env)
	}
	cmd.Env = append(cmd.Env,
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), string(out), err)
	}
	return string(out)
}

func reapTestConfig(rigRoot string) *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "test", Prefix: "ga"},
		Rigs:      []config.Rig{{Name: reapTestRigName, Path: rigRoot}},
	}
}

// injectLiveness overrides the reaper's process-table scan for the duration of
// the test so liveness is deterministic, and restores it on cleanup.
func injectLiveness(t *testing.T, state liveWorktreeState) {
	t.Helper()
	prev := collectLiveWorktreeStateFn
	collectLiveWorktreeStateFn = func() liveWorktreeState { return state }
	t.Cleanup(func() { collectLiveWorktreeStateFn = prev })
}

// TestReapClosedBeadWorktrees_ReapsIdleNestedWorktree proves the depth fix: a
// per-bead worktree nested two levels under .gc/worktrees/<rig>/ (which the old
// single-level os.ReadDir scan never saw) is discovered via porcelain and
// reaped when its bead is closed, the tree is clean, and nothing is live.
func TestReapClosedBeadWorktrees_ReapsIdleNestedWorktree(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	wt := addClosedWorktree(t, rigRoot, cityPath, "builder", "ga-idle01")
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-idle01", Status: "closed"}}, nil)
	cfg := reapTestConfig(rigRoot)
	injectLiveness(t, liveWorktreeState{scanned: true}) // scanned, but no live cwds

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, nil, false, events.Discard, nil, &stderr)

	if len(report.Reaped) != 1 || report.Reaped[0].BeadID != "ga-idle01" {
		t.Fatalf("Reaped = %+v, want exactly ga-idle01\nstderr:\n%s", report.Reaped, stderr.String())
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree %s still present after reap (stat err=%v)", wt, err)
	}
}

// TestReapClosedBeadWorktrees_ProtectsLiveWorktree is the canonical failing
// test for gastownhall/gascity#4492: a closed-bead worktree that is git-clean
// and fully pushed — and therefore reapable by every pre-existing gate — is NOT
// removed when a live process is working inside it. This is the exact shape of
// the would-reap-19-live incident.
func TestReapClosedBeadWorktrees_ProtectsLiveWorktree(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	// Nest under a pooled agent home (builder-2), mirroring the real fleet
	// layout where the would-reap-19-live incident occurred.
	wt := addClosedWorktree(t, rigRoot, cityPath, "builder-2", "ga-live01")
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-live01", Status: "closed"}}, nil)
	cfg := reapTestConfig(rigRoot)
	// A live process cwd sits inside the worktree (e.g. a nested build/test dir
	// would too — here the tree root itself).
	injectLiveness(t, liveWorktreeState{scanned: true, cwds: []string{pathutil.NormalizePathForCompare(wt)}})

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, nil, false, events.Discard, nil, &stderr)

	if len(report.Reaped) != 0 {
		t.Fatalf("Reaped = %+v, want 0 for a live worktree\nstderr:\n%s", report.Reaped, stderr.String())
	}
	if len(report.Protected) != 1 || !strings.Contains(report.Protected[0].Reason, "live") {
		t.Fatalf("Protected = %+v, want 1 live-protected entry", report.Protected)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("live worktree %s was removed or unstattable: %v", wt, err)
	}
}

// TestReapClosedBeadWorktrees_ProtectsWorktreeContainingRegisteredDescendant
// catches the silent data-loss path from gas-o248. A closed outer worktree can
// be git-clean while containing another registered worktree under an ignored
// worktrees/ directory. Removing the outer tree destroys the inner checkout and
// leaves the inner worktree registration dangling, so the reaper must refuse
// the outer tree before it reaches git worktree remove.
func TestReapClosedBeadWorktrees_ProtectsWorktreeContainingRegisteredDescendant(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	outer := addClosedWorktree(t, rigRoot, cityPath, "builder", "ga-outer1")
	inner := addNestedRegisteredWorktree(t, rigRoot, outer, "ga-inner1")
	store := beads.NewMemStoreFrom(1, []beads.Bead{
		{ID: "ga-outer1", Status: "closed"},
		{ID: "ga-inner1", Status: "open", Metadata: map[string]string{"work_dir": inner}},
	}, nil)
	cfg := reapTestConfig(rigRoot)
	injectLiveness(t, liveWorktreeState{scanned: true})

	excludePath := filepath.Join(rigRoot, ".git", "info", "exclude")
	existing, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read git exclude: %v", err)
	}
	if err := os.WriteFile(excludePath, append(existing, []byte("\nworktrees/\n")...), 0o644); err != nil {
		t.Fatalf("write git exclude: %v", err)
	}
	if got := strings.TrimSpace(mustGitOutput(t, outer, "status", "--porcelain")); got != "" {
		t.Fatalf("outer worktree status = %q, want clean with nested worktrees/ ignored", got)
	}

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, nil, false, events.Discard, nil, &stderr)

	if len(report.Reaped) != 0 {
		t.Fatalf("Reaped = %+v, want 0 when a registered descendant worktree exists\nstderr:\n%s", report.Reaped, stderr.String())
	}
	if len(report.Protected) != 1 || !strings.Contains(report.Protected[0].Reason, "descendant worktree") {
		t.Fatalf("Protected = %+v, want descendant-worktree protection", report.Protected)
	}
	if _, err := os.Stat(outer); err != nil {
		t.Fatalf("outer worktree should remain protected, stat err=%v", err)
	}
	if _, err := os.Stat(inner); err != nil {
		t.Fatalf("inner registered worktree should remain protected, stat err=%v", err)
	}
}

// TestReapClosedBeadWorktrees_ProtectsViaActiveSessionDir proves the
// active-session cross-check: even with no live process cwd, a worktree that
// matches an open session's recorded working directory is protected.
func TestReapClosedBeadWorktrees_ProtectsViaActiveSessionDir(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	wt := addClosedWorktree(t, rigRoot, cityPath, "builder", "ga-sess01")
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-sess01", Status: "closed"}}, nil)
	cfg := reapTestConfig(rigRoot)
	injectLiveness(t, liveWorktreeState{scanned: true}) // no live cwds

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, []string{wt}, false, events.Discard, nil, &stderr)

	if len(report.Reaped) != 0 {
		t.Fatalf("Reaped = %+v, want 0 when an active session claims the worktree", report.Reaped)
	}
	if len(report.Protected) != 1 || !strings.Contains(report.Protected[0].Reason, "session") {
		t.Fatalf("Protected = %+v, want 1 session-protected entry", report.Protected)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("session-claimed worktree %s was removed: %v", wt, err)
	}
}

// TestReapClosedBeadWorktrees_FailsClosedWhenLivenessUnavailable proves the
// fail-closed backstop: when the process-table scan is indeterminate, an
// otherwise perfectly reapable worktree is protected rather than removed.
func TestReapClosedBeadWorktrees_FailsClosedWhenLivenessUnavailable(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	wt := addClosedWorktree(t, rigRoot, cityPath, "builder", "ga-fail01")
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-fail01", Status: "closed"}}, nil)
	cfg := reapTestConfig(rigRoot)
	injectLiveness(t, liveWorktreeState{scanned: false}) // liveness indeterminate

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, nil, false, events.Discard, nil, &stderr)

	if len(report.Reaped) != 0 {
		t.Fatalf("Reaped = %+v, want 0 when liveness scan is unavailable (fail closed)", report.Reaped)
	}
	if len(report.Protected) != 1 || !strings.Contains(report.Protected[0].Reason, "liveness scan unavailable") {
		t.Fatalf("Protected = %+v, want 1 fail-closed entry", report.Protected)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree %s removed despite indeterminate liveness: %v", wt, err)
	}
}

// TestReapClosedBeadWorktrees_DryRunRemovesNothing proves dry-run classifies
// but never deletes: the would-reap set is populated and reported, an event is
// emitted, yet the worktree remains on disk.
func TestReapClosedBeadWorktrees_DryRunRemovesNothing(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	wt := addClosedWorktree(t, rigRoot, cityPath, "builder", "ga-dry001")
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-dry001", Status: "closed"}}, nil)
	cfg := reapTestConfig(rigRoot)
	injectLiveness(t, liveWorktreeState{scanned: true})

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, nil, true, events.Discard, nil, &stderr)

	if !report.DryRun {
		t.Fatal("report.DryRun = false, want true")
	}
	if len(report.Reaped) != 1 || report.Reaped[0].BeadID != "ga-dry001" {
		t.Fatalf("Reaped (would-reap) = %+v, want exactly ga-dry001", report.Reaped)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("dry-run removed or broke worktree %s: %v", wt, err)
	}
	if !strings.Contains(stderr.String(), "dry-run: would reap") {
		t.Fatalf("stderr = %q, want a dry-run would-reap line", stderr.String())
	}
}

// TestReapClosedBeadWorktrees_SkipsOpenBead confirms a worktree whose bead is
// still open is untouched and not reported as reaped or protected.
func TestReapClosedBeadWorktrees_SkipsOpenBead(t *testing.T) {
	cityPath, rigRoot := initReapRig(t)
	wt := addClosedWorktree(t, rigRoot, cityPath, "builder", "ga-open01")
	store := beads.NewMemStoreFrom(1, []beads.Bead{{ID: "ga-open01", Status: "open"}}, nil)
	cfg := reapTestConfig(rigRoot)
	injectLiveness(t, liveWorktreeState{scanned: true})

	var stderr bytes.Buffer
	report := reapClosedBeadWorktrees(cityPath, cfg, map[string]beads.Store{"mrig": store}, nil, false, events.Discard, nil, &stderr)

	if len(report.Reaped) != 0 || len(report.Protected) != 0 {
		t.Fatalf("open bead touched: Reaped=%+v Protected=%+v", report.Reaped, report.Protected)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("worktree for open bead %s was removed: %v", wt, err)
	}
}
