package main

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// shippedCloseBoundaryCheck qualifies the boundary Gas City can actually
// enforce. Managed writes enter through gc and share the shipped-close policy,
// but the pinned Beads v1.1.0 provider has no atomic pre-close policy hook. A
// separately invoked raw bd binary can therefore mutate a bd-backed scope
// without consulting Gas City.
type shippedCloseBoundaryCheck struct {
	cityPath string
	cfg      *config.City
}

func newShippedCloseBoundaryCheck(cityPath string, cfg *config.City) *shippedCloseBoundaryCheck {
	return &shippedCloseBoundaryCheck{cityPath: cityPath, cfg: cfg}
}

func (c *shippedCloseBoundaryCheck) Name() string { return "shipped-close-boundary" }

func (c *shippedCloseBoundaryCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name()}
	if c.cfg == nil {
		res.Status = doctor.StatusOK
		res.Message = "shipped-close boundary check skipped until expanded config loads"
		return res
	}

	bdScopes := c.bdBackedScopes()
	if c.cfg.Beads.ShippedCloseWarnOnlyEnabled() {
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = "shipped-close policy is not production-enforced while warn-only compatibility is enabled"
		res.Details = append(res.Details, "supported managed closing mutations must enter through `gc bd`; direct raw `bd` writes are unsupported")
		if len(bdScopes) > 0 {
			res.Details = append(res.Details, "raw-close-reachable scopes: "+strings.Join(bdScopes, ", "))
		}
		// Advisory, never a block. The mode is scoped to the mutation target,
		// so "warn-only is on" no longer describes the whole city, and an
		// operator who reads it that way will file a refusal from a
		// fence-capable rig as a bug. Naming those scopes here is the cheapest
		// place to correct that; refusing the topology outright would fail a
		// whole city for a per-scope arrangement that is individually sane.
		res.Details = append(res.Details, "the compatibility mode relaxes only closes written through the pinned bd contract")
		if strict := c.strictlyEnforcedScopes(); len(strict) > 0 {
			res.Details = append(res.Details, "still strictly enforced (natively fence-capable): "+strings.Join(strict, ", "))
		}
		res.FixHint = "disable beads.shipped_close_warn_only after the shipped-work audit and observation gate pass"
		return res
	}

	if !c.cfg.Beads.DirectRawBDWrites {
		res.Status = doctor.StatusOK
		res.Message = "shipped-close policy covers configured supported managed mutation surfaces"
		res.Details = []string{"arbitrary out-of-band raw `bd` execution is unsupported and cannot be detected by configuration validation"}
		return res
	}

	if len(bdScopes) == 0 {
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = "beads.direct_raw_bd_writes is enabled but no bd-backed scope is configured"
		res.FixHint = "remove the stale direct_raw_bd_writes declaration"
		return res
	}

	res.Status = doctor.StatusError
	res.Severity = doctor.SeverityBlocking
	res.Message = "production shipped-close enforcement cannot be qualified while beads.direct_raw_bd_writes enables raw `bd` closes"
	res.Details = []string{
		"raw-close-reachable scopes: " + strings.Join(bdScopes, ", "),
		"pinned Beads v1.1.0 exposes only post-mutation hooks, not an atomic pre-close policy hook",
		"supported managed closing mutations must enter through `gc bd`; separately invoked raw `bd` close/status-close writes are unsupported",
	}
	res.FixHint = "disable beads.direct_raw_bd_writes and route supported closes through gc bd, or keep production qualification disabled until the provider offers an atomic pre-close policy hook"
	return res
}

func (c *shippedCloseBoundaryCheck) bdBackedScopes() []string {
	var scopes []string
	if c.scopeHasConfiguredOrEffectiveBDPath(c.cityPath) {
		scopes = append(scopes, fmt.Sprintf("city (%s)", c.bdScopeEvidence(c.cityPath)))
	}
	for _, rig := range c.cfg.Rigs {
		if strings.TrimSpace(rig.Path) == "" || !c.scopeHasConfiguredOrEffectiveBDPath(rig.Path) {
			continue
		}
		scopes = append(scopes, fmt.Sprintf("rig:%s (%s)", rig.Name, c.bdScopeEvidence(rig.Path)))
	}
	return scopes
}

// scopeHasConfiguredOrEffectiveBDPath deliberately considers both the stable
// city configuration/store marker and this doctor's effective provider env.
// A transient GC_BEADS=file must not hide a configured raw-bd declaration,
// while GC_BEADS=bd is itself an effective raw-bd-capable path worth reporting.
func (c *shippedCloseBoundaryCheck) scopeHasConfiguredOrEffectiveBDPath(scopeRoot string) bool {
	if providerUsesBdStoreContract(rawBeadsProviderForScope(scopeRoot, c.cityPath)) {
		return true
	}
	if scopeUsesBdStoreContract(scopeRoot) {
		return true
	}
	// A scope-local file marker is authoritative for a distinct rig, so do not
	// apply the city's configured provider to that rig. At the city root the
	// configured provider is independent policy evidence: a stale file marker
	// and a transient GC_BEADS=file override must not erase configured bd reachability.
	if !samePath(scopeRoot, c.cityPath) && scopeUsesFileStoreContract(scopeRoot) {
		return false
	}
	provider := strings.TrimSpace(c.cfg.Beads.Provider)
	if provider == "" {
		provider = "bd"
	}
	return providerUsesBdStoreContract(provider)
}

// strictlyEnforcedScopes names the scopes whose managed closes keep refusing
// contract violations while beads.shipped_close_warn_only is true.
//
// It reads the close-routing resolver the gate itself consults, NOT this
// check's raw-reachability probe above. The two answer different questions on
// purpose: raw reachability is deliberately generous, because a transient
// GC_BEADS=file must not hide a configured raw-bd entrypoint, while an
// operator reading this line is asking which of their scopes will still refuse
// a close today. Only the resolver that routes the close can answer that.
func (c *shippedCloseBoundaryCheck) strictlyEnforcedScopes() []string {
	var scopes []string
	if !workRecordCloseTargetForScope(c.cityPath, c.cityPath).BDStoreContract {
		scopes = append(scopes, fmt.Sprintf("city (%s)", c.bdScopeEvidence(c.cityPath)))
	}
	for _, rig := range c.cfg.Rigs {
		if strings.TrimSpace(rig.Path) == "" || workRecordCloseTargetForScope(rig.Path, c.cityPath).BDStoreContract {
			continue
		}
		scopes = append(scopes, fmt.Sprintf("rig:%s (%s)", rig.Name, c.bdScopeEvidence(rig.Path)))
	}
	return scopes
}

func (c *shippedCloseBoundaryCheck) bdScopeEvidence(scopeRoot string) string {
	effective := rawBeadsProviderForScope(scopeRoot, c.cityPath)
	configured := strings.TrimSpace(c.cfg.Beads.Provider)
	if configured == "" {
		configured = "bd"
	}
	if effective == configured {
		return "provider=" + effective
	}
	return fmt.Sprintf("configured=%s effective=%s", configured, effective)
}

func (c *shippedCloseBoundaryCheck) CanFix() bool { return false }

func (c *shippedCloseBoundaryCheck) Fix(_ *doctor.CheckContext) error { return nil }

// gc start's warmup scan is intentionally fail-open and cannot serve as a
// production gate. This check gates explicit gc doctor qualification instead.
func (c *shippedCloseBoundaryCheck) WarmupEligible() bool { return false }
