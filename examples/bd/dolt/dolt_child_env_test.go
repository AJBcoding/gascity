package dolt_test

import (
	"slices"
	"strings"
	"testing"
)

// doltEventFlushGuardKey disables dolt's event-flush goroutine, which otherwise
// blocks subprocess exit for up to 10 minutes.
const doltEventFlushGuardKey = "DOLT_DISABLE_EVENT_FLUSH"

// doltChildEnv builds the environment for a subprocess that will run dolt,
// either directly or through a pack script.
//
// filteredEnv deliberately scrubs every GC_*/DOLT_*-prefixed key so host state
// cannot leak into a test. That scrub also removes the event-flush guard
// TestMain sets, so a child built from filteredEnv alone can block up to 10
// minutes on event flush. This re-adds exactly that one key and nothing else.
//
// scrubKeys is forwarded to filteredEnv: name the keys the caller intends to
// set explicitly afterwards, the same way filteredEnv is called directly.
func doltChildEnv(scrubKeys ...string) []string {
	return append(filteredEnv(scrubKeys...), doltEventFlushGuardKey+"=true")
}

// TestDoltChildEnvCarriesEventFlushGuard pins the contract that main_test.go's
// comment claims but filteredEnv silently breaks.
//
// TestMain sets DOLT_DISABLE_EVENT_FLUSH in the test process because a dolt
// subprocess without it can block up to 10 minutes on its event-flush
// goroutine. filteredEnv strips every DOLT_*-prefixed key on purpose
// (health_test.go, pinned by TestFilteredEnvStripsGCAndDOLTPrefixes), so the
// guard never reached a single child. The existing
// TestDoltEventFlushDisabledInTestProcess asserts only that the PARENT has it,
// so the gap went unnoticed.
//
// gas-2jc: eight concurrent compact runs sat ~7 minutes in exec.Cmd.Wait under
// load, inside that 10-minute window.
func TestDoltChildEnvCarriesEventFlushGuard(t *testing.T) {
	env := doltChildEnv()
	if !slices.Contains(env, "DOLT_DISABLE_EVENT_FLUSH=true") {
		t.Fatalf("doltChildEnv() must carry DOLT_DISABLE_EVENT_FLUSH=true; without it a dolt child can block 10 min on event flush (got %d entries)", len(env))
	}
}

// TestDoltChildEnvStillScrubsHostLeakage keeps the fix from regressing the
// scrub it builds on: re-adding the flush guard must not reopen the GC_*/DOLT_*
// host-leakage hole that TestFilteredEnvStripsGCAndDOLTPrefixes guards.
func TestDoltChildEnvStillScrubsHostLeakage(t *testing.T) {
	t.Setenv("GC_DOLT_STATE_FILE", "/host/leak/dolt-state.json")
	t.Setenv("GC_DOLT_PORT", "38676")
	t.Setenv("DOLT_CLI_PASSWORD", "host-leak")
	t.Setenv("CHILD_ENV_TEST_KEEP", "kept")

	got := doltChildEnv()

	for _, entry := range got {
		key, _, _ := strings.Cut(entry, "=")
		if key == doltEventFlushGuardKey {
			continue
		}
		if strings.HasPrefix(key, "GC_") || strings.HasPrefix(key, "DOLT_") {
			t.Errorf("doltChildEnv leaked %q; only %s may survive the scrub", entry, doltEventFlushGuardKey)
		}
	}

	if !slices.Contains(got, "CHILD_ENV_TEST_KEEP=kept") {
		t.Error("doltChildEnv dropped an unrelated non-GC_/DOLT_ entry")
	}
}

// TestDoltChildEnvHonorsExplicitScrubKeys checks that doltChildEnv forwards its
// arguments to filteredEnv, so callers can still name keys they intend to set
// themselves afterwards.
//
// The probe key deliberately carries no GC_/DOLT_ prefix. With one, the prefix
// scrub would strip it whether or not the argument was forwarded, and the test
// would pass vacuously.
func TestDoltChildEnvHonorsExplicitScrubKeys(t *testing.T) {
	t.Setenv("CHILD_ENV_TEST_DROP", "dropped")

	if !slices.Contains(doltChildEnv(), "CHILD_ENV_TEST_DROP=dropped") {
		t.Fatal("probe key was not present without scrubbing; the forwarding assertion below would be vacuous")
	}

	if slices.Contains(doltChildEnv("CHILD_ENV_TEST_DROP"), "CHILD_ENV_TEST_DROP=dropped") {
		t.Error("doltChildEnv did not forward its scrub keys to filteredEnv")
	}
}
