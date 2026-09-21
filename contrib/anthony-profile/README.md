# Anthony's manual provider switcher

A standalone Python 3.11 helper for deliberate provider-lane changes. It uses
the deployed `gc`; it adds no Gas City command, daemon, account service, lock,
or journal. It never resets, drains, wakes, suspends, or moves a session.

Implementation target: Anthony's deployment, not an upstream provider API.
The approved mechanism is Addendum A of the
[design review](../../engdocs/design/2026-09-20-anthony-manual-switcher-review.md).

## Commands

Run from this directory, or make `anthony-profile` a local symlink to
`anthony_profile.py`. The executable requires Python 3.11 or newer on PATH.
Every invocation requires an explicit city; `--gc` selects a particular binary.

```sh
python3 -B anthony_profile.py --city /Users/anthonybyrnes/code/cities/anthony preview codex
python3 -B anthony_profile.py --city /Users/anthonybyrnes/code/cities/anthony apply codex
python3 -B anthony_profile.py --city /Users/anthonybyrnes/code/cities/anthony status
python3 -B anthony_profile.py --city /Users/anthonybyrnes/code/cities/anthony status --json
python3 -B anthony_profile.py --city /Users/anthonybyrnes/code/cities/anthony undo
```

`preview` and `apply` accept an existing file path, or a variant name resolved
as `CITY/config/lanes/NAME.toml`. Both validate again; a preview is not a saved
authorization token. `apply` prints the same diff and membership as preview.
Only call apply after reviewing preview. The explicit invocation is confirmation;
there is no hidden prompt or automatic selection.

Exit 0 means validation or the synchronous soft-reload request completed; it
does not mean the fleet adopted the new settings. Exit 1 means the request was
rejected or an I/O error occurred. Exit 2 means activation/reload is unconfirmed,
or status contains incomplete observations. Read diagnostics, not just the code.

## The files

| File | Purpose |
| --- | --- |
| `config/lanes/*.toml` | Reviewed named assignments, tracked in the city repository |
| `.gc/provider-lanes.toml` | Active desired lane definitions; ignored by the config watcher |
| `.gc/provider-lanes.previous.toml` | The recovery target saved before changing active bytes |

There is no current-profile pointer or session-status file. Run commands
sequentially as one operator. Do not edit configuration, packs, site bindings,
or effort profiles concurrently with preview/apply. Root and active bytes are
rechecked before writes, but that check is not locking or a transactional snapshot
of every imported file.

Variants contain only `[providers.lane_*]` tables. Each has a concrete sibling
`base`, explicit `model` and `effort`, and optionally a Codex `--profile` pair.
They cannot carry patches, account credentials, commands, environment overrides,
pool limits, named seats, or lifecycle changes.

Gas City synthesizes an on-demand template for each provider, including each
lane, at city scope and in each rig. Preview includes these templates;
defining a lane does not create a
running seat. Membership otherwise comes from each agent's resolved provider.
Concrete providers must not inherit lanes, even through Gas City's bare-base
syntax, or their supposedly pinned agents would change too.

## Codex effort and accounts

The concrete sibling selects the account via an absolute `CODEX_HOME`. The lane
owns the model and profile selection together. Its `effort = ""` suppresses
Gas City's `-c model_reasoning_effort=...` argument, which breaks this deployment's
herdr launch detection.

For example, a lane can select `args_append = ["--profile", "gc-high"]`, with
this separate, effort-only file in that account home:

```toml
# CODEX_HOME/gc-high.config.toml
model_reasoning_effort = "high"
```

