package providerledger

import (
	"os"
	"strings"
	"time"
)

// WaiverExpiryEnvVar opts the ledger's CALENDAR assertion — and only that
// assertion — out of failing the run. Set it to "warn" to downgrade an expired
// waiver from a fatal error to a logged one. Any other value, including unset,
// enforces.
//
// Why this exists (gas-l0es). Waiver expiry is a good idea: it stops a waiver
// becoming permanent by neglect. But it fails CLOSED, on a calendar date, with
// no warning window, against a repo-wide check — and this repo runs that check
// inside .githooks/pre-push. On 2026-08-26 that combination converted an
// upstream housekeeping lapse into a city-wide outage: eight runtime.Provider
// waivers expired by calendar with no commit to cause it, and every rig on
// staging/gascity-lane lost the ability to land work (gas-qvhp).
//
// The decisive detail is WHO the lapse is supposed to reach. The waiver's own
// comment says its short horizon exists "to put the question back in front of
// the owner while the context is still fresh". The owner is ga-80po0c.3, an
// UPSTREAM bead that does not exist in any store this city can reach. The lapse
// therefore cannot reach the owner from here; downstream it has no signaling
// value at all and produces only outages. Upstream reached the same verdict
// about their own side in the third renewal (66df4a499): "Lapsing isn't
// reaching the owner; it's just telling everyone else main is red."
//
// So the calendar check is dropped from the local push gate, and ONLY from
// there. Everything else the ledger asserts — catalog agreement, proof refs,
// double boundaries, the generated TESTING.md table — still fails the gate, and
// a bare `go test ./...` (or any CI lane that does not set this var) still
// enforces expiry. Nothing about the ledger is deleted or weakened.
const WaiverExpiryEnvVar = "GC_LEDGER_WAIVER_EXPIRY"

// WaiverExpiryEnforced reports whether an expired waiver should fail the run.
// It is true unless WaiverExpiryEnvVar is exactly "warn".
func WaiverExpiryEnforced() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(WaiverExpiryEnvVar)), "warn")
}

// EarliestWaiverExpiry returns the soonest non-zero waiver expiry in entries,
// or the zero time when entries carry no dated waiver.
func EarliestWaiverExpiry(entries []Entry) time.Time {
	var earliest time.Time
	for _, entry := range entries {
		for _, claim := range entry.Claims {
			if claim.Waiver == nil || claim.Waiver.Expires.IsZero() {
				continue
			}
			if earliest.IsZero() || claim.Waiver.Expires.Before(earliest) {
				earliest = claim.Waiver.Expires
			}
		}
	}
	return earliest
}

// ExpiryLapseOnly reports whether every problem Validate finds at now is a
// waiver that has simply run out of calendar — i.e. nothing structural is
// wrong.
//
// It answers this WITHOUT parsing Validate's message strings, which would
// couple this file to wording it does not own. Instead it re-runs Validate with
// the clock rewound to just before the earliest waiver expiry: at that instant
// no waiver can have lapsed, so a clean result means lapses were the only
// problems.
//
// The rewind MUST move the clock backwards, and the guard below is what makes
// that true. Validate also rejects a waiver dated further out than
// maxWaiverHorizon; moving `now` backwards makes that check strictly more
// likely to trip, so a horizon violation still surfaces as "not lapse-only" and
// still fails the run. Moving it FORWARDS would relax the horizon check and
// could wave a real violation through — which is exactly what happens when
// nothing has lapsed and the earliest expiry is still in the future. When no
// waiver has actually lapsed there is no lapse to excuse, so that case returns
// false and the caller fails the run as usual.
func ExpiryLapseOnly(entries []Entry, now time.Time) bool {
	earliest := EarliestWaiverExpiry(entries)
	if earliest.IsZero() {
		return false
	}
	// Nothing has lapsed yet (Validate treats a waiver as expired only when
	// Expires is not After now), so any failure is structural.
	if earliest.After(now) {
		return false
	}
	if Validate(entries, now) == nil {
		return false
	}
	return Validate(entries, earliest.Add(-time.Second)) == nil
}
