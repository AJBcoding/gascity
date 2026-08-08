package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// Tests for RigDefaultBranchCheck (gas-e6r). A rig whose configured
// default_branch does not resolve in its repo silently mis-bases every agent
// worktree, so doctor must name it rather than let it stay invisible.

func TestRigDefaultBranchCheck_ResolvesLocally_OK(t *testing.T) {
	rigPath := initGitRepoOnBranch(t, "main")
	c := NewRigDefaultBranchCheck(config.Rig{
		Name:          "testrig",
		Path:          rigPath,
		DefaultBranch: "main",
	})

	r := c.Run(&CheckContext{})

	if r.Status != StatusOK {
		t.Fatalf("status = %d (%s), want StatusOK", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "main") {
		t.Errorf("message = %q, want the branch name", r.Message)
	}
}

func TestRigDefaultBranchCheck_LocalOnlyBranchNotesUnpushed(t *testing.T) {
	// The gascity shape: default_branch names an integration branch that
	// exists only in the local clone. Valid as a worktree base, but worth
	// saying out loud — no other clone can reproduce it.
	rigPath := initGitRepoOnBranch(t, "feat/local-integration")
	c := NewRigDefaultBranchCheck(config.Rig{
		Name:          "testrig",
		Path:          rigPath,
		DefaultBranch: "feat/local-integration",
	})

	r := c.Run(&CheckContext{})

	if r.Status != StatusOK {
		t.Fatalf("status = %d (%s), want StatusOK", r.Status, r.Message)
	}
	joined := strings.Join(r.Details, " ")
	if !strings.Contains(joined, "local") {
		t.Errorf("Details = %v, want a note that the branch is local-only", r.Details)
	}
}

func TestRigDefaultBranchCheck_MissingBranch_ErrorsLoudly(t *testing.T) {
	rigPath := initGitRepoOnBranch(t, "main")
	c := NewRigDefaultBranchCheck(config.Rig{
		Name:          "testrig",
		Path:          rigPath,
		DefaultBranch: "feat/never-created",
	})

	r := c.Run(&CheckContext{})

	if r.Status != StatusError {
		t.Fatalf("status = %d (%s), want StatusError", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "feat/never-created") {
		t.Errorf("message = %q, want the unresolvable branch name", r.Message)
	}
	if r.FixHint == "" {
		t.Errorf("FixHint is empty, want an actionable hint")
	}
}

func TestRigDefaultBranchCheck_Unconfigured_OK(t *testing.T) {
	// No default_branch is not a misconfiguration — there is nothing pinned,
	// so there is nothing to verify.
	rigPath := initGitRepoOnBranch(t, "main")
	c := NewRigDefaultBranchCheck(config.Rig{Name: "testrig", Path: rigPath})

	r := c.Run(&CheckContext{})

	if r.Status != StatusOK {
		t.Fatalf("status = %d (%s), want StatusOK", r.Status, r.Message)
	}
}

func TestRigDefaultBranchCheck_GitUnavailable_Warns(t *testing.T) {
	c := NewRigDefaultBranchCheck(config.Rig{
		Name:          "testrig",
		Path:          t.TempDir(),
		DefaultBranch: "main",
	})
	c.gitPath = func(string) (string, error) { return "", errors.New("not found") }

	r := c.Run(&CheckContext{})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d (%s), want StatusWarning", r.Status, r.Message)
	}
}

func TestRigDefaultBranchCheck_NotAGitRepo_Warns(t *testing.T) {
	c := NewRigDefaultBranchCheck(config.Rig{
		Name:          "testrig",
		Path:          t.TempDir(),
		DefaultBranch: "main",
	})

	r := c.Run(&CheckContext{})

	if r.Status != StatusWarning {
		t.Fatalf("status = %d (%s), want StatusWarning", r.Status, r.Message)
	}
}
