package doctor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/events"
)

// WHY THIS CHECK EXISTS (gas-1jyd). Three separate token leaks ran for hours
// on this city and every one was found by a human doing a manual sweep:
// escalate-rollups retrying ~2,000x/day against an endpoint it could never
// reach; held+routed beads spawning a polecat every ~2 minutes that could only
// decline (one woke 130 times in 19.25h); and three Dolt-only orders firing on
// a MySQL city, failing 100% for 19h at ~150 runs/day.
//
// The unifying shape is WORK DISPATCHED TO AN ACTOR THAT CANNOT COMPLETE IT.
// Every component behaves correctly in isolation — a held bead spawns a polecat
// that correctly declines; a Dolt order on a MySQL city correctly exits
// EX_CONFIG. The waste lives in the DISAGREEMENT between components, which no
// single component can see. That is why per-component health checks miss it,
// and why this reasons over outcomes instead.
const (
	wastedWorkName = "wasted-work"

	// Signature 1 window. Order cadences vary widely, and the sample gates
	// below need enough firings to be meaningful: measured against this
	// city's log, a 6h window left the slow-cadence mol-dog-compactor at 3
	// firings — below the 5-firing gate — while failing 100%. 12h was the
	// shortest window that caught all three known-bad orders; 24h doubles
	// that margin for the price of a slightly longer backward read.
	wastedWorkOrderWindow = 24 * time.Hour

	// Signature 2 window. Tuned against the known spawn loop rather than
	// guessed: at the originally-suggested 1h window NOTHING fired, not even
	// the 130-wake incident, because its wakes came in bursts and no single
	// hour cleared the threshold. At 3h a merely-thrashy agent joined the
	// known-bad one. 6h fired on exactly the real incident with every other
	// agent silent, and held that discrimination out to 19h.
	wastedWorkSpawnWindow = 6 * time.Hour

	// A 100% failure rate is unambiguous and should alarm on few samples.
	wastedWorkFullFailureMinFires = 5
	// A partial rate needs more samples so genuine flake does not alarm:
	// orphan-sweep legitimately swung 3%-48% with load (az-ar8z), and that
	// was a real timeout issue rather than a config error. The detector must
	// not treat the two as the same thing.
	wastedWorkPartialFailureMinFires = 20
	wastedWorkPartialFailureRate     = 0.5

	// Below this many wakes an agent is not spawning enough to call a loop.
	wastedWorkSpawnMinWakes = 10

	// Memory guard for a pathological log. Generous enough that a normal 24h
	// window is never truncated; the check reports its effective window so a
	// truncated read cannot masquerade as a quiet one.
	wastedWorkMaxEvents = 200000

	// messageIssueType marks mail beads. Reading mail is not doing work — see
	// wastedWorkProgress.
	wastedWorkMessageIssueType = "message"
)

// wastedWorkReadFunc reads the trailing event window. Injectable so tests can
// drive the check without a real event log.
type wastedWorkReadFunc func(path string, since time.Time, types []string, maxEvents int) ([]events.Event, error)

// WastedWorkCheck alarms on work dispatched to an actor that cannot complete
// it: orders failing at a pathological rate, and agents spawning without
// making progress.
type WastedWorkCheck struct {
	cityPath    string
	clock       func() time.Time
	orderWindow time.Duration
	spawnWindow time.Duration
	maxEvents   int
	readWindow  wastedWorkReadFunc
}

// NewWastedWorkCheck creates the wasted-work detector.
func NewWastedWorkCheck(cityPath string) *WastedWorkCheck {
	return &WastedWorkCheck{
		cityPath:    cityPath,
		clock:       time.Now,
		orderWindow: wastedWorkOrderWindow,
		spawnWindow: wastedWorkSpawnWindow,
		maxEvents:   wastedWorkMaxEvents,
		readWindow:  events.ReadTailSince,
	}
}

// Name returns the check identifier shown by gc doctor.
func (c *WastedWorkCheck) Name() string { return wastedWorkName }

// CanFix reports whether the check can remediate. It cannot: the fix for a
// pathologically failing order is a config change whose shape depends on why
// it fails, and the fix for a spawn loop is a routing decision.
func (c *WastedWorkCheck) CanFix() bool { return false }

// Fix is a no-op because remediation depends on the root cause.
func (c *WastedWorkCheck) Fix(_ *CheckContext) error { return nil }

