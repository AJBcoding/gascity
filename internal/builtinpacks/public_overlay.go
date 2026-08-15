package builtinpacks

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"time"
)

const gastownWitnessPatrolFormula = "formulas/mol-witness-patrol.toml"

type patchedGastownPackFS struct {
	base fs.FS
}

func patchedGastownFS(base fs.FS) fs.FS {
	return patchedGastownPackFS{base: base}
}

func (p patchedGastownPackFS) Open(name string) (fs.File, error) {
	file, err := p.base.Open(name)
	if err != nil {
		return nil, err
	}
	if path.Clean(name) != gastownWitnessPatrolFormula {
		return file, nil
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	data, err := fs.ReadFile(p.base, name)
	if err != nil {
		return nil, err
	}
	patched, err := patchGastownWitnessPatrol(data)
	if err != nil {
		return nil, err
	}
	return &patchedFile{
		reader: bytes.NewReader(patched),
		info: patchedFileInfo{
			name:    info.Name(),
			size:    int64(len(patched)),
			mode:    info.Mode(),
			modTime: info.ModTime(),
		},
	}, nil
}

func (p patchedGastownPackFS) ReadFile(name string) ([]byte, error) {
	data, err := fs.ReadFile(p.base, name)
	if err != nil {
		return nil, err
	}
	if path.Clean(name) != gastownWitnessPatrolFormula {
		return data, nil
	}
	return patchGastownWitnessPatrol(data)
}

func (p patchedGastownPackFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(p.base, name)
}

type patchedFile struct {
	reader *bytes.Reader
	info   patchedFileInfo
}

func (f *patchedFile) Stat() (fs.FileInfo, error) {
	return f.info, nil
}

func (f *patchedFile) Read(p []byte) (int, error) {
	return f.reader.Read(p)
}

func (f *patchedFile) Close() error {
	return nil
}

type patchedFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
}

func (i patchedFileInfo) Name() string {
	return i.name
}

func (i patchedFileInfo) Size() int64 {
	return i.size
}

func (i patchedFileInfo) Mode() fs.FileMode {
	return i.mode
}

func (i patchedFileInfo) ModTime() time.Time {
	return i.modTime
}

func (i patchedFileInfo) IsDir() bool {
	return false
}

func (i patchedFileInfo) Sys() any {
	return nil
}

func patchGastownWitnessPatrol(data []byte) ([]byte, error) {
	const insertionPoint = "\n\n2. An `absent` lookup (assignee not in the map) is orphaned only for"
	if !bytes.Contains(data, []byte(insertionPoint)) {
		return nil, fmt.Errorf("gastown witness patrol formula patch insertion point not found")
	}
	return bytes.Replace(data, []byte(insertionPoint), []byte(gastownWitnessPatrolClaimingSessionGuard+insertionPoint), 1), nil
}

const gastownWitnessPatrolClaimingSessionGuard = `

   **Live-alias claiming-session cross-check.** Alias recycling can make the
   assignee resolves live check true even when the session that actually
   claimed the bead is dead. Before treating a live assignee as healthy,
   cross-check the bead's own claiming session.

   Build a companion exact-identifier to current-session-id map from the same
   session roster:

       ASSIGNEE_SESSION_ID_MAP=$(jq -n --slurpfile sessions "$SESSIONS_FILE" --slurpfile session_beads "$SESSION_BEADS_FILE" '
         def add($m; $key; $id):
           if (($key // "") | length) == 0 or (($id // "") | length) == 0 then $m
           else $m + {($key): $id}
           end;
         (reduce ($sessions[0].sessions // [])[] as $s ({};
             add(.; $s.id;           $s.id)
           | add(.; $s.name;         $s.id)
           | add(.; $s.session_name; $s.id)
           | add(.; $s.alias;        $s.id)
           | add(.; $s.agent_name;   $s.id)
         )) as $from_sessions
         | reduce ($session_beads[0] // [])[] as $b ($from_sessions;
             add(.; $b.metadata.configured_named_identity; ($b.id // $b.metadata."gc.session_id" // "")))')

       gc_liveness_probe() {
         printf '%s' "$LIVENESS_MAP" | jq -r --arg id "$1" '.[$id] // "absent"'
       }

       gc_identity_session_id() {
         printf '%s' "$ASSIGNEE_SESSION_ID_MAP" | jq -r --arg a "$1" '.[$a] // empty'
       }

   When the assignee resolves live (active or awake), read the claimed
   session identity from the work bead and compare it to the current alias
   holder:

       BEAD_JSON=$(gc bd show "$BEAD_ID" --json)
       CLAIMING_SESSION_ID=$(printf '%s' "$BEAD_JSON" | jq -r '.[0].metadata."gc.session_id" // empty')
       CURRENT_ALIAS_SESSION_ID=$(gc_identity_session_id "$ASSIGNEE")

   If CLAIMING_SESSION_ID is empty, keep the old classification. If it equals
   CURRENT_ALIAS_SESSION_ID, the claiming session IS the current alias-holder;
   keep the bead classified as not orphaned (the gas-76r control shape).

   If CLAIMING_SESSION_ID is non-empty, CURRENT_ALIAS_SESSION_ID is non-empty,
   and they differ, the alias has recycled. Probe the claiming session itself:

       CLAIMING_SESSION_STATE=$(gc_liveness_probe "$CLAIMING_SESSION_ID")

   If the claiming session probes archived, closed, or absent, classify
   the bead as stalled-not-orphaned and run the same recovery path as an
   orphaned pool bead: salvage/push any work, then release or reassign instead
   of silently skipping because the recycled alias is live.

   If the claiming session probes creating, asleep, drained,
   suspended, draining, quarantined, active, or awake, do NOT recover
   it here; that exact session still has a non-terminal owner state.

   If CURRENT_ALIAS_SESSION_ID cannot be resolved, or the claiming session
   probes any unknown state, the result is UNVERIFIABLE. Fail closed: skip that
   bead for this cycle, log the reason, and continue. Do not fire this guard
   when the claiming session is the current alias-holder.
`
