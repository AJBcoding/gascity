# Clean-Room Stock Pipeline Design

## Purpose

Establish a known-good Gas City build, test, initialization, lifecycle, release,
and rollback path without inheriting code, configuration, Git topology, runtime
state, or storage from the broken local system.

The first successful result is a golden stock baseline. Existing production,
candidate, integration, staging, refinery, polecat, and pack branches are
evidence only. None is an ancestor or automatic source of changes for the
golden baseline.

## Decision

Start from a freshly fetched and pinned commit on the official
`gastownhall/gascity` `main` branch. Use the upstream default `gascity` init
template and a built-in Codex provider. Use the upstream stock bead store:

- bead provider: `bd`;
- storage backend: managed local Dolt;
- authored backend value: empty, whose documented and source-defined default is
  `dolt`;
- DoltLite: disabled unless a later, separately approved capability test opts
  into it;
- MySQL: excluded from the clean-room environment, config, metadata, and
  credentials.

The test must positively assert that the resolved backend is Dolt. Merely
omitting MySQL flags is insufficient.

## Goals

1. Produce an official-source release artifact with verifiable version, commit,
   cleanliness, platform, and checksum metadata.
2. Initialize and operate a stock disposable city without reading or mutating
   the current city, MySQL store, user LaunchAgents, user runtime directories,
   pack cache, or Git worktrees.
3. Exercise a representative stock lifecycle from empty disk through clean
   shutdown, then repeat it from empty disk with the same result.
4. Capture bounded wall time, peak memory, process, filesystem, and artifact
   evidence for each gate.
5. Define the single-branch topology that may be created only after the golden
   run passes.
6. Make every later fork or pack capability earn admission through a failing
   golden-baseline test, owning-layer analysis, and an independently reviewable
   topic change.

## Non-goals

- Preserving behavior merely because it exists in the deployed fork.
- Merging or rebasing any legacy integration, staging, candidate, `temp`, or
  production branch into the clean baseline.
- Reading, copying, migrating, or testing against the production MySQL store.
- Importing the current city configuration or its pinned community packs.
- Repairing the existing refinery before the stock pipeline is proven.
- Deleting branches, worktrees, tags, bundles, databases, binaries, or runtime
  state.
- Deploying the golden artifact during the baseline phase.

## Ground truth

The source commit, not a local remote-tracking branch name, is authoritative.
Before work begins, resolve the official remote `main` with `git ls-remote`,
fetch that exact object, and record its full SHA. The current audit observed
that local `origin/main` and `upstream/main` were stale at different commits
despite naming the same official repository.

The stock backend contract comes from `internal/config/config.go`:

- empty beads provider resolves to `bd`;
- `bd` is Dolt-backed by default;
- empty `beads.backend` resolves to `dolt`;
- `doltlite` is explicit opt-in.

The stock template contract comes from `cmd/gc/cmd_init.go`:

- the non-interactive default init template is `gascity`;
- a non-interactive provider-bearing run uses an explicit built-in provider;
- `--no-start` initializes files and imports without registering or starting
  the city.

## Isolation architecture

Every run receives a newly created root that contains all mutable inputs and
outputs:

```text
clean-room root
├── home/                 isolated HOME
├── gc-home/              isolated GC_HOME
├── xdg-runtime/          isolated XDG_RUNTIME_DIR
├── tmp/                  isolated temporary directory
├── source/               standalone official-source checkout
├── city/                 disposable stock city
├── cache/                isolated Go and Gas City caches
├── shims/                fail-closed host-service shims
├── evidence/             commands, timings, hashes, snapshots, and logs
└── artifacts/            GoReleaser output copied by checksum
```

The root must not be nested inside the current city repository or any existing
Gas City worktree. All paths must be explicit absolute paths. The run may read
the official source remote and language dependencies; it may not read existing
user or production configuration as a fallback.

The one permitted host-derived input is the minimum authentication material
needed by the selected built-in Codex provider. It must be copied or mounted
read-only through an explicit allowlist into the isolated provider home before
the run begins. The manifest records its source class and fingerprint but never
its contents. Provider code must not fall back to the real user home when that
input is missing; readiness fails instead.

On Darwin, the isolated `HOME` redirects `~/Library/LaunchAgents` into the
clean-room root. A fail-closed `launchctl` shim precedes the host binary on
`PATH` for tests that are not explicitly exercising a mocked launchd contract.
The real user LaunchAgents directory is snapshotted before and after each
lifecycle gate and must remain byte-for-byte unchanged.

## Pipeline stages

### 1. Source and environment attestation

- Resolve and pin the live official `main` SHA.
- Create a standalone checkout at that exact SHA.
- Record remote URLs, tool versions, operating system, architecture, compiler,
  SDK, available disk, and resource limits.
- Record absence of MySQL variables, credentials, sockets, and metadata within
  the clean-room root.
- Snapshot protected host paths and relevant process names.

Failure stops the run before dependency download or initialization.

### 2. Repository gates

- Download dependencies with bounded concurrency.
- Run CI-parity checks and fast tests.
- Run documentation checks.
- Run acceptance tiers in their repository-defined dependency order.
- Run integration shards only inside the isolated environment.
- Run tutorial golden tests.
- Run the Darwin regression gate on Darwin.

Each command records start/end time, exit status, maximum resident set size,
and captured stdout/stderr. A timeout is a failure to diagnose, not permission
to increase the timeout automatically.

### 3. Release artifact gate

