package main

// The revision fence for gated work-record closes on the bd passthrough.
//
// runWorkRecordCloseGate evaluates the shipped-close contract against a row
// it READ; doBd then execs the original bd argv, and bd mutates whatever row
// exists at write time. Nothing joined the two: a concurrent writer could
// change type, outcome, or evidence between the evaluation and the
// subprocess close, so the verdict was about a row that no longer existed
// (finding gas-dq28, important; recorded and deferred by the Task 2
// rereview). The Task 6 stale-revision coverage never touched this route —
// it drove the ConditionalWriter directly, which is exactly the protection
// the passthrough lacked.
//
// The join is bd's own --if-revision fence, the flag BdStore's *IfMatch
// verbs already ride: after the gate passes, the exact revision it evaluated
// is appended to the argv that is exec'd, so bd refuses the write if the row
// moved. Capability comes from the store's existing detection
// (beads.ProbeConditionalWriteSupport — the same memoized four-verb --help
// probe and runtime latch the fenced store verbs consult), never from a
// second probe or a re-spelled flag string.
//
// # Scope: exactly what the gate evaluated
//
// Only invocations workRecordCloseTargets recognizes are considered, and
// within those only targets whose evaluated row (or its projected
// prospective state, for the atomic update --status=closed form) is inside
// the work-record contract. Everything else — every non-close verb, every
// close of a control/exempt bead — execs byte-identically, probes nothing,
// and works against any bd. That keeps the blast radius to the population
// whose close the gate actually judged.
//
// # Fail-closed when the fence cannot be applied
//
// Under enforcement, a gated close whose write cannot be fenced — bd too old
// to parse --if-revision, the capability probe failed, the runtime
// unsupported latch is set, the evaluated row was unreadable, or the batch
// shape cannot carry one fence per row — is REFUSED with the obstacle and
// the remedy named, never executed unfenced: an unfenced close could land
// after a concurrent change invalidates the very validation that admitted
// it. Under the bounded beads.shipped_close_warn_only compatibility mode the
// close proceeds unfenced exactly as it did before the fence existed, but
// says so on stderr — the silent unfenced write is the shape this seam
// exists to remove.
//
// # Cost
//
// The capability probe runs only when a gated close is actually about to
// exec: four bd <verb> --help subprocesses, memoized on the store instance
// the write-ID guard already opened for this invocation. Non-gated commands
// pay nothing.

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/workclose"
)

