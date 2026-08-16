# Gas City Pack Ephemeral Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task with review checkpoints.

**Goal:** Add a stock, deterministic, shadow-only integration stage to the `gascity` pack so every build reviews one assembled candidate rather than disconnected implementation worktrees.

**Architecture:** Extend the shared `build-base` graph with an `integrate` stage between both implementation drains and implementation summarization. A providerless integration operator runs a deterministic Python helper that validates a machine-readable manifest, fetches the declared target, creates an isolated scratch worktree, applies source commits in dependency order, runs argv-only verification commands, and writes an immutable result. This phase never pushes, opens a PR, updates the target ref, or closes shipped work.

**Tech Stack:** Graph v2 formula TOML, Python 3.11 standard library, Git CLI, YAML build-artifact schemas, `unittest`, existing Gas City pack validation helpers.

**Spec:** [`engdocs/design/2026-08-16-ephemeral-integration-and-landing-contract.md`](../design/2026-08-16-ephemeral-integration-and-landing-contract.md)

## Global Constraints

- Work in a fresh `gascity-packs` worktree based on upstream commit `3b3b89f2011e06d84459aa7bea1552382f13930a`; do not copy ancestry or code from the broken city worktree.
- Use stock file-backed Gas City/Beads behavior. Do not add MySQL, Dolt server, Gastown, refinery, polecat, or persistent integration-worker dependencies.
- Preserve `gc.work_commit` as the source-worktree commit. New integration fields must not reinterpret or overwrite it.
- The integration helper must use `subprocess.run` with argument vectors, never `shell=True` or user-controlled command strings.
- The scratch worktree must be under the declared artifact root and outside source worktrees. Validate resolved paths before mutation.
- Phase-one output is shadow evidence only. The implementation must contain no `git push`, hosting API, PR creation, target-ref mutation, bead close, or shipped-state transition.
- All failures must leave a typed result artifact. Conflict results must include conflict paths and the last clean candidate; verification failures must include the failed argv and exit code.
- Test first for every behavior. Observe the intended failure before implementing it.

---

## Task 1: Establish the isolated pack baseline

**Files:**

- Create worktree only; no source changes.

**Step 1: Create a clean clone and feature worktree**

Run from a temporary directory, following `superpowers:using-git-worktrees`:

```bash
git clone https://github.com/gastownhall/gascity-packs.git gascity-packs
cd gascity-packs
git fetch origin 3b3b89f2011e06d84459aa7bea1552382f13930a
git switch --detach 3b3b89f2011e06d84459aa7bea1552382f13930a
git worktree add ../gascity-packs-ephemeral-integration -b mechanic/ephemeral-integration
cd ../gascity-packs-ephemeral-integration
```

Expected: `git status --short` is empty and `git rev-parse HEAD` prints the pinned commit.

**Step 2: Record the stock baseline**

Run:

```bash
python3 -m unittest discover -s gascity/tests -p 'test_*.py'
python3 -m unittest gascity.tests.test_derived_pack_compatibility
go test ./...
python3 validate_registry.py registry.toml
```

Expected: all commands pass. Record wall time, peak RSS, and swap count for the Python suite in the bead note before changing code.

**Step 3: Commit**

No commit for this task. Do not continue if the pinned upstream baseline is red; diagnose the baseline as a separate issue.

---

## Task 2: Add typed integration manifest and result artifacts

**Files:**

- Create: `gascity/schemas/build/integration-manifest.v1.yaml`
- Create: `gascity/schemas/build/integration-result.v1.yaml`
- Modify: `gascity/assets/scripts/validate_build_artifact.py`
- Modify: `gascity/tests/test_artifacts.py`
- Modify: `gascity/tests/test_validators.py`

**Step 1: Write failing schema-presence and validator tests**

Update the expected schema map in `test_artifacts.py` with:

```python
"integration-manifest.v1.yaml": "gc.build.integration-manifest.v1",
"integration-result.v1.yaml": "gc.build.integration-result.v1",
```

In `BuildArtifactValidatorTests`, add both schemas to `SCHEMA_SECTIONS` and `SCHEMA_STATUS`, then add focused tests proving:

