# Verification record — 2026-09-20

The helper is implemented. It is not installed into Anthony's live city.
No lane membership, active include, account home, pool count, seat mode, or
production session was changed by this implementation.

## Environment

- Feature branch: `feat/anthony-profile-lanes`.
- Deploy-lineage base: `99f601c7d` (`integration/deploy-20260815-safety`).
- Installed CLI used for integration tests: `~/.local/gc-bin/gc`, embedded
  revision `2c5c83305` (different from this worktree's Go source).
- Python: 3.11.6. Codex CLI: 0.155.1.
- No Gas City Go code or upstream provider API was modified.

## Measured checks

The full helper test invocation passed 44 tests: 35 hermetic tests, eight tests
using the installed `gc` against owned temporary cities, and one using a private
scratch snapshot of Anthony's full configuration. The ordinary invocation skips
the nine opt-in installed-binary checks.

Coverage includes candidate replacement, relative pack imports, ignored `.gc`
include loading, two-hop provider resolution, explicit profile validation,
agent-option rejection, concrete-exception inheritance, atomic writes,
pre/post-rename undo failure, reload failure/timeout diagnostics, retry behavior,
secret-field exclusion, orphan/missing process observations, and known effort
mismatches. No integration test starts a provider session.

`make check-docs`, `go vet ./...`, and the configured `.githooks/pre-commit` passed.
Sandbox-only failures from initial attempts were retried with normal access to
the shared Go cache and process table.

`LOCAL_TEST_JOBS=2 make test-fast-parallel` failed in the unchanged
`internal/testutil/providerledger` package: its
`TestCatalogMatchesProductionWiringAndDocumentation` rejects eight runtime
waivers owned by `ga-80po0c.3` that expired on 2026-08-26. All six CLI shards and
the other baseline jobs passed; the unit-core job contains that failure. No Go
source or dependency file differs from the deploy-lineage base. This utility
does not extend unrelated policy waivers or claim the full repository is green.
The full output is in the local run's `unit-core.log` under
`/var/folders/nz/lvjgdpvx1g9420rrwszk_t840000gn/T/gc-local-tests.rervxN/`.

Read-only process inspection found the current Python419 Furiosa process via
herdr (`w2E:p3M`, PID 3334 at observation time):

```text
CODEX_HOME=/Users/anthonybyrnes/.codex-profiles/py419-polecat
GC_CITY=/Users/anthonybyrnes/code/cities/anthony
GC_TEMPLATE=python419/gastown.polecat
GC_SESSION_ID=az-wisp-svwjw
argv model: gpt-5.6-sol
model_reasoning_effort= argument: absent
```

These are historical observations, not PID files or current-state assertions.
The helper's status command correlated 12 observed launches with 22 open session
beads in that snapshot. Two active beads lacked correlated foreground processes;
status reported uncertainty rather than assuming they stopped or migrated.

## Corrections to the design's assumptions

1. **Native validation does not reject stale cross-provider models.** A temporary
   Codex-lane agent retaining `model = "claude-sonnet-5"` passed the installed
   `gc config show --validate`. The helper rejects agent-level model/effort keys
   itself; this guard is necessary.
2. **Config inspection can write generated runtime assets.** The first review
   found that symlinking `.gc/scripts`, `.gc/system`, and legacy caches would let
   scratch validation write through. The implementation now uses private `.gc`
   directories and copies the `.gc/site.toml` configuration input. A regression
   test verifies scratch-generated assets cannot change the original.
3. **Undo must not destroy its recovery copy.** It is an idempotent restore, not
   a two-file toggle. Named variants provide redo. Tests cover failure before
   replacement and after replacement but before directory durability confirmation.
4. **Warnings are part of the contract.** The installed JSON reload response omits
   acceptance warnings, so apply uses synchronous text output and retains both
   streams, including failures/timeouts. Config warnings nested under
   `validation.warnings` are retained too.
5. **One-time migration is not a routine staged switch.** Root/template edits
   trigger the ordinary watcher; racing a subsequent soft reload cannot guarantee
   no drains. Installation needs a deliberate watcher-controlled maintenance window.
6. **Missing legacy packs may be skipped with a warning.** Unsupported runtime
   composition inputs and missing/skipped input diagnostics now reject preflight;
   successful native validation alone does not prove complete membership.
7. **Provider templates expand into rigs.** One new lane generated 16 on-demand
   templates in Anthony's snapshot: one city template and one in each of 15 rigs.
   This is visible in preview and does not itself launch seats.

## Remaining live-installation gates

| Addendum A acceptance item | Evidence/status |
| --- | --- |
| Existing concrete Codex account environment propagation | Passed on the observed live Python419 seat |
| `.gc` include and faithful replacement loading | Passed with temporary cities, relative local packs, and a private full-city snapshot |
| Stacked provider-overlay defect | Reproduced; helper does not use that path |
| Stale agent defaults | Native rejection expectation disproved; helper guard tested |
| Two-hop lane fresh launch under herdr, Claude and Codex | Not run; config resolution is not launch proof |
| Same lane's resume preserves model and effort | Not run |
| Codex native effort profiles through herdr, including effective effort | Not run; no silent home-default fallback enabled |
| Watcher remains quiet on ignored-file rename | Deployed source inspected; isolated running-city check not run |
| Actual lane membership migration and preserved pins | Not installed; requires reviewed city diff and safe maintenance window |

The operator approved the explicit cross-provider pairings on 2026-09-20:
Opus/low ↔ Sol/high; Sonnet/low ↔ Luna/medium; Haiku/low ↔ Luna/low;
Fable/high ↔ Astra/high. The files in `examples/` now record this approved policy,
with existing concrete-provider exceptions preserved. This closes the model-mapping
decision, not the launch/resume or migration gates. Native profiles must pass
fresh/resume acceptance before use as an operational Codex variant. New
researcher/recon pools and mandate prompts are not created by this utility.

## Independent review

A read-only reviewer identified scratch write-through, failed-undo recovery loss,
bare-base exception inheritance, ignored effort evidence, and dropped diagnostics.
Each has a regression test and correction. A follow-up review checks the corrected
helper separately from the uncompleted live-installation gates. The final review
found no remaining important issues in the corrected paths and assessed the helper
as ready to ship within its documented scope, not ready for live deployment.
