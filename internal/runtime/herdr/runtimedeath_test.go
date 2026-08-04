package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// A mid-session runtime death is invisible today: herdr's log knows the pane and
// the terminating signal but not which gc session it was, and gc knows the
// session name but neither pane nor status — so attributing a death takes a
// hand-written timestamp join across two logs. recordRuntimeDeath closes that
// gap on the herdr side by persisting the pane id and the terminal agent_status
// that drove the not-alive verdict, at the moment the verdict is reached.

// TestRecordRuntimeDeathWritesAttribution is the core case: the artifact must
// name the session, the pane, and the terminal status, and include the final
// screen, so the death can be attributed without consulting herdr's log.
func TestRecordRuntimeDeathWritesAttribution(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	path := p.recordRuntimeDeath("kit--gastown__refinery", "232", "exited", "the last thing on screen\n")

	if path == "" {
		t.Fatal("recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading artifact at %s: %v", path, err)
	}
	got := string(b)
	for _, want := range []string{
		"session: kit--gastown__refinery",
		"pane: 232",
		"agent-status: exited",
		"the last thing on screen",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("artifact missing %q; got:\n%s", want, got)
		}
	}
	// Records live alongside the per-session sidecar dirs, never inside one —
	// see TestRecordRuntimeDeathSurvivesSessionTeardown for why that placement
	// is load-bearing rather than cosmetic.
	if wantDir := filepath.Join(p.metaDir, runtimeDeathsDir); filepath.Dir(path) != wantDir {
		t.Errorf("record dir = %s; want %s", filepath.Dir(path), wantDir)
	}
}

// TestRecordRuntimeDeathKeepsFirstObservation pins write-once semantics.
// ObserveLiveness is a polling path: a session that stays dead is observed
// terminal on every tick. Overwriting each time would march the recorded
// moment forward and replace the screen captured nearest the death with a
// later, emptier one — destroying exactly the evidence the artifact exists to
// hold. The first observation is the one closest to the death, so it wins.
func TestRecordRuntimeDeathKeepsFirstObservation(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	first := p.recordRuntimeDeath("kit--gastown__refinery", "232", "exited", "panic: the real final screen\n")
	if first == "" {
		t.Fatal("first recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	second := p.recordRuntimeDeath("kit--gastown__refinery", "232", "gone", "a later, emptier screen\n")

	b, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("reading artifact: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "panic: the real final screen") {
		t.Errorf("artifact lost the first observation's screen; got:\n%s", got)
	}
	if strings.Contains(got, "a later, emptier screen") {
		t.Errorf("a repeat observation overwrote the original death record; got:\n%s", got)
	}
	if second != "" && second != first {
		t.Errorf("second call returned a new path %s; want \"\" or the existing %s", second, first)
	}
}

// TestRecordRuntimeDeathStampsObservationTime pins the field that makes the
// artifact joinable against herdr's own log, where the terminating signal
// lives. It must be UTC RFC3339 — herdr logs UTC, and a local-time stamp would
// silently mis-order the join by the UTC offset.
func TestRecordRuntimeDeathStampsObservationTime(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	path := p.recordRuntimeDeath("kit--gastown__refinery", "232", "exited", "")
	if path == "" {
		t.Fatal("recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading artifact: %v", err)
	}

	var stamp string
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "observed: "); ok {
			stamp = strings.TrimSpace(rest)
			break
		}
	}
	if stamp == "" {
		t.Fatalf("artifact has no \"observed:\" line; got:\n%s", b)
	}
	ts, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("observed timestamp %q is not RFC3339: %v", stamp, err)
	}
	if _, offset := ts.Zone(); offset != 0 {
		t.Errorf("observed timestamp %q has UTC offset %ds; want 0 (herdr logs UTC)", stamp, offset)
	}
}

