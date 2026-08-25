package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCheckCensusOwnerLiveness runs the shell self-test for
// scripts/check-census-owner-liveness.sh, the worked example for the
// three-valued check exit convention (gas-xraq, see
// engdocs/contributors/check-exit-code-conventions.md). It pins the exit
// contract — 0 ran-clean, 1 ran-finding, 2 could-not-run — across every path
// that used to be indistinguishable: a doctor warning carrying only skipped
// scopes, a missing gc/bd/jq, unparseable doctor JSON, an absent check, a
// failed or unparseable dedupe query, and a failed alert creation. Hermetic:
// each case runs under a PATH rebuilt from scratch holding only symlinked
// coreutils plus gc/bd stubs, so no bead store, city, or network is touched.
func TestCheckCensusOwnerLiveness(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command(filepath.Join(root, "scripts", "test-check-census-owner-liveness.sh"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test-check-census-owner-liveness.sh failed: %v\n%s", err, out)
	}
}
