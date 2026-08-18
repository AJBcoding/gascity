package workclose

import "testing"

// TestEnforceRelaxesOnlyTheBDContractTarget is the whole rule in one table: the
// bounded compatibility setting is city-global, the decision is not.
func TestEnforceRelaxesOnlyTheBDContractTarget(t *testing.T) {
	for name, tc := range map[string]struct {
		warnOnly bool
		target   CloseTarget
		want     bool
	}{
		"setting off, bd target":       {target: CloseTarget{BDStoreContract: true}, want: true},
		"setting off, native target":   {want: true},
		"setting on, bd target":        {warnOnly: true, target: CloseTarget{BDStoreContract: true}, want: false},
		"setting on, native target":    {warnOnly: true, want: true},
		"setting on, target undecided": {warnOnly: true, target: CloseTarget{}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Enforce(tc.warnOnly, tc.target); got != tc.want {
				t.Fatalf("Enforce(%v, %+v) = %v, want %v", tc.warnOnly, tc.target, got, tc.want)
			}
		})
	}
}

// TestZeroCloseTargetIsEnforced pins the fail direction structurally: a route
// that forgets to describe its target passes the zero value, and the zero value
// must never be the relaxed answer.
func TestZeroCloseTargetIsEnforced(t *testing.T) {
	var forgotten CloseTarget
	if !Enforce(true, forgotten) {
		t.Fatal("the zero CloseTarget was relaxed; an undescribed target must stay enforced")
	}
}
