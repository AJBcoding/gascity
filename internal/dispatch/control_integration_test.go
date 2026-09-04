package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/formula"
	"github.com/gastownhall/gascity/internal/session"
)

// ---------------------------------------------------------------------------
// Integration tests: full retry/ralph lifecycle through processRetryControl
// and processRalphControl, including molecule.Attach for spawning attempts.
// ---------------------------------------------------------------------------

// makeRetryControl creates a workflow root + retry control bead with frozen step spec.
func makeRetryControl(t *testing.T, store beads.Store, stepRef string, spec *formula.Step, maxAttempts int) (root, control beads.Bead) {
	t.Helper()
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal step spec: %v", err)
	}
	root = mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control = mustCreate(t, store, beads.Bead{
		Title: spec.Title + " (retry)",
		Metadata: map[string]string{
			"gc.kind":             "retry",
			"gc.root_bead_id":     root.ID,
			"gc.step_ref":         stepRef,
			"gc.step_id":          spec.ID,
			"gc.max_attempts":     strconv.Itoa(maxAttempts),
			"gc.on_exhausted":     "hard_fail",
			"gc.source_step_spec": string(specJSON),
			"gc.control_epoch":    "1",
		},
	})
	return
}

// makeAttemptBead creates and closes an attempt bead with the given outcome metadata.
func makeAttemptBead(t *testing.T, store beads.Store, rootID, stepRef string, attemptNum int, meta map[string]string) beads.Bead {
	t.Helper()
	baseMeta := map[string]string{
		"gc.root_bead_id": rootID,
		"gc.step_ref":     stepRef,
		"gc.attempt":      strconv.Itoa(attemptNum),
	}
	for k, v := range meta {
		baseMeta[k] = v
	}
	b := mustCreate(t, store, beads.Bead{
		Title:    "attempt " + strconv.Itoa(attemptNum),
		Metadata: baseMeta,
	})
	mustClose(t, store, b.ID)
	return b
}

// TestRetryLifecycleTransientThenPass exercises the full lifecycle:
// attempt 1 fails transient → processRetryControl spawns attempt 2 via Attach →
// attempt 2 passes → processRetryControl closes control as pass.
func TestRetryLifecycleTransientThenPass(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()

	spec := &formula.Step{
		ID:    "review",
		Title: "Review",
		Type:  "task",
		Retry: &formula.RetrySpec{MaxAttempts: 3},
	}
	root, control := makeRetryControl(t, store, "mol-test.review", spec, 3)

	// --- Attempt 1: transient failure ---
	attempt1 := makeAttemptBead(t, store, root.ID, "mol-test.review.attempt.1", 1, map[string]string{
		"gc.outcome":        "fail",
		"gc.failure_class":  "transient",
		"gc.failure_reason": "rate_limited",
	})
	mustDep(t, store, control.ID, attempt1.ID, "blocks")

	result, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("processRetryControl attempt 1: %v", err)
	}
	if result.Action != "retry" {
		t.Fatalf("attempt 1 action = %q, want retry", result.Action)
	}

	// Control should still be open, epoch advanced.
	controlAfter1 := mustGet(t, store, control.ID)
	if controlAfter1.Status != "open" {
		t.Fatalf("control status after attempt 1 = %q, want open", controlAfter1.Status)
	}
	if controlAfter1.Metadata["gc.control_epoch"] != "2" {
		t.Fatalf("epoch after attempt 1 = %q, want 2", controlAfter1.Metadata["gc.control_epoch"])
	}

	// Attach should have created attempt 2 beads in the store.
	// Find attempt 2 by step_ref pattern.
	attempt2 := findAttemptByRef(t, store, root.ID, "mol-test.review.attempt.2")
	if attempt2.ID == "" {
		t.Fatal("attempt 2 was not created by Attach")
	}
	if attempt2.Metadata["gc.attempt"] != "2" {
		t.Fatalf("attempt 2 gc.attempt = %q, want 2", attempt2.Metadata["gc.attempt"])
	}
	if attempt2.Metadata["gc.root_bead_id"] != root.ID {
		t.Fatalf("attempt 2 gc.root_bead_id = %q, want %q", attempt2.Metadata["gc.root_bead_id"], root.ID)
	}

	// --- Attempt 2: pass ---
	// Close attempt 2 with pass outcome.
	if err := store.SetMetadataBatch(attempt2.ID, map[string]string{
		"gc.outcome":     "pass",
		"gc.output_json": `{"verdict":"approved"}`,
	}); err != nil {
		t.Fatalf("set attempt 2 metadata: %v", err)
	}
	mustClose(t, store, attempt2.ID)

	// Process control again — should see attempt 2 passed.
	result2, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("processRetryControl attempt 2: %v", err)
	}
	if result2.Action != "pass" {
		t.Fatalf("attempt 2 action = %q, want pass", result2.Action)
	}

	// Control should be closed with pass outcome and propagated output.
	controlFinal := mustGet(t, store, control.ID)
	if controlFinal.Status != "closed" {
		t.Fatalf("control final status = %q, want closed", controlFinal.Status)
	}
	if controlFinal.Metadata["gc.outcome"] != "pass" {
		t.Fatalf("control outcome = %q, want pass", controlFinal.Metadata["gc.outcome"])
	}
	if controlFinal.Metadata["gc.output_json"] != `{"verdict":"approved"}` {
		t.Fatalf("control output_json = %q, want propagated", controlFinal.Metadata["gc.output_json"])
	}

	// Attempt log should have 2 entries.
	var log []map[string]string
	if err := json.Unmarshal([]byte(controlFinal.Metadata["gc.attempt_log"]), &log); err != nil {
		t.Fatalf("unmarshal attempt_log: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("attempt_log entries = %d, want 2", len(log))
	}
	if log[0]["outcome"] != "transient" || log[0]["action"] != "retry" {
		t.Errorf("log[0] = %v, want transient/retry", log[0])
	}
	if log[1]["outcome"] != "pass" || log[1]["action"] != "close" {
		t.Errorf("log[1] = %v, want pass/close", log[1])
	}
}

// TestProcessControlOptInRalphExitDispositionIntegration catches the public
// controller entrypoint losing routing for any distinct result class or
// reporting a created attempt outside an executed continue exit. The complete
// numeric matrix belongs to TestClassifyRalphCheckDisposition.
func TestProcessControlOptInRalphExitDispositionIntegration(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		timeout     string
		wantAction  string
		wantErr     error
		wantCreated int
	}{
		{name: "pass", script: "#!/bin/sh\nexit 0\n", timeout: "30s", wantAction: "pass"},
		{name: "continue", script: "#!/bin/sh\nexit 1\n", timeout: "30s", wantAction: "retry", wantCreated: 1},
		{name: "pending exit", script: "#!/bin/sh\nexit 20\n", timeout: "30s", wantErr: ErrControlPending},
		{name: "hard exit", script: "#!/bin/sh\nexit 10\n", timeout: "30s", wantAction: "hard-fail"},
		{name: "hard signal", script: "#!/bin/sh\nkill -TERM $$\n", timeout: "30s", wantAction: "hard-fail"},
		{name: "gate error", script: "#!/definitely/missing/interpreter\nexit 0\n", timeout: "30s", wantErr: ErrControlPending},
		{name: "gate timeout", script: "#!/bin/sh\nwhile :; do :; done\n", timeout: "10s", wantErr: ErrControlPending},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRalphCheckFixture(t, `[1]`, `[20,24]`, "", tc.script, tc.timeout, 3)
			result, err := ProcessControl(fixture.store, mustGet(t, fixture.store, fixture.control.ID), fixture.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ProcessControl err = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("ProcessControl: %v", err)
			}
			if result.Action != tc.wantAction || result.Created != tc.wantCreated {
				t.Fatalf("result = %+v, want action=%q created=%d", result, tc.wantAction, tc.wantCreated)
			}
		})
	}
}

