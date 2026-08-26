package providerledger

import (
	"testing"
	"time"
)

func TestWaiverExpiryEnforced(t *testing.T) {
	tests := []struct {
		name  string
		set   bool
		value string
		want  bool
	}{
		{name: "unset enforces", set: false, want: true},
		{name: "empty enforces", set: true, value: "", want: true},
		{name: "warn opts out", set: true, value: "warn", want: false},
		{name: "WARN opts out case-insensitively", set: true, value: "WARN", want: false},
		{name: "padded warn opts out", set: true, value: "  warn  ", want: false},
		{name: "any other value enforces", set: true, value: "enforce", want: true},
		{name: "truthy-looking value still enforces", set: true, value: "1", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(WaiverExpiryEnvVar, tt.value)
			} else {
				t.Setenv(WaiverExpiryEnvVar, "")
				// t.Setenv cannot unset; empty is the unset-equivalent input.
			}
			if got := WaiverExpiryEnforced(); got != tt.want {
				t.Fatalf("WaiverExpiryEnforced() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEarliestWaiverExpiry(t *testing.T) {
	base := time.Date(2026, time.August, 26, 0, 0, 0, 0, time.UTC)

	t.Run("picks the soonest dated waiver", func(t *testing.T) {
		entries := []Entry{
			validRuntimeEntry("runtime.late", "exact:late", waiverClaim(base.Add(48*time.Hour))),
			validRuntimeEntry("runtime.soon", "exact:soon", waiverClaim(base.Add(2*time.Hour))),
			validRuntimeEntry("runtime.mid", "exact:mid", waiverClaim(base.Add(24*time.Hour))),
		}
		if got := EarliestWaiverExpiry(entries); !got.Equal(base.Add(2 * time.Hour)) {
			t.Fatalf("EarliestWaiverExpiry() = %v, want %v", got, base.Add(2*time.Hour))
		}
	})

	t.Run("zero when no dated waiver", func(t *testing.T) {
		entries := []Entry{validRuntimeEntry("runtime.na", "exact:na", ContractClaim{
			Contract:            ContractRuntimeProvider,
			Disposition:         DispositionNotApplicable,
			NotApplicableReason: "faulting double",
		})}
		if got := EarliestWaiverExpiry(entries); !got.IsZero() {
			t.Fatalf("EarliestWaiverExpiry() = %v, want zero", got)
		}
	})
}

func TestExpiryLapseOnly(t *testing.T) {
	now := time.Date(2026, time.August, 26, 12, 0, 0, 0, time.UTC)
	lapsed := now.Add(-24 * time.Hour)
	current := now.Add(14 * 24 * time.Hour)

	t.Run("true when the only problem is a lapsed waiver", func(t *testing.T) {
		entries := []Entry{validRuntimeEntry("runtime.lapsed", "exact:lapsed", waiverClaim(lapsed))}
		if Validate(entries, now) == nil {
			t.Fatal("precondition: Validate should fail on the lapsed waiver")
		}
		if !ExpiryLapseOnly(entries, now) {
			t.Fatal("ExpiryLapseOnly() = false, want true")
		}
	})

	t.Run("false when a structural problem rides along", func(t *testing.T) {
		entries := []Entry{
			validRuntimeEntry("runtime.lapsed", "exact:lapsed", waiverClaim(lapsed)),
			// Waived disposition with no waiver at all: structural, never excusable.
			validRuntimeEntry("runtime.broken", "exact:broken", ContractClaim{
				Contract:    ContractRuntimeProvider,
				Disposition: DispositionWaived,
			}),
		}
		if ExpiryLapseOnly(entries, now) {
			t.Fatal("ExpiryLapseOnly() = true, want false — a structural problem must not be excused")
		}
	})

	t.Run("false when the ledger is clean", func(t *testing.T) {
		entries := []Entry{validRuntimeEntry("runtime.ok", "exact:ok", waiverClaim(current))}
		if err := Validate(entries, now); err != nil {
			t.Fatalf("precondition: Validate should pass, got %v", err)
		}
		if ExpiryLapseOnly(entries, now) {
			t.Fatal("ExpiryLapseOnly() = true, want false on a clean ledger")
		}
	})

	// The regression that motivated the earliest.After(now) guard: with nothing
	// lapsed, the rewind target sits in the FUTURE. Rewinding forward relaxes
	// the maxWaiverHorizon check and would wave a real horizon violation
	// through as if it were a mere calendar lapse.
	t.Run("false for an over-horizon waiver with nothing lapsed", func(t *testing.T) {
		entries := []Entry{validRuntimeEntry("runtime.far", "exact:far", waiverClaim(now.Add(200*24*time.Hour)))}
		if Validate(entries, now) == nil {
			t.Fatal("precondition: Validate should reject a waiver beyond maxWaiverHorizon")
		}
		if ExpiryLapseOnly(entries, now) {
			t.Fatal("ExpiryLapseOnly() = true, want false — a horizon violation is not a calendar lapse")
		}
	})

	t.Run("false when there are no dated waivers", func(t *testing.T) {
		entries := []Entry{validRuntimeEntry("runtime.na", "exact:na", ContractClaim{
			Contract:    ContractRuntimeProvider,
			Disposition: DispositionWaived,
		})}
		if ExpiryLapseOnly(entries, now) {
			t.Fatal("ExpiryLapseOnly() = true, want false")
		}
	})
}

func waiverClaim(expires time.Time) ContractClaim {
	return ContractClaim{
		Contract:    ContractRuntimeProvider,
		Disposition: DispositionWaived,
		Waiver: &Waiver{
			Owner:   runtimeContractWaiverOwner,
			Expires: expires,
			Reason:  "tracked contract gap",
		},
	}
}