The helper reads but never creates or edits account homes or profile files. It
rejects missing profiles and profiles containing other policy settings. Codex
0.155.1 writes its own UI counters and hook-trust hashes into selected profiles;
only `tui.model_availability_nux` integer counters and `hooks.state` entries
containing a `trusted_hash` are allowed alongside effort. Executable hooks,
model/account overrides, and unknown fields still reject validation. The native
profile-file mechanism is described in
[OpenAI's configuration documentation](https://learn.chatgpt.com/docs/config-file/config-advanced#profiles).
Existence and syntax are not evidence that a running session loaded the profile.
Project configuration and interactive changes can still override effective effort;
inspect the provider session itself during acceptance and cutover.

No silent home-default fallback is implemented. If the fresh/resume canary fails,
report that failure and decide whether compound account/effort homes are acceptable.
The helper does not invent a Claude-to-Codex capability equivalence.

The operator approved these exact pairings on 2026-09-20. The
[Claude](examples/claude.toml) and [Codex](examples/codex.toml) files contain the
approved assignments; they are not yet installed in the live city.

The [membership review](../../engdocs/design/2026-09-21-anthony-lane-membership-review.md)
found that these four-lane examples are not installable unchanged with the
deployed binary: the Haiku full ID and Astra provider default fail its model
enum. It proposes three initial lanes, Haiku's existing alias with alias-aware
status verification, and escalation deferred. Production effort profiles are
also absent. These remain installation gates, not changes made by preview.

| Lane | Claude | Codex |
| --- | --- | --- |
| Coordination, research, maintenance | Opus / low | Sol / high |
| Workers | Sonnet / low | Luna / medium |
| Recon | Haiku / low | Luna / low |
| Escalation | Fable / high | Astra / high |

These are operator-chosen counterparts, not claims of equivalent capabilities.
Existing concrete-provider exceptions remain untouched. Coordination, research,
and maintenance can share a lane because their assignment is identical.

## One-time migration, separate from switching

The helper deliberately does not migrate or install a live city's configuration.
It refuses apply until the fixed include and an initial active lane file exist.

1. Use the approved variants above and review the static template membership.
   Model approval does not create new researcher/recon pools. Preserve explicit
   concrete-provider exceptions, including Python419's Codex polecats.
2. Prepare the migration in a separate city configuration checkout. Add this
   top-level line before `[workspace]`, retaining any existing includes:

   ```toml
   include = [".gc/provider-lanes.toml"]
   ```

3. Put the initial reviewed definitions at that path. Assign existing templates
   to lanes in their city-owned agent files or static root patches. Remove their
   `option_defaults.model` and `.effort` keys at the source; setting empty values
   is not removal. Keep unrelated options. Managed agents cannot override `args`,
   `start_command`, `resume_command`, or `CODEX_HOME`; these would bypass lane
   settings. Do not modify shared packs to do this.
4. Preview every variant against that prepared checkout. Confirm unmatched static
   patches fail and all expected exceptions remain unmanaged. Crew templates do
   not acquire per-rig identities merely because named seats have them.
5. Complete fresh and resume canaries under herdr. Check actual PID environment,
   argv, and the session's effective model/effort. Include `.gc` rename/watcher
   behavior in an isolated running city before relying on staged production changes.
6. Install the one-time root/template migration during a deliberate maintenance
   window with configuration watching controlled. Editing watched root/template
   files and then racing `gc reload --soft` is not a staged-cutover guarantee.
   Ordinary watcher reloads can drain sessions. This utility does not automate
   stopping the watcher or restarting the supervisor.

Once installed, routine switches touch only the ignored active include and
explicitly request `gc reload --soft --timeout 45s`. They do not edit watched
templates or `city.toml`.

## Preview safety and limitations

The deployed provider merge drops `base` and `option_defaults` when an existing
provider is redefined with stacked `-f`. The helper therefore constructs a
temporary replacement root, copies `city.toml`, symlinks configuration siblings,
and supplies candidate bytes at the actual include path.

Its `.gc` is private: only the input `.gc/site.toml` is copied from the city.
This matters because `gc config show/explain` can regenerate `.gc/scripts` and
prune old `.gc/system/packs`; runtime directories must not be write-through
symlinks. Configurations depending on other `.gc` inputs need explicit adaptation
and are not silently approximated: runtime-path includes and missing/skipped
composition diagnostics reject preview/apply. Scratch cleanup runs on ordinary exceptions.
A forcibly killed process can leave a temporary directory; it is not a resume
journal and has no effect on live selection.

The deployed native validator accepts a stale Claude model on a Codex-backed
agent. The helper's separate check for agent-level model/effort keys is therefore
required, not redundant with `gc config show --validate`.

Gas City can maintain its own global pack caches while loading configuration.
Likewise, its existing session/config inspection commands may refresh generated
runtime assets. The helper does not modify desired config during preview/status;
it does not promise those upstream inspection commands perform zero filesystem I/O.

## Staged cutover and undo

After apply, newly launched seats use the desired configuration. Existing processes
may retain old settings indefinitely. Soft reload stamps new accepted hashes on
old sessions, so neither a matching hash nor a bead's provider field proves adoption.
Soft reload can also report failed acceptance, canceled drains, or empty desired
state. The helper retains both output streams, including on timeout, and reports
any reload stderr/warning as unconfirmed. It does not run automatic rollback.

Use status to see bead lifecycle state beside actual herdr process observations.
It correlates `GC_CITY`, `GC_SESSION_ID`, and `GC_TEMPLATE`; reads only selected
environment fields into its report; and shows PID, pane, observed model/profile,
account home, desired lane, and known mismatches. It does not print raw environments
or prompts. An unreadable/missing process, orphan, or ambiguous match remains
unverified. Snapshots can race normal lifecycle activity. Launch matches do not
certify effective settings after interactive changes. Account identity for Claude
remains the host-global runbook's concern.

At a safe stopping point, inspect the session bead and its assigned work, use the
existing lifecycle runbook, and inspect both again afterward. Do not use reset as
a convenient migration primitive: it clears in-flight work assignment. Lifecycle
command output alone is not evidence of completion. The helper never decides that
a busy seat is safe to interrupt.

Suspension is asynchronous: an acknowledgement or a bead marked `suspended`
can precede actual process exit. Wait for herdr/process evidence before waking
the seat; otherwise the same process may simply remain alive. For Codex resume,
also require a persisted provider conversation key on the session bead. The
normal `gc prime --hook --hook-format codex` SessionStart hook records that key;
herdr's separate identity-reporting hook alone does not. Keep both integrations
when provisioning an account home. The isolated acceptance run verified fresh
and resumed Sol/high and Opus/low, then mixed-state apply and undo; see the
[verification record](VERIFICATION.md).

Undo restores saved desired bytes through the same validator and soft-reload path.
It is an idempotent restore, not a toggle: it retains its recovery target, so a
failed/interrupted undo can be retried without destroying the only saved copy.
To redo a selection, apply its named variant. Undo does not revive previous
processes, conversations, assignees, or credentials.

An identical apply reattempts reload without overwriting previous bytes. A new
apply saves the current active bytes before replacing them. The two file writes
are not one crash-atomic transaction: a failed new apply can leave previous equal
to active. Keep the named variants in version control. If activation or reload is
uncertain, inspect active/previous bytes and process evidence; reapply the intended
variant or undo deliberately. A later root/pack change may make an old variant
invalid; undo then refuses rather than forcing an invalid city configuration.

Per-rig provider exceptions, Claude account switching, pool counts and
`on_demand`/`always` remain existing configuration/runbook operations. Creating new
pool roles or changing their prompts and persistent mandates is outside this utility.

## Verification

```sh
python3 -B -m unittest discover -s contrib/anthony-profile -v
ANTHONY_PROFILE_TEST_GC=/Users/anthonybyrnes/.local/gc-bin/gc \
  python3 -B -m unittest discover -s contrib/anthony-profile -v
ANTHONY_PROFILE_TEST_GC=/Users/anthonybyrnes/.local/gc-bin/gc \
  ANTHONY_PROFILE_TEST_CITY=/Users/anthonybyrnes/code/cities/anthony \
  python3 -B -m unittest discover -s contrib/anthony-profile -v
```

Run these from the repository root. The second invocation uses only owned
temporary cities and the deployed binary; it starts no sessions. It covers
`.gc` includes, relative pack imports, both provider chains, duplicate lane
rejection, stale agent pins, and the installed stacked-provider-merge defect.
The third also reads a pre-install city into a private scratch snapshot and checks
replacement validation there. It does not add an include or lanes to the real city.

See the [verification record](VERIFICATION.md) for measured results and remaining
live-installation gates. Unit tests are not a substitute for fresh/resume canaries.
