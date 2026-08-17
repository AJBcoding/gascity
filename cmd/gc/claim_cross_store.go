package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/sling"
)

// crossStoreClaimDir resolves the working directory a cross-store-eligible
// agent's bd write (claim / update) must run in so it targets the bead's OWN
// store rather than the agent's default store. This is the WRITE counterpart to
// the gc-hook read federation (vp-kvp stage iii): a city-scoped singleton sees
// rig-store work via the federated work query, so its claim must land in that
// rig store.
//
// Returns ("", false) when no redirection applies — a rig-scoped agent
// (byte-for-byte unchanged), or a bead whose prefix does not resolve to a
// configured rig (HQ/city-owned work stays in the agent's own store). The pure
// signature keeps the per-bead routing decision unit-testable; the impure
// store-env edge lives in agentScriptCrossStoreBeadEnv.
func crossStoreClaimDir(cfg *config.City, agentCfg *config.Agent, beadID string) (string, bool) {
	if cfg == nil || !agentIsCrossStoreEligible(agentCfg) {
		return "", false
	}
	rigDir := sling.RigDirForBead(cfg, strings.TrimSpace(beadID))
	if rigDir == "" {
		return "", false
	}
	return rigDir, true
}

// agentScriptCrossStoreBeadEnv is the impure edge wired into the agent-script
// bd_claim / bd_update path: it resolves the current agent identity and city
// config from the environment and, for a cross-store-eligible runner, returns
// the rig store directory + bd subprocess env a write against beadID must use.
// Best-effort — any resolution failure returns ok=false so the write falls back
// to the default (inherited) environment, preserving rig-agent behavior.
func agentScriptCrossStoreBeadEnv(beadID string) (string, []string, bool) {
	dir, env, redirect, err := agentScriptManagedCloseBeadEnv(beadID)
	if err != nil {
		return "", nil, false
	}
	return dir, env, redirect
}

// agentScriptManagedCloseBeadEnv preserves the difference between a legitimate
// inherited-store target and an inability to resolve the target. Managed close
// callers must refuse the latter rather than guessing at a possibly duplicate
// row in the inherited store.
func agentScriptManagedCloseBeadEnv(beadID string) (string, []string, bool, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		return "", nil, false, fmt.Errorf("bead ID is required")
	}
	cityPath, err := resolveCity()
	if err != nil {
		return "", nil, false, fmt.Errorf("resolving city: %w", err)
	}
	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		return "", nil, false, fmt.Errorf("loading city config: %w", err)
	}
	// Normalize relative rig paths to absolute so RigDirForBead returns a usable
	// command dir, matching cmd_hook's resolveRigPaths call after loadCityConfig.
	resolveRigPaths(cityPath, cfg.Rigs)

	agentName := strings.TrimSpace(os.Getenv("GC_ALIAS"))
	if agentName == "" {
		agentName = strings.TrimSpace(os.Getenv("GC_AGENT"))
	}
	if agentName == "" {
		return "", nil, false, fmt.Errorf("agent identity is unavailable")
	}
	a, ok := resolveAgentIdentity(cfg, agentName, currentRigContext(cfg))
	if !ok {
		return "", nil, false, fmt.Errorf("agent identity %q is not configured", agentName)
	}
	rigDir, ok := crossStoreClaimDir(cfg, &a, beadID)
	if !ok {
		return "", nil, false, nil
	}
	overrides, err := bdRuntimeEnvForRigWithError(cityPath, cfg, rigDir)
	if err != nil {
		return "", nil, false, fmt.Errorf("resolving bead store environment: %w", err)
	}
	return rigDir, mergeRuntimeEnv(os.Environ(), overrides), true, nil
}
