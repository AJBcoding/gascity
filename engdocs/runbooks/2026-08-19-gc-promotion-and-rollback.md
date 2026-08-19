# Runbook: promoting and rolling back the `gc` binary

Status: FIRST WRITTEN DOWN 2026-08-19 (gas-w1t9). Nothing in it has been
executed. Every fact below was measured on the deployment it describes; the
provenance of each is given so a reader can re-verify rather than trust.

This exists because the promotion path was tribal knowledge. The 2026-08-15
safety-train plan listed "present exact promotion and rollback commands" as a
task and no such artifact was ever committed, so a 258 MB binary swap on a live
supervised city was one improvisation away from happening from memory.

## Who this is for and what it does not do

This documents ONE deployment: the Darwin/arm64 host that runs the `anthony`
city under a launchd user agent. Host-specific values are written as literals
because a runbook full of placeholders is a runbook nobody can follow, but they
are marked `[THIS DEPLOYMENT]` so a different host adapts rather than copies.

It does NOT cover: which branch should be promoted, the merge or lineage
decision, or unfreezing the refinery merge queue. It starts from "a commit has
been chosen and gated" and ends at "it is live or it has been rolled back".

## Authorization markers

- `[AUTH]` — requires explicit operator authorization in the current session.
  Per the 2026-08-15 plan: "No live binary swap, supervisor restart, or live
  pack refresh without explicit user approval at the promotion gate."
- Everything unmarked is read-only or writes only to a scratch/backup path.

---

## 0. The topology, and how to re-verify it

| Thing | Value `[THIS DEPLOYMENT]` | Verify with |
| --- | --- | --- |
| PATH entry point | `~/.local/bin/gc` — a 154-byte **shim script**, not a binary | `file ~/.local/bin/gc; cat ~/.local/bin/gc` |
| Real executable | `~/.local/gc-bin/gc` | `plutil -p ~/Library/LaunchAgents/com.gascity.supervisor.plist` |
| Supervisor service | `gui/501/com.gascity.supervisor` | `launchctl print gui/$(id -u)/com.gascity.supervisor` |
| Supervisor argv | `~/.local/gc-bin/gc supervisor run` | same |
| KeepAlive | `{Crashed: true, SuccessfulExit: false}` | `plutil -p …/com.gascity.supervisor.plist` |

The shim is load-bearing:

```sh
#!/bin/sh
# Isolation wrapper: gas city (zpriddy fork) + its MySQL-capable bd.
export PATH="$HOME/.local/gc-bin:$PATH"
exec "$HOME/.local/gc-bin/gc" "$@"
```

It exists so every `gc` invocation resolves the MySQL-capable `bd` that lives
beside the binary in `~/.local/gc-bin`. Anything that replaces the shim changes
which `bd` the city talks to.

## 1. DO NOT RUN `make install`

`make install` is wrong for this deployment and will break it in two ways at
once. This is the single most important line in this document.

Verified by reading the target and by `make -pn`:

- `INSTALL_DIR` resolves to `$(go env GOPATH)/bin` = `/Users/anthonybyrnes/go/bin`,
  **not** `~/.local/gc-bin`. So the new binary lands somewhere the supervisor
  does not read, and the supervisor keeps running the old image indefinitely —
  a permanent split brain between CLI and supervisor rather than a bounded
  mixed-version window.
- Because `INSTALL_DIR != $HOME/.local/bin`, the target then runs
  `rm -f "$HOME/.local/bin/gc"` followed by `ln -sf "$INSTALL_DIR/gc" "$HOME/.local/bin/gc"`.
  That **deletes the isolation shim** and replaces it with a symlink, silently
  dropping the `PATH` export that puts the MySQL-capable `bd` first.

Promotion on this host is a deliberate copy into `~/.local/gc-bin/gc`, described
in step 5. `make build` is fine and is used below; only `make install` is banned.

## 2. Reproduce the production build configuration

The live binary is not a default build. Read its own build settings:

```sh
go version -m ~/.local/gc-bin/gc | head -3
```

Measured on the current live binary (commit `f28bf5659`):

```
go1.26.5
build -ldflags="-X main.version=dev -X main.commit=f28bf5659 -X main.date=2026-08-14T14:09:16Z"
build CGO_ENABLED=1
build CGO_CFLAGS=-I/opt/homebrew/opt/icu4c@78/include
build GOARCH=arm64  GOOS=darwin
```

So production is a **CGO-enabled** build with the Homebrew ICU headers on the
include path. This matters more than it looks:

