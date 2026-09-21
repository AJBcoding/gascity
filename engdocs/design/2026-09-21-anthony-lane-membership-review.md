# Anthony lane membership: proposed diff

Status: **membership approved 2026-09-21; not installed**. Prepared 2026-09-21 from
the current on-disk city at `/Users/anthonybyrnes/code/cities/anthony`, including
uncommitted configuration and convention-loaded agent files, not from Git HEAD.

This is the membership review requested after the
[manual-switcher design](2026-09-20-anthony-manual-switcher-review.md).
The operator approved the 47-template membership, preserved exceptions, and
deferred escalation below. This approval does not authorize live installation.
No live config, account home, session, named-seat mode, pool limit, or running
process was changed.

## Proposed membership

47 existing templates opt in; 94 existing templates remain unchanged.
These are template counts, not running-seat counts.

| Lane | Existing templates joining | Count | Claude target | Codex target |
| --- | --- | ---: | --- | --- |
| `lane_coordination` | Mayor, deacon, Gastown dog; all 15 witnesses; 13 unpinned refineries; Python419 project-lead | 32 | Opus 5 / low | Sol / high |
| `lane_worker` | 12 unpinned polecat pools; shared `anthony` and `frank` templates | 14 | Sonnet 5 / low | Luna / medium |
| `lane_recon` | `gastown.boot` | 1 | Haiku 4.5 / low | Luna / low |
| Escalation, deferred | None; reserve the approved mapping, but do not declare a provider yet | 0 | Fable 5 / high | Astra / high |

No researcher pool, dedicated recon pool, new cron agent, or mandate prompt is
created. Research and maintenance roles can opt into coordination later.
“Dogs” means `gastown.dog` in this proposal; the separate `bd.dog` is explicitly
left unchanged pending a separate choice.

### What changes even if the initial variant is Claude

These are configured defaults, not a claim about any currently running process.

| Existing template(s) | Before | Proposed Claude setting |
| --- | --- | --- |
| `gastown.mayor` | Opus 5 / high | Opus 5 / low |
| `gastown.deacon`, 15 witnesses, 13 unpinned refineries | Sonnet 5 / low | Opus 5 / low |
| `gastown.dog` | Declared Haiku 4.5 / low; see schema caveat below | Opus 5 / low |
| Python419 project-lead | No model pin; inherited Claude effort `max` | Opus 5 / low |
| 12 unpinned polecat pools, `anthony`, `frank` | No model pin; inherited Claude effort `max` | Sonnet 5 / low |
| `gastown.boot` | Declared Haiku 4.5 / low; see schema caveat below | Same intended model/effort, through recon |

The deployed Claude provider does **not** specify a default model. Do not label
the unpinned baseline “Opus/high”: runtime/home defaults require separate process
inspection. This review did not interrupt or inspect individual sessions.

### Exact rig membership

C = coordination; W = worker. A concrete provider name means preserved pin,
not lane membership. “—” means no such template exists in the resolved inventory.
The first three columns are `gastown.witness`, `gastown.refinery`, and
`gastown.polecat`; project-lead is `oversight-rig.project-lead`.

| Rig | Witness | Refinery | Polecat | Project-lead |
| --- | --- | --- | --- | --- |
| kit | C | `codex_mac` | `codex_mac` | `codex_mac` |
| python419 | C | `codex_mac` | `codex_py419` | C |
| revkit | C | C | W | — |
| GunnInternships | C | C | W | — |
| CIPcodes | C | C | W | — |
| n990 | C | C | W | — |
| Indigo | C | C | W | — |
| knowledge_extraction_suite | C | C | W | — |
| tool | C | C | W | — |
| cash | C | C | W | — |
| Aisystem | C | C | W | — |
| CSULBemail | C | C | W | — |
| gascity | C | C | `codex_mac` | — |
| deckkit | C | C | W | — |
| cota-calendar | C | C | W | — |

At city scope: `gastown.mayor`, `gastown.deacon`, `gastown.dog` → C;
`gastown.boot` → recon; bare `anthony`, `frank` → W.