// TestProcessControlNestedRalphAbortHardFailsOuterBeforeCheck catches an inner
// abort_scope failure being reinterpreted as an outer numeric continuation.
func TestProcessControlNestedRalphAbortHardFailsOuterBeforeCheck(t *testing.T) {
	t.Parallel()
	fixture := newRalphCheckFixture(t, `[1]`, `[20,24]`, "", "#!/bin/sh\nexit 1\n", "30s", 3)
	open := "open"
	if err := fixture.store.Update(fixture.iteration.ID, beads.UpdateOpts{
		Status: &open,
		Metadata: map[string]string{
			beadmeta.ScopeRoleMetadataKey: beadmeta.ScopeRoleBody,
		},
	}); err != nil {
		t.Fatalf("reopen outer iteration scope: %v", err)
	}

	innerControl := mustCreate(t, fixture.store, beads.Bead{
		Title: "inner ralph",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:        beadmeta.KindRalph,
			beadmeta.RootBeadIDMetadataKey:  fixture.root.ID,
			beadmeta.StepRefMetadataKey:     "mol-test.review-loop.iteration.1.inner",
			beadmeta.StepIDMetadataKey:      "inner",
			beadmeta.ScopeRefMetadataKey:    "mol-test.review-loop.iteration.1",
			beadmeta.ScopeRoleMetadataKey:   beadmeta.ScopeRoleMember,
			beadmeta.OnFailMetadataKey:      "abort_scope",
			beadmeta.MaxAttemptsMetadataKey: "1",
		},
	})
	innerIteration := mustCreate(t, fixture.store, beads.Bead{
		Title: "inner iteration 1",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:        beadmeta.KindScope,
			beadmeta.RootBeadIDMetadataKey:  fixture.root.ID,
			beadmeta.StepRefMetadataKey:     "mol-test.review-loop.iteration.1.inner.iteration.1",
			beadmeta.StepIDMetadataKey:      "inner",
			beadmeta.RalphStepIDMetadataKey: "inner",
			beadmeta.AttemptMetadataKey:     "1",
			beadmeta.OutcomeMetadataKey:     beadmeta.OutcomeFail,
		},
	})
	mustClose(t, fixture.store, innerIteration.ID)
	mustDep(t, fixture.store, innerControl.ID, innerIteration.ID, "blocks")
	mustDep(t, fixture.store, fixture.iteration.ID, innerControl.ID, "blocks")

	innerResult, err := ProcessControl(fixture.store, mustGet(t, fixture.store, innerControl.ID), fixture.opts)
	if err != nil {
		t.Fatalf("ProcessControl inner: %v", err)
	}
	if innerResult.Action != "fail" {
		t.Fatalf("inner result = %+v, want exhausted fail", innerResult)
	}
	outerSubject := mustGet(t, fixture.store, fixture.iteration.ID)
	if outerSubject.Status != "closed" || outerSubject.Metadata[beadmeta.OutcomeMetadataKey] != beadmeta.OutcomeFail {
		t.Fatalf("outer subject = %q/%q, want abort_scope closed/fail", outerSubject.Status, outerSubject.Metadata[beadmeta.OutcomeMetadataKey])
	}
	beforeOuterCount := ralphFixtureBeadCount(t, fixture.store)

	outerResult, err := ProcessControl(fixture.store, mustGet(t, fixture.store, fixture.control.ID), fixture.opts)
	if err != nil {
		t.Fatalf("ProcessControl outer: %v", err)
	}
	if outerResult.Action != "hard-fail" || outerResult.Created != 0 {
		t.Fatalf("outer result = %+v, want pre-check hard-fail without spawn", outerResult)
	}
	if _, err := os.Stat(fixture.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outer check invocation marker err = %v, want not-exist", err)
	}
	if got := mustGet(t, fixture.store, fixture.control.ID).Metadata[beadmeta.FailureReasonMetadataKey]; got != "ralph_subject_failed_before_check" {
		t.Fatalf("outer failure reason = %q, want ralph_subject_failed_before_check", got)
	}
	if got := ralphFixtureBeadCount(t, fixture.store); got != beforeOuterCount {
		t.Fatalf("outer bead count = %d, want unchanged %d", got, beforeOuterCount)
	}
	if next := findAttemptByRef(t, fixture.store, fixture.root.ID, "mol-test.review-loop.iteration.2"); next.ID != "" {
		t.Fatalf("unexpected outer iteration 2: %+v", next)
	}
}

func TestRetryLifecycleRequiredOutputMissingDoesNotPass(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()

	spec := &formula.Step{
		ID:    "prepare-items",
		Title: "Prepare Items",
		Type:  "task",
		Retry: &formula.RetrySpec{MaxAttempts: 2},
	}
	root, control := makeRetryControl(t, store, "mol-test.prepare-items", spec, 2)

	attempt1 := makeAttemptBead(t, store, root.ID, "mol-test.prepare-items.attempt.1", 1, map[string]string{
		"gc.outcome":              "pass",
		"gc.output_json_required": "true",
	})
	mustDep(t, store, control.ID, attempt1.ID, "blocks")

	result, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("processRetryControl attempt 1: %v", err)
	}
	if result.Action != "retry" {
		t.Fatalf("attempt 1 action = %q, want retry", result.Action)
	}

	attempt2 := findAttemptByRef(t, store, root.ID, "mol-test.prepare-items.attempt.2")
	if attempt2.ID == "" {
		t.Fatal("attempt 2 was not created by Attach")
	}
	if err := store.SetMetadataBatch(attempt2.ID, map[string]string{
		"gc.outcome":              "pass",
		"gc.output_json_required": "true",
	}); err != nil {
		t.Fatalf("set attempt 2 metadata: %v", err)
	}
	mustClose(t, store, attempt2.ID)

	result2, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("processRetryControl attempt 2: %v", err)
	}
	if result2.Action != "fail" {
		t.Fatalf("attempt 2 action = %q, want fail", result2.Action)
	}

	controlFinal := mustGet(t, store, control.ID)
	if controlFinal.Status != "closed" {
		t.Fatalf("control final status = %q, want closed", controlFinal.Status)
	}
	if controlFinal.Metadata["gc.outcome"] != "fail" {
		t.Fatalf("control outcome = %q, want fail", controlFinal.Metadata["gc.outcome"])
	}
	if controlFinal.Metadata["gc.failure_reason"] != "missing_required_output_json" {
		t.Fatalf("control failure_reason = %q, want missing_required_output_json", controlFinal.Metadata["gc.failure_reason"])
	}
	if controlFinal.Metadata["gc.output_json"] != "" {
		t.Fatalf("control output_json = %q, want empty", controlFinal.Metadata["gc.output_json"])
	}
}

// TestRetryLifecycleExhaustion exercises: attempt 1 fails → retry →
// attempt 2 fails → retry → attempt 3 fails → exhausted (hard_fail).
func TestRetryLifecycleExhaustion(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()

	spec := &formula.Step{
		ID:    "deploy",
		Title: "Deploy",
		Type:  "task",
		Retry: &formula.RetrySpec{MaxAttempts: 3},
	}
	root, control := makeRetryControl(t, store, "mol-test.deploy", spec, 3)

	// --- Attempt 1: transient ---
	attempt1 := makeAttemptBead(t, store, root.ID, "mol-test.deploy.attempt.1", 1, map[string]string{
		"gc.outcome":        "fail",
		"gc.failure_class":  "transient",
		"gc.failure_reason": "timeout",
	})
	mustDep(t, store, control.ID, attempt1.ID, "blocks")

	result1, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	if result1.Action != "retry" {
		t.Fatalf("round 1 action = %q, want retry", result1.Action)
	}

	// --- Attempt 2: transient ---
	attempt2 := findAttemptByRef(t, store, root.ID, "mol-test.deploy.attempt.2")
	if err := store.SetMetadataBatch(attempt2.ID, map[string]string{
		"gc.outcome":        "fail",
		"gc.failure_class":  "transient",
		"gc.failure_reason": "network_error",
	}); err != nil {
		t.Fatalf("set attempt 2 meta: %v", err)
	}
	mustClose(t, store, attempt2.ID)

	result2, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	if result2.Action != "retry" {
		t.Fatalf("round 2 action = %q, want retry", result2.Action)
	}

	// --- Attempt 3: transient (exhausted) ---
	attempt3 := findAttemptByRef(t, store, root.ID, "mol-test.deploy.attempt.3")
	if err := store.SetMetadataBatch(attempt3.ID, map[string]string{
		"gc.outcome":        "fail",
		"gc.failure_class":  "transient",
		"gc.failure_reason": "still_broken",
	}); err != nil {
		t.Fatalf("set attempt 3 meta: %v", err)
	}
	mustClose(t, store, attempt3.ID)

	result3, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("round 3: %v", err)
	}
	if result3.Action != "fail" {
		t.Fatalf("round 3 action = %q, want fail (exhausted)", result3.Action)
	}

	// Control should be closed with fail + hard_fail disposition.
	controlFinal := mustGet(t, store, control.ID)
	if controlFinal.Status != "closed" {
		t.Fatalf("control status = %q, want closed", controlFinal.Status)
	}
	if controlFinal.Metadata["gc.outcome"] != "fail" {
		t.Fatalf("control outcome = %q, want fail", controlFinal.Metadata["gc.outcome"])
	}
	if controlFinal.Metadata["gc.final_disposition"] != "hard_fail" {
		t.Fatalf("disposition = %q, want hard_fail", controlFinal.Metadata["gc.final_disposition"])
	}

	// Attempt log should have 3 entries.
	var log []map[string]string
	if err := json.Unmarshal([]byte(controlFinal.Metadata["gc.attempt_log"]), &log); err != nil {
		t.Fatalf("unmarshal attempt_log: %v", err)
	}
	if len(log) != 3 {
		t.Fatalf("attempt_log entries = %d, want 3", len(log))
	}
}