- A plain `make build` with default CGO fails on this host — `go-icu-regex`'s
  `internal/icu/file.cpp` cannot find `unicode/regex.h` (measured 2026-08-16,
  failed after ~14.5s). That failure is what pushes people toward
  `CGO_ENABLED=0`.
- `CGO_ENABLED=0` succeeds and produces a **different binary** — 166,260,992
  bytes versus the live 258,152,176. It is not a drop-in substitute for a
  CGO-linked production build, and promoting one would change `go-icu-regex`
  behaviour without any test saying so.

Confirm the ICU headers are present before building:

```sh
ls /opt/homebrew/opt/icu4c@78/include/unicode/regex.h
```

## 3. Preflight

Run all of these and stop on the first surprise.

```sh
# a. The tree you are promoting is the tree you gated, and it is clean.
git -C <build-worktree> rev-parse HEAD
git -C <build-worktree> status --porcelain          # must be empty

# b. Record what is live right now, from the binary itself, not from memory.
gc version --json --long
shasum -a 256 ~/.local/gc-bin/gc

# c. Free space. The binary is ~258 MB and you are about to keep two copies.
df -h ~/.local/gc-bin

# d. The supervisor is in the state you think it is.
launchctl print gui/$(id -u)/com.gascity.supervisor | head -20
```

Record (b) somewhere durable before touching anything: it is the rollback
target, and a rollback plan that depends on remembering a SHA is not a plan.

## 4. Build

Bounded, from the worktree holding the chosen commit. Bounded concurrency is
not decoration: an unbounded build on this host competes with a live city.

```sh
cd <build-worktree>
env CGO_ENABLED=1 \
    CGO_CFLAGS=-I/opt/homebrew/opt/icu4c@78/include \
    GOMAXPROCS=2 GOFLAGS=-p=2 \
    /usr/bin/time -l make build
```

`make build` already does the right things: `-buildvcs=false` (so the binary is
not stamped with a nested worktree's parent commit — see the invalid 159 MB
artifact preserved from 2026-08-16), version/commit/date `ldflags`, and on
Darwin `scripts/sign-darwin-local.sh`.

Then verify the artifact BEFORE it goes anywhere near `gc-bin`:

```sh
./bin/gc version --json --long        # commit must equal the SHA from 3(a)
file ./bin/gc                         # Mach-O arm64
go version -m ./bin/gc | head -6      # CGO_ENABLED=1, ICU CFLAGS present
shasum -a 256 ./bin/gc
```

If the reported commit does not match what you built, stop. That exact
mismatch — VCS metadata naming a different commit — is a known failure mode on
this host and is why `-buildvcs=false` is in the Makefile.

## 5. Pre-swap backup, then install `[AUTH]`

Follow the convention already present in `~/.local/gc-bin` (there are eight
such backups): `gc.bak-pre-<reason>-<timestamp>`.

```sh
# Backup FIRST, and verify it before overwriting anything.
STAMP=$(date +%Y%m%d-%H%M)
cp -f ~/.local/gc-bin/gc ~/.local/gc-bin/gc.bak-pre-<reason>-$STAMP
shasum -a 256 ~/.local/gc-bin/gc ~/.local/gc-bin/gc.bak-pre-<reason>-$STAMP
# The two hashes MUST match. If they do not, stop: the copy is bad.
```

Install atomically. Write the temp file **inside** `~/.local/gc-bin` so the
`mv` is a same-filesystem rename and no reader ever sees a partial binary:

```sh
tmp=~/.local/gc-bin/.gc.tmp.$$
cp -f <build-worktree>/bin/gc "$tmp"
chmod 0755 "$tmp"
mv -f "$tmp" ~/.local/gc-bin/gc          # atomic rename
shasum -a 256 ~/.local/gc-bin/gc         # must equal the artifact hash from step 4
```

Do not delete or touch `~/.local/bin/gc`. The shim stays exactly as it is.

## 6. The mixed-version window — read before step 7

The moment step 5's `mv` completes, the deployment is running **two versions at
once**, and it stays that way until the supervisor is restarted:

- **Every new `gc` invocation** — your shell, every hook, every agent command,
  every formula step that shells out to `gc` — immediately executes the NEW
  binary.
- **The supervisor process** (`gc supervisor run`, PID from `launchctl print`)
  keeps the OLD binary's image mapped, because a running process does not
  change when its file is replaced. It runs the old code until restarted.

So during this window, new-version CLI writes are being read by an old-version
supervisor. That is only safe if the two versions agree about on-disk state.
For a train that changes state formats — the shipped-close work adds a typed
`delivery.landed` event and changes close-gate enforcement — **this window is
exactly where a compatibility defect would show up**, and it is why the
supervisor restart is a separately authorized action rather than part of the
swap.

