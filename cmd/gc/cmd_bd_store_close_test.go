package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
)

// fileStoreCloseCity configures a stock file-provider city in a temp dir and
// seeds its scope-local FileStore (.gc/beads.json) with the supplied beads.
// This is the exact deployment shape the gas-dq28 review found unreachable
// through `gc bd`: the pack close command must work here, not only in the
// direct store composition the backend matrix exercises.
func fileStoreCloseCity(t *testing.T, seed ...beads.Bead) string {
	t.Helper()
	cityDir := fileStoreCloseCityRoot(t, `[workspace]
name = "demo"

[beads]
provider = "file"
`)
	seedFileStoreCloseScope(t, cityDir, seed...)
	return cityDir
}

// fileStoreCloseCityRoot writes one file-provider city.toml and isolates the
// process state every managed-close fixture depends on. It is shared by the
// city-scoped fixture above and the rig-scoped fixture below: the flag reset,
// env scrub, and cwd isolation are identical for both, and only the configured
// scopes differ.
func fileStoreCloseCityRoot(t *testing.T, cityTOML string) string {
	t.Helper()

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	t.Cleanup(func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	})
	cityFlag = ""
	rigFlag = ""

	// Scrub inherited beads env (see TestGcBdRejectsGCBeadsFileOverride):
	// a leaked GC_BEADS/GC_BEADS_SCOPE_ROOT from an agent session would
	// override the explicit file provider this fixture configures.
	clearInheritedBeadsEnv(t)

	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	// Isolate cwd so an ambient .beads/redirect cannot retarget the scope
	// (see TestResolveBdScopeTarget for rationale).
	setCwd(t, cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)
	return cityDir
}

// seedFileStoreCloseScope seeds one scope-local FileStore (<scope>/.gc/beads.json)
// with the supplied beads, honoring their explicit IDs.
func seedFileStoreCloseScope(t *testing.T, scopeRoot string, seed ...beads.Bead) {
	t.Helper()
	store, err := openScopeLocalFileStore(scopeRoot)
	if err != nil {
		t.Fatalf("open seed FileStore: %v", err)
	}
	store.HonorExplicitIDs = true
	for _, bead := range seed {
		created, err := store.Create(bead)
		if err != nil {
			t.Fatalf("seed %s: %v", bead.ID, err)
		}
		if created.ID != bead.ID {
			t.Fatalf("seed ID = %q, want %q", created.ID, bead.ID)
		}
	}
}

// fileStoreCloseBead re-reads one bead through a fresh store handle so
// assertions observe persisted state, not the seeding handle's memory.
func fileStoreCloseBead(t *testing.T, cityDir, id string) beads.Bead {
	t.Helper()
	store, err := openScopeLocalFileStore(cityDir)
	if err != nil {
		t.Fatalf("reopen FileStore: %v", err)
	}
	bead, err := store.Get(id)
	if err != nil {
		t.Fatalf("reopen/get %s: %v", id, err)
	}
	return bead
}

// TestGcBdFileStoreCloseValidNonShippedPersists is the pack-facing pipeline
// half of the Task 6 qualification: the exact command the worker formulas
// run (`gc bd close <id>`) must perform the managed close on a stock
// file-provider city instead of bouncing off the bd-provider rejection.
func TestGcBdFileStoreCloseValidNonShippedPersists(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-w1", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-w1"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	if persisted := fileStoreCloseBead(t, cityDir, "demo-w1"); persisted.Status != "closed" {
		t.Fatalf("persisted status = %q, want closed", persisted.Status)
	}
}

