package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTailWindowLog writes evts as JSONL and returns the path.
func writeTailWindowLog(t *testing.T, evts ...Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var sb strings.Builder
	for _, event := range evts {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write events log: %v", err)
	}
	return path
}

// TestReadTailSinceKeepsWindowAndOrder pins the contract: events at or after
// the floor come back in chronological order, and older events do not.
func TestReadTailSinceKeepsWindowAndOrder(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	path := writeTailWindowLog(t,
		Event{Seq: 1, Type: "order.fired", Ts: now.Add(-10 * time.Hour)},
		Event{Seq: 2, Type: "order.fired", Ts: now.Add(-2 * time.Hour)},
		Event{Seq: 3, Type: "order.failed", Ts: now.Add(-1 * time.Hour)},
	)

	got, err := ReadTailSince(path, now.Add(-6*time.Hour), nil, 0)
	if err != nil {
		t.Fatalf("ReadTailSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2 inside the 6h window", len(got))
	}
	if got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("got seqs [%d %d], want chronological [2 3]", got[0].Seq, got[1].Seq)
	}
}

// TestReadTailSinceFiltersTypes checks that the type filter drops unwanted
// events without shortening the window the walk covers.
func TestReadTailSinceFiltersTypes(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	path := writeTailWindowLog(t,
		Event{Seq: 1, Type: "order.fired", Ts: now.Add(-3 * time.Hour)},
		Event{Seq: 2, Type: "bead.updated", Ts: now.Add(-2 * time.Hour)},
		Event{Seq: 3, Type: "session.woke", Ts: now.Add(-1 * time.Hour)},
	)

	got, err := ReadTailSince(path, now.Add(-6*time.Hour), []string{"order.fired", "session.woke"}, 0)
	if err != nil {
		t.Fatalf("ReadTailSince: %v", err)
	}
	if len(got) != 2 || got[0].Type != "order.fired" || got[1].Type != "session.woke" {
		t.Fatalf("got %+v, want only order.fired and session.woke", got)
	}
}

// TestReadTailSinceStopsAtTimeFloor is the cost guard. Without a time floor a
// tail read walks the whole log; this pins that a narrow window over a log far
// larger than one chunk reads only the recent end of it. The log here spans
// many 64KB chunks, so a walk that ignored the floor would return the old
// events too.
func TestReadTailSinceStopsAtTimeFloor(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	// Pad each old event so the old region spans well past one chunk.
	padding := strings.Repeat("x", 2048)
	evts := make([]Event, 0, 260)
	for i := 0; i < 250; i++ {
		evts = append(evts, Event{
			Seq:     uint64(i + 1),
			Type:    "order.fired",
			Ts:      now.Add(-48 * time.Hour),
			Message: padding,
		})
	}
	evts = append(evts, Event{Seq: 999, Type: "order.fired", Ts: now.Add(-30 * time.Minute)})
	path := writeTailWindowLog(t, evts...)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() <= tailWindowChunkSize {
		t.Fatalf("log is %d bytes; test needs a log larger than one %d-byte chunk", info.Size(), tailWindowChunkSize)
	}

	got, err := ReadTailSince(path, now.Add(-1*time.Hour), nil, 0)
	if err != nil {
		t.Fatalf("ReadTailSince: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 999 {
		t.Fatalf("got %d events (first seq %v), want only the in-window event 999 — the walk did not stop at the floor",
			len(got), firstSeq(got))
	}
}

// TestReadTailSinceCapsRetainedEvents pins the memory guard.
func TestReadTailSinceCapsRetainedEvents(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	evts := make([]Event, 0, 10)
	for i := 0; i < 10; i++ {
		evts = append(evts, Event{Seq: uint64(i + 1), Type: "order.fired", Ts: now.Add(-time.Duration(10-i) * time.Minute)})
	}
	path := writeTailWindowLog(t, evts...)

	got, err := ReadTailSince(path, now.Add(-6*time.Hour), nil, 3)
	if err != nil {
		t.Fatalf("ReadTailSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want the 3 the cap allows", len(got))
	}
	// The walk is backward, so the cap keeps the NEWEST events.
	if got[len(got)-1].Seq != 10 {
		t.Fatalf("newest retained seq = %d, want 10; the cap must keep the newest events", got[len(got)-1].Seq)
	}
}

// TestReadTailSinceMissingLogIsNotAnError keeps a city with no event log yet
// from turning every caller red.
func TestReadTailSinceMissingLogIsNotAnError(t *testing.T) {
	got, err := ReadTailSince(filepath.Join(t.TempDir(), "absent.jsonl"), time.Time{}, nil, 0)
	if err != nil {
		t.Fatalf("missing log must not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing log returned %d events, want none", len(got))
	}
}

// TestReadTailSinceMixedTimezoneOffsets pins that the floor compares instants,
// not strings. The city log carries both -07:00 and Z timestamps, so a
// lexicographic or offset-naive comparison silently mis-windows real events.
func TestReadTailSinceMixedTimezoneOffsets(t *testing.T) {
	zone := time.FixedZone("PDT", -7*3600)
	// Same instant, two spellings: 18:30Z == 11:30-07:00.
	inWindowUTC := time.Date(2026, 8, 13, 18, 30, 0, 0, time.UTC)
	inWindowLocal := time.Date(2026, 8, 13, 11, 45, 0, 0, zone)
	outOfWindow := time.Date(2026, 8, 13, 1, 0, 0, 0, zone)
	path := writeTailWindowLog(t,
		Event{Seq: 1, Type: "order.fired", Ts: outOfWindow},
		Event{Seq: 2, Type: "order.fired", Ts: inWindowUTC},
		Event{Seq: 3, Type: "order.fired", Ts: inWindowLocal},
	)

	since := time.Date(2026, 8, 13, 11, 0, 0, 0, zone) // 18:00Z
	got, err := ReadTailSince(path, since, nil, 0)
	if err != nil {
		t.Fatalf("ReadTailSince: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("got %d events %v, want the two at-or-after the floor regardless of offset spelling", len(got), allSeqs(got))
	}
}

func firstSeq(evts []Event) string {
	if len(evts) == 0 {
		return "none"
	}
	return fmt.Sprintf("%d", evts[0].Seq)
}

func allSeqs(evts []Event) []uint64 {
	out := make([]uint64, 0, len(evts))
	for _, event := range evts {
		out = append(out, event.Seq)
	}
	return out
}
