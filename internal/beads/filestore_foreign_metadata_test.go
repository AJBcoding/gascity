package beads_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/fsys"
)

// FileStore rewrites the ENTIRE file on every write: save() re-serializes the
// whole in-memory snapshot to a temp file and renames it over the store. That
// makes "does a write preserve metadata this binary never sets?" a real
// question rather than a pedantic one — any single mutation of any row
// re-serializes every other row too.
//
// It is load-bearing across a version boundary. `gc bd close <id> --reason`
// persists the reason as a `close_reason` metadata key, and a binary that
// predates that route still opens, reads and rewrites the same store. If a
// rewrite dropped keys the writing binary does not itself author, rolling a
// deployment back would silently destroy close reasons — the audit trail would
// evaporate on the next unrelated update, with no error anywhere.
//
// The fixture is not hand-written. testdata/gas_ot20_new_binary_filestore.json
// is the verbatim on-disk store a binary carrying the atomic-close-reason route
// produced when it ran `gc bd close <id> --reason ...` against a scratch city,
// captured for gas-ot20. Asserting against real bytes rather than an imitation
// is what makes this a compatibility test instead of a self-consistency one.
const foreignMetadataFixture = "testdata/gas_ot20_new_binary_filestore.json"

// foreignCloseReason is the exact reason string inside the fixture. It is
// deliberately hostile to naive handling — an embedded double quote, a
// backslash, a doubled percent verb, a colon/equals pair and non-ASCII runes —
// so a lossy re-encode anywhere in the load/save cycle shows up as a diff
// instead of passing unnoticed.
const foreignCloseReason = `refinery said "ok\yes": 100%% — verdict ✅ n=1`

const (
	foreignSubjectID   = "gas-ot20-subject"
	foreignBystanderID = "gas-ot20-bystander"
	foreignMutatedID   = "gas-ot20-oldwrite"
)

// stageForeignMetadataStore copies the fixture into a per-test temp dir so a
// test's own writes can never mutate the committed bytes, and returns the copy.
func stageForeignMetadataStore(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(foreignMetadataFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "beads.json")
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		t.Fatalf("stage fixture: %v", err)
	}
	return dst
}