// TestGcBdFileStoreCloseReasonFromPackPersistsAtomically is the exact close
// command rendered by the integrated Superpowers code-review workflow. The
// FileStore front door must translate its reason into the repository's
// close_reason convention and commit status plus metadata in one revision.
func TestGcBdFileStoreCloseReasonFromPackPersistsAtomically(t *testing.T) {
	const reason = "Code-review feedback processed and approved."
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-reason-pack", Title: "structural review run", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.KindMetadataKey: "run"},
	})
	before := fileStoreCloseBead(t, cityDir, "demo-reason-pack")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-reason-pack", "--reason", reason}, &stdout, &stderr); got != 0 {
		t.Fatalf("exact derived close = %d, want 0; stderr=%q", got, stderr.String())
	}
	persisted := fileStoreCloseBead(t, cityDir, "demo-reason-pack")
	if persisted.Status != "closed" {
		t.Fatalf("persisted status = %q, want closed", persisted.Status)
	}
	if got := persisted.Metadata["close_reason"]; got != reason {
		t.Fatalf("persisted close_reason = %q, want %q", got, reason)
	}
	if persisted.Revision != before.Revision+1 {
		t.Fatalf("persisted revision = %d, want exactly one increment from %d", persisted.Revision, before.Revision)
	}
}

func TestGcBdFileStoreCloseReasonTrimsWhitespace(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-reason-trim", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-reason-trim", "--reason", "  completed cleanly  \n"}, &stdout, &stderr); got != 0 {
		t.Fatalf("reasoned close = %d, want 0; stderr=%q", got, stderr.String())
	}
	if got := fileStoreCloseBead(t, cityDir, "demo-reason-trim").Metadata["close_reason"]; got != "completed cleanly" {
		t.Fatalf("persisted close_reason = %q, want trimmed value", got)
	}
}

func TestGcBdFileStoreCloseReasonRejectsWhitespaceOnly(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-reason-empty", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})
	before := fileStoreCloseBead(t, cityDir, "demo-reason-empty")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-reason-empty", "--reason", "  \n"}, &stdout, &stderr); got == 0 {
		t.Fatalf("whitespace-only reason succeeded; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--reason") {
		t.Fatalf("stderr = %q, want --reason refusal", stderr.String())
	}
	after := fileStoreCloseBead(t, cityDir, "demo-reason-empty")
	if after.Status != before.Status || after.Revision != before.Revision || after.Metadata["close_reason"] != before.Metadata["close_reason"] {
		t.Fatalf("refusal mutated row: before=%+v after=%+v", before, after)
	}
}

// TestGcBdFileStoreUpdateStatusClosedStampsAndCloses covers the atomic
// stamp-and-close form the worker formulas render (`gc bd update <id>
// --set-metadata gc.work_outcome=… --status closed`): the metadata stamped in
// the same invocation must satisfy the gate and persist with the close.
func TestGcBdFileStoreUpdateStatusClosedStampsAndCloses(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-w2", Title: "work record", Type: "task", Status: "open",
	})

	var stdout, stderr bytes.Buffer
	args := []string{"update", "demo-w2", "--set-metadata", "gc.work_outcome=no-op", "--status", "closed"}
	if got := doBd(args, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(update --status closed) = %d, want 0; stderr=%q", got, stderr.String())
	}
	persisted := fileStoreCloseBead(t, cityDir, "demo-w2")
	if persisted.Status != "closed" {
		t.Fatalf("persisted status = %q, want closed", persisted.Status)
	}
	if persisted.Metadata[beadmeta.WorkOutcomeMetadataKey] != beadmeta.WorkOutcomeNoOp {
		t.Fatalf("persisted outcome = %q, want %q", persisted.Metadata[beadmeta.WorkOutcomeMetadataKey], beadmeta.WorkOutcomeNoOp)
	}
}

// TestGcBdFileStoreShippedCloseWithoutLandingEvidenceRefused proves the
// managed route enforces the same shipped-close contract as every other
// close adapter: branch reachability alone cannot close shipped work.
func TestGcBdFileStoreShippedCloseWithoutLandingEvidenceRefused(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.email", "store-close@example.com")
	runGit(t, repoDir, "config", "user.name", "Store Close")
	if err := os.WriteFile(filepath.Join(repoDir, "artifact.txt"), []byte("shipped\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "shipped work")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-w3", Title: "shipped work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{
			beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
			beadmeta.WorkCommitMetadataKey:  commit,
			beadmeta.WorkBranchMetadataKey:  "main",
			beadmeta.WorkDirMetadataKey:     repoDir,
		},
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-w3"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd(close shipped-without-evidence) = 0, want refusal; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "work-record gate (enforced)") {
		t.Fatalf("stderr = %q, want enforced gate refusal", stderr.String())
	}
	if !strings.Contains(stderr.String(), beadmeta.DeliveryStateMetadataKey) {
		t.Fatalf("stderr = %q, want landing-evidence violation naming %s", stderr.String(), beadmeta.DeliveryStateMetadataKey)
	}
	if persisted := fileStoreCloseBead(t, cityDir, "demo-w3"); persisted.Status == "closed" {
		t.Fatal("refused shipped close mutated the authoritative store")
	}
}

