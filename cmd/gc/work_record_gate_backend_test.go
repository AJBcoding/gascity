package main

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/workclose"
)

const managedMatrixStoreRef = "city:test"

type managedMutationBackend struct {
	name string
	open func(t *testing.T) (beads.Store, func() beads.Store)
}

func managedMutationBackends() []managedMutationBackend {
	return []managedMutationBackend{
		{
			name: "MemStore",
			open: func(t *testing.T) (beads.Store, func() beads.Store) {
				t.Helper()
				store := beads.NewMemStore()
				store.HonorExplicitIDs = true
				return store, func() beads.Store { return store }
			},
		},
		{
			name: "FileStore",
			open: func(t *testing.T) (beads.Store, func() beads.Store) {
				t.Helper()
				path := filepath.Join(t.TempDir(), "beads.json")
				store, err := beads.OpenFileStore(fsys.OSFS{}, path)
				if err != nil {
					t.Fatalf("open FileStore: %v", err)
				}
				store.HonorExplicitIDs = true
				return store, func() beads.Store {
					reopened, err := beads.OpenFileStore(fsys.OSFS{}, path)
					if err != nil {
						t.Fatalf("reopen FileStore: %v", err)
					}
					return reopened
				}
			},
		},
	}
}

func TestManagedMutationBackendMatrix(t *testing.T) {
	backends := managedMutationBackends()
	if len(backends) != 2 {
		t.Fatalf("managed mutation backends = %d, want MemStore and FileStore", len(backends))
	}

	for _, backend := range backends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			t.Run("authoritative close persists", func(t *testing.T) {
				store, reopen := backend.open(t)
				current := createManagedMatrixBead(t, store, "gc-matrix-close", map[string]string{
					beadmeta.WorkDirMetadataKey:     t.TempDir(),
					beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
				})

				prospective := current
				prospective.Status = "closed"
				assertManagedCloseAllowed(t, store, current, prospective, nil)
				if err := managedConditionalWriter(t, store).CloseIfMatch(current.ID, current.Revision); err != nil {
					t.Fatalf("CloseIfMatch: %v", err)
				}

				persisted := reopenManagedMatrixBead(t, reopen, current.ID)
				if persisted.Status != "closed" {
					t.Fatalf("persisted status = %q, want closed", persisted.Status)
				}
			})

			t.Run("exact shipped update persists and replay is idempotent", func(t *testing.T) {
				store, reopen := backend.open(t)
				current := createManagedMatrixBead(t, store, "gc-matrix-shipped", nil)
				repoDir, metadata, journal := managedLandingEvidence(t, current.ID, managedMatrixStoreRef)
				metadata[beadmeta.WorkDirMetadataKey] = repoDir
				closed := "closed"
				opts := beads.UpdateOpts{Status: &closed, Metadata: metadata}
				prospective := workclose.ProjectUpdate(current, opts)
				assertManagedCloseAllowed(t, store, current, prospective, journal)
				if err := managedConditionalWriter(t, store).UpdateIfMatch(current.ID, current.Revision, opts); err != nil {
					t.Fatalf("UpdateIfMatch: %v", err)
				}

				replayStore := reopen()
				persisted := getManagedMatrixBead(t, replayStore, current.ID)
				if persisted.Status != "closed" || persisted.Metadata[beadmeta.WorkOutcomeMetadataKey] != beadmeta.WorkOutcomeShipped {
					t.Fatalf("persisted shipped close = status %q outcome %q", persisted.Status, persisted.Metadata[beadmeta.WorkOutcomeMetadataKey])
				}
				beforeReplayRevision := persisted.Revision
				assertManagedCloseAllowed(t, replayStore, persisted, persisted, journal)
				writer := managedConditionalWriter(t, replayStore)
				if err := writer.CloseIfMatch(current.ID, persisted.Revision); err != nil {
					t.Fatalf("already-closed CloseIfMatch: %v", err)
				}
				afterReplay := reopenManagedMatrixBead(t, reopen, current.ID)
				if afterReplay.Status != persisted.Status {
					t.Fatalf("replay status = %q, want unchanged %q", afterReplay.Status, persisted.Status)
				}
				if !maps.Equal(afterReplay.Metadata, persisted.Metadata) {
					t.Fatalf("replay metadata changed: got %#v, want %#v", afterReplay.Metadata, persisted.Metadata)
				}
				if afterReplay.Revision != beforeReplayRevision {
					t.Fatalf("replay revision = %d, want unchanged %d", afterReplay.Revision, beforeReplayRevision)
				}
			})

			t.Run("stale revision refuses mutation", func(t *testing.T) {
				store, reopen := backend.open(t)
				current := createManagedMatrixBead(t, store, "gc-matrix-stale", map[string]string{
					beadmeta.WorkDirMetadataKey:     t.TempDir(),
					beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
				})
				prospective := current
				prospective.Status = "closed"
				assertManagedCloseAllowed(t, store, current, prospective, nil)

				concurrent := "concurrent writer"
				writer := managedConditionalWriter(t, store)
				if err := writer.UpdateIfMatch(current.ID, current.Revision, beads.UpdateOpts{Description: &concurrent}); err != nil {
					t.Fatalf("concurrent UpdateIfMatch: %v", err)
				}
				if err := writer.CloseIfMatch(current.ID, current.Revision); !beads.IsPreconditionFailed(err) {
					t.Fatalf("stale CloseIfMatch error = %v, want precondition failure", err)
				}

				persisted := reopenManagedMatrixBead(t, reopen, current.ID)
				if persisted.Status == "closed" || persisted.Description != concurrent {
					t.Fatalf("stale refusal persisted status=%q description=%q", persisted.Status, persisted.Description)
				}
			})

			t.Run("multi ID refusal is all or nothing", func(t *testing.T) {
				store, reopen := backend.open(t)
				valid := createManagedMatrixBead(t, store, "gc-matrix-valid", map[string]string{
					beadmeta.WorkDirMetadataKey:     t.TempDir(),
					beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
				})
				invalid := createManagedMatrixBead(t, store, "gc-matrix-invalid", map[string]string{
					beadmeta.WorkDirMetadataKey:     t.TempDir(),
					beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
					beadmeta.WorkCommitMetadataKey:  strings.Repeat("a", 40),
				})
				var stderr strings.Builder
				blocked := evaluateWorkRecordCloseGate(
					[]string{"close", valid.ID, invalid.ID},
					store,
					nil,
					t.TempDir(),
					managedMatrixStoreRef,
					nil,
					true,
					&stderr,
				)
				if !blocked {
					t.Fatalf("multi-ID close allowed; stderr=%q", stderr.String())
				}

				for _, id := range []string{valid.ID, invalid.ID} {
					persisted := reopenManagedMatrixBead(t, reopen, id)
					if persisted.Status == "closed" {
						t.Fatalf("%s mutated despite batch refusal", id)
					}
				}
			})

			t.Run("event journal read failure refuses mutation", func(t *testing.T) {
				store, reopen := backend.open(t)
				current := createManagedMatrixBead(t, store, "gc-matrix-journal", nil)
				repoDir, metadata, _ := managedLandingEvidence(t, current.ID, managedMatrixStoreRef)
				metadata[beadmeta.WorkDirMetadataKey] = repoDir
				seed := managedConditionalWriter(t, store)
				if err := seed.UpdateIfMatch(current.ID, current.Revision, beads.UpdateOpts{Metadata: metadata}); err != nil {
					t.Fatalf("seed metadata: %v", err)
				}
				current = reopenManagedMatrixBead(t, reopen, current.ID)
				prospective := current
				prospective.Status = "closed"
				provider := &unreadableManagedCloseEvidence{Fake: events.NewFake()}
				var stderr strings.Builder
				if !evaluateResolvedWorkCloseWithProvider(current, prospective, t.TempDir(), managedMatrixStoreRef, &config.City{}, workRecordCloseTargetForStore(store), provider, &stderr) {
					t.Fatalf("journal failure allowed mutation; stderr=%q", stderr.String())
				}

				persisted := reopenManagedMatrixBead(t, reopen, current.ID)
				if persisted.Status == "closed" {
					t.Fatal("journal failure mutated authoritative store")
				}
			})

			t.Run("duplicate copy mutates only supplied authority", func(t *testing.T) {
				authoritative, reopenAuthoritative := backend.open(t)
				retained, reopenRetained := backend.open(t)
				id := "gc-matrix-dual"
				current := createManagedMatrixBead(t, authoritative, id, map[string]string{
					beadmeta.WorkDirMetadataKey:     t.TempDir(),
					beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
				})
				createManagedMatrixBead(t, retained, id, map[string]string{
					beadmeta.WorkDirMetadataKey:     t.TempDir(),
					beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp,
				})
				prospective := current
				prospective.Status = "closed"
				assertManagedCloseAllowed(t, authoritative, current, prospective, nil)
				if err := managedConditionalWriter(t, authoritative).CloseIfMatch(id, current.Revision); err != nil {
					t.Fatalf("authoritative CloseIfMatch: %v", err)
				}

				if got := reopenManagedMatrixBead(t, reopenAuthoritative, id); got.Status != "closed" {
					t.Fatalf("authoritative status = %q, want closed", got.Status)
				}
				if got := reopenManagedMatrixBead(t, reopenRetained, id); got.Status == "closed" {
					t.Fatalf("retained duplicate status = %q, want open", got.Status)
				}
			})
		})
	}
}

