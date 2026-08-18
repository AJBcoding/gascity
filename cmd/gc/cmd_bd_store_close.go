package main

// The in-process work-record close for `gc bd` on a non-bd beads provider.
//
// `gc bd` is the managed front door for shipped-close policy: the packs
// render `gc bd close <id>` and `gc bd update <id> … --status closed` into
// every worker, and Task 4 routed every graph/formula close through this
// seam so the work-record gate sees each close before it lands. On a
// bd-backed scope the gate evaluates and the subprocess mutates. On a stock
// non-bd scope (a file-provider city, the SDK's own default-for-dev shape)
// there is no subprocess to hand the mutation to — and until this file, doBd
// rejected the whole command with the bd-provider error BEFORE the gate ran.
// The Task 6 qualification matrix proved the policy/store composition on
// FileStore, but the command the packs actually run bounced off the front
// door (finding gas-dq28): a "supported" route every configured worker was
// told to use and none could complete.
//
// So the close verbs the gate recognizes — and ONLY those — are served here,
// in process, with the same read → evaluate → revision-fenced write shape the
// class-store door (cmd_bd_by_id.go) and the API close handlers
// (internal/api/huma_handlers_beads.go) already implement:
//
//	resolve the store → Get the authoritative row (capturing Revision) →
//	project the prospective state → evaluate the shared close policy →
//	CloseIfMatch/UpdateIfMatch fenced on that exact revision.
//
// The fence is the point, not an optimization. The gate's verdict is about
// the row it read; an unfenced write after it could land on a row a
// concurrent writer changed in between, which is precisely the policy/write
// race the bd passthrough closes with --if-revision
// (work_record_close_fence.go). A store that cannot fence therefore gets a
// typed refusal, never a fallback to the unconditional Close/Update.
//
// # What is deliberately NOT served
//
//   - Every non-close verb. list/show/create/reopen/… keep the bd-provider
//     rejection byte-identical: this file exists to make the CLOSE contract
//     hold, not to grow a second bd implementation, and the pinned rejection
//     tests (TestGcBdRejectsNonBdProvider and friends) stay meaningful.
//   - A batched close. One --if-revision fences one row, so a multi-ID close
//     would need per-id writes with partial-failure semantics bd's exit
//     contract cannot express — the same reason parseBdByIDCloseArgs refuses
//     batches on the class door. Refused with the remedy named.
//   - Spellings the object model cannot represent (--notes, --reason-file,
//     --claim-next, …). Serving one by dropping it would report a command as
//     executed after silently changing what it meant; the refusal names the
//     flag, exactly like the class door's refusals. The one FileStore-specific
//     extension is `close <id> --reason <text>`: close_reason metadata and the
//     closed status fit in one UpdateOpts and therefore one fenced write.
//
// Both refusal classes were plain rejections before this file, so nothing
// that used to work stops working; they only exchange the misleading
// "bd-backed providers" error for one that names the actual obstacle.

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/workclose"
)

