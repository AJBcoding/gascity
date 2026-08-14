package herdr

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/fsys"
)

// herdr exposes no wall-clock activity timestamp — its agent object carries
// only agent_status and state_change_seq, and the seq moves on status
// transitions alone, not on output (gas-s04b measured a working agent's seq
// frozen across 12s of active output). GetLastActivity therefore derives
// activity by observation:
//
//   - A working agent is active NOW. Its seq is frozen for the whole turn, so
//     aging it would let auto-suspend stop a session mid-turn; and a
//     hung-in-a-loop agent deliberately reads as active — matching tmux's
//     output-driven semantics, where a spinner is activity. Whether an
//     always-active agent is productive is the witness's judgment, not Go's.
//   - An at-rest agent (idle, done, blocked) was last active when its current
//     rest epoch began. The globally-monotonic seq identifies the epoch: the
//     first observation stamps "now" into the sidecar keyed by that seq, and
//     while the seq holds, readers get the persisted stamp back — so idleness
//     ages at observation resolution (the controller tick / witness patrol
//     cadence), which is what auto-suspend and stuck-detection need.
//   - An unknown status fails safe toward active, the same direction as
//     agentAliveFromStatus: a session we cannot classify must never age into
//     an auto-suspend, which merely preserves the pre-derivation behavior for
//     that session.
//
// The stamp is durable (sidecar) rather than in-memory because every reader
// is a separate short-lived process (`gc session list`, the controller tick):
// the observation baseline must survive across them — the same shape as the
// acp provider's durable stamp.

// Sidecar keys for the derived last-activity stamp. lastActivityMetaKey
// matches the acp provider's durable-stamp key so the concept reads the same
// across providers' sidecars; the seq companion records the herdr
// state_change_seq observed when the stamp was written. Living in the meta
// namespace means Stop's sidecar wipe already removes both.
const (
	lastActivityMetaKey    = "gc_last_activity"
	lastActivitySeqMetaKey = "gc_last_activity_seq"
)

// GetLastActivity returns the session's derived last-activity time: "now" for
// a working agent, the persisted first observation of the current rest epoch
// for an at-rest one, and the last persisted stamp (or the zero time, meaning
// unknown) when the agent is absent — a stopped session or a raw shell pane
// that never registered an agent. Transport failures surface as errors so
// callers can tell "herdr unreachable" from "no activity".
func (p *Provider) GetLastActivity(name string) (time.Time, error) {
	info, present, err := p.c.getAgent(context.Background(), herdrAgentName(name))
	if err != nil {
		return time.Time{}, fmt.Errorf("herdr: last activity for %q: %w", name, err)
	}
	prevStamp, prevSeq, err := p.persistedActivity(name)
	if err != nil {
		return time.Time{}, err
	}
	if !present {
		return prevStamp, nil
	}
	stamp, persist := activityFromObservation(info.AgentStatus, info.StateChangeSeq, prevStamp, prevSeq, time.Now())
	if persist {
		if err := p.stampActivity(name, stamp, info.StateChangeSeq); err != nil {
			return time.Time{}, err
		}
	}
	return stamp, nil
}

// activityFromObservation folds one observed agent status into the persisted
// activity baseline: the returned stamp is what GetLastActivity reports, and
// persist says whether (stamp, seq) must be written back to the sidecar.
// Split from GetLastActivity so the verdict is unit-testable without shelling
// out to herdr, like livenessFromAgent.
func activityFromObservation(status string, seq int64, prevStamp time.Time, prevSeq string, now time.Time) (stamp time.Time, persist bool) {
	if !agentAtRestFromStatus(status) {
		return now, false
	}
	if !prevStamp.IsZero() && prevSeq == strconv.FormatInt(seq, 10) {
		return prevStamp, false
	}
	return now, true
}

// agentAtRestFromStatus reports whether a herdr agent_status describes an
// agent doing nothing. herdr's status vocabulary is idle, working, blocked,
// done, unknown; idle and done are at rest, and so is blocked — a session
// stuck at a prompt must age so stuck-detection sees it, where reading it as
// perpetually active would hide exactly the case the witness exists to catch.
// The terminal statuses agentAliveFromStatus recognizes are at rest for the
// same reason. Everything else — working, unknown, anything herdr adds later
// — reads as actively working (see the package comment for the fail-safe
// rationale).
func agentAtRestFromStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "idle", "done", "blocked",
		"exited", "stopped", "dead", "gone", "terminated", "closed", "crashed":
		return true
	default:
		return false
	}
}

// persistedActivity reads the durable stamp and its companion seq from the
// sidecar. A missing stamp is "unknown" (zero time, nil error). A stamp that
// no longer parses is treated as missing rather than surfaced as an error:
// the next at-rest observation re-seeds it, where erroring would leave the
// session permanently invisible to activity readers — the same silent
// blindness this derivation exists to fix.
func (p *Provider) persistedActivity(name string) (time.Time, string, error) {
	raw, err := p.GetMeta(name, lastActivityMetaKey)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("herdr: reading last activity for %q: %w", name, err)
	}
	seq, err := p.GetMeta(name, lastActivitySeqMetaKey)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("herdr: reading last activity seq for %q: %w", name, err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, seq, nil
	}
	t, perr := time.Parse(time.RFC3339Nano, raw)
	if perr != nil {
		return time.Time{}, seq, nil
	}
	return t, seq, nil
}

// stampActivity durably records the observation. Writes are atomic because
// concurrent short-lived readers (controller tick, witness patrol, session
// list) race this path; the stamp lands before the seq so a crash between the
// two reads as a seq mismatch and re-stamps — the conservative direction —
// rather than pairing a fresh seq with a stale stamp and under-reporting.
func (p *Provider) stampActivity(name string, t time.Time, seq int64) error {
	dir := filepath.Join(p.metaDir, sanitize(name))
	if err := (fsys.OSFS{}).MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("herdr: creating activity sidecar dir for %q: %w", name, err)
	}
	stamp := []byte(t.UTC().Format(time.RFC3339Nano))
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, filepath.Join(dir, sanitize(lastActivityMetaKey)), stamp, 0o644); err != nil {
		return fmt.Errorf("herdr: writing activity stamp for %q: %w", name, err)
	}
	seqBytes := []byte(strconv.FormatInt(seq, 10))
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, filepath.Join(dir, sanitize(lastActivitySeqMetaKey)), seqBytes, 0o644); err != nil {
		return fmt.Errorf("herdr: writing activity seq for %q: %w", name, err)
	}
	return nil
}
