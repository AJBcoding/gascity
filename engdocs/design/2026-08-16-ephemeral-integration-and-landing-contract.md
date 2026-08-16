# Ephemeral Integration and Truthful Landing Contract

## Status

Approved for mechanic implementation planning; proposed for upstream adoption.
Pending written-spec review.

This design replaces the useful delivery guarantees of a persistent refinery
with a bounded integration workflow, an evidence-producing publication
boundary, and a universal close invariant. It complements the
[clean-room stock pipeline design](2026-08-16-clean-room-stock-pipeline-design.md).
The clean-room golden baseline remains the prerequisite for implementation and
deployment of this design.

## Problem

The stock `gascity` build contract supports two implementation drain policies:

- `same-session`, where one shared execution context processes items in order;
- `separate`, the default, where items run in isolated detached worktrees.

In the separate policy, workers create focused commits and implementation
summaries in their source-anchor worktrees. The current pack then summarizes
those artifacts, reviews, finalizes, and optionally publishes. It does not
declare a stage that assembles the detached commits into one reviewable branch.
The publisher can push or open a pull request, but it does not collect the
worker commits first.

This creates two distinct truth gaps:

1. a workflow can report implementation success while its commits remain in
   disconnected worktrees;
2. a work bead can be closed as shipped without exact evidence that the
   corresponding result reached the declared remote target.

A persistent refinery does not repair the invariant. Historical audit found
that most real closes bypassed refinery formulas, and a role implemented by a
long-running model session introduced its own queue, identity, prompt-drift,
worktree-ownership, and publication failure modes.

## Decision

Refinery becomes a protocol rather than a persistent actor.

The delivery path is:

```text
implementation drain
  -> durable integration manifest
  -> ephemeral integration attempt
  -> review and fix on the assembled candidate
  -> verified publication
  -> typed landed evidence
  -> universal shipped-close validation
```

Gas City never hardcodes a refinery or other role name. The `gascity` pack
declares an on-demand integration role and formulas. Core supplies only generic
formula, event, store, and routing mechanisms. No standing named session or
scheduled integration patrol is required.

The first clean-room baseline explicitly uses `drain_policy=same-session`,
`push=false`, and `open_pr=false`. Separate drain is not qualified for
production use until the integration and landing contract passes its own
gates.

## Scope and ownership

The work is split into independently reviewable repository changes.

| Capability | Owning repository | Responsibility |
| --- | --- | --- |
| Integration graph and prompts | `gastownhall/gascity-packs` | Extend the `gascity` build contract, collect source commits, assemble a candidate, route exceptions, and produce integration artifacts. |
| Typed landing observation | `gastownhall/gascity` | Register and emit typed landing events without introducing a hardcoded role. |
| Universal close validation | `gastownhall/beads` | Reject a shipped close that lacks valid, exact landing evidence while exempting structural and ephemeral records. |
| Clean-room qualification | mechanic-owned clean upstream branch | Prove the stock and enhanced paths without production state, MySQL, Gastown, or legacy Git ancestry. |

No repository may silently compensate for a missing invariant in another
repository. In particular, a pack prompt cannot stand in for close validation,
and a retrospective branch scanner cannot stand in for publication evidence.

## Delivery state model

The existing bead status remains `open`, `in_progress`, `blocked`, or `closed`.
Delivery progress is recorded separately in `gc.delivery_state` so this change
does not require a new global status enum.

```text
implementing
  -> integration_ready
  -> integrating
  -> review_ready
  -> publication_pending
  -> landed
  -> closed
```

Failure transitions are explicit:

```text
integration conflict or combined-test failure
  -> needs_rework -> integration_ready

external decision required
  -> blocked -> integration_ready

publication race or verification failure
  -> publication_pending with failure evidence
```

`closed` is never an intermediate delivery state. A worker finishes by
recording `integration_ready`; it does not close shipped work. Non-delivery
outcomes remain possible as `no-op`, `blocked`, or `abandoned`, each with its
own reason contract.

## Worker submission contract

Each implementation source anchor records the following before entering
`integration_ready`:

- source bead ID;
- workflow and convoy IDs;
- absolute source worktree path;
- source base SHA;
- source result SHA;
- ordered dependency bead IDs;
- changed paths;
- focused and final verification evidence;
- implementation-summary artifact path and hash.

