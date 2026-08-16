---
title: "gas-ftv4 Structure and Ownership Map"
---

> Read-only incident map captured on 2026-08-16 for `gas-ftv4`. This document
> separates the installed runtime, the local integration/refinery lane, city
> packs, and the upstream release path so subsequent work changes the owning
> layer instead of treating the stack as one system.

## Safety boundary

This trace did not deploy, merge, restart services, register LaunchAgents, or
query the data plane. It inspected Git refs/config, pack definitions, bead
metadata, process/session state, build metadata, and bounded build/test output.
Secrets in MySQL metadata were not recorded.

## System map

```text
shell `gc`
  -> ~/.local/bin/gc (PATH-selection wrapper)
  -> ~/.local/gc-bin/gc @ f28bf5659, CGO_ENABLED=1
  -> ~/.local/gc-bin/bd
  -> MySQL-backed city and rig bead stores

city.toml imports + packs.lock
  -> cached gascity-packs @ 5c1d0ac9
  -> resolved scheduled orders (currently two project-lead patrols)

work bead
  -> polecat source branch
  -> refinery worktree (`temp` currently tracks one polecat branch)
  -> configured target `staging/gascity-lane`
  -> candidate / RC workflow
  -> release artifact and metadata verification
```

The first path is the installed runtime. The second is city configuration.
The third is integration and release. A successful clean upstream build proves
only the build stage; it does not prove that the fork compatibility layer or
the current refinery state is safe to deploy.

## Evidence snapshot

### Installed runtime and clean rebuild

| Item | Observed evidence |
| --- | --- |
| Shell entry point | `~/.local/bin/gc`, a wrapper that prepends `~/.local/gc-bin` |
| Installed executable | `~/.local/gc-bin/gc`, Mach-O arm64, 258,152,176 bytes |
| Installed identity | commit `f28bf5659`, build date `2026-08-14T14:09:16Z`, `CGO_ENABLED=1` |
| Installed `bd` | wrapper-selected `~/.local/gc-bin/bd`, version `1.1.0 (dev)` |
| Store declaration | city and rig `.beads/metadata.json` both declare `backend: mysql` |
| Clean rebuild source | upstream `ab54f93c3b3d30bb8f52f7e92c0761fb75340752` |
| Bounded dependency step | `go mod download`: exit 0, 0.34 s, 24,854,528-byte max RSS, zero swaps |
| Bounded build | `CGO_ENABLED=0 GOMAXPROCS=2 GOFLAGS=-p=2 make build`: exit 0, 20.16 s, 1,806,385,152-byte max RSS, zero swaps |
| Rebuilt artifact | `bin/gc`, 166,260,992 bytes, SHA-256 `e20ced2e63f68503260cca32cf43af6b127aaee970bac610175f5d2369d80712` |
| Rebuilt identity | commit `ab54f93c3`, version `dev`; provenance/verifier contract tests passed |

Production and upstream pin the same relevant storage dependencies, but the
production-to-upstream diff contains 25 storage-surface files and roughly
1,244 additions / 75 deletions, including `cmd/gc/bd_env.go`, provider wiring,
MySQL commands/tests, and `internal/beads/contract` changes. The compatibility
owner is therefore the Gas City fork integration layer, not a changed Dolt,
go-mysql-server, Vitess, or driver version.

### Ref and promotion topology

| Ref | Commit / relationship |
| --- | --- |
| Preserved production | `rebuild-source-production-20260815-f28bf5659` -> `f28bf5659` |
| Integration | `integration/deploy-20260815` -> `4c85ec92`; production +69 commits |
| Default staging | `staging/gascity-lane` -> `fe97694cc`; production +65 commits |
| Refinery scratch | `temp` -> `bd644377a`; staging +1 commit |
| Candidate | `rebuild-source-candidate-20260815` -> `5ae2f217`; production +83 commits |
| Clean upstream | `upstream/main` -> `ab54f93c3`; diverged from production (142 production-only, 77 upstream-only commits) |

The refs are not a linear promotion chain:

- Integration and staging diverge after `7432620ce`; neither contains the
  other.