// The positive half of the shipped-close contract on the managed FileStore
// route. Everything above proves refusals; without the rows below, a canonical
// store ref this adapter computes WRONG (cmd_bd_store_close.go's
// workRecordCanonicalStoreRef call) would make every legitimate shipped
// FileStore close fail while every negative test stayed green, because a wrong
// ref refuses exactly like missing evidence does. So the evidence here is real
// and durable — an acknowledged delivery.landed event in the city's own
// events.jsonl, read back through the production journal — and each near miss
// differs from the passing case in exactly one bound field.

const (
	fileStoreLandingEventID = "gcl-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fileStoreLandedSHA      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fileStoreShippedCommit builds a one-commit repo whose HEAD is reachable on
// main, so the work-record contract's Git provenance rule is satisfied by a
// real commit rather than a stub.
func fileStoreShippedCommit(t *testing.T) (repoDir, commit string) {
	t.Helper()
	repoDir = t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.email", "store-close@example.com")
	runGit(t, repoDir, "config", "user.name", "Store Close")
	if err := os.WriteFile(filepath.Join(repoDir, "artifact.txt"), []byte("shipped\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "shipped work")
	return repoDir, strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
}

// fileStoreShippedMetadata is the complete metadata a shipped work record must
// carry for the landing event seeded by appendFileStoreLandingEvent to
// corroborate it.
func fileStoreShippedMetadata(repoDir, commit string) beads.StringMap {
	return beads.StringMap{
		beadmeta.WorkOutcomeMetadataKey:          beadmeta.WorkOutcomeShipped,
		beadmeta.WorkCommitMetadataKey:           commit,
		beadmeta.WorkBranchMetadataKey:           "main",
		beadmeta.WorkDirMetadataKey:              repoDir,
		beadmeta.DeliveryStateMetadataKey:        beadmeta.DeliveryStateLanded,
		beadmeta.DeliveryEventIDMetadataKey:      fileStoreLandingEventID,
		beadmeta.DeliverySourceCommitMetadataKey: commit,
		beadmeta.DeliveryLandedSHAMetadataKey:    fileStoreLandedSHA,
	}
}

// appendFileStoreLandingEvent writes the delivery.landed event to the city's
// real event journal through the durable append path — one acknowledged batch,
// file and directory synced — so the close gate reads it back exactly as it
// would in production instead of from an in-memory double.
func appendFileStoreLandingEvent(t *testing.T, cityDir string, bound events.DeliveryWorkRecordRef) {
	t.Helper()
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           fileStoreLandingEventID,
		Repository:        "https://example.invalid/acme/repo.git",
		TargetRef:         "refs/heads/main",
		ObservedLandedSHA: fileStoreLandedSHA,
		WorkRecords:       []events.DeliveryWorkRecordRef{bound},
		VerifiedAt:        "2026-08-17T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal landing payload: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("create city runtime dir: %v", err)
	}
	recorder, err := events.NewFileRecorder(filepath.Join(cityDir, ".gc", "events.jsonl"), io.Discard)
	if err != nil {
		t.Fatalf("open city event journal: %v", err)
	}
	if err := recorder.AppendBatch([]events.Event{{
		Type:    events.DeliveryLanded,
		Actor:   "gc.landing",
		Subject: fileStoreLandingEventID,
		Payload: payload,
	}}); err != nil {
		t.Fatalf("append landing event: %v", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("close city event journal: %v", err)
	}
}

// TestGcBdFileStoreShippedCloseWithExactLandingEvidenceSucceeds is the missing
// positive: a shipped work record whose durable evidence names THIS store,
// THIS bead, and THIS commit closes through the real adapter. It is the only
// row that fails if the adapter's canonical store ref stops matching the ref
// the landing evidence is written with.
func TestGcBdFileStoreShippedCloseWithExactLandingEvidenceSucceeds(t *testing.T) {
	repoDir, commit := fileStoreShippedCommit(t)
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-shipped", Title: "shipped work record", Type: "task", Status: "open",
		Metadata: fileStoreShippedMetadata(repoDir, commit),
	})
	appendFileStoreLandingEvent(t, cityDir, events.DeliveryWorkRecordRef{
		StoreRef: "city:demo", BeadID: "demo-shipped", WorkCommit: commit,
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-shipped"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(close shipped-with-evidence) = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "work-record gate") {
		t.Fatalf("stderr = %q, want no gate violation for exactly bound evidence", stderr.String())
	}
	if persisted := fileStoreCloseBead(t, cityDir, "demo-shipped"); persisted.Status != "closed" {
		t.Fatalf("persisted status = %q, want closed", persisted.Status)
	}
}

// TestGcBdFileStoreShippedCloseRefusesNearMissLandingEvidence varies exactly
// one bound field of the evidence the test above proves sufficient. Each row
// must be refused through the real adapter: evidence bound to another store,
// another bead, or another commit is not evidence for this close.
func TestGcBdFileStoreShippedCloseRefusesNearMissLandingEvidence(t *testing.T) {
	for _, test := range []struct {
		name  string
		bound func(exact events.DeliveryWorkRecordRef) events.DeliveryWorkRecordRef
	}{
		{
			name: "wrong store ref",
			bound: func(exact events.DeliveryWorkRecordRef) events.DeliveryWorkRecordRef {
				exact.StoreRef = "rig:elsewhere"
				return exact
			},
		},
		{
			name: "wrong bead id",
			bound: func(exact events.DeliveryWorkRecordRef) events.DeliveryWorkRecordRef {
				exact.BeadID = "demo-other"
				return exact
			},
		},
		{
			name: "wrong commit",
			bound: func(exact events.DeliveryWorkRecordRef) events.DeliveryWorkRecordRef {
				exact.WorkCommit = strings.Repeat("c", 40)
				return exact
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoDir, commit := fileStoreShippedCommit(t)
			cityDir := fileStoreCloseCity(t, beads.Bead{
				ID: "demo-shipped", Title: "shipped work record", Type: "task", Status: "open",
				Metadata: fileStoreShippedMetadata(repoDir, commit),
			})
			appendFileStoreLandingEvent(t, cityDir, test.bound(events.DeliveryWorkRecordRef{
				StoreRef: "city:demo", BeadID: "demo-shipped", WorkCommit: commit,
			}))

			var stdout, stderr bytes.Buffer
			if got := doBd([]string{"close", "demo-shipped"}, &stdout, &stderr); got == 0 {
				t.Fatalf("doBd(close with %s) = 0, want refusal; stdout=%q", test.name, stdout.String())
			}
			if !strings.Contains(stderr.String(), "is not bound to store") {
				t.Fatalf("stderr = %q, want an unbound-evidence refusal", stderr.String())
			}
			if persisted := fileStoreCloseBead(t, cityDir, "demo-shipped"); persisted.Status == "closed" {
				t.Fatalf("%s closed the record anyway", test.name)
			}
		})
	}
}

// TestGcBdFileStoreRigScopedShippedCloseBindsRigStoreRef exercises store-ref
// ROUTING rather than inferring it from the single-scope case: the same close
// under a rig scope must be checked against "rig:<name>", so evidence bound to
// the city ref is refused and evidence bound to the rig ref succeeds. Both rows
// run against the same fixture, which is what makes the pair load-bearing.
func TestGcBdFileStoreRigScopedShippedCloseBindsRigStoreRef(t *testing.T) {
	for _, test := range []struct {
		name      string
		storeRef  string
		wantClose bool
	}{
		{name: "bound to the rig store", storeRef: "rig:" + fileStoreCloseRigName, wantClose: true},
		{name: "bound to the city store", storeRef: "city:demo", wantClose: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoDir, commit := fileStoreShippedCommit(t)
			cityDir, rigDir := fileStoreCloseRigCity(t, beads.Bead{
				ID: "wf-1", Title: "rig-scoped shipped work record", Type: "task", Status: "open",
				Metadata: fileStoreShippedMetadata(repoDir, commit),
			})
			appendFileStoreLandingEvent(t, cityDir, events.DeliveryWorkRecordRef{
				StoreRef: test.storeRef, BeadID: "wf-1", WorkCommit: commit,
			})

			var stdout, stderr bytes.Buffer
			code := doBd([]string{"--rig", fileStoreCloseRigName, "close", "wf-1"}, &stdout, &stderr)
			persisted := fileStoreCloseBead(t, rigDir, "wf-1")
			if test.wantClose {
				if code != 0 {
					t.Fatalf("rig-scoped close = %d, want 0; stderr=%q", code, stderr.String())
				}
				if persisted.Status != "closed" {
					t.Fatalf("persisted status = %q, want closed", persisted.Status)
				}
				return
			}
			if code == 0 {
				t.Fatalf("rig-scoped close accepted city-bound evidence; stdout=%q", stdout.String())
			}
			if !strings.Contains(stderr.String(), "is not bound to store") {
				t.Fatalf("stderr = %q, want an unbound-evidence refusal", stderr.String())
			}
			if persisted.Status == "closed" {
				t.Fatal("city-bound evidence closed a rig-scoped work record")
			}
		})
	}
}

const fileStoreCloseRigName = "workflows"

// fileStoreCloseRigCity is fileStoreCloseCity with one bound rig whose own
// scope-local FileStore holds the seeded beads, so `gc bd --rig` resolves a rig
// scope and the managed close computes "rig:<name>" as its canonical store ref.
func fileStoreCloseRigCity(t *testing.T, seed ...beads.Bead) (cityDir, rigDir string) {
	t.Helper()
	cityDir = fileStoreCloseCityRoot(t, `[workspace]
name = "demo"

[beads]
provider = "file"

[[rigs]]
name = "`+fileStoreCloseRigName+`"
path = "rigs/`+fileStoreCloseRigName+`"
prefix = "wf"
`)
	rigDir = filepath.Join(cityDir, "rigs", fileStoreCloseRigName)
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("create rig dir: %v", err)
	}
	seedFileStoreCloseScope(t, rigDir, seed...)
	return cityDir, rigDir
}

