package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/workclose"
)

// alwaysReachable / neverReachable are injected commit-reachability oracles so
// the work-record validation is testable without a real git repo.
func alwaysReachable(string, string) bool { return true }
func neverReachable(string, string) bool  { return false }

func TestValidateWorkRecordOnClose(t *testing.T) {
	tests := []struct {
		name      string
		meta      map[string]string
		reachable func(string, string) bool
		wantViol  string // substring expected in the (single) violation; "" ⇒ no violations
	}{
		{
			name:     "no-op close passes",
			meta:     map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
			wantViol: "",
		},
		{
			name:     "blocked close passes",
			meta:     map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeBlocked},
			wantViol: "",
		},
		{
			name: "shipped with reachable commit passes",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: alwaysReachable,
			wantViol:  "",
		},
		{
			name: "shipped with commit NOT reachable on branch is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: neverReachable,
			wantViol:  "not reachable",
		},
		{
			name: "shipped without commit is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: alwaysReachable,
			wantViol:  beadmeta.WorkCommitMetadataKey,
		},
		{
			name: "shipped without branch is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
			},
			reachable: alwaysReachable,
			wantViol:  beadmeta.WorkBranchMetadataKey,
		},
		{
			name:     "missing outcome is rejected",
			meta:     map[string]string{},
			wantViol: "missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			name:     "unknown outcome is rejected",
			meta:     map[string]string{beadmeta.WorkOutcomeMetadataKey: "done"},
			wantViol: "invalid",
		},
		{
			name: "whitespace-padded shipped outcome is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: " shipped ",
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: alwaysReachable,
			wantViol:  `invalid gc.work_outcome=" shipped "`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reachable := tc.reachable
			if reachable == nil {
				reachable = neverReachable
			}
			bead := beads.Bead{ID: "wr-1", Type: "task", Metadata: tc.meta}
			got := validateWorkRecordOnClose(bead, reachable)
			if tc.wantViol == "" {
				if len(got) != 0 {
					t.Fatalf("expected no violations, got %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("expected a violation containing %q, got none", tc.wantViol)
			}
			joined := strings.Join(got, " | ")
			if !strings.Contains(joined, tc.wantViol) {
				t.Fatalf("violation %q does not contain %q", joined, tc.wantViol)
			}
		})
	}
}

