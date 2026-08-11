package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// poolWorkDirCity builds a city whose rig-scoped agent carries the pack-shaped
// templated work_dir (packs/gastown/agents/polecat/agent.toml), so resolving the
// agent's *template* identity names a per-agent worktree path that only a
// concrete session is ever supposed to materialize.
func poolWorkDirCity(t *testing.T) (cityDir string, cfg *config.City) {
	t.Helper()
	cityDir = t.TempDir()
	rigDir := filepath.Join(cityDir, "myrig")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(rig): %v", err)
	}
	echo := "builtin:claude"
	cfg = &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:     "polecat",
			Provider: "claude",
			Scope:    "rig",
			Dir:      "myrig",
			WorkDir:  ".gc/worktrees/{{.Rig}}/polecats/{{.AgentBase}}",
		}},
		Providers: map[string]config.ProviderSpec{
			"claude": {Base: &echo, Command: "/bin/echo"},
		},
		Rigs: []config.Rig{{Name: "myrig", Path: rigDir}},
	}
	return cityDir, cfg
}

// TestPoolTriggerWorkDirDoesNotMaterializeWorkDir guards gas-m7h. Pool trigger
// binding asks a purely metadata question — "what work_dir value should this
// session bead carry?" — yet it used to answer it through a resolver whose
// MkdirAll materialized the path as a side effect. Under the template identity
// that path is the *generic* agent dir, so every reconcile tick left a husk:
// a plain directory that is not a linked worktree and has no .git, which means
// `git rev-parse --show-toplevel` inside it walks up and resolves to the
// operator's CITY checkout. Any later salvage or git-write path that trusts the
// bound metadata.work_dir would then operate on the operator's own repo.
//
// Resolution must be side-effect free; materialization belongs to the
// session-START path for a concrete session.
func TestPoolTriggerWorkDirDoesNotMaterializeWorkDir(t *testing.T) {
	cityDir, cfg := poolWorkDirCity(t)
	bp := newAgentBuildParams("test-city", cityDir, cfg, runtime.NewFake(), time.Now().UTC(), nil, io.Discard)
	cfgAgent := &cfg.Agents[0]

	got := poolTriggerWorkDir(bp, cfgAgent, "myrig/polecat", SessionRequest{WorkBeadID: "gas-1"})

	want := filepath.Join(cityDir, ".gc", "worktrees", "myrig", "polecats", "polecat")
	if got != want {
		t.Fatalf("poolTriggerWorkDir = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("pool trigger binding materialized the agent work_dir %q (stat err=%v); "+
			"resolving a work_dir for metadata must not create it — a husk dir here "+
			"git-walks up to the operator's city repo (gas-m7h)", got, err)
	}
}

// TestPrepareTemplateResolutionLeavesNoEmptyWorkDir covers the other half of
// gas-m7h: the reconcile-tick pre-staging step. It resolved the work dir up
// front and only then decided whether it had any hook or overlay file to write,
// so an agent with neither still got a directory minted on every tick — which is
// exactly the shape the fleet audit found, husks that were empty or held only
// .gc/tmp residue. Its writers (hooks.Install, the overlay stager) create the
// directories they need, so the pre-staging step must leave the filesystem
// untouched when it has nothing to stage.
func TestPrepareTemplateResolutionLeavesNoEmptyWorkDir(t *testing.T) {
	cityDir, cfg := poolWorkDirCity(t)
	bp := newAgentBuildParams("test-city", cityDir, cfg, runtime.NewFake(), time.Now().UTC(), nil, io.Discard)
	cfgAgent := &cfg.Agents[0]

	// Without this the function returns before ever resolving a work dir, and
	// the assertion below would hold vacuously.
	if _, err := config.ResolveProvider(cfgAgent, bp.workspace, bp.providers, bp.lookPath); err != nil {
		t.Fatalf("ResolveProvider must succeed or this test proves nothing: %v", err)
	}

	prepareTemplateResolution(bp, cfgAgent, "myrig/polecat", io.Discard)

	workDir := filepath.Join(cityDir, ".gc", "worktrees", "myrig", "polecats", "polecat")
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("pre-staging minted an empty work_dir %q (stat err=%v) with nothing to stage; "+
			"it must not create a directory it does not write into (gas-m7h)", workDir, err)
	}
}

// TestResolveTemplateDoesNotMaterializeWorkDir closes the last tick-frequency
// husk vector (gas-oax3). resolveTemplate runs on every reconcile tick for
// every DESIRED session — including ones that are never created, because a
// create budget capped them or the agent is draining — so materializing during
// resolution minted a directory per tick for sessions that never ran.
//
// Creation moved to the session-create path (internal/session: createStarted
// and the reconciler's ensureRunning bridges), which is reached only when a
// session is actually about to run in the directory.
func TestResolveTemplateDoesNotMaterializeWorkDir(t *testing.T) {
	cityDir, cfg := poolWorkDirCity(t)
	bp := newAgentBuildParams("test-city", cityDir, cfg, runtime.NewFake(), time.Now().UTC(), nil, io.Discard)
	cfgAgent := &cfg.Agents[0]

	// Without this the assertion below could hold vacuously: a resolveTemplate
	// that failed early would never reach the work_dir step at all.
	params, err := resolveTemplate(bp, cfgAgent, "myrig/polecat", nil)
	if err != nil {
		t.Fatalf("resolveTemplate must succeed or this test proves nothing: %v", err)
	}

	want := filepath.Join(cityDir, ".gc", "worktrees", "myrig", "polecats", "polecat")
	if params.WorkDir != want {
		t.Fatalf("resolveTemplate WorkDir = %q, want %q; the value must still be resolved, only its materialization moved", params.WorkDir, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("resolveTemplate materialized the agent work_dir %q (stat err=%v); "+
			"it runs every tick for every desired session, so creating here mints a husk "+
			"for sessions that are never started (gas-oax3)", want, err)
	}
}
