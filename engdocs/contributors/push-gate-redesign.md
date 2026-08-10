# Pre-Push Test Gate Redesign

- **Status:** Proposed (design only — implementation sequencing is the mayor's)
- **Design date:** 2026-08-10
- **Design base:** `integration/deploy-20260804` at `e6920605b`
- **Design bead:** `gas-mnpr` (supersedes `gas-87kf`'s framing)
- **Operator position:** part 1 endorsed 2026-08-09; parts 2-4 open
- **Related:** `gas-2uct` (lock port, committed, not landed), `gas-cebv`
  (Darwin sizing), `gas-4zb` (ambient env), `gas-9nx` (disk preflight)

## Executive verdict

The gate's problem is not that too many agents run it. It is that **one run
is sized to consume the whole host**, and nothing measures whether the host
can afford it.

Both halves are verifiable on the current tree, not just from incident
narrative:

- `.githooks/pre-push` ends in `exec make test-fast-parallel` — a full-repo
  fan-out — with no notion of what the push actually changed.
- `scripts/test-local-job-count` budgets 4 GiB per job and, on Darwin, derives
  its budget from `sysctl hw.memsize`, i.e. **total** RAM (lines 118-127; the
  comment says so outright). Measured on this host at 2026-08-10T19:3xZ under
  load average 48: it returned **11**, unchanged from an idle host. 11 jobs ×
  4 GiB = **44 GiB budgeted against 14.5 GiB actually available**.

That is a constant ~3× over-commit that is *blind to load by construction*.
It explains the incident shape better than agent count does: at load 323, 8 of
18 `go test` drivers traced to a single worktree. Throttling across agents
(`gas-87kf`) would not have prevented it.

The four parts below are ordered by leverage per unit of risk. Parts 3 and 4
are where the incident actually lives; part 1 is the biggest steady-state win
and is already endorsed; part 2 is the one that changes team workflow and
should land last.

## Part 3 first: bound one gate (highest leverage, smallest change)

Do this before anything else. It is the only part that shrinks a *single*
gate's footprint, and every other part is less valuable while one gate can
still take the machine.

**It is not a one-line parallelism change.** Three settings are a single
unit, and tuning one alone is how this has previously produced false reds:

1. **Parallelism** — `-p` (packages built/tested concurrently) and
   `-parallel` (tests within a package).
2. **Timeout** — lowering parallelism *raises* per-package wall clock. Go's
   default is 10m per package, and `cmd/gc` cannot pass bounded-and-unsharded
   inside it (observed: `panic: test timed out after 10m0s` at 601.69s). The
   timeout budget must move with the parallelism.
3. **Sharding** — `CMD_GC_PROCESS_TOTAL=6` exists because `cmd/gc` is too big
   for one job. Sharding is load-bearing, not an optimization, and a bounding
   design that drops it will reintroduce the timeout.

Ambient environment is a fourth, separate trap: `internal/productmetrics`
reads a usage-metrics disable variable that is set in every agent shell and
fails deterministically when inherited (`gas-4zb`). Any canonical gate entry
point must scrub it.

```
WRONG:  go test -p 2 -parallel 2 ./...
RIGHT:  env -u GC_DISABLE_USAGE_METRICS go test -p 2 -parallel 2 -timeout 30m ./...
```

**Design:** make the (parallelism, timeout, shard-count) triple a single named
profile resolved in one place, rather than three independently-overridable
knobs. `scripts/test-local-job-count` is already the shared policy point for
the Makefile and the runner; extend that role rather than adding a second one.
A profile that sets parallelism without setting the matching timeout should be
rejected loudly, not silently accepted.

**Fix the sizing input while here (`gas-cebv`).** On Darwin, read *available*
memory (`vm_stat`: free + inactive, since inactive is reclaimable), not
`hw.memsize`. Linux already reads `MemAvailable`. Until this changes, every
other memory-based decision in this design inherits a 3× error.

## Part 1: scope the gate to the diff (operator-endorsed)

At push time, run the packages the change can affect, not `./...`.

The hook already computes the diff it needs — `.githooks/pre-push:30` runs
`git diff --name-only "$remote_sha" "$local_sha" -- '*.go'` and throws the
result away, keeping only a boolean. Part 1 is: keep the file list, map it to
packages, and pass those to the runner.

**The mapping must include the reverse-dependency closure** of changed
non-test sources, or the gate gives false confidence: editing a widely-imported
package and testing only that package proves very little. `go list` supplies
this without hand-rolled parsing:

Sketch — the shape, not a finished script:

```bash
# 1. changed files -> the packages that contain them
changed_pkgs=$(git diff --name-only "$base" "$head" -- '*.go' \
  | xargs -r -n1 dirname | sort -u \
  | sed 's|^|./|' | xargs -r go list 2>/dev/null | sort -u)

# 2. reverse closure: every package whose transitive deps hit one of those.
#    `{{join .Deps " "}}` already flattens the transitive set, so one pass
#    over `go list ./...` is enough — no recursive walk needed.
go list -f '{{.ImportPath}} {{join .Deps " "}}' ./... \
  | awk -v pkgs="$changed_pkgs" 'BEGIN{split(pkgs,a," ");for(i in a)want[a[i]]=1}
      {for(i=2;i<=NF;i++) if($i in want){print $1;break}}'
```

The gate runs the **union** of step 1 and step 2 — step 2 emits only the
importers, so testing its output alone would skip the very package that
changed.

Test-only changes need no closure (a `_test.go` edit affects its own package
alone); changed non-test sources do. Splitting those two cases is what keeps
the scoped set small in the common case without weakening it in the risky one.

The awk pass above was verified against synthetic `go list` output: given a
change to `example/util`, it selects the two packages that transitively import
it and excludes the one that does not, and emits nothing for a package nobody
imports.

Two cases must stay conservative and fall back to the full suite:

- **Non-Go changes with global blast radius** — `go.mod`/`go.sum`, generated
  code, `Makefile`, `scripts/`, `.githooks/`, testdata goldens. A file-type
  allowlist is the wrong default here; enumerate what is *safe to scope* and
  treat everything else as full.
- **New remote branch.** Today `pre-push:26-27` sets `go_changed=1` whenever
  `remote_sha` is zero — eleven lines *above* the `*.go` test — so a
  **docs-only first push runs the entire Go suite**. This is not hypothetical;
  it is why `gas-2uct` needed a `--no-verify` operator call for a scripts-only
  change, and it blocked a docs-only push again on 2026-08-10 (`gas-9f29`).
  The correct base for a new branch is the merge-base against the integration
  branch, which is cheap and almost always exists. Falling back to "full suite"
  should be reserved for the case where no merge-base can be found at all.

Fixing that one case is arguably the highest-value single edit in this whole
document: it converts the most common bypass request into a non-event.

## Part 2: move the full suite to merge time

Push-time gates protect a branch nobody else consumes. The integration branch
is what actually needs protecting, and the refinery / merge queue is already a
serialization point — so N pushes collapse to 1 full gate by construction, and
the retry-storm dynamic disappears.

Combined with part 1, a polecat still gets fast, relevant feedback at push
time; the exhaustive verdict moves to the point where it is load-bearing.

**This cannot be delegated to GitHub Actions.** Fork PRs have the macOS jobs
label-gated behind `needs-mac`, which a pull-only contributor cannot apply, so
"just run it in CI" silently means "do not run the macOS suite at all."

**Sequencing risk:** landing part 2 before parts 3-4 concentrates *more* work
into the merge queue while a single gate can still saturate the host. Land it
last.

## Part 4: host admission control

### Most of this is already written — land `gas-2uct` first

`gas-2uct` ports upstream's `scripts/push-gate-lock-lib.sh` (345 lines, self-
tests 51/0). It is committed at `1808471cd`, published on `ajb`, and **not on
the lane** (`gas-1f3u`). It already provides, verified by reading the port:

- a cross-invocation bound via numbered `flock` slot files;
- crash-safe release — normal exit, test failure and crash all free the slot,
  with no PID-liveness probing;
- a **bounded** wait that polls and prints a diagnostic naming current holders
  the moment it starts waiting;
- `exit 75` (`EX_TEMPFAIL`), so exhaustion is distinguishable from a real test
  failure;
- FD inheritance deliberately severed at the fan-out boundary, so a leaked
  daemon (tmux, `dolt sql-server`, an escaped `gc`) cannot pin a slot — plus
  `push_gate_describe_slots` to identify that case when it happens anyway.

Do not design a semaphore from scratch. Land the port, then close the gaps
below. Note that landing it does *not* shrink any single gate — job count
measured 11 before and after — so `gas-cebv` and part 3 stand regardless.

### Gap 1: admission must be judged on memory headroom, not slot count

The ported lock bounds *how many* gates run. It does not know whether the host
can afford even one. Memory is the binding constraint: at peak the host showed
46 GB used / 38 MB free with ~17 GB swap and 80-87% **system** time — kernel
thrash, not compute. A CPU- or count-based rule admits straight into that; at
the moment one agent was blocked the box was 50% idle on CPU with 2.0 GB free
RAM and swap 91% full.

Admission should compare *available* memory against a measured per-gate
requirement (single-gate peak summed RSS measured at ~2.3 GB) plus headroom,
and refuse — with the number in the message — when it does not fit. The
existing disk preflight (`pre-push:95-119`) is the idiom to copy: an explicit
floor, an env override, a documented bypass, and an explicit **fail-open**
contract when the probe cannot answer.

### Gap 2: FIFO, because `flock` is not fair

`flock` wakeups are unordered, so the ported lock can starve an early arrival
in favor of a luckier poller. Requirement: an explicit queue with arrival
order, not a race to observe idleness.

This is the lesson from the interim rule that shipped by hand: *"wait until the
toolchain count reads 0 twice, 60s apart."* During one 40-minute bounded run
the count never reached 0, so two healthy, authorized gates spun indefinitely.
**Wait-for-idle is not an admission policy; it is a spin with no fairness and
no bound.**

### Gap 3: a bound on waiting, then escalate

The port has a wait bound and maps it to `exit 75`. What it lacks is the
policy above it: after N minutes (interim N=45 set by hand) the agent must
escalate rather than continue waiting, and the escalation must be actionable.

### Gap 4: the queue must be observable

A starved agent and a dead agent must be distinguishable **without peeking at
a terminal**. In the incident a monitor twice reported a starved canary as
possibly-failed; the cost was investigation time on top of lost throughput.

This is the lease bug (`gas-9d4e`) in a different dress: *a state that means
"waiting" must be readable as "waiting", or the system reports it as failure.*
Whatever the queue is, it needs a read-only view naming who holds slots, who
is waiting, and since when.

### A note on clearance checks

Any "is the host clear?" probe must count the memory holders. The short form
undercounts roughly 2×:

```bash
# blind to compile/link/vet — the processes that actually hold the memory
ps -eo comm= | grep -cxE 'go|gc.test'
# correct
ps -eo comm= | grep -cxE 'go|gc.test|compile|link|vet|asm|cgo'
```

And a count of 0 is still not clearance — gate on memory headroom (gap 1).

## Recommended sequencing

1. **Part 3 + `gas-cebv` sizing fix.** Shrinks one gate; unblocks honest
   measurement for everything else.
2. **Part 1, starting with the new-branch merge-base case.** Largest
   steady-state reduction; removes the most common `--no-verify` request.
3. **Part 4: land `gas-2uct`, then gaps 1-4** (memory headroom first — it is
   the one that prevents thrash; FIFO and observability follow).
4. **Part 2 last**, once a single gate is bounded and admission is real.

## Open questions for the mayor

1. **Does a scoped gate satisfy the merge queue, or only the polecat?** If the
   refinery re-runs the full suite anyway (part 2), part 1's scoping is purely
   a latency win and can be aggressive. If not, the reverse-dependency closure
   is load-bearing for correctness and should be conservative.
2. **Where does admission live** — in `.githooks/pre-push` only, or around
   every heavy `make` target? The lock's own docs note the caller may be a
   direct `make` invocation, not just a push. Hook-only admission leaves the
   documented hole.
3. **What is the escalation target** for gap 3 — mail to mayor, a bead, or a
   refusal the agent reports upward? This determines whether the bound is
   enforceable or advisory.
