# Anthony manual switcher: handoff

As of 2026-09-23, the city-local helper is implemented, reviewed, and pushed.
It is not installed in Anthony's live city. The remaining work is deployment
preparation and an explicitly authorized, controlled installation.

## Start here

Continue as a standalone assistant: no Gas City seat identity, bead, or mail.
The operator wants deliberate choices, verification, and undo, with the fewest
moving parts. Do not reopen approved design choices without new evidence.
This handoff does not authorize account-home writes, live config edits, reloads,
session actions, merging, or deployment.

| Item | Location or state |
| --- | --- |
| Owned worktree | `/Users/anthonybyrnes/code/gascity/.claude/worktrees/anthony-profile` |
| Feature branch | `feat/anthony-profile-lanes`, tracking `ajb/feat/anthony-profile-lanes` |
| Implementation tip | `c3663e921` — Haiku compatibility and actual-variant validation |
| Deploy-lineage base | `99f601c7d`, `integration/deploy-20260815-safety` |
| Shared repository | `/Users/anthonybyrnes/code/gascity`; preserve unrelated work |
| Live city | `/Users/anthonybyrnes/code/cities/anthony`; preserve on-disk edits, not just Git HEAD |
| Installed binary used in acceptance | `/Users/anthonybyrnes/.local/gc-bin/gc`, embedded revision `2c5c83305` |

The implementation tip was confirmed on the remote on September 23; the
worktree was clean before adding this handoff. The binary revision above is
historical acceptance evidence, not a fresh binary identity check.

Read in this order:

1. [Helper contract and runbook](../../contrib/anthony-profile/README.md).
2. [Approved membership and exact migration diff](2026-09-21-anthony-lane-membership-review.md).
3. [Verification record and corrected assumptions](../../contrib/anthony-profile/VERIFICATION.md).
4. [Design review, especially Addendum A](2026-09-20-anthony-manual-switcher-review.md).
   Its original proposals and “untested” labels are historical; the subsequent
   approvals and verification record supersede them.

The original constraint set is
`/Users/anthonybyrnes/code/cities/anthony/docs/research/2026-09-20-multi-axis-switcher-research.md`.
Supporting records are `docs/research/2026-08-24-provider-switching-research.md`
and `docs/runbooks/claude-accounts-and-quota.md` under that city. The original
`/Users/anthonybyrnes/Downloads/profile-switcher-recipe.md` is a straw man, not
the implementation specification.

## Approved decisions

- Explicit staged cutover: change desired settings; show remaining old launches;
  move seats manually at safe stopping points. A mixed fleet is acceptable.
- Preserve every explicit concrete-provider exception, including Claude pins.
- Switch a few provider-lane definitions, not per-agent patch variants. Static
  membership stays in city-owned configuration.
- Keep the standalone `anthony-profile` name. No upstream account system,
  daemon, automatic rotation, transition journal, or concurrent-writer support.
- First deliverable is fleet lane switching. Per-rig pool providers, account
  changes, seat modes, and counts remain existing config/runbook operations.
  Claude accounts remain host-global; no per-rig Claude account feature.

| Lane | Claude | Codex | Approved existing membership |
| --- | --- | --- | --- |
| `lane_coordination` | Opus 5 / low | Sol / high | 32: mayor, deacon, Gastown dog, 15 witnesses, 13 unpinned refineries, Python419 project-lead |
| `lane_worker` | Sonnet 5 / low | Luna / medium | 14: 12 unpinned polecat pools, shared `anthony` and `frank` templates |
| `lane_recon` | Haiku 4.5 / low | Luna / low | 1: `gastown.boot` |
| Escalation, deferred | Fable 5 / high | Astra / high | None; do not declare this provider yet |

These are operator-chosen counterparts, not capability equivalences. The
shipped [Claude](../../contrib/anthony-profile/examples/claude.toml) and
[Codex](../../contrib/anthony-profile/examples/codex.toml) variants contain only
the three active lanes. The tested binary rejects Astra even in an unused
provider definition. Do not silently substitute another model.

Membership approval covers 47 existing templates, not 47 running seats. The
reviewed snapshot retained all 75 concrete-provider entries and left 94 of 141
existing templates unchanged. Three lanes additionally synthesized 48 on-demand
provider templates, giving 95 managed entries and 94 unmanaged entries. Those
counts are a review baseline, not an invariant to impose on a changed city.

`kit/jeeves` remains pinned to `codex_mac`; `bd.dog` is excluded. Bare `anthony`
and `frank` have no per-rig patch keys. No researcher/recon pool, maintenance
agent, mandate prompt, seat-mode change, or pool resize was authorized here.

## What is built

[anthony_profile.py](../../contrib/anthony-profile/anthony_profile.py) is a
Python 3.11 helper with `preview`, `apply`, `status`, and `undo`.

- Named variants: `CITY/config/lanes/*.toml`.
- Fixed active include: `CITY/.gc/provider-lanes.toml`.
- Recovery bytes: `CITY/.gc/provider-lanes.previous.toml`.
- Apply validates a private replacement view, saves recovery bytes, atomically
  replaces the active file, then requests `gc reload --soft --timeout 45s`.
- Status correlates session beads with herdr/process evidence. A matching launch
  is not proof of current effective model/effort or account identity.
- Undo restores desired bytes and requests reload; it does not rewind sessions,
  conversations, assignments, or credentials. It retains its recovery target so
  retry is safe. Reapply a named variant for redo.