// TestRetryLifecycleEpochAdvancesPerAttempt verifies that each retry
// Attach increments the epoch, preventing stale controllers from acting.
func TestRetryLifecycleEpochAdvancesPerAttempt(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()

	spec := &formula.Step{
		ID:    "build",
		Title: "Build",
		Type:  "task",
		Retry: &formula.RetrySpec{MaxAttempts: 5},
	}
	root, control := makeRetryControl(t, store, "mol-test.build", spec, 5)

	// Create and fail 3 attempts, checking epoch each time.
	for i := 1; i <= 3; i++ {
		ref := "mol-test.build.attempt." + strconv.Itoa(i)
		if i == 1 {
			// First attempt created manually (compiler would normally do this).
			attempt := makeAttemptBead(t, store, root.ID, ref, i, map[string]string{
				"gc.outcome":        "fail",
				"gc.failure_class":  "transient",
				"gc.failure_reason": "flaky",
			})
			mustDep(t, store, control.ID, attempt.ID, "blocks")
		} else {
			// Subsequent attempts created by Attach — find and fail them.
			attempt := findAttemptByRef(t, store, root.ID, ref)
			if attempt.ID == "" {
				t.Fatalf("attempt %d not found", i)
			}
			if err := store.SetMetadataBatch(attempt.ID, map[string]string{
				"gc.outcome":        "fail",
				"gc.failure_class":  "transient",
				"gc.failure_reason": "flaky",
			}); err != nil {
				t.Fatalf("set attempt %d meta: %v", i, err)
			}
			mustClose(t, store, attempt.ID)
		}

		_, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
		if err != nil {
			t.Fatalf("processRetryControl round %d: %v", i, err)
		}

		expectedEpoch := strconv.Itoa(i + 1)
		actual := mustGet(t, store, control.ID).Metadata["gc.control_epoch"]
		if actual != expectedEpoch {
			t.Fatalf("epoch after round %d = %q, want %q", i, actual, expectedEpoch)
		}
	}
}

// TestBuildAttemptRecipeEnrichesNestedRetryChildren verifies that
// buildAttemptRecipe propagates gc.kind, gc.source_step_spec,
// gc.control_epoch, gc.max_attempts for nested retry children.
func TestBuildAttemptRecipeEnrichesNestedRetryChildren(t *testing.T) {
	t.Parallel()

	step := &formula.Step{
		ID:    "self-review",
		Title: "Self Review",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 5},
		Children: []*formula.Step{
			{
				ID:    "review-code",
				Title: "Review Code",
				Type:  "task",
				Retry: &formula.RetrySpec{MaxAttempts: 3, OnExhausted: "soft_fail"},
			},
			{
				ID:    "apply-fixes",
				Title: "Apply Fixes",
				Type:  "task",
				Needs: []string{"review-code"},
			},
		},
	}

	control := beads.Bead{
		ID: "gc-1",
		Metadata: map[string]string{
			"gc.step_id":  "self-review",
			"gc.step_ref": "mol-demo.self-review",
		},
	}

	recipe := buildAttemptRecipe(step, control, 2)

	// Should have 6 steps: scope root + 2 children + 1 spec bead + 2 scope-checks.
	if len(recipe.Steps) != 6 {
		t.Fatalf("steps = %d, want 6", len(recipe.Steps))
	}

	// Find the review-code child step.
	var reviewStep *formula.RecipeStep
	var specStep *formula.RecipeStep
	var reviewScopeCheck *formula.RecipeStep
	var applyScopeCheck *formula.RecipeStep
	for i := range recipe.Steps {
		if recipe.Steps[i].ID == "mol-demo.self-review.iteration.2.review-code" {
			reviewStep = &recipe.Steps[i]
		}
		if recipe.Steps[i].ID == "mol-demo.self-review.iteration.2.review-code.spec" {
			specStep = &recipe.Steps[i]
		}
		if recipe.Steps[i].ID == "mol-demo.self-review.iteration.2.review-code-scope-check" {
			reviewScopeCheck = &recipe.Steps[i]
		}
		if recipe.Steps[i].ID == "mol-demo.self-review.iteration.2.apply-fixes-scope-check" {
			applyScopeCheck = &recipe.Steps[i]
		}
	}
	if reviewStep == nil {
		t.Fatal("review-code child step not found in recipe")
	}

	// Should have retry-specific metadata.
	if reviewStep.Metadata["gc.kind"] != "retry" {
		t.Errorf("review-code gc.kind = %q, want retry", reviewStep.Metadata["gc.kind"])
	}
	if reviewStep.Metadata["gc.max_attempts"] != "3" {
		t.Errorf("review-code gc.max_attempts = %q, want 3", reviewStep.Metadata["gc.max_attempts"])
	}
	if reviewStep.Metadata["gc.control_epoch"] != "1" {
		t.Errorf("review-code gc.control_epoch = %q, want 1", reviewStep.Metadata["gc.control_epoch"])
	}
	if reviewStep.Metadata["gc.on_exhausted"] != "soft_fail" {
		t.Errorf("review-code gc.on_exhausted = %q, want soft_fail", reviewStep.Metadata["gc.on_exhausted"])
	}

	// Frozen step spec stored as a separate spec bead.
	if specStep == nil {
		t.Fatal("review-code.spec bead not found in recipe")
	}
	if reviewScopeCheck == nil {
		t.Fatal("review-code scope-check bead not found in recipe")
	}
	if applyScopeCheck == nil {
		t.Fatal("apply-fixes scope-check bead not found in recipe")
	}
	if specStep.Metadata["gc.kind"] != "spec" {
		t.Errorf("spec gc.kind = %q, want spec", specStep.Metadata["gc.kind"])
	}
	if reviewScopeCheck.Metadata["gc.kind"] != "scope-check" {
		t.Errorf("review-code scope-check gc.kind = %q, want scope-check", reviewScopeCheck.Metadata["gc.kind"])
	}
	if applyScopeCheck.Metadata["gc.kind"] != "scope-check" {
		t.Errorf("apply-fixes scope-check gc.kind = %q, want scope-check", applyScopeCheck.Metadata["gc.kind"])
	}
	var frozenSpec formula.Step
	if err := json.Unmarshal([]byte(specStep.Description), &frozenSpec); err != nil {
		t.Fatalf("unmarshal frozen spec: %v", err)
	}
	if frozenSpec.ID != "review-code" {
		t.Errorf("frozen spec ID = %q, want review-code", frozenSpec.ID)
	}
	if frozenSpec.Retry == nil || frozenSpec.Retry.MaxAttempts != 3 {
		t.Errorf("frozen spec retry = %+v, want MaxAttempts=3", frozenSpec.Retry)
	}
}

// TestBuildAttemptRecipeEnrichesNestedRalphChildren verifies that
// buildAttemptRecipe propagates gc.kind, gc.check_*, and a spec bead
// for nested ralph children.
func TestBuildAttemptRecipeEnrichesNestedRalphChildren(t *testing.T) {
	t.Parallel()

	step := &formula.Step{
		ID:    "converge",
		Title: "Converge",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 5},
		Children: []*formula.Step{
			{
				ID:      "inner-converge",
				Title:   "Inner Converge",
				Type:    "task",
				Timeout: "2m",
				Ralph: &formula.RalphSpec{
					MaxAttempts: 3,
					Check: &formula.RalphCheckSpec{
						Mode:    "script",
						Path:    "/tmp/check.sh",
						Timeout: "30s",
					},
				},
			},
		},
	}

	control := beads.Bead{
		ID: "gc-1",
		Metadata: map[string]string{
			"gc.step_id":  "converge",
			"gc.step_ref": "mol-test.converge",
		},
	}

	recipe := buildAttemptRecipe(step, control, 1)

	// Find the inner-converge child.
	var innerStep *formula.RecipeStep
	for i := range recipe.Steps {
		if recipe.Steps[i].ID == "mol-test.converge.iteration.1.inner-converge" {
			innerStep = &recipe.Steps[i]
			break
		}
	}
	if innerStep == nil {
		t.Fatal("inner-converge child step not found")
	}

	if innerStep.Metadata["gc.kind"] != "ralph" {
		t.Errorf("inner gc.kind = %q, want ralph", innerStep.Metadata["gc.kind"])
	}
	if innerStep.Metadata["gc.max_attempts"] != "3" {
		t.Errorf("inner gc.max_attempts = %q, want 3", innerStep.Metadata["gc.max_attempts"])
	}
	if innerStep.Metadata["gc.check_mode"] != "script" {
		t.Errorf("inner gc.check_mode = %q, want script", innerStep.Metadata["gc.check_mode"])
	}
	if innerStep.Metadata["gc.check_path"] != "/tmp/check.sh" {
		t.Errorf("inner gc.check_path = %q, want /tmp/check.sh", innerStep.Metadata["gc.check_path"])
	}
	if innerStep.Metadata["gc.check_timeout"] != "30s" {
		t.Errorf("inner gc.check_timeout = %q, want 30s", innerStep.Metadata["gc.check_timeout"])
	}
	if innerStep.Metadata["gc.step_timeout"] != "2m" {
		t.Errorf("inner gc.step_timeout = %q, want 2m", innerStep.Metadata["gc.step_timeout"])
	}
	for _, key := range []string{
		beadmeta.ContinueExitCodesMetadataKey,
		beadmeta.PendingExitCodesMetadataKey,
	} {
		if value, exists := innerStep.Metadata[key]; exists {
			t.Errorf("legacy inner %s = %q, want key absent", key, value)
		}
	}
	// Frozen step spec stored as a separate spec bead.
	var innerSpecStep *formula.RecipeStep
	for i := range recipe.Steps {
		if recipe.Steps[i].ID == "mol-test.converge.iteration.1.inner-converge.spec" {
			innerSpecStep = &recipe.Steps[i]
			break
		}
	}
	if innerSpecStep == nil {
		t.Error("inner missing spec bead for frozen step spec")
	} else if innerSpecStep.Metadata["gc.kind"] != "spec" {
		t.Errorf("inner spec gc.kind = %q, want spec", innerSpecStep.Metadata["gc.kind"])
	}
}

