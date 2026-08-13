package doctor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

// The fixtures below are the real incidents that motivated gas-1jyd, taken
// from this city's own .gc/events.jsonl rather than synthesized. Counts are
// the measured ones. Keeping them real is what makes the negative controls
// meaningful: they are the actual healthy orders and busy agents that ran
// alongside the failures, so a detector that merely flags high-volume things
// cannot pass this file.
const (
	// Measured failure/firing counts over the incident window.
	fixtureJSONLExportFires     = 73
	fixtureReaperFires          = 37
	fixtureCompactorFires       = 9
	fixtureBeadsHealthFires     = 919
	fixtureGateSweepFires       = 915
	fixtureExitStatus78         = "exit status 78"
	fixtureFuriosaWakes         = 131
	fixtureCapableWakes         = 99
	fixtureCapableClaims        = 99
	fixtureRictusWakes          = 88
	fixtureRictusClaims         = 4
	fixtureFuriosaAgent         = "gascity/gastown.furiosa"
	fixtureCapableAgent         = "gascity/gastown.capable"
	fixtureRictusAgent          = "gascity/gastown.rictus"
	fixtureJSONLExportOrder     = "jsonl-export"
	fixtureReaperOrder          = "reaper"
	fixtureCompactorOrder       = "mol-dog-compactor"
	fixtureBeadsHealthOrder     = "beads-health"
	fixtureGateSweepOrder       = "gate-sweep"
	fixtureControllerActor      = "controller"
	fixtureSessionEventActor    = "gc"
	fixtureBeadEventActor       = "cache-reconcile"
	fixtureMoleculeIssueType    = "molecule"
	fixtureMessageIssueType     = "message"
	fixtureSpreadWindowFraction = 4
)

// wastedWorkFixture accumulates events and hands them to a check.
type wastedWorkFixture struct {
	now  time.Time
	evts []events.Event
	seq  uint64
}

func newWastedWorkFixture() *wastedWorkFixture {
	return &wastedWorkFixture{now: time.Date(2026, 8, 13, 11, 34, 0, 0, time.UTC)}
}

func (f *wastedWorkFixture) add(event events.Event) {
	f.seq++
	event.Seq = f.seq
	f.evts = append(f.evts, event)
}

// spreadWithin places n events across the trailing fraction of a window so
// they land inside both signatures' windows regardless of which is shorter.
func (f *wastedWorkFixture) spreadWithin(window time.Duration, n int, emit func(ts time.Time)) {
	if n <= 0 {
		return
	}
	span := window / fixtureSpreadWindowFraction
	for i := 0; i < n; i++ {
		offset := time.Duration(int64(span) * int64(i) / int64(n))
		emit(f.now.Add(-span + offset))
	}
}

// order records n firings and, when failures > 0, that many failures.
func (f *wastedWorkFixture) order(name string, fires, failures int, message string) {
	f.spreadWithin(wastedWorkOrderWindow, fires, func(ts time.Time) {
		f.add(events.Event{Type: events.OrderFired, Ts: ts, Actor: fixtureControllerActor, Subject: name})
	})
	f.spreadWithin(wastedWorkOrderWindow, failures, func(ts time.Time) {
		f.add(events.Event{Type: events.OrderFailed, Ts: ts, Actor: fixtureControllerActor, Subject: name, Message: message})
	})
}

// wakes records n session.woke events for an agent. Each wake gets its own
// session id, which is the real shape of a spawn loop.
func (f *wastedWorkFixture) wakes(agent string, n int) {
	i := 0
	f.spreadWithin(wastedWorkSpawnWindow, n, func(ts time.Time) {
		i++
		f.add(events.Event{
			Type:      events.SessionWoke,
			Ts:        ts,
			Actor:     fixtureSessionEventActor,
			Subject:   agent,
			SessionID: fmt.Sprintf("az-wisp-%s-%d", strings.ReplaceAll(agent, "/", "-"), i),
		})
	})
}

