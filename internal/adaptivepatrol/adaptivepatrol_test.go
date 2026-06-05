package adaptivepatrol

import (
	"testing"
	"time"
)

func TestControllerStartsAtBase(t *testing.T) {
	c := New(30*time.Second, 5, 8)
	if got, want := c.Current(), 30*time.Second; got != want {
		t.Errorf("Current() = %v, want %v", got, want)
	}
}

func TestNoOpsBelowThresholdDoNotChangeInterval(t *testing.T) {
	c := New(30*time.Second, 5, 8)
	for i := 0; i < 4; i++ {
		newInt, changed := c.OnPatrolTick()
		if changed {
			t.Fatalf("tick %d: changed unexpectedly to %v", i, newInt)
		}
		if newInt != 30*time.Second {
			t.Fatalf("tick %d: Current=%v, want 30s", i, newInt)
		}
	}
}

func TestDoublesAtThreshold(t *testing.T) {
	c := New(30*time.Second, 5, 8)
	for i := 0; i < 4; i++ {
		if _, changed := c.OnPatrolTick(); changed {
			t.Fatalf("tick %d changed prematurely", i)
		}
	}
	newInt, changed := c.OnPatrolTick()
	if !changed {
		t.Fatalf("threshold tick did not change interval")
	}
	if want := 60 * time.Second; newInt != want {
		t.Errorf("after threshold: interval = %v, want %v", newInt, want)
	}
}

func TestDoublesUntilCeiling(t *testing.T) {
	base := 30 * time.Second
	c := New(base, 1, 8)
	wantSequence := []time.Duration{
		2 * base,
		4 * base,
		8 * base,
		8 * base,
		8 * base,
	}
	for i, want := range wantSequence {
		newInt, changed := c.OnPatrolTick()
		if i < 3 {
			if !changed {
				t.Errorf("step %d: expected change, got none (Current=%v)", i, newInt)
			}
		} else {
			if changed {
				t.Errorf("step %d: ceiling reached but changed=true (newInt=%v)", i, newInt)
			}
		}
		if newInt != want {
			t.Errorf("step %d: interval = %v, want %v", i, newInt, want)
		}
	}
}

func TestOnEventResetsToBase(t *testing.T) {
	base := 30 * time.Second
	c := New(base, 1, 8)
	for i := 0; i < 5; i++ {
		c.OnPatrolTick()
	}
	if c.Current() != 8*base {
		t.Fatalf("setup: Current=%v, want %v", c.Current(), 8*base)
	}
	newInt, changed := c.OnEvent()
	if !changed {
		t.Fatalf("OnEvent did not signal change after back-off")
	}
	if newInt != base {
		t.Errorf("OnEvent: interval = %v, want %v", newInt, base)
	}
}

func TestOnEventAtBaseIsNoOp(t *testing.T) {
	base := 30 * time.Second
	c := New(base, 5, 8)
	newInt, changed := c.OnEvent()
	if changed {
		t.Errorf("OnEvent at base reported change=true")
	}
	if newInt != base {
		t.Errorf("OnEvent at base: interval = %v, want %v", newInt, base)
	}
}

func TestEventResetsNoOpCounter(t *testing.T) {
	base := 30 * time.Second
	c := New(base, 5, 8)
	for i := 0; i < 4; i++ {
		c.OnPatrolTick()
	}
	c.OnEvent()
	for i := 0; i < 4; i++ {
		_, changed := c.OnPatrolTick()
		if changed {
			t.Fatalf("after event reset, tick %d changed prematurely", i)
		}
	}
	_, changed := c.OnPatrolTick()
	if !changed {
		t.Errorf("5th tick after reset did not trip back-off")
	}
}

func TestDegenerateValuesUseSafeDefaults(t *testing.T) {
	c := New(30*time.Second, 0, 8)
	for i := 0; i < 100; i++ {
		_, changed := c.OnPatrolTick()
		if changed {
			t.Fatalf("threshold=0: tick %d changed", i)
		}
	}
	if got, want := c.Current(), 30*time.Second; got != want {
		t.Errorf("threshold=0 Current = %v, want %v", got, want)
	}
	c2 := New(30*time.Second, 1, 1)
	for i := 0; i < 5; i++ {
		_, changed := c2.OnPatrolTick()
		if changed {
			t.Fatalf("maxMultiplier=1: tick %d changed", i)
		}
	}
}
