package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShippedCloseGraduationReleaseCheck(t *testing.T) {
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "check-shipped-close-graduation.sh")
	run := func(t *testing.T, version, root string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", script, version, root)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return string(out), 0
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run graduation check: %v", err)
		}
		return string(out), exitErr.ExitCode()
	}
	writeArtifacts := func(t *testing.T, root string) {
		t.Helper()
		files := map[string]string{
			"internal/config/config.go":                              "shipped_close_warn_only",
			"cmd/gc/work_record_gate.go":                             "ShippedCloseWarnOnly",
			"internal/rollout/flag_beads_shipped_close_warn_only.go": "NoticeCompatibilityModeActive",
			"internal/events/events.go":                              "work.close.warn_only.used",
			"docs/reference/config.md":                               "shipped_close_warn_only",
		}
		for name, body := range files {
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	withArtifacts := t.TempDir()
	writeArtifacts(t, withArtifacts)
	if out, code := run(t, "v1.5.0", withArtifacts); code != 0 {
		t.Fatalf("introduction release refused: code=%d output=%s", code, out)
	}
	out, code := run(t, "v1.6.0", withArtifacts)
	if code == 0 {
		t.Fatalf("removal release accepted stale migration artifacts: %s", out)
	}
	for _, want := range []string{"config", "consumer", "notice", "event", "docs", "v1.6.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("boundary output missing %q: %s", want, out)
		}
	}
	if out, code := run(t, "v1.6.0", t.TempDir()); code != 0 {
		t.Fatalf("removal release refused a graduated tree: code=%d output=%s", code, out)
	}
}

func TestVersionTagGateCallsShippedCloseGraduation(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	for _, want := range []string{"GC_RELEASE_VERSION", "check-shipped-close-graduation.sh"} {
		if !strings.Contains(text, want) {
			t.Errorf("check-version-tag is not injectable/native: Makefile missing %q", want)
		}
	}
}