// beadEvent records a bead lifecycle event carrying the assignee in its
// payload, exactly as the real log does.
func (f *wastedWorkFixture) beadEvent(eventType, assignee, status, issueType string, n int) {
	f.spreadWithin(wastedWorkSpawnWindow, n, func(ts time.Time) {
		payload, err := json.Marshal(map[string]any{
			"id":         "gas-fixture",
			"assignee":   assignee,
			"status":     status,
			"issue_type": issueType,
		})
		if err != nil {
			panic(err)
		}
		f.add(events.Event{
			Type:    eventType,
			Ts:      ts,
			Actor:   fixtureBeadEventActor,
			Subject: "gas-fixture",
			Payload: payload,
		})
	})
}

// claims records n beads moved to in_progress by an agent.
func (f *wastedWorkFixture) claims(assignee string, n int) {
	f.beadEvent(events.BeadUpdated, assignee, "in_progress", fixtureMoleculeIssueType, n)
}

// run executes the check over the fixture's events.
func (f *wastedWorkFixture) run(t *testing.T) *CheckResult {
	t.Helper()
	check := NewWastedWorkCheck(t.TempDir())
	check.clock = func() time.Time { return f.now }
	check.readWindow = func(_ string, since time.Time, types []string, _ int) ([]events.Event, error) {
		keep := map[string]bool{}
		for _, eventType := range types {
			keep[eventType] = true
		}
		var out []events.Event
		for _, event := range f.evts {
			if event.Ts.Before(since) || !keep[event.Type] {
				continue
			}
			out = append(out, event)
		}
		return out, nil
	}
	return check.Run(&CheckContext{CityPath: check.cityPath})
}

func detailsContaining(result *CheckResult, needle string) []string {
	var out []string
	for _, detail := range result.Details {
		if strings.Contains(detail, needle) {
			out = append(out, detail)
		}
	}
	return out
}

func assertFires(t *testing.T, result *CheckResult, needle string) {
	t.Helper()
	if got := detailsContaining(result, needle); len(got) == 0 {
		t.Fatalf("no alarm mentioning %q; status=%v message=%q details=%v",
			needle, result.Status, result.Message, result.Details)
	}
}

func assertSilent(t *testing.T, result *CheckResult, needle string) {
	t.Helper()
	if got := detailsContaining(result, needle); len(got) > 0 {
		t.Fatalf("false alarm on %q: %v", needle, got)
	}
}

// TestWastedWorkSignature1FiresOnRealFailingOrders is POSITIVE 1 from gas-1jyd:
// three orders that failed 100% of the time for 19h on a live city, all with
// "exit status 78". The detector must name every one of them, and must surface
// the message — "exit status 78" says EX_CONFIG/misconfiguration rather than
// flake, which is the difference between a config fix and a retry.
func TestWastedWorkSignature1FiresOnRealFailingOrders(t *testing.T) {
	f := newWastedWorkFixture()
	f.order(fixtureJSONLExportOrder, fixtureJSONLExportFires, fixtureJSONLExportFires, fixtureExitStatus78)
	f.order(fixtureReaperOrder, fixtureReaperFires, fixtureReaperFires, fixtureExitStatus78)
	f.order(fixtureCompactorOrder, fixtureCompactorFires, fixtureCompactorFires, fixtureExitStatus78)

	result := f.run(t)

	if result.Status != StatusError {
		t.Fatalf("status = %v, want error; message = %q", result.Status, result.Message)
	}
	for _, order := range []string{fixtureJSONLExportOrder, fixtureReaperOrder, fixtureCompactorOrder} {
		assertFires(t, result, "order "+order)
	}
	if got := detailsContaining(result, fixtureExitStatus78); len(got) != 3 {
		t.Fatalf("want the failure message on all 3 findings, got %d: %v", len(got), result.Details)
	}
	// Waste must be loud but must not gate the city's dispatch.
	if result.Severity != SeverityAdvisory {
		t.Fatalf("severity = %v, want advisory", result.Severity)
	}
	if result.FixHint == "" {
		t.Fatal("a failing check must carry an inspect hint")
	}
}

