package dolt_test

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Prevent dolt's event-flush goroutine from blocking subprocess exit
	// for up to 10 minutes.
	//
	// This covers the test process ONLY. It does not reach subprocesses:
	// filteredEnv scrubs every GC_*/DOLT_*-prefixed key, this one included.
	// A child that runs dolt must be built with doltChildEnv, which re-adds
	// the guard after the scrub (gas-2jc).
	if err := os.Setenv("DOLT_DISABLE_EVENT_FLUSH", "true"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// TestDoltEventFlushDisabledInTestProcess pins the TestMain contract for the
// test process itself. It says nothing about subprocesses — the child-side
// contract is pinned by TestDoltChildEnvCarriesEventFlushGuard.
func TestDoltEventFlushDisabledInTestProcess(t *testing.T) {
	if os.Getenv("DOLT_DISABLE_EVENT_FLUSH") != "true" {
		t.Fatal("DOLT_DISABLE_EVENT_FLUSH not set to true by TestMain — dolt subprocesses will hang for up to 10 min on event flush")
	}
}
