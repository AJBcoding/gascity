package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/sling"
)

// TestPrintSlingWarningsBlockedBead covers the operator-facing half of gas-9wc.
// The core probe is worthless if its result never reaches a human: the original
// failure was silent precisely because both observable signals — the sling's own
// output and the bead's gc.routed_to — read "dispatched". The warning has to
// name the blockers and say what will not happen, in the same shape as the
// sibling "routed but nothing will pick it up" warnings (suspended rig, empty
// pool).
func TestPrintSlingWarningsBlockedBead(t *testing.T) {
	var stderr bytes.Buffer
	printSlingWarnings(sling.SlingResult{
		BeadID:    "gas-dr5",
		Target:    "gascity/gastown.polecat",
		BlockedBy: []string{"gas-3xn", "gas-9wc"},
	}, &stderr)

	got := stderr.String()
	for _, want := range []string{"gas-dr5", "gas-3xn", "gas-9wc", "gascity/gastown.polecat"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr = %q, want it to name %q", got, want)
		}
	}
	if !strings.Contains(got, "warning:") {
		t.Errorf("stderr = %q, want a warning: prefix matching the sibling route warnings", got)
	}
	if !strings.Contains(got, "no session will spawn") {
		t.Errorf("stderr = %q, want it to state the consequence — a route that reads as success but never dispatches is the bug", got)
	}
}

// TestPrintSlingWarningsUnblockedBeadSilent pins the negative: an ordinary
// sling must not grow a new line of noise.
func TestPrintSlingWarningsUnblockedBeadSilent(t *testing.T) {
	var stderr bytes.Buffer
	printSlingWarnings(sling.SlingResult{BeadID: "gas-dr5", Target: "gascity/gastown.polecat"}, &stderr)

	if got := stderr.String(); got != "" {
		t.Errorf("stderr = %q, want empty for an unblocked route", got)
	}
}

// TestSlingJSONWarningsBlockedBead pins the --json surface. Agents sling with
// --json and move on, so a warning that exists only on stderr leaves the
// caller most likely to hit this bug exactly as blind as before the fix.
func TestSlingJSONWarningsBlockedBead(t *testing.T) {
	warnings := slingJSONWarnings(sling.SlingResult{BeadID: "gas-dr5", BlockedBy: []string{"gas-3xn"}})
	if !slices.Contains(warnings, "bead_blocked") {
		t.Errorf("warnings = %v, want to contain bead_blocked", warnings)
	}
}

// TestSlingJSONResultCarriesBlockers pins the blocker IDs onto the typed
// payload. The tag alone says "something blocks this"; the IDs are what make
// it actionable without a second hand-rolled query — the exact hand-rolled
// query the gas-9wc follow-up note showed can be silently mis-scoped to the
// wrong store and return a false "nothing here".
func TestSlingJSONResultCarriesBlockers(t *testing.T) {
	payload := slingJSONFromResult(sling.SlingResult{
		BeadID:    "gas-dr5",
		Target:    "gascity/gastown.polecat",
		BlockedBy: []string{"gas-3xn"},
	})
	if !slices.Contains(payload.BlockedBy, "gas-3xn") {
		t.Errorf("payload.BlockedBy = %v, want to contain gas-3xn", payload.BlockedBy)
	}
}
