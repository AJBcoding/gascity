package doctor

import (
	"fmt"
	"os/exec"

	"github.com/gastownhall/gascity/internal/config"
)

// RigDefaultBranchCheck verifies that a rig's configured default_branch
// actually resolves in the rig repository.
//
// default_branch is the base agent worktrees are cut from. A value that names
// no existing ref cannot be honored, and the historical behavior was to fall
// back to origin/HEAD without a word — so a rig deliberately pinned to an
// integration branch silently got worktrees based on main, and its agents
// debugged a tree that did not match the deployed binary (gas-e6r). This check
// makes that misconfiguration visible without waiting for an agent to spawn.
//
// SeverityAdvisory; WarmupEligible.
type RigDefaultBranchCheck struct {
	rig     config.Rig
	gitPath func(name string) (string, error) // injectable for tests
}

// NewRigDefaultBranchCheck creates a default-branch resolution check for the
// given rig.
func NewRigDefaultBranchCheck(rig config.Rig) *RigDefaultBranchCheck {
	return &RigDefaultBranchCheck{rig: rig, gitPath: exec.LookPath}
}

// Name returns the check identifier.
func (c *RigDefaultBranchCheck) Name() string { return "rig:" + c.rig.Name + ":default-branch" }

// WarmupEligible returns true so this check runs during gc start warm-up.
func (c *RigDefaultBranchCheck) WarmupEligible() bool { return true }

// CanFix returns false; creating or renaming a branch requires operator
// judgment about which ref was actually intended.
func (c *RigDefaultBranchCheck) CanFix() bool { return false }

// Fix is a no-op.
func (c *RigDefaultBranchCheck) Fix(_ *CheckContext) error { return nil }

// Run reports whether the rig's configured default_branch resolves to a ref in
// the rig repository, preferring the remote-tracking ref and accepting a
// local-only branch.
func (c *RigDefaultBranchCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name(), Severity: SeverityAdvisory}

	branch := c.rig.EffectiveDefaultBranch()
	if branch == "" {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("rig %q: no default_branch configured", c.rig.Name)
		return r
	}

	gitBin, err := c.gitPath("git")
	if err != nil {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("rig %q: unable to verify default_branch %q — git unavailable", c.rig.Name, branch)
		return r
	}

	// Confirm the path is a git repo before reading refs, so "not a repo" is
	// reported as such rather than as a missing branch.
	if _, err := runGitCommand(gitBin, c.rig.Path, "rev-parse", "--git-dir"); err != nil {
		r.Status = StatusWarning
		r.Message = fmt.Sprintf("rig %q: unable to verify default_branch %q — path is not a git repo", c.rig.Name, branch)
		return r
	}

	remoteRef := "refs/remotes/origin/" + branch
	localRef := "refs/heads/" + branch

	if _, err := runGitCommand(gitBin, c.rig.Path, "show-ref", "--verify", "--quiet", remoteRef); err == nil {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("rig %q default_branch %q resolves (origin)", c.rig.Name, branch)
		return r
	}

	if _, err := runGitCommand(gitBin, c.rig.Path, "show-ref", "--verify", "--quiet", localRef); err == nil {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("rig %q default_branch %q resolves (local only)", c.rig.Name, branch)
		r.Details = []string{
			fmt.Sprintf("%s exists locally but not on origin — agent worktrees can be based on it here, but no other clone can fetch it", branch),
		}
		return r
	}

	r.Status = StatusError
	r.Message = fmt.Sprintf("rig %q default_branch %q does not resolve in %s", c.rig.Name, branch, c.rig.Path)
	r.Details = []string{
		fmt.Sprintf("looked for %s and %s", remoteRef, localRef),
		"agent worktrees cannot be based on this branch; worktree setup fails rather than silently using origin/HEAD",
	}
	r.FixHint = fmt.Sprintf("git -C %q fetch origin %s   # or correct default_branch in city.toml", c.rig.Path, branch)
	return r
}
