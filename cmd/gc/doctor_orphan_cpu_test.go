package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/doctor"
)

func TestParseETime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"11:26", 11*time.Minute + 26*time.Second, true},
		{"00:05", 5 * time.Second, true},
		{"1:23:45", time.Hour + 23*time.Minute + 45*time.Second, true},
		{"11:26:03", 11*time.Hour + 26*time.Minute + 3*time.Second, true},
		{"2-03:04:05", 51*time.Hour + 4*time.Minute + 5*time.Second, true},
		{"11-03:44:12", 267*time.Hour + 44*time.Minute + 12*time.Second, true},
		{"  05:32  ", 5*time.Minute + 32*time.Second, true},
		{"", 0, false},
		{"abc", 0, false},
		{"1:2:3:4", 0, false},
		{"05", 0, false},
		{"x-01:00:00", 0, false},
		{"1-xx:00:00", 0, false},
		{"-1:00", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseETime(tc.in)
		if ok != tc.ok {
			t.Errorf("parseETime(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parseETime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParsePSInitChildren(t *testing.T) {
	// Shape of `ps -Ao ppid=,pid=,pcpu=,etime=,comm=`: right-aligned numeric
	// columns, command last and possibly containing spaces.
	out := strings.Join([]string{
		"    1   431   0.0 11-03:44:12 /usr/sbin/distnoted",
		"    1 54321  80.1    11:26:03 -zsh",
		"  502 54322   0.1       00:05 ps",
		"    1 54323  79.4    11:26:03 /Applications/Some App.app/Contents/MacOS/Some App",
		"garbage line",
		"    1 notapid 12.0   01:00 broken",
		"",
	}, "\n")

	got := parsePSInitChildren(out)
	if len(got) != 3 {
		t.Fatalf("parsePSInitChildren returned %d rows, want 3: %+v", len(got), got)
	}

	if got[0].PID != 431 || got[0].CPUPercent != 0.0 || got[0].Command != "/usr/sbin/distnoted" {
		t.Errorf("row 0 = %+v, want pid 431 / 0.0%% / distnoted", got[0])
	}
	if got[0].Elapsed != 267*time.Hour+44*time.Minute+12*time.Second {
		t.Errorf("row 0 elapsed = %v, want 267h44m12s", got[0].Elapsed)
	}
	if got[1].PID != 54321 || got[1].CPUPercent != 80.1 || got[1].Command != "-zsh" {
		t.Errorf("row 1 = %+v, want pid 54321 / 80.1%% / -zsh", got[1])
	}
	// A command containing spaces must survive as one field, not be truncated.
	if got[2].Command != "/Applications/Some App.app/Contents/MacOS/Some App" {
		t.Errorf("row 2 command = %q, want the full spaced path", got[2].Command)
	}
}

// psFixture builds a `ps` output line in the column shape the check parses.
func psFixture(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func newTestOrphanCPUCheck(out string, err error) *orphanCPUCheck {
	c := newOrphanCPUCheck()
	c.runPS = func() (string, error) { return out, err }
	return c
}

func TestOrphanCPUCheckIsAdvisoryAndNeverFixes(t *testing.T) {
	c := newOrphanCPUCheck()
	if c.CanFix() {
		t.Error("CanFix() = true; the check must never kill a process — an operator decides")
	}
	if err := c.Fix(&doctor.CheckContext{}); err != nil {
		t.Errorf("Fix() = %v, want nil no-op", err)
	}
	if c.WarmupEligible() {
		t.Error("WarmupEligible() = true, want false (matches every other in-tree check)")
	}
	if c.Name() != "orphan-cpu" {
		t.Errorf("Name() = %q, want %q", c.Name(), "orphan-cpu")
	}
}

func TestOrphanCPUCheckFlagsSustainedInitChildBurn(t *testing.T) {
	// The gas-7ipo incident shape: four `while :; do :; done` loops reparented
	// to init after `BG=$(jobs -p)` captured nothing, burning ~80% each for 11h.
	out := psFixture(
		"    1 54321  80.1    11:26:03 -zsh",
		"    1 54322  79.4    11:26:03 -zsh",
		"  502 54999   0.1       00:05 ps",
	)
	res := newTestOrphanCPUCheck(out, nil).Run(&doctor.CheckContext{})

	if res.Status != doctor.StatusWarning {
		t.Fatalf("Status = %v, want StatusWarning; message: %s", res.Status, res.Message)
	}
	if res.Severity != doctor.SeverityAdvisory {
		t.Errorf("Severity = %v, want SeverityAdvisory (observability must never gate)", res.Severity)
	}
	joined := res.Message + "\n" + strings.Join(res.Details, "\n")
	for _, want := range []string{"54321", "54322"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report omits PID %s; an operator needs exact PIDs to kill safely.\n%s", want, joined)
		}
	}
	// A pattern kill takes out other agents' gates and orphans their children
	// to init — this defect, inflicted deliberately and at wider scale. The
	// report may name `pkill` only to forbid it, and must point at the safe
	// alternative instead.
	if strings.Contains(joined, "pkill") && !strings.Contains(joined, "Do NOT pattern-kill") {
		t.Errorf("report mentions pkill without forbidding it.\n%s", joined)
	}
	if !strings.Contains(joined, "kill <pid>") {
		t.Errorf("report does not tell the reader to kill exact PIDs.\n%s", joined)
	}
}

func TestOrphanCPUCheckIgnoresIdleAndYoungProcesses(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{
			// Long-lived init children at rest: every launchd/systemd daemon.
			name: "idle daemon",
			out:  psFixture("    1   431   0.2 11-03:44:12 /usr/sbin/distnoted"),
		},
		{
			// Hot but young: a compile or test run that just started.
			name: "young burner",
			out:  psFixture("    1 54321  99.0       00:42 go"),
		},
		{
			// Not an init child: owned by a live parent, so someone can reap it.
			name: "owned burner",
			out:  psFixture("  502 54321  99.0    11:26:03 -zsh"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := newTestOrphanCPUCheck(tc.out, nil).Run(&doctor.CheckContext{})
			if res.Status != doctor.StatusOK {
				t.Errorf("Status = %v, want StatusOK; message: %s", res.Status, res.Message)
			}
		})
	}
}

// TestRunPSInitChildrenParsesRealProcessTable guards the `ps` invocation
// itself. Fixtures cannot catch a flag this platform's ps rejects or a column
// layout that shifted underneath the parser — only running the real command
// can. It asserts every elapsed value the real host reports is a format
// parseETime understands, so a silent drop of every row is impossible.
func TestRunPSInitChildrenParsesRealProcessTable(t *testing.T) {
	out, err := runPSInitChildren()
	if err != nil {
		t.Skipf("ps unavailable on this host: %v", err)
	}

	rows, parsed := 0, 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err != nil {
			continue
		}
		rows++
		if _, ok := parseETime(fields[3]); ok {
			parsed++
		}
	}

	if rows == 0 {
		t.Fatalf("ps produced no parseable rows — the -o column list is wrong for this platform.\nfirst 200 bytes: %q", out[:min(len(out), 200)])
	}
	if parsed != rows {
		t.Errorf("parseETime rejected %d of %d real elapsed values — the column layout or format assumption is wrong", rows-parsed, rows)
	}
}

func TestOrphanCPUCheckSkipsWhenPSUnavailable(t *testing.T) {
	res := newTestOrphanCPUCheck("", errors.New("exec: \"ps\": executable file not found")).Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusOK {
		t.Errorf("Status = %v, want StatusOK; an unavailable probe must not manufacture a finding", res.Status)
	}
	if !strings.Contains(res.Message, "skipped") {
		t.Errorf("Message = %q, want it to say the probe was skipped", res.Message)
	}
}
