package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// selfRemoteRepo initializes a repository and configures the named remotes,
// returning its path. A remote URL of "" means "point at this repository" —
// the herdr-src shape. It runs git through the package's existing doctorRunGit
// helper rather than a new exec site, which the subprocess census counts.
func selfRemoteRepo(t *testing.T, remotes map[string]string) string {
	t.Helper()
	doctorGitOK(t)
	dir := doctorInitRepo(t)
	for name, url := range remotes {
		if url == "" {
			url = dir
		}
		doctorRunGit(t, dir, "remote", "add", name, url)
	}
	return dir
}

func runSelfRemoteCheck(t *testing.T, path string) *CheckResult {
	t.Helper()
	c := NewRigSelfRemoteCheck(config.Rig{Name: "gascity", Path: path})
	if got, want := c.Name(), "rig:gascity:self-remote"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	return c.Run(&CheckContext{})
}

// TestRigSelfRemoteCheckReportsSelfReferentialRemote is the operator-facing half
// of gas-9sg. The durability probes now defend themselves against a remote that
// points at its own repository, but the misconfiguration stayed invisible — and
// silence is what let two keystone fixes sit single-copy on local disk. The
// check names the remote so it can be removed or knowingly kept.
func TestRigSelfRemoteCheckReportsSelfReferentialRemote(t *testing.T) {
	repo := selfRemoteRepo(t, map[string]string{
		"origin":    "https://github.com/gastownhall/gascity.git",
		"herdr-src": "",
	})

	r := runSelfRemoteCheck(t, repo)

	if r.Status != StatusWarning {
		t.Errorf("Status = %v, want %v", r.Status, StatusWarning)
	}
	if !strings.Contains(r.Message, "herdr-src") {
		t.Errorf("Message = %q, want it to name the self-referential remote", r.Message)
	}
	if r.FixHint == "" {
		t.Error("FixHint is empty; the operator needs the command that removes the remote")
	}
	// The surviving real remote must be reported, so the operator can see that
	// publication is still possible and where it goes.
	if !strings.Contains(strings.Join(r.Details, "\n"), "origin") {
		t.Errorf("Details = %q, want the surviving publication remote named", r.Details)
	}
}

// TestRigSelfRemoteCheckErrorsWhenNoPublicationRemoteSurvives covers the worst
// topology: every configured remote points back at this repository, so the rig
// has remotes but can publish to none of them. Work committed here exists in
// exactly one place.
func TestRigSelfRemoteCheckErrorsWhenNoPublicationRemoteSurvives(t *testing.T) {
	repo := selfRemoteRepo(t, map[string]string{"herdr-src": ""})

	r := runSelfRemoteCheck(t, repo)

	if r.Status != StatusError {
		t.Errorf("Status = %v, want %v (no remote can publish)", r.Status, StatusError)
	}
	if !strings.Contains(r.Message, "herdr-src") {
		t.Errorf("Message = %q, want it to name the self-referential remote", r.Message)
	}
}

// TestRigSelfRemoteCheckOKWithRealRemotesOnly guards against a check that cries
// wolf: a rig whose remotes are all genuine passes silently.
func TestRigSelfRemoteCheckOKWithRealRemotesOnly(t *testing.T) {
	repo := selfRemoteRepo(t, map[string]string{
		"origin":   "https://github.com/gastownhall/gascity.git",
		"upstream": "git@github.com:gastownhall/gascity.git",
	})

	if r := runSelfRemoteCheck(t, repo); r.Status != StatusOK {
		t.Errorf("Status = %v, want %v (all remotes are real)", r.Status, StatusOK)
	}
}

// TestRigSelfRemoteCheckOKWithNoRemotes keeps a deliberately local-only rig from
// being reported as misconfigured. Having no remote is a topology choice; having
// a remote that only looks like one is the defect.
func TestRigSelfRemoteCheckOKWithNoRemotes(t *testing.T) {
	repo := selfRemoteRepo(t, nil)

	if r := runSelfRemoteCheck(t, repo); r.Status != StatusOK {
		t.Errorf("Status = %v, want %v (no remotes is a valid topology)", r.Status, StatusOK)
	}
}

// TestRigSelfRemoteCheckStaysQuietWhenPathIsNotARepo keeps one condition to one
// check. A rig path that is not a repository is rig:<name>:git's finding, and
// warning about it here too inflates every warmup tally with a duplicate — which
// is exactly what a second warning did to gc start's check count. This check
// reports no finding of its own and says where the real one lives.
func TestRigSelfRemoteCheckStaysQuietWhenPathIsNotARepo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	r := runSelfRemoteCheck(t, dir)

	if r.Status != StatusOK {
		t.Errorf("Status = %v, want %v: a non-repo path is rig:<name>:git's finding, not this check's", r.Status, StatusOK)
	}
	if !strings.Contains(r.Message, "not readable") || !strings.Contains(r.Message, "rig:gascity:git") {
		t.Errorf("Message = %q, want it to report the unreadable probe and point at the check that owns it", r.Message)
	}
}

// TestRigSelfRemoteCheckDoesNotFix pins the check as report-only. Whether a
// same-repo remote is deleted or deliberately kept for local fetching is an
// operator judgment, not something doctor should decide.
func TestRigSelfRemoteCheckDoesNotFix(t *testing.T) {
	c := NewRigSelfRemoteCheck(config.Rig{Name: "gascity", Path: t.TempDir()})
	if c.CanFix() {
		t.Error("CanFix() = true, want false: removing a remote is an operator decision")
	}
	if err := c.Fix(&CheckContext{}); err != nil {
		t.Errorf("Fix() error = %v, want nil no-op", err)
	}
}
