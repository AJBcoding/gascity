# Gas City Typed Landing Event Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a strict, provider-neutral `gc landing record` boundary that observes an exact authoritative remote ref and records bounded, typed, idempotent `delivery.landed` evidence without publishing or closing work.

**Architecture:** A new `internal/landing` domain service validates an operator-supplied receipt, delegates remote observation through a narrow interface, constructs a deterministic event ID, and confirms the typed event is readable after recording. The CLI supplies a hardened Git observer and the configured city event provider. The event payload is registered in `internal/events`, so existing SSE and generated OpenAPI projections expose it without a landing-specific API handler.

**Tech Stack:** Go 1.25, Cobra CLI, existing `internal/events` provider and typed-payload registry, Git CLI with argv-only execution and `git.SanitizedEnv`, Huma/OpenAPI generation, Go tests with local bare repositories.

**Spec:** [`engdocs/design/2026-08-16-ephemeral-integration-and-landing-contract.md`](../design/2026-08-16-ephemeral-integration-and-landing-contract.md)

## Global Constraints

- Base implementation on current stock upstream `b1153eb25288c61c1cd063ec9062882d5f249e90`, plus only the approved clean-room design and plan commits.
- Keep core role-neutral: no refinery, polecat, publisher-role, Gastown, or other pack role name in production code.
- Use the stock configured event provider and file-backed test stores. Add no MySQL, go-mysql-server, managed Dolt server, or backend-specific behavior.
- `gc landing record` observes only. It contains no push, force, fetch-to-local-ref, PR creation, bead update, bead close, cleanup, or worktree deletion path.
- The command must independently run `git ls-remote --exit-code <remote-name> <target-ref>` after resolving the configured remote identity. A local ref, remote-tracking ref, caller assertion, or branch-name match cannot satisfy it.
- Accept only a remote name matching `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` and a target under `refs/heads/`; never pass an operator string beginning with `-` as a Git operand.
- Git execution uses argument vectors, `shell=false` by construction, a 30-second context timeout, `git.SanitizedEnv()`, and captured output. Never serialize environment or credential material.
- Reject remote URLs containing embedded userinfo. Record the resolved remote URL or absolute local path as repository identity; redact it in errors with `gitcred.RedactUserinfo`.
- Accept only lowercase 40-character Git SHAs and `sha256:<64 lowercase hex>` artifact hashes.
- Bound receipt input to 64 KiB, each scalar to its declared maximum, and included work IDs to 256 unique entries. The event carries no source-to-integrated map or command output.
- The service, not the caller, supplies `verified_at` after remote observation.
- A deterministic landing event ID is derived from canonical receipt fields excluding `verified_at` and actor. Sequential replay re-observes the remote and returns the existing event without appending another record.
- A dropped best-effort `Record` is not success: the service must read the event back by deterministic ID before returning success.
- Direct mode requires `approved_candidate_sha == expected_landed_sha == observed_landed_sha`. Pull-request mode permits the landed SHA to differ from the candidate but still requires the caller's exact expected landed SHA to match the remote.
- This phase makes no `landed` or `shipped` claim about production work. It supplies the observation primitive that a later publication plan consumes; Beads enforcement remains a separate repository change.

---

### Task 0: Establish the stock-backend baseline

**Files:** None.

**Interfaces:**

- Confirms: the synced stock tree is healthy before landing-specific changes.
- Records: bounded test timing, memory, and swap evidence for later comparison.

- [ ] **Step 1: Verify the focused stock surfaces**

```bash
/usr/bin/time -l env CGO_ENABLED=0 GOFLAGS=-p=2 go test ./internal/events ./internal/api
```

Expected: both focused packages pass on the stock file-backed configuration. Record wall time, maximum RSS, and swaps. Do not add the unsharded `./cmd/gc` package here: repository policy requires its broad suite to use the supported shard runner in Step 2.

- [ ] **Step 2: Verify the full stock tree and generated documentation**

```bash
LOCAL_TEST_JOBS=2 make test-fast-parallel
make check-docs
git status --short
```

Expected: the repository-supported sharded fast suite and documentation checks pass, and only this uncommitted plan file appears in status. If a failure occurs, reproduce it at `b1153eb25288c61c1cd063ec9062882d5f249e90` before changing landing code. Never substitute raw `go test ./cmd/gc` or raw `go test ./...` for the documented broad-sweep runner.