// TestWastedWorkSignature1SilentOnHealthyHighVolumeOrders is NEGATIVE 1: the
// two busiest orders on the city, 919 and 915 firings with zero failures. This
// is the control that proves the detector discriminates rather than flagging
// high-volume things.
func TestWastedWorkSignature1SilentOnHealthyHighVolumeOrders(t *testing.T) {
	f := newWastedWorkFixture()
	f.order(fixtureBeadsHealthOrder, fixtureBeadsHealthFires, 0, "")
	f.order(fixtureGateSweepOrder, fixtureGateSweepFires, 0, "")

	result := f.run(t)

	if result.Status != StatusOK {
		t.Fatalf("status = %v, want ok; message = %q details = %v", result.Status, result.Message, result.Details)
	}
}

// TestWastedWorkSignature1SampleGates pins both thresholds. A 100% rate alarms
// on few samples because it is unambiguous; a partial rate needs more samples
// so that genuine flake does not alarm — orphan-sweep legitimately swung
// 3%-48% with load (az-ar8z) and that was a timeout issue, not a config error.
func TestWastedWorkSignature1SampleGates(t *testing.T) {
	tests := []struct {
		name      string
		order     string
		fires     int
		failures  int
		wantAlarm bool
	}{
		{name: "full rate below sample gate", order: "tiny-total-failure", fires: 4, failures: 4, wantAlarm: false},
		{name: "full rate at sample gate", order: "small-total-failure", fires: 5, failures: 5, wantAlarm: true},
		{name: "partial rate below sample gate", order: "flaky-few", fires: 19, failures: 10, wantAlarm: false},
		{name: "partial rate at sample gate", order: "flaky-many", fires: 20, failures: 10, wantAlarm: true},
		{name: "partial rate below rate gate", order: "mostly-fine", fires: 100, failures: 49, wantAlarm: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newWastedWorkFixture()
			f.order(tc.order, tc.fires, tc.failures, fixtureExitStatus78)

			result := f.run(t)

			if tc.wantAlarm {
				assertFires(t, result, "order "+tc.order)
				return
			}
			assertSilent(t, result, "order "+tc.order)
		})
	}
}

// TestWastedWorkSignature2FiresOnSpawnLoop is POSITIVE 2: the agent woken 131
// times in the incident window that claimed and closed nothing.
func TestWastedWorkSignature2FiresOnSpawnLoop(t *testing.T) {
	f := newWastedWorkFixture()
	f.wakes(fixtureFuriosaAgent, fixtureFuriosaWakes)

	result := f.run(t)

	if result.Status != StatusError {
		t.Fatalf("status = %v, want error; message = %q", result.Status, result.Message)
	}
	assertFires(t, result, "agent "+fixtureFuriosaAgent)
}

// TestWastedWorkSignature2SilentOnAgentsThatClaimWork is NEGATIVE 2, and the
// hardest case: agents that woke just as often but were doing real dispatch.
// It separates "spawned and worked" from "spawned and declined".
func TestWastedWorkSignature2SilentOnAgentsThatClaimWork(t *testing.T) {
	f := newWastedWorkFixture()
	f.wakes(fixtureCapableAgent, fixtureCapableWakes)
	f.claims(fixtureCapableAgent, fixtureCapableClaims)
	f.wakes(fixtureRictusAgent, fixtureRictusWakes)
	f.claims(fixtureRictusAgent, fixtureRictusClaims)

	result := f.run(t)

	if result.Status != StatusOK {
		t.Fatalf("status = %v, want ok; message = %q details = %v", result.Status, result.Message, result.Details)
	}
}

// TestWastedWorkMailIsNotProgress is the regression guard for the correlation
// that does not hold. The real spawn-loop agent closed two beads during its 19h
// of doing nothing, and both were mail. Counting a closed message bead as
// progress would silence the alarm on the exact incident this detector exists
// to catch — and silence it because the agent read its mail.
func TestWastedWorkMailIsNotProgress(t *testing.T) {
	f := newWastedWorkFixture()
	f.wakes(fixtureFuriosaAgent, fixtureFuriosaWakes)
	f.beadEvent(events.BeadClosed, fixtureFuriosaAgent, "closed", fixtureMessageIssueType, 2)

	result := f.run(t)

	assertFires(t, result, "agent "+fixtureFuriosaAgent)
}

