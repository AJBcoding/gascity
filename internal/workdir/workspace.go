package workdir

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/pathutil"
)

// InWorktreesLane reports whether path resolves under cityPath's worktrees
// root — a pure path-membership check, not a claim about who creates the
// leaf directory. A pack with no pre_start hook (the SDK has zero hardcoded
// roles, so this is legitimate) may still put a plain, gc-created directory
// there. Callers deciding whether to MkdirAll must also check that the
// agent actually configures pre_start before treating this lane as
// "someone else creates it, defer to ValidateWorktreeWorkspace" (gas-g9qg,
// DECISION 3 of gas-tvn5).
func InWorktreesLane(cityPath, path string) bool {
	if path == "" {
		return false
	}
	return pathutil.PathWithin(WorktreesRoot(cityPath), path)
}

// ValidateWorktreeWorkspace refuses a resolved work_dir that sits in the one
// lane where a linked worktree is genuinely expected — under the city's
// worktrees root — but is not one.
//
// The harm it prevents (py-3xe5, gas-u1f6): a path under the worktrees root
// that is a plain directory is not a workspace. An agent booted there has no
// git, no code, and no .beads; a path that is a plain *repo* is worse, because
// two sessions sharing one checkout move HEAD out from under each other's
// uncommitted work.
//
// The scoping is deliberate and was measured against the shapes actually in
// production (gas-tvn5). A blanket ".git must be a FILE" rule refuses to spawn
// the mayor and every witness (agent dirs under .gc/agents/**, no .git at all)
// and external repo checkouts (a plain .git directory) — all three legitimate.
// Only the worktrees lane carries the linked-worktree expectation, so only it
// is checked; every other shape resolves unchanged.
//
// A path that does not exist yet is accepted: the worktree is created by the
// pack's pre_start hook via "git worktree add", which runs after work_dir
// resolution. Failing closed on a missing path would refuse every polecat's
// first spawn.
func ValidateWorktreeWorkspace(cityPath, path string) error {
	if !InWorktreesLane(cityPath, path) {
		return nil
	}
	root := WorktreesRoot(cityPath)

	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		// Not created yet — pre_start's "git worktree add" makes it.
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"work_dir %q under the worktrees root %q is unreadable: %w", path, root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"work_dir %q under the worktrees root %q is not a directory: refusing a work_dir that is not a usable workspace",
			path, root)
	}

	gitPath := filepath.Join(path, ".git")
	gitInfo, err := os.Lstat(gitPath)
	if os.IsNotExist(err) {
		return fmt.Errorf(
			"work_dir %q under the worktrees root %q has no .git: refusing to run in a plain directory with no git, no code, and no .beads",
			path, root)
	}
	if err != nil {
		return fmt.Errorf(
			"work_dir %q under the worktrees root %q has an unreadable .git: %w", path, root, err)
	}
	if !gitInfo.Mode().IsRegular() {
		return fmt.Errorf(
			"work_dir %q under the worktrees root %q is not a linked worktree (.git is not a worktree pointer file): refusing a shared checkout where a linked worktree is expected",
			path, root)
	}
	return nil
}
