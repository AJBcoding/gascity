package herdr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// A mid-session runtime death is attributable only at the moment the liveness
// verdict is reached: herdr's log records the pane and the terminating signal
// but not which gascity session it was, and gascity records the session name but
// neither pane nor status. ObserveLiveness is the one place both are in hand.
//
// The record is split across two files because the two facts have different
// lifetimes:
//
//   - The durable record lives in a deaths directory alongside — not inside —
//     the per-session sidecar dirs. It must outlive the session, because the
//     reconciler's response to this exact verdict is to recycle the zombie:
//     it observes running-but-not-alive and calls sp.Stop, whose clearMeta does
//     RemoveAll on the session's own sidecar dir. A record kept there would be
//     deleted in the same reconciler pass that wrote it.
//   - The pending marker lives in the per-session dir and is deliberately
//     disposable. It marks "this death episode is already captured" so repeat
//     polls of a still-dead session skip the work, and being cleared by teardown
//     or by the session coming back alive is exactly the re-arm the next death
//     needs.
const (
	// runtimeDeathsDir collects durable death records. The leading dot keeps it
	// out of boundSessionNames' session scan, which skips directories with no
	// bound-name sidecar file anyway.
	runtimeDeathsDir = ".deaths"
	// runtimeDeathPending marks a death episode already recorded for a session.
	runtimeDeathPending = "runtime-death.pending"
	// runtimeDeathStopping marks a teardown we initiated, so the pane going away
	// is not misread as a runtime death.
	runtimeDeathStopping = "runtime-death.stopping"
	// runtimeDeathPeekLines bounds the final-screen capture kept with a record —
	// enough scrollback to hold a panic or a shutdown message without letting a
	// diagnostic artifact grow without bound.
	runtimeDeathPeekLines = 200
	// runtimeDeathStamp names records by UTC instant, colon-free for portability.
	runtimeDeathStamp = "20060102T150405Z"
)

// shouldRecordRuntimeDeath reports whether a liveness verdict describes a real
// runtime death worth recording: a present agent (Running) that herdr reports
// terminal (!Alive). That is the only not-alive shape carrying attribution —
// herdr still holds the pane and the status.
//
// It deliberately excludes the empty verdict, which livenessFromAgent returns
// for BOTH a failed `agent get` and an agent absent from the registry. Neither
// is a death: a transport failure says nothing about the runtime, and an absent
// agent has no pane or status left to attribute. Recording either would inject
// false deaths into the evidence set — and these are the common cases, not edge
// ones (agent_not_found alone accounts for thousands of lines in herdr's log).
func shouldRecordRuntimeDeath(lv runtime.Liveness) bool {
	return lv.Running && !lv.Alive
}

// runtimeDeathRecorded reports whether this session's current death episode is
// already captured. It is a cheap local stat used to skip the two herdr
// round-trips that gathering a record costs, on the many polls where a session
// sits dead and the write would be discarded anyway.
func (p *Provider) runtimeDeathRecorded(name string) bool {
	if p.metaDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(p.metaDir, sanitize(name), runtimeDeathPending))
	return err == nil
}

// clearRuntimeDeath re-arms death capture for a session. It runs whenever the
// session is observed alive: a restarted session can die again, and without
// this the pending marker would suppress every death after the first. It clears
// only the marker — recorded evidence is never discarded. Best-effort and
// idempotent; the marker is absent on nearly every call.
func (p *Provider) clearRuntimeDeath(name string) {
	if p.metaDir == "" {
		return
	}
	os.Remove(filepath.Join(p.metaDir, sanitize(name), runtimeDeathPending)) //nolint:errcheck
}