// applyWorkRecordCloseFence joins the close gate's verdict to the bd
// subprocess mutation. evaluated is the write-ID guard's pre-fetched rows —
// the exact rows the gate validated (it prefers preFetched over its own
// re-read, pinned by TestEvaluateWorkRecordCloseGateUsesPreFetchedBead) — so
// the appended fence is the revision the policy actually judged. It returns
// the argv to exec and whether the close must be refused instead.
func applyWorkRecordCloseFence(bdArgs []string, store beads.Store, evaluated map[string]beads.Bead, enforce bool, stderr io.Writer) ([]string, bool) {
	ids, isClose := workRecordCloseTargets(bdArgs)
	if !isClose {
		return bdArgs, false
	}

	warnUnfenced := func(subject, cause string) {
		fmt.Fprintf(stderr, "gc bd: work-record gate (warn-only): close of %s proceeds without a revision fence (%s); a concurrent modification between validation and the bd write will not be detected\n", subject, cause) //nolint:errcheck // best-effort stderr
	}

	gated := make([]string, 0, len(ids))
	unknown := make([]string, 0, len(ids))
	for _, id := range ids {
		current, have := evaluated[id]
		if !have {
			unknown = append(unknown, id)
			continue
		}
		if workRecordCloseFenceGated(current, bdArgs) {
			gated = append(gated, id)
		}
	}
	if len(gated) == 0 && len(unknown) == 0 {
		// Every target is outside the work-record contract: the passthrough
		// stays byte-identical, whatever bd is installed.
		return bdArgs, false
	}
	if len(unknown) > 0 {
		// The gate could not read these rows. Under enforcement it already
		// refused (unreachable here, kept as defense in depth); under
		// warn-only it proceeded on telemetry, and a fence without the
		// evaluated revision has nothing truthful to carry.
		if enforce {
			fmt.Fprintf(stderr, "gc bd: work-record gate (enforced): close of %s refused: the authoritative row was not readable when the close was validated, so the write cannot be fenced on the validated revision; retry the command\n", strings.Join(unknown, ", ")) //nolint:errcheck // best-effort stderr
			return bdArgs, true
		}
		warnUnfenced(strings.Join(ids, ", "), "the authoritative row was not readable at validation time")
		return bdArgs, false
	}
	if len(ids) > 1 {
		// --if-revision fences exactly one revision; a batched close cannot
		// join each validation to its own write, and splitting the exec here
		// would invent partial-failure semantics bd's exit contract cannot
		// express (the same reason the class door refuses batches).
		if enforce {
			fmt.Fprintf(stderr, "gc bd: work-record gate (enforced): closing %d work records in one bd invocation refused: %s fences exactly one revision, so a batched close cannot join each validation to its write; close each bead in its own invocation\n", len(ids), beads.ConditionalWriteFenceFlag) //nolint:errcheck // best-effort stderr
			return bdArgs, true
		}
		warnUnfenced(strings.Join(ids, ", "), "a batched close cannot be revision-fenced")
		return bdArgs, false
	}

	id := ids[0]
	revision := evaluated[id].Revision
	fence := strconv.FormatInt(revision, 10)
	if supplied, present := workRecordCloseFenceInArgs(bdArgs); present {
		// The operator brought their own fence. Matching the validated
		// revision it is exactly the fence this seam would add — pass it
		// through untouched, never doubled. A different value would let bd
		// admit a write on a row state the gate never judged, so under
		// enforcement it is refused rather than silently overridden.
		if strings.TrimSpace(supplied) == fence {
			return bdArgs, false
		}
		if enforce {
			fmt.Fprintf(stderr, "gc bd: work-record gate (enforced): close of %s refused: %s %s does not match revision %d, the row this invocation validated; drop the flag to let gc fence the close on the validated revision, or re-run with that revision\n", id, beads.ConditionalWriteFenceFlag, supplied, revision) //nolint:errcheck // best-effort stderr
			return bdArgs, true
		}
		warnUnfenced(id, fmt.Sprintf("the supplied %s %s differs from validated revision %d", beads.ConditionalWriteFenceFlag, supplied, revision))
		return bdArgs, false
	}

	if capable, reason := beads.ProbeConditionalWriteSupport(store); !capable {
		if enforce {
			fmt.Fprintf(stderr, "gc bd: work-record gate (enforced): close of %s refused: validated against revision %d, but this scope's bd cannot fence the write (%s); an unfenced close could land after a concurrent change invalidates that validation. Upgrade bd to a build that supports %s, or set beads.shipped_close_warn_only = true to temporarily proceed unfenced.\n", id, revision, reason, beads.ConditionalWriteFenceFlag) //nolint:errcheck // best-effort stderr
			return bdArgs, true
		}
		warnUnfenced(id, "bd cannot fence the write: "+reason)
		return bdArgs, false
	}

	// Copy before appending: bdArgs aliases the caller's slice, and the fence
	// belongs to this one exec, not to any later reuse of the argv.
	fenced := make([]string, 0, len(bdArgs)+2)
	fenced = append(fenced, bdArgs...)
	fenced = append(fenced, beads.ConditionalWriteFenceFlag, fence)
	return fenced, false
}

// workRecordCloseFenceGated reports whether the close of this evaluated row
// is inside the work-record contract — the same question the gate's policy
// answers (workclose.Evaluate over current OR prospective), asked with the
// same argv projection, so the fence covers exactly the closes the gate
// judged and nothing else.
func workRecordCloseFenceGated(current beads.Bead, bdArgs []string) bool {
	if workclose.AppliesTo(current) {
		return true
	}
	prospective, err := applyWorkRecordUpdateMetadata(current, bdArgs)
	if err != nil {
		// The gate saw the identical projection failure as a violation; under
		// enforcement it refused before this ran. Staying conservative here
		// keeps warn-only from silently exempting an unprojectable close.
		return true
	}
	prospective.Status = "closed"
	return workclose.AppliesTo(prospective)
}

// workRecordCloseFenceInArgs reports an operator-supplied --if-revision and
// its value, in both spellings bd accepts, stopping at the `--` terminator
// like the mutation scanners. (Today the write-ID guard's flag scanner
// rejects an unrecognized --if-revision before doBd reaches the fence, so
// this is defense in depth for the day bd's manifest grows the flag.)
func workRecordCloseFenceInArgs(bdArgs []string) (string, bool) {
	for i := 1; i < len(bdArgs); i++ {
		arg := bdArgs[i]
		if arg == "--" {
			break
		}
		if value, ok := strings.CutPrefix(arg, beads.ConditionalWriteFenceFlag+"="); ok {
			return value, true
		}
		if arg == beads.ConditionalWriteFenceFlag {
			if i+1 < len(bdArgs) {
				return bdArgs[i+1], true
			}
			return "", true
		}
	}
	return "", false
}
