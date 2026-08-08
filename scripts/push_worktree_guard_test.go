package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestPushWorktreeGuard runs the shell self-test for the pre-push
// wrong-worktree guard and verdict header (gas-4pz). It exercises the real
// .githooks/pre-push against real temp repos and bare remotes: the hook
// must refuse to produce a test verdict from a worktree whose HEAD is not
// contained in any ref being pushed (the parked-repo-root false-P0 mode),
// must name the tested branch+SHA and the pushed refs when the suite does
// run, and must leave deletion-only and non-Go pushes untouched. Hermetic:
// temp git repos and a stub Makefile only, no network/bd/model calls.
func TestPushWorktreeGuard(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command(filepath.Join(root, "scripts", "test-push-worktree-guard.sh"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test-push-worktree-guard.sh failed: %v\n%s", err, out)
	}
}