---

### Task 1: Register the bounded typed landing payload

**Files:**

- Create: `internal/events/landing_payloads.go`
- Create: `internal/events/landing_payloads_test.go`
- Modify: `internal/events/events.go`
- Test: `internal/api/event_payloads_coverage_test.go`

**Interfaces:**

- Produces: `events.DeliveryLanded = "delivery.landed"`.
- Produces: `events.DeliveryLandedPayload`, the sole wire/event shape consumed by later tasks.
- Preserves: the existing `events.Payload`, `RegisterPayload`, `KnownEventTypes`, and API `oneOf` registry contracts.

- [ ] **Step 1: Write the failing event registration test**

Create `internal/events/landing_payloads_test.go` with a test that references the not-yet-defined symbols and verifies JSON names:

```go
func TestDeliveryLandedPayloadIsRegistered(t *testing.T) {
    sample, ok := LookupPayload(DeliveryLanded)
    if !ok {
        t.Fatal("delivery.landed payload is not registered")
    }
    if _, ok := sample.(DeliveryLandedPayload); !ok {
        t.Fatalf("payload type = %T", sample)
    }
    raw, err := json.Marshal(DeliveryLandedPayload{
        EventID: "gcl-abc", WorkflowID: "run-1", IntegrationAttemptID: "attempt-1",
        Repository: "https://example.invalid/acme/repo.git", Remote: "origin",
        TargetRef: "refs/heads/main", ExpectedTargetSHA: strings.Repeat("a", 40),
        ApprovedCandidateSHA: strings.Repeat("b", 40), ObservedLandedSHA: strings.Repeat("b", 40),
        PublicationMode: "direct", IntegrationResultPath: "/tmp/result.md",
        IntegrationResultHash: "sha256:" + strings.Repeat("c", 64),
        WorkBeadIDs: []string{"gc-1"}, VerifiedAt: "2026-08-16T20:00:00Z",
    })
    if err != nil { t.Fatal(err) }
    for _, key := range []string{"event_id", "workflow_id", "integration_attempt_id", "repository", "remote", "target_ref", "expected_target_sha", "approved_candidate_sha", "observed_landed_sha", "publication_mode", "integration_result_path", "integration_result_hash", "work_bead_ids", "verified_at"} {
        if !bytes.Contains(raw, []byte(`"`+key+`"`)) { t.Errorf("missing %s in %s", key, raw) }
    }
}
```

- [ ] **Step 2: Run the focused test and observe the compile failure**

Run:

```bash
go test ./internal/events -run TestDeliveryLandedPayloadIsRegistered -count=1
```

Expected: compile failure naming `DeliveryLanded` and `DeliveryLandedPayload`.

- [ ] **Step 3: Add the event constant and payload**

Add `DeliveryLanded = "delivery.landed"` to `internal/events/events.go` and include it in `KnownEventTypes`. Create this exact payload in `landing_payloads.go`:

```go
type DeliveryLandedPayload struct {
    EventID                 string   `json:"event_id" doc:"Deterministic landing receipt identifier."`
    WorkflowID              string   `json:"workflow_id"`
    IntegrationAttemptID    string   `json:"integration_attempt_id"`
    Repository              string   `json:"repository" doc:"Credential-free authoritative remote identity."`
    Remote                  string   `json:"remote"`
    TargetRef               string   `json:"target_ref"`
    ExpectedTargetSHA       string   `json:"expected_target_sha"`
    ApprovedCandidateSHA    string   `json:"approved_candidate_sha"`
    ObservedLandedSHA       string   `json:"observed_landed_sha"`
    PublicationMode         string   `json:"publication_mode" enum:"direct,pull_request"`
    IntegrationResultPath   string   `json:"integration_result_path"`
    IntegrationResultHash   string   `json:"integration_result_hash"`
    WorkBeadIDs             []string `json:"work_bead_ids" maxItems:"256"`
    VerifiedAt              string   `json:"verified_at" format:"date-time"`
}

func (DeliveryLandedPayload) IsEventPayload() {}

func init() {
    RegisterPayload(DeliveryLanded, DeliveryLandedPayload{})
}
```

- [ ] **Step 4: Run registration and API coverage tests**

Run:

```bash
go test ./internal/events ./internal/api -run 'TestDeliveryLandedPayloadIsRegistered|TestEveryKnownEventTypeHasRegisteredPayload' -count=1
```

