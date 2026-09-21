# Verification record — updated 2026-09-21

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

The full helper test invocation passed 48 tests: 38 hermetic tests, nine tests
using the installed `gc` against owned temporary cities, and one using a private
scratch snapshot of Anthony's full configuration. The ordinary invocation skips
the ten opt-in installed-binary checks. The latest full run took 8.783 seconds;
the hermetic invocation took 0.087 seconds. These are single-run observations,
not performance baselines.

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
The September 21 rerun after the Haiku correction reproduced those same eight
expired waivers. Its full output is in `unit-core.log` under
`/var/folders/nz/lvjgdpvx1g9420rrwszk_t840000gn/T/gc-local-tests.j25peZ/`.

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

## Membership-review correction — 2026-09-21

The [membership diff](../../engdocs/design/2026-09-21-anthony-lane-membership-review.md)
compares all existing templates and exposes gaps not exercised by the earlier
worker-only snapshot test. The original four-lane examples did not validate
unchanged: the deployed enum rejects the full Haiku ID in provider defaults
and rejects Astra. The native Haiku alias emits the intended full model ID,
but the helper's literal model comparison incorrectly reported that pair as
pending cutover. Production effort profiles are also absent.

A three-lane diagnostic candidate (Haiku alias, escalation omitted) passed
Claude preview and preserved all 75 existing concrete-provider entries.
Exactly 47 existing templates joined lanes; 94 stayed unchanged. The corresponding
Codex candidate passed native configuration resolution but correctly failed helper
preview on a missing effort profile. No examples, helper code, live configuration,
account homes, or sessions were changed by that review.

The operator subsequently approved that membership and a narrow Haiku spelling
compatibility rule. The helper now translates only Claude's `haiku` choice to
`claude-haiku-4-5-20251001` in desired launch evidence. Native explain JSON omits
the schema, so this is an explicit deployment-specific rule, not runtime schema
discovery. Revalidate it against launch argv after a binary or schema change.

Test-first evidence: the Haiku comparison failed with false `pending-cutover`;
the actual-example integration test failed on both the Haiku full ID and Astra.
The corrected three-lane examples now both pass native validation and helper
preview in private test cities, using effort-only profiles in a temporary account
home. The tests assert all six model/effort assignments, selected Codex profiles
and home, unchanged active bytes, and no undo-file creation. No provider process
was launched for these tests. Production profiles and live installation remain
pending; this closes the example-validation and spelling gaps, not deployment.
The corrected Claude example also passed preview against the approved private
47-template migration snapshot, reporting boot's intended full Haiku model ID.

## Remaining live-installation gates

| Addendum A acceptance item | Evidence/status |
| --- | --- |
| Existing concrete Codex account environment propagation | Passed on the observed live Python419 seat |
| `.gc` include and faithful replacement loading | Passed with temporary cities, relative local packs, and a private full-city snapshot |
| Stacked provider-overlay defect | Reproduced; helper does not use that path |
| Stale agent defaults | Native rejection expectation disproved; helper guard tested |
| Two-hop lane fresh launch under herdr, Claude and Codex | Passed in isolated city: Opus/low and Sol/high; Luna/medium also passed fresh launch |
| Same lane's resume preserves model and effort | Passed for Opus/low and Sol/high, with new PIDs and unchanged conversation IDs |
| Codex native effort profiles through herdr, including effective effort | Passed: UI and turn-context records confirm high effort over a low-effort home default; no `=` effort argument |
| Watcher remains quiet on ignored-file rename | Passed in isolated running city; watched-root edit served as a positive control |
| Actual lane membership migration and preserved pins | 47-template diff approved; snapshot preserves all pins; not installed |
| Actual shipped lane variants | Both three-lane examples validate and preview in isolated test cities; escalation deferred |
| Production effort profiles and controlled installation | Not provisioned or installed; requires deliberate maintenance window |

The operator approved the explicit cross-provider pairings on 2026-09-20:
Opus/low ↔ Sol/high; Sonnet/low ↔ Luna/medium; Haiku/low ↔ Luna/low;
Fable/high ↔ Astra/high. The files in `examples/` now record this approved policy,
with existing concrete-provider exceptions preserved. This closes the model-mapping
decision; representative launch/resume acceptance is now complete, while live
installation remains pending. Membership is approved. This is not an availability test of
every approved model or every production account/rig configuration. New
researcher/recon pools and mandate prompts are not created by this utility.

## Isolated runtime acceptance — 2026-09-21

The owned test city used a private `GC_HOME`, file-backed session ledger, loopback
API port 18372, and herdr session `lane-canary-nil7FU`. Codex used a private
temporary home with a copy of the existing account credential and independently
created effort profiles. No production configuration, account profile, or session
was edited or cycled. The binary remained the September 12 build with embedded
commit `2c5c83305`; herdr was 0.7.5, Codex 0.155.1, and Claude Code 2.1.278.

