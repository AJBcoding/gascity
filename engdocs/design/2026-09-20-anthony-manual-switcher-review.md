# Anthony city manual switcher: design for review

Date: 2026-09-20

Status: review draft. The operator requested this write-up for another agent
to review. This is not approval to implement, edit live configuration, move
sessions, or deploy anything.

Reviewed 2026-09-20: see [Addendum A](#addendum-a-design-review-2026-09-20).
It keeps this draft's operator contract and honesty rules but recommends a
different switching mechanism (provider lanes instead of swapped agent
patches), and it supersedes "Validation and precedence", review question 3,
and review question 4. It also corrects one assumption: stacked `-f` preflight
is measurably unfaithful for provider definitions. The body below is left as
written so the decision trail stays readable.

Target city: `/Users/anthonybyrnes/code/cities/anthony`.

Source workspace: `/Users/anthonybyrnes/code/gascity`. This proposal concerns
the deployed city and a city-local operator tool. It proposes no new Gas City
primitive, account system, daemon, or core CLI command.

## Recommendation and decisions

Use two explicit provider variants, one fixed included TOML file, and a small
helper for preview, validated activation, observation, and configuration undo.
Preserve explicitly pinned exceptions. Keep existing-session cutover manual
in the first version, subject to operator confirmation.

The purpose is to let one operator choose settings, inspect the effect, and
undo the configuration change with few moving parts. A successful reload must
never be presented as evidence that every running process has switched.

| Topic | State of the discussion |
| --- | --- |
| Manual, deliberate switching; no automatic rotation | Required by operator |
| Preserve explicit provider exceptions | Confirmed: “Preserve explicit exceptions” |
| Temporarily mixed fleet | Operator initially accepted explicit staged cutover, then requested more detail |
| Leave all existing-session movement manual | Recommended and explained; not yet explicitly confirmed after that explanation |
| Model/effort policy | Latest recommendation: explicit approved assignments per template and provider; concrete assignments remain undecided |
| Account selection | Keep provider siblings for Codex and the existing shared-credential procedure for Claude |
| Name | Proposed city-local `anthony-profile`; not approved |
| First deliverable | Proposed axis 1 tooling; existing mechanisms and runbooks for axes 2–4 |
| Implementation | Not authorized |

## Problem and scope

The operator wants control over four axes:

1. Provider across the fleet, preserving the intended allocation of models
   and reasoning effort to work.
2. Provider for a particular rig's polecat pool.
3. Account within a provider.
4. Named-session mode (`on_demand` or `always`) and pool capacity per rig.

The hardest new requirement is axis 1's model policy. Axis 2 already works in
this city. Account selection already has provider-specific mechanisms. Axis 4
has configuration support but has not been exercised through these patches
here. Covering every axis with new verbs would not, by itself, improve the
operator's ability to trust the system.

“Fleet-wide” means the reviewed set of model-backed templates, with explicitly
pinned exceptions preserved. It must not blindly include shell-based control
dispatchers or every provider-generated pseudo-agent. Shared crew templates
such as `anthony` and `frank` have bare template identities; their named seats
do not imply independently patchable per-rig agent definitions.

The exact managed template set and exception list need review before install.
An existing provider setting alone is not evidence that the operator intended
it as a permanent pin. python419's explicitly approved Codex polecat exception
is the concrete example discussed.

## Does one mechanism cover the four axes?

Mostly, for desired configuration. Patches do not perform every associated
operation.

| Axis | Existing configuration mechanism | Residue |
| --- | --- | --- |
| Fleet provider and model policy | Agent patches; `rig="*"` for a named template across rigs, plus city-scoped templates | Human-approved provider-specific assignments; existing-session cutover |
| One rig's polecats | Exact rig/template agent patch | Existing workers keep their current process until cutover |
| Codex account | Select a provider sibling with its own `CODEX_HOME` | Confirm actual home and account; existing processes retain their launch context |
| Claude account | Shared credential procedure | Cannot be expressed as a per-rig account patch in this deployment |
| Named-seat mode and pool counts | City-level named-session patches; agent pool min/max or corresponding scaling fields | Reconciliation, wake conditions, and actual capacity must be observed |

`rig="*"` still requires a template name. One such block is not a universal
provider flip. A named-session mode patch targets an existing canonical seat
name; it does not create arbitrary new named seats. Mode and pool capacity are
also different controls: lowering a pool limit is not equivalent to removing
or stopping every named seat in a rig.

The residue does not currently justify a second switcher subsystem. Claude
credential changes and deliberate session handoffs should remain documented
manual procedures. If the operator later requires the tool to complete live
cutovers, that is a larger design with its own failure and recovery contract.

## Evidence and corrections to the research

The source documents were read in full during this discussion:

- `/Users/anthonybyrnes/code/cities/anthony/docs/research/2026-09-20-multi-axis-switcher-research.md`
- `/Users/anthonybyrnes/code/cities/anthony/docs/research/2026-08-24-provider-switching-research.md`
- `/Users/anthonybyrnes/code/cities/anthony/docs/runbooks/claude-accounts-and-quota.md`
- `/Users/anthonybyrnes/Downloads/profile-switcher-recipe.md`

The first three supply constraints and history. The recipe supplies an
alternative to critique. Their implementation recommendations are not
operator approval of this design.

### Process environment gate: passed on this deployment

Read-only inspection on 2026-09-20 found the following live processes:

| PID at observation | Agent | Session bead |
| --- | --- | --- |
| 61571 | `python419/gastown.furiosa` | `az-wisp-7hjlm` |
| 2740 | `python419/gastown.slit` | `az-wisp-1d7g5` |
| 4051 | `python419/gastown.rictus` | `az-wisp-fzbzz` |
| 4468 | `python419/gastown.nux` | `az-wisp-0qqdl` |

All four process environments contained:

```text
GC_TEMPLATE=python419/gastown.polecat
GC_CITY=/Users/anthonybyrnes/code/cities/anthony
CODEX_HOME=/Users/anthonybyrnes/.codex-profiles/py419-polecat
```

That home matches `[providers.codex_py419].env`. The session listing for
`az-wisp-fzbzz` also reported provider `codex_py419` and command
`codex --dangerously-bypass-approvals-and-sandbox --model gpt-5.6-sol`.

This establishes propagation for these herdr-backed Codex launches. It does
not establish every provider/runtime path, credential identity, or effective
reasoning effort. PIDs are historical observations, not future lookup keys.
The upstream report concerns tmux and explicitly says herdr was not tested:
[issue #4945](https://github.com/gastownhall/gascity/issues/4945).

### The supposedly low-effort home has drifted

The files read during this session contained:

| Home | Model in its base config | Effort in its base config |
| --- | --- | --- |
| `~/.codex` | `gpt-6-astra` | `xhigh` |
| `~/.codex-profiles/csulb` | `gpt-5.6-sol` | `medium` |
| `~/.codex-profiles/py419-polecat` | `gpt-5.6-sol` | `high` |

Thus the report's low/medium/high inventory was already stale. Selecting
“the low home” would make a false promise. These are file observations, not
claims about what an already-running process loaded earlier.

The live city configuration also warns that the provider named exactly
`codex` has a launch collision that can emit the forbidden effort argument.
It recommends a non-colliding sibling for CSULB. Do not infer that the account
is safely selectable merely because the existing provider name is declared.

### Installed binary and config semantics

The inspected binary was `/Users/anthonybyrnes/.local/gc-bin/gc`, with embedded
build commit `2c5c83305` and build date `2026-09-12T13:44:58Z`. Source checks
used that commit, not the unrelated workspace HEAD. Relevant locations:

| Source at `2c5c83305` | What was checked |
| --- | --- |
| `internal/config/compose.go:136`, `:1115` | CLI `-f` appends after includes; patch lists accumulate |
| `internal/config/patch.go:63`, `:162` | Agent args support explicit replacement; option defaults merge additively |
| `internal/config/provider.go:391` | Empty effective option values suppress emitted option arguments |
| `cmd/gc/soft_reload.go:160` | Soft reload updates accepted config fingerprints; a matching hash is not launch proof |
| `cmd/gc/controller.go:888` | File watcher ignores `.gc` paths; an overlay there needs explicit reload |

`gc config show --validate` ran successfully against the installed city and
printed existing warnings, including `always`/`wake_mode="fresh"` cases.
This was baseline validation, not validation of a proposed switcher variant.
No candidate files or canary sessions were created.

## Model policy: approved assignments, not tier conversion

The operator supplied another user's arrangement: Opus/low for coordination,
refinery work, research, and maintenance; Sonnet and Luna worker pools; a Luna
reconnaissance pool; a Fable lane for difficult debugging and PR work. That
user also described persistent mandates and repeated handoffs to keep a
coordinator focused while delegating unrelated escalations.

This is evidence of that user's policy and experience, not a prescribed
configuration for Anthony's city or a benchmark of those models.

The useful distinction is between model choice, reasoning effort, and work
assignment. “Opus/low” need not be a low-capability allocation. Matching effort
strings across providers does not preserve the operator's intent.

The proposed policy is therefore:

- Every managed, switching template has an explicitly approved Claude
  assignment and Codex assignment.
- Each assignment specifies model and effort. Repeated values across
  templates are acceptable and readable.
- Account choice is independent of the intended model/effort assignment.
- Explicit pins retain provider, account home, model, effort, and required
  launch settings across variants.
- A missing counterpart rejects the whole apply before activation. Preview
  names the template requiring a decision; it does not quietly skip it or
  inherit a mutable home default.

Store the assignments in the TOML variants. Avoid a separate tier registry or
a second mapping table embedded in the helper. There is no approved concrete
mapping yet. The earlier suggested Opus/high → Sol/high, Sonnet/low →
Sol/medium, Haiku/low → Sol/low mapping was withdrawn as the preferred design.

Mandate persistence belongs in agent prompts and handoff practice. The
switcher cannot preserve conversation context merely by selecting equivalent
launch settings.

### Separating Codex account from effort

Prefer testing Codex's native named config profiles before adding account
homes for each effort. The installed CLI exposes `--profile <name>` for both
fresh sessions and resume. It loads `$CODEX_HOME/<name>.config.toml`.

A proposed arrangement would select the account with a provider sibling and
select effort with arguments such as `--profile gc-high`. A small effort
profile can coexist with an explicit model in the Gas City patch. There is
no `=` in that profile argument, avoiding the specific reported herdr trigger.
The official docs describe these files as layers above base user config and
below project/CLI config:
[Codex profiles](https://learn.chatgpt.com/docs/config-file/config-advanced#profiles).

This path has not been proven end-to-end through Gas City and herdr. Before
relying on it, check fresh launch, resume, project overrides, missing-profile
failure, and observed effective effort. Each selectable account home must
have the intended profile available. Base home changes must not silently
change the approved assignment.

If it fails, bring back the concrete failure for a design decision. Existing
compound home/account/effort choices are a possible fallback, but silently
reducing level preservation to “use whatever this home defaults to” is not.

## Alternatives

| Candidate | Moving parts | Cannot do | Characteristic failure |
| --- | --- | --- | --- |
| A. Fragments and runbook | Named TOML variants, fixed include, read-only verifier, manual activation procedure | Enforce that the operator validates before installation; complete live cutover | Skipped preflight or wrong copy can activate a bad patch; unrelated axis combinations multiply variants |
| B. Guarded activation helper, recommended | A plus one helper and a previous-file backup | Complete live cutover, rotate credentials, support concurrent writers | Config can be installed while reload is unconfirmed; existing seats may remain on old settings indefinitely |
| C. Transition manager | B plus handoff/stop/wake actions, serialization, journal, retries and recovery rules | Guarantee work survives unreliable lifecycle operations without additional evidence | Queued or misreported actions, stalled wakes, assignment changes and ambiguous recovery |

Candidate B makes validation unavoidable through the supported apply path
without taking ownership of the problematic lifecycle machinery. It assumes
one operator performs one configuration transaction at a time. Concurrent
invocations or concurrent config editors are outside that guarantee; a lock
cannot be omitted while promising multi-writer correctness.

## Recommended operator contract

These commands are proposed, not implemented:

| Operation | Responsibility |
| --- | --- |
| `anthony-profile preview <variant>` | Show resolved before/after assignments, affected templates, preserved exceptions and validation errors |
| `anthony-profile apply <variant>` | Revalidate, preserve previous bytes, atomically install, request soft reload, and report observations |
| `anthony-profile status` | Read desired configuration, session lifecycle and actual process evidence again |
| `anthony-profile undo` | Validate and restore the previous configuration, request soft reload, and report observations |

Use a deliberately distinct name. Upstream
[issue #3117](https://github.com/gastownhall/gascity/issues/3117) proposes
`gc provider set|clear` with a runtime override store and draining. Sharing
its spelling would not make a TOML activator's storage or lifecycle semantics
compatible. Keep the city-local helper easy to retire if upstream eventually
covers the required behavior.

### Artifacts and why each exists

| Artifact | What breaks without it |
| --- | --- |
| Two reviewed provider variants | The intended per-template assignments are implicit and hard to inspect |
| One fixed include and active TOML file | Activation must repeatedly edit the large city config |
| Validation and atomic replacement in one helper | A skipped check or partial write can cause a city-wide config failure |
| Previous active bytes | Undo depends on reconstructing what was actually active |
| Read-only observation | A clean reload can be mistaken for a completed fleet switch |
| Small runbook | Credential changes and session handoffs remain undocumented operational residue |

Native Codex effort files earn their place only if their acceptance check
passes: they separate effort from credential-bearing homes using an existing
CLI feature. They are not another switcher service.

Active configuration is the actual included file. Its digest may label a
known variant; an unmatched digest means unrecognized/custom bytes, not an
automatically inferred variant. The digest says nothing about runtime
convergence. Do not persist another “current profile” pointer or session
status cache.

### Staged cutover, precisely

After a provider apply, existing processes are intended to continue while
fresh launches use the new assignment. Soft reload accepts configuration
drift; the helper does not send handoffs, reset sessions, suspend seats, or
force wakeups. It must surface soft-reload failures or warnings rather than
promise that no seat could be affected.

An illustrative report could be:

| Seat | Desired | Observed | Interpretation |
| --- | --- | --- | --- |
| mayor | Approved Codex assignment | Claude process | Manual cutover remains |
| python419 polecat | Pinned Codex/high assignment | Matching process/home; effort confirmed separately | Preserved exception |
| sleeping witness | Approved Codex assignment | No process | Configured; launch unverified |

The operator can move an existing seat at a safe stopping point using a
reviewed handoff procedure. Session beads are the evidence for lifecycle
state; herdr and real process observations are the evidence for what is
running. The runbook must not disguise `gc session reset` as a safe recycle
primitive: the research reports assignment clearing and duplicate-session
hazards. Neither suspension nor pinning has been proven here as a universal
replacement that preserves work and selects the new provider on resume.

A long-running seat may never switch without operator action. This first
version would not guarantee immediate quota relief or a cutover deadline.
That limitation is the outstanding cutover decision for the operator.

Mode and capacity changes are outside this first-version apply contract.
Changing a seat to `always` or adjusting a pool can trigger lifecycle work;
soft reload does not turn those operations into passive declarations.

### Validation and precedence

A patch matching nothing is a city-wide outage hazard. Every supported apply
must reject errors before touching the active file, using the installed
binary's `gc config show --validate -f <candidate>` and
`gc config explain -f <candidate>` plus the helper's limited structural checks.

There is an important constraint on those dry runs: `-f` stacks a candidate
after the currently included file. It does not preview replacement. The
recommended initial restriction is that both variants have the same ordered
target structure and explicitly specify the same managed fields: provider,
model, effort, and profile arguments. No arbitrary additional mutation fields
are accepted by this narrow helper.

For those fields, the required property is:

```text
apply(candidate, apply(active, base)) == apply(candidate, base)
```

Prove that property for the supported variants before depending on stacked
dry runs. Every inherited incompatible option must be overwritten; omission
cannot clear `option_defaults`. Codex patches keep `effort=""` and explicitly
overwrite the Claude model with the approved Codex model. Claude patches
restore their approved settings and clear Codex-only arguments. Explicit
argument replacement is supported in the inspected build, but its behavior
still belongs in the acceptance checks.

If completeness cannot be established, require a validation view of the
actual replacement configuration. Do not temporarily install a candidate in
the live city just to validate it. The reviewer should challenge whether the
completeness restriction remains simpler than such a replacement preview.

Within a variant, broad template patches precede exact exceptions. Includes
append after the root's patches, so an included wildcard can overwrite an
existing root-level rig override. Explicit exceptions must therefore be
represented at the final effective precedence and verified as unchanged.
Maintaining duplicate exception values in root and variants is a drift risk;
the one-time ownership arrangement needs review before installation.

Coverage checks must use the current template inventory, not a historical
count. During inspection, config reported 141 agent entries and 76 named
sessions; the report had 125 agent entries. Neither number by itself identifies
the set that should switch. New or unclassified relevant templates require
an explicit decision before claiming fleet coverage.

### Verification, failures and undo

Status should distinguish installed desired bytes, acknowledged reload,
resolved settings, and actual running processes. Re-read the live population;
do not restrict verification forever to a start-of-transition snapshot.

Correlate session identities with herdr and process identities. Use process
arguments, selected environment fields, and appropriate live provider output
to establish model/home/effort where possible. A current home config file does
not prove an old process's loaded settings. A home path does not prove account
identity. A session bead's accepted config fingerprint does not prove that
the process relaunched. Missing evidence stays explicitly unverified, with
observation errors visible. Never print credential values.

| Failure or interruption | Required behavior |
| --- | --- |
| Candidate invalid, incomplete, unmatched, or missing a provider counterpart | Reject whole apply; leave active bytes unchanged |
| File or base configuration changed after preview | Revalidate at apply; preview is not a durable permission slip |
| Interruption while preparing replacement | Keep the existing active file intact |
| Interruption after replacement but before confirmed reload | Report installed bytes and unconfirmed adoption; permit deliberate reapply or undo |
| Reload error or timeout | Show the ambiguity and preserve the undo target; do not claim runtime rollback |
| Old-provider processes still running | List them as awaiting cutover |
| Session/process inspection fails | Report the error and unverified scope, not a clean fleet |
| Same variant applied again | Permit reload retry without overwriting the original undo bytes with identical current bytes |
| Undo requested | Validate previous bytes against today's base config, restore, reload, and observe |

Atomic replacement applies to the configuration file, not to the entire
fleet. Restoring bytes cannot rewind provider conversations, work assignments,
credential changes, or already-running sessions. A previously valid file
can become invalid when templates or packs change; undo must validate too.

The proposal does not automatically roll back on a reload timeout: the
request may still complete, and a second mutation can obscure what happened.
An explicit undo plus fresh observation is the recommended recovery contract.

## What survives from the seed recipe

Keep the fixed include, complete declarations for managed settings, validation,
atomic activation, configuration undo, and verification of real launches.

Drop the transition journal, automatic mail/nudge/handoff actions, takeover
reset path, duplicate policy tables inside the script, and four-state global
verdict machine. Query current observations instead. A lock is omitted only
under the explicit one-operator, one-transaction-at-a-time assumption.

These omissions reduce machinery because the helper does not own session
migration. Adding that responsibility later requires reconsidering recovery
and serialization rather than quietly extending the same small script.

## Scope, effort and deferred work

First deliverable, if approved: axis 1 variants and helper, preserved explicit
exceptions, observation, and configuration undo. Axis 2 remains the existing
per-rig patch procedure. Codex account choices remain provider siblings;
Claude account changes follow the existing runbook and credential-identity
checks. Axis 4 remains a separately validated manual procedure.

A rough estimate is a few focused days after approval, assuming native Codex
profiles and the deployed reload behavior pass acceptance checks. The work
would establish the template assignments and ownership boundary, verify
profile launch/resume behavior, implement the narrow helper, and exercise
invalid candidates, interruption, partial adoption, and undo. This estimate
does not include a lifecycle repair project or a transition manager.

Defer independent verbs for all four axes, arbitrary fragment composition,
automatic rotation, credential mutation, dashboards, multi-writer support,
and automated existing-session convergence. If exact per-seat observation
requires substantially more machinery than expected, reassess the helper's
scope rather than reporting weaker evidence as success.

## Requested review

Review for the fewest moving parts an operator can trust at 2am. Explain what
concretely breaks without each component you recommend retaining or adding.

1. Is guarded activation worth more than fragments plus a runbook here?
2. Is the staged contract useful enough if live cutover remains manual?
3. Can complete variants make `-f` preflight faithfully represent replacement,
   including cleared arguments and wildcard/exception precedence?
4. How should pins have one clear owner while remaining inspectable and
   unchanged across both provider variants?
5. Does native Codex `--profile` actually preserve intended effort through
   Gas City, herdr, project overrides, and resume? What is the smallest
   post-approval acceptance check?
6. Are the proposed observation and undo claims honest under timeout, missing
   evidence, changing base configuration, and mixed live providers?
7. Can any artifact, command, or restriction be removed without weakening a
   required guarantee? Does any omitted mechanism have a concrete necessity?

Return findings with evidence and severity, followed by a simplified preferred
design. Treat concrete model assignments, the tool name, manual-only cutover,
and the first-deliverable scope as pending operator decisions. Do not mistake
this review draft or the research document's implementation prompt for build
authorization.

Suggested instruction to the reviewing agent:

> Review this document and its cited local research as a standalone design
> partner. Stay read-only: no production code, config edits, builds, reloads,
> session actions, credentials changes, beads, mail, or external messages.
> Challenge the recommendation and minimize moving parts. Distinguish measured
> facts from proposals and untested assumptions. Report your findings and
> recommendation to the operator; implementation requires separate sign-off.

## Addendum A: design review, 2026-09-20

Reviewer: a second agent session, read-only. Nothing was installed, reloaded,
or moved. Two `gc config show` invocations layered a scratch file (since
deleted) over the live city to measure merge behavior; `config show` does not
mutate the city. No beads, mail, or upstream posts were made.

Evidence labels used below: **measured** (observed on the live city with the
deployed binary), **read** (source at deployed commit `2c5c83305`),
**untested** (needs an acceptance check).

### A.1 Verdict

Keep the operator contract (preview / apply / status / undo), the staged
cutover, the refusal to treat reload as launch proof, and everything the draft
drops from the seed recipe. Change the mechanism: switch **provider
definitions**, not agent patches. Most of the draft's hardest sections exist
only because the variant files are agent patches.

| Hazard in the draft | Cause | Under provider lanes |
| --- | --- | --- |
| A patch matching nothing takes the city down (already happened: `kit/anthony`) | Swapped file is keyed on agent identity; unmatched key is a hard error | Swapped file contains no agent keys |
| Included wildcard overrides a root-level pin; pin values duplicated across variants | Includes append after root patches | Pins never reference a lane; one owner, no precedence question |
| `apply(candidate, apply(active, base)) == apply(candidate, base)` proof obligation | Additive `option_defaults`; omission cannot clear | Two fields per lane, both always fully specified |
| Coverage audit against 141 agent entries | Variant must enumerate templates | Membership is opt-in and greppable |

### A.2 Preferred design: provider lanes

gc already supports custom-to-custom provider inheritance
(`base = "provider:X"`, `internal/config/chain.go:238`, **read**) and
provider-level `option_defaults` as the layer beneath agent-level ones
(`internal/config/options.go:42`, **read**). A scratch provider

```toml
[providers.lane_probe]
base = "provider:codex_mac"
option_defaults = { model = "gpt-5.6-sol", effort = "" }
```

resolved with both option defaults intact and passed
`gc config show --validate` on the live city (**measured**).

A **lane** is a provider that names one approved assignment class. The swapped
file holds only lanes, one table per class, for example:

```toml
# variant: codex
[providers.lane_worker]
base = "provider:codex_mac"      # account axis: this one word
option_defaults = { model = "gpt-5.6-sol", effort = "" }

# variant: claude — same lane name
[providers.lane_worker]
base = "provider:claude"
option_defaults = { model = "claude-sonnet-5", effort = "low" }
```

Lane names and the number of lanes follow the operator's assignment classes
(still undecided; see A.7). The model and effort values above are
placeholders, not proposals.

Properties:

- **Axis 1** is swapping a file of a few provider tables.
- **Axis 3 (Codex account)** is the lane's `base`, independent of model and
  effort. Claude account switching is unchanged: upstream #4945 is open with
  no PR.
- **Pins** do not point at a lane. `python419/gastown.polecat` stays
  `provider = "codex_py419"` in the root config and is untouched by any
  variant. This answers review question 4.
- **Coverage** is the set of agents whose resolved provider is a lane. A new
  template is unmanaged until someone opts it in; it cannot be silently
  half-switched.
- Lane names cannot collide with the builtin name `codex` (gas-y1lu; upstream
  #6073 looks like a related report).

#### One-time migration (the risky step; do it once, deliberately)

Agent-level `option_defaults` win over provider-level ones, and an agent-level
`model = ""` would suppress the lane's model rather than defer to it. So
model and effort must be *removed* from managed agents, not blanked.

In this city that is tractable: every agent-level model/effort default is
city-owned — four `agents/*/agent.toml` files (`mechanic`, `code-review`,
`codex`, `codex2`) and 15 `option_defaults` lines in `city.toml` patches; the
packs carry none (**measured** by grep). Migration is: for each managed
template, set `provider` to its lane and delete its model/effort defaults.

Pack-owned templates (`gastown.*`, `oversight-rig.*`) still need a root-level
agent patch to set `provider`. Those patches remain, but they become
**static**: written once, validated once, never swapped. The match-nothing
hazard applies to editing them, as it does today, not to switching.

The migration changes every managed agent's config hash, so it must itself be
adopted with `gc reload --soft`, with the pools it touches observed afterward.

### A.3 Correction: stacked `-f` preflight is unfaithful for providers

`deepMergeProvider` (`internal/config/compose.go`, **read**) merges an explicit
list of fields. `base` and `option_defaults` are not on it. When a layered
file redefines a provider that already exists, those two fields are dropped —
no warning, no error.

**Measured:** layering `[providers.codex_mac] option_defaults = { model = … }`
over the live city left `codex_mac` unchanged in the resolved output, and
`--validate` exited 0. The same code is on workspace HEAD,
`integration/deploy-20260804`, and `upstream/main`. No upstream issue or PR
covers it (searched 2026-09-20).

Consequence: `gc config show --validate -f <candidate-lanes>` validates the
*currently active* lanes and reports success. It must not be the preflight.
Two acceptable replacements:

1. **Replacement view, no gc change (recommended first).** Build a scratch
   city directory: symlink the real city's entries, copy `city.toml`, place
   the candidate where the include points, run
   `gc config show --city <scratch> --validate` and `config explain`. Remove
   the scratch directory unconditionally. **Untested** — confirm `--city`
   resolves packs and relative includes through symlinks.
2. **Fix gc.** Teach `deepMergeProvider` `base` and `option_defaults`. Small,
   upstream-owned, base on `origin/main`. With it, fully specified lanes make
   stacked `-f` faithful and option 1 can be retired.

This supersedes the draft's "Validation and precedence" algebra. If the
operator rejects lanes and keeps agent-patch variants, the draft's
completeness restriction still holds for agent fields, since agent patches do
not go through `deepMergeProvider`.

### A.4 Missing invariant: where the active file lives

The config watcher ignores only `.gc/` and `.beads/` paths
(`shouldIgnoreConfigWatchEvent`, `cmd/gc/controller.go`, **read**). If the
active include sits beside `city.toml`, the helper's atomic rename triggers an
ordinary reload, which drains every session whose hash drifted — the opposite
of staged cutover, fleet-wide, at the moment of apply.

Make it explicit:

- Reviewed variants are tracked files in the city repo (for example
  `config/lanes/<variant>.toml`).
- The active copy lives under `.gc/` (gitignored here, **measured**) and is
  adopted **only** by `gc reload --soft`.
- `apply` must surface `softReloadAcceptanceResult` warnings: `Failed`
  sessions "may still drain", and an empty desired state skips acceptance
  (`cmd/gc/soft_reload.go`, **read**). Add this row to the draft's failure
  table.

**Untested:** that an `include` of a `.gc/` path is honored. The city has no
`include` line today (**measured**), so adding one is itself a one-time root
edit, to be adopted with `--soft` like the migration in A.2.

### A.5 Upstream state that bears on the plan (searched 2026-09-20)

| Upstream | State | Effect here |
| --- | --- | --- |
| #5441 / PR #5446 | Issue open; PR open since 2026-09-05, mergeable, unreviewed; **not in the deployed binary** | With `--profile` routed via `args_append` on a `builtin:codex` provider, the builtin's `model`/`effort` defaults are still emitted as flags, and Codex lets flags beat the profile. `effort = ""` masks the effort half today; keep it. The PR's documented caveat: in a leaf→custom→builtin chain, an *intermediate* provider's explicit model is also suppressed when the leaf routes a profile — so put model and any `--profile` argument on the same (lane) hop, never split across hops. |
| #6465 | Open | `config explain` showed a provider override that never reached the launched argv. Confirms the draft's rule: only process arguments are launch evidence. |
| #5659 | Open | An agent-level `resume_command` drops provider `option_defaults` on plain resume. Lane-managed agents must not carry their own `resume_command`; resume belongs in the acceptance check. |
| #6073 | Open | Provider named `codex` emits model/effort flags and hangs. Same family as gas-y1lu. |
| #3117 | Open since 2026-06-28, no PR | The draft's reason for a distinct, retirable city-local name stands. |
| #4945 | Open, no PR | Claude account stays a manual runbook. |
| #5710, PR #3222 | Open; per-dispatch model selection was reverted | No upstream path to per-work-item model choice; config-level assignment is the only route. |
| PR #956, PR #4063 | Merged | Provider inheritance (what lanes use) and `rig = "*"` (what the draft uses) are both supported in the deployed build. |

### A.6 Answers to the requested review

1. **Guarded helper vs fragments + runbook.** Helper, but smaller. The city
   directory holds roughly 60 `city.toml.bak-*` files: that is the runbook
   approach's observed failure mode. With lanes the helper is: build
   replacement view → validate → keep previous bytes → rename into `.gc/` →
   `gc reload --soft` → print warnings. `undo` is `apply` of the saved bytes
   through the same path, not a separate mechanism.
2. **Staged contract with manual cutover.** Useful and honest. Note that soft
   reload *stamps the new hash on old processes*, so after apply no
   hash-based signal can distinguish switched from unswitched seats; status
   must rest on process evidence, as the draft says.
3. **Faithful `-f` preflight.** No for provider variants (A.3). Yes, with the
   draft's restrictions, for agent-patch variants.
4. **Single owner for pins.** Pins are agents whose provider is a concrete
   provider, declared once in root. Variants cannot reach them (A.2).
5. **Codex `--profile`.** Still **untested**, and now known to interact with
   an unmerged upstream fix (A.5). Smallest acceptance check: one canary lane
   on an empty pool; fresh launch, then resume; read `ps` argv and the
   session's own reported model/effort for both. Pass requires no
   `-c model_reasoning_effort=` in argv (herdr Raw-path trigger) and the
   profile's effort observed in the session. If it fails, fall back to
   per-effort homes and report the concrete failure, as the draft requires.
6. **Observation and undo honesty.** Yes, with the A.4 drain-warning row
   added.
7. **What can go.** Under lanes: the helper's structural checks, the
   same-ordered-structure restriction, wildcard/exception precedence
   verification, the template coverage inventory, and a resolved-template
   `preview` (a diff of two small lane files plus the list of lane-managed
   agents is sufficient). `status` can be a separate read-only script; it
   shares no state with `apply`. Nothing omitted by the draft needs to come
   back.

### A.7 Acceptance checks before build authorization

All **untested**; each is small and read-only or confined to an empty canary
pool.

1. A two-hop lane (`lane → codex_mac → builtin:codex`) launches under herdr
   with the expected `CODEX_HOME`, model argument, and no `=`-bearing effort
   argument. Same for a Claude lane.
2. Resume of that canary preserves model and effort (#5659).
3. A stale Claude model left on an agent fails validation loudly under a
   Codex lane (expected via `ValidateOptionDefaults`; confirm).
4. `include` of a `.gc/` path is honored and the watcher stays quiet on
   rename.
5. The scratch-city replacement view validates what would actually be active.
6. The `--profile` check in A.6 item 5, if effort is to be separated from
   account homes.

### A.8 Decisions still with the operator

Unchanged from the draft: concrete model/effort assignments, the tool name,
manual-only cutover, first-deliverable scope. New:

- Lanes vs swapped agent patches.
- The lane set: how many assignment classes, and which templates join each.
- Whether to contribute upstream: an issue/PR for the `deepMergeProvider` gap
  (A.3), and the herdr launch evidence (778 clean / 15 flagged launches) as a
  data point on PR #5446. Neither has been posted.

This addendum is a review, not build authorization.
