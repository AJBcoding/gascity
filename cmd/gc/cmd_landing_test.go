package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/landing"
)

const (
	landingCLIShaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	landingCLIShaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	landingCLIShaC = "cccccccccccccccccccccccccccccccccccccccc"
)

type landingCLIObserver struct {
	observation landing.RemoteObservation
	err         error
	calls       int
}

func (o *landingCLIObserver) Observe(_ context.Context, _, _, _ string) (landing.RemoteObservation, error) {
	o.calls++
	return o.observation, o.err
}

type landingCLIReceipt struct {
	WorkflowID            string   `json:"workflow_id"`
	IntegrationAttemptID  string   `json:"integration_attempt_id"`
	RepositoryPath        string   `json:"repository_path"`
	Repository            string   `json:"repository"`
	Remote                string   `json:"remote"`
	TargetRef             string   `json:"target_ref"`
	ExpectedTargetSHA     string   `json:"expected_target_sha"`
	ApprovedCandidateSHA  string   `json:"approved_candidate_sha"`
	ExpectedLandedSHA     string   `json:"expected_landed_sha"`
	PublicationMode       string   `json:"publication_mode"`
	IntegrationResultPath string   `json:"integration_result_path"`
	IntegrationResultHash string   `json:"integration_result_hash"`
	WorkBeadIDs           []string `json:"work_bead_ids"`
}

func writeLandingCLIReceipt(t *testing.T) (string, landingCLIReceipt) {
	t.Helper()
	root := t.TempDir()
	receipt := landingCLIReceipt{
		WorkflowID:            "build-1",
		IntegrationAttemptID:  "attempt-1",
		RepositoryPath:        root,
		Repository:            "https://example.invalid/acme/repo.git",
		Remote:                "origin",
		TargetRef:             "refs/heads/main",
		ExpectedTargetSHA:     landingCLIShaA,
		ApprovedCandidateSHA:  landingCLIShaB,
		ExpectedLandedSHA:     landingCLIShaB,
		PublicationMode:       "direct",
		IntegrationResultPath: filepath.Join(root, "integration-result.md"),
		IntegrationResultHash: "sha256:" + strings.Repeat("c", 64),
		WorkBeadIDs:           []string{"gc-a", "gc-b"},
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "receipt.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, receipt
}

func runLandingCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmdWithOptions(&stdout, &stderr, rootCommandOptions{})
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func installLandingCLIDependencies(t *testing.T, provider events.Provider, observer landing.RemoteObserver) {
	t.Helper()
	previousProvider := landingOpenEventsProvider
	previousObserver := landingNewObserver
	landingOpenEventsProvider = func(io.Writer, string) (events.Provider, int) { return provider, 0 }
	landingNewObserver = func() landing.RemoteObserver { return observer }
	t.Cleanup(func() {
		landingOpenEventsProvider = previousProvider
		landingNewObserver = previousObserver
	})
}

func TestLandingRecordCLIRecordsVerifiedReceiptAndPrintsTypedJSON(t *testing.T) {
	t.Setenv("GC_ALIAS", "landing-test-actor")
	path, receipt := writeLandingCLIReceipt(t)
	provider := events.NewFake()
	observer := &landingCLIObserver{observation: landing.RemoteObservation{
		Repository: receipt.Repository,
		SHA:        receipt.ExpectedLandedSHA,
	}}
	installLandingCLIDependencies(t, provider, observer)

	stdout, stderr, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json")
	if err != nil {
		t.Fatalf("Execute: %v, stderr=%q", err, stderr)
	}
	var result landingRecordJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON output %q: %v", stdout, err)
	}
	if result.SchemaVersion != "1" || !result.OK || !strings.HasPrefix(result.EventID, "gcl-") || result.ObservedLandedSHA != landingCLIShaB || result.AlreadyRecorded {
		t.Fatalf("result = %#v", result)
	}
	listed, err := provider.List(events.Filter{Type: events.DeliveryLanded})
	if err != nil || len(listed) != 1 {
		t.Fatalf("events=%d err=%v", len(listed), err)
	}
	if listed[0].Actor != "landing-test-actor" {
		t.Fatalf("event actor = %q", listed[0].Actor)
	}
}