func openForeignMetadataStore(t *testing.T, path string) *beads.FileStore {
	t.Helper()
	store, err := beads.OpenFileStore(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	return store
}

// getForeign re-opens the store from disk before reading. Re-opening is the
// point: it is the cross-process path, and the only way to observe what was
// actually persisted rather than what is still sitting in memory.
func getForeign(t *testing.T, path, id string) beads.Bead {
	t.Helper()
	b, err := openForeignMetadataStore(t, path).Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return b
}

// TestFileStoreReadsForeignCloseReasonExactly pins the read half: a store
// written by a binary with the atomic close-reason route loads here with the
// reason byte-exact and the rest of the row intact.
func TestFileStoreReadsForeignCloseReasonExactly(t *testing.T) {
	subject := getForeign(t, stageForeignMetadataStore(t), foreignSubjectID)

	if got := subject.Metadata["close_reason"]; got != foreignCloseReason {
		t.Fatalf("close_reason:\n got %q\nwant %q", got, foreignCloseReason)
	}
	if subject.Status != "closed" {
		t.Fatalf("status = %q, want closed", subject.Status)
	}
	// The reason must sit alongside pre-existing metadata, not displace it.
	if got := subject.Metadata["gc.work_outcome"]; got != "no-op" {
		t.Fatalf("gc.work_outcome = %q, want no-op", got)
	}
	if got := subject.Metadata["pre_existing_key"]; got != "must survive too" {
		t.Fatalf("pre_existing_key = %q, want %q", got, "must survive too")
	}
	// The close was one fenced write, so the revision advanced exactly once
	// from the seeded 1 — and survived the reload, which only works because
	// fileData persists revisions out of band (Bead.Revision is json:"-").
	if subject.Revision != 2 {
		t.Fatalf("revision = %d, want 2", subject.Revision)
	}
}

// TestFileStoreQueryFindsForeignCloseReason keeps the reason reachable through
// the query surface, not only a by-id Get. A store that served it from Get but
// dropped it from List would lose the reason for every reporting path.
func TestFileStoreQueryFindsForeignCloseReason(t *testing.T) {
	store := openForeignMetadataStore(t, stageForeignMetadataStore(t))

	got, err := store.List(beads.ListQuery{
		Metadata:      map[string]string{"close_reason": foreignCloseReason},
		IncludeClosed: true,
		AllowScan:     true,
	})
	if err != nil {
		t.Fatalf("List by close_reason: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("metadata query matched %d beads, want exactly 1", len(got))
	}
	if got[0].ID != foreignSubjectID {
		t.Fatalf("matched %q, want %q", got[0].ID, foreignSubjectID)
	}
}

// TestFileStoreRewriteOfUnrelatedBeadPreservesForeignMetadata is the one that
// guards the destructive case. Writing to one bead re-serializes every bead, so
// this asserts an unrelated update leaves the subject's foreign metadata, the
// bystander row, and the file's revision sealing untouched.
func TestFileStoreRewriteOfUnrelatedBeadPreservesForeignMetadata(t *testing.T) {
	path := stageForeignMetadataStore(t)

	if err := openForeignMetadataStore(t, path).Update(foreignMutatedID, beads.UpdateOpts{
		Metadata: map[string]string{"untouched": "after"},
	}); err != nil {
		t.Fatalf("Update(%s): %v", foreignMutatedID, err)
	}

	subject := getForeign(t, path, foreignSubjectID)
	if got := subject.Metadata["close_reason"]; got != foreignCloseReason {
		t.Fatalf("unrelated write destroyed close_reason:\n got %q\nwant %q", got, foreignCloseReason)
	}
	if subject.Status != "closed" {
		t.Fatalf("subject status = %q, want closed", subject.Status)
	}

	bystander := getForeign(t, path, foreignBystanderID)
	if got := bystander.Metadata["bystander_key"]; got != "bystander value" {
		t.Fatalf("bystander_key = %q, want %q", got, "bystander value")
	}

	assertRevisionsSealed(t, path)
}

// TestFileStoreUpdateMergesMetadataRatherThanReplacing pins the semantics the
// whole guarantee rests on: UpdateOpts.Metadata merges key-wise. If it ever
// became a whole-map replace, a one-key update would silently delete every
// other key on the row, close_reason included.
func TestFileStoreUpdateMergesMetadataRatherThanReplacing(t *testing.T) {
	path := stageForeignMetadataStore(t)

	if err := openForeignMetadataStore(t, path).Update(foreignSubjectID, beads.UpdateOpts{
		Metadata: map[string]string{"added_later": "by another binary"},
	}); err != nil {
		t.Fatalf("Update(%s): %v", foreignSubjectID, err)
	}

	subject := getForeign(t, path, foreignSubjectID)
	if got := subject.Metadata["close_reason"]; got != foreignCloseReason {
		t.Fatalf("metadata update replaced the map:\n got %q\nwant %q", got, foreignCloseReason)
	}
	if got := subject.Metadata["added_later"]; got != "by another binary" {
		t.Fatalf("added_later = %q, want the new key to land", got)
	}
	if got := subject.Metadata["gc.work_outcome"]; got != "no-op" {
		t.Fatalf("gc.work_outcome = %q, want no-op", got)
	}
}

// TestFileStoreReopenCloseCyclePreservesForeignMetadata covers the lifecycle a
// bead actually goes through when work resumes: reopen, then close again
// through the plain Close path that carries no reason of its own. The reason
// recorded by the earlier atomic close must still be there.
func TestFileStoreReopenCloseCyclePreservesForeignMetadata(t *testing.T) {
	path := stageForeignMetadataStore(t)

	if err := openForeignMetadataStore(t, path).Reopen(foreignSubjectID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if got := getForeign(t, path, foreignSubjectID).Metadata["close_reason"]; got != foreignCloseReason {
		t.Fatalf("reopen dropped close_reason:\n got %q\nwant %q", got, foreignCloseReason)
	}

	if err := openForeignMetadataStore(t, path).Close(foreignSubjectID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	subject := getForeign(t, path, foreignSubjectID)
	if got := subject.Metadata["close_reason"]; got != foreignCloseReason {
		t.Fatalf("reopen/close cycle dropped close_reason:\n got %q\nwant %q", got, foreignCloseReason)
	}
	if subject.Status != "closed" {
		t.Fatalf("status = %q, want closed", subject.Status)
	}
}

// TestFileStoreRewriteKeepsForeignMetadataByteExactOnDisk asserts at the byte
// level rather than through the decoder. A lossy re-encode that normalised the
// escaped quote, the backslash, the doubled percent or the non-ASCII runes
// could otherwise hide behind a decoder that re-normalised it the same way on
// the read back.
func TestFileStoreRewriteKeepsForeignMetadataByteExactOnDisk(t *testing.T) {
	path := stageForeignMetadataStore(t)

	if err := openForeignMetadataStore(t, path).Update(foreignMutatedID, beads.UpdateOpts{
		Metadata: map[string]string{"untouched": "after"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rewritten store: %v", err)
	}
	var fd struct {
		Beads []struct {
			ID       string            `json:"id"`
			Metadata map[string]string `json:"metadata"`
		} `json:"beads"`
	}
	if err := json.Unmarshal(raw, &fd); err != nil {
		t.Fatalf("rewritten store is not valid JSON: %v", err)
	}
	for _, b := range fd.Beads {
		if b.ID != foreignSubjectID {
			continue
		}
		if b.Metadata["close_reason"] != foreignCloseReason {
			t.Fatalf("on-disk close_reason after rewrite:\n got %q\nwant %q", b.Metadata["close_reason"], foreignCloseReason)
		}
		return
	}
	t.Fatalf("subject %q vanished from the store after the rewrite", foreignSubjectID)
}

// assertRevisionsSealed checks a rewrite left the file in the revisions-aware
// shape. fileData documents that a writer which drops the seal and the
// revisions map forces the next reader to re-seed every revision at the
// continuity floor, so this keeps that regression visible.
func assertRevisionsSealed(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	var fd struct {
		Revisions       map[string]int64 `json:"revisions"`
		RevisionsSealed bool             `json:"revisions_sealed"`
	}
	if err := json.Unmarshal(raw, &fd); err != nil {
		t.Fatalf("decode store: %v", err)
	}
	if !fd.RevisionsSealed {
		t.Fatal("rewrite dropped revisions_sealed; the next reader re-seeds at the continuity floor")
	}
	if fd.Revisions[foreignSubjectID] == 0 {
		t.Fatalf("rewrite dropped the revision for %q", foreignSubjectID)
	}
}
