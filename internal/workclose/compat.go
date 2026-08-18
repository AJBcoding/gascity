package workclose

// The bounded shipped-close compatibility mode's scope.
//
// beads.shipped_close_warn_only is a city-global SETTING, but it must never be
// a city-global RULE. It exists for exactly one deficiency — the pinned bd
// cannot revision-fence a managed close — and what it permits is far wider
// than that deficiency: a policy-invalid close (missing outcome, missing or
// inexact durable landing evidence) proceeds after telemetry. Resolving it
// against city config alone therefore strips strict enforcement from every
// natively fence-capable store in a mixed-provider city, which has no
// compatibility need whatsoever.
//
// So the decision is scoped to the target each mutation route actually
// mutates. The route resolves that target — it is the only layer that knows
// whether the write is a bd subprocess against a scope or an in-process fenced
// write against an already-opened store — and hands it here. This package
// never re-derives it.

// CloseTarget names the store a resolved managed close will actually mutate.
//
// The zero value is the natively fence-capable store, so a route that does not
// describe its target stays ENFORCED rather than silently relaxed.
type CloseTarget struct {
	// BDStoreContract reports that the resolved target is written through the
	// pinned bd store contract. A mutation route sets it from what it actually
	// writes: the resolved provider for the scope a bd subprocess mutates, or
	// the resolved store for an in-process write. It is never read off city
	// configuration, which cannot answer for a rig.
	BDStoreContract bool
}

// Enforce reports whether a resolved close blocks contract violations rather
// than warning through them. warnOnlyConfigured is the city-global bounded
// compatibility setting; it can only relax a bd-contract target.
func Enforce(warnOnlyConfigured bool, target CloseTarget) bool {
	return !warnOnlyConfigured || !target.BDStoreContract
}
