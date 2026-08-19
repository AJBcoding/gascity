package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
)

// orderFiringSeries returns count order.fired events for subject spaced gap
// apart, oldest first, with the newest at end.
func orderFiringSeries(subject string, end time.Time, gap time.Duration, count int) []events.Event {
	evts := make([]events.Event, 0, count)
	for i := count - 1; i >= 0; i-- {
		evts = append(evts, events.Event{
			Type:    events.OrderFired,
			Subject: subject,
			Ts:      end.Add(-time.Duration(i) * gap),
		})
	}
	return evts
}

func TestObservedDispatchFloor_NoProbesWhenEveryOrderMeetsItsSchedule(t *testing.T) {
	// Every order fires at its declared cadence: there is no evidence of a
	// dispatcher floor at all, so nothing may be floored. Inferring a floor
	// here would let a genuinely broken fast order hide behind a slow one.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	var evts []events.Event
	evts = append(evts, orderFiringSeries("orphan-sweep", now, 5*time.Minute, 30)...)
	evts = append(evts, orderFiringSeries("wisp-compact", now, time.Hour, 30)...)

	floor := observedDispatchFloor(
		map[string]time.Duration{"orphan-sweep": 5 * time.Minute, "wisp-compact": time.Hour},
		evts,
	)
	if floor != 0 {
		t.Fatalf("floor = %v, want 0 when no order is dispatcher-bound", floor)
	}
}

func TestObservedDispatchFloor_UsesFastestDispatcherBoundOrder(t *testing.T) {
	// Two orders are demonstrably dispatcher-bound (they declare far below
	// what they achieve). The floor is the best cadence the dispatcher
	// actually sustains — the minimum across probes, not the average and not
	// the slowest, so the floor stays as small as the evidence allows.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	var evts []events.Event
	evts = append(evts, orderFiringSeries("beads-health", now, 2*time.Minute, 30)...)
	evts = append(evts, orderFiringSeries("gate-sweep", now, 3*time.Minute, 30)...)
	evts = append(evts, orderFiringSeries("orphan-sweep", now, 5*time.Minute, 30)...)

	floor := observedDispatchFloor(
		map[string]time.Duration{
			"beads-health": 30 * time.Second,
			"gate-sweep":   30 * time.Second,
			"orphan-sweep": 5 * time.Minute,
		},
		evts,
	)
	if floor != 2*time.Minute {
		t.Fatalf("floor = %v, want 2m (fastest dispatcher-bound order)", floor)
	}
}

func TestObservedDispatchFloor_TracksSustainedCadenceNotTypicalOne(t *testing.T) {
	// A jittery dispatcher: most cycles are quick, a minority are far slower.
	// Flooring at the typical gap would leave the slow tail permanently over
	// the 1.5x/3x thresholds, which is the false-page loop this fix removes.
	// The floor must reflect the cadence the dispatcher sustains, so it lands
	// in the slow tail rather than at the fast mode.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	var firings []time.Time
	at := now.Add(-8 * time.Hour)
	for i := 0; i < 40; i++ {
		gap := 2 * time.Minute
		if i%5 == 0 {
			gap = 6 * time.Minute
		}
		at = at.Add(gap)
		firings = append(firings, at)
	}
	evts := make([]events.Event, 0, len(firings))
	for _, ts := range firings {
		evts = append(evts, events.Event{Type: events.OrderFired, Subject: "beads-health", Ts: ts})
	}

	floor := observedDispatchFloor(
		map[string]time.Duration{"beads-health": 30 * time.Second},
		evts,
	)
	if floor != 6*time.Minute {
		t.Fatalf("floor = %v, want 6m — the sustained cadence, not the 2m mode", floor)
	}
}

func TestObservedDispatchFloor_IgnoresOrdersWithTooFewFirings(t *testing.T) {
	// A handful of firings is not a cadence. Below the sample threshold the
	// check must fall back to declared intervals rather than floor against
	// noise.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	evts := orderFiringSeries("beads-health", now, 2*time.Minute, orderFiringFloorMinGaps)

	floor := observedDispatchFloor(
		map[string]time.Duration{"beads-health": 30 * time.Second},
		evts,
	)
	if floor != 0 {
		t.Fatalf("floor = %v, want 0 with fewer than %d gaps", floor, orderFiringFloorMinGaps)
	}
}

func TestObservedDispatchFloor_CappedWhenDispatcherIsWedged(t *testing.T) {
	// A dispatcher this slow is itself the outage. Past the cap the floor is
	// discarded so the check keeps paging instead of ratifying the stall as
	// the new normal.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	gap := orderFiringDispatchFloorMax + 5*time.Minute
	evts := orderFiringSeries("beads-health", now, gap, 30)

	floor := observedDispatchFloor(
		map[string]time.Duration{"beads-health": time.Minute},
		evts,
	)
	if floor != 0 {
		t.Fatalf("floor = %v, want 0 above the %v cap", floor, orderFiringDispatchFloorMax)
	}
}