| Probe | Actual process evidence | Effective-setting evidence |
| --- | --- | --- |
| Codex fresh, two provider hops | `--profile gc-high --model gpt-5.6-sol`; expected private `CODEX_HOME`; no `-c` effort argument | UI and JSONL turn context report Sol/high, overriding the test home's Luna/low defaults |
| Codex resume | PID 35029 → 50097; `codex resume 01a0c5ba-c9a0-7cb3-9751-c9a03ee10717 --profile gc-high ... --model gpt-5.6-sol` | Same conversation ID on session bead and herdr; resumed UI and turn context still Sol/high; `CANARY_RESUME_OK` response |
| Claude fresh and resume | PID 98673 → 35034; fresh `--session-id` becomes `--resume` with the same `cf78d92c-f386-44da-8033-e78b46197eed` key; `--model claude-opus-5 --effort low` survives | `/model` selector reports Opus 5 and Low effort both times; selector dismissed without changing defaults |
| New seat after soft apply | PID 97238; `--profile gc-medium --model gpt-5.6-luna` | UI reports Luna/medium and replies `CANARY_OK` |
| Same lane switched from Codex to Claude | Existing Codex PIDs 50097 and 97238 survive; new PID 22935 runs `claude --model claude-sonnet-5 --effort low` | New seat's `/model` selector reports Sonnet 5 / Low; old Codex seats correctly remain `pending-cutover` |

These PIDs and IDs are historical test evidence, not stored runtime status.
The first stripped-down Codex fixture lacked session-start integration and could
not prove resume: herdr identity reporting alone did not populate the session
bead's `session_key`. Repeating with the ordinary `gc prime` SessionStart hook
persisted the key and enabled the successful resume above. No resume success is
claimed for the earlier fresh restarts. Suspend acknowledgements also preceded
actual stop; acceptance waited for the old PID and pane to disappear.

At 20:54:22 UTC, an atomic rename changed only `.gc/provider-lanes.toml` from
Sol/high to Luna/medium. At 20:54:53 UTC the watcher had not reloaded, both session
beads remained awake, and the original Sol/high PID was unchanged. The explicit
soft-reload operation subsequently accepted the change. A comment-only edit to
the watched root then produced a `Config reloaded` line, confirming an active
watcher rather than a disabled test environment.

The helper's full apply/status/undo cycle was exercised against that running
city. Applying Luna/medium reported acceptance on one session but left PID 50097
on Sol/high; status called it `pending-cutover`. A new seat launched as
Luna/medium. Undo restored desired Sol/high and accepted drift on two sessions,
but retained both PIDs; the newer Luna seat became `pending-cutover`. The tested
contract is desired-state reversal, not runtime rollback.

A second apply changed that same lane's base from the Codex sibling to the Claude
sibling. Both old Codex processes survived; a new session of the same template
launched Claude Sonnet/low. Undo changed desired state back to Codex and retained
all four processes, including the new Claude seat. Status again reported the
cross-provider mismatches rather than trusting the bead's unchanged lane name.

This run exposed a real helper defect: Codex appends `tui.model_availability_nux`
and `hooks.state.*.trusted_hash` bookkeeping to a selected profile. The original
effort-only validator rejected the profile after use, blocking even status and
undo. A regression failed first; the fix admits only those observed schemas,
with tests rejecting executable hooks, model/account overrides, malformed values,
and unknown fields. Actual canary status and undo then passed with the rewritten
profile. The helper still never writes profile files itself.

Cleanup stopped the isolated supervisor and all canary PIDs, deleted only the
stopped `lane-canary-nil7FU` herdr session, and removed the temporary credential
copy. The remaining private test configuration and logs are at
`/private/tmp/anthony-lane-canary.nil7FU`; they are disposable acceptance artifacts,
not part of the switcher's operation. No service was installed with launchd.

## Independent review

A read-only reviewer identified scratch write-through, failed-undo recovery loss,
bare-base exception inheritance, ignored effort evidence, and dropped diagnostics.
Each has a regression test and correction. A follow-up review checks the corrected
helper separately from the uncompleted live-installation gates. The final review
found no remaining important issues in the corrected paths and assessed the helper
as ready to ship within its documented scope, not ready for live deployment.
The September 21 profile-bookkeeping fix received a separate read-only review:
no important findings, with the strict policy boundary retained. A separate
read-only review of the Haiku rule, actual-variant tests, three-lane examples,
and approval record found no critical or important issues; the reviewer reran
both focused hermetic tests successfully. Runtime canaries pass as recorded
above and membership is approved; provisioning and controlled installation
remain separate work.