// maybeDoBdStoreClose serves a work-record close invocation in process when
// the scope's beads provider is not bd-backed. handled=false means the
// invocation is not a close the gate recognizes and the caller's existing
// provider rejection stands unchanged.
func maybeDoBdStoreClose(cityPath string, cfg *config.City, target execStoreTarget, provider string, bdArgs []string, stdout, stderr io.Writer) (int, bool) {
	ids, isClose := workRecordCloseTargets(bdArgs)
	if !isClose {
		return 0, false
	}
	if len(ids) != 1 {
		fmt.Fprintf(stderr, "gc bd: managed close on the %q beads provider closes one bead per invocation (%d given): a batch cannot fence each write on the revision its validation read; close each bead separately\n", provider, len(ids)) //nolint:errcheck // best-effort stderr
		return 1, true
	}

	// Parse through the class door's own served parsers so the two in-process
	// close surfaces cannot drift: what they serve, this serves; what they
	// refuse (a flag with no UpdateOpts representation), this refuses with the
	// same flag named.
	var (
		op       bdByIDOp
		rejected string
		ok       bool
	)
	switch bdArgs[0] {
	case "close":
		op, rejected, ok = parseBdStoreCloseArgs(bdArgs[1:])
	case "update":
		op, rejected, ok = parseBdByIDUpdateArgs(bdArgs[1:])
	}
	if !ok {
		because := "this spelling is not one the in-process route serves"
		if rejected != "" {
			because = fmt.Sprintf("%s has no representation in the managed close contract", rejected)
		}
		fmt.Fprintf(stderr, "gc bd: managed close on the %q beads provider serves `gc bd %s` in process, and %s; refusing rather than silently dropping it\n", provider, bdArgs[0], because) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	// Defensive scanner-agreement check: workRecordCloseTargets and the served
	// parser read the same argv with different scanners. If they ever disagree
	// on the subject or on whether this closes at all, no write happens — the
	// caller's provider rejection is the safe answer.
	closing := op.Verb == bdByIDClose ||
		(op.Verb == bdByIDUpdate && op.Update.Status != nil && strings.EqualFold(strings.TrimSpace(*op.Update.Status), "closed"))
	if !closing || op.ID != ids[0] {
		return 0, false
	}

	store, err := openStoreAtForCityWithConfig(target.ScopeRoot, cityPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd %s: opening the %q beads store for %s: %v\n", bdStoreCloseVerb(op), provider, target.ScopeRoot, err) //nolint:errcheck // best-effort stderr
		return 1, true
	}
	storeRef := workRecordCanonicalStoreRef(target.ScopeRoot, cityPath, cfg)
	return doBdStoreCloseResolved(op, store, target.ScopeRoot, cityPath, storeRef, provider, cfg, stdout, stderr), true
}

const bdStoreCloseReasonMetadataKey = "close_reason"

// parseBdStoreCloseArgs is deliberately narrower than bd's close parser and
// deliberately separate from parseBdByIDCloseArgs. The shared by-ID parser
// feeds GraphStore.Close, which cannot carry metadata; teaching it --reason
// would silently broaden the graph surface. This FileStore route can represent
// the exact pack spelling atomically as UpdateIfMatch(status + close_reason).
// Short, equals, file, session, workflow, and batch spellings remain refused.
func parseBdStoreCloseArgs(args []string) (op bdByIDOp, rejected string, ok bool) {
	op = bdByIDOp{Verb: bdByIDClose}
	reasonSeen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			op.JSON = true
		case arg == "--reason":
			if reasonSeen || i+1 >= len(args) {
				return bdByIDOp{}, "--reason", false
			}
			i++
			reason := strings.TrimSpace(args[i])
			if reason == "" {
				return bdByIDOp{}, "--reason", false
			}
			reasonSeen = true
			closed := "closed"
			op.Update = beads.UpdateOpts{
				Status:   &closed,
				Metadata: beads.StringMap{bdStoreCloseReasonMetadataKey: reason},
			}
		case strings.HasPrefix(arg, "-"):
			name, _, _ := strings.Cut(arg, "=")
			return bdByIDOp{}, name, false
		default:
			if op.ID != "" || arg == "" {
				return bdByIDOp{}, "", false
			}
			op.ID = arg
		}
	}
	if op.ID == "" {
		return bdByIDOp{}, "", false
	}
	return op, "", true
}