// Run evaluates both signatures over one bounded backward pass of the log.
func (c *WastedWorkCheck) Run(ctx *CheckContext) *CheckResult {
	result := &CheckResult{Name: c.Name()}

	cityPath := c.cityPath
	if cityPath == "" && ctx != nil {
		cityPath = ctx.CityPath
	}
	if cityPath == "" {
		result.Status = StatusError
		result.Message = "city path unavailable"
		return result
	}

	now := c.clock()
	if now.IsZero() {
		now = time.Now()
	}
	// One read covers the longer window; the shorter signature filters the
	// same slice. Reading twice would double the cost of the thing this check
	// exists to prevent.
	window := c.orderWindow
	if c.spawnWindow > window {
		window = c.spawnWindow
	}

	read := c.readWindow
	if read == nil {
		read = events.ReadTailSince
	}
	eventPath := filepath.Join(cityPath, citylayout.RuntimeRoot, "events.jsonl")
	evts, err := read(eventPath, now.Add(-window), wastedWorkEventTypes(), c.maxEvents)
	if err != nil {
		result.Status = StatusError
		result.Message = fmt.Sprintf("read event window: %v", err)
		return result
	}
	if len(evts) == 0 {
		result.Status = StatusOK
		result.Message = "no events in the trailing window"
		return result
	}

	orderFindings := c.detectFailingOrders(evts, now.Add(-c.orderWindow))
	spawnFindings := c.detectSpawnWithoutProgress(evts, now.Add(-c.spawnWindow))

	for _, finding := range orderFindings {
		result.Details = append(result.Details, finding.detail)
	}
	for _, finding := range spawnFindings {
		result.Details = append(result.Details, finding.detail)
	}

	// Report the window actually covered. A log that rotated, or a read the
	// memory cap truncated, yields a shorter window than requested, and a
	// short window is exactly when a quiet result is least trustworthy.
	// nonNegativeDuration guards against an event dated ahead of the clock;
	// a negative "covered" window would read as nonsense in the summary.
	covered := formatWastedWorkDuration(nonNegativeDuration(now.Sub(evts[0].Ts)))
	if len(orderFindings) == 0 && len(spawnFindings) == 0 {
		result.Status = StatusOK
		result.Message = fmt.Sprintf("no wasted work detected (%d events over the last %s)", len(evts), covered)
		return result
	}

	result.Status = StatusError
	// Advisory, not blocking. This detects waste, not incorrectness: a
	// misconfigured order burning tokens must be loud, but gating every
	// dispatch in the city on it would trade a token leak for a throughput
	// stall — a worse outcome than the one being reported.
	result.Severity = SeverityAdvisory
	result.Message = wastedWorkSummary(len(orderFindings), len(spawnFindings), covered)
	result.FixHint = wastedWorkFixHint(orderFindings, spawnFindings)
	return result
}

// wastedWorkEventTypes lists every type either signature consumes. Anything
// else is dropped at read time so the window costs only what it must.
func wastedWorkEventTypes() []string {
	return []string{
		events.OrderFired,
		events.OrderFailed,
		events.SessionWoke,
		events.BeadUpdated,
		events.BeadClosed,
	}
}

// wastedWorkFinding is one alarm, carrying the detail line and the inspect
// target used to build the fix hint.
type wastedWorkFinding struct {
	detail string
	hint   string
}

