package runtime

import "testing"

// The predicate is the heart of the confirmation contract, so it is tested as a
// pure function against the shapes actually observed on the fleet (gas-jfy6).
func TestInputLineHoldsPending(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		sent   string
		want   bool
	}{
		{
			// The exact shape the mayor peeked: a multi-line nudge collapses to
			// a paste placeholder, so the literal text never appears.
			name:   "paste placeholder stranded in the composer",
			screen: "● Done thinking.\n\n> [Pasted text #13 +15 lines]",
			sent:   "PAUSE: stand down and stop work immediately",
			want:   true,
		},
		{
			name:   "literal text stranded in the composer",
			screen: "Nothing claimed, nothing to execute.\n\n> fix gas-5tm1",
			sent:   "fix gas-5tm1",
			want:   true,
		},
		{
			name:   "empty composer means the payload was submitted",
			screen: "> fix gas-5tm1\n● Working...\n\n> ",
			sent:   "fix gas-5tm1",
			want:   false,
		},
		{
			// The regression that a whole-screen search would cause: after a
			// successful submit the text is echoed into the transcript. Reading
			// anywhere but the composer reports a delivered message as stranded.
			name:   "text echoed in the transcript is not pending",
			screen: "> fix gas-5tm1\n● I'll fix gas-5tm1 now.\n\n>",
			sent:   "fix gas-5tm1",
			want:   false,
		},
		{
			name:   "unrelated composer content is not our payload",
			screen: "● idle\n\n> some other thing the operator typed",
			sent:   "fix gas-5tm1",
			want:   false,
		},
		{
			name:   "long payload truncated in the composer still matches",
			screen: "● idle\n\n> PAUSE: stand down and st",
			sent:   "PAUSE: stand down and stop work immediately, do not claim anything",
			want:   true,
		},
		{
			name:   "blank screen probes nothing",
			screen: "",
			sent:   "fix gas-5tm1",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, observed := InputLineHoldsPending(tc.screen, tc.sent)
			if got != tc.want {
				t.Fatalf("InputLineHoldsPending() = %v, want %v (observed %q)", got, tc.want, observed)
			}
			if got && observed == "" {
				t.Fatal("pending input reported with no observation to show the operator")
			}
		})
	}
}

// Probed=false (no probe for this transport) must never be mistaken for
// Confirmed=true. Conflating them is how the contract degrades back into the
// defect it exists to catch.
func TestDeliveryConfirmationUnprobedIsNotConfirmed(t *testing.T) {
	var c DeliveryConfirmation
	if c.Confirmed {
		t.Fatal("zero-value DeliveryConfirmation must not read as confirmed")
	}
	if c.Probed {
		t.Fatal("zero-value DeliveryConfirmation must not read as probed")
	}
}