The source worktree must be an existing worktree for the expected repository.
The result SHA must be a commit reachable from that worktree and must descend
from the recorded base. A dirty worktree, missing commit, launcher-checkout
path, or base mismatch fails submission.

Source worktrees and their commits remain preserved until verified landing.
Cleanup is a later, separately recorded operation.

## Integration manifest

After the implementation drain, a controller-routed stage produces a
`gc.build.integration-manifest.v1` artifact. It contains:

- workflow, convoy, repository, and target identities;
- authoritative remote and target ref;
- observed target SHA used as the integration base;
- every source record and summary hash;
- a deterministic topological order derived from bead dependencies;
- overlap analysis for changed paths;
- the verification policy to run on the assembled candidate;
- manifest creation time and hash.

The stage fails if a drain member is missing required evidence, if the
dependency graph is cyclic, or if two supposedly independent items have an
unresolved ordering dependency. Lexical bead ID ordering is used only as a
stable tie-breaker among genuinely independent items.

The manifest is immutable for one integration attempt. A changed source
commit or target SHA creates a new attempt and a new manifest hash.

## Ephemeral integration attempt

The `gascity` pack adds an `integration-base` virtual contract and a concrete
`integrate` formula. Build factories inherit a new `integrate` stage between
implementation drain and implementation summarization. Methodology packs may
add validation, but may not bypass the contract.

An attempt performs these operations:

1. Fetch the explicitly configured remote target.
2. Require the fetched target SHA to equal the manifest base SHA. A moved
   target produces a new attempt rather than silently rebasing the old one.
3. Create a detached scratch worktree below the workflow artifact root from
   that exact base SHA. Never check out or move the target branch itself.
4. Verify every source SHA and summary hash from the manifest.
5. Cherry-pick source commits in deterministic dependency order.
6. Abort immediately on a conflict. Preserve conflict paths and the last clean
   candidate SHA as evidence; do not let the integration transaction improvise
   a resolution.
7. Run the manifest's combined verification commands against the assembled
   tree.
8. Record the candidate SHA, tree hash, source-to-integrated commit map,
   verification results, and scratch worktree in a
   `gc.build.integration-result.v1` artifact.

The scratch ref is namespaced by workflow and attempt, for example
`gc/integration/<workflow-id>/<attempt>`. It is not a promotion ref and is
never a source branch for the target worktree.

The integration role is providerless and on-demand. It drains after the
attempt. A conflict or combined-test failure creates explicit rework routed to
an implementation or exception lane; it does not keep an always-on refinery
alive.

## Review and fix contract

Review begins only after a clean integration result exists. Reviewers receive
the candidate worktree and candidate SHA as their source of truth. Per-worker
summaries remain trace evidence, but reviewing summaries alone cannot approve
the build.

Any review fix is made against the candidate in a single integration-fix lane.
The fix produces a new candidate SHA and reruns the affected combined checks.
The final report names the exact approved candidate SHA.

For `same-session`, the integration stage still emits a result artifact. It
verifies that all drain items refer to the same coherent worktree and records
its candidate SHA; it performs no cherry-picks. This keeps review and publish
inputs identical across both drain policies.

## Publication contract

Publisher consumes the approved integration result, never an implicit current
checkout. Publication is disabled unless a positive `push` or `open_pr`
authorization is present.

### Direct fast-forward mode

Direct publication:

1. fetches the declared remote target;
2. requires the remote SHA to equal the integration result's expected base;
3. requires that base to be an ancestor of the approved candidate;
4. pushes the candidate to the exact target with an expected-object lease;
5. reads the remote ref again with `ls-remote`;
6. succeeds only when the observed remote SHA equals the approved candidate.

Target movement, a non-fast-forward candidate, rejection by branch protection,
or remote verification mismatch leaves the workflow in
`publication_pending`. It never retries with force, a different remote, or a
broader credential.

### Pull-request mode

PR mode pushes an immutable candidate ref and opens a PR against the declared
target. Opening a PR is `published`, not `landed`. Work remains open until a
trusted adapter observes the merged remote result and records the actual
landed SHA. Squash and server-generated merge commits are supported because
the landing stamp records the post-merge SHA rather than trying to infer it
from the source commit graph.

