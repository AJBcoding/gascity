// Package adaptivepatrol implements opt-in patrol-interval back-off for
// the city reconciler. The reconciler patrols every patrol_interval to
// reconcile desired vs actual state. When the fleet is stable for many
// consecutive ticks, the patrol still scans the world every interval,
// burning CPU and bd query load with nothing to do.
//
// The Controller in this package is a pure state machine: callers feed
// it OnPatrolTick (a patrol-driven tick that observed no state change)
// and OnEvent (any state-change signal — new bead, agent transition,
// sling event). The controller doubles the interval up to a ceiling
// after a configurable number of consecutive no-op ticks, and snaps
// back to base on any event. Callers ticker.Reset only when the
// returned changed flag is true.
//
// This package contains no I/O and no references to live system state.
// All decisions are arithmetic over base, threshold, and ceiling — ZFC
// compliant by construction.
package adaptivepatrol

import (
	"sync"
	"time"
)

// Controller adapts a fixed patrol interval based on observed activity.
// Construct with New; call OnPatrolTick from the patrol path and OnEvent
// from each state-change channel. All methods are safe for concurrent use.
type Controller struct {
	mu               sync.Mutex
	base             time.Duration
	maxMultiplier    int
	idleThreshold    int
	current          time.Duration
	consecutiveNoOps int
}

// New returns a Controller pinned at base. idleThreshold is the number
// of consecutive no-op patrol ticks required before doubling the
// current interval. maxMultiplier is the ceiling expressed as a
// multiple of base (the controller never exceeds base * maxMultiplier).
//
// Degenerate values disable adaptation:
//   - idleThreshold <= 0: OnPatrolTick is a no-op (current pinned to base).
//   - maxMultiplier <= 1: ceiling equals base; OnPatrolTick never doubles.
func New(base time.Duration, idleThreshold, maxMultiplier int) *Controller {
	return &Controller{
		base:          base,
		maxMultiplier: maxMultiplier,
		idleThreshold: idleThreshold,
		current:       base,
	}
}

// Current returns the controller's current interval.
func (c *Controller) Current() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// OnPatrolTick records a patrol-driven tick that observed no state
// change. Returns the resulting interval and a flag indicating whether
// it changed from the previous interval. The caller should ticker.Reset
// only when changed is true.
func (c *Controller) OnPatrolTick() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.idleThreshold <= 0 || c.maxMultiplier <= 1 {
		return c.current, false
	}
	c.consecutiveNoOps++
	if c.consecutiveNoOps < c.idleThreshold {
		return c.current, false
	}
	c.consecutiveNoOps = 0
	ceiling := time.Duration(c.maxMultiplier) * c.base
	if c.current >= ceiling {
		return c.current, false
	}
	next := c.current * 2
	if next > ceiling {
		next = ceiling
	}
	if next == c.current {
		return c.current, false
	}
	c.current = next
	return c.current, true
}

// OnEvent records that a state change reset the patrol back-off.
// Returns the resulting interval and whether it changed from the
// previous value. The caller should ticker.Reset only when changed is
// true. OnEvent always resets the no-op counter, even when the
// interval was already at base, so subsequent ticks restart from zero.
func (c *Controller) OnEvent() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutiveNoOps = 0
	if c.current == c.base {
		return c.current, false
	}
	c.current = c.base
	return c.current, true
}
