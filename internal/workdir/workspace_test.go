package workdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// polecatWorkDir returns the path a gastown polecat resolves to under the
// city's worktrees root, matching the shipped
// work_dir = ".gc/worktrees/{{.Rig}}/polecats/{{.AgentBase}}".
func polecatWorkDir(cityPath string) string {
	return filepath.Join(cityPath, ".gc", "worktrees", "gascity", "polecats", "gastown.rictus")
}

func TestValidateWorktreeWorkspace_LinkedWorktreeAccepted(t *testing.T) {
	city := t.TempDir()
	work := polecatWorkDir(city)
	writeValidGitPointer(t, work, filepath.Join(city, "repo", ".git", "worktrees", "rictus"))

	if err := ValidateWorktreeWorkspace(city, work); err != nil {
		t.Fatalf("ValidateWorktreeWorkspace() = %v, want nil (.git is a worktree pointer file)", err)
	}
}

// A plain directory under the worktrees root is the py-3xe5 husk: the agent
// boots into a workspace with no git, no code, no .beads.
func TestValidateWorktreeWorkspace_RefusesPlainDirWithNoGit(t *testing.T) {
	city := t.TempDir()
	work := polecatWorkDir(city)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	err := ValidateWorktreeWorkspace(city, work)
	if err == nil {
		t.Fatal("ValidateWorktreeWorkspace() = nil, want error (plain dir under worktrees root is not a workspace)")
	}
	if !strings.Contains(err.Error(), work) {
		t.Errorf("error %q missing the offending path %q", err.Error(), work)
	}
}

// A plain repo (.git directory) under the worktrees root is not a linked
// worktree: two sessions sharing it put two checkouts in one tree (gas-u1f6).
func TestValidateWorktreeWorkspace_RefusesPlainRepoGitDir(t *testing.T) {
	city := t.TempDir()
	work := polecatWorkDir(city)
	if err := os.MkdirAll(filepath.Join(work, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ValidateWorktreeWorkspace(city, work)
	if err == nil {
		t.Fatal("ValidateWorktreeWorkspace() = nil, want error (.git directory is not a linked worktree)")
	}
	if !strings.Contains(err.Error(), work) {
		t.Errorf("error %q missing the offending path %q", err.Error(), work)
	}
}

// The worktree is created by the pack's pre_start hook (`git worktree add`),
// which runs after work_dir resolution. A not-yet-created path must resolve
// cleanly or no polecat could ever spawn for the first time.
func TestValidateWorktreeWorkspace_MissingPathAccepted(t *testing.T) {
	city := t.TempDir()

	if err := ValidateWorktreeWorkspace(city, polecatWorkDir(city)); err != nil {
		t.Fatalf("ValidateWorktreeWorkspace() = %v, want nil (pre_start creates the worktree)", err)
	}
}

// The regression this rule was split out for (gas-tvn5): a blanket
// ".git must be a FILE" refuses the mayor, every witness, and kit/anthony.
// These are the shapes measured in the live city on 2026-08-09.
func TestValidateWorktreeWorkspace_ProductionShapesOutsideWorktreesRootStillSpawn(t *testing.T) {
	city := t.TempDir()

	mayor := filepath.Join(city, ".gc", "agents", "mayor")
	witness := filepath.Join(city, ".gc", "agents", "gascity", "witness")
	for _, dir := range []string{mayor, witness} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// An external plain repo checkout, e.g. ~/PycharmProjects/kit.
	external := filepath.Join(t.TempDir(), "kit")
	if err := os.MkdirAll(filepath.Join(external, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"mayor agent dir (no .git)", mayor},
		{"witness agent dir (no .git)", witness},
		{"external plain repo (.git dir)", external},
	} {
		if err := ValidateWorktreeWorkspace(city, tc.path); err != nil {
			t.Errorf("ValidateWorktreeWorkspace(%s) = %v, want nil (outside the worktrees root)", tc.name, err)
		}
	}
}

func TestValidateWorktreeWorkspace_RefusesNonDirectoryPath(t *testing.T) {
	city := t.TempDir()
	work := polecatWorkDir(city)
	if err := os.MkdirAll(filepath.Dir(work), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(work, []byte("not a workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ValidateWorktreeWorkspace(city, work); err == nil {
		t.Fatal("ValidateWorktreeWorkspace() = nil, want error (work_dir is a regular file)")
	}
}

func TestInWorktreesLane_UnderRootTrue(t *testing.T) {
	city := t.TempDir()
	if !InWorktreesLane(city, polecatWorkDir(city)) {
		t.Error("InWorktreesLane() = false, want true (path is under the worktrees root)")
	}
}

// Agent dirs and external repo checkouts are not worktrees, and neither
// carries a session-scoped identity under the worktrees root.
func TestInWorktreesLane_OutsideRootFalse(t *testing.T) {
	city := t.TempDir()
	mayor := filepath.Join(city, ".gc", "agents", "mayor")
	external := filepath.Join(t.TempDir(), "kit")

	for _, tc := range []struct {
		name string
		path string
	}{
		{"agent dir", mayor},
		{"external repo", external},
	} {
		if InWorktreesLane(city, tc.path) {
			t.Errorf("InWorktreesLane(%s) = true, want false (outside the worktrees root)", tc.name)
		}
	}
}

func TestInWorktreesLane_EmptyPathFalse(t *testing.T) {
	if InWorktreesLane(t.TempDir(), "") {
		t.Error("InWorktreesLane() = true, want false for an empty path")
	}
}

// WorktreesRoot is env-overridable; the lane check must follow it rather
// than assuming <city>/.gc/worktrees.
func TestInWorktreesLane_HonorsWorktreesRootOverride(t *testing.T) {
	city := t.TempDir()
	root := t.TempDir()
	t.Setenv("GC_WORKTREES_DIR", root)

	underOverride := filepath.Join(root, "gascity", "polecats", "gastown.rictus")
	if !InWorktreesLane(city, underOverride) {
		t.Error("InWorktreesLane() = false, want true (under the overridden worktrees root)")
	}

	// The default location is no longer the worktrees lane once overridden.
	if InWorktreesLane(city, polecatWorkDir(city)) {
		t.Error("InWorktreesLane() = true, want false (outside the overridden worktrees root)")
	}
}

// WorktreesRoot is env-overridable; the refusal lane must follow it rather
// than assuming <city>/.gc/worktrees.
func TestValidateWorktreeWorkspace_HonorsWorktreesRootOverride(t *testing.T) {
	city := t.TempDir()
	root := t.TempDir()
	t.Setenv("GC_WORKTREES_DIR", root)

	husk := filepath.Join(root, "gascity", "polecats", "gastown.rictus")
	if err := os.MkdirAll(husk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeWorkspace(city, husk); err == nil {
		t.Fatal("ValidateWorktreeWorkspace() = nil, want error (husk under the overridden worktrees root)")
	}

	// The default location is no longer the worktrees lane once overridden.
	def := polecatWorkDir(city)
	if err := os.MkdirAll(def, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorktreeWorkspace(city, def); err != nil {
		t.Errorf("ValidateWorktreeWorkspace() = %v, want nil (outside the overridden worktrees root)", err)
	}
}