## Typed landing evidence

Gas City adds a registered typed event for a verified landing. The payload is
bounded and contains:

- workflow and integration attempt IDs;
- repository identity;
- remote and target ref;
- expected pre-publication target SHA;
- approved candidate SHA;
- observed landed SHA;
- publication mode;
- integration-result artifact path and hash;
- included work bead IDs;
- verification timestamp.

Large source-to-integrated maps remain in the hashed integration artifact, not
the event payload. One event may cover a bounded build; every included work
bead records the event ID and artifact hash.

The event is emitted only after observing the authoritative remote. A push
attempt, local ref move, PR creation, agent assertion, or branch-name match is
not a landing event.

## Work-record stamping

After landing verification, the publication boundary stamps each included work
bead with:

```text
gc.work_outcome=shipped
gc.source_commit=<worker result SHA>
gc.work_commit=<worker result SHA, preserving the existing work-record meaning>
gc.integrated_commit=<candidate commit corresponding to this source record>
gc.integration_candidate=<approved candidate SHA>
gc.integration_result_path=<absolute artifact path>
gc.integration_result_hash=sha256:<digest>
gc.landed_sha=<observed remote SHA>
gc.landed_remote=<remote identity>
gc.landed_target=refs/heads/<target>
gc.landed_event_id=<typed event ID>
gc.delivery_state=landed
```

`gc.work_commit` is not repurposed: it remains the focused worker result.
`gc.integrated_commit` is the source record's corresponding commit in the
assembled candidate, while `gc.integration_candidate` names the approved tip.
For a direct fast-forward, `gc.integration_candidate` and `gc.landed_sha` are
the same SHA. For a squash or server-generated merge, `gc.landed_sha` is the
observed post-merge SHA and `gc.integration_candidate` preserves the reviewed
input. The hashed integration result maintains the source-to-candidate trace.

The narrow guarantee is deliberate: the recorded landed SHA exists at the
declared remote target and the work record is included in the referenced
integration result. It does not claim that the issue's prose is semantically
complete.

## Universal close invariant

The blocking invariant belongs in the bead close path because human, agent,
formula, API, and proxied-server closes must share it.

For a real work bead with `gc.work_outcome=shipped`, close requires:

- all landing metadata above;
- a valid integration-result artifact hash;
- inclusion of the bead ID in that artifact;
- the declared landed SHA currently observed at the declared remote target;
- a matching typed landing event ID recorded by the publication boundary.

Validation fetches or reads the authoritative remote before testing the SHA; a
stale local remote-tracking ref cannot satisfy the gate.

Structural workflow roots, control beads, messages, ephemeral wisps, and
controller bookkeeping are exempt through an explicit predicate. `no-op`,
`blocked`, and `abandoned` outcomes use reason and evidence rules but do not
require a landing. Exemptions are typed; `--force`, an arbitrary metadata flag,
or a specially worded close reason cannot bypass the gate.

The close validator is configuration-gated during rollout, with `warn` and
`error` modes. Production qualification requires `error`.

### Compatibility and consumer audit

Before implementation changes any shared work-record field, event, close
predicate, or outcome value, enumerate every code, prompt, formula, hook,
dashboard, and document consumer in Gas City, gascity-packs, beads, and the
maintained downstream fork. Record the current meaning and required migration
for each consumer.

Existing keys are not repurposed. New semantics receive new names, and mixed
old/new records remain readable throughout warning-mode rollout. The universal
gate must not depend on reading a machine-local artifact path when invoked by a
proxied server; portable receipt fields and the observed remote ref are the
blocking evidence, while artifact paths and hashes provide the richer audit
trail where available.

The threat model is accidental or faulty false completion by authorized
writers, not a malicious writer with unrestricted repository and bead-metadata
access. A hostile authorized writer can forge local metadata or source state;
preventing that requires a separately designed signing and authorization
system.

## Recovery and idempotency

Every side effect has an evidence checkpoint:

- before integration: manifest hash and observed base;
- after each cherry-pick: source SHA and resulting candidate SHA;
- after verification: command result and tree hash;
- before publication: approved candidate and expected remote SHA;
- after publication: observed remote SHA and landing event ID;
- before close: stamped evidence revalidated.