- Staging is an ancestor of both `temp` and candidate.
- `temp` is not an ancestor of candidate; each has unique commits.
- Candidate and clean upstream also diverge.
- The live refinery worktree is on `temp`, whose Git config sets remote `ajb`
  and merge ref `refs/heads/polecat/gas-8bqa`. It is currently ahead 3 and
  behind 1 relative to that worker branch. The apparently duplicated Dolt
  change exists under different commit IDs (`bd644377a` locally and
  `5966ba2b3` remotely).

This makes `temp` a mutable scratch/worker branch, not a stable promotion or
deployment target.

### Refinery ownership and active block

The refinery role is defined by the city pack, not hardcoded into the Go
runtime:

- `packs/gastown/agents/refinery/agent.toml` owns worktree/provider/session
  setup.
- `packs/gastown/formulas/mol-refinery-patrol.toml` owns rebase, quality gates,
  merge/push, verified landing, and escalation policy.
- The formula's configured default target is `main`, while the city rig's
  effective default target is `staging/gascity-lane`.
- The live refinery is handling `gas-8bqa`, whose metadata targets
  `staging/gascity-lane`.

`gas-8bqa` is explicitly held after its integration gate registered real
`com.gascity.supervisor.gc-home-*` LaunchAgents in the human user domain and
generated notifications. Its containment dependency is `gas-li1k`. The
refinery correctly stopped that run and left the work unmerged. Until the
containment is reviewed and the branch/tracking topology is repaired or
validated, the refinery lane is not a safe deploy path.

### Pack and prompt ownership

| Concern | Owning layer | Evidence |
| --- | --- | --- |
| Duplicate project-lead patrols (`#267`) | gascity-packs order plus city import resolution | `city.toml` imports the same oversight pack for `kit` and `python419`; `packs.lock` pins `5c1d0ac9`; `gc order list --json` resolves two enabled `patrol-project-leads` orders with null scope |
| Boot deterministic health probe (`#219`) | local city pack | `packs/gastown/agents/boot/prompt.template.md` performs session/bead/mail inspection and then relies on model judgment; no deterministic healthy/idle probe exists |
| Status-line concurrency (`#207`) | local city pack | `packs/gastown/assets/scripts/status-line.sh` has a portable bounded process-group runner and a 30-second cache, but no cold-cache single-flight or stale-serve lock; current tests cover bounds and cleanup only |
| Build provenance | Gas City Makefile and release workflows | `make build` injects version/commit/date with `-buildvcs=false`; RC/stable workflows use GoReleaser and metadata verification |
| MySQL compatibility | Gas City fork integration surface | Dependency pins match upstream while command/provider/contract files differ materially |

## Falsifiable next probes

1. **Order scoping:** in an isolated city config, make the project-lead patrol
   city-scoped and assert that the current count of two resolved orders becomes
   one without losing both rigs' coverage.
2. **Boot health:** add a contract test for a deterministic probe that exits
   healthy/idle before invoking model judgment; the current prompt should fail
   that test.
3. **Status-line single-flight:** add a concurrent cold-cache test that counts
   expensive `gc hook` / mail calls; the current script should duplicate work,
   and the fix should preserve its existing two-second bound and process cleanup.
4. **Fork compatibility:** construct a file/patch/dependency matrix from
   `f28bf5659` to `ab54f93c3` and select the minimum MySQL/runtime compatibility
   stack required on top of clean upstream. Build success alone is not the
   selection rule.
5. **Refinery containment:** prove `gas-li1k` prevents registration in the real
   user LaunchAgent domain, then repair or deliberately replace `temp`'s
   polecat tracking before re-enabling an integration gate.

## Safe sequence

1. Implement and test the three pack-owned fixes in an isolated,
   mechanic-owned city-pack worktree.
2. Produce the minimal fork compatibility series on top of clean upstream,
   preserving MySQL backend contracts and production CLI behavior.
3. Run bounded unit/integration checks in an isolated environment and build an
   RC artifact through the metadata-verifying release path.
4. Keep the current refinery path out of deployment until LaunchAgent
   containment and branch ownership are independently verified.

No evidence in this trace assigns the current duplication, boot judgment, or
status-line cache problems to Dolt, go-mysql-server, Vitess, or the MySQL data
plane. Escalate into those layers only if a measured probe crosses that
boundary.
