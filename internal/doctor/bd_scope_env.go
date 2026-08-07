package doctor

import (
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// bdScopeVarKeys are the environment variables that select which beads store a
// command resolves. They are projected — not inherited — for every bd
// subprocess doctor spawns against a specific scope.
var bdScopeVarKeys = []string{
	// BEADS_DIR overrides bd's working-directory scope resolution outright,
	// so an inherited value silently redirects the command at another store.
	"BEADS_DIR",
	// GC_BEADS_SCOPE_ROOT is gc's own scope hint. A stale value must not
	// re-derive a scope the caller did not ask for.
	"GC_BEADS_SCOPE_ROOT",
}

// bdScopeEnv returns the environment for a bd subprocess that must resolve its
// store from dir and nothing else.
//
// Setting only cmd.Dir is not enough. BEADS_DIR takes precedence over the
// working directory, and every agent session exports one, so a doctor run from
// inside a session would read every scope it checks against whichever store
// that session happened to point at rather than the scope under test — four
// phantom "missing custom type(s)" failures per run on a real city, which also
// masked the one genuine finding.
//
// Only the scope variables are projected. The backend connection variables
// (BEADS_DOLT_SERVER_*, credentials) are deliberately preserved: they say how
// to reach the store, not which store it is, and stripping them would break
// shared-server cities.
//
// extra entries are appended last so they take precedence over the projection.
func bdScopeEnv(dir string, extra ...string) []string {
	ambient := beads.ProcessEnvSnapshotExcludingNativeDoltOpen()
	out := make([]string, 0, len(ambient)+len(extra)+1)
	for _, entry := range ambient {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || isBDScopeVar(key) {
			continue
		}
		out = append(out, entry)
	}
	out = append(out, "BEADS_DIR="+filepath.Join(dir, ".beads"))
	return append(out, extra...)
}

// isBDScopeVar reports whether key selects which beads store a command
// resolves, and so must be projected rather than inherited.
func isBDScopeVar(key string) bool {
	for _, scoped := range bdScopeVarKeys {
		if key == scoped {
			return true
		}
	}
	return false
}
