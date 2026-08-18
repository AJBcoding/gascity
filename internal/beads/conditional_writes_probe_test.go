package beads

import (
	"strings"
	"testing"
)

// probeSupportStore is a minimal wrapper that hides every optional capability
// behind the embedded Store interface while declaring a resolve target —
// modeling the cmd/gc policy wrapper, whose interface embedding would
// otherwise make a capable inner store look incapable.
type probeSupportTargeter struct {
	Store
	target Store
}

func (w probeSupportTargeter) ConditionalWritesResolveTarget() Store { return w.target }

// probeSupportOpaque hides every optional capability and declares nothing.
type probeSupportOpaque struct {
	Store
}

func TestProbeConditionalWriteSupportRunsBdCapabilityProbe(t *testing.T) {
	helps := 0
	capable := NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		helps++
		return []byte("Flags:\n      --if-revision int   fence\n"), nil
	})
	ok, reason := ProbeConditionalWriteSupport(capable)
	if !ok || reason != "" {
		t.Fatalf("capable bd = (%v, %q), want (true, \"\")", ok, reason)
	}
	if helps != len(conditionalWriteProbeVerbs) {
		t.Fatalf("probe ran %d bd --help subprocesses, want %d", helps, len(conditionalWriteProbeVerbs))
	}

	old := NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		return []byte("Flags:\n      --json   machine output\n"), nil
	})
	ok, reason = ProbeConditionalWriteSupport(old)
	if ok || !strings.Contains(reason, conditionalWriteFlag) {
		t.Fatalf("pre-fence bd = (%v, %q), want incapable naming %s", ok, reason, conditionalWriteFlag)
	}
}

func TestProbeConditionalWriteSupportHonorsRuntimeLatch(t *testing.T) {
	store := NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		return []byte(conditionalWriteFlag), nil
	})
	store.markConditionalWritesUnsupported()
	ok, reason := ProbeConditionalWriteSupport(store)
	if ok || !strings.Contains(reason, "latched") {
		t.Fatalf("latched bd = (%v, %q), want incapable naming the latch", ok, reason)
	}
}

func TestProbeConditionalWriteSupportOnNativeCapableStores(t *testing.T) {
	if ok, reason := ProbeConditionalWriteSupport(NewMemStore()); !ok || reason != "" {
		t.Fatalf("MemStore = (%v, %q), want (true, \"\")", ok, reason)
	}
	disabled := NewMemStore()
	disabled.DisableConditionalWrites = true
	if ok, _ := ProbeConditionalWriteSupport(disabled); ok {
		t.Fatal("MemStore with DisableConditionalWrites reported capable")
	}
}

func TestProbeConditionalWriteSupportFollowsResolveTargets(t *testing.T) {
	inner := NewMemStore()
	ok, reason := ProbeConditionalWriteSupport(probeSupportTargeter{Store: inner, target: inner})
	if !ok || reason != "" {
		t.Fatalf("wrapped capable store = (%v, %q), want (true, \"\")", ok, reason)
	}
}

func TestProbeConditionalWriteSupportFailsClosedWithoutWriter(t *testing.T) {
	if ok, reason := ProbeConditionalWriteSupport(probeSupportOpaque{Store: NewMemStore()}); ok || reason == "" {
		t.Fatalf("capability-hiding wrapper = (%v, %q), want incapable with a reason", ok, reason)
	}
	if ok, reason := ProbeConditionalWriteSupport(nil); ok || reason == "" {
		t.Fatalf("nil store = (%v, %q), want incapable with a reason", ok, reason)
	}
}
