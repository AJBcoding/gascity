package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/shellquote"
)

// bdReadyPoolDemandShell returns the canonical bd ready predicate for
// unassigned, non-epic pool demand routed to target. gc.routed_to is the
// canonical persisted routing key: the graph.v2 stamper and the legacy stamper
// both stamp it on every routable bead, including the workflow root (ga-eld2x
// retired the short-lived gc.run_target wire field). This predicate is the main
// source of truth for "is there work on this routed queue?" that both the
// worker (via EffectiveWorkQuery Tier 3) and the reconciler (via
// EffectivePoolDemandQuery, count-form) ask; diverging the two re-introduces
// the protocol-mismatch class (see the "scale_check ↔ work_query
// correspondence" note in engdocs/architecture/dispatch.md).
//
// target is passed as a positional argument to the outer sh -c command, not
// interpolated into the nested shell body. That keeps routes containing shell
// metacharacters as data instead of executable syntax.
func bdReadyIncludeEphemeralArg(includeEphemeralReady bool) string {
	if includeEphemeralReady {
		return " --include-ephemeral"
	}
	return ""
}

// holdLabelMatchCondsJQ renders the jq boolean expression that tests whether
// the label currently in scope (`.`) is one of the beadmeta.DispatchHoldLabels
// values, e.g. `. == "hold:mayor" or . == "hold:external"`. Shared by every
// jq-based hold filter so the label set is spelled exactly once.
func holdLabelMatchCondsJQ() string {
	conds := make([]string, len(beadmeta.DispatchHoldLabels))
	for i, label := range beadmeta.DispatchHoldLabels {
		conds[i] = `. == "` + label + `"`
	}
	return strings.Join(conds, " or ")
}

// heldLabelCountJQ counts how many beadmeta.DispatchHoldLabels values the FIRST
// row of a work-query result carries. The in_progress serve gate reads it off
// the candidate row it already holds, so the hold check costs no extra bd call.
// `.[0].labels // []` absorbs both an absent row and bd's null-valued labels
// field; a non-JSON payload makes jq fail and the gate falls back to 0, which is
// the fail-open behavior TestInProgressTierServesUnparseableHeldCandidateFailOpen
// pins.
func heldLabelCountJQ() string {
	return `[ (.[0].labels // [])[] | select(` + holdLabelMatchCondsJQ() + `) ] | length`
}

// jqMeta renders the jq expression that reads a bead-metadata key with an
// empty-string default, e.g. (.metadata["gc.routed_to"] // ""). Shell/jq
// builders use it so embedded key spellings stay anchored to the beadmeta
// vocabulary constants.
func jqMeta(key string) string {
	return `(.metadata["` + key + `"] // "")`
}

func bdReadyPoolDemandShell(limitFlag string, includeEphemeralReady bool) string {
	return `bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --metadata-field "` + beadmeta.RoutedToMetadataKey + `=$target" --unassigned --exclude-type=epic --json ` + limitFlag
}

// bdReadyPoolDemandMigrationShell is a temporary raw compatibility probe for
// graph.v2 workflow roots created before gc.routed_to root stamping shipped.
// It is scoped to workflow roots so gc.run_target remains an authoring hint
// everywhere else. Callers must pass its output through
// poolDemandMigrationFilterJQ so a stale divergent gc.run_target cannot remain
// visible once a root carries gc.routed_to. This retirement-window fallback
// requires jq in the default worker/reconciler environment; remove it with the
// Go-side legacy candidates after the backfill completion tracked by ga-dhf44.
func bdReadyPoolDemandMigrationShell(limitFlag string, includeEphemeralReady bool) string {
	return `bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --metadata-field "` + beadmeta.RunTargetMetadataKey + `=$target" --metadata-field "` + beadmeta.KindMetadataKey + `=` + beadmeta.KindWorkflow + `" --unassigned --exclude-type=epic --json --sort oldest ` + limitFlag
}