func TestIsWorkRecordGatedBead(t *testing.T) {
	tests := []struct {
		name string
		bead beads.Bead
		want bool
	}{
		{name: "plain task bead is gated", bead: beads.Bead{Type: "task"}, want: true},
		{name: "empty type defaults to gated", bead: beads.Bead{}, want: true},
		{
			name: "workflow root is not gated",
			bead: beads.Bead{Type: "task", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow}},
			want: false,
		},
		{
			name: "control run step is not gated",
			bead: beads.Bead{Type: "task", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindRun}},
			want: false,
		},
		{name: "convoy bead is not gated", bead: beads.Bead{Type: "convoy"}, want: false},
		{name: "message bead is not gated", bead: beads.Bead{Type: "message"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWorkRecordGatedBead(tc.bead); got != tc.want {
				t.Fatalf("isWorkRecordGatedBead = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidWorkOutcome(t *testing.T) {
	for _, v := range []string{
		beadmeta.WorkOutcomeShipped, beadmeta.WorkOutcomeNoOp,
		beadmeta.WorkOutcomeBlocked, beadmeta.WorkOutcomeAbandoned,
	} {
		if !validWorkOutcome(v) {
			t.Errorf("validWorkOutcome(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "pass", "fail", "skipped", "done", "SHIPPED"} {
		if validWorkOutcome(v) {
			t.Errorf("validWorkOutcome(%q) = true, want false", v)
		}
	}
}

func TestWorkRecordCloseTargets(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantIDs []string
		wantOK  bool
	}{
		{"close subcommand", []string{"close", "wr-1"}, []string{"wr-1"}, true},
		{"close multiple", []string{"close", "wr-1", "wr-2"}, []string{"wr-1", "wr-2"}, true},
		{"update status=closed", []string{"update", "wr-1", "--status=closed"}, []string{"wr-1"}, true},
		{"update --status closed", []string{"update", "wr-1", "--status", "closed"}, []string{"wr-1"}, true},
		{"update -s closed", []string{"update", "wr-1", "-s", "closed"}, []string{"wr-1"}, true},
		{"last repeated status closes", []string{"update", "wr-1", "--status=open", "--status=closed"}, []string{"wr-1"}, true},
		{"last repeated status stays open", []string{"update", "wr-1", "--status=closed", "--status=open"}, nil, false},
		{"status-looking value is consumed", []string{"update", "wr-1", "--notes", "--status=open", "--status", "closed"}, []string{"wr-1"}, true},
		{"update to open is not a close", []string{"update", "wr-1", "--status=open"}, nil, false},
		{"update without status is not a close", []string{"update", "wr-1", "--notes", "x"}, nil, false},
		{"read subcommand is not a close", []string{"show", "wr-1"}, nil, false},
		{"empty args", nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids, ok := workRecordCloseTargets(tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (ids=%v)", ok, tc.wantOK, ids)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("ids = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

// TestEvaluateWorkRecordCloseGate exercises the full gate plumbing (store read,
// scoping, warn vs enforce fork) over an in-memory store, covering ADR-0009
// acceptance (b)/(c) at the integration level.
func TestEvaluateWorkRecordCloseGate(t *testing.T) {
	beadsList := []beads.Bead{
		{ID: "wr-shipped-nocommit", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}},
		{ID: "wr-noop", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp}},
		{ID: "wr-atomic-noop", Type: "task", Status: "in_progress", Metadata: map[string]string{}},
		{ID: "wr-missing", Type: "task", Status: "in_progress", Metadata: map[string]string{}},
		{ID: "wr-control", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow}},
		{ID: "wr-becomes-task", Type: "convoy", Status: "in_progress", Metadata: map[string]string{}},
	}
	newStore := func() beads.Store { return beads.NewMemStoreFrom(1, beadsList, nil) }

	tests := []struct {
		name      string
		args      []string
		enforce   bool
		wantBlock bool
		wantWarn  string // substring expected on stderr; "" ⇒ no output
	}{
		{"non-close subcommand is ignored", []string{"show", "wr-shipped-nocommit"}, true, false, ""},
		{"control bead is exempt", []string{"close", "wr-control"}, true, false, ""},
		{"no-op close passes", []string{"close", "wr-noop"}, true, false, ""},
		{"shipped-no-commit warns only in compatibility mode", []string{"close", "wr-shipped-nocommit"}, false, false, "work-record gate (warn-only)"},
		{"shipped-no-commit blocks when enforced", []string{"close", "wr-shipped-nocommit"}, true, true, "work-record gate (enforced)"},
		{"multi-ID close applies the same verdict", []string{"close", "wr-noop", "wr-shipped-nocommit"}, true, true, "close of wr-shipped-nocommit"},
		{"missing outcome blocks when enforced", []string{"close", "wr-missing"}, true, true, "missing " + beadmeta.WorkOutcomeMetadataKey},
		{"update --status=closed is gated", []string{"update", "wr-shipped-nocommit", "--status=closed"}, true, true, "close of wr-shipped-nocommit"},
		{
			"atomic update validates submitted metadata",
			[]string{"update", "wr-atomic-noop", "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"atomic type change cannot manufacture an ungated shipped task",
			[]string{"update", "wr-becomes-task", "--type", "task", "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped, "--status=closed"},
			true,
			true,
			beadmeta.WorkCommitMetadataKey,
		},
		{
			"metadata JSON validates submitted no-op",
			[]string{"update", "wr-missing", "--metadata", `{"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"metadata equals JSON validates submitted no-op",
			[]string{"update", "wr-missing", `--metadata={"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"last repeated metadata JSON value wins",
			[]string{"update", "wr-missing", `--metadata={"gc.work_outcome":"no-op"}`, `--metadata={"unrelated":"value"}`, "--status=closed"},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			"last repeated metadata JSON ignores an earlier malformed value",
			[]string{"update", "wr-missing", `--metadata={not-json}`, `--metadata={"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"metadata JSON cannot hide shipped evidence requirements behind stored no-op",
			[]string{"update", "wr-noop", `--metadata={"gc.work_outcome":"shipped"}`, "--status=closed"},
			true,
			true,
			beadmeta.WorkCommitMetadataKey,
		},
		{
			"metadata JSON cannot combine with later set-metadata",
			[]string{"update", "wr-noop", `--metadata={"gc.work_outcome":"shipped"}`, "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			true,
			"cannot project metadata",
		},
		{
			"metadata JSON cannot combine with earlier set-metadata",
			[]string{"update", "wr-noop", "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, `--metadata={"gc.work_outcome":"shipped"}`, "--status=closed"},
			true,
			true,
			"cannot project metadata",
		},
		{
			"unset-metadata wins over set-metadata regardless of argv order",
			[]string{"update", "wr-missing", "--unset-metadata", beadmeta.WorkOutcomeMetadataKey, "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			"metadata JSON cannot combine with unset-metadata",
			[]string{"update", "wr-noop", "--unset-metadata", beadmeta.WorkOutcomeMetadataKey, `--metadata={"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			true,
			"cannot project metadata",
		},
		{
			"non-string metadata uses beads StringMap coercion",
			[]string{"update", "wr-noop", `--metadata={"gc.work_outcome":true}`, "--status=closed"},
			true,
			true,
			`invalid gc.work_outcome="true"`,
		},
		{
			"malformed metadata JSON fails closed",
			[]string{"update", "wr-noop", `--metadata={not-json}`, "--status=closed"},
			true,
			true,
			"cannot project --metadata",
		},
		{
			"metadata file input fails closed",
			[]string{"update", "wr-noop", "--metadata", "@work-record.json", "--status=closed"},
			true,
			true,
			"cannot project --metadata",
		},
		{
			"metadata-looking positional after terminator is not projected",
			[]string{"update", "wr-missing", "--status=closed", "--", "--set-metadata=" + beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			"metadata-like flag value is not submitted metadata",
			[]string{"update", "wr-missing", "--notes", "--set-metadata=" + beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			var journal events.Provider
			if !tc.enforce {
				journal = events.NewFake()
			}
			block := evaluateWorkRecordCloseGate(tc.args, newStore(), nil, t.TempDir(), "", journal, tc.enforce, &stderr)
			if block != tc.wantBlock {
				t.Fatalf("block = %v, want %v; stderr=%s", block, tc.wantBlock, stderr.String())
			}
			out := stderr.String()
			if tc.wantWarn == "" {
				if out != "" {
					t.Fatalf("expected no gate output, got %q", out)
				}
				return
			}
			if !strings.Contains(out, tc.wantWarn) {
				t.Fatalf("gate output %q does not contain %q", out, tc.wantWarn)
			}
		})
	}
}

func TestEvaluateWorkRecordCloseGateRejectsBranchReachableShippedUpdateWithoutLandingEvidence(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	artifactPath := filepath.Join(repoDir, "artifact.txt")
	if err := os.WriteFile(artifactPath, []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "test: integrate artifact")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID:     "wr-atomic-shipped",
		Type:   "task",
		Status: "in_progress",
		Metadata: map[string]string{
			beadmeta.WorkDirMetadataKey: repoDir,
		},
	}}, nil)
	args := []string{
		"update", "wr-atomic-shipped",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=main",
		"--status=closed",
	}
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, store, nil, repoDir, "city:test", nil, true, &stderr); !block {
		t.Fatalf("branch-reachable shipped close without landing evidence was allowed; stderr=%s", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, beadmeta.DeliveryStateMetadataKey) {
		t.Fatalf("close rejection did not diagnose missing landing evidence: %q", got)
	}
}

func TestEvaluateWorkRecordCloseGateAllowsAtomicShippedUpdateWithLandingEvidence(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	if err := os.WriteFile(filepath.Join(repoDir, "artifact.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "test: integrate artifact")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	const eventID = "gcl-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	landedSHA := strings.Repeat("f", 40)
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           eventID,
		ObservedLandedSHA: landedSHA,
		WorkRecords: []events.DeliveryWorkRecordRef{{
			StoreRef: "city:test", BeadID: "wr-atomic-shipped", WorkCommit: commit,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{Type: events.DeliveryLanded, Subject: eventID, Payload: payload})
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "wr-atomic-shipped", Type: "task", Status: "in_progress",
		Metadata: map[string]string{beadmeta.WorkDirMetadataKey: repoDir},
	}}, nil)
	args := []string{
		"update", "wr-atomic-shipped",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=main",
		"--set-metadata", beadmeta.DeliveryStateMetadataKey + "=landed",
		"--set-metadata", beadmeta.DeliveryEventIDMetadataKey + "=" + eventID,
		"--set-metadata", beadmeta.DeliverySourceCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.DeliveryLandedSHAMetadataKey + "=" + landedSHA,
		"--status=closed",
	}
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, store, nil, repoDir, "city:test", journal, true, &stderr); block {
		t.Fatalf("valid atomic shipped close blocked; stderr=%s", stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("valid atomic shipped close warned: %q", got)
	}
}

func TestEvaluateWorkRecordCloseGateRejectsAtomicPaddedShippedOutcomeWithLandingEvidence(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	if err := os.WriteFile(filepath.Join(repoDir, "artifact.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "test: integrate artifact")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	const eventID = "gcl-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	landedSHA := strings.Repeat("f", 40)
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           eventID,
		ObservedLandedSHA: landedSHA,
		WorkRecords: []events.DeliveryWorkRecordRef{{
			StoreRef: "city:test", BeadID: "wr-atomic-padded-shipped", WorkCommit: commit,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{Type: events.DeliveryLanded, Subject: eventID, Payload: payload})
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "wr-atomic-padded-shipped", Type: "task", Status: "in_progress",
		Metadata: map[string]string{beadmeta.WorkDirMetadataKey: repoDir},
	}}, nil)
	args := []string{
		"update", "wr-atomic-padded-shipped",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "= shipped ",
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=main",
		"--set-metadata", beadmeta.DeliveryStateMetadataKey + "=landed",
		"--set-metadata", beadmeta.DeliveryEventIDMetadataKey + "=" + eventID,
		"--set-metadata", beadmeta.DeliverySourceCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.DeliveryLandedSHAMetadataKey + "=" + landedSHA,
		"--status=closed",
	}
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, store, nil, repoDir, "city:test", journal, true, &stderr); !block {
		t.Fatalf("padded atomic shipped outcome was allowed; stderr=%s", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, `invalid gc.work_outcome=" shipped "`) {
		t.Fatalf("padded atomic shipped outcome did not report the raw invalid value: %q", got)
	}
}

// panicOnGetStore embeds a nil beads.Store and overrides Get to panic. It
// proves a code path never falls back to the store for a given ID — used to
// assert the close gate actually consumes preFetched beads instead of
// re-reading them: gc bd close previously paid for the same store.Get twice,
// once in the write-ID guard and once in this gate.
type panicOnGetStore struct{ beads.Store }

func (panicOnGetStore) Get(id string) (beads.Bead, error) {
	panic("store.Get called for id " + id + ": preFetched bead should have been used")
}

type unreadableWorkRecordStore struct{ beads.Store }

func (unreadableWorkRecordStore) Get(string) (beads.Bead, error) {
	return beads.Bead{}, errors.New("authoritative store unavailable")
}

type unreadableWarnOnlyProvider struct{ *events.Fake }

func (p unreadableWarnOnlyProvider) List(events.Filter) ([]events.Event, error) {
	return nil, errors.New("usage readback unavailable")
}

func TestEvaluateWorkRecordCloseGateFailsClosedWhenAuthoritativeReadCannotClassifyOutcome(t *testing.T) {
	args := []string{"close", "wr-unreadable"}
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, unreadableWorkRecordStore{}, nil, t.TempDir(), "city:test", nil, true, &stderr); !block {
		t.Fatalf("close failed open when authoritative read could not classify its outcome; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "authoritative store unavailable") {
		t.Fatalf("read refusal was not diagnosed: %q", stderr.String())
	}
}

func TestEvaluateWorkRecordCloseGateUsesPreFetchedBead(t *testing.T) {
	preFetched := map[string]beads.Bead{
		"wr-shipped-nocommit": {ID: "wr-shipped-nocommit", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}},
	}
	var stderr strings.Builder
	block := evaluateWorkRecordCloseGate([]string{"close", "wr-shipped-nocommit"}, panicOnGetStore{}, preFetched, t.TempDir(), "", nil, true, &stderr)
	if !block {
		t.Fatalf("expected block=true for shipped-without-commit, got false; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "work-record gate (enforced)") {
		t.Fatalf("expected enforced gate output, got %q", stderr.String())
	}
}

func TestWarnOnlyCloseEmitsTypedUsageTelemetry(t *testing.T) {
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "wr-warn", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped},
	}}, nil)
	journal := events.NewFake()
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate([]string{"close", "wr-warn"}, store, nil, t.TempDir(), "city:test", journal, false, &stderr); block {
		t.Fatal("warn-only compatibility close blocked")
	}
	got, err := journal.List(events.Filter{Type: "work.close.warn_only.used", Subject: "wr-warn"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("warn-only telemetry events = %d, want exactly one", len(got))
	}
}

func TestWarnOnlyCloseFailsClosedWhenUsageCannotBeConfirmed(t *testing.T) {
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "wr-warn", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	}}, nil)
	for _, test := range []struct {
		name     string
		provider events.Provider
	}{
		{name: "nil"},
		{name: "append failure", provider: events.NewFailFake()},
		{name: "readback failure", provider: unreadableWarnOnlyProvider{Fake: events.NewFake()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr strings.Builder
			if block := evaluateWorkRecordCloseGate([]string{"close", "wr-warn"}, store, nil, t.TempDir(), "city:test", test.provider, false, &stderr); !block {
				t.Fatalf("warn-only close proceeded without confirmed usage telemetry; stderr=%q", stderr.String())
			}
			if !strings.Contains(stderr.String(), "telemetry") {
				t.Fatalf("telemetry refusal not diagnosed: %q", stderr.String())
			}
		})
	}
}

func TestRunWorkRecordCloseGateWarnOnlyFailsClosedWhenJournalOpenFails(t *testing.T) {
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}
	bead := beads.Bead{ID: "wr-noop", Type: "task", Status: "open", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
	}}
	var stderr strings.Builder
	if block := runWorkRecordCloseGate([]string{"close", bead.ID}, t.TempDir(), "/nonexistent/no-event-journal", cfg, panicOnGetStore{}, map[string]beads.Bead{bead.ID: bead}, bdCloseTarget, &stderr); !block {
		t.Fatalf("warn-only close proceeded after journal open failure; stderr=%q", stderr.String())
	}
}

func TestResolvedByIDWarnOnlyCloseFailsClosedWhenUsageCannotBeConfirmed(t *testing.T) {
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}
	current := beads.Bead{ID: "wr-noop", Type: "task", Status: "open", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
	}}
	prospective := current
	prospective.Status = "closed"
	for _, test := range []struct {
		name     string
		provider events.Provider
	}{
		{name: "nil"},
		{name: "append failure", provider: events.NewFailFake()},
		{name: "readback failure", provider: unreadableWarnOnlyProvider{Fake: events.NewFake()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr strings.Builder
			if block := evaluateResolvedWorkCloseWithProvider(current, prospective, t.TempDir(), "city:test", cfg, bdCloseTarget, test.provider, &stderr); !block {
				t.Fatalf("by-ID warn-only close proceeded without confirmed telemetry; stderr=%q", stderr.String())
			}
		})
	}
}