func createManagedMatrixBead(t *testing.T, store beads.Store, id string, metadata map[string]string) beads.Bead {
	t.Helper()
	created, err := store.Create(beads.Bead{
		ID:       id,
		Title:    "managed mutation matrix",
		Type:     "task",
		Status:   "open",
		Metadata: metadata,
	})
	if err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if created.ID != id {
		t.Fatalf("created ID = %q, want %q", created.ID, id)
	}
	return created
}

func reopenManagedMatrixBead(t *testing.T, reopen func() beads.Store, id string) beads.Bead {
	t.Helper()
	return getManagedMatrixBead(t, reopen(), id)
}

func getManagedMatrixBead(t *testing.T, store beads.Store, id string) beads.Bead {
	t.Helper()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("reopen/get %s: %v", id, err)
	}
	return got
}

func managedConditionalWriter(t *testing.T, store beads.Store) beads.ConditionalWriter {
	t.Helper()
	writer, ok := beads.ConditionalWriterFor(store)
	if !ok {
		t.Fatalf("%T does not implement beads.ConditionalWriter", store)
	}
	return writer
}

// assertManagedCloseAllowed passes the store the matrix actually opened as the
// close target, which is what an in-process route does: the compatibility
// decision reads the resolved store, never city config.
func assertManagedCloseAllowed(t *testing.T, store beads.Store, current, prospective beads.Bead, evidence events.Provider) {
	t.Helper()
	var stderr strings.Builder
	if evaluateResolvedWorkCloseWithProvider(current, prospective, t.TempDir(), managedMatrixStoreRef, &config.City{}, workRecordCloseTargetForStore(store), evidence, &stderr) {
		t.Fatalf("managed close blocked: %s", stderr.String())
	}
}

