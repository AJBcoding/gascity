package session

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// workDirPerm is the mode used for a session's working directory. It matches
// the mode the resolver used while it still materialized, so moving creation
// here does not change the directory an operator ends up with.
const workDirPerm os.FileMode = 0o755

// materializeWorkDir creates dir, and every missing parent, so a session can be
// started in it.
//
// This is the single owner of work_dir creation on the spawn path. The runtime
// providers never create the directory and the Manager consumes spec.WorkDir
// as-is, so before this existed a session's cwd existed only as a side effect
// of somebody having RESOLVED it earlier in a reconcile tick — which is what
// made an ordinary metadata question mint husk directories (gas-m7h, gas-oax3).
//
// An empty dir is not an error: callers that leave WorkDir unset inherit the
// provider's own default cwd, and there is nothing to create.
func materializeWorkDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, workDirPerm); err != nil {
		return fmt.Errorf("creating session work dir %q: %w", dir, err)
	}
	return nil
}

// materializeWorkDirBestEffort creates dir but never blocks a start on failing
// to, reporting the failure on stderr instead.
//
// The two spawn points want opposite error policies. The create path fails
// CLOSED: an operator just asked for a session, a cwd that cannot be created is
// an immediate legible error, and nothing is half-created when it refuses.
//
// The reconciler's start bridges fail OPEN. They wake sessions the fleet is
// already depending on, and by then the directory has normally existed since
// create time; work_dir there is whatever bead metadata recorded, which may
// name a path this host cannot create at all. Refusing to wake on MkdirAll
// would convert a path problem into "agents mysteriously stopped waking" —
// far harder to diagnose than the error the provider raises on a missing cwd,
// and the same silent-stall shape the disk pre-flight deliberately avoids by
// failing open. So note it and let Start produce the real failure.
func materializeWorkDirBestEffort(dir string, stderr io.Writer) {
	if err := materializeWorkDir(dir); err != nil {
		if stderr == nil {
			stderr = os.Stderr
		}
		fmt.Fprintf(stderr, "session: work dir materialization failed (fail-open): %v\n", err) //nolint:errcheck
	}
}