// doBdStoreCloseResolved is the store-driven core of the managed close, split
// from the argv/refusal wrapper so it is unit-testable with an injected
// store. It mirrors doBdByIDClose/doBdByIDUpdate one seam lower: the policy
// runs against the exact row that was read, and the write is fenced on that
// row's revision so the two cannot be about different states.
func doBdStoreCloseResolved(op bdByIDOp, store beads.Store, scopeRoot, cityPath, storeRef, provider string, cfg *config.City, stdout, stderr io.Writer) int {
	verb := bdStoreCloseVerb(op)
	current, err := store.Get(op.ID)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd %s: %s: %v\n", verb, op.ID, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	reasonedClose := op.Verb == bdByIDClose && op.Update.Status != nil && len(op.Update.Metadata) == 1 && op.Update.Metadata[bdStoreCloseReasonMetadataKey] != ""
	alreadyClosedReasonReplay := reasonedClose && strings.EqualFold(strings.TrimSpace(current.Status), "closed")
	prospective := workclose.ProjectClose(current)
	if op.Verb == bdByIDUpdate || (reasonedClose && !alreadyClosedReasonReplay) {
		prospective = workclose.ProjectUpdate(current, op.Update)
	}
	// scopeRoot is the repo fallback for commit-reachability, matching the
	// passthrough gate's rule: gc.work_dir when the record carries it, the
	// mutated scope otherwise.
	//
	// The compatibility target is this store, not the city setting: the write
	// below is an in-process revision-fenced write through the store this route
	// holds, so a natively fencing provider (the FileStore this route exists
	// for) stays strictly enforced even while a bd-backed city has
	// beads.shipped_close_warn_only on. It has no compatibility need — it can
	// fence.
	if evaluateResolvedWorkCloseForRoute("gc.bd.store", current, prospective, scopeRoot, storeRef, cityPath, cfg, workRecordCloseTargetForStore(store), stderr) {
		return 1
	}
	// A reason belongs to the transition that first closed the row. Replaying a
	// completed command may report the durable row but must not replace its
	// audit reason or bump its revision.
	if alreadyClosedReasonReplay {
		return printBdByIDBead(current, op.JSON, fmt.Sprintf("the %q beads provider store", provider), stdout, stderr)
	}
	writer, ok := bdStoreCloseConditionalWriter(store)
	if !ok {
		fmt.Fprintf(stderr, "gc bd %s: %s refused: the %q beads store does not support revision-fenced writes, and the work-record close contract forbids an unfenced close (the row could change between validation and the write)\n", verb, op.ID, provider) //nolint:errcheck // best-effort stderr
		return 1
	}
	if op.Verb == bdByIDUpdate || reasonedClose {
		err = writer.UpdateIfMatch(op.ID, current.Revision, op.Update)
	} else {
		err = writer.CloseIfMatch(op.ID, current.Revision)
	}
	if err != nil {
		switch {
		case beads.IsPreconditionFailed(err):
			// The retryable concurrent-modification answer, in the same words
			// the class door and the API conflict response use.
			fmt.Fprintf(stderr, "gc bd %s: %s changed concurrently; retry the command\n", verb, op.ID) //nolint:errcheck // best-effort stderr
		case errors.Is(err, beads.ErrConditionalWriteUnsupported):
			// The store advertised ConditionalWriter but refused the fence at
			// write time (e.g. FileStore.DisableConditionalWrites). Same
			// fail-closed verdict as the missing-capability arm above.
			fmt.Fprintf(stderr, "gc bd %s: %s refused: the %q beads store rejected the revision-fenced write (%v); the work-record close contract forbids retrying it unfenced\n", verb, op.ID, provider, err) //nolint:errcheck // best-effort stderr
		default:
			fmt.Fprintf(stderr, "gc bd %s: %s: %v\n", verb, op.ID, err) //nolint:errcheck // best-effort stderr
		}
		return 1
	}
	written, err := store.Get(op.ID)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd %s: %s was written and could not be re-read: %v\n", verb, op.ID, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	// Re-read and render what the store now holds — doBdByIDUpdate's rule, for
	// its reason: rendering the request back would report what was asked for.
	return printBdByIDBead(written, op.JSON, fmt.Sprintf("the %q beads provider store", provider), stdout, stderr)
}

// bdStoreCloseVerb names the subcommand in operator-facing diagnostics.
func bdStoreCloseVerb(op bdByIDOp) string {
	if op.Verb == bdByIDUpdate {
		return "update"
	}
	return "close"
}

// bdStoreCloseConditionalWriter resolves the store's fenced-write capability,
// following only wrapper-declared resolution targets (the same walk
// workstamp's stamping service uses). The walk matters because doBd's store
// factory wraps every store in the cmd/gc bead-policy layer, which embeds the
// Store interface — the embedded interface does not promote ConditionalWriter,
// and the policy layer's own ConditionalWritesResolveTarget documents that
// fenced writes are meant to resolve against the inner store. The bound is a
// defensive terminator, not a depth anyone legitimately reaches.
func bdStoreCloseConditionalWriter(store beads.Store) (beads.ConditionalWriter, bool) {
	for range 8 {
		if writer, ok := beads.ConditionalWriterFor(store); ok {
			return writer, true
		}
		targeter, ok := store.(beads.ConditionalWritesResolveTargeter)
		if !ok {
			return nil, false
		}
		next := targeter.ConditionalWritesResolveTarget()
		if next == nil || next == store {
			return nil, false
		}
		store = next
	}
	return nil, false
}