// recordRuntimeDeath persists a runtime-death diagnostic naming the session, the
// pane, and the terminal agent_status that drove the not-alive verdict, plus the
// final screen. It is the herdr analog of the tmux provider's recordStartCrash,
// which covers only the startup path and so leaves mid-session deaths
// unattributed.
//
// Write-once per death episode: the first observation is the one nearest the
// death, so later polls of the same still-dead session must not march the
// recorded moment forward or replace the captured screen with a later, emptier
// one. Records accumulate across episodes — that is the point, since a single
// death proves nothing and the pattern only emerges over days.
//
// Best-effort: a disabled sidecar (empty metaDir) or any I/O error returns ""
// without affecting the liveness verdict. Returns the record path when written.
func (p *Provider) recordRuntimeDeath(name, paneID, agentStatus, paneContent string) string {
	if p.metaDir == "" || p.runtimeDeathRecorded(name) {
		return ""
	}

	var b strings.Builder
	// UTC to match herdr's own log, which is what this record gets joined
	// against to recover the terminating signal.
	fmt.Fprintf(&b, "observed: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "session: %s\n", name)
	if paneID != "" {
		fmt.Fprintf(&b, "pane: %s\n", paneID)
	}
	// The join key. herdr's log identifies a dead pane by a numeric id gascity
	// never sees and by this pid, which it logs both on spawn and on
	// termination — so the pid is what ties this record to a terminating signal.
	if pid, err := p.GetMeta(name, metaBoundPID); err == nil && strings.TrimSpace(pid) != "" {
		fmt.Fprintf(&b, "pid: %s\n", strings.TrimSpace(pid))
	}
	if agentStatus != "" {
		fmt.Fprintf(&b, "agent-status: %s\n", agentStatus)
	} else {
		// Live herdr drops a dead agent from its registry rather than parking it
		// at a terminal status, so this is the ordinary case, not a degraded
		// one: the death was detected by the pane binding going stale.
		b.WriteString("agent-status: (absent — detected via confirmed-gone pane binding)\n")
	}
	b.WriteString("--- last pane output ---\n")
	b.WriteString(paneContent)
	if paneContent != "" && !strings.HasSuffix(paneContent, "\n") {
		b.WriteByte('\n')
	}

	dir := filepath.Join(p.metaDir, runtimeDeathsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	f, path := createRuntimeDeathRecord(dir, name, paneID)
	if f == nil {
		return ""
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(b.String()); err != nil {
		return ""
	}
	p.markRuntimeDeathRecorded(name)
	return path
}

// createRuntimeDeathRecord creates the record file, never truncating an
// existing one. The name carries the session, the UTC instant and the pane, so
// a record is identifiable without opening it and two deaths of the same
// session are distinct files. The stamp only resolves to the second, so a
// numeric suffix disambiguates the residual collision — a session that dies,
// restarts and dies again inside one second is not expected, but silently
// dropping the second record would be the wrong way to find that out.
// Returns a nil file when no free name is available.
func createRuntimeDeathRecord(dir, name, paneID string) (*os.File, string) {
	base := fmt.Sprintf("%s-%s", sanitize(name), time.Now().UTC().Format(runtimeDeathStamp))
	if paneID != "" {
		base += "-" + sanitize(paneID)
	}
	for i := 0; i < 10; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		path := filepath.Join(dir, candidate+".log")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, path
		}
		if !os.IsExist(err) {
			return nil, ""
		}
	}
	return nil, ""
}

// markStopIntended records that a teardown of this session is our own doing, so
// the pane disappearing is not reported as a runtime death. Stop cannot simply
// clear the binding first to achieve this: resolving the pane before closing it
// is what fixed the "sleep leak" (name lost ⇒ pane never found ⇒ panes piled up
// across sleep cycles), so the ordering is load-bearing and the intent is
// recorded alongside it instead. clearMeta at the end of Stop removes the
// marker with the rest of the sidecar.
func (p *Provider) markStopIntended(name string) {
	if p.metaDir == "" {
		return
	}
	dir := filepath.Join(p.metaDir, sanitize(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	os.WriteFile(filepath.Join(dir, runtimeDeathStopping), nil, 0o644) //nolint:errcheck
}

// stopIntended reports whether this session's teardown was initiated by us.
func (p *Provider) stopIntended(name string) bool {
	if p.metaDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(p.metaDir, sanitize(name), runtimeDeathStopping))
	return err == nil
}

// markRuntimeDeathRecorded sets the pending marker. A failure here is tolerable
// and deliberately non-fatal: the cost is a duplicate record on a later poll,
// never a lost one.
func (p *Provider) markRuntimeDeathRecorded(name string) {
	dir := filepath.Join(p.metaDir, sanitize(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	os.WriteFile(filepath.Join(dir, runtimeDeathPending), nil, 0o644) //nolint:errcheck
}