// TestGcBdFileStoreAlreadyClosedReplayIsIdempotent pins the crash-replay
// contract: re-running the close the worker already completed succeeds
// without mutating the persisted row again.
func TestGcBdFileStoreAlreadyClosedReplayIsIdempotent(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-w4", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-w4"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(first close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	closed := fileStoreCloseBead(t, cityDir, "demo-w4")
	if closed.Status != "closed" {
		t.Fatalf("first close persisted status = %q, want closed", closed.Status)
	}

	stdout.Reset()
	stderr.Reset()
	if got := doBd([]string{"close", "demo-w4"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(replay close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	replayed := fileStoreCloseBead(t, cityDir, "demo-w4")
	if replayed.Status != "closed" {
		t.Fatalf("replay status = %q, want closed", replayed.Status)
	}
	if replayed.Revision != closed.Revision {
		t.Fatalf("replay revision = %d, want unchanged %d", replayed.Revision, closed.Revision)
	}
}

// TestGcBdFileStoreAlreadyClosedReasonReplayDoesNotRewrite pins the reasoned
// crash-replay contract: a later invocation cannot replace the audit reason or
// bump the revision after the first atomic close committed.
func TestGcBdFileStoreAlreadyClosedReasonReplayDoesNotRewrite(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-reason-replay", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-reason-replay", "--reason", "first durable reason"}, &stdout, &stderr); got != 0 {
		t.Fatalf("first reasoned close = %d, want 0; stderr=%q", got, stderr.String())
	}
	closed := fileStoreCloseBead(t, cityDir, "demo-reason-replay")

	stdout.Reset()
	stderr.Reset()
	if got := doBd([]string{"close", "demo-reason-replay", "--reason", "replacement must not land"}, &stdout, &stderr); got != 0 {
		t.Fatalf("reasoned replay = %d, want 0; stderr=%q", got, stderr.String())
	}
	replayed := fileStoreCloseBead(t, cityDir, "demo-reason-replay")
	if replayed.Metadata["close_reason"] != "first durable reason" {
		t.Fatalf("replay close_reason = %q, want original", replayed.Metadata["close_reason"])
	}
	if replayed.Revision != closed.Revision {
		t.Fatalf("replay revision = %d, want unchanged %d", replayed.Revision, closed.Revision)
	}
}

// TestGcBdFileStoreNonCloseVerbsStayRejected keeps the managed route scoped
// to the close verbs the gate recognizes: everything else on a non-bd
// provider must keep the existing typed rejection so the qualification
// matrix's provider boundary stays meaningful.
func TestGcBdFileStoreNonCloseVerbsStayRejected(t *testing.T) {
	fileStoreCloseCity(t, beads.Bead{
		ID: "demo-w5", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})

	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "list", args: []string{"list"}},
		{name: "show", args: []string{"show", "demo-w5"}},
		{name: "update without close", args: []string{"update", "demo-w5", "--set-metadata", "k=v"}},
		{name: "reopen", args: []string{"reopen", "demo-w5"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := doBd(test.args, &stdout, &stderr); got == 0 {
				t.Fatalf("doBd(%v) = 0, want provider rejection", test.args)
			}
			if !strings.Contains(stderr.String(), "only supported for bd-backed beads providers") {
				t.Fatalf("stderr = %q, want provider rejection", stderr.String())
			}
		})
	}
}