Expected: both tests pass.

- [ ] **Step 5: Commit the typed contract**

```bash
git add internal/events/events.go internal/events/landing_payloads.go internal/events/landing_payloads_test.go
git commit -m "feat(events): register verified landing payload"
```

---

### Task 2: Build the validation and idempotent recording service

**Files:**

- Create: `internal/landing/landing.go`
- Create: `internal/landing/landing_test.go`

**Interfaces:**

- Consumes: `events.DeliveryLandedPayload`, `events.DeliveryLanded`, `events.Event`, and `events.Filter`.
- Produces: `landing.RecordRequest`, `landing.RemoteObservation`, `landing.RemoteObserver`, `landing.EventJournal`, `landing.Result`, `landing.Service.Record`, and validation errors.
- `RemoteObserver.Observe(context.Context, repositoryPath, remote, targetRef string) (RemoteObservation, error)` performs the only remote lookup.
- `EventJournal` exposes only `Record(events.Event)` and `List(events.Filter) ([]events.Event, error)`.

- [ ] **Step 1: Write failing validation and service tests**

Create table-driven tests covering:

```go
func TestRecordRejectsMalformedOrOversizedReceiptBeforeObservation(t *testing.T)
func TestRecordObservesRemoteBeforeEmittingTypedEvent(t *testing.T)
func TestRecordDirectModeRequiresCandidateAtRemote(t *testing.T)
func TestRecordPullRequestModeAllowsExactServerGeneratedLanding(t *testing.T)
func TestRecordRejectsObservedTargetMismatchWithoutEvent(t *testing.T)
func TestRecordSequentialReplayReobservesAndDoesNotAppend(t *testing.T)
func TestRecordFailsWhenRecordedEventCannotBeReadBack(t *testing.T)
func TestRecordEventIDIsStableAcrossActorAndClockChanges(t *testing.T)
```

Use fakes that record call order. The happy-path request is:

```go
RecordRequest{
    WorkflowID: "build-1", IntegrationAttemptID: "attempt-1",
    RepositoryPath: repo, Repository: remoteURL, Remote: "origin",
    TargetRef: "refs/heads/main", ExpectedTargetSHA: shaA,
    ApprovedCandidateSHA: shaB, ExpectedLandedSHA: shaB,
    PublicationMode: "direct", IntegrationResultPath: resultPath,
    IntegrationResultHash: "sha256:" + strings.Repeat("c", 64),
    WorkBeadIDs: []string{"gc-a", "gc-b"}, Actor: "gc.publisher",
}
```

The malformed table must individually reject an empty required scalar, unsafe remote, non-head target ref, malformed SHA/hash, relative repository/artifact path, duplicate or empty bead ID, more than 256 bead IDs, a scalar over its declared limit, and an unsupported publication mode.

- [ ] **Step 2: Run the package test and observe the missing-package failure**

Run:

```bash
go test ./internal/landing -count=1
```

Expected: failure because `internal/landing` does not exist.

- [ ] **Step 3: Define the domain types and bounds**

Create `landing.go` with these public types:

```go
const (
    MaxReceiptBytes = 64 << 10
    MaxWorkBeadIDs = 256
)

type RecordRequest struct {
    WorkflowID, IntegrationAttemptID string
    RepositoryPath, Repository, Remote, TargetRef string
    ExpectedTargetSHA, ApprovedCandidateSHA, ExpectedLandedSHA string
    PublicationMode string
    IntegrationResultPath, IntegrationResultHash string
    WorkBeadIDs []string
    Actor string
}

type RemoteObservation struct {
    Repository string
    SHA string
}

type RemoteObserver interface {
    Observe(context.Context, string, string, string) (RemoteObservation, error)
}

type EventJournal interface {
    Record(events.Event)
    List(events.Filter) ([]events.Event, error)
}

type Result struct {
    EventID string `json:"event_id"`
    ObservedLandedSHA string `json:"observed_landed_sha"`
    AlreadyRecorded bool `json:"already_recorded"`
}

type Service struct {
    Observer RemoteObserver
    Journal EventJournal
    Now func() time.Time
}
```

Use named constants for scalar limits: IDs 256 bytes, repository identity and artifact path 4096, remote 128, target ref 512, actor 256. Validate UTF-8 and trimmed equality so hidden whitespace cannot alter identity.

- [ ] **Step 4: Implement deterministic identity and record ordering**