// TestWastedWorkClosingRealWorkIsProgress is the complement: an agent that
// closes non-mail beads is working, even with no in_progress transition of its
// own. The refinery is exactly this shape — it closes work it never claimed.
func TestWastedWorkClosingRealWorkIsProgress(t *testing.T) {
	f := newWastedWorkFixture()
	f.wakes("gascity/gastown.refinery", 40)
	f.beadEvent(events.BeadClosed, "gascity/gastown.refinery", "closed", fixtureMoleculeIssueType, 1)

	result := f.run(t)

	assertSilent(t, result, "agent gascity/gastown.refinery")
}

// TestWastedWorkAttributionIgnoresEventActor pins the finding that forced this
// design. The event `actor` field carries no agent identity: it is uniformly
// "gc" on session events and "cache-reconcile" on every bead event. A detector
// that attributed progress by actor would report every agent as idle. Here the
// agent genuinely claimed work, and the check must see it through the payload
// assignee despite the actor naming the machinery.
func TestWastedWorkAttributionIgnoresEventActor(t *testing.T) {
	f := newWastedWorkFixture()
	f.wakes(fixtureCapableAgent, fixtureCapableWakes)
	f.claims(fixtureCapableAgent, 1)
	for _, event := range f.evts {
		switch event.Type {
		case events.SessionWoke:
			if event.Actor != fixtureSessionEventActor {
				t.Fatalf("fixture drift: session actor = %q, want %q", event.Actor, fixtureSessionEventActor)
			}
		case events.BeadUpdated:
			if event.Actor != fixtureBeadEventActor {
				t.Fatalf("fixture drift: bead actor = %q, want %q", event.Actor, fixtureBeadEventActor)
			}
		}
	}

	result := f.run(t)

	assertSilent(t, result, "agent "+fixtureCapableAgent)
}

// TestWastedWorkCanonicalizesSessionNameAssignee pins the identity join. A
// polecat may claim under its ephemeral session name
// ("gastown__polecat-az-wisp-b4ee1") rather than its agent name, and that name
// embeds the session id from its own session.woke event. Without the join the
// agent's wakes and its claims land under two different keys and a working
// agent looks idle.
func TestWastedWorkCanonicalizesSessionNameAssignee(t *testing.T) {
	f := newWastedWorkFixture()
	const sessionID = "az-wisp-b4ee1"
	f.spreadWithin(wastedWorkSpawnWindow, wastedWorkSpawnMinWakes+5, func(ts time.Time) {
		f.add(events.Event{
			Type:      events.SessionWoke,
			Ts:        ts,
			Actor:     fixtureSessionEventActor,
			Subject:   fixtureRictusAgent,
			SessionID: sessionID,
		})
	})
	f.claims("gastown__polecat-"+sessionID, 1)

	result := f.run(t)

	assertSilent(t, result, "agent "+fixtureRictusAgent)
}

// TestWastedWorkSpawnSampleGate keeps a quiet agent from alarming just because
// it woke a few times without claiming anything.
func TestWastedWorkSpawnSampleGate(t *testing.T) {
	f := newWastedWorkFixture()
	f.wakes("python419/core.control-dispatcher", wastedWorkSpawnMinWakes-1)

	result := f.run(t)

	if result.Status != StatusOK {
		t.Fatalf("status = %v, want ok; details = %v", result.Status, result.Details)
	}
}

// TestWastedWorkBothSignaturesReportTogether pins the combined summary: the
// real incident had both shapes running at once.
func TestWastedWorkBothSignaturesReportTogether(t *testing.T) {
	f := newWastedWorkFixture()
	f.order(fixtureJSONLExportOrder, fixtureJSONLExportFires, fixtureJSONLExportFires, fixtureExitStatus78)
	f.wakes(fixtureFuriosaAgent, fixtureFuriosaWakes)
	f.order(fixtureBeadsHealthOrder, fixtureBeadsHealthFires, 0, "")
	f.claims(fixtureCapableAgent, fixtureCapableClaims)

	result := f.run(t)

	if result.Status != StatusError {
		t.Fatalf("status = %v, want error", result.Status)
	}
	assertFires(t, result, "order "+fixtureJSONLExportOrder)
	assertFires(t, result, "agent "+fixtureFuriosaAgent)
	assertSilent(t, result, "order "+fixtureBeadsHealthOrder)
	for _, want := range []string{"1 order", "1 agent"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message %q missing %q", result.Message, want)
		}
	}
}