func TestBuildNestedControlSeedMatchesCompiledInnerScope(t *testing.T) {
	t.Parallel()

	inner := &formula.Step{
		ID:    "inner",
		Title: "Inner",
		Type:  "task",
		Ralph: &formula.RalphSpec{
			MaxAttempts: 2,
			Check:       &formula.RalphCheckSpec{Mode: "exec", Path: "inner.sh"},
		},
		Children: []*formula.Step{
			{ID: "work", Title: "Work", Type: "task"},
		},
	}
	outer := &formula.Step{
		ID:    "mol.outer",
		Title: "Outer",
		Type:  "task",
		Ralph: &formula.RalphSpec{
			MaxAttempts: 2,
			Check:       &formula.RalphCheckSpec{Mode: "exec", Path: "outer.sh"},
		},
		Children: []*formula.Step{inner},
	}

	compiled, err := formula.ApplyRalph([]*formula.Step{outer})
	if err != nil {
		t.Fatalf("ApplyRalph: %v", err)
	}
	var compiledWork *formula.Step
	for _, step := range compiled {
		if step.ID == "mol.outer.iteration.1.inner.iteration.1.work" {
			compiledWork = step
			break
		}
	}
	if compiledWork == nil {
		t.Fatal("compile-time inner body work step not found")
	}

	seedSteps, _ := buildNestedControlSeed(inner, "mol.outer.iteration.2.inner")
	var seededWork *formula.RecipeStep
	for i := range seedSteps {
		if seedSteps[i].ID == "mol.outer.iteration.2.inner.iteration.1.work" {
			seededWork = &seedSteps[i]
			break
		}
	}
	if seededWork == nil {
		t.Fatal("runtime nested seed inner body work step not found")
	}

	compiledScope := compiledWork.Metadata[beadmeta.ScopeRefMetadataKey]
	if want := "mol.outer.iteration.1.inner.iteration.1"; compiledScope != want {
		t.Errorf("compile-time gc.scope_ref = %q, want %q", compiledScope, want)
	}
	seededScope := seededWork.Metadata[beadmeta.ScopeRefMetadataKey]
	if want := "mol.outer.iteration.2.inner.iteration.1"; seededScope != want {
		t.Errorf("runtime seed gc.scope_ref = %q, want %q", seededScope, want)
	}

	compiledRelative := strings.TrimPrefix(compiledScope, "mol.outer.iteration.1.")
	seededRelative := strings.TrimPrefix(seededScope, "mol.outer.iteration.2.")
	if compiledRelative != seededRelative {
		t.Errorf("relative inner scope differs: compile-time %q, runtime seed %q", compiledRelative, seededRelative)
	}
}

// TestBuildAttemptRecipeNestedRalphExitPolicyMatchesCompiledControl catches
// outer-iteration re-seeding dropping or noncanonically encoding the opted-in
// inner ralph control's exit-disposition policy.
func TestBuildAttemptRecipeNestedRalphExitPolicyMatchesCompiledControl(t *testing.T) {
	t.Parallel()

	inner := &formula.Step{
		ID:    "inner",
		Title: "Inner",
		Type:  "task",
		Ralph: &formula.RalphSpec{
			MaxAttempts: 3,
			Check: &formula.RalphCheckSpec{
				Mode:              "exec",
				Path:              "inner.sh",
				ContinueExitCodes: []int{7, 1},
				PendingExitCodes:  []int{24, 20},
			},
		},
	}
	outer := &formula.Step{
		ID:    "mol.outer",
		Title: "Outer",
		Type:  "task",
		Ralph: &formula.RalphSpec{
			MaxAttempts: 2,
			Check:       &formula.RalphCheckSpec{Mode: "exec", Path: "outer.sh"},
		},
		Children: []*formula.Step{inner},
	}

	compiled, err := formula.ApplyRalph([]*formula.Step{outer})
	if err != nil {
		t.Fatalf("ApplyRalph: %v", err)
	}
	var compiledInner *formula.Step
	for _, step := range compiled {
		if step.ID == "mol.outer.iteration.1.inner" {
			compiledInner = step
			break
		}
	}
	if compiledInner == nil {
		t.Fatal("compile-time iteration-1 inner control not found")
	}

	runtimeRecipe := buildAttemptRecipe(outer, beads.Bead{
		ID: "gc-outer",
		Metadata: map[string]string{
			beadmeta.StepIDMetadataKey:  "mol.outer",
			beadmeta.StepRefMetadataKey: "mol.outer",
		},
	}, 2)
	var runtimeInner *formula.RecipeStep
	for i := range runtimeRecipe.Steps {
		if runtimeRecipe.Steps[i].ID == "mol.outer.iteration.2.inner" {
			runtimeInner = &runtimeRecipe.Steps[i]
			break
		}
	}
	if runtimeInner == nil {
		t.Fatal("runtime iteration-2 inner control not found")
	}

	for _, tc := range []struct {
		key  string
		want string
	}{
		{key: beadmeta.ContinueExitCodesMetadataKey, want: `[1,7]`},
		{key: beadmeta.PendingExitCodesMetadataKey, want: `[20,24]`},
	} {
		compiledValue := compiledInner.Metadata[tc.key]
		if compiledValue != tc.want {
			t.Fatalf("compile-time %s = %q, want %q", tc.key, compiledValue, tc.want)
		}
		if runtimeValue := runtimeInner.Metadata[tc.key]; runtimeValue != compiledValue {
			t.Fatalf("runtime %s = %q, want byte-identical compile-time value %q", tc.key, runtimeValue, compiledValue)
		}
	}
	if got := inner.Ralph.Check.ContinueExitCodes; len(got) != 2 || got[0] != 7 || got[1] != 1 {
		t.Fatalf("source continue exit codes mutated to %v, want [7 1]", got)
	}
	if got := inner.Ralph.Check.PendingExitCodes; len(got) != 2 || got[0] != 24 || got[1] != 20 {
		t.Fatalf("source pending exit codes mutated to %v, want [24 20]", got)
	}
}

// TestProcessControlReseededNestedRalphPendingExitRemainsPending catches a
// re-seeded opted-in inner control treating its pending exit as legacy
// continuation and spawning another inner iteration.
func TestProcessControlReseededNestedRalphPendingExitRemainsPending(t *testing.T) {
	t.Parallel()

	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "inner.sh"), []byte("#!/bin/sh\nexit 20\n"), 0o755); err != nil {
		t.Fatalf("write inner check: %v", err)
	}
	inner := &formula.Step{
		ID:    "inner",
		Title: "Inner",
		Type:  "task",
		Ralph: &formula.RalphSpec{
			MaxAttempts: 3,
			Check: &formula.RalphCheckSpec{
				Mode:              "exec",
				Path:              "inner.sh",
				Timeout:           "30s",
				ContinueExitCodes: []int{1},
				PendingExitCodes:  []int{20, 24},
			},
		},
	}
	outer := &formula.Step{
		ID:    "outer",
		Title: "Outer",
		Type:  "task",
		Ralph: &formula.RalphSpec{
			MaxAttempts: 3,
			Check:       &formula.RalphCheckSpec{Mode: "exec", Path: "outer.sh"},
		},
		Children: []*formula.Step{inner},
	}
	specJSON, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("marshal outer source step: %v", err)
	}

	store := beads.NewMemStore()
	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
	})
	outerControl := mustCreate(t, store, beads.Bead{
		Title: "outer ralph",
		Metadata: map[string]string{
			beadmeta.KindMetadataKey:           beadmeta.KindRalph,
			beadmeta.RootBeadIDMetadataKey:     root.ID,
			beadmeta.StepIDMetadataKey:         "outer",
			beadmeta.StepRefMetadataKey:        "mol.outer",
			beadmeta.MaxAttemptsMetadataKey:    "3",
			beadmeta.SourceStepSpecMetadataKey: string(specJSON),
			beadmeta.ControlEpochMetadataKey:   "1",
		},
	})
	opts := testProcessOptionsWithControlDispatcher("")
	opts.CityPath = cityPath
	opts.StorePath = cityPath
	opts.Tracef = t.Logf
	if err := spawnNextAttempt(context.Background(), store, outerControl, 2, opts); err != nil {
		t.Fatalf("spawn outer iteration 2: %v", err)
	}

	innerControl := findAttemptByRef(t, store, root.ID, "mol.outer.iteration.2.inner")
	if innerControl.ID == "" {
		t.Fatal("re-seeded iteration-2 inner control not found")
	}
	innerIteration := findAttemptByRef(t, store, root.ID, "mol.outer.iteration.2.inner.iteration.1")
	if innerIteration.ID == "" {
		t.Fatal("re-seeded inner iteration 1 not found")
	}
	if err := store.SetMetadataBatch(innerIteration.ID, map[string]string{
		beadmeta.OutcomeMetadataKey: beadmeta.OutcomePass,
	}); err != nil {
		t.Fatalf("mark inner iteration passed: %v", err)
	}
	mustClose(t, store, innerIteration.ID)
	beforeCount := ralphFixtureBeadCount(t, store)

	result, err := ProcessControl(store, mustGet(t, store, innerControl.ID), opts)
	if !errors.Is(err, ErrControlPending) {
		t.Fatalf("ProcessControl err = %v, want %v (result=%+v)", err, ErrControlPending, result)
	}
	if result.Processed || result.Action != "" || result.Created != 0 {
		t.Fatalf("pending result = %+v, want zero result", result)
	}
	if got := ralphFixtureBeadCount(t, store); got != beforeCount {
		t.Fatalf("pending bead count = %d, want unchanged %d", got, beforeCount)
	}
	if next := findAttemptByRef(t, store, root.ID, "mol.outer.iteration.2.inner.iteration.2"); next.ID != "" {
		t.Fatalf("unexpected continued inner iteration: %+v", next)
	}
}