`Service.Record` must execute in this order:

```go
validate request
observation := Observer.Observe(ctx, RepositoryPath, Remote, TargetRef)
require observation.Repository == request.Repository
require observation.SHA == request.ExpectedLandedSHA
require direct-mode candidate == expected landed SHA
payload := buildPayload(request, observation, Now().UTC())
payload.EventID = deterministicEventID(payload) // canonical struct excludes EventID, VerifiedAt, Actor
if eventWithIDAlreadyExists(Journal, payload.EventID) { return Result{..., AlreadyRecorded: true}, nil }
Journal.Record(events.Event{Type: events.DeliveryLanded, Actor: request.Actor, Subject: payload.EventID, RunID: request.WorkflowID, Payload: mustJSON(payload)})
require eventWithIDAlreadyExists(Journal, payload.EventID)
return Result{EventID: payload.EventID, ObservedLandedSHA: observation.SHA}, nil
```

Use `gcl-` plus the full lowercase SHA-256 digest of a JSON-marshaled private canonical struct. Decode every existing candidate event through `events.DecodePayload` and compare its `EventID`; do not substring-search raw JSON.

- [ ] **Step 5: Run the domain tests**

```bash
go test ./internal/landing ./internal/events -count=1
```

Expected: all tests pass.

- [ ] **Step 6: Commit the service**

```bash
git add internal/landing
git commit -m "feat(landing): verify and record exact remote landings"
```

---

### Task 3: Add the strict `gc landing record` CLI and Git observer

**Files:**

- Create: `cmd/gc/cmd_landing.go`
- Create: `cmd/gc/cmd_landing_test.go`
- Create: `cmd/gc/landing_git.go`
- Create: `cmd/gc/landing_git_test.go`
- Modify: `cmd/gc/main.go`

**Interfaces:**

- Consumes: `landing.Service`, `landing.RecordRequest`, `openCityEventsProvider`, `eventActor`, `git.SanitizedEnv`, and `gitcred.RedactUserinfo`.
- Produces: `gc landing record --receipt <absolute-json-path> [--json]`.
- The receipt JSON is the snake-case projection of `landing.RecordRequest`; `Actor` is never accepted from the file and comes from `eventActor()`.

- [ ] **Step 1: Write failing Git observer tests**

Use a real local bare repository and injectable command runner to cover:

```go
func TestGitLandingObserverReadsExactHeadRefAndRepositoryIdentity(t *testing.T)
func TestGitLandingObserverRejectsUnsafeRemoteNameBeforeGit(t *testing.T)
func TestGitLandingObserverRejectsNonHeadTargetBeforeGit(t *testing.T)
func TestGitLandingObserverRejectsEmbeddedRemoteCredentials(t *testing.T)
func TestGitLandingObserverRejectsMissingOrAmbiguousRef(t *testing.T)
func TestGitLandingObserverUsesSanitizedEnvironmentAndDeadline(t *testing.T)
```

Assert the only network argv ends in:

```text
ls-remote --exit-code origin refs/heads/main
```

and contains no shell string, fetch, push, or update-ref operation.

- [ ] **Step 2: Run the observer tests and observe missing symbols**

```bash
go test ./cmd/gc -run '^TestGitLandingObserver' -count=1
```

Expected: compile failure naming the missing observer.

- [ ] **Step 3: Implement the observer**

Implement a `gitLandingObserver` satisfying `landing.RemoteObserver`. Resolve the repository top-level, resolve exactly one fetch URL for the named remote, reject embedded URL userinfo, normalize a local path to an absolute clean path, then run the exact `ls-remote` argv under a 30-second child context. Parse exactly one `<40-hex>\t<target-ref>` row and reject empty, extra, peeled, abbreviated, or mismatched rows.

- [ ] **Step 4: Write failing CLI tests**

Add tests for:

```go
func TestLandingRecordCLIRecordsVerifiedReceiptAndPrintsTypedJSON(t *testing.T)
func TestLandingRecordCLIReplayReportsAlreadyRecorded(t *testing.T)
func TestLandingRecordCLIRejectsOversizedReceiptBeforeRemoteLookup(t *testing.T)
func TestLandingRecordCLITargetMismatchExitsNonzeroAndWritesNoEvent(t *testing.T)
func TestLandingRecordCLIHasNoPublishOrCloseFlags(t *testing.T)
```