- a manifest accepts one source with `bead_id`, `base_sha`, `result_sha`, `dependencies`, `worktree`, `changed_paths`, `summary.path`, and `summary.hash`;
- a manifest rejects duplicate bead IDs, missing dependency IDs, a dependency cycle, non-absolute repository/worktree paths, and a source result SHA that is not 40 lowercase hex characters;
- a result accepts `ready`, `needs_rework`, and `failed` integration outcomes while its build-artifact status remains `approved` or `blocked` as appropriate;
- a `ready` result requires `candidate_sha`, `tree_sha`, a complete source map, and verification records whose `argv` values are non-empty string lists;
- `needs_rework` requires non-empty `conflict_paths` and `last_clean_candidate_sha`;
- `failed` verification requires a failed argv and non-zero exit code;
- owner/persona/role remains forbidden in base schemas.

Use a helper that builds YAML-front-matter Markdown so each test changes only the contract field under test.

**Step 2: Run the validator tests and observe failure**

```bash
python3 -m unittest \
  gascity.tests.test_artifacts.ArtifactHelperTests.test_build_artifact_schema_files_have_stable_ids \
  gascity.tests.test_validators.BuildArtifactValidatorTests
```

Expected: the schema files are absent and the validator does not enforce integration-specific invariants.

**Step 3: Define the two immutable schemas**

Use the existing schema shape. The manifest schema must require these front-matter paths:

```yaml
schema_id: gc.build.integration-manifest.v1
artifact: integration-manifest
allowed_statuses: [draft, approved, blocked, superseded]
required_front_matter:
  - schema
  - workflow.id
  - workflow.formula
  - methodology.pack
  - methodology.name
  - producer.formula
  - producer.stage
  - producer.attempt
  - status
  - integration.repository
  - integration.remote
  - integration.target_ref
  - integration.base_sha
  - integration.artifact_root
  - integration.sources
  - integration.verification
  - trace
coverage_statuses: [covered, not_applicable, deferred, blocked, out_of_scope, superseded]
required_sections: [Candidate, Sources, Verification, Safety]
```

The result schema must require the same workflow/methodology/producer envelope plus:

```yaml
  - integration.outcome
  - integration.manifest_path
  - integration.manifest_hash
  - integration.base_sha
  - integration.scratch_worktree
  - integration.source_map
  - integration.verification
```

Its required sections are `Outcome`, `Candidate`, `Source Map`, `Verification`, `Conflicts`, and `Safety`.

**Step 4: Add schema-specific validation**

In `validate_build_artifact.py`, call a new dispatcher immediately after `validate_required_front_matter`:

```python
validate_schema_contract(schema_id, front_matter)
```

Implement private helpers named `validate_schema_contract(schema_id, front_matter)`,
`validate_integration_manifest(integration)`, `validate_integration_result(integration)`,
`validate_git_sha(value, field)`, `validate_argv_list(value, field)`, and
`topological_source_order(sources)`. Preserve the type contracts `None`, `None`,
`None`, `str`, `list[list[str]]`, and `list[str]`, respectively.

`topological_source_order` must preserve manifest order among simultaneously-ready sources, reject duplicates/unknown dependencies/cycles, and be reusable by the Git assembler in Task 3.

**Step 5: Run tests and commit**

```bash
python3 -m unittest gascity.tests.test_artifacts gascity.tests.test_validators
git add gascity/schemas/build gascity/assets/scripts/validate_build_artifact.py gascity/tests/test_artifacts.py gascity/tests/test_validators.py
git commit -m "feat(gascity): define typed integration artifacts"
```

Expected: both suites pass.

---

## Task 3: Build the deterministic shadow integrator with TDD

**Files:**

- Create: `gascity/assets/scripts/integrate_candidate.py`
- Create: `gascity/tests/test_integrate_candidate.py`

**Step 1: Write Git fixture helpers and the happy-path failing test**

The test fixture creates, under `TemporaryDirectory`, a bare `origin.git`, a seed clone with `main`, and two detached source worktrees/branches whose commits touch different files. Do not use global Git config; pass fixture-local `user.name` and `user.email` with `git -c`.

Write `test_assembles_dependency_order_and_writes_ready_result` to invoke:

```python
code = module.main(["assemble", "--manifest", str(manifest), "--result", str(result)])
```

Assert:

- exit code is zero;
- result outcome is `ready`;
- source map order follows dependencies, not reversed manifest order;
- `candidate_sha` and `tree_sha` exist in the fixture repository;
- both changed files exist in the scratch worktree;
- verification includes the exact argv and exit code zero;
- target `refs/heads/main` in the bare origin is unchanged;
- the result records `push_performed: false`, `pr_opened: false`, and `target_ref_updated: false`.