// detectFailingOrders implements Signature 1: an order whose firings almost
// always fail. Firings and failures are counted independently within the
// window rather than paired run-by-run — a failure whose firing predates the
// window still evidences a broken order, and the event log carries no run
// identity linking the two.
//
// An order with failures but no firings inside the window is skipped: its rate
// has no denominator, and the sample gates exist precisely so the detector
// speaks only where it has evidence. The cost is that an order slower than the
// window cannot reach the 5-firing gate, which is inherent to rate thresholds
// and is why the window is 24h rather than shorter.
func (c *WastedWorkCheck) detectFailingOrders(evts []events.Event, since time.Time) []wastedWorkFinding {
	fired := map[string]int{}
	failed := map[string]int{}
	messages := map[string]map[string]int{}
	for _, event := range evts {
		if event.Ts.Before(since) || event.Subject == "" {
			continue
		}
		switch event.Type {
		case events.OrderFired:
			fired[event.Subject]++
		case events.OrderFailed:
			failed[event.Subject]++
			if messages[event.Subject] == nil {
				messages[event.Subject] = map[string]int{}
			}
			messages[event.Subject][event.Message]++
		}
	}

	var findings []wastedWorkFinding
	for _, subject := range sortedKeys(fired) {
		fires := fired[subject]
		failures := failed[subject]
		if fires <= 0 || failures <= 0 {
			continue
		}
		rate := float64(failures) / float64(fires)
		alarming := (fires >= wastedWorkFullFailureMinFires && rate >= 1.0) ||
			(fires >= wastedWorkPartialFailureMinFires && rate >= wastedWorkPartialFailureRate)
		if !alarming {
			continue
		}
		// The message is what makes this actionable: "exit status 78" says
		// EX_CONFIG/misconfiguration rather than flake, which is the
		// difference between a config fix and a retry.
		detail := fmt.Sprintf("order %s: %d/%d firings failed (%.0f%%) in the last %s",
			subject, failures, fires, rate*100, formatWastedWorkDuration(c.orderWindow))
		if common := mostCommon(messages[subject]); common != "" {
			detail += fmt.Sprintf("; most common: %q", common)
		}
		findings = append(findings, wastedWorkFinding{
			detail: detail,
			hint:   fmt.Sprintf("gc order history %s --limit 20", orderNameFromSubject(subject)),
		})
	}
	return findings
}

// detectSpawnWithoutProgress implements Signature 2: an agent woken many times
// that completed nothing.
//
// ATTRIBUTION — verified against this city's log rather than assumed, because
// a detector built on a correlation that does not hold is worse than none.
// The event `actor` field carries NO agent identity for any relevant type: it
// is uniformly "gc" on session.woke/session.stopped (1368/1368 events) and
// "cache-reconcile" on every bead.created/updated/closed (9518/9518 closes).
// So neither "closes whose actor is this agent" nor the fallback "no
// bead.updated in between from that actor" can discriminate at all — both
// would report every agent as idle.
//
// What DOES carry identity is the bead payload: `payload.assignee` holds agent
// names in the same namespace as `session.woke.subject`. This joins entirely
// within the event log, with no bead-store round trip.
//
// Progress deliberately EXCLUDES message beads. The known spawn-loop agent
// closed two beads during its 19h of doing nothing, and both were mail
// (issue_type "message"). Counting them would have silenced the alarm on the
// exact incident this exists to catch, and silenced it because the agent read
// its mail.
func (c *WastedWorkCheck) detectSpawnWithoutProgress(evts []events.Event, since time.Time) []wastedWorkFinding {
	agentBySession := wastedWorkAgentsBySession(evts)

	wakes := map[string]int{}
	progress := map[string]int{}
	for _, event := range evts {
		if event.Ts.Before(since) {
			continue
		}
		switch event.Type {
		case events.SessionWoke:
			agent := wastedWorkCanonicalAgent(event.Subject, event.SessionID, agentBySession)
			if agent != "" {
				wakes[agent]++
			}
		case events.BeadUpdated, events.BeadClosed:
			payload := decodeWastedWorkBead(event.Payload)
			if payload.Assignee == "" || payload.IssueType == wastedWorkMessageIssueType {
				continue
			}
			if event.Type == events.BeadUpdated && payload.Status != "in_progress" {
				continue
			}
			progress[wastedWorkCanonicalAssignee(payload.Assignee, agentBySession)]++
		}
	}

	var findings []wastedWorkFinding
	for _, agent := range sortedKeys(wakes) {
		woke := wakes[agent]
		if woke < wastedWorkSpawnMinWakes || progress[agent] > 0 {
			continue
		}
		findings = append(findings, wastedWorkFinding{
			detail: fmt.Sprintf("agent %s: woke %d times in the last %s and claimed or closed no work",
				agent, woke, formatWastedWorkDuration(c.spawnWindow)),
			hint: fmt.Sprintf("gc agents peek %s", agent),
		})
	}
	return findings
}

// wastedWorkBeadPayload is the slice of a bead event payload this check reads.
type wastedWorkBeadPayload struct {
	Assignee  string `json:"assignee"`
	Status    string `json:"status"`
	IssueType string `json:"issue_type"`
}