// TestSpawnNextAttemptPropagatesRoutingMetadata verifies that
// spawnNextAttempt stamps gc.routed_to metadata from gc.execution_routed_to.
func TestSpawnNextAttemptPropagatesRoutingMetadata(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()

	spec := &formula.Step{
		ID:    "lint",
		Title: "Lint",
		Type:  "task",
		Retry: &formula.RetrySpec{MaxAttempts: 3},
	}
	specJSON, _ := json.Marshal(spec)

	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "lint retry",
		Metadata: map[string]string{
			"gc.kind":                "retry",
			"gc.root_bead_id":        root.ID,
			"gc.step_ref":            "mol-test.lint",
			"gc.step_id":             "lint",
			"gc.max_attempts":        "3",
			"gc.source_step_spec":    string(specJSON),
			"gc.control_epoch":       "1",
			"gc.execution_routed_to": "polecat",
		},
	})

	// Create attempt 1 (manually, simulating compiler).
	attempt1 := makeAttemptBead(t, store, root.ID, "mol-test.lint.attempt.1", 1, map[string]string{
		"gc.outcome":        "fail",
		"gc.failure_class":  "transient",
		"gc.failure_reason": "timeout",
	})
	mustDep(t, store, control.ID, attempt1.ID, "blocks")

	// Process — should spawn attempt 2 with routing labels.
	_, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("processRetryControl: %v", err)
	}

	// Find attempt 2 and check its labels.
	attempt2 := findAttemptByRef(t, store, root.ID, "mol-test.lint.attempt.2")
	if attempt2.ID == "" {
		t.Fatal("attempt 2 not created")
	}

	// Check routing metadata.
	if attempt2.Metadata["gc.execution_routed_to"] != "polecat" {
		t.Errorf("attempt 2 gc.execution_routed_to = %q, want polecat", attempt2.Metadata["gc.execution_routed_to"])
	}
	if attempt2.Metadata["gc.routed_to"] != "polecat" {
		t.Errorf("attempt 2 gc.routed_to = %q, want polecat", attempt2.Metadata["gc.routed_to"])
	}

	for _, l := range attempt2.Labels {
		if l == "pool:polecat" {
			t.Errorf("attempt 2 labels = %v, should not contain legacy pool label", attempt2.Labels)
		}
	}
}

func TestSpawnNextAttemptPreservesExplicitChildPoolRoutes(t *testing.T) {
	t.Parallel()

	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(`
[workspace]
name = "maintainer-city"
provider = "claude"

[providers.claude]
base = "builtin:claude"

[[agent]]
name = "claude"
dir = "gascity"

[agent.pool]
min = 0
max = -1

[[agent]]
name = "codex"
dir = "gascity"

[agent.pool]
min = 0
max = -1

[[agent]]
name = "control-dispatcher"
dir = "gascity"
max_active_sessions = 1
`), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}

	store := beads.NewMemStore()
	spec := &formula.Step{
		ID:    "review-loop",
		Title: "Review / fix loop",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 3},
		Children: []*formula.Step{
			{
				ID:    "review-claude",
				Title: "Code review: Claude",
				Type:  "task",
				Metadata: map[string]string{
					"gc.run_target": "gascity/claude",
				},
				Retry: &formula.RetrySpec{MaxAttempts: 3},
			},
			{
				ID:    "review-codex",
				Title: "Code review: Codex",
				Type:  "task",
				Metadata: map[string]string{
					"gc.run_target": "gascity/codex",
				},
				Retry: &formula.RetrySpec{MaxAttempts: 3},
			},
			{
				ID:    "synthesize",
				Title: "Synthesize findings",
				Type:  "task",
				Needs: []string{"review-claude", "review-codex"},
			},
		},
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal step spec: %v", err)
	}

	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "review-loop",
		Metadata: map[string]string{
			"gc.kind":                "ralph",
			"gc.root_bead_id":        root.ID,
			"gc.step_ref":            "mol-adopt-pr-v2.review-loop",
			"gc.step_id":             "review-loop",
			"gc.source_step_spec":    string(specJSON),
			"gc.control_epoch":       "1",
			"gc.execution_routed_to": "gascity/claude",
		},
	})

	if err := spawnNextAttempt(t.Context(), store, control, 2, ProcessOptions{CityPath: cityPath}); err != nil {
		t.Fatalf("spawnNextAttempt: %v", err)
	}

	scope := findAttemptByRef(t, store, root.ID, "mol-adopt-pr-v2.review-loop.iteration.2")
	if scope.ID == "" {
		t.Fatal("ralph scope iteration not created")
	}
	if scope.Metadata["gc.routed_to"] != "gascity/claude" {
		t.Fatalf("scope gc.routed_to = %q, want gascity/claude", scope.Metadata["gc.routed_to"])
	}
	if containsString(scope.Labels, "pool:gascity/claude") {
		t.Fatalf("scope labels = %v, should not contain legacy pool label", scope.Labels)
	}

	claude := findAttemptByRef(t, store, root.ID, "mol-adopt-pr-v2.review-loop.iteration.2.review-claude")
	if claude.ID == "" {
		t.Fatal("review-claude child not created")
	}
	if got := claude.Metadata["gc.routed_to"]; got != "gascity/control-dispatcher" {
		t.Fatalf("review-claude gc.routed_to = %q, want gascity/control-dispatcher", got)
	}
	if claude.Metadata["gc.execution_routed_to"] != "gascity/claude" {
		t.Fatalf("review-claude gc.execution_routed_to = %q, want gascity/claude", claude.Metadata["gc.execution_routed_to"])
	}
	if containsString(claude.Labels, "pool:gascity/claude") {
		t.Fatalf("review-claude labels = %v, should not contain legacy pool label", claude.Labels)
	}
	if claude.Assignee != "" {
		t.Fatalf("review-claude assignee = %q, want empty routed control-dispatcher queue", claude.Assignee)
	}

	codex := findAttemptByRef(t, store, root.ID, "mol-adopt-pr-v2.review-loop.iteration.2.review-codex")
	if codex.ID == "" {
		t.Fatal("review-codex child not created")
	}
	if got := codex.Metadata["gc.routed_to"]; got != "gascity/control-dispatcher" {
		t.Fatalf("review-codex gc.routed_to = %q, want gascity/control-dispatcher", got)
	}
	if codex.Metadata["gc.execution_routed_to"] != "gascity/codex" {
		t.Fatalf("review-codex gc.execution_routed_to = %q, want gascity/codex", codex.Metadata["gc.execution_routed_to"])
	}
	if containsString(codex.Labels, "pool:gascity/codex") {
		t.Fatalf("review-codex labels = %v, should not contain legacy pool label", codex.Labels)
	}
	if containsString(codex.Labels, "pool:gascity/claude") {
		t.Fatalf("review-codex labels = %v, should not contain pool:gascity/claude", codex.Labels)
	}
	if codex.Assignee != "" {
		t.Fatalf("review-codex assignee = %q, want empty routed control-dispatcher queue", codex.Assignee)
	}

	synthesize := findAttemptByRef(t, store, root.ID, "mol-adopt-pr-v2.review-loop.iteration.2.synthesize")
	if synthesize.ID == "" {
		t.Fatal("synthesize child not created")
	}
	if synthesize.Metadata["gc.routed_to"] != "gascity/claude" {
		t.Fatalf("synthesize gc.routed_to = %q, want gascity/claude fallback", synthesize.Metadata["gc.routed_to"])
	}
	if containsString(synthesize.Labels, "pool:gascity/claude") {
		t.Fatalf("synthesize labels = %v, should not contain legacy pool label", synthesize.Labels)
	}

	assertSpawnedSpecClosedAndUnrouted(t, store, root.ID, "review-claude")
	assertSpawnedSpecClosedAndUnrouted(t, store, root.ID, "review-codex")

	claudeSpec, err := findSpecBead(store, claude)
	if err != nil {
		t.Fatalf("findSpecBead(review-claude): %v", err)
	}
	if claudeSpec.Status != "closed" {
		t.Fatalf("review-claude spec status = %q, want closed", claudeSpec.Status)
	}

	codexSpec, err := findSpecBead(store, codex)
	if err != nil {
		t.Fatalf("findSpecBead(review-codex): %v", err)
	}
	if codexSpec.Status != "closed" {
		t.Fatalf("review-codex spec status = %q, want closed", codexSpec.Status)
	}
}