// TestClearRuntimeDeathReArmsForTheNextDeath is the other half of write-once.
// A session that dies is restarted by the reconciler and can die again; if the
// stale artifact kept blocking the write, only the very first death in a
// session's lifetime would ever be recorded — useless for an investigation that
// depends on accumulating deaths over days. Observing the session alive clears
// the record and re-arms capture.
func TestClearRuntimeDeathReArmsForTheNextDeath(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	if first := p.recordRuntimeDeath("kit--gastown__refinery", "232", "exited", "first death\n"); first == "" {
		t.Fatal("first recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	p.clearRuntimeDeath("kit--gastown__refinery")

	second := p.recordRuntimeDeath("kit--gastown__refinery", "248", "crashed", "second death\n")
	if second == "" {
		t.Fatal("post-clear recordRuntimeDeath returned \"\"; want the next death recorded")
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("reading artifact: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "second death") || !strings.Contains(got, "pane: 248") {
		t.Errorf("artifact does not describe the second death; got:\n%s", got)
	}
}

// TestShouldRecordRuntimeDeath pins which not-alive verdicts are real deaths.
// Only a *present* agent herdr reports terminal is one: that is the case where
// herdr still holds the pane and the status, i.e. where attribution is actually
// available. The other two not-alive shapes are not deaths and must not be
// recorded as such — a transport failure to herdr and an agent absent from the
// registry both yield an empty Liveness, and writing a death record for either
// would manufacture false attributions in exactly the evidence set this exists
// to keep clean. (An `agent get` failure is common: the benign agent_not_found
// path alone accounts for thousands of error lines in herdr's own log.)
func TestShouldRecordRuntimeDeath(t *testing.T) {
	cases := []struct {
		what string
		lv   runtime.Liveness
		want bool
	}{
		{"present agent reported terminal", runtime.Liveness{Running: true, Alive: false}, true},
		{"present and alive", runtime.Liveness{Running: true, Alive: true}, false},
		{"transport failure or absent agent", runtime.Liveness{}, false},
	}
	for _, c := range cases {
		if got := shouldRecordRuntimeDeath(c.lv); got != c.want {
			t.Errorf("%s: shouldRecordRuntimeDeath(%+v) = %v; want %v", c.what, c.lv, got, c.want)
		}
	}
}

// TestRuntimeDeathRecordedGuardsRepeatCost is the cost guard on the polling
// path. Gathering a death record costs two herdr round-trips (resolve the pane,
// read its screen), and write-once makes every one of them after the first a
// no-op. A session can sit dead across many reconciler ticks, so ObserveLiveness
// checks this cheap local predicate before paying for the gathering.
func TestRuntimeDeathRecordedGuardsRepeatCost(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	if p.runtimeDeathRecorded("kit--gastown__refinery") {
		t.Error("runtimeDeathRecorded = true before any death was recorded; want false")
	}
	if path := p.recordRuntimeDeath("kit--gastown__refinery", "232", "exited", ""); path == "" {
		t.Fatal("recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	if !p.runtimeDeathRecorded("kit--gastown__refinery") {
		t.Error("runtimeDeathRecorded = false after a death was recorded; want true")
	}
	// The re-arm must reopen the cheap path too, or the next death would be
	// skipped before the write-once guard ever got a say.
	p.clearRuntimeDeath("kit--gastown__refinery")
	if p.runtimeDeathRecorded("kit--gastown__refinery") {
		t.Error("runtimeDeathRecorded = true after clear; want false (capture must be re-armed)")
	}
}

// TestRecordRuntimeDeathSurvivesSessionTeardown is the case that decides where
// the record may live. The reconciler's response to the very verdict that
// triggers a record is to recycle the zombie: session_lifecycle_parallel.go
// observes running-but-not-alive and immediately calls sp.Stop, whose clearMeta
// does RemoveAll on the session's sidecar directory. A record kept in that
// directory is therefore deleted in the same reconciler pass that wrote it, and
// the artifact would never be observed in production even with every unit test
// green. Post-mortem evidence has to outlive the teardown that follows the
// death it documents.
func TestRecordRuntimeDeathSurvivesSessionTeardown(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	path := p.recordRuntimeDeath("kit--gastown__refinery", "232", "exited", "the evidence\n")
	if path == "" {
		t.Fatal("recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	if err := p.clearMeta("kit--gastown__refinery"); err != nil {
		t.Fatalf("clearMeta: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("death record did not survive session teardown (this is what sp.Stop does right after the death is observed): %v", err)
	}
	if !strings.Contains(string(b), "the evidence") {
		t.Errorf("record survived but lost its content; got:\n%s", b)
	}
}

// TestClearRuntimeDeathIsIdempotent guards the common path: clearRuntimeDeath
// runs on every observation of a live session, so a missing artifact is the
// normal case and must be a silent no-op rather than an error or a panic.
func TestClearRuntimeDeathIsIdempotent(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}
	p.clearRuntimeDeath("never--seen__session")
	p.clearRuntimeDeath("never--seen__session")
}

// TestRecordRuntimeDeathCarriesBoundPID pins the field that actually closes the
// join. herdr's log identifies a dead pane by a NUMERIC id (pane=139) and a pid
// (pid=54505); the pane id gascity holds is herdr's namespaced form (w1G:p3V),
// which never appears in the log — so the recorded pane id alone cannot be
// matched against the signal. The pid can: herdr logs the same pid on
// "pane child spawned" and on "pane session terminated". It has to be captured
// while the session is alive, because at death the pane is gone and process-info
// no longer answers.
func TestRecordRuntimeDeathCarriesBoundPID(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}
	if err := p.SetMeta("kit--gastown__refinery", metaBoundPID, "54505"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}

	path := p.recordRuntimeDeath("kit--gastown__refinery", "w1G:p3V", "", "")
	if path == "" {
		t.Fatal("recordRuntimeDeath returned \"\"; want a written artifact path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading record: %v", err)
	}
	if !strings.Contains(string(b), "pid: 54505") {
		t.Errorf("record lacks the bound pid, so it cannot be joined to herdr's log; got:\n%s", b)
	}
}

// TestRecordRuntimeDeathWithoutBoundPID keeps the record useful when the pid was
// never captured (a session bound by an older gascity, or a capture that
// failed). The record must still be written — a partial attribution beats none.
func TestRecordRuntimeDeathWithoutBoundPID(t *testing.T) {
	p := &Provider{metaDir: t.TempDir()}

	path := p.recordRuntimeDeath("kit--gastown__refinery", "w1G:p3V", "", "")
	if path == "" {
		t.Fatal("recordRuntimeDeath returned \"\" when no pid was bound; want the record written anyway")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "pid:") {
		t.Errorf("record claims a pid it never had; got:\n%s", b)
	}
}