func decodeWastedWorkBead(payload []byte) wastedWorkBeadPayload {
	var out wastedWorkBeadPayload
	if len(payload) == 0 {
		return out
	}
	// A payload this check cannot parse contributes nothing rather than
	// failing the run; the alternative is one malformed line turning the
	// detector red for a reason unrelated to wasted work.
	_ = json.Unmarshal(payload, &out)
	return out
}

// wastedWorkAgentsBySession maps a session id to its agent name. Identity has
// two spellings in the log: the agent name ("gascity/gastown.furiosa") and the
// ephemeral session name ("gastown__polecat-az-wisp-b4ee1"), and both appear as
// wake subjects AND as bead assignees. Without this map one agent's activity
// splits across two keys, which can drop each below threshold and hide a real
// loop. session_id is present on every session.woke event, so it is the
// reliable join key.
func wastedWorkAgentsBySession(evts []events.Event) map[string]string {
	out := map[string]string{}
	for _, event := range evts {
		if event.Type != events.SessionWoke || event.SessionID == "" || event.Subject == "" {
			continue
		}
		// Prefer the agent-name spelling; only fall back to a session-name
		// subject when nothing better has been seen for this session.
		if isSessionNameForm(event.Subject) {
			if _, ok := out[event.SessionID]; !ok {
				out[event.SessionID] = event.Subject
			}
			continue
		}
		out[event.SessionID] = event.Subject
	}
	return out
}

// isSessionNameForm reports whether an identity is the ephemeral session-name
// spelling rather than a stable agent name.
func isSessionNameForm(identity string) bool {
	return strings.Contains(identity, "__")
}

// wastedWorkCanonicalAgent resolves a wake subject to a stable agent name.
func wastedWorkCanonicalAgent(subject, sessionID string, agentBySession map[string]string) string {
	if subject != "" && !isSessionNameForm(subject) {
		return subject
	}
	if resolved, ok := agentBySession[sessionID]; ok && !isSessionNameForm(resolved) {
		return resolved
	}
	return subject
}

// wastedWorkCanonicalAssignee resolves a bead assignee to a stable agent name.
// A session-name assignee embeds the session id as its suffix, which is how a
// polecat that claims under its session identity is credited to the agent that
// spawned it.
func wastedWorkCanonicalAssignee(assignee string, agentBySession map[string]string) string {
	if !isSessionNameForm(assignee) {
		return assignee
	}
	for sessionID, agent := range agentBySession {
		if sessionID != "" && strings.HasSuffix(assignee, sessionID) && !isSessionNameForm(agent) {
			return agent
		}
	}
	return assignee
}

func wastedWorkSummary(orderCount, spawnCount int, covered string) string {
	var parts []string
	if orderCount > 0 {
		parts = append(parts, fmt.Sprintf("%s failing at a pathological rate", pluralize(orderCount, "order", "orders")))
	}
	if spawnCount > 0 {
		parts = append(parts, fmt.Sprintf("%s spawning without progress", pluralize(spawnCount, "agent", "agents")))
	}
	return fmt.Sprintf("%s (last %s)", strings.Join(parts, ", "), covered)
}

// wastedWorkFixHint points at the first finding. Order findings come first:
// their message usually names the cause outright.
func wastedWorkFixHint(orderFindings, spawnFindings []wastedWorkFinding) string {
	if len(orderFindings) > 0 {
		return "Inspect with: " + orderFindings[0].hint
	}
	if len(spawnFindings) > 0 {
		return "Inspect with: " + spawnFindings[0].hint
	}
	return ""
}

// orderNameFromSubject strips the rig scope an order subject may carry
// ("escalate-rollups:rig:python419") so the hint names something runnable.
func orderNameFromSubject(subject string) string {
	if name, _, found := strings.Cut(subject, ":rig:"); found {
		return name
	}
	return subject
}

func mostCommon(counts map[string]int) string {
	best := ""
	bestCount := 0
	for _, message := range sortedKeys(counts) {
		if counts[message] > bestCount {
			best, bestCount = message, counts[message]
		}
	}
	return best
}

// sortedKeys keeps details, and therefore golden output, deterministic.
func sortedKeys(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for key := range counts {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func formatWastedWorkDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Round(time.Minute)/time.Minute))
	}
	d = d.Round(time.Hour)
	return fmt.Sprintf("%dh", int(d/time.Hour))
}