- Build through the repository's GoReleaser snapshot path.
- Run `scripts/verify-release-binary-metadata.sh` against the produced archive.
- Record archive and binary SHA-256 values, size, Mach-O/ELF identity, signing
  identity when applicable, and `gc version --json --long` output.
- Reject an artifact whose commit differs from the pinned source, whose VCS
  state is dirty, or whose metadata comes from a parent repository.

The Makefile build may be retained as a diagnostic comparison, but only the
metadata-verified GoReleaser artifact advances to the lifecycle gate.

### 4. Stock initialization gate

Using the release artifact, initialize a new city with:

- template `gascity`;
- explicit built-in Codex provider;
- `--no-start` for the initial filesystem/config inspection;
- no `--file`, `--from`, bootstrap profile, hosted Dolt, DoltLite, or MySQL
  settings.

Inspect the generated config and `.beads/metadata.json`. The gate requires:

- the generated template and pack pins match the pinned binary's stock output;
- provider selection is explicit and readiness results are recorded;
- the resolved bead provider is `bd`;
- the resolved storage backend is `dolt`;
- no MySQL fields or credentials exist;
- all generated paths remain below the clean-room root.

### 5. Stock lifecycle gate

Exercise only public stock behavior:

1. start the isolated city using the repository-supported test boundary;
2. wait on an observable readiness condition rather than a fixed sleep;
3. query city and session status;
4. create, read, update, and close a disposable bead;
5. exercise one stock formula or work-dispatch path with a bounded no-op task;
6. restart through the supported graceful path;
7. verify state expected to persist still exists;
8. stop cleanly and verify no owned processes remain.

No existing rig is registered, no current pack is imported, and no external
message, deployment, or branch update is performed.

### 6. Host-integrity and repeatability gate

- Compare protected filesystem and process snapshots.
- Require zero new real LaunchAgents, production processes, worktrees, Git refs,
  city registrations, or files outside the clean-room root.
- Preserve the entire first run root, create a different empty sibling root,
  and repeat stages 4 and 5. Cleanup is a later operator decision and is not
  part of baseline proof.
- Require the same resolved backend, lifecycle outcomes, and artifact identity
  on the second run.

### 7. Golden-baseline publication

After every prior gate passes:

- create an immutable evidence manifest naming the source SHA, artifact hashes,
  stock pack versions, backend, platform, and gate results;
- preserve the artifact and manifest without deploying them;
- create a new golden-baseline tag or ref only with operator approval;
- send the evidence summary for independent review.

Passing this stage proves a functioning stock baseline. It does not prove that
the current production workload or configuration can migrate unchanged.

## Evidence manifest

The manifest is machine-readable and records, for every stage:

- command and working directory;
- explicit environment allowlist with secrets redacted;
- source and binary commits;
- backend and template;
- start/end timestamps and elapsed time;
- exit status, timeout status, and signal;
- peak RSS and swap count where supported;
- output log path and checksum;
- protected-path before/after snapshot checksums;
- created process IDs and their verified termination state;
- produced artifact paths, sizes, and SHA-256 values.

Missing evidence is a failed gate, not a warning.

## Failure handling

- Stop at the first failed boundary and preserve the entire clean-room root.
- Do not retry with legacy code, current config, production data, broader host
  permissions, or a longer timeout.
- Form one falsifiable hypothesis about the owning layer and run the smallest
  isolated probe that distinguishes it.
- A repository test defect is fixed upstream or on an isolated topic branch;
  the golden baseline does not accumulate unrelated workarounds.
- Any mutation outside the clean-room root is a release blocker even if all
  functional tests pass.

## Post-baseline topology

Only after the golden baseline is independently reviewed may a new topology be
created:

```text
official/main @ pinned SHA
        |
        v
golden stock baseline
        |
        +-- one independently tested capability topic at a time
        |
        v
single integration target
        |
        v
immutable RC -> reviewed deployment -> new production tag
```

Legacy refs remain outside this ancestry. A legacy change is admitted only when
the stock baseline fails a required behavior test, source tracing identifies
the owning layer, and the minimal change passes the full affected gates.

Pack and city-configuration changes remain in their owning repositories. Data
backend changes remain behind the bead provider/backend contract. Refinery work
must use an ephemeral scratch ref and may advance the single integration target
only by a verified fast-forward or compare-and-swap operation; it must never
make the target worktree track a source branch.

The detailed post-baseline integration, publication-evidence, and shipped-close
contract is defined in
[Ephemeral Integration and Truthful Landing Contract](2026-08-16-ephemeral-integration-and-landing-contract.md).
Until that contract is implemented and independently qualified, the golden
stock workflow must use `drain_policy=same-session`, `push=false`, and
`open_pr=false`. The stock default `separate` drain is a capability under test,
not an admitted production topology.

## Acceptance criteria

The design is implemented successfully only when all of the following are
true:

1. The source is the freshly resolved official commit, with no legacy branch in
   its ancestry beyond upstream history.
2. The GoReleaser artifact passes release metadata verification.
3. Two clean-room stock lifecycle runs succeed from separate empty roots.
4. Both runs resolve `bd` plus managed local `dolt` and contain no MySQL
   configuration or metadata.
5. Protected host state is unchanged and no owned processes leak.
6. Evidence is complete enough for an independent operator to reproduce the
   result without consulting the broken city.
7. No deployment, production migration, legacy patch replay, branch cleanup, or
   refinery repair occurs as part of the baseline proof.
8. The baseline build uses one coherent same-session implementation worktree;
   no detached implementation commit is treated as integrated or shipped.
