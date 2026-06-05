package main

import (
	"time"

	"github.com/gastownhall/gascity/internal/adaptivepatrol"
	"github.com/gastownhall/gascity/internal/config"
)

// newAdaptivePatrolFromDaemon builds an adaptive-patrol controller from
// daemon config. Returns nil when adaptive_patrol is not enabled, so
// callers can skip the controller branches entirely. When enabled, the
// controller is constructed even with degenerate threshold/multiplier
// values — the controller's New itself treats those as no-op so
// callers do not need to special-case.
func newAdaptivePatrolFromDaemon(d config.DaemonConfig, base time.Duration) *adaptivepatrol.Controller {
	if !d.AdaptivePatrolEnabled() {
		return nil
	}
	return adaptivepatrol.New(
		base,
		d.AdaptivePatrolIdleThresholdOrDefault(),
		d.AdaptivePatrolMaxMultiplierOrDefault(),
	)
}