// TestWastedWorkQuietCityIsOK keeps the check green on a city with nothing to
// report, and pins that the summary states the window it actually covered — a
// quiet result is least trustworthy when the window is short.
func TestWastedWorkQuietCityIsOK(t *testing.T) {
	f := newWastedWorkFixture()
	f.order(fixtureBeadsHealthOrder, fixtureBeadsHealthFires, 0, "")

	result := f.run(t)

	if result.Status != StatusOK {
		t.Fatalf("status = %v, want ok; details = %v", result.Status, result.Details)
	}
	if !strings.Contains(result.Message, "last") {
		t.Fatalf("message %q must state the window it covered", result.Message)
	}
}

// TestWastedWorkEmptyLogIsOK covers a city whose log has not been written yet.
func TestWastedWorkEmptyLogIsOK(t *testing.T) {
	f := newWastedWorkFixture()

	result := f.run(t)

	if result.Status != StatusOK {
		t.Fatalf("status = %v, want ok on an empty log", result.Status)
	}
}

// TestWastedWorkReadIsTimeBounded is the cost guard. This check exists to find
// quiet waste; a detector that scans a 72MB log in full on every doctor run
// would be its own best example. The read must be bounded by time and by a
// retained-event cap, and must ask only for the types the signatures consume.
func TestWastedWorkReadIsTimeBounded(t *testing.T) {
	f := newWastedWorkFixture()
	var gotSince time.Time
	var gotTypes []string
	var gotMax int
	check := NewWastedWorkCheck(t.TempDir())
	check.clock = func() time.Time { return f.now }
	check.readWindow = func(_ string, since time.Time, types []string, maxEvents int) ([]events.Event, error) {
		gotSince, gotTypes, gotMax = since, types, maxEvents
		return nil, nil
	}

	check.Run(&CheckContext{CityPath: check.cityPath})

	if gotSince.IsZero() {
		t.Fatal("read was not bounded by time; a zero floor walks the whole log")
	}
	if window := f.now.Sub(gotSince); window > wastedWorkOrderWindow {
		t.Fatalf("read window %s exceeds the longest signature window %s", window, wastedWorkOrderWindow)
	}
	if gotMax <= 0 {
		t.Fatal("read was not bounded by a retained-event cap")
	}
	if len(gotTypes) == 0 {
		t.Fatal("read asked for every event type; it should ask only for what the signatures consume")
	}
	for _, eventType := range gotTypes {
		switch eventType {
		case events.OrderFired, events.OrderFailed, events.SessionWoke, events.BeadUpdated, events.BeadClosed:
		default:
			t.Fatalf("read asked for unused event type %q", eventType)
		}
	}
}

// TestWastedWorkReadErrorSurfaces keeps a broken read from reporting silence.
func TestWastedWorkReadErrorSurfaces(t *testing.T) {
	check := NewWastedWorkCheck(t.TempDir())
	check.readWindow = func(_ string, _ time.Time, _ []string, _ int) ([]events.Event, error) {
		return nil, fmt.Errorf("boom")
	}

	result := check.Run(&CheckContext{CityPath: check.cityPath})

	if result.Status != StatusError {
		t.Fatalf("status = %v, want error when the read fails", result.Status)
	}
	if !strings.Contains(result.Message, "boom") {
		t.Fatalf("message %q must carry the read error", result.Message)
	}
}

// TestWastedWorkOrderHintStripsRigScope keeps the inspect hint runnable for a
// rig-scoped order subject ("escalate-rollups:rig:python419").
func TestWastedWorkOrderHintStripsRigScope(t *testing.T) {
	f := newWastedWorkFixture()
	f.order("escalate-rollups:rig:python419", 30, 30, fixtureExitStatus78)

	result := f.run(t)

	if !strings.Contains(result.FixHint, "gc order history escalate-rollups ") {
		t.Fatalf("hint %q must name the order without its rig scope", result.FixHint)
	}
}