Crew membership is template-wide. `anthony` currently backs 12 root-declared
named seats; `frank` backs two. There are no per-rig `anthony` or `frank`
patch keys. All their named-seat modes remain unchanged.
Jeeves exists only as `kit/jeeves`, already pinned to `codex_mac`, so no
Jeeves template joins a lane.

## Preserved exceptions and exclusions

All 75 existing resolved entries with a concrete provider remain unchanged,
including provider, options, hooks, and other agent fields.

| Deliberate pin | Provider retained | Agent option defaults retained |
| --- | --- | --- |
| `bob` | `claude` | None; suspension retained |
| `code-review` | `claude` | `model = "claude-opus-5"`; inherited effort |
| `mechanic` | `claude` | Opus 5 / high |
| `codex`, `codex2`, `kit/codex`, `kit/jeeves`, `kit/oversight-rig.project-lead` | `codex_mac` | `effort = ""` |
| `kit/gastown.polecat`, `gascity/gastown.polecat` | `codex_mac` | `effort = ""` |
| `python419/gastown.polecat` | `codex_py419` | Sol; `effort = ""`; Codex hook installation retained |
| `kit/gastown.refinery`, `python419/gastown.refinery` | `codex_mac` | `model = ""`, `effort = ""` |

The other 62 concrete-provider entries are existing synthesized provider
templates; they also stay unchanged. The 19 remaining unchanged entries without
a concrete provider are `bd.dog`, `GunnInternships/cota_triage.cota-triage`,
`node-presence`, and 16 city/rig `core.control-dispatcher` templates.

Thus “switch” means the reviewed lane members, not literally every model-backed
template. Claude-pinned specialists, `bd.dog`, and COTA triage can remain Claude
during a Codex selection. Workspace provider remains `claude`; account siblings
remain unchanged. No account identity or effort-home default is inferred here.

## Validation and newly discovered installation gates

Used the installed `gc` (embedded revision `2c5c83305`) against private
before/after roots with private `.gc` directories and copied site bindings.
No reload, lifecycle operation, or live root edit was performed.

- Native validation and helper preview passed for the three-lane Claude
  candidate below. The original four-lane example did **not** pass unchanged.
- Every existing template was compared after normalizing scratch-root paths.
  Exactly 47 changed, only in `Provider` and `OptionDefaults`; all 94 others
  matched. All existing provider definitions and other resolved configuration,
  including named sessions and rig/pool settings, matched.
- Three new provider definitions synthesize 48 on-demand templates: three at
  city scope and three per rig. Inventory becomes 141 → 189, not 48 additional
  running seats. Helper preview therefore reports 95 managed entries
  (47 existing + 48 synthesized), 94 unmanaged.
- Both provider families resolve the same membership. The three-lane Codex
  candidate resolves Sol/high-profile, Luna/medium-profile, and Luna/low-profile
  with `CODEX_HOME=/Users/anthonybyrnes/.codex` and `effort = ""`.
  This is configuration evidence, not fresh launch/effective-effort evidence.
- Native validation retained the same 42 pre-existing warnings; they concern
  fresh/always lifecycle, refinery idle settings, and singleton demand behavior.
  No missing/skipped composition warning was accepted.

Follow-up disposition after approval:

1. **Haiku spelling and status.** The deployed model-choice enum accepts
   `haiku`, whose flag mapping is exactly
   `--model claude-haiku-4-5-20251001`; it rejects the full ID as a provider
   default. The existing agent-level full ID passed config loading because
   that validation path differs. The scratch candidate below uses `haiku`,
   preserving the intended model. A direct probe originally returned false
   `pending-cutover` for this alias/full-ID pair. The operator then approved
   one explicit compatibility rule, now implemented and regression-tested:
   translate Claude's `haiku` choice to this full ID in desired launch evidence.
   Native explain JSON omits the model-choice schema, so this is not dynamic
   schema discovery. Recheck the rule after binary or schema changes.