func poolDemandMigrationFilterJQ(limit int) string {
	filter := `[.[] | select(` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == "")]`
	if limit > 0 {
		filter += ` | .[:` + strconv.Itoa(limit) + `]`
	}
	return shellquote.Join([]string{"jq", filter})
}

func bdQueryEphemeralStatusShell(status string) string {
	return `bd query --json ` + shellquote.Quote("ephemeral=true AND status="+status) + ` --limit=0`
}

func bdQueryEphemeralStatusQuietShell(status string) string {
	return bdQueryEphemeralStatusShell(status) + ` 2>/dev/null`
}

func legacyEphemeralReadyFilterJQ(selector string, limit int) string {
	filter := `[.[] | ` + selector +
		` | select(((.issue_type // .type // "") != "epic"))` +
		` | select(([ (.dependencies // [])[]` +
		` | select((.type // .dep_type // "") as $t | ($t == "blocks" or $t == "waits-for" or $t == "conditional-blocks"))` +
		` | select((.status // .depends_on_status // "") != "closed") ] | length) == 0)]` +
		` | sort_by(.created_at // "")`
	if limit > 0 {
		filter += ` | .[:` + strconv.Itoa(limit) + `]`
	}
	return filter
}

func legacyEphemeralPoolDemandShell(limit int, includeEphemeralReady, quiet bool) string {
	if includeEphemeralReady {
		return `printf "[]"`
	}
	filter := legacyEphemeralReadyFilterJQ(
		`select((.assignee // "") == "")`+
			` | select((`+jqMeta(beadmeta.RoutedToMetadataKey)+` == $target) or ((`+jqMeta(beadmeta.RoutedToMetadataKey)+` == "") and (`+jqMeta(beadmeta.RunTargetMetadataKey)+` == $target) and (`+jqMeta(beadmeta.KindMetadataKey)+` == "`+beadmeta.KindWorkflow+`")))`,
		limit,
	)
	if quiet {
		// The quiet form is the work-query path, where the assigned-ready probe
		// has already populated the memo with this exact unlimited scan earlier
		// in the same script. Reuse it instead of re-running bd: the query is
		// byte-identical, every bd exec costs a flat ~0.16s, and `gc hook` runs
		// the whole script once per store (kit-r0e2).
		//
		// The scan-then-jq shape is load-bearing and must survive: bd list
		// cannot see ephemeral beads at all, so this unfiltered scan piped into
		// jq is the only thing that surfaces patrol wisps. Only the redundant
		// second scan is elided; the filter stays client-side.
		//
		// The memo is read across the $( ) the caller wraps this in — a write
		// inside that subshell would not reach the parent — so this pays only
		// when the assigned-ready probe ran first. It is still correct if it did
		// not: ephemeralScanMemoShell populates the var lazily on first use.
		return ephemeralScanMemoShell(ephemeralOpenMemoVar, "open") +
			`{ printf "%s" "$` + ephemeralOpenMemoVar + `" | jq --arg target "$target" ` +
			shellquote.Quote(filter) + ` 2>/dev/null; } || printf "[]"`
	}
	return `{ ` + bdQueryEphemeralStatusShell("open") + ` | jq --arg target "$target" ` +
		shellquote.Quote(filter) + `; } || printf "[]"`
}

// poolDemandFirstRowFunctionScript emits the work_query Tier 3 function: it
// reads the first ready, unassigned, routed bead for the supplied target,
// prints it, and exits 0. The caller appends a terminal fallthrough
// (printf "[]") for the empty case.
func poolDemandFirstRowFunctionScript(includeEphemeralReady bool) string {
	return `probe_pool_demand() { ` +
		`target="$1"; ` +
		`[ -z "$target" ] && return 1; ` +
		`r=$(` + routedReadyTierCommand(includeEphemeralReady) + `); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`legacy_candidates=$(` + bdReadyPoolDemandMigrationShell("--limit=20", includeEphemeralReady) + ` 2>/dev/null); ` +
		`r=$(printf "%s" "$legacy_candidates" | ` + poolDemandMigrationFilterJQ(1) + ` 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`legacy_ephemeral_candidates=$(` + legacyEphemeralPoolDemandShell(20, includeEphemeralReady, true) + `); ` +
		`r=$(printf "%s" "$legacy_ephemeral_candidates" | jq '.[0:1]' 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`return 1; ` +
		`}; `
}

func routedReadyTierCommand(includeEphemeralReady bool) string {
	// The shared predicate stays order-free so the count-form does no wasted
	// sorting; the worker first-row path asks bd for the oldest candidates.
	// The tier is widened past a single row (limit=20, not limit=1) so a
	// self-blocked head (is_blocked / status==blocked) has Ready routed work
	// behind it to fall through to instead of idle-exiting; the hook layer
	// (filterUnreadyHookCandidates) strips the blocked head from the result.
	return bdReadyPoolDemandShell("--sort oldest --limit=20", includeEphemeralReady) + ` 2>/dev/null`
}

// poolDemandCountShell emits the reconciler count-form for target: it counts
// ready, unassigned, routed demand and prints the array length. It shares the
// canonical and migration predicates with poolDemandFirstRowFunctionScript so
// the reconciler's spawn decision and the worker's claim decision read the
// same demand shape.
//
// Unlike the work_query probe, this form must NOT redirect bd stderr or default
// to zero: a failed `bd ready` has to surface as an error rather than
// masquerade as "no demand", which would silently stop the pool from spawning.
// The && chain ensures any non-zero bd exit short-circuits the whole expression
// (TestEffectiveScaleCheckUsesReadyOnly).
func poolDemandCountShell(target string, includeEphemeralReady bool) string {
	script := `target="$1"; ` +
		`ready_json=$(` + bdReadyPoolDemandShell("--limit 0", includeEphemeralReady) + `) || exit $?; ` +
		`legacy_candidates=$(` + bdReadyPoolDemandMigrationShell("--limit 0", includeEphemeralReady) + `) || exit $?; ` +
		`legacy_json=$(printf "%s" "$legacy_candidates" | ` + poolDemandMigrationFilterJQ(0) + `) || exit $?; ` +
		`legacy_ephemeral_json=$(` + legacyEphemeralPoolDemandShell(0, includeEphemeralReady, false) + `); ` +
		`printf "%s\n%s\n%s\n" "$ready_json" "$legacy_json" "$legacy_ephemeral_json" | jq -s "(add // []) | unique_by(.id) | length"`
	return shellquote.Join([]string{"sh", "-c", script, "--", target})
}

func (a *Agent) poolDemandTarget() string {
	target := a.QualifiedName()
	if a.PoolName != "" {
		target = a.PoolName
	}
	return target
}

func standardAssignedWorkQueryScript(includeEphemeralReady bool) string {
	return standardAssignedInProgressWorkQueryScript(includeEphemeralReady) +
		standardAssignedReadyWorkQueryScript(includeEphemeralReady)
}

func standardAssignedInProgressWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`r=$(bd list --status in_progress --assignee="$id" --json --limit=1 2>/dev/null); ` +
		`if [ -n "$r" ] && [ "$r" != "[]" ]; then ` +
		inProgressBlockedByEnrichmentScript("r") +
		`fi; ` +
		ephemeralAssignedInProgressProbeScript("id", includeEphemeralReady) +
		`done; `
}

// inProgressBlockedByEnrichmentScript hardens the in_progress "crash recovery"
// work-query tier against re-serving a bead that cannot progress.
//
// `bd list --status in_progress` does no readiness computation: unlike
// `bd ready` it emits neither blocked_by nor is_blocked. That makes the
// defensive hook-side filter (filterUnreadyHookCandidates ->
// isDepBlockedHookCandidate) a structural no-op for this tier, because an
// absent blocked_by is correctly read as "not blocked". A step that is
// in_progress + assigned but held by an open gate or an unclosed blocking
// dependency is therefore re-served on every hook tick, forever.
//
// `bd ready` cannot be substituted here: it excludes in_progress by design,
// so it would return nothing and defeat crash recovery entirely. Instead we
// read the candidate's own dependency rows and attach the blocked_by array
// the rest of the pipeline already knows how to interpret. When the candidate
// is blocked we skip it and fall through to the ready-gated tier, so a session
// holding one blocked step can still be served its other ready assigned work.
//
// Only ready-blocking dependency types are considered, matching
// beads.IsReadyBlockingDependencyType; parent-child and tracks edges never
// block readiness. Status interpretation is left to the shared Go filter:
// any non-closed blocker counts.
//
// The same serve gate also skips a candidate parked on a canonical dispatch
// hold (beadmeta.DispatchHoldLabels), read off the candidate row this tier
// already holds — no extra bd call (gas-kg6). A held bead has no blocking
// dependency, so the blocked_by gate alone let it through and the tier
// re-served it on every tick; because the tier short-circuits with `exit 0`,
// that also starved the ready tiers below it, and a worker that correctly
// parked its bead could never reach its own ready queue. The hold dimension is
// the same defect class as the dependency dimension above, so it shares the
// same fall-through.
//
// Scope (ga-5736js): this is the WORK-SERVING decision only. The assignee-scoped
// probes that answer "does a session still need to exist" —
// ephemeralAssignedReadyProbeScript here and filterReadyByAssignee in
// cmd/gc/dispatch_control_ready.go — stay hold-transparent by design so a held
// assignment still keeps its owner visible to demand and recovery accounting.
//
// Enrichment is fail-open: a failed or unparseable `bd show` / `bd list`
// degrades to the stock behavior of serving the candidate unchanged, never to
// dropping it, so a malformed or log-prefixed bd stdout can never disable
// crash recovery. The hold count fails open the same way.
func inProgressBlockedByEnrichmentScript(shellVar string) string {
	const blockingDepsJQ = `[.[0].dependencies[]? | ` +
		`select(.dependency_type == "blocks" or .dependency_type == "waits-for" or ` +
		`.dependency_type == "conditional-blocks") | {id, status}]`
	const openBlockerCountJQ = `[.[] | select(((.status // "") | ascii_downcase) != "closed")] | length`

	const enrichJQ = `map(. + {blocked_by: $bb})`

	v := `$` + shellVar
	// The enriched payload lands in a scratch var derived from shellVar so the
	// candidate itself is never clobbered: if jq fails (non-JSON or
	// log-prefixed `bd list` stdout) the original is served unchanged.
	enrichedVar := shellVar + `_enriched`
	e := `$` + enrichedVar
	return `bid=$(printf "%s" "` + v + `" | jq -r ".[0].id // empty" 2>/dev/null); ` +
		`bb="[]"; ` +
		`[ -n "$bid" ] && bb=$(bd show "$bid" --json 2>/dev/null | ` +
		`jq -c ` + shellquote.Quote(blockingDepsJQ) + ` 2>/dev/null); ` +
		`[ -z "$bb" ] && bb="[]"; ` +
		`nblocked=$(printf "%s" "$bb" | jq -r ` + shellquote.Quote(openBlockerCountJQ) + ` 2>/dev/null); ` +
		`[ -z "$nblocked" ] && nblocked=0; ` +
		`nheld=$(printf "%s" "` + v + `" | jq -r ` + shellquote.Quote(heldLabelCountJQ()) + ` 2>/dev/null); ` +
		`[ -z "$nheld" ] && nheld=0; ` +
		`if [ "$nblocked" = "0" ] && [ "$nheld" = "0" ]; then ` +
		enrichedVar + `=$(printf "%s" "` + v + `" | jq -c --argjson bb "$bb" ` +
		shellquote.Quote(enrichJQ) + ` 2>/dev/null); ` +
		`[ -n "` + e + `" ] && [ "` + e + `" != "[]" ] && ` + shellVar + `="` + e + `"; ` +
		`printf "%s" "` + v + `" && exit 0; ` +
		`fi; `
}

func standardAssignedReadyWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`r=$(bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --assignee="$id" --json --limit=1 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		ephemeralAssignedReadyProbeScript("id", includeEphemeralReady) +
		`done; `
}

func legacyControlAssignedWorkQueryScript(includeEphemeralReady bool) string {
	return legacyControlAssignedInProgressWorkQueryScript(includeEphemeralReady) +
		legacyControlAssignedReadyWorkQueryScript(includeEphemeralReady)
}

func legacyControlAssignedInProgressWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`legacy=""; case "$id" in *control-dispatcher) legacy="${id%control-dispatcher}workflow-control";; esac; ` +
		`for cand in "$id" "$legacy"; do ` +
		`[ -z "$cand" ] && continue; ` +
		`r=$(bd list --status in_progress --assignee="$cand" --json --limit=1 2>/dev/null); ` +
		`if [ -n "$r" ] && [ "$r" != "[]" ]; then ` +
		inProgressBlockedByEnrichmentScript("r") +
		`fi; ` +
		ephemeralAssignedInProgressProbeScript("cand", includeEphemeralReady) +
		`done; ` +
		`done; `
}

func legacyControlAssignedReadyWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`legacy=""; case "$id" in *control-dispatcher) legacy="${id%control-dispatcher}workflow-control";; esac; ` +
		`for cand in "$id" "$legacy"; do ` +
		`[ -z "$cand" ] && continue; ` +
		`r=$(bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --assignee="$cand" --json --limit=1 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		ephemeralAssignedReadyProbeScript("cand", includeEphemeralReady) +
		`done; ` +
		`done; `
}

// Shell variables holding the memoized ephemeral scans. One per status, since
// the two probes scan different statuses and share a script.
const (
	ephemeralInProgressMemoVar = "__gc_eph_in_progress"
	ephemeralOpenMemoVar       = "__gc_eph_open"
)

// ephemeralScanMemoShell emits the shell that populates memoVar with one
// unlimited ephemeral scan for status, at most once per script run.
//
// The assigned-work probes loop over three candidate identities
// ($GC_SESSION_ID, $GC_SESSION_NAME, $GC_ALIAS), and this scan is
// loop-invariant: the identity enters only through the jq filter downstream,
// never through the bd query. Re-running it per identity therefore spent an
// extra subprocess per iteration for a byte-identical result. Every bd exec
// costs a flat ~0.13s of process startup and connection regardless of query
// shape, and `gc hook` runs the whole script once per store, so the identity
// loop alone accounted for 14 of 33 bd execs on an idle agent (kit-021).
//
// The guard keeps the scan LAZY on purpose. Hoisting it to the top of the
// script would dedupe just as well but would add a subprocess to the fast path,
// where an earlier identity already matched and the fallback is never reached
// (TestAssignedInProgressQuerySkipsEphemeralProbeWhenFirstIdentityHasWork).
//
// A failed scan memoizes as empty for the rest of the script instead of being
// retried on the next identity. The probe is best-effort either way — its
// stderr is discarded and a failure already presents as "no work" — so this
// trades an incidental retry for a bounded subprocess count.
func ephemeralScanMemoShell(memoVar, status string) string {
	return `if [ -z "${` + memoVar + `_done:-}" ]; then ` +
		memoVar + `=$(` + bdQueryEphemeralStatusQuietShell(status) + `); ` +
		memoVar + `_done=1; fi; `
}

func ephemeralAssignedInProgressProbeScript(shellVar string, includeEphemeralReady bool) string {
	_ = includeEphemeralReady
	return ephemeralScanMemoShell(ephemeralInProgressMemoVar, "in_progress") +
		`r=$(printf "%s" "$` + ephemeralInProgressMemoVar + `" | ` +
		`jq --arg id "$` + shellVar + `" '[.[] | select((.assignee // "") == $id)] | .[:1]' 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; `
}

func ephemeralAssignedReadyProbeScript(shellVar string, includeEphemeralReady bool) string {
	if includeEphemeralReady {
		return ""
	}
	filter := legacyEphemeralReadyFilterJQ(`select((.assignee // "") == $id)`, 1)
	return ephemeralScanMemoShell(ephemeralOpenMemoVar, "open") +
		`r=$(printf "%s" "$` + ephemeralOpenMemoVar + `" | ` +
		`jq --arg id "$` + shellVar + `" ` + shellquote.Quote(filter) + ` 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; `
}

func poolDemandOriginGateScript() string {
	return `case "$GC_SESSION_ORIGIN" in ` +
		`ephemeral|"") ;; ` +
		`*) exit 0 ;; ` +
		`esac; `
}

func routedPoolWorkQueryProbeScript(includeEphemeralReady bool, targetCount int) string {
	script := poolDemandOriginGateScript() + poolDemandFirstRowFunctionScript(includeEphemeralReady)
	for i := 1; i <= targetCount; i++ {
		script += fmt.Sprintf(`probe_pool_demand "$%d"; `, i)
	}
	return script + `printf "[]"`
}

func routedPoolWorkQueryCommand(includeEphemeralReady bool, targets ...string) string {
	args := []string{"sh", "-c", routedPoolWorkQueryProbeScript(includeEphemeralReady, len(targets)), "--"}
	args = append(args, targets...)
	return shellquote.Join(args)
}

// queryKind names one of the built-in agent query shapes.
type queryKind int

const (
	queryWork queryKind = iota
	queryAssignedInProgress
	queryAssignedReady
	queryRoutedPool
	queryPoolDemand
	queryOnDeath
	queryOnBoot
)

// querySpec describes how one query kind resolves: which user override
// field short-circuits the default, and how the default script is built.
type querySpec struct {
	// override returns the user-supplied command that replaces the
	// default entirely, or "" when the default applies.
	override func(*Agent) string
	// build returns the default command. includeEphemeralReady carries
	// beads.UsesBD105ReadySemantics(); the onDeath/onBoot builders ignore
	// it today and MUST keep ignoring it (S04b invariant I6).
	build func(a *Agent, includeEphemeralReady bool) string
}

// queryTable maps every query kind to its override field and default
// builder. It is populated once at init and only read afterward.
var queryTable = map[queryKind]querySpec{
	queryWork:               {override: func(a *Agent) string { return a.WorkQuery }, build: buildWorkQuery},
	queryAssignedInProgress: {override: func(a *Agent) string { return a.WorkQuery }, build: buildAssignedInProgressQuery},
	queryAssignedReady:      {override: func(a *Agent) string { return a.WorkQuery }, build: buildAssignedReadyQuery},
	queryRoutedPool:         {override: func(a *Agent) string { return a.WorkQuery }, build: buildRoutedPoolQuery},
	queryPoolDemand:         {override: func(a *Agent) string { return a.ScaleCheck }, build: buildPoolDemandQuery},
	queryOnDeath:            {override: func(a *Agent) string { return a.OnDeath }, build: buildOnDeath},
	queryOnBoot:             {override: func(a *Agent) string { return a.OnBoot }, build: buildOnBoot},
}

// effectiveQuery is the single resolver behind every Effective*Query
// accessor: the kind's user override verbatim if set, else the kind's
// default builder.
func (a *Agent) effectiveQuery(kind queryKind, includeEphemeralReady bool) string {
	spec := queryTable[kind]
	if o := spec.override(a); o != "" {
		return o
	}
	return spec.build(a, includeEphemeralReady)
}

// effectiveQueryForBeads resolves a kind using the bd compatibility
// semantics configured for the city.
func (a *Agent) effectiveQueryForBeads(kind queryKind, beads BeadsConfig) string {
	return a.effectiveQuery(kind, beads.UsesBD105ReadySemantics())
}

// EffectiveWorkQuery returns the work query command for this agent.
// If WorkQuery is set, returns it as-is. Otherwise returns the default
// three-tier query with multi-identifier assignee resolution.
//
// Assignee resolution order: $GC_SESSION_ID (bead ID) > $GC_SESSION_NAME
// (tmux session name) > $GC_ALIAS (named identity / qualified name).
// All three are checked so work is found regardless of which identifier
// was used when assigning.
//
// State priority: in_progress+assigned (crash recovery) >
// ready+assigned (pre-assigned) > ready+unassigned+routed_to (pool).
// Executable formula roots can be epic-typed; the bead storage policy decides
// whether those roots are history-backed, no-history, or ephemeral for the
// configured bd compatibility mode. Molecule containers are not routable
// demand.
//
// Parent epics are excluded from the routed (pool) tier only
// (--exclude-type=epic). An unassigned parent epic has no executable spec —
// its semantic is "all children done" — so a pool worker claiming one does
// undefined work (gc-udx; the repro is a routed parent epic, see
// TestEffectiveWorkQuerySkipsEpicLeafScenario). The assigned tiers do NOT
// exclude epics: work already assigned to this agent is owned, and the
// patrol-loop pattern (gastown witness/refinery/deacon) can self-assign an
// epic wisp that the agent must resume after a session restart. Excluding
// epics there silently stranded those wisps (gc hook exited 1 with empty
// output). Roles that need different behavior still opt in via an explicit
// work_query in their agent config; that custom query is returned unchanged
// above.
//
// When the reconciler runs the query for demand detection (no session
// context), all identity vars are empty → assignee tiers skip → only
// the routed_to tier fires to detect new demand.
//
// Tier 3's canonical and migration predicates are shared with
// EffectivePoolDemandQuery so reconciler spawn decisions and worker claim
// decisions stay symmetric.
func (a *Agent) EffectiveWorkQuery() string {
	return a.effectiveQuery(queryWork, false)
}

// EffectiveWorkQueryForBeads returns the default work query using the bd
// compatibility semantics configured for the city.
func (a *Agent) EffectiveWorkQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryWork, beads)
}

func buildWorkQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	legacyTarget := legacyWorkflowControlQualifiedName(target)
	if legacyTarget == "" {
		script := standardAssignedWorkQueryScript(includeEphemeralReady) +
			poolDemandOriginGateScript() +
			poolDemandFirstRowFunctionScript(includeEphemeralReady) +
			`probe_pool_demand "$1"; ` +
			`printf "[]"`
		return shellquote.Join([]string{"sh", "-c", script, "--", target})
	}
	script := legacyControlAssignedWorkQueryScript(includeEphemeralReady) +
		poolDemandOriginGateScript() +
		poolDemandFirstRowFunctionScript(includeEphemeralReady) +
		`probe_pool_demand "$1"; ` +
		`probe_pool_demand "$2"; ` +
		`printf "[]"`
	return shellquote.Join([]string{"sh", "-c", script, "--", target, legacyTarget})
}

// EffectiveAssignedInProgressQuery returns the assigned-in-progress-only command
// for prompt templates that spell out crash recovery as a separate startup tier.
// A custom WorkQuery is treated as the caller-owned full discovery contract, so
// split-tier prompts may run that same custom command in each query slot.
func (a *Agent) EffectiveAssignedInProgressQuery() string {
	return a.effectiveQuery(queryAssignedInProgress, false)
}

// EffectiveAssignedInProgressQueryForBeads returns the assigned-in-progress
// query using the bd compatibility semantics configured for the city.
func (a *Agent) EffectiveAssignedInProgressQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryAssignedInProgress, beads)
}

func buildAssignedInProgressQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	if legacyWorkflowControlQualifiedName(target) != "" {
		return shellquote.Join([]string{"sh", "-c", legacyControlAssignedInProgressWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
	}
	return shellquote.Join([]string{"sh", "-c", standardAssignedInProgressWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
}

// EffectiveAssignedReadyQuery returns the assigned-ready-only command for
// prompt templates that spell out claim-first startup in separate tiers. A
// custom WorkQuery is treated as the caller-owned full discovery contract, so
// split-tier prompts may run that same custom command in each query slot.
func (a *Agent) EffectiveAssignedReadyQuery() string {
	return a.effectiveQuery(queryAssignedReady, false)
}

// EffectiveAssignedReadyQueryForBeads returns the assigned-ready-only query
// using the bd compatibility semantics configured for the city.
func (a *Agent) EffectiveAssignedReadyQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryAssignedReady, beads)
}

func buildAssignedReadyQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	if legacyWorkflowControlQualifiedName(target) != "" {
		return shellquote.Join([]string{"sh", "-c", legacyControlAssignedReadyWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
	}
	return shellquote.Join([]string{"sh", "-c", standardAssignedReadyWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
}

// EffectiveRoutedPoolQuery returns the routed-pool-only command for prompt
// templates that spell out claim-first startup in separate tiers. It is the
// prompt-side counterpart to EffectiveWorkQuery's routed pool tier.
func (a *Agent) EffectiveRoutedPoolQuery() string {
	return a.effectiveQuery(queryRoutedPool, false)
}

// EffectiveRoutedPoolQueryForBeads returns the routed-pool-only command using
// the bd compatibility semantics configured for the city.
func (a *Agent) EffectiveRoutedPoolQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryRoutedPool, beads)
}

func buildRoutedPoolQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	legacyTarget := legacyWorkflowControlQualifiedName(target)
	if legacyTarget == "" {
		return routedPoolWorkQueryCommand(includeEphemeralReady, target)
	}
	return routedPoolWorkQueryCommand(includeEphemeralReady, target, legacyTarget)
}

func legacyWorkflowControlQualifiedName(target string) string {
	target = strings.TrimSpace(target)
	if target == ControlDispatcherAgentName {
		return "workflow-control"
	}
	const suffix = "/" + ControlDispatcherAgentName
	if strings.HasSuffix(target, suffix) {
		return strings.TrimSuffix(target, suffix) + "/workflow-control"
	}
	return ""
}

// EffectiveSlingQuery returns the sling query command template for this agent.
// The template uses {} as a placeholder for the bead ID.
// If SlingQuery is set, returns it as-is. Otherwise returns the default:
// "bd update {} --set-metadata gc.routed_to=<template>"
//
// All agents use metadata-based routing. The reconciler and scale_check
// handle session creation; sling just stamps the target template.
func (a *Agent) EffectiveSlingQuery() string {
	if a.SlingQuery != "" {
		return a.SlingQuery
	}
	return a.DefaultSlingQuery()
}

// DefaultSlingQuery returns the built-in metadata-routing sling query for
// this agent. Callers outside config should prefer this helper over rebuilding
// the command string to preserve the bd boundary invariant.
func (a *Agent) DefaultSlingQuery() string {
	return "bd update {} --set-metadata " + beadmeta.RoutedToMetadataKey + "=" + a.QualifiedName()
}

// EffectivePoolDemandQuery returns the count-form pool-demand query the
// reconciler runs to detect new unassigned routed work. It is the
// reconciler-side counterpart to EffectiveWorkQuery's Tier 3 (the worker
// claim path): both derive their predicates from the same helpers so
// any future change to the pool-demand shape flows to both paths
// simultaneously.
//
// If ScaleCheck is set (user override), it takes precedence and is
// returned as-is. Otherwise the default count-form is returned.
//
// Assigned in-progress work is resumed from session beads, so it must
// not create additional generic pool demand here.
//
// See engdocs/architecture/dispatch.md "scale_check ↔ work_query
// correspondence" and the protocol-mismatch class regression addressed
// by PR #1516.
func (a *Agent) EffectivePoolDemandQuery() string {
	return a.effectiveQuery(queryPoolDemand, false)
}

// EffectivePoolDemandQueryForBeads returns the count-form demand query using
// the bd compatibility semantics configured for the city.
func (a *Agent) EffectivePoolDemandQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryPoolDemand, beads)
}

func buildPoolDemandQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	return poolDemandCountShell(target, includeEphemeralReady)
}

// EffectiveScaleCheck returns the scale check command for this agent.
// Pass-through to EffectivePoolDemandQuery for back-compat with code and
// configs that name the predicate "scale_check"; new call sites should
// prefer EffectivePoolDemandQuery to make the dependency on the
// work_query predicate explicit.
func (a *Agent) EffectiveScaleCheck() string {
	return a.EffectivePoolDemandQuery()
}

// RecoveryHookMarker prefixes every diagnostic the DEFAULT on_death/on_boot
// recovery hooks print to stdout when a bd release fails. It is the contract
// between the generated templates (which emit it) and the controller callers
// (which surface only marked output): a user-supplied on_death/on_boot override
// is passed through verbatim and carries no marker, so its stdout is not
// mislabeled or spammed into the recovery log.
const RecoveryHookMarker = "gc-recovery:"

// EffectiveOnDeath returns the on_death command for this agent.
// If OnDeath is set, returns it. Otherwise returns the default recovery hook
// that unclaims in-progress work assigned to this concrete agent identity.
func (a *Agent) EffectiveOnDeath() string {
	return a.effectiveQuery(queryOnDeath, false)
}

// EffectiveOnDeathForBeads returns the default on_death command using the bd
// compatibility semantics configured for the city.
func (a *Agent) EffectiveOnDeathForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryOnDeath, beads)
}

