package main

import "testing"

// TestGasHZ3Z_PoolPolecatClaimIdentityIsAssignedWork pins the identity a pool
// polecat actually claims work under.
//
// Live shape, captured from the anthony city 2026-08-19 while the session was
// still running: a pool polecat's session bead is minted with a runtime
// SessionName ("gastown__polecat-az-wisp-bm7dj") and an opaque wisp ID, carries
// NO configured_named_identity, and is not a configured named session. But it
// claims work under its canonical instance name ("gascity/gastown.nux") — so
// the work bead's assignee matches neither of the two fields
// sessionAssigneeMatches checked, and the configured-named fallback is gated
// off for pool slots.
//
// The consequence was not cosmetic: sessionHasAssignedWork returned false for
// every pool polecat holding claimed work, countAssignedScaleSlots counted
// zero, the reconciler's tick summary reported work_requested=false, and it
// drained sessions that were mid-task. Two polecats were retired ~20 minutes
// into compat-surface work this way (gas-hz3z).
func TestGasHZ3Z_PoolPolecatClaimIdentityIsAssignedWork(t *testing.T) {
	const (
		template      = "gascity/gastown.polecat"
		wispID        = "az-wisp-bm7dj"
		runtimeName   = "gastown__polecat-az-wisp-bm7dj"
		claimedAsQual = "gascity/gastown.nux"
	)

	poolSlot := AwakeSessionBead{
		ID:          wispID,
		SessionName: runtimeName,
		Template:    template,
		State:       "active",
		// A pool slot has neither of these. They are what the pre-fix matcher
		// depended on, and their absence is the whole bug.
		NamedIdentity:          "",
		ConfiguredNamedSession: false,
		CanonicalInstanceName:  claimedAsQual,
	}

	inProgress := []AwakeWorkBead{{
		ID:       "gas-ot20",
		Assignee: claimedAsQual,
		Status:   "in_progress",
		Blocked:  false,
	}}

	t.Run("session_holding_claimed_work_is_seen", func(t *testing.T) {
		if !sessionHasAssignedWork(inProgress, nil, poolSlot) {
			t.Fatalf("pool polecat %s claimed %s as %q but sessionHasAssignedWork "+
				"reported no work; the reconciler drains this slot mid-task",
				wispID, inProgress[0].ID, claimedAsQual)
		}
	})

	t.Run("slot_is_counted_so_it_is_not_drained", func(t *testing.T) {
		got := countAssignedScaleSlots([]AwakeSessionBead{poolSlot}, inProgress, nil, template)
		if got != 1 {
			t.Fatalf("countAssignedScaleSlots = %d, want 1 (work_requested=false is what "+
				"retired the live sessions)", got)
		}
	})

	// Negative control: the canonical name must not become a wildcard. Work
	// claimed by a DIFFERENT polecat must not hold this slot awake, or the
	// drain path stops being able to retire genuinely idle slots.
	t.Run("another_polecats_work_does_not_match", func(t *testing.T) {
		otherWork := []AwakeWorkBead{{
			ID:       "gas-a2td",
			Assignee: "gascity/gastown.slit",
			Status:   "in_progress",
		}}
		if sessionHasAssignedWork(otherWork, nil, poolSlot) {
			t.Fatal("work assigned to gascity/gastown.slit matched the gascity/gastown.nux slot")
		}
	})

	// Negative control, and the one that constrains the fix's shape: the
	// canonical name is a REUSABLE SLOT LABEL, not a per-generation identity.
	// freeCanonicalIdentityMetadata runs only on the named-session retirement
	// path, so a drained pool slot keeps its canonical name indefinitely.
	// Verified on live beads 2026-08-19: az-wisp-uwaps and az-wisp-umdyr are
	// distinct drained generations of gascity/gastown.slit and BOTH still carry
	// canonical_instance_name=gascity/gastown.slit.
	//
	// If a drained generation could match live work, this fix would trade the
	// drain bug for an over-retention bug during the ~44s window between drain
	// and bead close, holding a dead slot open.
	t.Run("drained_generation_does_not_match_live_work", func(t *testing.T) {
		stale := poolSlot
		stale.ID = "az-wisp-uwaps" // the previous occupant of this same slot
		stale.SessionName = "gastown__polecat-az-wisp-uwaps"
		stale.State = "drained"
		stale.Drained = true

		if sessionAssigneeMatches(nil, stale, claimedAsQual) {
			t.Fatal("a drained generation matched live work via the reusable slot label; " +
				"a dead slot would be held open")
		}
		if got := countAssignedScaleSlots(
			[]AwakeSessionBead{stale, poolSlot}, inProgress, nil, template,
		); got != 1 {
			t.Fatalf("countAssignedScaleSlots = %d, want 1: one work bead must hold exactly "+
				"one slot, not both the live slot and its drained predecessor", got)
		}
	})

	// Negative control: an empty canonical name must not match an empty or
	// whitespace assignee. sessionHasAssignedWork already skips empty
	// assignees; this guards the matcher itself against a blank-matches-blank
	// hole if that filter ever moves.
	t.Run("blank_canonical_name_matches_nothing", func(t *testing.T) {
		blank := poolSlot
		blank.CanonicalInstanceName = ""
		if sessionAssigneeMatches(nil, blank, "") {
			t.Fatal("empty assignee matched a slot with no canonical name")
		}
		if sessionAssigneeMatches(nil, blank, claimedAsQual) {
			t.Fatal("slot with no canonical name matched a real assignee")
		}
	})
}
