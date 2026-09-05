package herdr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// ── LIVE end-to-end: the real Provider.Start against a real herdr and a real
// TUI (gas-vs0e) ─────────────────────────────────────────────────────────────
//
// The unit tests prove we CALL runtime.AcceptStartupDialogs and that dialog.go's
// matcher covers the captured modal text. They cannot prove the whole chain
// works, because the fake herdr renders whatever the test tells it to.
//
// That distinction is not academic here. Earlier in this bead's history a fix
// passed its unit tests, was reviewed, was approved, and did not execute at all
// on the majority provider — the fake said what the author expected the real
// thing to say. So this test drives Start against a genuine agent booting into a
// genuinely untrusted directory, and asserts on what the real TUI renders.
//
// Opt-in and self-skipping: it needs a live herdr server and the agent binary,
// and it starts a real session that costs a real provider session. Run with
//
//	GC_HERDR_LIVE_SESSION=anthony go test ./internal/runtime/herdr/ \
//	    -run TestLiveStartDismissesTrustDialogAndDeliversFirstTurn -v -timeout 15m
//
// It creates its own workspace (placement is derived from the session name, so
// "xsyxlive--<kind>" lands in a workspace called "xsyxlive") and closes nothing
// belonging to the city.
func TestLiveStartDismissesTrustDialogAndDeliversFirstTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("live: needs a real herdr server and agent binary")
	}
	session := os.Getenv("GC_HERDR_LIVE_SESSION")
	if session == "" {
		t.Skip("live: set GC_HERDR_LIVE_SESSION to the city's herdr session name")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("live: herdr not on PATH")
	}

	for _, kind := range []string{"claude", "codex"} {
		for _, variant := range []string{"plain", "preapproval"} {
			t.Run(kind+"-"+variant, func(t *testing.T) {
				if _, err := exec.LookPath(kind); err != nil {
					t.Skipf("live: %s not on PATH", kind)
				}
				// A FRESH directory is the whole precondition: it is what makes the
				// agent raise its workspace-trust modal. t.TempDir() is new per run,
				// so neither TUI has ever been asked to trust it.
				workDir := t.TempDir()
				if variant == "preapproval" {
					// The pre-approval arm grows a warning block onto the same
					// modal. gas-193q was first diagnosed as that block inverting
					// the option order; the plain arm is here to keep that claim
					// falsifiable, because measurement showed BOTH arms inverted
					// and the block is not what does it.
					writeLivePreApproval(t, workDir)
				}
				name := "xsyxlive--" + kind + "-" + variant

				p := New(session, t.TempDir(), workDir, 10*time.Second, 0)
				t.Cleanup(func() { _ = p.Stop(name) })

				const sentinel = "XSYX-LIVE-OK"
				cfg := runtime.Config{
					Command:      kind,
					WorkDir:      workDir,
					Nudge:        "Reply with exactly " + sentinel + " and nothing else.",
					ProcessNames: []string{kind},
				}

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				if err := p.Start(ctx, name, cfg); err != nil {
					t.Fatalf("Start: %v", err)
				}

				// Poll the real pane for the agent's REPLY. Not for the payload
				// anywhere on screen: the payload sitting in a composer, or echoed
				// in a transcript, proves only that keys were typed. A reply proves
				// the turn ran, which is the thing that was broken.
				//
				// Paced by herdr's own lifecycle signal rather than a fixed sleep,
				// per the resource census's standing invariant ("each owning test
				// replaces elapsed wall time with its lifecycle signal",
				// ResourceFixedSleep). `agent wait --until working` returns the
				// moment the agent starts a turn and otherwise costs its bound, so
				// a stuck agent — the failing case — paces the loop for free while
				// a healthy one is observed as soon as it moves. The verdict stays
				// the screen: a spurious transition cannot pass this test, because
				// only the reply satisfies the assertion.
				deadline := time.Now().Add(3 * time.Minute)
				var last string
				for time.Now().Before(deadline) {
					screen, err := p.Peek(name, 200)
					if err == nil {
						last = screen
						if strings.Count(screen, sentinel) > 1 {
							return // prompt + reply: the first turn ran
						}
					}
					_, _ = p.c.run(ctx, "agent", "wait", herdrAgentName(name),
						"--until", "working", "--timeout", "3000")
				}

				if containsTrustModal(last) {
					t.Fatalf("%s is still parked on its workspace-trust modal — Start did not dismiss it:\n%s", kind, tail(last, 25))
				}
				t.Fatalf("%s never ran its first turn (no reply containing %q):\n%s", kind, sentinel, tail(last, 25))
			})
		}
	}
}

// writeLivePreApproval gives workDir a permissions allow-list, which is what
// makes Claude Code add its "This folder pre-approves N tool permissions"
// warning to the trust modal.
func writeLivePreApproval(t *testing.T, workDir string) {
	t.Helper()
	const body = `{"permissions":{"allow":["Bash(ls:*)","Bash(cat:*)","Bash(grep:*)"]}}`
	dir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.local.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// containsTrustModal mirrors the strings dialog.go matches, so a failure can say
// WHICH way it failed: still on the modal (dismissal broken) versus dismissed
// but the turn never ran (delivery broken). Those want different fixes.
func containsTrustModal(s string) bool {
	return strings.Contains(s, "Quick safety check") ||
		strings.Contains(s, "trust this folder") ||
		strings.Contains(s, "Do you trust the contents of this directory?")
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
