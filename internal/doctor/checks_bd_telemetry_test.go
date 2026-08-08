package doctor

import (
	"strings"
	"testing"
)

// bd writes one telemetry event file per CLI invocation when metrics are
// enabled, and gc drives bd constantly. With event flushing disabled those
// files are written and never drained, so the queue grows without bound —
// this host reached 12G and 65k+ entries before anyone noticed, because
// nothing in gc looked (gas-f85). These tests pin what the check must say.

func TestBdTelemetryCheckFlagsUnboundedCombination(t *testing.T) {
	c := &BdTelemetryCheck{
		readConfig: func() (bdTelemetryConfig, error) {
			return bdTelemetryConfig{MetricsEnabled: true, EventFlushDisabled: true, Found: true}, nil
		},
	}
	r := c.Run(nil)
	if r.Status != StatusWarning {
		t.Fatalf("Status = %v; want a warning when bd emits into a queue nothing drains", r.Status)
	}
	// The operator needs the one command that stops it, not a description.
	if !strings.Contains(r.FixHint, "metrics.disabled") {
		t.Errorf("FixHint = %q; want the bd config command that stops the emission", r.FixHint)
	}
}

func TestBdTelemetryCheckPassesWhenMetricsDisabled(t *testing.T) {
	c := &BdTelemetryCheck{
		readConfig: func() (bdTelemetryConfig, error) {
			return bdTelemetryConfig{MetricsEnabled: false, EventFlushDisabled: true, Found: true}, nil
		},
	}
	if r := c.Run(nil); r.Status != StatusOK {
		t.Fatalf("Status = %v; want OK — nothing is being emitted, so the drain setting cannot accumulate", r.Status)
	}
}

// Emission with flushing ENABLED is not the unbounded case: the queue drains.
// It must not warn, or the check cries wolf on a healthy default and gets
// ignored — which is how the real one stayed invisible.
func TestBdTelemetryCheckPassesWhenFlushEnabled(t *testing.T) {
	c := &BdTelemetryCheck{
		readConfig: func() (bdTelemetryConfig, error) {
			return bdTelemetryConfig{MetricsEnabled: true, EventFlushDisabled: false, Found: true}, nil
		},
	}
	if r := c.Run(nil); r.Status != StatusOK {
		t.Fatalf("Status = %v; want OK — emitted events are drained", r.Status)
	}
}

// No bd config file at all means bd's own defaults apply, which this check
// cannot read. Reporting a warning there would fire on every host that never
// wrote a config, so it stays quiet and says why.
func TestBdTelemetryCheckIsQuietWhenNoConfigFound(t *testing.T) {
	c := &BdTelemetryCheck{
		readConfig: func() (bdTelemetryConfig, error) { return bdTelemetryConfig{Found: false}, nil }, //nolint:nilerr
	}
	if r := c.Run(nil); r.Status != StatusOK {
		t.Fatalf("Status = %v; want OK when there is no bd config to judge", r.Status)
	}
}

func TestBdTelemetryCheckSeverityIsAdvisory(t *testing.T) {
	c := &BdTelemetryCheck{
		readConfig: func() (bdTelemetryConfig, error) {
			return bdTelemetryConfig{MetricsEnabled: true, EventFlushDisabled: true, Found: true}, nil
		},
	}
	// Disk filling slowly is real, but it must never gate a deploy the way a
	// broken store would.
	if r := c.Run(nil); r.Severity != SeverityAdvisory {
		t.Errorf("Severity = %v; want advisory", r.Severity)
	}
}