2. **Escalation deferred, not substituted.** The deployed Codex enum rejects
   `gpt-6-astra`. Even an unused provider definition blocks whole-city
   validation. Escalation is now omitted from both example variants
   until it has a member and a validated launch path. Keep Fable/high ↔
   Astra/high as the approved future assignment; do not silently use Sol instead.
3. **Production effort profiles absent.** None of
   `~/.codex/gc-low.config.toml`, `gc-medium.config.toml`, or
   `gc-high.config.toml` exists. Helper Codex preview correctly refused the
   first missing profile. Provisioning remains a separate approved installation
   action; no home files or credentials were changed here.

The earlier worker-only snapshot test and representative Opus/Sol/Luna canaries
did not validate every model in the original example files. A subsequent
test-first correction validates the actual three-lane examples in private cities
with temporary effort profiles. Live deployment is still gated on provisioning
and controlled installation; see the
[verification record](../../contrib/anthony-profile/VERIFICATION.md).

## Source diff for review, not application

This is the exact scratch delta for the three-lane Claude candidate. It is not
a ready-to-apply production patch: production effort profiles, fresh validation
against current inputs, and a watcher-controlled installation window still
come first. Old historical root comments are retained in this narrow diff;
comments that describe superseded crew/model defaults need correction during
the approved migration.

The otherwise redundant earlier `gascity/gastown.refinery` patch is removed:
leaving its Sonnet/medium keys would override the lane after the later wildcard
defaults are removed. The later concrete refinery/polecat pins remain in their
existing order. Only Python419's project-lead gets a new exact-key patch;
Kit's concrete project-lead pin is never targeted.

