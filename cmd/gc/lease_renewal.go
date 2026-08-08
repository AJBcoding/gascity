package main

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// leaseRenewalInterval derives the lease-renewal cadence from the configured
// claim-lease TTL: renew at one third of the TTL, so a renewal can miss twice
// (a slow tick, a transient bd error) before the lease lapses. Deriving the
// cadence from the TTL is the gas-76r invariant — the two values cannot drift
// apart the way the prompt-driven ~16-22 minute cadence drifted against bd's
// 5-minute window. A zero or negative TTL degenerates to renewing on every
// pass: with no meaningful window to pace against, renewing too often is safe
// and renewing never is the bug this exists to fix.
func leaseRenewalInterval(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	return ttl / 3
}

// leaseSweepTarget names a bead store for lease-renewal sweep logging.
type leaseSweepTarget struct {
	label string
	store beads.Store
}

// sweepClaimLeaseRenewals renews the claim lease of every in_progress bead
// whose assignee is a running session, across the given stores. Stores without
// lease semantics are skipped silently (capability absence, not failure); a
// renewal that fails for one bead is logged and does not stop the sweep. Beads
// whose holder is NOT running are deliberately left alone: their lease going
// stale is what lets bd reclaim return a dead worker's bead to ready.
func sweepClaimLeaseRenewals(targets []leaseSweepTarget, running map[string]bool, stderr io.Writer, logPrefix string) int {
	renewed := 0
	for _, target := range targets {
		renewer, ok := target.store.(beads.LeaseRenewer)
		if !ok {
			continue
		}
		held, err := target.store.ListOpen("in_progress")
		if err != nil {
			fmt.Fprintf(stderr, "%s: lease renewal: %s: listing in_progress beads: %v\n", logPrefix, target.label, err) //nolint:errcheck // best-effort stderr
			continue
		}
		for _, b := range held {
			assignee := strings.TrimSpace(b.Assignee)
			if assignee == "" || !running[assignee] {
				continue
			}
			if err := renewer.RenewLease(b.ID, assignee); err != nil {
				if errors.Is(err, beads.ErrLeaseRenewalUnsupported) {
					// A lease-capable wrapper over a lease-less backing store
					// (e.g. CachingStore over the native store): same as the
					// store not implementing LeaseRenewer at all.
					break
				}
				fmt.Fprintf(stderr, "%s: lease renewal: %s: %s: %v\n", logPrefix, target.label, b.ID, err) //nolint:errcheck // best-effort stderr
				continue
			}
			renewed++
		}
	}
	return renewed
}

// runLeaseRenewalWatchdog keeps the claim leases of beads held by live
// sessions continuously valid (gas-76r). It runs from the reconciler tick but
// self-gates to the cadence derived from the configured claim-lease TTL, so
// heartbeat writes stay a small fraction of the TTL regardless of tick rate.
// Only sessions with a live runtime (StateActive) count as holders; renewals
// stop the moment the reconciler stops seeing the session alive, which is
// exactly when the lease SHOULD lapse so bd reclaim can recover the bead.
func (cr *CityRuntime) runLeaseRenewalWatchdog(now time.Time, sessionBeads *sessionBeadSnapshot) int {
	interval := leaseRenewalInterval(cr.cfg.Daemon.ClaimLeaseTTLDuration())
	if !cr.leaseRenewalWatchdogLast.IsZero() && now.Sub(cr.leaseRenewalWatchdogLast) < interval {
		return 0
	}
	cr.leaseRenewalWatchdogLast = now

	running := map[string]bool{}
	for _, info := range sessionBeads.OpenInfos() {
		if info.State != sessionpkg.StateActive {
			continue
		}
		if name := strings.TrimSpace(info.SessionName); name != "" {
			running[name] = true
		}
	}
	if len(running) == 0 {
		return 0
	}

	var targets []leaseSweepTarget
	if store := cr.cityBeadStore(); store != nil {
		targets = append(targets, leaseSweepTarget{label: "city", store: store})
	}
	rigStores := cr.rigBeadStores()
	names := make([]string, 0, len(rigStores))
	for name := range rigStores {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if rigStores[name] == nil {
			continue
		}
		targets = append(targets, leaseSweepTarget{label: name, store: rigStores[name]})
	}
	return sweepClaimLeaseRenewals(targets, running, cr.stderr, cr.logPrefix)
}
