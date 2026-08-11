package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/doctor"
)

// orphanCPUCheck reports processes that have been reparented to init and are
// burning CPU — host-level damage that no bead, session, or worktree check can
// see.
//
// Every liveness check Gas City has is scoped to its own objects: the witness
// watches beads and sessions, the reapers watch worktrees and dolt dirs. A
// process whose parent died belongs to no session, so no agent owns it and no
// salvage rule covers it. It is invisible to all of them. That gap was measured
// at 11h26m (gas-7ipo): an agent benchmarking a flaky timing test wrote
//
//	for j in 1 2 3 4; do (while :; do :; done) & done
//	BG=$(jobs -p)
//	kill $BG 2>/dev/null
//
// where `BG=$(jobs -p)` runs in a command-substitution subshell and under
// `zsh -c` captured no PIDs at all, so the kill was a silent no-op. The four
// busy loops reparented to init and spun at ~80% each — 3.2 of 16 cores, half a
// day, doing nothing. Nothing noticed. This check would have named them in
// minutes.
//
// Pure observability (SeverityAdvisory, CanFix false): it runs one `ps` and
// never signals anything. That is deliberate, not an omission. A process at
// PPID 1 is not evidence of a bug — it is how every launchd and systemd daemon
// legitimately runs — so the classification "runaway vs. legitimate hog" is a
// judgment call, and judgment calls do not belong in Go. The check reports the
// three facts that make the call possible (init-owned, sustained burn, how
// long) and names exact PIDs so whoever reads it can act precisely. It
// deliberately does not suggest a pattern kill: roughly ten agents run
// identical `go test` commands on a city host, so a `pkill -f` takes out other
// agents' work and orphans their children to init — inflicting this very defect
// at wider scale.
//
// The %CPU column means different things per platform, and the check works on
// both for different reasons. Linux ps reports cputime/elapsed — a true
// lifetime average — so a loop orphaned hours ago keeps reporting the ~80% it
// has burned all along. BSD/macOS ps reports "a decaying average over up to a
// minute of previous (real) time", so it reports what the process is burning
// now. Either way a sustained burner reads high and an idle daemon reads low,
// which is the only distinction this check draws.
//
// Where they differ is which imprecision each carries, and neither is worth
// engineering around here: Linux keeps reporting a process that burned hard
// early and has since gone quiet, while macOS can catch a minute-long spike
// inside a long-lived process. minElapsed bounds the second; nothing bounds the
// first. That asymmetry is a reason this check stays advisory and names PIDs
// for a human to identify — not a reason to build cross-platform sampling into
// a diagnostic that runs one ps.
type orphanCPUCheck struct {
	// minCPUPercent is the lifetime-average %CPU at or above which an
	// init-owned process is worth reporting.
	minCPUPercent float64
	// minElapsed is how long a process must have been alive before its
	// average is meaningful, so ordinary short-lived load is not reported.
	minElapsed time.Duration
	// runPS returns raw `ps` output in the column shape parsePSInitChildren
	// expects. Injectable for tests.
	runPS func() (string, error)
}

const (
	// defaultOrphanCPUPercent is the threshold from the gas-7ipo field
	// measurement: the leaked busy loops sat at ~79-80% lifetime average, and
	// no legitimate init-owned daemon on a healthy city host approaches it.
	defaultOrphanCPUPercent = 50.0
	// defaultOrphanMinElapsed keeps ordinary work out of the report. A build
	// or test binary can hold 100% for a minute or two entirely legitimately;
	// past this it is either long work someone should know about, or a leak.
	defaultOrphanMinElapsed = 5 * time.Minute
	// orphanPSTimeout bounds the probe. Doctor runs on patrol ticks, so a
	// hung ps must not stall the run; a timeout yields no output, which the
	// check reports as skipped rather than as a clean result.
	orphanPSTimeout = 5 * time.Second
)

// initChildProc is one process whose parent is PID 1, as reported by ps.
type initChildProc struct {
	PID        int
	CPUPercent float64
	Elapsed    time.Duration
	Command    string
}

func newOrphanCPUCheck() *orphanCPUCheck {
	return &orphanCPUCheck{
		minCPUPercent: defaultOrphanCPUPercent,
		minElapsed:    defaultOrphanMinElapsed,
		runPS:         runPSInitChildren,
	}
}

func (c *orphanCPUCheck) Name() string                     { return "orphan-cpu" }
func (c *orphanCPUCheck) CanFix() bool                     { return false }
func (c *orphanCPUCheck) Fix(_ *doctor.CheckContext) error { return nil }
func (c *orphanCPUCheck) WarmupEligible() bool             { return false }