Run and observe `FileNotFoundError` for the missing module.

**Step 2: Implement parsing, safety, and the happy path**

Create the following immutable data types:

```python
@dataclass(frozen=True)
class SourceRecord:
    bead_id: str
    base_sha: str
    result_sha: str
    dependencies: tuple[str, ...]
    worktree: Path
    changed_paths: tuple[str, ...]
    summary_path: Path
    summary_hash: str

@dataclass(frozen=True)
class IntegrationManifest:
    path: Path
    repository: Path
    artifact_root: Path
    remote: str
    target_ref: str
    base_sha: str
    sources: tuple[SourceRecord, ...]
    verification: tuple[tuple[str, ...], ...]
```

Add functions `load_manifest(path) -> IntegrationManifest`, `run_git(repository,
*args, check=True) -> subprocess.CompletedProcess[str]`, `assemble(manifest,
result_path) -> int`, `write_result(path, front_matter, sections) -> None`, and
`main(argv=None) -> int`.

Implementation sequence:

1. read the manifest text and parse it with `validate_build_artifact.validate_artifact_text(text, expected_schema="gc.build.integration-manifest.v1")`;
2. resolve and verify the repository top level;
3. require the artifact root and result parent to resolve inside the repository or its declared artifact root without symlink escape;
4. compute and record `sha256:<hex>` of the exact manifest bytes;
5. `git fetch --no-tags <remote> <target_ref>`;
6. verify declared `base_sha` is the fetched target commit;
7. create `<artifact_root>/integration/<workflow-id>/attempt-<n>/candidate` with `git worktree add --detach "$scratch_worktree" "$base_sha"`;
8. apply each `result_sha` using `git cherry-pick --no-edit` in stable topological order;
9. run each verification argv with `cwd=scratch_worktree`, `shell=False`, captured text, and no inherited `GC_*` mutation;
10. write the result atomically via a sibling temporary file plus `Path.replace`.

Do not clean up the scratch worktree in phase one; it is review evidence.

**Step 3: Add failing conflict, verification, and safety tests**

Add tests for:

- two source commits that conflict: returns non-zero, aborts cherry-pick, writes `needs_rework`, conflict paths, last clean SHA, and no target mutation;
- failing verification argv: writes `failed`, the exact argv/exit code, candidate/tree SHAs, and no target mutation;
- source SHA absent from the declared source worktree: rejected before candidate creation;
- result SHA that is not descended from its source `base_sha`: rejected;
- repository, artifact-root, result-path, or source-worktree symlink escape: rejected;
- duplicate/unknown/cyclic dependencies: rejected using the shared validator helper;
- an argv containing shell metacharacters is passed literally and cannot create a sentinel file;
- an existing attempt directory or result file is never overwritten.

Observe the focused failures before implementing each branch.

**Step 4: Implement failure paths and provenance checks**

Before candidate creation, verify each source with:

```bash
git -C <source-worktree> rev-parse HEAD
git -C <source-worktree> merge-base --is-ancestor <base-sha> <result-sha>
git -C <repository> cat-file -e <result-sha>^{commit}
```

On cherry-pick failure, collect `git diff --name-only --diff-filter=U`, record `HEAD` as the last clean candidate, then run `git cherry-pick --abort`. Preserve stdout/stderr only in the artifact root; redact environment values and do not serialize the process environment.

**Step 5: Run tests and commit**

```bash
python3 -m unittest gascity.tests.test_integrate_candidate
python3 -m unittest gascity.tests.test_validators
git add gascity/assets/scripts/integrate_candidate.py gascity/tests/test_integrate_candidate.py
git commit -m "feat(gascity): assemble shadow integration candidates"
```

Expected: all focused tests pass and the helper contains no push/PR/close path.

---

## Task 4: Add the integration formulas, operator, and workflow assets

**Files:**

