package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// tailWindowChunkSize matches the backward-read chunk used by
// readFilteredTailFromFile so both tail readers page the log identically.
const tailWindowChunkSize int64 = 64 * 1024

// ReadTailSince reads events at or after since from the tail of path, walking
// the file backward and stopping once it reaches events older than since.
//
// WHY THIS EXISTS. ReadFilteredTail bounds a tail read by COUNT: it walks
// backward until it has collected limit matching events, so a time predicate
// alone never stops it — Filter{Since: t} with a generous limit walks all the
// way to the start of the file. That is unusable for anything reasoning over a
// trailing window: the active log on a busy city is tens of megabytes and
// grows (72MB/58k events when this was written), so a "last 24h" question that
// pays for the whole log is itself the kind of quiet waste it would be
// detecting. ReadTailSince bounds by TIME, so a 24h window on a 3-day log
// reads roughly a third of it and a 1h window reads almost nothing.
//
// types, when non-empty, keeps only events of those types. Events outside the
// set still participate in the time floor, so filtering never makes the walk
// read further than it otherwise would.
//
// maxEvents, when positive, caps retained events as a memory guard. A caller
// that needs to know whether the cap truncated its window should compare the
// oldest returned event against since — this returns no separate signal.
//
// Only the active file is read; rotated archives are not, matching
// ReadFilteredTail. A window extending past a rotation is therefore short,
// which under-reports rather than invents.
func ReadTailSince(path string, since time.Time, types []string, maxEvents int) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading events window: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat events window: %w", err)
	}
	return readTailSinceFromFile(f, info.Size(), since, tailWindowTypeSet(types), maxEvents)
}

// tailWindowTypeSet indexes the wanted types. A nil result means "keep all",
// which is distinct from an empty set and is why this returns nil rather than
// an empty map for no types.
func tailWindowTypeSet(types []string) map[string]bool {
	if len(types) == 0 {
		return nil
	}
	set := make(map[string]bool, len(types))
	for _, eventType := range types {
		set[eventType] = true
	}
	return set
}

func readTailSinceFromFile(f *os.File, size int64, since time.Time, keep map[string]bool, maxEvents int) ([]Event, error) {
	if size <= 0 {
		return nil, nil
	}
	var reversed []Event
	var pending []byte
	end := size
	done := false
	for end > 0 && !done {
		n := tailWindowChunkSize
		if end < n {
			n = end
		}
		start := end - n
		chunk := make([]byte, n)
		if _, err := f.ReadAt(chunk, start); err != nil && err != io.EOF {
			return nil, fmt.Errorf("reading events window: %w", err)
		}
		data := make([]byte, 0, len(chunk)+len(pending))
		data = append(data, chunk...)
		data = append(data, pending...)
		parts := bytes.Split(data, []byte{'\n'})
		// A chunk boundary splits a line; its head belongs to the chunk read
		// next (further back in the file) and is only complete once joined
		// there. The final chunk (start == 0) has no such predecessor.
		firstComplete := 0
		if start > 0 {
			pending = append(pending[:0], parts[0]...)
			firstComplete = 1
		} else {
			pending = nil
		}
		for i := len(parts) - 1; i >= firstComplete; i-- {
			line := bytes.TrimSuffix(parts[i], []byte{'\r'})
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var event Event
			if err := json.Unmarshal(line, &event); err != nil {
				continue
			}
			if event.Ts.Before(since) {
				// Stop after finishing this chunk rather than at the first old
				// event. The log is append-ordered, but concurrent writers can
				// interleave a few events around the boundary, and scanning the
				// rest of a chunk already in memory costs nothing.
				done = true
				continue
			}
			if keep != nil && !keep[event.Type] {
				continue
			}
			if maxEvents > 0 && len(reversed) >= maxEvents {
				done = true
				break
			}
			reversed = append(reversed, event)
		}
		end = start
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed, nil
}
