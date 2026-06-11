package doctor

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// DefaultCheckTimeout bounds how long an individual check may run before
// the doctor runner abandons it and records a timed-out advisory error.
// Picked to be long enough that healthy in-tree checks finish well within
// the budget (most complete in milliseconds) while short enough that a
// hung managed-bd / dolt probe does not stall a doctor run for minutes.
const DefaultCheckTimeout = 5 * time.Second

// Report summarizes the results of a doctor run.
type Report struct {
	// Passed is the number of checks with StatusOK.
	Passed int
	// Warned is the number of checks with StatusWarning.
	Warned int
	// Failed is the number of checks with StatusError (any severity).
	Failed int
	// BlockingFailed is the number of failed checks whose Severity is
	// SeverityBlocking — the subset of Failed that should gate dispatch,
	// CLI exit codes, and other automation.
	BlockingFailed int
	// Fixed is the number of checks remediated by --fix.
	Fixed int
	// Results holds the per-check results in the order they ran. Populated
	// by Run so callers that need structured output (e.g. `gc doctor --json`)
	// can project every result without re-running checks.
	Results []*CheckResult
}

// Doctor runs registered health checks and reports results.
type Doctor struct {
	checks []Check
	// CheckTimeout bounds each check's execution. Zero falls back to
	// DefaultCheckTimeout. A check that exceeds the bound is recorded as
	// StatusError + SeverityAdvisory with a "check timed out" message;
	// the runner moves on to the next check rather than blocking.
	// Advisory severity means timeouts do not gate dispatch / exit code,
	// so a slow probe never breaks automation that calls `gc doctor`.
	CheckTimeout time.Duration
}

// Register adds a check to the doctor's check list.
func (d *Doctor) Register(c Check) {
	d.checks = append(d.checks, c)
}

// Run executes all registered checks, streaming results to w as each
// completes. When fix is true, fixable checks that fail are remediated
// and re-run. Returns a summary report whose Results field holds every
// check result in execution order.
func (d *Doctor) Run(ctx *CheckContext, w io.Writer, fix bool) *Report {
	return d.run(ctx, w, fix, true)
}

// RunCollect executes all registered checks without streaming per-check
// output. The returned Report's Results field holds every check result in
// execution order so callers can render structured output (e.g. JSON).
// Fix semantics match Run.
func (d *Doctor) RunCollect(ctx *CheckContext, fix bool) *Report {
	return d.run(ctx, io.Discard, fix, false)
}

func (d *Doctor) run(ctx *CheckContext, w io.Writer, fix, stream bool) *Report {
	// Normalize ctx so individual checks always get a non-nil context with
	// an Output writer set. Done here so both Run and RunCollect benefit
	// — RunCollect routes Output to io.Discard so a check that writes to
	// ctx.Output incidentally won't disturb the JSON-collect path.
	if ctx == nil {
		ctx = &CheckContext{}
	}
	runCtx := *ctx
	if runCtx.Output == nil {
		runCtx.Output = w
	}
	ctx = &runCtx

	timeout := d.CheckTimeout
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}

	r := &Report{}
	for _, c := range d.checks {
		result := runWithTimeout(c, ctx, timeout)

		// Attempt fix if requested and the check supports it. Skip --fix
		// for timed-out checks: we do not know what state the abandoned
		// goroutine left behind, and the fix has no observed failure to
		// remediate.
		if fix && result.Status != StatusOK && !isTimeoutResult(result) && c.CanFix() {
			if err := c.Fix(ctx); err == nil {
				// Re-run to verify the fix worked. Re-uses the same per-
				// check timeout so a check that only hangs on Fix-followup
				// still cannot stall the run.
				result = runWithTimeout(c, ctx, timeout)
				if result.Status == StatusOK {
					result.Fixed = true
				} else {
					result.FixAttempted = true
				}
			} else {
				result.FixError = err.Error()
				result.FixAttempted = true
			}
		}

		if stream {
			printResult(w, result, ctx.Verbose)
			if r, ok := c.(Renderer); ok {
				r.RenderExtras(ctx, w)
			}
		}
		r.Results = append(r.Results, result)

		switch {
		case result.Fixed:
			r.Fixed++
			r.Passed++ // Fixed counts as passed.
		case result.Status == StatusOK:
			r.Passed++
		case result.Status == StatusWarning:
			r.Warned++
		case result.Status == StatusError:
			r.Failed++
			if result.Severity == SeverityBlocking {
				r.BlockingFailed++
			}
		}
	}
	return r
}

