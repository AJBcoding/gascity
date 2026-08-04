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

// TestRuntimeDeathLive drives a real signal death through a real herdr and
// asserts ObserveLiveness leaves an attributable record behind. The unit tests
// prove the record's shape against a temp dir; only this proves the wiring
// fires at all, which rests on a claim no unit test can check: that herdr keeps
// a dead agent registered long enough to report a terminal agent_status. If
// herdr instead drops the agent on death, ObserveLiveness sees "absent", which
// is deliberately not a recordable death — and the feature would be silently
// inert in production with every unit test green.
//
// The death is self-inflicted by the session's own command (SIGHUP to its
// process group), matching the signal that dominates the observed production
// deaths.
func TestRuntimeDeathLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live herdr test in -short mode")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skip("herdr not installed")
	}

	metaDir := t.TempDir()
	p := New("gctest-death", metaDir, t.TempDir(), 0, 0)
	_ = p.Stop("dying")
	t.Cleanup(func() { _ = p.Stop("dying"); _ = p.TeardownServer() })

	ctx := context.Background()
	cfg := runtime.Config{
		WorkDir: t.TempDir(),
		// Announce, then hang up on itself — a real terminating signal, not a
		// clean exit, so herdr reports a death rather than a completion.
		Command: `echo ALIVE-MARKER; sleep 2; kill -HUP $$; sleep 30`,
	}
	if err := p.Start(ctx, "dying", cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Establish the session was genuinely up first, so a failure below is a
	// death-capture failure and not a start failure.
	var up bool
	for i := 0; i < 30; i++ {
		if screen, _ := p.Peek("dying", 10); strings.Contains(screen, "ALIVE-MARKER") {
			up = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !up {
		t.Fatal("session never produced its startup marker; cannot attribute a death to it")
	}
	if lv := p.ObserveLiveness("dying", nil); !lv.Running || !lv.Alive {
		t.Fatalf("pre-death ObserveLiveness = %+v; want Running=true Alive=true", lv)
	}

	// Poll the way the reconciler does, letting the wiring record on whichever
	// tick first sees the death.
	var final runtime.Liveness
	for i := 0; i < 40; i++ {
		time.Sleep(500 * time.Millisecond)
		final = p.ObserveLiveness("dying", nil)
		if !final.Alive {
			break
		}
	}
	if final.Alive {
		t.Fatalf("session still reads alive 20s after SIGHUP; ObserveLiveness = %+v", final)
	}
	t.Logf("post-death liveness: %+v", final)

	records, _ := filepath.Glob(filepath.Join(metaDir, runtimeDeathsDir, "*.log"))
	if len(records) == 0 {
		// Distinguish the two ways this fails: no record because the verdict
		// was not a recordable death (herdr dropped the agent), versus no
		// record despite a recordable verdict (a wiring bug).
		if !shouldRecordRuntimeDeath(final) {
			t.Fatalf("herdr does not keep a dead agent registered: post-death verdict %+v is 'absent', "+
				"which carries no pane or status to attribute. The provider-local artifact cannot "+
				"capture this death; attribution has to come from the binding/pane path instead", final)
		}
		t.Fatal("verdict was a recordable death but no record was written — wiring bug")
	}
	if len(records) != 1 {
		t.Errorf("got %d death records for one death; want exactly 1: %v", len(records), records)
	}

	b, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatalf("reading death record: %v", err)
	}
	got := string(b)
	t.Logf("death record %s:\n%s", filepath.Base(records[0]), got)
	for _, want := range []string{"session: dying", "observed: ", "pane: ", "agent-status: "} {
		if !strings.Contains(got, want) {
			t.Errorf("death record missing %q; got:\n%s", want, got)
		}
	}

	// Repeat polls of a still-dead session must not pile up records.
	for i := 0; i < 3; i++ {
		p.ObserveLiveness("dying", nil)
	}
	if again, _ := filepath.Glob(filepath.Join(metaDir, runtimeDeathsDir, "*.log")); len(again) != 1 {
		t.Errorf("repeat observations produced %d records; want 1: %v", len(again), again)
	}

	// The join, end to end. The record is only useful if its pid is the one
	// herdr writes on "pane session terminated" — that line carries the signal,
	// and the pane id gascity holds (namespaced, w1:p1) never appears in the log
	// at all. Assert against herdr's real log rather than trusting the encoding.
	var recPID string
	for _, line := range strings.Split(got, "\n") {
		if rest, ok := strings.CutPrefix(line, "pid: "); ok {
			recPID = strings.TrimSpace(rest)
		}
	}
	if recPID == "" {
		t.Fatal("death record carries no pid; it cannot be joined to herdr's log")
	}
	home, _ := os.UserHomeDir()
	hlog, err := os.ReadFile(filepath.Join(home, ".config", "herdr", "sessions", "gctest-death", "herdr-server.log"))
	if err != nil {
		t.Skipf("herdr session log unreadable, cannot verify the join: %v", err)
	}
	want := "pid=" + recPID + " signal="
	if !strings.Contains(string(hlog), want) {
		t.Errorf("recorded pid %s never appears on a herdr termination line (looked for %q) — "+
			"the captured pid is not the one herdr reports, so the join does not close", recPID, want)
	} else {
		for _, line := range strings.Split(string(hlog), "\n") {
			if strings.Contains(line, want) {
				t.Logf("JOIN OK — herdr log line for the recorded pid:\n  %s", strings.TrimSpace(line))
			}
		}
	}
}
