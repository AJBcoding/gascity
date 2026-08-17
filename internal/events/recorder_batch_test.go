package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFileRecorderAppendBatchWritesContiguousEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var stderr bytes.Buffer
	recorder, err := NewFileRecorder(path, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	recorder.Record(Event{Type: BeadCreated, Actor: "seed"})
	explicit := time.Unix(123, 0).UTC()

	if err := recorder.AppendBatch([]Event{
		{Type: ExecutionWorkAssociated, Actor: "reemit", Subject: "work", RunID: "run"},
		{Type: ExecutionStepDefined, Actor: "reemit", Subject: "step", RunID: "run", StepID: "build", Ts: explicit},
	}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	got, err := ReadAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("events = %#v, want three", got)
	}
	if got[1].Seq != 2 || got[2].Seq != 3 {
		t.Fatalf("batch sequences = %d,%d, want 2,3", got[1].Seq, got[2].Seq)
	}
	if got[1].Ts.IsZero() || !got[2].Ts.Equal(explicit) {
		t.Fatalf("batch timestamps = %s,%s, want generated then %s", got[1].Ts, got[2].Ts, explicit)
	}
}

func TestFileRecorderAppendBatchMarshalsEverythingBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder, err := NewFileRecorder(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })

	err = recorder.AppendBatch([]Event{
		{Type: ExecutionWorkAssociated, Actor: "reemit", Subject: "would-partially-land"},
		{Type: ExecutionStepDefined, Actor: "reemit", Payload: json.RawMessage(`{`)},
	})
	if err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("AppendBatch error = %v, want marshal error", err)
	}
	got, readErr := ReadAll(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(got) != 0 {
		t.Fatalf("events = %#v, want no partial batch", got)
	}
}

func TestFileRecorderAppendBatchSurfacesClosedAndLockErrors(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		recorder, err := NewFileRecorder(path, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := recorder.AppendBatch([]Event{{Type: ExecutionStepDefined}}); err == nil || !strings.Contains(err.Error(), "closed") {
			t.Fatalf("AppendBatch error = %v, want closed error", err)
		}
	})

	t.Run("lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "events.jsonl")
		recorder, err := NewFileRecorder(path, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = recorder.Close() })
		sibling := mustOpenSiblingLock(t, path)
		t.Cleanup(func() { _ = sibling.Close() })

		err = recorder.AppendBatch([]Event{{Type: ExecutionStepDefined}})
		if err == nil || !strings.Contains(err.Error(), "lock") {
			t.Fatalf("AppendBatch error = %v, want lock error", err)
		}
	})
}

func TestWarnOnlyAcknowledgmentFailsClosedOnFileSyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder, err := NewFileRecorder(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	injected := errors.New("injected file sync failure")
	recorder.syncFileFn = func(*os.File) error {
		sibling, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Errorf("open sibling during sync: %v", openErr)
			return injected
		}
		defer sibling.Close() //nolint:errcheck // test-only probe
		lockErr := syscall.Flock(int(sibling.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			_ = syscall.Flock(int(sibling.Fd()), syscall.LOCK_UN)
			t.Error("durability barrier ran after the append flock was released")
		} else if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			t.Errorf("probe append flock during sync: %v", lockErr)
		}
		return injected
	}

	_, err = RecordWorkCloseWarnOnlyUse(recorder, WorkCloseWarnOnlyUsedPayload{
		Route: "gc.bd", StoreRef: "city:test", BeadID: "ga-sync-fail",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("warn-only acknowledgment error = %v, want injected sync failure", err)
	}
}

func TestWarnOnlyAcknowledgmentNonCreatorRequiresOwnDirectorySync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	creator, err := NewFileRecorder(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = creator.Close() })
	nonCreator, err := NewFileRecorder(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = nonCreator.Close() })

	injected := errors.New("injected non-creator directory sync failure")
	var creatorSyncCalls, nonCreatorSyncCalls int
	creator.syncDirFn = func(string) error {
		creatorSyncCalls++
		return nil
	}
	nonCreator.syncDirFn = func(string) error {
		nonCreatorSyncCalls++
		sibling, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Errorf("open sibling during directory sync: %v", openErr)
			return injected
		}
		defer sibling.Close() //nolint:errcheck // test-only probe
		lockErr := syscall.Flock(int(sibling.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			_ = syscall.Flock(int(sibling.Fd()), syscall.LOCK_UN)
			t.Error("non-creator directory barrier ran after the append flock was released")
		} else if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			t.Errorf("probe append flock during non-creator directory sync: %v", lockErr)
		}
		return injected
	}

	_, err = RecordWorkCloseWarnOnlyUse(nonCreator, WorkCloseWarnOnlyUsedPayload{
		Route: "gc.bd", StoreRef: "city:test", BeadID: "ga-non-creator-sync-fail",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("non-creator warn-only acknowledgment error = %v, want injected directory sync failure", err)
	}
	if creatorSyncCalls != 0 {
		t.Fatalf("creator directory sync calls = %d, want 0 before non-creator append", creatorSyncCalls)
	}
	if nonCreatorSyncCalls != 1 {
		t.Fatalf("non-creator directory sync calls = %d, want 1", nonCreatorSyncCalls)
	}
}

func TestFileRecorderAppendBatchRequiresParentSyncForEveryAcknowledgment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	recorder, err := NewFileRecorder(path, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	injected := errors.New("injected directory sync failure")
	var syncDirCalls int
	var barriers []string
	recorder.syncFileFn = func(*os.File) error {
		barriers = append(barriers, "file")
		return nil
	}
	recorder.syncDirFn = func(string) error {
		syncDirCalls++
		barriers = append(barriers, "directory")
		return nil
	}

	err = recorder.AppendBatch([]Event{{Type: ExecutionStepDefined}})
	if err != nil {
		t.Fatalf("first AppendBatch: %v", err)
	}
	if syncDirCalls != 1 {
		t.Fatalf("directory sync calls = %d, want 1", syncDirCalls)
	}
	if got := strings.Join(barriers, ","); got != "file,directory" {
		t.Fatalf("first-creation barriers = %q, want file,directory", got)
	}

	recorder.syncDirFn = func(string) error {
		syncDirCalls++
		barriers = append(barriers, "directory")
		return injected
	}
	if err := recorder.AppendBatch([]Event{{Type: ExecutionStepDefined}}); !errors.Is(err, injected) {
		t.Fatalf("second AppendBatch error = %v, want directory sync failure", err)
	}
	recorder.syncDirFn = func(string) error {
		syncDirCalls++
		barriers = append(barriers, "directory")
		return nil
	}
	if err := recorder.AppendBatch([]Event{{Type: ExecutionStepDefined}}); err != nil {
		t.Fatalf("third AppendBatch: %v", err)
	}
	if syncDirCalls != 3 {
		t.Fatalf("directory sync calls = %d, want one barrier per append", syncDirCalls)
	}
	if got := strings.Join(barriers, ","); got != "file,directory,file,directory,file,directory" {
		t.Fatalf("barrier sequence = %q, want file then directory for every append", got)
	}
}

func TestWriteBatchDetectsShortWriteInOneCall(t *testing.T) {
	writer := &shortBatchWriter{}
	err := writeBatch(writer, []byte("complete batch"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeBatch error = %v, want io.ErrShortWrite", err)
	}
	if writer.calls != 1 {
		t.Fatalf("write calls = %d, want one", writer.calls)
	}
}

type shortBatchWriter struct {
	calls int
}

func (w *shortBatchWriter) Write(data []byte) (int, error) {
	w.calls++
	return len(data) - 1, nil
}
