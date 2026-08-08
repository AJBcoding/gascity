package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// bdTelemetryConfig is the slice of bd's own configuration this check needs.
type bdTelemetryConfig struct {
	// MetricsEnabled is true when bd writes a telemetry event per CLI
	// invocation (bd's metrics.disabled = false).
	MetricsEnabled bool
	// EventFlushDisabled is true when nothing drains those events
	// (dolt.disable-event-flush = true).
	EventFlushDisabled bool
	// Found is false when no bd config file exists, in which case bd's own
	// defaults apply and this check has nothing to judge.
	Found bool
}

// BdTelemetryCheck reports bd telemetry that emits into a queue nothing
// drains.
//
// bd writes one event file per CLI invocation when its metrics are enabled.
// That is harmless while something flushes the queue. With flushing disabled
// the two settings combine into unbounded growth by construction, and gc
// drives bd constantly, so the whole fleet contributes: one host reached 12G
// across 65k+ entries with nothing in gc looking at it (gas-f85).
type BdTelemetryCheck struct {
	readConfig func() (bdTelemetryConfig, error)
}

// NewBdTelemetryCheck creates the bd telemetry check against the user-global
// bd configuration.
func NewBdTelemetryCheck() *BdTelemetryCheck {
	return &BdTelemetryCheck{readConfig: readBdTelemetryConfig}
}

// Name returns the check identifier ("bd-telemetry-queue").
func (c *BdTelemetryCheck) Name() string { return "bd-telemetry-queue" }

// Run executes the check.
func (c *BdTelemetryCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name(), Severity: SeverityAdvisory, Status: StatusOK}

	cfg, err := c.readConfig()
	if err != nil {
		// An unreadable bd config is not evidence of a leak. Say so and move
		// on rather than gating anything on a parse failure.
		r.Status = StatusWarning
		r.Message = "cannot read bd config to check telemetry settings"
		r.Details = []string{err.Error()}
		return r
	}
	if !cfg.Found {
		r.Message = "no bd config file; bd defaults apply"
		return r
	}
	if !cfg.MetricsEnabled || !cfg.EventFlushDisabled {
		r.Message = "bd telemetry is not accumulating"
		return r
	}

	r.Status = StatusWarning
	r.Message = "bd writes a telemetry event per invocation into a queue nothing drains"
	r.Details = []string{
		"metrics.disabled = false: bd emits one event file per CLI invocation",
		"dolt.disable-event-flush = true: those events are never drained",
		"gc drives bd constantly, so this grows for as long as both hold",
	}
	r.FixHint = "bd config set metrics.disabled true"
	return r
}

// readBdTelemetryConfig reads the user-global bd config. A missing file is
// reported as not-found rather than an error: bd runs fine without one.
func readBdTelemetryConfig() (bdTelemetryConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return bdTelemetryConfig{}, fmt.Errorf("resolving home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "bd", "config.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // fixed, user-owned config path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return bdTelemetryConfig{Found: false}, nil
		}
		return bdTelemetryConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}

	// Only the two keys matter; bd owns the rest of this file's schema and it
	// must not be a parse error here when bd adds to it.
	var raw struct {
		Metrics struct {
			Disabled *bool `yaml:"disabled"`
		} `yaml:"metrics"`
		Dolt struct {
			DisableEventFlush *bool `yaml:"disable-event-flush"`
		} `yaml:"dolt"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return bdTelemetryConfig{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	cfg := bdTelemetryConfig{Found: true}
	// bd emits unless metrics.disabled is explicitly true, so an absent key
	// means emitting.
	cfg.MetricsEnabled = raw.Metrics.Disabled == nil || !*raw.Metrics.Disabled
	cfg.EventFlushDisabled = raw.Dolt.DisableEventFlush != nil && *raw.Dolt.DisableEventFlush
	return cfg, nil
}

// CanFix reports false: the remedy writes to bd's user-global config, which
// is the operator's file and affects every tool on the host that reads it,
// not just this city. gc states the one-line command instead of reaching
// outside its own state to run it.
func (c *BdTelemetryCheck) CanFix() bool { return false }

// Fix is a no-op; CanFix reports false so the runner never calls it.
func (c *BdTelemetryCheck) Fix(_ *CheckContext) error { return nil }

// WarmupEligible reports false: an unbounded queue grows over days, so it
// does not need to be scanned on every `gc start`.
func (c *BdTelemetryCheck) WarmupEligible() bool { return false }