func (c *orphanCPUCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name(), Severity: doctor.SeverityAdvisory}

	out, err := c.runPS()
	if err != nil {
		res.Status = doctor.StatusOK
		res.Message = fmt.Sprintf("orphan-cpu: process table unavailable (%v) — skipped", err)
		return res
	}

	scanned := parsePSInitChildren(out)
	var burners []initChildProc
	for _, p := range scanned {
		if p.CPUPercent >= c.minCPUPercent && p.Elapsed >= c.minElapsed {
			burners = append(burners, p)
		}
	}

	if len(burners) == 0 {
		res.Status = doctor.StatusOK
		res.Message = fmt.Sprintf("no orphaned CPU burners: %d live init-owned processes scanned, none averaging >=%.0f%% CPU for %s or longer",
			len(scanned), c.minCPUPercent, c.minElapsed)
		return res
	}

	var total float64
	for _, p := range burners {
		total += p.CPUPercent
	}
	res.Status = doctor.StatusWarning
	res.Message = fmt.Sprintf("%d init-owned process(es) burning CPU with no owner: ~%.1f cores total (>=%.0f%% avg for %s+)",
		len(burners), total/100, c.minCPUPercent, c.minElapsed)

	res.Details = []string{
		"These processes were reparented to PID 1, so their parent is gone and no session,",
		"bead, or worktree check will ever account for them. They are burning CPU right now:",
	}
	for _, p := range burners {
		res.Details = append(res.Details, fmt.Sprintf("  PID %d  %.1f%% avg  up %s  %s",
			p.PID, p.CPUPercent, formatOrphanElapsed(p.Elapsed), p.Command))
	}
	res.Details = append(res.Details,
		"",
		"Some init-owned processes are legitimate (launchd/systemd daemons, a nohup'd job).",
		"Identify each PID before acting: ps -p <pid> -o pid=,ppid=,lstart=,args=",
		"Kill only the exact PIDs you have identified, one at a time: kill <pid>",
		"Do NOT pattern-kill. Roughly ten agents run identical `go test` commands on a city",
		"host, so `pkill -f` takes out other agents' gates and orphans their children to",
		"init — inflicting this same defect deliberately and at wider scale.",
		"",
		"Preventing it: when a shell spawns background load, capture each PID at spawn time",
		"and trap the cleanup, because `BG=$(jobs -p)` runs in a subshell and captures",
		"nothing — which is how this class of leak is born:",
		"  pids=(); for j in 1 2 3 4; do (while :; do :; done) & pids+=($!); done",
		"  trap 'kill \"${pids[@]}\" 2>/dev/null' EXIT INT TERM",
	)
	return res
}

// runPSInitChildren runs `ps` and returns raw output with one row per process:
// ppid, pid, state, %cpu, elapsed, command. The `=` suffixes suppress column
// headers, which POSIX, BSD (macOS), and Linux ps all honor, so the output
// needs no header skipping and parses the same everywhere.
func runPSInitChildren() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), orphanPSTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "ps", "-Ao", "ppid=,pid=,state=,pcpu=,etime=,comm=").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parsePSInitChildren returns the live rows of ps output whose parent is PID 1.
//
// Zombies are dropped. A zombie keeps the lifetime-average %CPU it earned while
// alive but consumes nothing, and it is reparented to init by definition — so
// without this it is the false positive most likely to reach an operator, and
// the one most likely to send them killing something already dead. State is
// multi-character on BSD ("ZN") and single on Linux ("Z"), so the test is a
// prefix, matching how internal/pidutil reads the same field.
//
// Rows it cannot parse are skipped rather than reported: a malformed row is a
// row we know nothing about, and inventing a finding from it would be worse
// than missing it.
func parsePSInitChildren(out string) []initChildProc {
	var procs []initChildProc
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// ppid pid state pcpu etime comm... — comm is last, may contain spaces.
		if len(fields) < 6 {
			continue
		}
		ppid, err := strconv.Atoi(fields[0])
		if err != nil || ppid != 1 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if strings.HasPrefix(fields[2], "Z") {
			continue
		}
		cpu, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			continue
		}
		elapsed, ok := parseETime(fields[4])
		if !ok {
			continue
		}
		procs = append(procs, initChildProc{
			PID:        pid,
			CPUPercent: cpu,
			Elapsed:    elapsed,
			Command:    strings.Join(fields[5:], " "),
		})
	}
	return procs
}

// parseETime parses ps's elapsed-time format, [[DD-]HH:]MM:SS. It reports
// ok=false for anything it cannot read exactly, so a format this does not
// understand drops the row instead of producing a wrong duration.
func parseETime(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}

	var days int
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		d, err := strconv.Atoi(s[:dash])
		if err != nil || d < 0 {
			return 0, false
		}
		days = d
		s = s[dash+1:]
	}

	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var hours int
	if len(parts) == 3 {
		h, err := strconv.Atoi(parts[0])
		if err != nil || h < 0 {
			return 0, false
		}
		hours = h
		parts = parts[1:]
	}
	minutes, err := strconv.Atoi(parts[0])
	if err != nil || minutes < 0 {
		return 0, false
	}
	seconds, err := strconv.Atoi(parts[1])
	if err != nil || seconds < 0 {
		return 0, false
	}

	return time.Duration(days)*24*time.Hour +
		time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second, true
}

// formatOrphanElapsed renders a duration for the report. Go's default prints
// an 11-hour leak as "11h26m3s"; operators reading a CPU report scan for hours
// and minutes, so seconds are dropped past the first hour.
func formatOrphanElapsed(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
