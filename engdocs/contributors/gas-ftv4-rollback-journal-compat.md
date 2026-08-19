# gas-ftv4 Rollback Compatibility — Surface B (Event Journal)

## Question

If the gas-ftv4 train is promoted and later rolled back to `f28bf5659`, can
that older binary read an event journal the newer one wrote — one containing
the two event types gas-ftv4 adds, `delivery.landed` and
`work.close.warn_only.used`?

This is surface B of the compatibility matrix tracked by `gas-lksp`. Surfaces
C (bead metadata, `gas-a2td`) and E (FileStore close reasons, `gas-ot20`) are
separate beads and are **not** answered here.

## Verdict

**No rollback blocker on surface B.** Established by execution, not by code
reading alone.

Three claims were required, in ascending order of what they cost if false.
All three hold:

1. The old binary does not error on the unknown types.
2. It does not abort a scan mid-stream — events recorded *after* an
   unrecognised one are still returned, on both the unfiltered and the
   filtered read paths.
3. Its read-modify-write (rotation's `gzipAndArchive`) neither drops nor
   rewrites the unknown events. This is the one that actually matters: that
   evidence is what a roll-forward would depend on.

The old binary treats both new types as *custom events*, which is a
first-class supported case in that lineage rather than an accident — `gc event
emit` accepts arbitrary types, so the custom-event branch already exists and
is exercised.

## Method

Two scratch worktrees, one at each commit, and a journal handed between them:

| Role   | Commit       | What it does                                          |
| ------ | ------------ | ----------------------------------------------------- |
| writer | `b55144ae2`  | gate tree; writes the journal with its own `FileRecorder` |
| reader | `f28bf5659`  | pre-gas-ftv4 lineage; the binary a rollback restores   |

The journal is produced by the **new binary's own writer**, not by a
hand-authored fixture, so the old side consumes bytes the gate tree really
emits. `work.close.warn_only.used` is emitted through its real production
helper `events.RecordWorkCloseWarnOnlyUse`; `delivery.landed` is built and
recorded exactly as `internal/landing/landing.go` builds and records it,
including the nested `work_records` array so payload loss is visible.

The writer emits a `manifest.json` carrying, per event, the seq, type, actor,
subject, and the SHA-256 of the payload bytes as they landed on disk. Every
byte assertion on the old side is anchored to that manifest rather than to the
old side's own input — an assertion compared only against its own input is
self-referential and cannot fail.

Fixture shape, deliberate: the two unknown events are **sandwiched between
known ones**.

```
seq 1  bead.created                known to both lineages
seq 2  delivery.landed             unknown to the old binary
seq 3  work.close.warn_only.used   unknown to the old binary
seq 4  bead.updated                known — mid-scan-abort detector
seq 5  bead.created                known — mid-scan-abort detector
```

A reader that gave up at the first unrecognised type would lose seq 4 and 5,
which the manifest's exact count makes visible.

### Constraints honoured

- **Nothing live was touched.** Every mutating step runs against a copy in
  `t.TempDir()`. The live journal, the live store, and the live city were
  never read from or written to.
- **The CLI route was refused deliberately**, for the same reason recorded on
  the Train 1 surfaces: the old binary's `gc events rotate` requires the city
  to be registered with the live supervisor, and registering a scratch city
  against live infrastructure to make a test pass is not an acceptable way to
  get a green. The package-level probes answer the same question touching
  nothing live.

## What was measured

Six probes, all passing against `f28bf5659`:

| Probe | Proves |
| ----- | ------ |
| `TestGasUJQ2OldBinaryDoesNotKnowTrain2Types` | Anti-vacuity guard: neither type is in this tree's `KnownEventTypes` and neither has a registered payload. Without it, a future edit teaching the old tree these types would leave every assertion below passing while testing nothing. |
| `TestGasUJQ2OldReaderAcceptsTrain2Journal` | `ReadAll` returns all 5 events with seq/type/actor/subject intact and payload digests matching the manifest; the events after the unknowns are asserted present separately, so a failure names the real problem. |
| `TestGasUJQ2OldFilteredQueryTraversesUnknownEvents` | Filtered reads by type, by actor, and across a seq window spanning the unknowns each return exactly the expected event. Filtering *on* an unknown type is also not an error — it selects, which is how a roll-forward queries the evidence back. |
| `TestGasUJQ2OldDecodePayloadDegradesGracefully` | `DecodePayload` on both unknown types returns `(nil, false, nil)` — the opaque-envelope fall-through, not a decode error. |
| `TestGasUJQ2RotationPreservesTrain2Events` | `gzipAndArchive` produces an archive whose decompressed bytes are **byte-identical** to the source, and reading the archive back through the old reader returns all 5 events with payload digests unchanged. |
| `TestGasUJQ2OldWireProjectionAcceptsTrain2Events` | The API/SSE projection (`wireEventFrom`, `toWireEvent`) renders both unknown events through the custom-event envelope with payload bytes matching the manifest, and drops neither. |

The last one is the leg that distinguishes this surface from the Train 1
surfaces: `delivery.landed` carries a *rich* payload with nested objects and
arrays, so the typed projection is where it could plausibly diverge from the
simpler Train 1 additions. It does not.

### The greens are not vacuous

Each byte and count assertion was shown to fail on a deliberately mutated
fixture before the verdict was accepted:

- **Dropping the `delivery.landed` line** turns the count assertion red
  (`returned 4 events, want 5`), fails the unknown-type filter lookup, and
  fails the post-rotation re-read.
- **Re-encoding the `delivery.landed` payload** with one byte of different
  whitespace — a stand-in for a decode/re-encode — turns the digest
  assertions red in the reader probe, the rotation probe, and the wire
  projection probe.

## Structural reasons behind the result

Confirmed by reading `f28bf5659`, and consistent with what the probes measured:

- `Event.Type` is a plain `string` — no enum, no custom `UnmarshalJSON`
  anywhere in the old `internal/events` package. Unknown types cannot fail to
  decode.
- `Payload` is `json.RawMessage`, an opaque passthrough. Payload richness is
  irrelevant to the old reader.
- `reader.go`'s scan loop `continue`s past a line it cannot unmarshal rather
  than aborting, and `matchesFilter` only ever *selects* — it has no
  reject-on-unknown branch.
- `DecodePayload` returns `(nil, false, nil)` for an unregistered type so
  callers fall back to the raw envelope; `internal/api`'s `customEventPayload`
  then only checks `json.Valid` and passes the raw bytes through.
- `gzipAndArchive` is `gzip.NewWriter` + `io.Copy` — a byte-level copy with no
  decode/re-encode — followed by `os.Rename`, then removal of the source. The
  only parser in that path, `readSeqWindow`, reads `seq` alone.

## Adjacent surfaces checked and cleared

- **Archive retention.** A rollback to a binary with a different retention
  default could prune archives holding landing evidence. It does not: both
  trees source `archiveRetainAge` identically from
  `rot.ArchiveRetainAgeDuration()` in `cmd/gc/providers.go` and pass it to
  `events.WithArchiveRetainAge`. Behaviour is config-driven and unchanged
  across the rollback.

## What this does not cover

- Surfaces C (`gas-a2td`, bead metadata — the pre-fix
  `hasConflictingDeliveryMetadata`) and E (`gas-ot20`, FileStore close
  reasons). Both remain open.
- Surface D, close-gate enforcement: rolling back *relaxes* enforcement. That
  is a policy regression, not corruption, and is accepted rather than fixed.
- Non-file event providers. These probes cover the built-in JSONL
  `FileRecorder`, which is the journal a rollback inherits.

## Reproducing

```bash
SCRATCH=/var/tmp/gas-ujq2-compat
mkdir -p "$SCRATCH/journal"
git worktree add --detach "$SCRATCH/new" b55144ae2
git worktree add --detach "$SCRATCH/old" f28bf5659

# Drop the appendix sources into place:
#   $SCRATCH/new/internal/events/gasujq2_writer_probe_test.go
#   $SCRATCH/old/internal/events/gasujq2_compat_probe_test.go
#   $SCRATCH/old/internal/api/gasujq2_wire_probe_test.go

cd "$SCRATCH/new" && GAS_UJQ2_JOURNAL_DIR="$SCRATCH/journal" \
  go test ./internal/events/ -count=1 -run TestGasUJQ2WriteTrain2Journal

cd "$SCRATCH/old" && GAS_UJQ2_JOURNAL_DIR="$SCRATCH/journal" \
  go test ./internal/events/ ./internal/api/ -count=1 -run TestGasUJQ2
```

The probes `t.Skip` when `GAS_UJQ2_JOURNAL_DIR` is unset, so they are inert
outside this harness. The old-tree probe reuses that tree's existing
`seqsOf` test helper.

Clean up with `git worktree remove` for both scratch trees and
`rm -rf "$SCRATCH"`.

## Appendix: probe sources

Pinned to the commits above. These are point-in-time experiment sources, not
repo-lifecycle code — they are recorded here because the commits they pin are
immutable and the result must stay auditable.

### `new/internal/events/gasujq2_writer_probe_test.go` (writer, `b55144ae2`)

```go
package events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gasUJQ2Manifest is the cross-binary ground truth handed from the new-binary
// writer to the old-binary reader probe. The old side recomputes these digests
// over what it decoded; a dropped event changes Count, a rewritten payload
// changes PayloadSHA256.
type gasUJQ2Manifest struct {
	Count  int                   `json:"count"`
	Events []gasUJQ2ManifestItem `json:"events"`
}

type gasUJQ2ManifestItem struct {
	Seq           uint64 `json:"seq"`
	Type          string `json:"type"`
	Actor         string `json:"actor"`
	Subject       string `json:"subject"`
	PayloadSHA256 string `json:"payload_sha256"`
}

// TestGasUJQ2WriteTrain2Journal writes a journal using THIS tree's own
// FileRecorder, carrying both gas-ftv4 event types (delivery.landed and
// work.close.warn_only.used) surrounded by types the pre-gas-ftv4 lineage also
// knows. The old-binary probe then reads these exact bytes, so the compat
// question is answered against what the new writer really emits rather than a
// hand-authored fixture.
//
// The two unknown events are deliberately sandwiched between known ones: an
// old reader that aborts its scan on the first unrecognised type would lose
// every event after seq 2, which the manifest's exact count makes visible.
func TestGasUJQ2WriteTrain2Journal(t *testing.T) {
	dir := os.Getenv("GAS_UJQ2_JOURNAL_DIR")
	if dir == "" {
		t.Skip("GAS_UJQ2_JOURNAL_DIR unset; this probe only runs under the gas-ujq2 harness")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("prepare journal dir: %v", err)
	}
	path := filepath.Join(dir, "events.jsonl")

	// A large max size keeps auto-rotation out of the writer: rotation is the
	// old side's experiment, driven explicitly there.
	rec, err := NewFileRecorder(path, os.Stderr, WithMaxSize(1<<30))
	if err != nil {
		t.Fatalf("new file recorder: %v", err)
	}
	defer rec.Close() //nolint:errcheck // probe cleanup

	// seq 1 — known to both lineages, before the unknowns.
	rec.Record(Event{
		Type:    BeadCreated,
		Actor:   "gascity/mechanic",
		Subject: "gas-ujq2",
		Message: "known-before",
	})

	// seq 2 — delivery.landed, built exactly as internal/landing/landing.go
	// builds it, including the nested WorkRecords array so payload loss or a
	// re-encode is visible in the digest.
	landed := DeliveryLandedPayload{
		EventID:               "gcl-ujq2-landing-receipt",
		WorkflowID:            "wf-gas-ftv4",
		IntegrationAttemptID:  "att-000123",
		Repository:            "github.com/gastownhall/gascity",
		Remote:                "origin",
		TargetRef:             "refs/heads/staging/gascity-lane",
		ExpectedTargetSHA:     "be84c5ea3000000000000000000000000000000a",
		ApprovedCandidateSHA:  "b55144ae2000000000000000000000000000000b",
		ObservedLandedSHA:     "b55144ae2000000000000000000000000000000b",
		PublicationMode:       "direct",
		IntegrationResultPath: ".gc/integration/att-000123.json",
		IntegrationResultHash: "sha256:11223344556677889900aabbccddeeff",
		WorkBeadIDs:           []string{"gas-ujq2", "gas-a2td", "gas-ot20"},
		WorkRecords: []DeliveryWorkRecordRef{
			{StoreRef: "city:anthony", BeadID: "gas-ujq2", WorkCommit: "aaaa111"},
			{StoreRef: "rig:gascity", BeadID: "gas-a2td", WorkCommit: "bbbb222"},
		},
		VerifiedAt: time.Date(2026, 8, 19, 21, 30, 0, 123456789, time.UTC).Format(time.RFC3339Nano),
	}
	landedRaw, err := json.Marshal(landed)
	if err != nil {
		t.Fatalf("encode delivery.landed payload: %v", err)
	}
	rec.Record(Event{
		Type:    DeliveryLanded,
		Actor:   "gascity/refinery",
		Subject: landed.EventID,
		RunID:   landed.WorkflowID,
		Payload: landedRaw,
	})

	// seq 3 — work.close.warn_only.used, emitted through its real production
	// helper so the bytes come from the shipping code path, not the test.
	if _, err := RecordWorkCloseWarnOnlyUse(rec, WorkCloseWarnOnlyUsedPayload{
		Route:          "gc bd close",
		StoreRef:       "rig:gascity",
		BeadID:         "gas-ujq2",
		ViolationCount: 2,
		RemovalVersion: "v0.9.0",
	}); err != nil {
		t.Fatalf("record work.close.warn_only.used: %v", err)
	}

	// seq 4 and 5 — known to both lineages, AFTER the unknowns. These are the
	// mid-scan-abort detectors.
	rec.Record(Event{
		Type:    BeadUpdated,
		Actor:   "gascity/gastown.furiosa",
		Subject: "gas-ujq2",
		Message: "known-after-1",
	})
	rec.Record(Event{
		Type:    BeadCreated,
		Actor:   "gascity/gastown.witness",
		Subject: "gas-lksp",
		Message: "known-after-2",
	})

	if err := rec.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}

	// Build the manifest from the file as written, so the digests describe
	// bytes on disk rather than the in-memory values above.
	written, err := ReadAll(path)
	if err != nil {
		t.Fatalf("new-binary read-back: %v", err)
	}
	if len(written) != 5 {
		t.Fatalf("new-binary read-back returned %d events, want 5", len(written))
	}
	manifest := gasUJQ2Manifest{Count: len(written)}
	for _, e := range written {
		sum := sha256.Sum256(e.Payload)
		manifest.Events = append(manifest.Events, gasUJQ2ManifestItem{
			Seq:           e.Seq,
			Type:          e.Type,
			Actor:         e.Actor,
			Subject:       e.Subject,
			PayloadSHA256: hex.EncodeToString(sum[:]),
		})
	}
	if manifest.Events[1].Type != DeliveryLanded || manifest.Events[2].Type != WorkCloseWarnOnlyUsed {
		t.Fatalf("fixture is not shaped as intended: %+v", manifest.Events)
	}

	blob, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), blob, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Logf("wrote %d events to %s (new binary)", len(written), path)
}
```

### `old/internal/events/gasujq2_compat_probe_test.go` (reader, `f28bf5659`)

```go
package events

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// gas-ujq2 — compat surface B.
//
// Question: can this binary (f28bf5659, the pre-gas-ftv4 lineage that a
// rollback would restore) read a journal written by the gas-ftv4 gate tree
// carrying delivery.landed and work.close.warn_only.used?
//
// The journal under GAS_UJQ2_JOURNAL_DIR was produced by the NEW binary's own
// FileRecorder. Nothing here reads or writes live city state: every mutating
// step runs against a copy in t.TempDir().
//
// Three things must hold, in ascending order of what they cost if false:
//  1. the old reader does not error on the unknown types;
//  2. it does not abort a scan mid-stream, silently truncating everything
//     recorded after the first unknown event;
//  3. its read-modify-write (rotation's gzipAndArchive) neither drops nor
//     rewrites the unknown events — that evidence is what a roll-forward
//     depends on.

const (
	gasUJQ2DeliveryLanded  = "delivery.landed"
	gasUJQ2WarnOnlyUsed    = "work.close.warn_only.used"
	gasUJQ2ExpectedCount   = 5
	gasUJQ2UnknownFirstIdx = 1 // seq 2
	gasUJQ2UnknownLastIdx  = 2 // seq 3
)

type gasUJQ2Manifest struct {
	Count  int                   `json:"count"`
	Events []gasUJQ2ManifestItem `json:"events"`
}

type gasUJQ2ManifestItem struct {
	Seq           uint64 `json:"seq"`
	Type          string `json:"type"`
	Actor         string `json:"actor"`
	Subject       string `json:"subject"`
	PayloadSHA256 string `json:"payload_sha256"`
}

func gasUJQ2Load(t *testing.T) (journalDir string, manifest gasUJQ2Manifest) {
	t.Helper()
	journalDir = os.Getenv("GAS_UJQ2_JOURNAL_DIR")
	if journalDir == "" {
		t.Skip("GAS_UJQ2_JOURNAL_DIR unset; this probe only runs under the gas-ujq2 harness")
	}
	blob, err := os.ReadFile(filepath.Join(journalDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read new-binary manifest: %v", err)
	}
	if err := json.Unmarshal(blob, &manifest); err != nil {
		t.Fatalf("decode new-binary manifest: %v", err)
	}
	if manifest.Count != gasUJQ2ExpectedCount {
		t.Fatalf("manifest count %d, want %d", manifest.Count, gasUJQ2ExpectedCount)
	}
	return journalDir, manifest
}

// gasUJQ2CopyJournal copies the new binary's journal into a scratch dir. Every
// mutating experiment below runs on a copy; the fixture is never modified.
func gasUJQ2CopyJournal(t *testing.T, journalDir string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(journalDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read fixture journal: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatalf("copy fixture journal: %v", err)
	}
	return path
}

func gasUJQ2Digest(payload json.RawMessage) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// TestGasUJQ2OldBinaryDoesNotKnowTrain2Types is the anti-vacuity guard. If a
// future edit taught this tree the gas-ftv4 types, every assertion below would
// still pass while testing nothing.
func TestGasUJQ2OldBinaryDoesNotKnowTrain2Types(t *testing.T) {
	for _, unknown := range []string{gasUJQ2DeliveryLanded, gasUJQ2WarnOnlyUsed} {
		for _, known := range KnownEventTypes {
			if known == unknown {
				t.Fatalf("%q is a known type in this tree; the compat probe would pass vacuously", unknown)
			}
		}
		if _, ok := LookupPayload(unknown); ok {
			t.Fatalf("%q has a registered payload in this tree; the compat probe would pass vacuously", unknown)
		}
	}
}

// TestGasUJQ2OldReaderAcceptsTrain2Journal covers claims (1) and (2): the old
// reader returns every event, including the two it does not recognise, with
// payload bytes unchanged — and keeps scanning past them.
func TestGasUJQ2OldReaderAcceptsTrain2Journal(t *testing.T) {
	journalDir, manifest := gasUJQ2Load(t)
	path := filepath.Join(journalDir, "events.jsonl") // read-only

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("old ReadAll errored on a journal with unknown event types: %v", err)
	}
	if len(got) != manifest.Count {
		t.Fatalf("old ReadAll returned %d events, want %d — events were dropped or the scan aborted", len(got), manifest.Count)
	}
	for i, want := range manifest.Events {
		e := got[i]
		if e.Seq != want.Seq || e.Type != want.Type || e.Actor != want.Actor || e.Subject != want.Subject {
			t.Errorf("event %d: got seq=%d type=%q actor=%q subject=%q, want seq=%d type=%q actor=%q subject=%q",
				i, e.Seq, e.Type, e.Actor, e.Subject, want.Seq, want.Type, want.Actor, want.Subject)
		}
		if digest := gasUJQ2Digest(e.Payload); digest != want.PayloadSHA256 {
			t.Errorf("event %d (%s): payload digest %s, want %s — the old reader altered a payload it does not understand",
				i, e.Type, digest, want.PayloadSHA256)
		}
	}

	// Claim (2), stated on its own so a failure names the real problem: the
	// two events recorded AFTER the unknowns must survive.
	afterFirstUnknown := manifest.Events[gasUJQ2UnknownLastIdx+1:]
	if len(afterFirstUnknown) == 0 {
		t.Fatal("fixture places no known events after the unknown ones; it cannot detect a mid-scan abort")
	}
	for _, want := range afterFirstUnknown {
		var found bool
		for _, e := range got {
			if e.Seq == want.Seq && e.Type == want.Type {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("event seq=%d type=%q recorded after an unknown type is missing: the scan aborted mid-stream", want.Seq, want.Type)
		}
	}
}

// TestGasUJQ2OldFilteredQueryTraversesUnknownEvents covers the "does not abort
// a query mid-scan" requirement on the filtered path specifically: a filter
// that selects an event positioned AFTER both unknowns must still find it.
func TestGasUJQ2OldFilteredQueryTraversesUnknownEvents(t *testing.T) {
	journalDir, manifest := gasUJQ2Load(t)
	path := filepath.Join(journalDir, "events.jsonl") // read-only

	last := manifest.Events[len(manifest.Events)-1]
	afterUnknowns := manifest.Events[gasUJQ2UnknownLastIdx+1]

	cases := []struct {
		name    string
		filter  Filter
		wantSeq uint64
	}{
		{"by type, past both unknowns", Filter{Type: afterUnknowns.Type, Subject: afterUnknowns.Subject}, afterUnknowns.Seq},
		{"by actor, last event", Filter{Actor: last.Actor}, last.Seq},
		{"by seq window spanning the unknowns", Filter{AfterSeq: manifest.Events[gasUJQ2UnknownFirstIdx].Seq - 1, Type: last.Type, Subject: last.Subject}, last.Seq},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadFiltered(path, tc.filter)
			if err != nil {
				t.Fatalf("old ReadFiltered errored: %v", err)
			}
			if len(got) != 1 || got[0].Seq != tc.wantSeq {
				t.Fatalf("filter %+v returned %d events (%v), want exactly seq %d", tc.filter, len(got), seqsOf(got), tc.wantSeq)
			}
		})
	}

	// A filter naming an unknown type is not an error either — it simply
	// selects, which is how a roll-forward would query the evidence back.
	for _, unknown := range []string{gasUJQ2DeliveryLanded, gasUJQ2WarnOnlyUsed} {
		got, err := ReadFiltered(path, Filter{Type: unknown})
		if err != nil {
			t.Fatalf("old ReadFiltered errored filtering on unknown type %q: %v", unknown, err)
		}
		if len(got) != 1 {
			t.Fatalf("filtering on unknown type %q returned %d events, want 1", unknown, len(got))
		}
		if len(got[0].Payload) == 0 {
			t.Fatalf("unknown type %q came back with an empty payload", unknown)
		}
	}
}

// TestGasUJQ2OldDecodePayloadDegradesGracefully pins the structural reason the
// rich delivery.landed payload is safe here: an unregistered type falls through
// to the opaque envelope path rather than raising a decode error.
func TestGasUJQ2OldDecodePayloadDegradesGracefully(t *testing.T) {
	journalDir, _ := gasUJQ2Load(t)
	got, err := ReadAll(filepath.Join(journalDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("old ReadAll: %v", err)
	}
	for _, e := range got {
		if e.Type != gasUJQ2DeliveryLanded && e.Type != gasUJQ2WarnOnlyUsed {
			continue
		}
		decoded, registered, err := DecodePayload(e.Type, e.Payload)
		if err != nil {
			t.Errorf("DecodePayload(%q) errored: %v — unknown payloads must not fail decode", e.Type, err)
		}
		if registered {
			t.Errorf("DecodePayload(%q) reported a registered payload in the old tree", e.Type)
		}
		if decoded != nil {
			t.Errorf("DecodePayload(%q) returned %v, want nil for an unregistered type", e.Type, decoded)
		}
	}
}

// TestGasUJQ2RotationPreservesTrain2Events covers claim (3), the one that
// actually matters: the old binary's read-modify-write must not drop or
// rewrite evidence it does not understand. Runs entirely on a copy.
func TestGasUJQ2RotationPreservesTrain2Events(t *testing.T) {
	journalDir, manifest := gasUJQ2Load(t)
	path := gasUJQ2CopyJournal(t, journalDir)
	dir := filepath.Dir(path)

	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read copied journal: %v", err)
	}

	first := manifest.Events[0].Seq
	last := manifest.Events[len(manifest.Events)-1].Seq
	archive := filepath.Join(dir, formatArchiveBasename(time.Now().UTC(), first, last))
	if err := gzipAndArchive(path, archive, os.Stderr); err != nil {
		t.Fatalf("old gzipAndArchive failed on a journal with unknown event types: %v", err)
	}

	// Byte equality: gzipAndArchive is a byte-level copy, so a decode/re-encode
	// of the unknown events would show up here.
	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close() //nolint:errcheck // read-only
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip archive: %v", err)
	}
	defer gr.Close() //nolint:errcheck // read-only
	restored, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read archive contents: %v", err)
	}
	if string(restored) != string(source) {
		t.Fatalf("archive contents differ from source: the old rotation path rewrote the journal\n--- source ---\n%s\n--- archive ---\n%s", source, restored)
	}

	// gzipAndArchive removes the source on success, so the events now live
	// only in the archive. The old reader must still return every one of them.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected rotation to consume the active log, stat returned: %v", err)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("old ReadAll over archive errored: %v", err)
	}
	if len(got) != manifest.Count {
		t.Fatalf("old ReadAll over archive returned %d events, want %d — rotation lost events", len(got), manifest.Count)
	}
	for i, want := range manifest.Events {
		if got[i].Seq != want.Seq || got[i].Type != want.Type {
			t.Errorf("archived event %d: got seq=%d type=%q, want seq=%d type=%q", i, got[i].Seq, got[i].Type, want.Seq, want.Type)
		}
		if digest := gasUJQ2Digest(got[i].Payload); digest != want.PayloadSHA256 {
			t.Errorf("archived event %d (%s): payload digest %s, want %s — rotation altered an unknown payload",
				i, got[i].Type, digest, want.PayloadSHA256)
		}
	}
}
```

### `old/internal/api/gasujq2_wire_probe_test.go` (SSE projection, `f28bf5659`)

```go
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/events"
)

// gas-ujq2 — compat surface B, API/SSE projection leg.
//
// The journal reader is not the only old-binary path these events reach. The
// SSE projection decodes each event's payload into its registered typed
// variant, which is where a rich unregistered payload (delivery.landed carries
// nested objects and arrays) could behave differently from the simpler Train 1
// additions. Unregistered types are meant to fall through to the custom-event
// envelope; this proves they do, on the bytes the new binary actually wrote.
func TestGasUJQ2OldWireProjectionAcceptsTrain2Events(t *testing.T) {
	dir := os.Getenv("GAS_UJQ2_JOURNAL_DIR")
	if dir == "" {
		t.Skip("GAS_UJQ2_JOURNAL_DIR unset; this probe only runs under the gas-ujq2 harness")
	}
	all, err := events.ReadAll(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read fixture journal: %v", err)
	}

	// Anchor the byte assertions to what the NEW binary wrote. Comparing the
	// projection's output only against its own input would be self-referential
	// and could not fail.
	blob, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read new-binary manifest: %v", err)
	}
	var manifest struct {
		Events []struct {
			Seq           uint64 `json:"seq"`
			Type          string `json:"type"`
			PayloadSHA256 string `json:"payload_sha256"`
		} `json:"events"`
	}
	if err := json.Unmarshal(blob, &manifest); err != nil {
		t.Fatalf("decode new-binary manifest: %v", err)
	}
	wantDigest := make(map[uint64]string, len(manifest.Events))
	for _, item := range manifest.Events {
		wantDigest[item.Seq] = item.PayloadSHA256
	}

	unknown := map[string]bool{"delivery.landed": true, "work.close.warn_only.used": true}
	var seen int
	for _, e := range all {
		if !unknown[e.Type] {
			continue
		}
		seen++

		envelope, err := wireEventFrom(e, nil)
		if err != nil {
			t.Fatalf("wireEventFrom(%q) errored: %v — an unknown type must fall through to the custom-event envelope", e.Type, err)
		}
		if envelope.Type != e.Type {
			t.Errorf("wireEventFrom(%q): envelope type %q", e.Type, envelope.Type)
		}
		raw, ok := envelope.Payload.Value.(json.RawMessage)
		if !ok {
			t.Fatalf("wireEventFrom(%q): payload is %T, want raw passthrough for an unregistered type", e.Type, envelope.Payload.Value)
		}
		if string(raw) != string(e.Payload) {
			t.Errorf("wireEventFrom(%q): payload bytes altered\n got: %s\nwant: %s", e.Type, raw, e.Payload)
		}
		sum := sha256.Sum256(raw)
		if digest := hex.EncodeToString(sum[:]); digest != wantDigest[e.Seq] {
			t.Errorf("wireEventFrom(%q) seq=%d: projected payload digest %s, want %s (as written by the new binary)",
				e.Type, e.Seq, digest, wantDigest[e.Seq])
		}

		// toWireEvent is the second projection over the same bytes; it drops
		// events it cannot render, which would silently hide the evidence.
		if _, ok := toWireEvent(e); !ok {
			t.Errorf("toWireEvent(%q) dropped the event", e.Type)
		}
	}
	if seen != 2 {
		t.Fatalf("found %d gas-ftv4 events in the fixture, want 2", seen)
	}
}
```
