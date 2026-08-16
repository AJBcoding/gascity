package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/landing"
)

type landingE2EFixture struct {
	repositoryPath string
	repository     string
	eventsPath     string
	receiptPath    string
	targetRef      string
	baseSHA        string
	candidateSHA   string
	resultHash     string
	receipt        landingCLIReceipt
}

type landingConfirmationFailureProvider struct {
	events.Provider
	listCalls int
}

func (p *landingConfirmationFailureProvider) List(filter events.Filter) ([]events.Event, error) {
	p.listCalls++
	if p.listCalls > 1 {
		return nil, errors.New("fixture event confirmation unavailable")
	}
	return p.Provider.List(filter)
}

func newLandingE2EFixture(t *testing.T) landingE2EFixture {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	landingTestGit(t, root, "init", "--bare", bare)
	landingTestGit(t, root, "init", "-b", "main", work)

	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landingTestGit(t, work, "add", "README.md")
	landingTestGit(t, work, "commit", "-m", "base A")
	baseSHA := landingTestGit(t, work, "rev-parse", "HEAD")
	landingTestGit(t, work, "remote", "add", "origin", bare)
	landingTestGit(t, work, "push", "origin", "HEAD:refs/heads/main")

	if err := os.WriteFile(filepath.Join(work, "candidate.txt"), []byte("candidate B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landingTestGit(t, work, "add", "candidate.txt")
	landingTestGit(t, work, "commit", "-m", "candidate B")
	candidateSHA := landingTestGit(t, work, "rev-parse", "HEAD")
	targetRef := "refs/heads/candidate-fixture"
	landingTestGit(t, work, "push", "origin", "HEAD:"+targetRef)

	resultPath := filepath.Join(root, "integration-result.json")
	result := []byte("{\"schema\":\"gc.build.integration-result.v1\"}\n")
	if err := os.WriteFile(resultPath, result, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(result)
	resultHash := "sha256:" + hex.EncodeToString(digest[:])
	receipt := landingCLIReceipt{
		WorkflowID:            "build-e2e",
		IntegrationAttemptID:  "attempt-e2e-1",
		RepositoryPath:        work,
		Repository:            filepath.Clean(bare),
		Remote:                "origin",
		TargetRef:             targetRef,
		ExpectedTargetSHA:     baseSHA,
		ApprovedCandidateSHA:  candidateSHA,
		ExpectedLandedSHA:     candidateSHA,
		PublicationMode:       "direct",
		IntegrationResultPath: resultPath,
		IntegrationResultHash: resultHash,
		WorkBeadIDs:           []string{"gas-e2e-a", "gas-e2e-b"},
	}
	receiptPath := filepath.Join(root, "landing-receipt.json")
	writeLandingE2EReceipt(t, receiptPath, receipt)

	return landingE2EFixture{
		repositoryPath: work,
		repository:     filepath.Clean(bare),
		eventsPath:     filepath.Join(root, "city", "events.jsonl"),
		receiptPath:    receiptPath,
		targetRef:      targetRef,
		baseSHA:        baseSHA,
		candidateSHA:   candidateSHA,
		resultHash:     resultHash,
		receipt:        receipt,
	}
}

func writeLandingE2EReceipt(t *testing.T, path string, receipt landingCLIReceipt) {
	t.Helper()
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func installLandingE2E(t *testing.T, eventsPath string, observer landing.RemoteObserver) {
	t.Helper()
	previousProvider := landingOpenEventsProvider
	previousObserver := landingNewObserver
	landingOpenEventsProvider = func(stderr io.Writer, _ string) (events.Provider, int) {
		provider, err := events.NewFileRecorder(eventsPath, stderr)
		if err != nil {
			t.Fatalf("NewFileRecorder: %v", err)
		}
		return provider, 0
	}
	landingNewObserver = func() landing.RemoteObserver { return observer }
	t.Cleanup(func() {
		landingOpenEventsProvider = previousProvider
		landingNewObserver = previousObserver
	})
}

func runLandingE2E(t *testing.T, receiptPath string) landingRecordJSONResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"landing", "record", "--receipt", receiptPath, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("gc landing record exit=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	validateJSONAgainstResultSchema(t, []string{"landing", "record"}, stdout.Bytes())
	var result landingRecordJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result %q: %v", stdout.String(), err)
	}
	return result
}

func runLandingE2EFailure(t *testing.T, receiptPath string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"landing", "record", "--receipt", receiptPath, "--json"}, &stdout, &stderr); code == 0 {
		t.Fatalf("gc landing record unexpectedly succeeded: stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
}

func TestLandingRecordEndToEnd(t *testing.T) {
	t.Setenv("GC_ALIAS", "landing-e2e-actor")
	fixture := newLandingE2EFixture(t)
	spy := &landingGitSpy{run: runLandingGitCommand}
	observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}
	installLandingE2E(t, fixture.eventsPath, observer)

	refsBefore := landingTestGit(t, fixture.repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	worktreeBefore := landingTestGit(t, fixture.repositoryPath, "status", "--porcelain=v1", "--untracked-files=all")
	first := runLandingE2E(t, fixture.receiptPath)
	second := runLandingE2E(t, fixture.receiptPath)

	if !first.OK || first.SchemaVersion != "1" || !strings.HasPrefix(first.EventID, "gcl-") {
		t.Fatalf("first result = %#v", first)
	}
	if second.EventID != first.EventID || !second.AlreadyRecorded {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if first.ObservedLandedSHA != fixture.candidateSHA || second.ObservedLandedSHA != fixture.candidateSHA {
		t.Fatalf("observed SHAs first=%q second=%q, want %q", first.ObservedLandedSHA, second.ObservedLandedSHA, fixture.candidateSHA)
	}

	remoteOutput := landingTestGit(t, fixture.repositoryPath, "ls-remote", "--exit-code", "origin", fixture.targetRef)
	remoteFields := strings.Fields(remoteOutput)
	if len(remoteFields) != 2 || remoteFields[0] != fixture.candidateSHA || remoteFields[1] != fixture.targetRef {
		t.Fatalf("authoritative remote output = %q", remoteOutput)
	}

	recorded, err := events.ReadFiltered(fixture.eventsPath, events.Filter{Type: events.DeliveryLanded})
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 {
		t.Fatalf("delivery.landed events = %d, want 1", len(recorded))
	}
	decoded, registered, err := events.DecodePayload(recorded[0].Type, recorded[0].Payload)
	if err != nil || !registered {
		t.Fatalf("DecodePayload registered=%v err=%v", registered, err)
	}
	payload := decoded.(events.DeliveryLandedPayload)
	if payload.EventID != first.EventID || payload.ObservedLandedSHA != fixture.candidateSHA || payload.ApprovedCandidateSHA != fixture.candidateSHA {
		t.Fatalf("landing payload = %#v", payload)
	}
	if !reflect.DeepEqual(payload.WorkBeadIDs, []string{"gas-e2e-a", "gas-e2e-b"}) {
		t.Fatalf("work_bead_ids = %#v", payload.WorkBeadIDs)
	}

	lsRemoteCalls := 0
	for _, call := range spy.calls {
		if strings.Join(call.args, " ") == "ls-remote --exit-code origin "+fixture.targetRef {
			lsRemoteCalls++
		}
	}
	if lsRemoteCalls != 2 {
		t.Fatalf("authoritative observations = %d, want 2", lsRemoteCalls)
	}

	refsAfter := landingTestGit(t, fixture.repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if refsAfter != refsBefore {
		t.Fatalf("remote refs changed\nbefore:\n%s\nafter:\n%s", refsBefore, refsAfter)
	}
	if mainSHA := landingTestGit(t, fixture.repository, "rev-parse", "refs/heads/main"); mainSHA != fixture.baseSHA {
		t.Fatalf("remote main = %q, want base %q", mainSHA, fixture.baseSHA)
	}
	worktreeAfter := landingTestGit(t, fixture.repositoryPath, "status", "--porcelain=v1", "--untracked-files=all")
	if worktreeAfter != worktreeBefore {
		t.Fatalf("repository worktree changed\nbefore=%q\nafter=%q", worktreeBefore, worktreeAfter)
	}
	if _, err := os.Stat(filepath.Join(fixture.repositoryPath, ".beads")); !os.IsNotExist(err) {
		t.Fatalf("landing command created bead state: %v", err)
	}

	t.Logf("remote=%s target=%s base=%s candidate=%s observed=%s event=%s result_hash=%s", fixture.repository, fixture.targetRef, fixture.baseSHA, fixture.candidateSHA, payload.ObservedLandedSHA, payload.EventID, fixture.resultHash)

	if err := os.WriteFile(filepath.Join(fixture.repositoryPath, "candidate-c.txt"), []byte("candidate C\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landingTestGit(t, fixture.repositoryPath, "add", "candidate-c.txt")
	landingTestGit(t, fixture.repositoryPath, "commit", "-m", "candidate C")
	movedSHA := landingTestGit(t, fixture.repositoryPath, "rev-parse", "HEAD")
	landingTestGit(t, fixture.repositoryPath, "push", "origin", "HEAD:"+fixture.targetRef)

	failureCases := []struct {
		name              string
		mutate            func(*landingCLIReceipt)
		wantGitCallsDelta int
	}{
		{
			name:              "target movement",
			mutate:            func(*landingCLIReceipt) {},
			wantGitCallsDelta: 3,
		},
		{
			name: "nonexistent ref",
			mutate: func(receipt *landingCLIReceipt) {
				receipt.TargetRef = "refs/heads/does-not-exist"
			},
			wantGitCallsDelta: 3,
		},
		{
			name: "direct mode candidate mismatch",
			mutate: func(receipt *landingCLIReceipt) {
				receipt.ExpectedLandedSHA = movedSHA
			},
			wantGitCallsDelta: 3,
		},
		{
			name: "pull request mode wrong landed SHA",
			mutate: func(receipt *landingCLIReceipt) {
				receipt.PublicationMode = "pull_request"
			},
			wantGitCallsDelta: 3,
		},
		{
			name: "malformed artifact hash",
			mutate: func(receipt *landingCLIReceipt) {
				receipt.ApprovedCandidateSHA = movedSHA
				receipt.ExpectedLandedSHA = movedSHA
				receipt.IntegrationResultHash = strings.Repeat("0", 64)
			},
			wantGitCallsDelta: 0,
		},
	}
	for _, test := range failureCases {
		t.Run(test.name, func(t *testing.T) {
			receipt := fixture.receipt
			test.mutate(&receipt)
			writeLandingE2EReceipt(t, fixture.receiptPath, receipt)
			callsBefore := len(spy.calls)
			refsBeforeFailure := landingTestGit(t, fixture.repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			runLandingE2EFailure(t, fixture.receiptPath)
			if delta := len(spy.calls) - callsBefore; delta != test.wantGitCallsDelta {
				t.Fatalf("Git calls = %d, want %d", delta, test.wantGitCallsDelta)
			}
			refsAfterFailure := landingTestGit(t, fixture.repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
			if refsAfterFailure != refsBeforeFailure {
				t.Fatalf("failed observation changed remote refs\nbefore:\n%s\nafter:\n%s", refsBeforeFailure, refsAfterFailure)
			}
			eventsAfterFailure, err := events.ReadFiltered(fixture.eventsPath, events.Filter{Type: events.DeliveryLanded})
			if err != nil || len(eventsAfterFailure) != 1 {
				t.Fatalf("delivery.landed events=%d err=%v, want original event only", len(eventsAfterFailure), err)
			}
		})
	}

	t.Run("unreadable event provider confirmation", func(t *testing.T) {
		receipt := fixture.receipt
		receipt.ApprovedCandidateSHA = movedSHA
		receipt.ExpectedLandedSHA = movedSHA
		writeLandingE2EReceipt(t, fixture.receiptPath, receipt)
		confirmationPath := filepath.Join(filepath.Dir(fixture.eventsPath), "confirmation-failure.jsonl")
		previousProvider := landingOpenEventsProvider
		var wrapped *landingConfirmationFailureProvider
		landingOpenEventsProvider = func(stderr io.Writer, _ string) (events.Provider, int) {
			provider, err := events.NewFileRecorder(confirmationPath, stderr)
			if err != nil {
				t.Fatalf("NewFileRecorder: %v", err)
			}
			wrapped = &landingConfirmationFailureProvider{Provider: provider}
			return wrapped, 0
		}
		t.Cleanup(func() { landingOpenEventsProvider = previousProvider })

		runLandingE2EFailure(t, fixture.receiptPath)
		if wrapped == nil || wrapped.listCalls != 2 {
			t.Fatalf("confirmation provider = %#v", wrapped)
		}
		attempted, err := events.ReadFiltered(confirmationPath, events.Filter{Type: events.DeliveryLanded})
		if err != nil || len(attempted) != 1 {
			t.Fatalf("attempted events=%d err=%v, want one unconfirmed append", len(attempted), err)
		}
	})
}