func managedLandingEvidence(t *testing.T, beadID, storeRef string) (string, map[string]string, *events.Fake) {
	t.Helper()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.email", "matrix@example.com")
	runGit(t, repoDir, "config", "user.name", "Managed Matrix")
	if err := os.WriteFile(filepath.Join(repoDir, "artifact.txt"), []byte("qualified\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "qualified landing")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	eventID := "gcl-" + strings.Repeat("a", 64)
	landedSHA := strings.Repeat("b", 40)
	payload, err := json.Marshal(events.DeliveryLandedPayload{
		EventID:           eventID,
		ObservedLandedSHA: landedSHA,
		WorkRecords: []events.DeliveryWorkRecordRef{{
			StoreRef: storeRef, BeadID: beadID, WorkCommit: commit,
		}},
	})
	if err != nil {
		t.Fatalf("marshal landing payload: %v", err)
	}
	journal := events.NewFake()
	journal.Record(events.Event{
		Type:    events.DeliveryLanded,
		Subject: eventID,
		Payload: payload,
	})
	return repoDir, map[string]string{
		beadmeta.WorkOutcomeMetadataKey:          beadmeta.WorkOutcomeShipped,
		beadmeta.WorkCommitMetadataKey:           commit,
		beadmeta.WorkBranchMetadataKey:           "main",
		beadmeta.DeliveryStateMetadataKey:        "landed",
		beadmeta.DeliveryEventIDMetadataKey:      eventID,
		beadmeta.DeliverySourceCommitMetadataKey: commit,
		beadmeta.DeliveryLandedSHAMetadataKey:    landedSHA,
	}, journal
}

type unreadableManagedCloseEvidence struct {
	*events.Fake
}

func (u *unreadableManagedCloseEvidence) List(events.Filter) ([]events.Event, error) {
	return nil, errors.New("managed matrix event journal unavailable")
}
