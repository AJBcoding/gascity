package doctor

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// envValues returns every value assigned to key in an environment slice, in
// order. bd sees only the last one, but a correct projection should not emit
// duplicates at all.
func envValues(env []string, key string) []string {
	var out []string
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k == key {
			out = append(out, v)
		}
	}
	return out
}

func TestBDScopeEnvPinsBeadsDirToScope(t *testing.T) {
	t.Setenv("BEADS_DIR", filepath.Join("/somewhere", "else", ".beads"))

	dir := t.TempDir()
	got := envValues(bdScopeEnv(dir), "BEADS_DIR")

	want := filepath.Join(dir, ".beads")
	if !slices.Equal(got, []string{want}) {
		t.Fatalf("BEADS_DIR = %v, want exactly [%q] — the ambient value must be replaced, not shadowed", got, want)
	}
}

func TestBDScopeEnvDropsAmbientScopeRoot(t *testing.T) {
	t.Setenv("GC_BEADS_SCOPE_ROOT", "/somewhere/else")

	if got := envValues(bdScopeEnv(t.TempDir()), "GC_BEADS_SCOPE_ROOT"); len(got) != 0 {
		t.Fatalf("GC_BEADS_SCOPE_ROOT = %v, want it dropped so a stale hint cannot re-derive a scope", got)
	}
}

func TestBDScopeEnvPreservesUnrelatedVars(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "3306")

	env := bdScopeEnv(t.TempDir())

	// The backend connection is not the scope. Stripping it would break
	// shared-server cities, so only the scope vars are projected.
	if got := envValues(env, "BEADS_DOLT_SERVER_PORT"); !slices.Equal(got, []string{"3306"}) {
		t.Fatalf("BEADS_DOLT_SERVER_PORT = %v, want [\"3306\"] preserved", got)
	}
	if got := envValues(env, "PATH"); !slices.Equal(got, []string{"/usr/bin:/bin"}) {
		t.Fatalf("PATH = %v, want [\"/usr/bin:/bin\"] preserved", got)
	}
}

func TestBDScopeEnvExtraWinsOverProjection(t *testing.T) {
	env := bdScopeEnv(t.TempDir(), "GIT_TERMINAL_PROMPT=0")

	if got := envValues(env, "GIT_TERMINAL_PROMPT"); !slices.Equal(got, []string{"0"}) {
		t.Fatalf("GIT_TERMINAL_PROMPT = %v, want [\"0\"]", got)
	}
	if last := env[len(env)-1]; last != "GIT_TERMINAL_PROMPT=0" {
		t.Fatalf("last entry = %q, want the extra entry last so it wins", last)
	}
}