```diff
--- a/city.toml
+++ b/city.toml
@@ -1,3 +1,5 @@
+include = [".gc/provider-lanes.toml"]
+
 [workspace]
 provider = "claude"

@@ -1038,11 +1040,6 @@
 # Note the rig still carries suspended_on_start = true (line 718), which is a
 # SEPARATE gate: it brings gascity up suspended at the next `gc start`. This
 # resume does not touch it.
-[[patches.agent]]
-dir = "gascity"
-name = "gastown.refinery"
-option_defaults = { model = "claude-sonnet-5", effort = "medium" }
-
 # Deployment hold 2026-08-15: keep Bob out of the safety-train validation and
 # soak until the Mayor explicitly resumes him after promotion.
 [[patches.agent]]
@@ -1159,7 +1156,7 @@
 # (crew sessions use the city-owned agents/anthony), status-line test.
 [[patches.agent]]
 name = "gastown.boot"
-option_defaults = { model = "claude-haiku-4-5-20251001", effort = "low" }
+provider = "lane_recon"

 # wake_mode 2026-08-24 (az-e40s): the supervisor tears down a wake_mode=fresh
 # session whenever the bead assigned to it changes — cmd/gc/session_bead_cycle.go
@@ -1174,13 +1171,13 @@
 [[patches.agent]]
 name = "gastown.deacon"
 prompt_template = "//overlays/gastown/prompts/deacon.prompt.template.md"
-option_defaults = { model = "claude-sonnet-5", effort = "low" }
+provider = "lane_coordination"
 wake_mode = "resume"

 [[patches.agent]]
 name = "gastown.dog"
 prompt_template = "//overlays/gastown/prompts/dog.prompt.template.md"
-option_defaults = { model = "claude-haiku-4-5-20251001", effort = "low" }
+provider = "lane_coordination"

 # wake_mode: see the az-e40s note on the deacon patch above. The mayor is the
 # clearest case in the city — a long-lived coordinator whose whole job is
@@ -1188,25 +1185,26 @@
 [[patches.agent]]
 name = "gastown.mayor"
 prompt_template = "//overlays/gastown/prompts/mayor.prompt.template.md"
-option_defaults = { model = "claude-opus-5", effort = "high" }
+provider = "lane_coordination"
 wake_mode = "resume"

 [[patches.agent]]
 name = "gastown.refinery"
 rig = "*"
 prompt_template = "//overlays/gastown/prompts/refinery.prompt.template.md"
-option_defaults = { model = "claude-sonnet-5", effort = "low" }
+provider = "lane_coordination"
 pre_start = ["{{.CityRoot}}/overlays/gastown/scripts/worktree-setup.sh {{.RigRoot}} {{.WorkDir}} {{.AgentBase}} --sync"]

 [[patches.agent]]
 name = "gastown.witness"
 rig = "*"
 prompt_template = "//overlays/gastown/prompts/witness.prompt.template.md"
-option_defaults = { model = "claude-sonnet-5", effort = "low" }
+provider = "lane_coordination"

 [[patches.agent]]
 name = "gastown.polecat"
 rig = "*"
+provider = "lane_worker"
 prompt_template = "//overlays/gastown/prompts/polecat.prompt.template.md"
 nudge = "Run gc hook --claim --json now; if it returns work, enter that bead's own worktree first as Directory Discipline requires, then execute the claimed formula immediately."
 pre_start = ["{{.CityRoot}}/overlays/gastown/scripts/worktree-setup.sh {{.RigRoot}} {{.WorkDir}} {{.AgentBase}} --sync"]
@@ -1420,6 +1418,11 @@
 provider = "codex_mac"
 option_defaults = { effort = "" }

+[[patches.agent]]
+dir = "python419"
+name = "oversight-rig.project-lead"
+provider = "lane_coordination"
+
 [[rigs]]
 name = "deckkit"
 prefix = "dk"
--- a/agents/anthony/agent.toml
+++ b/agents/anthony/agent.toml
@@ -1,4 +1,5 @@
 scope = "rig"
+provider = "lane_worker"
 wake_mode = "resume"
 # Rig-agnostic crew template: instantiated per-rig via [[named_session]] in
 # city.toml (authoritative rig list lives there — 12 rigs as of az-axj).
--- a/agents/frank/agent.toml
+++ b/agents/frank/agent.toml
@@ -1,4 +1,5 @@
 scope = "rig"
+provider = "lane_worker"
 wake_mode = "resume"
 # Rig-agnostic crew template: instantiated per-rig via [[named_session]] in
 # city.toml. {{.Rig}} resolves to the named_session's dir, {{.AgentBase}} to
--- /dev/null
+++ b/.gc/provider-lanes.toml
@@ -0,0 +1,13 @@
+# Operator-approved role-class assignments (2026-09-20); not installed.
+# Membership is static and separate. Concrete-provider exceptions stay concrete.
+[providers.lane_coordination]
+base = "provider:claude"
+option_defaults = { model = "claude-opus-5", effort = "low" }
+
+[providers.lane_worker]
+base = "provider:claude"
+option_defaults = { model = "claude-sonnet-5", effort = "low" }
+
+[providers.lane_recon]
+base = "provider:claude"
+option_defaults = { model = "haiku", effort = "low" }

```

## Review boundary and reproduction baseline

Decision: the operator approved the 47-template membership above, all listed
exclusions, and deferred escalation. Approval of membership is not permission
to deploy immediately; the installation gates above
and a deliberate staged-cutover window remain necessary.

The candidate lives only in the private inspection directory
`/private/tmp/anthony-membership.WmXOPo`. The diff above is the durable artifact.
Rebuild and revalidate from current on-disk inputs before any eventual migration.

SHA-256 before-image identifiers:

```text
1ba6c75d2f3945e8297f5d0b7fe1e03ece04af32ca4c6707aea9359e918bd635  city.toml
59cbd9d91ae4334aa140efad94efca7a05375aabf511f83a8038c14d8bb0982d  pack.toml
a96101958878a21659e3d6745139e99ab88da0cfb33aa09524d60458d9e10fe4  packs.lock
af45529af422bf016fdf74c9faadc66ba45e0a11c90ab6599210d12ba35d3b3f  .gc/site.toml
c48a9ed47758664ed060d37f494990cc74c9a4e1ef82be87c1b2a86011369dc2  agents/anthony/agent.toml
9b3513381c2919481e76b4807cd136f90d070caece579d6a478384a876a004a4  agents/frank/agent.toml
```