// TestGcBdFileStoreCloseRefusesUnservableSpellings pins the fail-closed edge
// of the managed route: a close whose spelling the in-process contract cannot
// represent is refused with the reason named, never half-served or forwarded.
func TestGcBdFileStoreCloseRefusesUnservableSpellings(t *testing.T) {
	cityDir := fileStoreCloseCity(t,
		beads.Bead{
			ID: "demo-w6", Title: "work record", Type: "task", Status: "open",
			Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
		},
		beads.Bead{
			ID: "demo-w7", Title: "work record", Type: "task", Status: "open",
			Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
		},
	)

	for _, test := range []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "batched close",
			args:       []string{"close", "demo-w6", "demo-w7"},
			wantStderr: "one bead per invocation",
		},
		{name: "close with reason equals spelling", args: []string{"close", "demo-w6", "--reason=done"}, wantStderr: "--reason"},
		{name: "close with short reason spelling", args: []string{"close", "demo-w6", "-r", "done"}, wantStderr: "-r"},
		{name: "close with reason file", args: []string{"close", "demo-w6", "--reason-file", "reason.txt"}, wantStderr: "--reason-file"},
		{name: "close with session", args: []string{"close", "demo-w6", "--session", "review-session"}, wantStderr: "--session"},
		{
			name:       "status close with notes",
			args:       []string{"update", "demo-w6", "--status", "closed", "--notes", "done"},
			wantStderr: "--notes",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := doBd(test.args, &stdout, &stderr); got == 0 {
				t.Fatalf("doBd(%v) = 0, want refusal", test.args)
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want mention of %q", stderr.String(), test.wantStderr)
			}
		})
	}
	for _, id := range []string{"demo-w6", "demo-w7"} {
		if persisted := fileStoreCloseBead(t, cityDir, id); persisted.Status == "closed" {
			t.Fatalf("%s mutated despite refusal", id)
		}
	}
}