- Create: `gascity/formulas/integration-base.formula.toml`
- Create: `gascity/formulas/integrate.formula.toml`
- Modify: `gascity/formulas/build-base.formula.toml`
- Create: `gascity/assets/workflows/integration-base/prepare-manifest.md`
- Create: `gascity/assets/workflows/integration-base/assemble-candidate.md`
- Create: `gascity/assets/workflows/integration-base/record-result.md`
- Create: `gascity/assets/workflows/build-base/integrate.md`
- Modify: `gascity/assets/workflows/build-base/prepare.md`
- Modify: `gascity/assets/workflows/build-base/summarize-implementation.md`
- Create: `gascity/roles/agents/integration-operator/agent.toml`
- Create: `gascity/roles/agents/integration-operator/prompt.template.md`
- Modify: `gascity/README.md`
- Modify: `gascity/tests/test_formula_assets.py`
- Modify: `gascity/tests/test_derived_pack_compatibility.py`

**Step 1: Write and run the failing graph contract tests**

In `gascity/tests/test_formula_assets.py`:

- Add `integration-base` and `integrate` to `FORMULAS`.
- Add `integration-operator` to `ROLE_AGENTS`.
- Insert `integrate` in `BUILD_BASE_STEPS` after `implement-same-session` and before `summarize-implementation`.
- Add `integration-base` to `METHODOLOGY_STAGE_CONTRACTS` with steps `prepare-manifest`, `assemble-candidate`, and `record-result`; set `target_required` false; require vars `artifact_root`, `integration_manifest_path`, `integration_result_path`, and `integration_target`.
- Add `"integration_formula": "integrate"` to `METHODOLOGY_FORMULA_VARS`.
- Extend `test_build_base_is_full_lifecycle_virtual_contract` to assert the integration stage needs both implementation alternatives, routes to `{{integration_target}}`, expands `integrate`, and is the only dependency of `summarize-implementation`.
- Assert defaults `integration_target = "gc.integration-operator"` and `integration_formula = "integrate"`.
- Assert every top-level and derived build resolves with exactly one `integrate` stage before summary/review. Update the existing expected-step comparisons rather than adding a parallel, weaker test.

Run:

```bash
python3 -m unittest \
  gascity.tests.test_formula_assets.FormulaAssetTests.test_expected_formula_set_is_convoy_first \
  gascity.tests.test_formula_assets.FormulaAssetTests.test_expected_role_agents_are_providerless \
  gascity.tests.test_formula_assets.FormulaAssetTests.test_methodology_stage_contracts_are_virtual_and_shadowable \
  gascity.tests.test_formula_assets.FormulaAssetTests.test_build_base_is_full_lifecycle_virtual_contract \
  gascity.tests.test_derived_pack_compatibility
```

Expected: failures name the missing formulas, role, variables, and `integrate` graph node. Continue immediately to implementation; do not commit the red state.

**Step 2: Implement the virtual and concrete formulas**

`integration-base.formula.toml` is `internal = true`, `target_required = false`, graph v2, and declares exactly these vars:

- required: `artifact_root`, `integration_manifest_path`, `integration_result_path`;
- defaulted: `integration_target = "gc.integration-operator"`.

Its steps are:

1. `prepare-manifest`, routed to `gc.run-operator`, producing and gating `gc.build.integration-manifest.v1`;
2. `assemble-candidate`, needing `prepare-manifest`, routed to `{{integration_target}}`, invoking only `.gc/scripts/integrate_candidate.py assemble --manifest "{{integration_manifest_path}}" --result "{{integration_result_path}}"`;
3. `record-result`, needing `assemble-candidate`, routed to `gc.run-operator`, gating `gc.build.integration-result.v1` and recording result path/hash/outcome/candidate/tree on the workflow root.

`integrate.formula.toml` must extend `integration-base`, be internal, and provide only the stock workflow assets. It must not add methodology logic.

**Step 3: Wire `build-base`**

Add vars:

```toml
[vars.integration_formula]
description = "Integration methodology formula used to assemble a review candidate."
default = "integrate"

[vars.integration_target]
description = "Role target for deterministic candidate assembly."
default = "gc.integration-operator"
```

Render them in the top-level description and prepare asset. Add the `integrate` stage after both implementation drains. It expands `integrate`, needs both conditional drain steps, routes to `{{integration_target}}`, and passes canonical artifact paths under `{{artifact_root}}/integration/`.

Change `summarize-implementation.needs` to only `integrate`. Update its asset so changed-files and verification sections describe the assembled candidate and cite the integration result path/hash; it must not summarize disconnected worktree heads as if shipped.

**Step 4: Define the providerless role**

`agent.toml`:

