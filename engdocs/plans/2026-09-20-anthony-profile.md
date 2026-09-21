# Anthony provider lanes implementation plan

> For agentic workers: use superpowers:executing-plans inline. This is one
> tightly coupled, standalone utility; no Gas City seat, bead, or mail is created.

**Goal:** Implement the approved Addendum A operator contract without modifying
Gas City or automatically moving running sessions.

**Architecture:** A city-local Python executable validates provider-only lane
variants in a temporary replacement view, atomically updates one ignored include,
preserves previous bytes, and requests a soft reload. Status reads session beads,
herdr process observations, and selected environment fields without trusting
accepted fingerprints as evidence of adoption.

**Tech stack:** Python 3.11 standard library, deployed `gc`, macOS `ps`, herdr.

**Spec:** `engdocs/design/2026-09-20-anthony-manual-switcher-review.md`, Addendum A.

## Global constraints

- Only lane provider definitions may vary. Concrete provider exceptions survive.
- Active file: `.gc/provider-lanes.toml`; previous: `.gc/provider-lanes.previous.toml`.
- No daemon, lock, journal, automatic rotation, reset, drain, or runtime rollback.
- One operator; sequential invocations; no concurrent config edits during apply.
- `effort = ""` is mandatory on Codex lanes. Explicit model at the lane hop.
- Agent-level model/effort keys must be absent, including empty values.
- Never use stacked `-f` as candidate validation. Never use JSON reload output
  on this deployment because it drops the acceptance warnings.
- Model policy is operator data, not a mapping algorithm in the program.
- Model/effort counterparts are approved in `contrib/anthony-profile/examples/`.
- Live installation awaits reviewed static membership and isolated launch acceptance.

## Files and interfaces

Create `contrib/anthony-profile/anthony_profile.py`: executable CLI and bounded
configuration transaction. `parse_variant(data: bytes) -> dict` rejects anything
outside lane definitions. `replacement_view(city: Path, data: bytes)` is a context
manager yielding a scratch city with a private `.gc` and copied `site.toml` input.
`Switcher(city: Path, gc: str = "gc")` exposes
`preview(data: bytes) -> dict`, `apply(data: bytes) -> dict`, `undo() -> dict`, and
`status() -> dict`. CLI returns 0 for validated preview/confirmed reload, 1 for
pre-activation rejection, 2 for ambiguous post-activation results or incomplete
status observations. Human output is default; status also offers JSON.

Create `contrib/anthony-profile/test_anthony_profile.py`: filesystem and CLI tests
using temporary cities and an injected command runner, never production sessions.
Create `contrib/anthony-profile/README.md`: commands, one-time migration,
single-operator contract, manual safe cutover, failure/undo semantics and evidence.
Do not modify Go files, active city configuration, account homes, or pool counts
as part of the helper's implementation tests.

## Implementation sequence

### 1. Faithful preflight and scoped variants

Write filesystem tests first. A real temporary city contains a fixed include,
an active lane based on `provider:claude`, a relative local pack, and a `site.toml`
input under `.gc`. Replacement view must retain relative reads while substituting
only active bytes and must disappear after exceptions. Reject symlinked active
or previous files, missing fixed include, non-lane tables, incomplete assignments,
and managed agents with pinned options or custom launch/resume commands.

```python
with replacement_view(city, candidate) as scratch:
    self.assertEqual((scratch / ACTIVE).read_bytes(), candidate)
    self.assertEqual((city / ACTIVE).read_bytes(), original)
    self.assertEqual((scratch / "pack/data").read_text(), "unchanged")
self.assertFalse(scratch.exists())
```

Run `python3 -B -m unittest discover -s contrib/anthony-profile -v`; confirm failure
before adding implementation. Implement shadow directories for root and `.gc`,
copy `city.toml`, symlink root siblings, copy `.gc/site.toml`, and write candidate
only in scratch. Never symlink writable runtime trees: deployed config commands
regenerate `.gc/scripts` and prune `.gc/system/packs`. Execute
`gc --city SCRATCH config show --validate --json` and provider explain for each
lane. Check the loaded raw provider fields equal the candidate fields, catching
duplicate definitions or precedence that makes validation unfaithful. Inspect
resolved agent overrides and refuse the migration hazards described above.

### 2. Atomic apply and reversible desired state

Test old bytes survive failed preflight, failed backup, reload failure, and timeout.
Assert identical retries reload again without overwriting previous bytes. Test
undo invokes the same validator and retains its recovery target; it is not a toggle.

```python
switcher.apply(candidate)
self.assertEqual((city / PREVIOUS).read_bytes(), original)
switcher.apply(candidate)
self.assertEqual((city / PREVIOUS).read_bytes(), original)
switcher.undo()
self.assertEqual((city / ACTIVE).read_bytes(), original)
self.assertEqual((city / PREVIOUS).read_bytes(), original)
```

Implement same-directory temporary write, flush/fsync, `os.replace`, directory
fsync. Save previous before activating. Recheck active and root config bytes
after validation; this catches ordinary concurrent edits without pretending to
offer locking. Invoke only `gc reload --soft --timeout 45s`, with bounded process
wait, preserving both output streams. Any post-activation failure reports active
desired bytes as changed and reload/adoption as unconfirmed. Never restore
runtime or automatically replay lifecycle commands.

### 3. Honest observations and operator documentation

Test command observations separately from configuration: matching bead provider
with an old Claude process must not be reported as Codex adoption. Unknown PID,
missing pane, unreadable environment and missing effort proof remain unknown.
Only print allowlisted environment fields, never arbitrary process environment.

Status reads config, session list JSON, herdr pane list/process-info, then `ps`
for the observed foreground PID. Correlate by `GC_CITY` and `GC_SESSION_ID`;
show orphaned/unmatched evidence separately. Report observed model and profile
arguments, account home, bead lifecycle state and desired lane. Do not claim
effective effort from a mutable profile file or launch hash. Templates without
live seats are not automatically failures or automatically converged.

Write migration and cutover instructions alongside the CLI. Record that provider
lanes do not add researcher/recon pools or persist mandates: those are separate
configuration/prompt work. Preserve Python419 and other explicit concrete pins.

## Verification and handoff

After each step run the focused tests. Exercise replacement validation with the
installed binary against isolated cities and a scratch view of Anthony's city;
prove `.gc` includes and relative pack resolution. Capture the stale-agent-model
rejection. Isolated launch/resume canaries must precede live lane deployment,
especially native Codex effort profiles. Record any unrun acceptance checks as
unverified, not passing.

Run focused Python tests, `make check-docs`, the applicable repository test baseline,
`go vet ./...`, and the configured pre-commit hook. Review the exact feature diff,
commit only this worktree's changes and publish only its feature branch. Do not
touch the original dirty worktree or create work-tracking beads for this standalone
operator request.