func buildOnDeath(a *Agent, includeEphemeralInProgress bool) string {
	route := a.QualifiedName()
	if a.PoolName != "" {
		route = a.PoolName
	}
	_ = includeEphemeralInProgress
	ephemeralRead := bdQueryEphemeralStatusQuietShell("in_progress") + ` | ` +
		`jq -r --arg assignee ` + shellquote.Quote(a.QualifiedName()) + ` '.[] | select((.assignee // "") == $assignee) | [.id, ` + jqMeta(beadmeta.RunTargetMetadataKey) + `, ` + jqMeta(beadmeta.RoutedToMetadataKey) + `] | @tsv' 2>/dev/null; `
	// Reset both assignee and status: clearing assignee alone leaves the bead
	// invisible to every work_query tier (Tier 1 needs assignee match, Tiers
	// 2/3 only match "ready" status). The next worker re-claims via Tier 3.
	// If routed metadata is missing entirely, backfill the canonical
	// gc.run_target route so reopened direct-assigned work does not stay
	// invisible.
	return `{ ` +
		`bd list --assignee=` + a.QualifiedName() +
		` --status=in_progress --json 2>/dev/null | ` +
		`jq -r '.[] | [.id, ` + jqMeta(beadmeta.RunTargetMetadataKey) + `, ` + jqMeta(beadmeta.RoutedToMetadataKey) + `] | @tsv' 2>/dev/null; ` +
		ephemeralRead +
		`} | ` +
		`while IFS="$(printf '\t')" read -r id run_target routed_to; do ` +
		`[ -z "$id" ] && continue; ` +
		`if [ -n "$run_target" ] || [ -n "$routed_to" ]; then ` +
		`if ! err=$(bd update "$id" --assignee "" --status open 2>&1 >/dev/null); then printf 'gc-recovery: on_death release failed for %s: %s\n' "$id" "$err"; fi; ` +
		`else if ! err=$(bd update "$id" --assignee "" --status open --set-metadata ` + shellquote.Quote(beadmeta.RunTargetMetadataKey+"="+route) + ` 2>&1 >/dev/null); then printf 'gc-recovery: on_death release failed for %s: %s\n' "$id" "$err"; fi; ` +
		`fi; ` +
		`done`
}