```toml
description = "Ephemeral integration operator for deterministic candidate assembly and verification"
scope = "rig"
fallback = false
```

The prompt must inherit the shared graph-claim protocol used by every other role, execute the declared helper exactly once, make no ad-hoc Git integration decisions, never push/close/open a PR, and close its claimed step only after a valid result artifact exists.

**Step 5: Make all formula and compatibility tests pass**

Run:

```bash
python3 -m unittest gascity.tests.test_formula_assets
python3 -m unittest gascity.tests.test_derived_pack_compatibility
```

Expected: `build-basic`, continuation formulas, and all four methodology packs resolve with exactly one integration seam and no route points at an undefined role.

**Step 6: Commit**

```bash
git add gascity/formulas gascity/assets/workflows gascity/roles/agents/integration-operator gascity/README.md gascity/tests/test_formula_assets.py gascity/tests/test_derived_pack_compatibility.py
git commit -m "feat(gascity): route builds through ephemeral integration"
```

---

## Task 5: Prove shadow integration end to end

**Files:**

- Create: `gascity/tests/test_ephemeral_integration_pipeline.py`
- Modify: `.github/workflows/test.yml` only if the existing workflow does not discover `gascity/tests/test_*.py`.

**Step 1: Write a failing pipeline test around the public pack surfaces**

The test must:

1. resolve `build-basic` through the same formula resolver used in `test_formula_assets.py`;
2. assert the graph order `implement* -> integrate -> summarize -> review -> finalize -> publish`;
3. create a local bare target and two source commits from the same base;
4. write and validate a canonical integration manifest artifact;
5. run the installed pack helper by its public `.gc/scripts/integrate_candidate.py` path shape;
6. validate the emitted result with `validate_build_artifact.py`;
7. inspect the candidate tree and prove it contains both source changes;
8. prove bare target `main` is unchanged;
9. scan all integration formulas/assets/scripts and fail on `git push`, `gh pr`, target `update-ref`, `bd close`, `gc bd close`, `shipped`, `refinery`, `polecat`, `mysql`, or `dolt sql-server`.

Observe the test fail before filling any missing fixture/install behavior.

**Step 2: Implement only the fixture/install support needed by the test**

Do not add production publication logic. If the public installed path differs, adapt the test fixture to the pack installer’s existing layout rather than teaching the production helper a second path convention.

**Step 3: Run the complete pack qualification**

```bash
python3 -m unittest discover -s gascity/tests -p 'test_*.py'
python3 -m unittest gascity.tests.test_derived_pack_compatibility
go test ./...
python3 validate_registry.py registry.toml
git diff --check
```

Expected: all pass. Record wall time, peak RSS, and swap count; compare them with Task 1’s baseline.

**Step 4: Run forbidden-topology audits**

```bash
rg -n -i 'refinery|polecat|mysql|go-mysql-server|dolt sql-server|git push|gh pr|update-ref|bd close|gc bd close' \
  gascity/formulas/integrat* \
  gascity/assets/workflows/integration-base \
  gascity/assets/scripts/integrate_candidate.py \
  gascity/roles/agents/integration-operator
```

Expected: no matches except explicit negative-language documentation tests. Manually verify any such match cannot execute.

**Step 5: Request review and commit**

Use `superpowers:requesting-code-review`, resolve findings with `superpowers:receiving-code-review`, then rerun Step 3 from a clean shell.

```bash
git add gascity/tests/test_ephemeral_integration_pipeline.py .github/workflows/test.yml
git commit -m "test(gascity): qualify shadow integration pipeline"
```

Do not publish the pack in this phase. The next independently reviewed plan adds Gas City’s typed landed event; a later Beads plan consumes that event for universal shipped-close enforcement.

---

## Acceptance Evidence

The phase is complete only when the handoff includes:

- clean upstream base SHA and feature branch SHA;
- full test commands and exit codes;
- baseline and final wall time, peak RSS, and swap count;
- manifest path/hash and result path/hash from the end-to-end fixture;
- base SHA, source SHAs, integrated SHAs, candidate SHA, and tree SHA;
- proof the target ref did not change;
- proof no push, PR, close, refinery, polecat, or MySQL path exists;
- derived-pack compatibility results for BMAD, Compound Engineering, Superpowers, and GStack;
- an explicit statement that `gc.work_commit` remains the source commit and no landed/shipped claim is made.