// TestDoBdStoreReasonedCloseCASRefusalLeavesStatusAndReasonUnchanged proves the
// close cannot be decomposed into an unfenced reason write followed by a
// fenced lifecycle write. The wrapper returns a stale read after committing an
// unrelated concurrent update; the one reason+status CAS must lose cleanly.
func TestDoBdStoreReasonedCloseCASRefusalLeavesStatusAndReasonUnchanged(t *testing.T) {
	cityDir := fileStoreCloseCity(t, beads.Bead{
		ID: "demo-reason-race", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{
			beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
			"close_reason":                  "existing reason",
		},
	})
	store, err := openScopeLocalFileStore(cityDir)
	if err != nil {
		t.Fatalf("open race FileStore: %v", err)
	}
	raceStore := &closeReasonRaceStore{Store: store, writer: store}
	closed := "closed"
	op := bdByIDOp{
		Verb: bdByIDClose,
		ID:   "demo-reason-race",
		Update: beads.UpdateOpts{
			Status:   &closed,
			Metadata: beads.StringMap{"close_reason": "replacement reason"},
		},
	}

	var stdout, stderr bytes.Buffer
	if got := doBdStoreCloseResolved(op, raceStore, cityDir, cityDir, "city:demo", "file", &config.City{}, &stdout, &stderr); got == 0 {
		t.Fatalf("contested reasoned close succeeded; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "changed concurrently") {
		t.Fatalf("stderr = %q, want concurrent-change refusal", stderr.String())
	}
	persisted := fileStoreCloseBead(t, cityDir, "demo-reason-race")
	if persisted.Status != "open" {
		t.Fatalf("persisted status = %q, want open", persisted.Status)
	}
	if got := persisted.Metadata["close_reason"]; got != "existing reason" {
		t.Fatalf("persisted close_reason = %q, want unchanged", got)
	}
}

type closeReasonRaceStore struct {
	beads.Store
	writer   beads.ConditionalWriter
	injected bool
}

func (s *closeReasonRaceStore) Get(id string) (beads.Bead, error) {
	bead, err := s.Store.Get(id)
	if err != nil || s.injected {
		return bead, err
	}
	s.injected = true
	if err := s.writer.UpdateIfMatch(id, bead.Revision, beads.UpdateOpts{Metadata: beads.StringMap{"concurrent": "won"}}); err != nil {
		return beads.Bead{}, err
	}
	return bead, nil
}

func (s *closeReasonRaceStore) UpdateIfMatch(id string, expectedRevision int64, opts beads.UpdateOpts) error {
	return s.writer.UpdateIfMatch(id, expectedRevision, opts)
}

func (s *closeReasonRaceStore) CloseIfMatch(id string, expectedRevision int64) error {
	return s.writer.CloseIfMatch(id, expectedRevision)
}

func (s *closeReasonRaceStore) DeleteIfMatch(id string, expectedRevision int64) error {
	return s.writer.DeleteIfMatch(id, expectedRevision)
}

func (s *closeReasonRaceStore) CompareAndSetMetadataKey(id, key, expected, next string) (bool, error) {
	return s.writer.CompareAndSetMetadataKey(id, key, expected, next)
}

// TestDoBdStoreCloseFailsClosedWithoutConditionalWriter pins the unfenced-
// write ban: a store that cannot fence the mutation on the evaluated revision
// refuses the close with a typed message instead of degrading to Close/Update.
func TestDoBdStoreCloseFailsClosedWithoutConditionalWriter(t *testing.T) {
	store := beads.NewMemStore()
	store.HonorExplicitIDs = true
	if _, err := store.Create(beads.Bead{
		ID: "demo-w8", Title: "work record", Type: "task", Status: "open",
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	op, rejected, ok := parseBdByIDCloseArgs(bdByIDClose, []string{"demo-w8"})
	if !ok {
		t.Fatalf("parse close args rejected %q", rejected)
	}
	var stdout, stderr bytes.Buffer
	// hideConditionalWriter blocks the ConditionalWriter assertion the same
	// way an incapable provider store does, without a resolve target to follow.
	code := doBdStoreCloseResolved(op, hideConditionalWriter{Store: store}, t.TempDir(), t.TempDir(), "city:test", "file", &config.City{}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("close through a fenceless store succeeded; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "revision-fenced") {
		t.Fatalf("stderr = %q, want typed fenceless refusal", stderr.String())
	}
	current, err := store.Get("demo-w8")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.Status == "closed" {
		t.Fatal("fenceless refusal still mutated the store")
	}
}

// hideConditionalWriter embeds the Store interface so optional capabilities
// (ConditionalWriter included) are not promoted, and declares no resolve
// target — modeling a provider store with no fenced-write support.
type hideConditionalWriter struct {
	beads.Store
}