The helper does not install itself, provision profiles, or issue lifecycle
commands. Apply requires an already-installed include and active file.

## Load-bearing constraints

1. Agent `option_defaults` merge additively and override lane defaults. Remove
   managed model/effort keys at their city-owned source; blank values are not
   removal. Native validation accepts some stale cross-provider agent defaults,
   so the helper's additional guard is necessary.
2. Codex lanes keep `effort = ""` to suppress the `=`-bearing effort argument
   that breaks this deployment's herdr launch detection. The lane owns both
   model and `--profile gc-low|gc-medium|gc-high`; the concrete sibling owns
   `CODEX_HOME`. Native effort profiles separate effort from the account home.
3. The operator explicitly approved one Claude spelling rule:
   `haiku` becomes `claude-haiku-4-5-20251001` in desired launch evidence.
   TOML retains `haiku`. There is no broader alias/tier map. Native explain JSON
   omits the choice-to-argv schema; recheck this rule after binary/schema changes.
4. Stacked `-f` provider overlays lose `base` and `option_defaults` in the tested
   binary. Use the helper's replacement view. Its `.gc` must remain private:
   native config inspection can regenerate/prune runtime assets, so symlinking
   the live runtime directory can cause write-through.
5. The `.gc` active path is watcher-ignored. Initial root/template edits are not.
   A root edit followed quickly by soft reload does not guarantee staged cutover;
   installation needs a deliberate watcher-controlled maintenance procedure.
6. An unmatched static patch is a whole-city validation failure. Validate the
   actual replacement and complete membership; do not hand-wave skipped inputs.
7. Lifecycle acknowledgements can precede completion or misreport it. Use session
   beads for lifecycle/assignment evidence and herdr/processes for runtime truth.
   Do not use `gc session reset` as migration shorthand: it clears assignment.
8. Codex resume needs its conversation key persisted on the session bead. Retain
   ordinary `gc prime --hook --hook-format codex` SessionStart integration as well
   as herdr identity reporting; the latter alone did not prove resume.

The helper tolerates only the observed Codex-written UI counters and hook-trust
hash bookkeeping alongside effort in profile files. Model/account overrides,
executable hooks, and unknown policy fields remain rejected. It never writes
these profiles or prints raw process environments/credentials.

## Verification and current installation state

September 21 recorded evidence:

- All 48 helper tests passed, including the actual three-lane example files,
  installed-binary temporary cities, and a private full-city snapshot.
- `CODEX_HOME` propagation was observed in a real Python419 Codex process,
  not inferred from config explain output.
- Isolated herdr canaries proved representative fresh/resume model and effort,
  mixed-fleet apply/undo, and watcher behavior. They did not test every model or
  production account/rig combination. Canary processes and credential copies
  were cleaned up; historical PIDs are not current-state evidence.
- Docs checks, `go vet ./...`, and the pre-commit hook passed. Independent review
  found no remaining critical or important issues in the corrected helper.
- The fast repository suite failed only in
  `internal/testutil/providerledger.TestCatalogMatchesProductionWiringAndDocumentation`:
  eight waivers owned by `ga-80po0c.3` expired on August 26. All six CLI shards and
  other jobs passed. No Go/dependency changes were made; do not extend unrelated
  policy waivers or describe the repository baseline as green.

September 23 handoff checks: 38 hermetic tests passed; 10 opt-in tests were
skipped. Docs checks passed. Remote implementation tip and missing deployment
files were checked read-only. The active/previous files and both named variants
remain absent, as do
`/Users/anthonybyrnes/.codex/gc-low.config.toml`, `gc-medium.config.toml`, and
`gc-high.config.toml`. No lane/include markers were found in the checked root,
site, or `anthony`/`frank` agent files. No live changes were made.

From the owned worktree, reproduce helper verification with:

```sh
python3 -B -m unittest discover -s contrib/anthony-profile -v
ANTHONY_PROFILE_TEST_GC=/Users/anthonybyrnes/.local/gc-bin/gc \
  ANTHONY_PROFILE_TEST_CITY=/Users/anthonybyrnes/code/cities/anthony \
  python3 -B -m unittest discover -s contrib/anthony-profile -v
```

The second command validates owned temporary cities and a private snapshot;
it does not activate live configuration or launch provider sessions. Native
validation may maintain its global pack cache. Do not confuse this test with
a production apply or a new runtime canary.

## Next agent's task and stop points

1. Recheck branch, deployed binary, current city inputs, and installation markers.
   Read the approved membership diff; rebuild a fresh private migration candidate
   from current disk. Preserve unrelated changes. Report changed membership or
   pins to the operator rather than silently expanding approval.
2. Validate both actual variants with private effort profiles first. Production
   provisioning needs explicit approval: three effort-only files under the
   approved account home, without replacing credentials or base configuration.
3. Present the exact installation procedure, initial variant, watcher control,
   before/after checks, and one-time migration rollback to the operator. Obtain
   authorization for the production profile writes and maintenance window.
   The helper's undo is not a rollback for root/template migration.
4. Only after that authorization, provision/install and verify the desired
   configuration, preserved pins, and running population. Then perform manual
   staged seat cutover at operator-approved safe stopping points, checking work
   assignments and actual processes before and after each action.

Start with read-only revalidation and a deployment proposal. No more switcher
features are required by the currently approved scope. Keep escalation, new
pools, automatic migration, account automation, upstream contributions, and
broader integration work deferred unless separately requested.