Keep the window short and deliberate. Do not swap and walk away.

## 7. Restart the supervisor `[AUTH]`

```sh
launchctl kickstart -k gui/$(id -u)/com.gascity.supervisor
```

`-k` kills the running instance and starts a fresh one, which is deterministic.
Prefer it over `kill`: `KeepAlive` here is `{Crashed: true, SuccessfulExit:
false}`, so a clean exit is NOT auto-restarted and a plain kill leaves the
outcome depending on how the process happened to exit.

`launchctl bootout` + `bootstrap` also works but unloads and reloads the job
definition; use it only if the plist itself changed.

Verify:

```sh
launchctl print gui/$(id -u)/com.gascity.supervisor | grep -E "state|pid"
tail -50 ~/.gc/supervisor.log
gc version --json --long
```

The PID must be new and the log must show a clean start, not a crash loop.

## 8. Deploy record

`deploy-record` appears in `city.toml` and `mol-refinery-patrol` as the third
element of crew's bundled "build + deploy + deploy-record" step, but no command
implements it and no artifact format is defined — there is no `gc deploy`
subcommand. **This is a proposal, not documentation of an existing practice.**

Record, at minimum, in the promotion bead: the promoted commit SHA, the
artifact's sha256, the build settings from `go version -m`, the path of the
pre-swap backup, the timestamp of the swap, the timestamp of the supervisor
restart, and who authorized each `[AUTH]` step. Without the backup path and the
prior sha256 written down, the rollback in step 9 depends on someone's memory.

## 9. Rollback

### 9a. Binary rollback `[AUTH]`

Two valid sources, in preference order:

1. The pre-swap backup from step 5 — always the freshest correct target.
2. The preserved artifact from the 2026-08-15 train:
   `.gc/agents/mayor/deploy-20260815/rollback/gc-live-f28bf5659`, recorded
   sha256 `f0a3444a1f34f4b70d9f496c1052456e4c3246cd657a99f650d1e18f90fc11c7`.
   Verified 2026-08-19: the file still matches that hash **and** matches the
   currently-live binary byte for byte.

```sh
shasum -a 256 <rollback-source>          # verify BEFORE restoring
tmp=~/.local/gc-bin/.gc.tmp.$$
cp -f <rollback-source> "$tmp"
chmod 0755 "$tmp"
mv -f "$tmp" ~/.local/gc-bin/gc
shasum -a 256 ~/.local/gc-bin/gc         # must equal the rollback source
launchctl kickstart -k gui/$(id -u)/com.gascity.supervisor
gc version --json --long                 # must report the old commit
```

### 9b. The plist is a SEPARATE decision

A binary rollback does not require a plist rollback, and the two should not be
bundled reflexively. The preserved
`.gc/agents/mayor/deploy-20260815/rollback/com.gascity.supervisor.plist`
(2026-08-14) was diffed against the live plist on 2026-08-19: the only
difference is one `PATH` entry — the preserved copy contains a
`cc-marketplace/safety-net/.../bin` path the live one no longer has.
`ProgramArguments`, `RunAtLoad`, `KeepAlive` and the log paths are identical.

Restore the plist only if the plist is implicated in the failure. Diff first:

```sh
diff <(plutil -p <preserved-plist>) <(plutil -p ~/Library/LaunchAgents/com.gascity.supervisor.plist)
```

### 9c. What a binary rollback does NOT undo

**A binary rollback is not a state rollback.** If the newer `gc` wrote state the
older one cannot read — event-journal rows, bead metadata, a typed
`delivery.landed` event, close-gate telemetry — restoring the executable leaves
that state in place for an older binary to misread.

Nobody should claim rollback safety for a state-changing train until an
old-binary compatibility gate has been run. See gas-w1t9's sibling scoping work.
Until then, treat 9a as "restores the code" and NOT as "restores the city".

## 10. Known drift to resolve before first use

`city.toml` states that the rig root keeps `integration/deploy-20260804` checked
out, because that is the worktree where the deploy lane's deliberate
fast-forward happens (gas-gg4). As of 2026-08-19 the rig root is on
`fix/gas-f0uf-wisp-prefix-agnostic` instead. This is the same drift that made
the gas-4pz pre-push hook correctly refuse a push from that tree. Restoring the
documented state is assessed separately; do not run a deploy-lane fast-forward
until it is resolved.

Also note: the live binary was built with go1.26.5, while the 2026-08-19 gate
ran under a go1.26.6 toolchain. Not known to be a problem, recorded so that a
promotion does not silently change the toolchain without anyone noticing.