// runWithTimeout executes c.Run in a goroutine and returns the first
// result observed within the given timeout. On timeout it synthesizes a
// CheckResult marked StatusError + SeverityAdvisory so the timeout does
// not gate dispatch / exit codes; an abandoned goroutine eventually
// finishes on its own (the doctor is one-shot CLI; the process exits
// soon afterward).
func runWithTimeout(c Check, ctx *CheckContext, timeout time.Duration) *CheckResult {
	done := make(chan *CheckResult, 1)
	go func() {
		// A panicking check would otherwise take the whole doctor down
		// silently — recover so the run can continue, surface the panic
		// as an error result, and let the operator see it.
		defer func() {
			if rec := recover(); rec != nil {
				done <- &CheckResult{
					Name:     c.Name(),
					Status:   StatusError,
					Severity: SeverityAdvisory,
					Message:  fmt.Sprintf("check panicked: %v", rec),
				}
			}
		}()
		done <- c.Run(ctx)
	}()
	select {
	case result := <-done:
		return result
	case <-time.After(timeout):
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusError,
			Severity: SeverityAdvisory,
			Message:  fmt.Sprintf("check timed out after %s", timeout),
			FixHint:  "rerun `gc doctor`; if the timeout persists, the underlying probe (managed-bd, dolt server, or controller socket) is likely unreachable",
		}
	}
}

// isTimeoutResult reports whether a CheckResult was synthesized by
// runWithTimeout in response to a timeout. Used to skip --fix attempts
// for timed-out checks.
func isTimeoutResult(r *CheckResult) bool {
	if r == nil {
		return false
	}
	return r.Status == StatusError && r.Severity == SeverityAdvisory &&
		strings.HasPrefix(r.Message, "check timed out")
}

// printResult writes a single check result line to w.
func printResult(w io.Writer, r *CheckResult, verbose bool) {
	var icon string
	switch {
	case r.Fixed:
		icon = "✓" // Fixed shows as pass.
	case r.Status == StatusOK:
		icon = "✓"
	case r.Status == StatusWarning:
		icon = "⚠"
	case r.Status == StatusError:
		icon = "✗"
	}

	suffix := ""
	if r.Fixed {
		suffix = " (fixed)"
	}
	advisorySuffix := ""
	if r.Status != StatusOK && !r.Fixed && r.Severity == SeverityAdvisory {
		advisorySuffix = " (advisory)"
	}
	fmt.Fprintf(w, "  %s %s — %s%s%s\n", icon, r.Name, r.Message, advisorySuffix, suffix) //nolint:errcheck // best-effort output
	if verbose {
		for _, d := range r.Details {
			fmt.Fprintf(w, "      %s\n", d) //nolint:errcheck // best-effort output
		}
	}
	if r.FixError != "" && r.Status != StatusOK && !r.Fixed {
		fmt.Fprintf(w, "      fix failed: %s\n", r.FixError) //nolint:errcheck // best-effort output
	} else if r.FixAttempted && r.Status != StatusOK && !r.Fixed {
		fmt.Fprintf(w, "      fix attempted; check still failing\n") //nolint:errcheck // best-effort output
	}
	if r.FixHint != "" && r.Status != StatusOK && !r.Fixed {
		fmt.Fprintf(w, "      hint: %s\n", r.FixHint) //nolint:errcheck // best-effort output
	}
}

// PrintSummary writes the final summary line to w.
func PrintSummary(w io.Writer, r *Report) {
	parts := []string{}
	if r.Passed > 0 {
		parts = append(parts, fmt.Sprintf("%d passed", r.Passed))
	}
	if r.Warned > 0 {
		parts = append(parts, fmt.Sprintf("%d warnings", r.Warned))
	}
	if r.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", r.Failed))
	}
	if advisory := r.Failed - r.BlockingFailed; advisory > 0 {
		parts = append(parts, fmt.Sprintf("%d advisory", advisory))
	}
	if r.Fixed > 0 {
		parts = append(parts, fmt.Sprintf("%d fixed", r.Fixed))
	}
	if len(parts) == 0 {
		fmt.Fprintln(w, "\nNo checks ran.") //nolint:errcheck // best-effort output
		return
	}
	fmt.Fprintf(w, "\n") //nolint:errcheck // best-effort output
	for i, p := range parts {
		if i > 0 {
			fmt.Fprintf(w, ", ") //nolint:errcheck // best-effort output
		}
		fmt.Fprintf(w, "%s", p) //nolint:errcheck // best-effort output
	}
	fmt.Fprintf(w, "\n") //nolint:errcheck // best-effort output
}