func TestLandingRecordCLIReplayReportsAlreadyRecorded(t *testing.T) {
	path, receipt := writeLandingCLIReceipt(t)
	provider := events.NewFake()
	observer := &landingCLIObserver{observation: landing.RemoteObservation{Repository: receipt.Repository, SHA: landingCLIShaB}}
	installLandingCLIDependencies(t, provider, observer)

	if _, stderr, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json"); err != nil {
		t.Fatalf("first Execute: %v, stderr=%q", err, stderr)
	}
	stdout, stderr, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json")
	if err != nil {
		t.Fatalf("second Execute: %v, stderr=%q", err, stderr)
	}
	var result landingRecordJSONResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.AlreadyRecorded || observer.calls != 2 {
		t.Fatalf("result=%#v observer calls=%d", result, observer.calls)
	}
	listed, err := provider.List(events.Filter{Type: events.DeliveryLanded})
	if err != nil || len(listed) != 1 {
		t.Fatalf("events=%d err=%v", len(listed), err)
	}
}

func TestLandingRecordCLIRejectsOversizedReceiptBeforeRemoteLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, landing.MaxReceiptBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := events.NewFake()
	observer := &landingCLIObserver{}
	installLandingCLIDependencies(t, provider, observer)

	_, _, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json")
	if err == nil {
		t.Fatal("Execute error = nil")
	}
	if observer.calls != 0 {
		t.Fatalf("observer calls = %d, want 0", observer.calls)
	}
	listed, listErr := provider.List(events.Filter{})
	if listErr != nil || len(listed) != 0 {
		t.Fatalf("events=%d err=%v", len(listed), listErr)
	}
}

func TestLandingRecordCLIRejectsActorFromReceiptBeforeRemoteLookup(t *testing.T) {
	path, _ := writeLandingCLIReceipt(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["actor"] = "receipt-controlled-actor"
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	provider := events.NewFake()
	observer := &landingCLIObserver{}
	installLandingCLIDependencies(t, provider, observer)

	if _, _, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json"); err == nil {
		t.Fatal("Execute error = nil")
	}
	if observer.calls != 0 {
		t.Fatalf("observer calls = %d, want 0", observer.calls)
	}
}

func TestLandingRecordCLIRejectsTrailingJSONBeforeRemoteLookup(t *testing.T) {
	path, _ := writeLandingCLIReceipt(t)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n{}\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	provider := events.NewFake()
	observer := &landingCLIObserver{}
	installLandingCLIDependencies(t, provider, observer)

	if _, _, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json"); err == nil {
		t.Fatal("Execute error = nil")
	}
	if observer.calls != 0 {
		t.Fatalf("observer calls = %d, want 0", observer.calls)
	}
}

func TestLandingRecordCLITargetMismatchExitsNonzeroAndWritesNoEvent(t *testing.T) {
	path, receipt := writeLandingCLIReceipt(t)
	provider := events.NewFake()
	observer := &landingCLIObserver{observation: landing.RemoteObservation{Repository: receipt.Repository, SHA: landingCLIShaC}}
	installLandingCLIDependencies(t, provider, observer)

	_, _, err := runLandingCLI(t, "landing", "record", "--receipt", path, "--json")
	if err == nil {
		t.Fatal("Execute error = nil")
	}
	listed, listErr := provider.List(events.Filter{})
	if listErr != nil || len(listed) != 0 {
		t.Fatalf("events=%d err=%v", len(listed), listErr)
	}
}

func TestLandingRecordCLIHasNoPublishOrCloseFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCmdWithOptions(&stdout, &stderr, rootCommandOptions{})
	record, _, err := root.Find([]string{"landing", "record"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"push", "publish", "force", "open-pr", "close", "bead"} {
		if record.Flags().Lookup(name) != nil || record.InheritedFlags().Lookup(name) != nil {
			t.Errorf("unexpected capability flag --%s", name)
		}
	}
	if record.Flags().Lookup("receipt") == nil || record.Flags().Lookup("json") == nil {
		t.Fatalf("record flags = %v", record.Flags().FlagUsages())
	}
}