func assertSpawnedSpecClosedAndUnrouted(t *testing.T, store beads.Store, rootID, specFor string) {
	t.Helper()
	all, err := store.List(beads.ListQuery{
		Metadata:      map[string]string{"gc.root_bead_id": rootID},
		IncludeClosed: true,
		TierMode:      beads.TierBoth,
	})
	if err != nil {
		t.Fatalf("ListByMetadata(gc.root_bead_id=%q): %v", rootID, err)
	}
	for _, bead := range all {
		if bead.Metadata["gc.kind"] != "spec" || bead.Metadata["gc.spec_for"] != specFor {
			continue
		}
		if bead.Status != "closed" {
			t.Fatalf("spec %s status = %q, want closed", bead.ID, bead.Status)
		}
		if bead.Assignee != "" {
			t.Fatalf("spec %s assignee = %q, want empty", bead.ID, bead.Assignee)
		}
		for _, key := range []string{"gc.routed_to", "gc.execution_routed_to"} {
			if bead.Metadata[key] != "" {
				t.Fatalf("spec %s metadata %s = %q, want empty; full metadata: %#v", bead.ID, key, bead.Metadata[key], bead.Metadata)
			}
		}
		return
	}
	t.Fatalf("missing spec bead for %q under root %s", specFor, rootID)
}

// TestSpawnNextAttemptRouteConfigLoadFailureIsTransient is the post-merge
// remediation of PR #4175 on the attempt-spawn path: a route-config load/parse
// failure must be classified as a transient controller-boundary error (retried
// as pending by markControllerSpawnError), not a hard failure that quarantines
// the in-flight molecule. It complements the fanout-path coverage in
// attempt_control_routing_test.go.
func TestSpawnNextAttemptRouteConfigLoadFailureIsTransient(t *testing.T) {
	t.Parallel()

	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("this = = not valid toml ["), 0o644); err != nil {
		t.Fatalf("write malformed city.toml: %v", err)
	}

	store := beads.NewMemStore()
	spec := &formula.Step{
		ID:    "review-loop",
		Title: "Review / fix loop",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 3},
		Children: []*formula.Step{{
			ID:       "review-claude",
			Title:    "Code review: Claude",
			Type:     "task",
			Metadata: map[string]string{"gc.run_target": "gascity/claude"},
		}},
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal step spec: %v", err)
	}
	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "review-loop",
		Metadata: map[string]string{
			"gc.kind":                "ralph",
			"gc.root_bead_id":        root.ID,
			"gc.step_ref":            "mol-adopt-pr-v2.review-loop",
			"gc.step_id":             "review-loop",
			"gc.source_step_spec":    string(specJSON),
			"gc.control_epoch":       "1",
			"gc.execution_routed_to": "gascity/claude",
		},
	})

	err = spawnNextAttempt(t.Context(), store, control, 2, ProcessOptions{CityPath: cityPath})
	if err == nil {
		t.Fatal("spawnNextAttempt: want error on malformed route config, got nil")
	}
	if !IsTransientControllerError(err) {
		t.Fatalf("route-config load failure classified hard (%v); want transient so the molecule retries as pending", err)
	}
}

func TestSpawnNextAttemptRoutesDirectSessionRetryControlViaDispatcher(t *testing.T) {
	t.Parallel()

	store := beads.NewMemStore()
	_ = mustCreate(t, store, beads.Bead{
		Title:  "sky",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":        "sky",
			"session_name": "s-gc-sky",
		},
	})
	spec := &formula.Step{
		ID:    "review-loop",
		Title: "Review / fix loop",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 3},
		Children: []*formula.Step{{
			ID:       "review-direct",
			Title:    "Code review",
			Type:     "task",
			Assignee: "sky",
			Retry:    &formula.RetrySpec{MaxAttempts: 3},
		}},
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal step spec: %v", err)
	}

	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "review-loop",
		Metadata: map[string]string{
			"gc.kind":             "ralph",
			"gc.root_bead_id":     root.ID,
			"gc.step_ref":         "mol-direct.review-loop",
			"gc.step_id":          "review-loop",
			"gc.source_step_spec": string(specJSON),
			"gc.control_epoch":    "1",
		},
	})

	if err := spawnNextAttempt(t.Context(), store, control, 2, testProcessOptionsWithControlDispatcher("")); err != nil {
		t.Fatalf("spawnNextAttempt: %v", err)
	}

	child := findAttemptByRef(t, store, root.ID, "mol-direct.review-loop.iteration.2.review-direct")
	if child.ID == "" {
		t.Fatal("review-direct child not created")
	}
	if got := child.Assignee; got != "" {
		t.Fatalf("review-direct assignee = %q, want empty when control-dispatcher is unresolved", got)
	}
	if got := child.Metadata["gc.routed_to"]; got != config.ControlDispatcherAgentName {
		t.Fatalf("review-direct gc.routed_to = %q, want %q", got, config.ControlDispatcherAgentName)
	}
	if got := child.Metadata["gc.execution_routed_to"]; got != "sky" {
		t.Fatalf("review-direct gc.execution_routed_to = %q, want direct session target preserved", got)
	}
}

func TestSpawnNextAttemptAttachesDrainControl(t *testing.T) {
	t.Parallel()

	store := beads.NewMemStore()
	spec := &formula.Step{
		ID:    "loop",
		Title: "Loop",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 3},
		Children: []*formula.Step{
			{
				ID:    "drain-items",
				Title: "Drain items",
				Drain: &formula.DrainSpec{Context: "separate", Formula: "item-formula"},
			},
		},
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal step spec: %v", err)
	}
	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "loop",
		Metadata: map[string]string{
			"gc.kind":             "ralph",
			"gc.root_bead_id":     root.ID,
			"gc.step_ref":         "mol-int.loop",
			"gc.step_id":          "loop",
			"gc.source_step_spec": string(specJSON),
			"gc.control_epoch":    "1",
		},
	})

	if err := spawnNextAttempt(t.Context(), store, control, 2, testProcessOptionsWithControlDispatcher("")); err != nil {
		t.Fatalf("spawnNextAttempt: %v", err)
	}

	drain := findAttemptByRef(t, store, root.ID, "mol-int.loop.iteration.2.drain-items")
	if drain.ID == "" {
		t.Fatal("drain control bead not attached for iteration 2")
	}
	if got := drain.Metadata["gc.kind"]; got != "drain" {
		t.Errorf("drain gc.kind = %q, want drain", got)
	}
	if got := drain.Metadata["gc.drain_formula"]; got != "item-formula" {
		t.Errorf("drain gc.drain_formula = %q, want item-formula", got)
	}
	// Drain is a control-dispatcher kind on the compile path; attempt
	// re-spawn must route it the same way.
	if got := drain.Metadata["gc.routed_to"]; got != config.ControlDispatcherAgentName {
		t.Errorf("drain gc.routed_to = %q, want %q", got, config.ControlDispatcherAgentName)
	}
}

func TestResolveAttemptRouteBinding_ConfigTargetBeatsCollidingSessionAlias(t *testing.T) {
	t.Parallel()

	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		Title:  "colliding session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":        "gascity/claude",
			"session_name": "s-gc-colliding",
		},
	}); err != nil {
		t.Fatalf("create colliding session: %v", err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name: "claude",
			Dir:  "gascity",
		}},
	}

	binding, ok := resolveAttemptRouteBinding("gascity/claude", cfg, store)
	if !ok {
		t.Fatal("resolveAttemptRouteBinding did not resolve config target")
	}
	if binding.directSessionID != "" {
		t.Fatalf("directSessionID = %q, want empty so config route is not hijacked by alias", binding.directSessionID)
	}
	if binding.qualifiedName != "gascity/claude" || !binding.metadataOnly {
		t.Fatalf("binding = %+v, want metadata-only gascity/claude config route", binding)
	}
}

func TestResolveAttemptRouteBinding_NamedSessionTargetUsesCanonicalBeadID(t *testing.T) {
	t.Parallel()

	store := &countingAttemptRouteStore{MemStore: beads.NewMemStore()}
	named, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name":              "test-city--worker",
			"template":                  "worker",
			"configured_named_session":  "true",
			"configured_named_identity": "worker",
			"configured_named_mode":     "on_demand",
			"state":                     "asleep",
			"continuity_eligible":       "true",
		},
	})
	if err != nil {
		t.Fatalf("create named session: %v", err)
	}
	maxActive := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			MaxActiveSessions: &maxActive,
		}},
		NamedSessions: []config.NamedSession{{
			Template: "worker",
			Mode:     "on_demand",
		}},
	}

	startCalls := store.calls
	binding, ok := resolveAttemptRouteBinding("worker", cfg, store)
	if !ok {
		t.Fatal("resolveAttemptRouteBinding did not resolve named target")
	}
	if binding.directSessionID != named.ID {
		t.Fatalf("directSessionID = %q, want canonical named bead ID %q", binding.directSessionID, named.ID)
	}
	if binding.qualifiedName != "" || binding.sessionName != "" {
		t.Fatalf("binding = %+v, want direct named session only", binding)
	}
	// Per-resolution List calls must stay bounded so the per-attempt cost
	// does not fan out under reconciler load. The previous implementation
	// issued four sequential List calls per resolution; collapsing them
	// into one label-scoped scan was the fix for ga-pa57. Allow a small
	// margin (≤2) for unrelated lookups in the binding path while still
	// guarding against regression to the four-call shape.
	if delta := store.calls - startCalls; delta > 2 {
		t.Fatalf("resolveAttemptRouteBinding issued %d List calls, want ≤2 (regression risk for ga-pa57 contention)", delta)
	}
}

type countingAttemptRouteStore struct {
	*beads.MemStore
	calls int
}

func (s *countingAttemptRouteStore) List(query beads.ListQuery) ([]beads.Bead, error) {
	s.calls++
	return s.MemStore.List(query)
}

