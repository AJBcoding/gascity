package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// fileStoreCloseCity configures a stock file-provider city in a temp dir and
// seeds its scope-local FileStore (.gc/beads.json) with the supplied beads.
// This is the exact deployment shape the gas-dq28 review found unreachable
// through `gc bd`: the pack close command must work here, not only in the
// direct store composition the backend matrix exercises.
func fileStoreCloseCity(t *testing.T, seed ...beads.Bead) string {
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
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "file"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Isolate cwd so an ambient .beads/redirect cannot retarget the scope
	// (see TestResolveBdScopeTarget for rationale).
	setCwd(t, cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	store, err := openScopeLocalFileStore(cityDir)
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
	return cityDir
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
		{
			name:       "close with reason text",
			args:       []string{"close", "demo-w6", "--reason", "done"},
			wantStderr: "--reason",
		},
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