The JSON output must be:

```json
{"schema_version":"1","ok":true,"event_id":"gcl-...","observed_landed_sha":"<sha>","already_recorded":false}
```

- [ ] **Step 5: Run the CLI tests and observe the missing command**

```bash
go test ./cmd/gc -run '^TestLandingRecordCLI' -count=1
```

Expected: failure because `landing` is not registered on the root command.

- [ ] **Step 6: Implement the CLI projection**

Add `newLandingCmd(stdout, stderr)` to `main.go`. `record` requires exactly one `--receipt` path and no positional arguments. Open with `os.Open`, read through `io.LimitReader(file, landing.MaxReceiptBytes+1)`, reject oversize and trailing JSON, require absolute `repository_path` and `integration_result_path`, set `Actor = eventActor()`, open `openCityEventsProvider(stderr, "gc landing record")`, call the service, and return a normal nonzero CLI error on any validation, observation, recording, or read-back failure. Unlike `gc event emit`, this command is not best-effort.

- [ ] **Step 7: Run CLI and package tests**

```bash
go test ./cmd/gc ./internal/landing ./internal/events -run 'Landing|DeliveryLanded' -count=1
```

Expected: all focused tests pass.

- [ ] **Step 8: Commit the CLI boundary**

```bash
git add cmd/gc/cmd_landing.go cmd/gc/cmd_landing_test.go cmd/gc/landing_git.go cmd/gc/landing_git_test.go cmd/gc/main.go
git commit -m "feat(gc): record remotely verified landing receipts"
```

---

### Task 4: Regenerate and verify typed API and CLI documentation

**Files:**

- Modify by generation: `internal/api/openapi.json`
- Modify by generation: `docs/reference/schema/openapi.json`
- Modify by generation: `docs/reference/schema/openapi.txt`
- Modify by generation: `docs/reference/schema/events.json`
- Modify by generation: `docs/reference/schema/events.txt`
- Modify by generation: `internal/api/genclient/client_gen.go`
- Modify by generation: `internal/api/dashboardspa/web/shared/src/generated/gc-supervisor-client/`
- Modify by generation: `docs/reference/cli.md`
- Modify: `docs/reference/events.md`

**Interfaces:**

- Consumes: the registered `DeliveryLandedPayload` and Cobra command tree.
- Produces: generated Go/TypeScript/OpenAPI event variants and CLI reference entries.

- [ ] **Step 1: Add a failing generated-contract test**

Extend `internal/api/event_payloads_1a_test.go` or the nearest typed-envelope test to require `delivery.landed` to map to a named `DeliveryLandedPayload` schema and forbid it from the custom-event variant.

- [ ] **Step 2: Run the generated-contract test before generation**

```bash
go test ./internal/api -run 'DeliveryLanded|TypedEvent' -count=1
```

Expected: failure because committed generated schemas do not yet include the new variant.

- [ ] **Step 3: Regenerate canonical artifacts**

```bash
make generate
go run ./cmd/genspec
go generate ./internal/api/genclient
cd internal/api/dashboardspa/web && npm run generate:client
```

Run the repository's existing CLI-reference generator identified by `make generate`; do not hand-edit generated JSON, Go, TypeScript, or `docs/reference/cli.md`.

- [ ] **Step 4: Document the semantic boundary**

Add a `delivery.landed` entry to `docs/reference/events.md` stating that it is emitted only after exact remote observation, carries a deterministic receipt ID, does not imply a bead was closed, and is not emitted by `gc event emit` validation.

- [ ] **Step 5: Verify generated consistency**

```bash
go test ./internal/api -run 'TestEveryKnownEventTypeHasRegisteredPayload|TestOpenAPISpecInSync|DeliveryLanded' -count=1
make check-schema
```

Expected: all tests pass and generation leaves no additional diff.

- [ ] **Step 6: Commit generated surfaces**

```bash
git add internal/api/openapi.json docs/reference/schema internal/api/genclient/client_gen.go internal/api/dashboardspa/web/shared/src/generated/gc-supervisor-client docs/reference/cli.md docs/reference/events.md internal/api/event_payloads_1a_test.go
git commit -m "docs(events): publish typed landing contract"
```

---

### Task 5: Qualify landing observation end to end

**Files:**

- Create: `cmd/gc/landing_e2e_test.go`
- Modify: `engdocs/design/2026-08-16-ephemeral-integration-and-landing-contract.md`