func TestResolveAttemptRouteBinding_NamedSessionTargetWithoutCanonicalBeadUsesMetadataRoute(t *testing.T) {
	t.Parallel()

	store := beads.NewMemStore()
	maxActive := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			MaxActiveSessions: &maxActive,
		}},
		NamedSessions: []config.NamedSession{{
			Template: "worker",
			Mode:     "on_demand",
		}},
	}

	binding, ok := resolveAttemptRouteBinding("worker", cfg, store)
	if !ok {
		t.Fatal("resolveAttemptRouteBinding did not resolve named target")
	}
	if binding.directSessionID != "" {
		t.Fatalf("directSessionID = %q, want empty without canonical bead", binding.directSessionID)
	}
	if binding.sessionName != "" {
		t.Fatalf("sessionName = %q, want empty so future runtime names are not assigned", binding.sessionName)
	}
	if binding.qualifiedName != "worker" || !binding.metadataOnly {
		t.Fatalf("binding = %+v, want metadata-only worker route", binding)
	}
}

func TestApplyAttemptControlStepRoute_ConfiguredControlDispatcherUsesMetadataRoute(t *testing.T) {
	t.Parallel()

	maxActive := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "maintainer-city"},
		Daemon:    config.DaemonConfig{FormulaV2: boolPtr(true)},
		Rigs: []config.Rig{{
			Name: "gascity",
			Path: t.TempDir(),
		}},
		Agents: []config.Agent{{
			Name: "claude",
			Dir:  "gascity",
		}, {
			Name:              config.ControlDispatcherAgentName,
			Dir:               "gascity",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			ProcessNames:      []string{"gc"},
			MaxActiveSessions: &maxActive,
		}},
	}

	step := &formula.RecipeStep{
		Metadata: map[string]string{
			"gc.routed_to": "stale-route",
		},
	}
	if err := applyAttemptControlStepRoute(step, "gascity/claude", cfg, beads.NewMemStore()); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}

	if step.Assignee != "" {
		t.Fatalf("assignee = %q, want empty routed control-dispatcher queue", step.Assignee)
	}
	if got := step.Metadata["gc.routed_to"]; got != "gascity/control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want gascity/control-dispatcher", got)
	}
	if got := step.Metadata["gc.execution_routed_to"]; got != "gascity/claude" {
		t.Fatalf("gc.execution_routed_to = %q, want gascity/claude", got)
	}
}

func TestApplyAttemptControlStepRoute_UsesExecutionRigContextForDirectSessionTarget(t *testing.T) {
	t.Parallel()

	maxActive := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "maintainer-city"},
		Rigs: []config.Rig{{
			Name: "frontend",
			Path: t.TempDir(),
		}},
		Agents: []config.Agent{{
			Name:              config.ControlDispatcherAgentName,
			Dir:               "frontend",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			ProcessNames:      []string{"gc"},
			MaxActiveSessions: &maxActive,
		}},
	}
	store := beads.NewMemStore()
	sessionBead, err := store.Create(beads.Bead{
		Title:  "frontend reviewer",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": "reviewer-1",
			"template":     "frontend/reviewer",
			"state":        "active",
		},
	})
	if err != nil {
		t.Fatalf("create session bead: %v", err)
	}
	step := &formula.RecipeStep{
		Metadata: map[string]string{
			"gc.execution_rig_context": "frontend",
			"gc.routed_to":             "stale-route",
		},
	}

	if err := applyAttemptControlStepRoute(step, sessionBead.ID, cfg, store); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}

	if step.Assignee != "" {
		t.Fatalf("assignee = %q, want empty routed control-dispatcher queue", step.Assignee)
	}
	if got := step.Metadata["gc.routed_to"]; got != "frontend/control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want frontend/control-dispatcher", got)
	}
	if got := step.Metadata["gc.execution_routed_to"]; got != sessionBead.ID {
		t.Fatalf("gc.execution_routed_to = %q, want direct session id %q", got, sessionBead.ID)
	}
}

// TestApplyAttemptControlStepRoute_UsesRigDispatcherForRigExecution covers the
// attempt-time analog of graph.v2 decoration: with a bound city dispatcher
// (core.control-dispatcher, Dir="", max_active_sessions=1) plus a
// per-rig copy (fixture/core.control-dispatcher), an attempt-kind control bead
// whose execution target lives in the rig must keep the rig-qualified route.
func TestApplyAttemptControlStepRoute_UsesRigDispatcherForRigExecution(t *testing.T) {
	t.Parallel()

	maxActive := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "maintainer-city"},
		Daemon:    config.DaemonConfig{FormulaV2: boolPtr(true)},
		Rigs: []config.Rig{{
			Name: "fixture",
			Path: t.TempDir(),
		}},
		Agents: []config.Agent{{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			ProcessNames:      []string{"gc"},
			MaxActiveSessions: &maxActive,
		}, {
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			Dir:               "fixture",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			ProcessNames:      []string{"gc"},
			MaxActiveSessions: &maxActive,
		}, {
			Name: "superpowers.brainstorming",
			Dir:  "fixture",
		}},
	}

	step := &formula.RecipeStep{
		Metadata: map[string]string{
			"gc.routed_to": "stale-route",
		},
	}
	if err := applyAttemptControlStepRoute(step, "fixture/superpowers.brainstorming", cfg, beads.NewMemStore()); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}

	if step.Assignee != "" {
		t.Fatalf("assignee = %q, want empty routed control-dispatcher queue", step.Assignee)
	}
	if got := step.Metadata["gc.routed_to"]; got != "fixture/core.control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want fixture/core.control-dispatcher", got)
	}
	if got := step.Metadata["gc.execution_routed_to"]; got != "fixture/superpowers.brainstorming" {
		t.Fatalf("gc.execution_routed_to = %q, want fixture/superpowers.brainstorming", got)
	}
}

func TestApplyAttemptControlStepRoute_UsesOwningStoreScopeOverExecutionScope(t *testing.T) {
	t.Parallel()

	maxActive := 1
	cfg := &config.City{Agents: []config.Agent{
		{Name: "city-worker", Scope: "city"},
		{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			MaxActiveSessions: &maxActive,
		},
		{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			Dir:               "fixture",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			MaxActiveSessions: &maxActive,
		},
	}}
	step := &formula.RecipeStep{Metadata: map[string]string{
		"gc.root_store_ref": "rig:fixture",
		"gc.routed_to":      "stale-route",
	}}

	if err := applyAttemptControlStepRoute(step, "city-worker", cfg, beads.NewMemStore()); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}
	if got := step.Metadata["gc.routed_to"]; got != "fixture/core.control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want owning-store route fixture/core.control-dispatcher", got)
	}
	if got := step.Metadata["gc.execution_routed_to"]; got != "city-worker" {
		t.Fatalf("gc.execution_routed_to = %q, want city-worker", got)
	}
}

func TestApplyAttemptControlStepRoute_UsesCityStoreScopeOverRigExecution(t *testing.T) {
	t.Parallel()

	maxActive := 1
	cfg := &config.City{Agents: []config.Agent{
		{Name: "worker", Dir: "fixture"},
		{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			MaxActiveSessions: &maxActive,
		},
		{
			Name:              config.ControlDispatcherAgentName,
			BindingName:       "core",
			Dir:               "fixture",
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			MaxActiveSessions: &maxActive,
		},
	}}
	step := &formula.RecipeStep{Metadata: map[string]string{
		beadmeta.RootStoreRefMetadataKey: "city:maintainer-city",
	}}

	if err := applyAttemptControlStepRoute(step, "fixture/worker", cfg, beads.NewMemStore()); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}
	if got := step.Metadata[beadmeta.RoutedToMetadataKey]; got != "core.control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want owning city-store route core.control-dispatcher", got)
	}
	if got := step.Metadata[beadmeta.ExecutionRoutedToMetadataKey]; got != "fixture/worker" {
		t.Fatalf("gc.execution_routed_to = %q, want fixture/worker", got)
	}
}

func TestApplyAttemptControlStepRoute_RejectsMissingOwningStoreDispatcher(t *testing.T) {
	t.Parallel()

	maxActive := 1
	cfg := &config.City{Agents: []config.Agent{{
		Name:              config.ControlDispatcherAgentName,
		BindingName:       "core",
		StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
		MaxActiveSessions: &maxActive,
	}}}
	step := &formula.RecipeStep{Metadata: map[string]string{
		"gc.root_store_ref": "rig:fixture",
	}}

	err := applyAttemptControlStepRoute(step, "city-worker", cfg, beads.NewMemStore())
	if err == nil || !strings.Contains(err.Error(), `control-dispatcher agent for rig "fixture" not found`) {
		t.Fatalf("applyAttemptControlStepRoute error = %v, want missing fixture dispatcher", err)
	}
	if got := step.Metadata["gc.routed_to"]; got != "" {
		t.Fatalf("gc.routed_to = %q, want no invented route", got)
	}
}

