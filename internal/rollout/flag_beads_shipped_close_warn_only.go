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
	// condition for that mandatory deletion. The revision-fence clause leads
	// because it is why a bd-backed city enables the mode at all: the pinned bd
	// cannot fence a managed close, so removing the mode before a fence-capable
	// bd is pinned and qualified would refuse every gated work-record close
	// rather than enforce one. The audit and observation clauses cannot detect
	// that, so satisfying them alone must never read as ready.
	ShippedCloseWarnOnlyRemovalCondition = "a bd exposing --if-revision is pinned (upstream beads#4682) and its revision-fence qualification is green, gc beads audit-shipped reports complete=true clean=true, and the durable event-journal query completes with zero acknowledged work.close.warn_only.used events throughout the declared observation window (warn-only closes refuse unless each event is durably read back)"
)

// ShippedCloseWarnOnly reports the city's configured bounded compatibility
// setting. The built-in and zero-value defaults are false (enforced).
//
// It is the SETTING, not the decision. The mode exists for one deficiency —
// the pinned bd cannot revision-fence a managed close — so a close is relaxed
// only when the store it actually mutates is written through that contract.
// Consumers pass this value and their resolved mutation target to
// workclose.Enforce; reading it alone re-creates the city-global rule that
// stripped strict enforcement from natively fence-capable stores.
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
