package beads

// Rollback-compatibility probe for bead metadata (gas-a2td, compat surface C).
//
// The question this file answers: if a newer binary writes lifecycle evidence
// under metadata keys this build has never heard of, and then the fleet rolls
// back to this build, does this build stamp over that evidence?
//
// It cannot, and the reason is structural rather than a matter of this build
// recognizing the keys: every metadata write route in the tree is a per-key
// merge. Bead.Metadata is a StringMap whose underlying type is
// map[string]string, so a key this build has no constant for is an ordinary map
// entry, not an unmodeled field. The native stores merge into the existing map
// and allocate a fresh one only when it is nil; BdStore hands bd one
// "--set-metadata key=value" argument per key and never a whole-map form on an
// update. The single whole-map "--metadata <json>" argument in the tree belongs
// to Create, where there is by definition no prior evidence to lose.
//
// These tests exist because that property is load-bearing and invisible: a
// future refactor that replaced the merge with an assignment, or that taught
// BdStore.Update to serialize the whole map the way Create does, would silently
// destroy landing evidence on every rollback and pass every other test in the
// package. Assertions are exact-value and exact-key-set so they cannot pass
// vacuously — a whole-map replace takes the foreign key count from three to
// zero.

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// foreignMetadata is evidence written by a NEWER binary under keys this build
// has no vocabulary for. It is deliberately spelled with literals rather than
// beadmeta constants: constants would mean this build knows the keys, which is
// the opposite of what is being probed. Every key here matches
// beadmeta.ValidKeyPattern, so the backend accepts it on an update — a key the
// backend would refuse would make the test vacuous for a different reason.
func foreignMetadata() map[string]string {
	return map[string]string{
		"gc.delivery_state":      "landed",
		"gc.delivery_event_id":   "evt-2f9c41",
		"gc.delivery_landed_sha": "b55144ae2f0c4d1a9e7b3382c6d5104f7ab9e210",
	}
}

// assertForeignMetadataIntact fails when any foreign key is missing or its value
// diverges, and reports the whole observed map so a failure shows what replaced
// it rather than only that something did.
func assertForeignMetadataIntact(t *testing.T, got map[string]string, context string) {
	t.Helper()
	for key, want := range foreignMetadata() {
		switch actual, ok := got[key]; {
		case !ok:
			t.Errorf("%s: foreign key %q was dropped; metadata is now %v", context, key, got)
		case actual != want:
			t.Errorf("%s: foreign key %q = %q, want %q (stamped over)", context, key, actual, want)
		}
	}
}

// TestBdStoreMetadataWritesArePerKeyMerges proves the bd-backed store — the one
// a mysql-backed city actually runs — can only ever instruct bd to merge single
// keys. bd itself is not rolled back with gc, so the whole compat question for
// this store reduces to the argv gc emits.
func TestBdStoreMetadataWritesArePerKeyMerges(t *testing.T) {
	tests := []struct {
		name    string
		write   func(*BdStore) error
		wantSet []string
	}{
		{
			name:    "SetMetadata",
			write:   func(s *BdStore) error { return s.SetMetadata("bd-1", "gc.work_outcome", "no-op") },
			wantSet: []string{"gc.work_outcome=no-op"},
		},
		{
			name: "SetMetadataBatch",
			write: func(s *BdStore) error {
				return s.SetMetadataBatch("bd-1", map[string]string{
					"gc.work_outcome": "no-op",
					"branch":          "polecat/bd-1",
				})
			},
			wantSet: []string{"branch=polecat/bd-1", "gc.work_outcome=no-op"},
		},
		{
			name: "Update",
			write: func(s *BdStore) error {
				return s.Update("bd-1", UpdateOpts{Metadata: map[string]string{"branch": "polecat/bd-1"}})
			},
			wantSet: []string{"branch=polecat/bd-1"},
		},
		{
			name: "CloseAll",
			write: func(s *BdStore) error {
				_, err := s.CloseAll([]string{"bd-1"}, map[string]string{"close_reason": "done"})
				return err
			},
			wantSet: []string{"close_reason=done"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			s := NewBdStore("/city", recordingBdRunner(&calls))
			if err := tc.write(s); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(calls) == 0 {
				t.Fatalf("%s made no bd call; the probe would pass vacuously", tc.name)
			}

			var gotSet []string
			for _, call := range calls {
				for i, arg := range call {
					switch {
					case arg == "--metadata" || strings.HasPrefix(arg, "--metadata="):
						t.Errorf("%s emitted whole-map --metadata (%v); an update carrying the "+
							"full map replaces keys this build cannot see", tc.name, call)
					case arg == "--unset-metadata" || strings.HasPrefix(arg, "--unset-metadata="):
						t.Errorf("%s emitted --unset-metadata (%v); this build must not clear keys", tc.name, call)
					case arg == "--set-metadata":
						if i+1 < len(call) {
							gotSet = append(gotSet, call[i+1])
						}
					case strings.HasPrefix(arg, "--set-metadata="):
						gotSet = append(gotSet, strings.TrimPrefix(arg, "--set-metadata="))
					}
				}
			}

			slices.Sort(gotSet)
			if !slices.Equal(gotSet, tc.wantSet) {
				t.Errorf("%s --set-metadata pairs = %v, want %v", tc.name, gotSet, tc.wantSet)
			}
		})
	}
}