func TestResolvedByIDWarnOnlyCloseProceedsAfterConfirmedUsage(t *testing.T) {
	warnOnly := true
	cfg := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}
	current := beads.Bead{ID: "wr-noop", Type: "task", Status: "open", Metadata: beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
	}}
	prospective := current
	prospective.Status = "closed"
	var stderr strings.Builder
	if block := evaluateResolvedWorkCloseWithProvider(current, prospective, t.TempDir(), "city:test", cfg, bdCloseTarget, events.NewFake(), &stderr); block {
		t.Fatalf("by-ID warn-only close blocked after confirmed telemetry; stderr=%q", stderr.String())
	}
}

// TestRunWorkRecordCloseGateReusesPreOpenedStore proves runWorkRecordCloseGate
// never calls openStoreAtForCity when handed a preOpened store — it's the IO
// wrapper's half of the dedup (evaluateWorkRecordCloseGate proves the
// preFetched-bead half above). cityPath is deliberately bogus: opening a
// real store at it would fail, causing the gate to fail open (block=false, no
// stderr) — indistinguishable from a no-op success. Asserting a violation
// fires instead proves preOpened/preFetched were actually used, not silently
// bypassed by a failed fallback open.
func TestRunWorkRecordCloseGateReusesPreOpenedStore(t *testing.T) {
	preFetched := map[string]beads.Bead{
		"wr-shipped-nocommit": {ID: "wr-shipped-nocommit", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}},
	}
	var stderr strings.Builder
	const bogusCityPath = "/nonexistent/does-not-exist"
	block := runWorkRecordCloseGate([]string{"close", "wr-shipped-nocommit"}, t.TempDir(), bogusCityPath, nil, panicOnGetStore{}, preFetched, bdCloseTarget, &stderr)
	if !block {
		t.Fatalf("expected block=true for shipped-without-commit, got false (fallback store open may have silently swallowed the preOpened store); stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "work-record gate (enforced)") {
		t.Fatalf("expected enforced gate output, got %q", stderr.String())
	}
}

func TestWorkRecordEnforceEnabled(t *testing.T) {
	t.Setenv("GC_WORK_RECORD_ENFORCE", "0")
	if !workRecordEnforceEnabled(&config.City{}, bdCloseTarget) {
		t.Fatal("absent migration setting must default to enforced mode; legacy env must not disable it")
	}
	warnOnly := true
	warnOnlyCity := &config.City{Beads: config.BeadsConfig{ShippedCloseWarnOnly: &warnOnly}}
	if workRecordEnforceEnabled(warnOnlyCity, bdCloseTarget) {
		t.Fatal("explicit shipped_close_warn_only=true must select warn-only mode for a bd-backed target")
	}
	if !workRecordEnforceEnabled(warnOnlyCity, workclose.CloseTarget{}) {
		t.Fatal("shipped_close_warn_only must not relax a natively fence-capable target")
	}
}

// bdCloseTarget is the mutation target of a close written through the pinned bd
// contract — the only target the bounded compatibility mode may relax.
var bdCloseTarget = workclose.CloseTarget{BDStoreContract: true}