A restarted workflow reads these checkpoints. It may reuse an unchanged clean
candidate, but it must create a new attempt after target movement, source
change, conflict resolution, or candidate modification. Publication retry is
safe only when the remote is still at the expected base or already at the exact
approved candidate.

Source worktrees, integration artifacts, and candidate refs are not deleted by
the landing transaction. Cleanup runs separately after the closed records and
landing evidence agree.

## Reconciliation and the remaining refinery role

Reconciliation is a backstop, not the source of truth. A bounded order may
report:

- `integration_ready` work older than its SLA;
- `publication_pending` attempts without progress;
- landed events whose beads remain open;
- shipped closes whose remote evidence later disappears;
- preserved worktrees eligible for reviewed cleanup.

The reconciler never lands or closes work on inference.

If the name `refinery` is retained, it names an on-demand exception lane. It
handles conflicts, combined-test failures, unexpected remote policy, and
recovery decisions. It has no always-on named session, no self-polling patrol,
and no exclusive authority over the truth of landing.

## Rollout

1. **Golden stock:** run `gascity` with same-session drain and publication
   disabled in the isolated managed-Dolt environment.
2. **Shadow integration:** enable manifest and integration-result generation
   for separate drain, but prohibit all pushes. Compare the candidate tree
   against a manually assembled fixture.
3. **Candidate publication:** publish only to a disposable candidate ref and
   verify typed landing events and work-record stamps without closing work.
4. **PR mode:** exercise external merge observation, including a
   server-generated or squash merge SHA.
5. **Close enforcement:** enable warning mode against fixture work, audit every
   exemption, then enable error mode.
6. **Production admission:** permit one independently reviewed build through
   the single integration target. Persistent Gastown refinery topology remains
   excluded.

Each rung has a rollback consisting of disabling the new capability and
leaving its evidence intact. Rollback never rewrites production history.

## Test strategy

### Pack contract tests

- build graphs place integration before review and publication;
- all maintained build methodologies inherit the integration contract;
- same-session emits a coherent no-cherry-pick result;
- separate drain assembles two independent commits deterministically;
- dependency order overrides lexical order;
- missing commits, dirty worktrees, path overlap, cycles, and conflicts fail
  closed;
- combined-test failure produces rework and cannot reach review;
- review and publisher receive the exact candidate SHA.

### Gas City core tests

- landing event type and payload are registered;
- unregistered or oversized payloads fail existing event invariants;
- direct publication emits only after remote verification;
- PR creation does not emit a landing;
- replay of an already observed exact landing is idempotent.

### Beads tests

- raw CLI, library, API, and proxied-server shipped closes share the gate;
- stale remote refs cannot pass;
- missing or mismatched artifacts, events, targets, and SHAs fail;
- direct and squash landing receipts pass;
- structural and ephemeral exemptions are exact;
- no-op, blocked, and abandoned outcomes remain usable;
- warning and error rollout modes behave consistently.

### End-to-end qualification

A disposable repository and managed-Dolt city exercise:

1. two non-overlapping parallel tasks;
2. a deterministic assembled candidate;
3. combined review and verification;
4. publication to a disposable bare remote;
5. exact remote landing evidence;
6. successful close only after the stamp;
7. a second run from an empty clean-room root;
8. zero mutation outside the clean-room root.

Negative fixtures cover target movement, conflicts, red combined tests,
publication rejection, crash recovery, and attempted premature close.

## Acceptance criteria

This design is complete only when:

1. no separate-drain build can reach review without an assembled candidate;
2. review, fixes, publication, and landing evidence all name the same approved
   candidate lineage;
3. shipped work cannot close through any supported close path before verified
   landing;
4. a worker may finish and drain without falsely closing its work;
5. no persistent integration/refinery session or polling loop is required;
6. conflicts and target movement preserve evidence and fail closed;
7. direct and PR landing modes record the actual remote SHA;
8. same-session and separate-drain runs share the same downstream contract;
9. all behavior is proven twice in isolated stock managed-Dolt environments;
10. the current Gastown topology, MySQL store, legacy branches, and broken
    refinery lane remain outside the proof and ancestry.