func TestOrderFiringCurrent_SubCycleOrderFlooredToDispatchCycle(t *testing.T) {
	// gas-t0tf regression. An order declaring an interval below the
	// controller's achievable tick can never satisfy age < 3*declared, so
	// this check paged a permanent BLOCKING CRITICAL that no operator action
	// could clear — the interval is a core-pack default, not city config.
	// Measured on the anthony city over 90h: beads-health and gate-sweep
	// declare 30s and both land at a 117-119s median, while every order
	// declaring >= 5m lands within 1.2x of its interval.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	cityPath, cfg := orderFiringTestCity(t)
	writeOrderFiringTestOrder(t, cityPath, "beads-health", "cooldown", "30s")
	evts := []events.Event{{Type: events.ControllerStarted, Ts: now.Add(-6 * time.Hour)}}
	evts = append(evts, orderFiringSeries("beads-health", now.Add(-2*time.Minute), 2*time.Minute, 30)...)
	writeOrderFiringTestEvents(t, cityPath, evts...)

	result := runOrderFiringCurrentTest(t, cfg, cityPath, now)
	if result.Status != StatusOK {
		t.Fatalf("status = %v, want OK; msg = %s; details = %v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "floored to observed dispatch cycle") {
		t.Fatalf("details = %v, want the floored expectation named in the row", result.Details)
	}
	if !strings.Contains(details, "declared 30s") {
		t.Fatalf("details = %v, want the declared interval kept visible", result.Details)
	}
}

func TestOrderFiringCurrent_FlooredOrderStillStaleWhenFiringStops(t *testing.T) {
	// The floor is computed from historical gaps, never from time-since-last
	// firing, so a dispatcher that stops entirely still pages: the gaps stay
	// small while the age grows past 3x the floor.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	cityPath, cfg := orderFiringTestCity(t)
	writeOrderFiringTestOrder(t, cityPath, "beads-health", "cooldown", "30s")
	evts := []events.Event{{Type: events.ControllerStarted, Ts: now.Add(-6 * time.Hour)}}
	evts = append(evts, orderFiringSeries("beads-health", now.Add(-30*time.Minute), 2*time.Minute, 30)...)
	writeOrderFiringTestEvents(t, cityPath, evts...)

	result := runOrderFiringCurrentTest(t, cfg, cityPath, now)
	if result.Status != StatusError {
		t.Fatalf("status = %v, want error; msg = %s; details = %v", result.Status, result.Message, result.Details)
	}
	if result.Severity != SeverityBlocking {
		t.Fatalf("Severity = %v, want SeverityBlocking when a floored order stops firing", result.Severity)
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "(CRITICAL: stale)") {
		t.Fatalf("details = %v, want stale detail", result.Details)
	}
}

func TestOrderFiringCurrent_FloorSkippedWhenItBarelyExceedsTheDeclaredInterval(t *testing.T) {
	// Caught against the live city: the observed cycle settles a couple of
	// seconds above a 5m declaration, so every 5m order grew a "(declared 5m,
	// floored to observed dispatch cycle)" row that reported a floor equal to
	// its own interval. Such an order is not dispatcher-bound — the
	// classifier's own overdue threshold already covers the difference — so it
	// must be judged on its declared interval with no floor annotation.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	cityPath, cfg := orderFiringTestCity(t)
	writeOrderFiringTestOrder(t, cityPath, "beads-health", "cooldown", "30s")
	writeOrderFiringTestOrder(t, cityPath, "cross-rig-deps", "cooldown", "5m")
	evts := []events.Event{{Type: events.ControllerStarted, Ts: now.Add(-12 * time.Hour)}}
	// The dispatcher sustains 5m2s: it floors the 30s order and must not
	// annotate the 5m one.
	cycle := 5*time.Minute + 2*time.Second
	evts = append(evts, orderFiringSeries("beads-health", now.Add(-cycle), cycle, 30)...)
	evts = append(evts, orderFiringSeries("cross-rig-deps", now.Add(-cycle), cycle, 30)...)
	writeOrderFiringTestEvents(t, cityPath, evts...)

	result := runOrderFiringCurrentTest(t, cfg, cityPath, now)
	for _, detail := range result.Details {
		if strings.HasPrefix(detail, "cross-rig-deps:") && strings.Contains(detail, "floored") {
			t.Fatalf("detail = %q, want no floor annotation on an order already at the cycle", detail)
		}
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "declared 30s, floored") {
		t.Fatalf("details = %v, want the sub-cycle order still floored", result.Details)
	}
}

func TestOrderFiringCurrent_FloorNeverLowersASlowerExpectation(t *testing.T) {
	// The floor only ever raises an expectation. A 6h order stays judged
	// against 6h even though the dispatcher cycles every couple of minutes;
	// otherwise a coarse order that silently stopped would read as healthy.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	cityPath, cfg := orderFiringTestCity(t)
	writeOrderFiringTestOrder(t, cityPath, "beads-health", "cooldown", "30s")
	writeOrderFiringTestOrder(t, cityPath, "prune-branches", "cooldown", "6h")
	evts := []events.Event{{Type: events.ControllerStarted, Ts: now.Add(-48 * time.Hour)}}
	evts = append(evts, orderFiringSeries("beads-health", now.Add(-2*time.Minute), 2*time.Minute, 30)...)
	evts = append(evts, events.Event{Type: events.OrderFired, Subject: "prune-branches", Ts: now.Add(-19 * time.Hour)})
	writeOrderFiringTestEvents(t, cityPath, evts...)

	result := runOrderFiringCurrentTest(t, cfg, cityPath, now)
	if result.Status != StatusError {
		t.Fatalf("status = %v, want error for the stalled 6h order; msg = %s; details = %v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "prune-branches") || !strings.Contains(details, "(CRITICAL: stale)") {
		t.Fatalf("details = %v, want prune-branches reported stale", result.Details)
	}
	if strings.Contains(details, "prune-branches: last fired 19h ago, expected every 2m") {
		t.Fatalf("details = %v, floor must not replace a coarser declared interval", result.Details)
	}
}