// nativeStoreFactory opens a store that persists in this process. Both
// implementations share MemStore's merge, but FileStore reaches it through a
// disk flush, so exercising both catches a divergence introduced in either.
type nativeStoreFactory struct {
	name string
	open func(t *testing.T) Store
}

func nativeStoreFactories() []nativeStoreFactory {
	return []nativeStoreFactory{
		{name: "MemStore", open: func(*testing.T) Store { return NewMemStore() }},
		{
			name: "FileStore",
			open: func(t *testing.T) Store {
				t.Helper()
				s, err := OpenFileStore(fsys.OSFS{}, filepath.Join(t.TempDir(), "beads.json"))
				if err != nil {
					t.Fatalf("OpenFileStore: %v", err)
				}
				return s
			},
		},
	}
}

// TestNativeStoreMetadataWritesPreserveForeignKeys drives every metadata write
// route on the in-process stores against a bead that already carries newer
// evidence, and checks after each that the evidence is byte-identical and that
// the write's own key landed alongside it rather than instead of it.
func TestNativeStoreMetadataWritesPreserveForeignKeys(t *testing.T) {
	writes := []struct {
		name    string
		write   func(Store, string) error
		wantKey string
		wantVal string
	}{
		{
			name:    "SetMetadata",
			write:   func(s Store, id string) error { return s.SetMetadata(id, "branch", "polecat/gas-a2td") },
			wantKey: "branch",
			wantVal: "polecat/gas-a2td",
		},
		{
			name: "SetMetadataBatch",
			write: func(s Store, id string) error {
				return s.SetMetadataBatch(id, map[string]string{"branch": "polecat/gas-a2td"})
			},
			wantKey: "branch",
			wantVal: "polecat/gas-a2td",
		},
		{
			name: "Update",
			write: func(s Store, id string) error {
				return s.Update(id, UpdateOpts{Metadata: map[string]string{"branch": "polecat/gas-a2td"}})
			},
			wantKey: "branch",
			wantVal: "polecat/gas-a2td",
		},
		{
			name: "CloseAll",
			write: func(s Store, id string) error {
				_, err := s.CloseAll([]string{id}, map[string]string{"close_reason": "merged"})
				return err
			},
			wantKey: "close_reason",
			wantVal: "merged",
		},
	}

	for _, factory := range nativeStoreFactories() {
		for _, w := range writes {
			t.Run(factory.name+"/"+w.name, func(t *testing.T) {
				store := factory.open(t)
				created, err := store.Create(Bead{
					Title:    "carries newer delivery evidence",
					Type:     "task",
					Metadata: foreignMetadata(),
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}

				if err := w.write(store, created.ID); err != nil {
					t.Fatalf("%s: %v", w.name, err)
				}

				got, err := store.Get(created.ID)
				if err != nil {
					t.Fatalf("Get after %s: %v", w.name, err)
				}
				assertForeignMetadataIntact(t, got.Metadata, factory.name+"/"+w.name)

				if got.Metadata[w.wantKey] != w.wantVal {
					t.Errorf("%s did not apply its own key: %s = %q, want %q",
						w.name, w.wantKey, got.Metadata[w.wantKey], w.wantVal)
				}

				wantKeys := slices.Sorted(maps.Keys(foreignMetadata()))
				wantKeys = append(wantKeys, w.wantKey)
				slices.Sort(wantKeys)
				gotKeys := slices.Sorted(maps.Keys(got.Metadata))
				if !slices.Equal(gotKeys, wantKeys) {
					t.Errorf("metadata keys after %s = %v, want %v", w.name, gotKeys, wantKeys)
				}
			})
		}
	}
}

// TestFileStoreDiskRoundTripPreservesForeignMetadata closes the one gap the
// in-process assertions leave: a store could merge correctly in memory and still
// drop unknown keys when it serializes. Reopening the same path forces the
// evidence back through JSON in a process that never held it.
func TestFileStoreDiskRoundTripPreservesForeignMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beads.json")

	writer, err := OpenFileStore(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	created, err := writer.Create(Bead{
		Title:    "carries newer delivery evidence",
		Type:     "task",
		Metadata: foreignMetadata(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := writer.SetMetadata(created.ID, "branch", "polecat/gas-a2td"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	reader, err := OpenFileStore(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := reader.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	assertForeignMetadataIntact(t, got.Metadata, "FileStore reopen")
	if got.Metadata["branch"] != "polecat/gas-a2td" {
		t.Errorf("branch = %q after reopen, want %q", got.Metadata["branch"], "polecat/gas-a2td")
	}
}
