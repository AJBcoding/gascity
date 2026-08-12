package beadmeta

import "testing"

// TestDispatchHoldLabelsMatchCanonicalHoldValues pins beadmeta as the single
// named home for the two canonical hold values documented in
// engdocs/contributors/hold-label-conventions.md (hold:mayor, hold:external)
// so internal/config and cmd/gc can share one definition instead of each
// re-spelling the label strings (ga-x9kptu / ga-5736js).
func TestDispatchHoldLabelsMatchCanonicalHoldValues(t *testing.T) {
	if HoldMayorLabel != "hold:mayor" {
		t.Fatalf("HoldMayorLabel = %q, want %q", HoldMayorLabel, "hold:mayor")
	}
	if HoldExternalLabel != "hold:external" {
		t.Fatalf("HoldExternalLabel = %q, want %q", HoldExternalLabel, "hold:external")
	}
	want := []string{HoldMayorLabel, HoldExternalLabel}
	if len(DispatchHoldLabels) != len(want) {
		t.Fatalf("DispatchHoldLabels = %#v, want %#v", DispatchHoldLabels, want)
	}
	for i, v := range want {
		if DispatchHoldLabels[i] != v {
			t.Fatalf("DispatchHoldLabels[%d] = %q, want %q", i, DispatchHoldLabels[i], v)
		}
	}
}

// TestHoldNoneLabelIsNotADispatchHold pins the load-bearing omission: hold:none
// is the release marker `bd set-state <id> hold=none` leaves behind, so it names
// a bead that is NOT held and must stay out of DispatchHoldLabels. Adding it
// there would strand every released bead — the exact starvation loop gas-kg6
// reported, re-created by the mechanism meant to end it (gas-x284).
func TestHoldNoneLabelIsNotADispatchHold(t *testing.T) {
	if HoldNoneLabel != "hold:none" {
		t.Fatalf("HoldNoneLabel = %q, want %q", HoldNoneLabel, "hold:none")
	}
	for i, v := range DispatchHoldLabels {
		if v == HoldNoneLabel {
			t.Fatalf("DispatchHoldLabels[%d] = %q: the release marker must never filter dispatch", i, v)
		}
	}
}
