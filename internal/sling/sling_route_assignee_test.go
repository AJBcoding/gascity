package sling

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// TestResolveRouteAssignee_NamedSessionTarget is the regression test for
// gas-7e2h: gc sling set gc.routed_to on a bead routed to a named session
// (e.g. the refinery) but never touched assignee. Consumers like
// mol-refinery-patrol's find-work step filter `--assignee=$GC_AGENT`, so a
// bead routed the documented way sat open and routed but was invisible to
// the very agent it was routed to -- no error, no escalation, just silence.
// A named session has one stable identity and no separate claim step, so
// routing itself must stamp assignee.
func TestResolveRouteAssignee_NamedSessionTarget(t *testing.T) {
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "alpha", Path: "/alpha"}},
		Agents: []config.Agent{{
			Name:              "refinery",
			Dir:               "alpha",
			MaxActiveSessions: intPtr(1),
		}},
		NamedSessions: []config.NamedSession{{
			Template: "refinery",
			Dir:      "alpha",
			Mode:     "always",
		}},
	}
	a := cfg.Agents[0]
	deps := SlingDeps{Cfg: cfg, CityName: "test-city"}

	assignee, warning := resolveRouteAssignee(a, deps)
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if assignee != "alpha/refinery" {
		t.Errorf("assignee = %q, want alpha/refinery", assignee)
	}
}

// TestResolveRouteAssignee_CityScopedNamedSession covers a named session with
// no Dir (city scope, e.g. the mayor): the resolved identity must stay
// unqualified, matching the GC_AGENT value that session actually runs with.
func TestResolveRouteAssignee_CityScopedNamedSession(t *testing.T) {
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              "mayor",
			MaxActiveSessions: intPtr(1),
		}},
		NamedSessions: []config.NamedSession{{
			Template: "mayor",
			Mode:     "always",
		}},
	}
	a := cfg.Agents[0]
	deps := SlingDeps{Cfg: cfg, CityName: "test-city"}

	assignee, warning := resolveRouteAssignee(a, deps)
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if assignee != "mayor" {
		t.Errorf("assignee = %q, want mayor", assignee)
	}
}

// TestResolveRouteAssignee_PoolTarget guards the fix from over-reaching: a
// pool target (many concurrent claimers, no [[named_session]] entry) must
// keep the existing claim-on-pickup contract untouched. Stamping assignee at
// route time would make the bead look claimed before any pool member ever
// picked it up.
func TestResolveRouteAssignee_PoolTarget(t *testing.T) {
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "alpha", Path: "/alpha"}},
		Agents: []config.Agent{{
			Name:              "polecat",
			Dir:               "alpha",
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(3),
		}},
	}
	a := cfg.Agents[0]
	deps := SlingDeps{Cfg: cfg, CityName: "test-city"}

	assignee, warning := resolveRouteAssignee(a, deps)
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if assignee != "" {
		t.Errorf("assignee = %q, want empty for a pool target", assignee)
	}
}

// TestResolveRouteAssignee_NoConfig guards the nil-Cfg / zero-value path used
// by callers that never populate SlingDeps.Cfg (e.g. some unit tests):
// resolution must degrade to "no assignee" rather than panic.
func TestResolveRouteAssignee_NoConfig(t *testing.T) {
	a := config.Agent{Name: "polecat", Dir: "alpha", MaxActiveSessions: intPtr(3)}
	deps := SlingDeps{}

	assignee, warning := resolveRouteAssignee(a, deps)
	if warning != "" {
		t.Fatalf("warning = %q, want none", warning)
	}
	if assignee != "" {
		t.Errorf("assignee = %q, want empty with no config", assignee)
	}
}
