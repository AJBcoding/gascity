package rollout

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/config"
)

// KeyBeadsShippedCloseWarnOnly is the registered bounded compatibility gate.
const KeyBeadsShippedCloseWarnOnly = "beads.shipped_close_warn_only"

const (
	keyBeadsShippedCloseWarnOnly = KeyBeadsShippedCloseWarnOnly
	// ShippedCloseWarnOnlyRemovalVersion is the exact release floor at which
	// the setting, warning path, doctor notice, and telemetry must be deleted.
	ShippedCloseWarnOnlyRemovalVersion = "v1.6.0"
	// ShippedCloseWarnOnlyRemovalCondition is the operator-verifiable readiness
	// condition for that mandatory deletion.
	ShippedCloseWarnOnlyRemovalCondition = "gc beads audit-shipped reports complete=true clean=true and the durable event-journal query completes with zero acknowledged work.close.warn_only.used events throughout the declared observation window (warn-only closes refuse unless each event is durably read back)"
)

// ShippedCloseWarnOnly reports whether managed close violations warn instead
// of refusing. The built-in and zero-value defaults are false (enforced).
func (f Flags) ShippedCloseWarnOnly() bool { return f.shippedCloseWarnOnly.value }

// WithShippedCloseWarnOnly overrides the gate on a ForTest Flags value.
func WithShippedCloseWarnOnly(enabled bool) ForTestOption {
	return func(b *flagsBuilder) {
		b.flags.shippedCloseWarnOnly = resolved[bool]{value: enabled, origin: OriginConfig}
	}
}

func readBeadsShippedCloseWarnOnly(cfg *config.City) (value bool, defined bool) {
	if cfg.Beads.ShippedCloseWarnOnly == nil {
		return false, false
	}
	return *cfg.Beads.ShippedCloseWarnOnly, true
}

func resolveBeadsShippedCloseWarnOnly(cfg *config.City, f *Flags) {
	value, defined := readBeadsShippedCloseWarnOnly(cfg)
	if !defined {
		return
	}
	f.shippedCloseWarnOnly = resolved[bool]{value: value, origin: OriginConfig}
	if value {
		f.notices = append(f.notices, Notice{
			Kind: NoticeCompatibilityModeActive, FlagKey: keyBeadsShippedCloseWarnOnly,
			ConfigValue: "true",
			Message:     fmt.Sprintf("%s=true is a temporary warn-only compatibility mode; remove it by %s after %s", keyBeadsShippedCloseWarnOnly, ShippedCloseWarnOnlyRemovalVersion, ShippedCloseWarnOnlyRemovalCondition),
		})
	}
}