func TestSpawnNextAttemptUsesOwningCityStoreForRigExecution(t *testing.T) {
	t.Parallel()

	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(`
[workspace]
name = "maintainer-city"

[daemon]
formula_v2 = true

[[rigs]]
name = "frontend"
path = "/tmp/frontend"

[[rigs]]
name = "backend"
path = "/tmp/backend"

[[agent]]
name = "reviewer"
dir = "frontend"

[[agent]]
name = "control-dispatcher"
max_active_sessions = 1

[[agent]]
name = "control-dispatcher"
dir = "frontend"
max_active_sessions = 1

[[agent]]
name = "reviewer"
dir = "backend"

[[agent]]
name = "control-dispatcher"
dir = "backend"
max_active_sessions = 1
`), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}

	store := beads.NewMemStore()
	spec := &formula.Step{
		ID:    "review-loop",
		Title: "Review loop",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 3},
		Children: []*formula.Step{
			{
				ID:    "review",
				Title: "Review",
				Type:  "task",
				Metadata: map[string]string{
					"gc.run_target":     "reviewer",
					"gc.root_store_ref": "rig:frontend",
				},
				Retry: &formula.RetrySpec{MaxAttempts: 2},
			},
		},
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal step spec: %v", err)
	}

	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "review-loop",
		Metadata: map[string]string{
			"gc.kind":                "ralph",
			"gc.root_bead_id":        root.ID,
			"gc.step_ref":            "mol-adopt-pr-v2.review-loop",
			"gc.step_id":             "review-loop",
			"gc.source_step_spec":    string(specJSON),
			"gc.control_epoch":       "1",
			"gc.execution_routed_to": "frontend/reviewer",
			"gc.root_store_ref":      "city:maintainer-city",
		},
	})

	if err := spawnNextAttempt(t.Context(), store, control, 2, ProcessOptions{CityPath: cityPath}); err != nil {
		t.Fatalf("spawnNextAttempt: %v", err)
	}

	review := findAttemptByRef(t, store, root.ID, "mol-adopt-pr-v2.review-loop.iteration.2.review")
	if review.ID == "" {
		t.Fatal("review child not created")
	}
	if got := review.Metadata["gc.execution_routed_to"]; got != "frontend/reviewer" {
		t.Fatalf("review gc.execution_routed_to = %q, want frontend/reviewer", got)
	}
	if got := review.Metadata["gc.routed_to"]; got != "control-dispatcher" {
		t.Fatalf("review gc.routed_to = %q, want owning city-store route control-dispatcher", got)
	}
	if got := review.Metadata["gc.root_store_ref"]; got != "city:maintainer-city" {
		t.Fatalf("review gc.root_store_ref = %q, want authoritative parent store city:maintainer-city", got)
	}
	if review.Assignee != "" {
		t.Fatalf("review assignee = %q, want empty routed control-dispatcher queue", review.Assignee)
	}
}

func TestApplyAttemptControlStepRoute_MinimalRigScopedDispatcherUsesMetadataRoute(t *testing.T) {
	t.Parallel()

	cfg := &config.City{
		Workspace: config.Workspace{Name: "maintainer-city"},
		Agents: []config.Agent{
			{
				Name: "claude",
				Dir:  "gascity",
			},
			{
				Name: "control-dispatcher",
				Dir:  "gascity",
			},
		},
	}

	step := &formula.RecipeStep{
		Metadata: map[string]string{
			"gc.routed_to": "stale-route",
		},
	}
	if err := applyAttemptControlStepRoute(step, "gascity/claude", cfg, beads.NewMemStore()); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}

	if step.Assignee != "" {
		t.Fatalf("assignee = %q, want empty routed control-dispatcher queue", step.Assignee)
	}
	if got := step.Metadata["gc.routed_to"]; got != "gascity/control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want gascity/control-dispatcher", got)
	}
}

func TestApplyAttemptControlStepRoute_KeepsControlBeadsOnDispatcherForNamedExecutionTarget(t *testing.T) {
	t.Parallel()

	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name":              "worker",
			"template":                  "worker",
			"configured_named_session":  "true",
			"configured_named_identity": "worker",
			"configured_named_mode":     "always",
			"state":                     "active",
		},
	}); err != nil {
		t.Fatalf("create named session: %v", err)
	}
	_, err := store.Create(beads.Bead{
		Title:  "control-dispatcher",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name":              "control-dispatcher",
			"template":                  config.ControlDispatcherAgentName,
			"configured_named_session":  "true",
			"configured_named_identity": config.ControlDispatcherAgentName,
			"configured_named_mode":     "always",
			"state":                     "active",
		},
	})
	if err != nil {
		t.Fatalf("create control-dispatcher session: %v", err)
	}

	maxActive := 1
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "worker",
			MaxActiveSessions: &maxActive,
		}, {
			Name:              config.ControlDispatcherAgentName,
			StartCommand:      config.ControlDispatcherStartCommandFor("{{.Agent}}"),
			ProcessNames:      []string{"gc"},
			MaxActiveSessions: &maxActive,
		}},
		NamedSessions: []config.NamedSession{{
			Template: "worker",
			Mode:     "always",
		}},
	}

	step := &formula.RecipeStep{
		ID:       "review-scope-check",
		Title:    "Finalize scope for review",
		Type:     "task",
		Metadata: map[string]string{"gc.kind": "scope-check"},
	}

	if err := applyAttemptControlStepRoute(step, "worker", cfg, store); err != nil {
		t.Fatalf("applyAttemptControlStepRoute: %v", err)
	}

	if got := step.Metadata["gc.execution_routed_to"]; got != "worker" {
		t.Fatalf("gc.execution_routed_to = %q, want worker", got)
	}
	if got := step.Metadata["gc.routed_to"]; got != "control-dispatcher" {
		t.Fatalf("gc.routed_to = %q, want control-dispatcher", got)
	}
	if step.Assignee != "" {
		t.Fatalf("assignee = %q, want empty routed control-dispatcher queue", step.Assignee)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestBuildAttemptRecipeScopeMetadataForRalph verifies that ralph iteration
// root beads get scope metadata (gc.scope_role, gc.scope_name, gc.ralph_step_id).
func TestBuildAttemptRecipeScopeMetadataForRalph(t *testing.T) {
	t.Parallel()

	step := &formula.Step{
		ID:    "self-review",
		Title: "Self Review",
		Type:  "task",
		Ralph: &formula.RalphSpec{MaxAttempts: 5},
		Children: []*formula.Step{
			{ID: "implement", Title: "Implement", Type: "task"},
			{ID: "check", Title: "Check", Type: "task", Needs: []string{"implement"}},
		},
	}

	control := beads.Bead{
		ID: "gc-1",
		Metadata: map[string]string{
			"gc.step_id":  "self-review",
			"gc.step_ref": "mol-demo.self-review",
		},
	}

	recipe := buildAttemptRecipe(step, control, 3)

	rootStep := recipe.Steps[0]
	if rootStep.Metadata["gc.kind"] != "scope" {
		t.Errorf("root gc.kind = %q, want scope", rootStep.Metadata["gc.kind"])
	}
	if rootStep.Metadata["gc.scope_role"] != "body" {
		t.Errorf("root gc.scope_role = %q, want body", rootStep.Metadata["gc.scope_role"])
	}
	if rootStep.Metadata["gc.scope_name"] != "self-review" {
		t.Errorf("root gc.scope_name = %q, want self-review", rootStep.Metadata["gc.scope_name"])
	}
	if rootStep.Metadata["gc.ralph_step_id"] != "self-review" {
		t.Errorf("root gc.ralph_step_id = %q, want self-review", rootStep.Metadata["gc.ralph_step_id"])
	}
}

// TestRetryIdempotencyKeyPreventsDoubleSpawn verifies that processing the
// same control bead twice (e.g., due to a race in the controller) does not
// create duplicate attempt sub-DAGs.
func TestRetryIdempotencyKeyPreventsDoubleSpawn(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()

	spec := &formula.Step{
		ID:    "build",
		Title: "Build",
		Type:  "task",
		Retry: &formula.RetrySpec{MaxAttempts: 3},
	}
	root, control := makeRetryControl(t, store, "mol-test.build", spec, 3)

	attempt1 := makeAttemptBead(t, store, root.ID, "mol-test.build.attempt.1", 1, map[string]string{
		"gc.outcome":        "fail",
		"gc.failure_class":  "transient",
		"gc.failure_reason": "flaky",
	})
	mustDep(t, store, control.ID, attempt1.ID, "blocks")

	// Process once — spawns attempt 2.
	_, err := processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("first process: %v", err)
	}

	allAfterFirst, _ := store.ListOpen()
	countAfterFirst := len(allAfterFirst)

	// Process again with same state -- epoch conflict should prevent double spawn.
	// The epoch was already incremented by the first Attach, so a second
	// processRetryControl with the same attempt (attempt 1 still closed, attempt 2
	// still open) will find attempt 2 as the latest and see it's not closed.
	// This verifies the pending guard.
	_, err = processRetryControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if !errors.Is(err, ErrControlPending) {
		t.Fatalf("second process error = %v, want %v", err, ErrControlPending)
	}

	// No new beads should have been created.
	allAfterSecond, _ := store.ListOpen()
	if len(allAfterSecond) != countAfterFirst {
		t.Errorf("bead count changed: %d → %d (double spawn?)", countAfterFirst, len(allAfterSecond))
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// findAttemptByRef finds a bead with a matching gc.step_ref in the workflow.
func findAttemptByRef(t *testing.T, store beads.Store, _, stepRef string) beads.Bead {
	t.Helper()
	all, err := store.List(beads.ListQuery{AllowScan: true, TierMode: beads.TierBoth})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, b := range all {
		if b.Metadata["gc.step_ref"] == stepRef {
			return b
		}
	}
	return beads.Bead{}
}