**Interfaces:**

- Consumes: the public `gc landing record` command, local file event provider, and generated event schema.
- Produces: clean-room evidence for exact remote observation and idempotent typed recording.

- [ ] **Step 1: Write the failing public-surface E2E test**

Create a bare `origin.git`, a seed repository with base commit A, and candidate commit B. Push B to `refs/heads/candidate-fixture` only as test setup. Initialize a stock file-backed city, write a receipt naming that exact ref and SHA, invoke the Cobra root through `run`, and assert:

```go
result event ID has prefix gcl-
event log contains exactly one delivery.landed event after two identical invocations
payload observed_landed_sha equals git ls-remote output for the target
payload approved_candidate_sha and work_bead_ids equal the receipt
second invocation reports already_recorded=true
main remains at A and no other remote ref changes
no bead metadata or status changes
```

Add negative subtests for target movement, nonexistent ref, direct-mode candidate mismatch, PR-mode wrong expected landed SHA, malformed artifact hash, and unreadable event-provider confirmation.

- [ ] **Step 2: Run E2E and observe any missing public wiring**

```bash
go test ./cmd/gc -run '^TestLandingRecordEndToEnd$' -count=1
```

Expected: the first run fails only on missing public wiring or behavior named by the assertion; implement no publication behavior to make it pass.

- [ ] **Step 3: Complete minimal wiring and update design status**

Make only the changes needed for the public command to satisfy the test. Update the design status to record that shadow integration and the typed observation primitive are implemented, while publication integration and universal close enforcement remain pending.

- [ ] **Step 4: Run bounded full qualification**

```bash
/usr/bin/time -l env CGO_ENABLED=0 GOFLAGS=-p=2 go test ./internal/events ./internal/landing ./internal/api
/usr/bin/time -l env CGO_ENABLED=0 GOFLAGS=-p=2 go test ./cmd/gc -run '^TestLanding' -count=1
LOCAL_TEST_JOBS=2 make test-fast-parallel
make check-docs
git diff --check
```

Record wall time, max RSS, and swaps. If the full repository suite exposes an unrelated stock failure, reproduce it at `b1153eb25288c61c1cd063ec9062882d5f249e90` before classifying it; do not waive a landing-related failure.

- [ ] **Step 5: Run forbidden-capability and consumer audits**

```bash
rg -n -i 'git push|force-with-lease|update-ref|gh pr|bd close|gc bd close|mysql|go-mysql-server|dolt sql-server|refinery|polecat|gastown' internal/landing cmd/gc/cmd_landing.go cmd/gc/landing_git.go internal/events/landing_payloads.go
rg -n 'DeliveryLanded|delivery\.landed|LandingEventID|landed_event' internal cmd docs engdocs
```

Expected: the first command has no executable-capability match; manually classify comments or negative tests. The second output is the consumer inventory for the next pack-publication and Beads-enforcement plans.

- [ ] **Step 6: Commit qualification**

```bash
git add cmd/gc/landing_e2e_test.go engdocs/design/2026-08-16-ephemeral-integration-and-landing-contract.md
git commit -m "test(landing): qualify exact remote observation"
```

---

## Acceptance Evidence

This phase is complete only when the handoff includes:

- stock upstream base SHA and final feature SHA;
- the exact bare remote, target ref, pre-publication SHA, candidate SHA, and observed landed SHA from the E2E fixture;
- the deterministic event ID and SHA-256 of the integration-result fixture;
- proof the second identical invocation appended no event but re-observed the remote;
- proof target movement and mismatched expected landing fail without an event;
- proof direct and pull-request modes enforce their distinct SHA relationships;
- proof the event appears as a typed OpenAPI/SSE variant;
- bounded full-suite timing, max RSS, and swap count;
- forbidden-capability audit showing no push, PR, close, MySQL, Gastown, refinery, or polecat execution path;
- an explicit statement that no work bead was stamped or closed and no production landing occurred.

## Deferred Boundaries

The following are intentionally excluded and require separate plans:

1. gascity-packs publication formulas that call `gc landing record` after direct push verification or trusted PR-merge observation;
2. stamping included work records with the returned event ID and portable receipt fields;
3. Beads warning/error enforcement for shipped close across CLI, library, API, and proxied-server paths;
4. cleanup of source worktrees or integration evidence;
5. production enablement.