// EffectiveOnBoot returns the on_boot command for this agent.
// If OnBoot is set, returns it. Otherwise returns the default recovery hook
// that unclaims in-progress work routed to this backing config.
func (a *Agent) EffectiveOnBoot() string {
	return a.effectiveQuery(queryOnBoot, false)
}

// EffectiveOnBootForBeads returns the default on_boot command using the bd
// compatibility semantics configured for the city.
func (a *Agent) EffectiveOnBootForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryOnBoot, beads)
}

func buildOnBoot(a *Agent, includeEphemeralInProgress bool) string {
	template := a.QualifiedName()
	if a.PoolName != "" {
		template = a.PoolName
	}
	_ = includeEphemeralInProgress
	ephemeralRead := bdQueryEphemeralStatusQuietShell("in_progress") + ` | ` +
		`jq -r --arg template "$template" '.[] | select((.assignee // "") == "") | select((` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == $template) or ((` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == "") and (` + jqMeta(beadmeta.RunTargetMetadataKey) + ` == $template) and (` + jqMeta(beadmeta.KindMetadataKey) + ` == "` + beadmeta.KindWorkflow + `"))) | .id' 2>/dev/null; `
	return `template=` + shellquote.Quote(template) + `; ` +
		`{ ` +
		`bd list --metadata-field "` + beadmeta.RoutedToMetadataKey + `=$template" --status=in_progress --no-assignee --json 2>/dev/null | ` +
		`jq -r '.[].id' 2>/dev/null; ` +
		`bd list --metadata-field "` + beadmeta.RunTargetMetadataKey + `=$template" --metadata-field "` + beadmeta.KindMetadataKey + `=` + beadmeta.KindWorkflow + `" --status=in_progress --no-assignee --json 2>/dev/null | ` +
		`jq -r '.[] | select(` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == "") | .id' 2>/dev/null; ` +
		ephemeralRead +
		`} | awk 'NF && !seen[$0]++' | ` +
		`xargs -rI{} sh -c 'if ! err=$(bd update "$1" --status open 2>&1 >/dev/null); then printf "gc-recovery: on_boot reopen failed for %s: %s\n" "$1" "$err"; fi' _ {}`
}
