package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// alwaysReachable / neverReachable are injected commit-reachability oracles so
// the work-record validation is testable without a real git repo.
func alwaysReachable(string, string) bool { return true }
func neverReachable(string, string) bool  { return false }

// alwaysOnRemote / neverOnRemote are injected durability oracles: they report
// whether a commit is contained in any remote-tracking ref. Reachability on a
// branch and presence on a remote are independent — a commit on a local-only
// branch is reachable but not durable — so they are separate oracles.
func alwaysOnRemote(string) bool { return true }
func neverOnRemote(string) bool  { return false }

// noRemoteContains / remoteContains are injected containment resolvers: they
// report which remote-tracking branches contain a commit. The stale claim-stamp
// rule (ADR-0009 Defect C) uses them to distinguish a delivered commit whose
// stamped gc.work_branch is merely stale from genuinely undelivered work.
func noRemoteContains(string) []string { return nil }

func remoteContains(branches ...string) func(string) []string {
	return func(string) []string { return branches }
}

func TestValidateWorkRecordOnClose(t *testing.T) {
	tests := []struct {
		name       string
		meta       map[string]string
		reachable  func(string, string) bool
		onRemote   func(string) bool
		containing func(string) []string
		wantViol   string // substring expected in the (single) violation; "" ⇒ no violations
		wantAdv    string // substring expected in the (single) advisory; "" ⇒ no advisories
	}{
		{
			name:     "no-op close passes",
			meta:     map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
			wantViol: "",
		},
		{
			name:     "blocked close passes",
			meta:     map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeBlocked},
			wantViol: "",
		},
		{
			name: "shipped with reachable commit passes",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: alwaysReachable,
			wantViol:  "",
		},
		{
			// az-6n75: the data-loss case. The polecat committed and the commit is
			// reachable on the branch it recorded, but the branch was never pushed,
			// so the work exists only in a worktree that any prune can destroy.
			// Reachability alone cannot see this — the branch resolves locally.
			name: "shipped with commit reachable locally but NOT on any remote is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "gc-gastown.rictus-5bca5afe897d",
			},
			reachable: alwaysReachable,
			onRemote:  neverOnRemote,
			wantViol:  "not present on any remote",
		},
		{
			name: "shipped with commit NOT reachable on branch is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: neverReachable,
			wantViol:  "not reachable",
		},
		{
			// ADR-0009 Defect C: gc.work_branch is stamped at claim time, before
			// the work exists, so an honest delivered close can carry a stale
			// branch. Delivered work (on a remote-tracking ref) must not be
			// blocked for a stale stamp — it gets a precise advisory naming the
			// branch the work actually landed on, so the record can be corrected.
			name: "shipped delivered on another remote branch passes with a stale-stamp advisory",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "gc-gastown.nux-1a2b3c4d5e6f",
			},
			reachable:  neverReachable,
			onRemote:   alwaysOnRemote,
			containing: remoteContains("origin/fix/wr-defect-c"),
			wantViol:   "",
			wantAdv:    "fix/wr-defect-c",
		},
		{
			// The advisory path must not open the az-6n75 hole: an unreachable
			// commit that is on NO remote ref is undelivered work and still
			// violates, stale stamp or not.
			name: "shipped unreachable and on no remote ref stays rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
				beadmeta.WorkBranchMetadataKey:  "gc-gastown.nux-1a2b3c4d5e6f",
			},
			reachable:  neverReachable,
			onRemote:   neverOnRemote,
			containing: noRemoteContains,
			wantViol:   "not reachable",
		},
		{
			name: "shipped without commit is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkBranchMetadataKey:  "bd-x",
			},
			reachable: alwaysReachable,
			wantViol:  beadmeta.WorkCommitMetadataKey,
		},
		{
			name: "shipped without branch is rejected",
			meta: map[string]string{
				beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped,
				beadmeta.WorkCommitMetadataKey:  "abc123",
			},
			reachable: alwaysReachable,
			wantViol:  beadmeta.WorkBranchMetadataKey,
		},
		{
			name:     "missing outcome is rejected",
			meta:     map[string]string{},
			wantViol: "missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			name:     "unknown outcome is rejected",
			meta:     map[string]string{beadmeta.WorkOutcomeMetadataKey: "done"},
			wantViol: "invalid",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reachable := tc.reachable
			if reachable == nil {
				reachable = neverReachable
			}
			onRemote := tc.onRemote
			if onRemote == nil {
				// Default to durable so pre-existing cases exercise only the rule
				// they were written for.
				onRemote = alwaysOnRemote
			}
			containing := tc.containing
			if containing == nil {
				// Default to no containing branches so pre-existing cases keep
				// their original violation semantics.
				containing = noRemoteContains
			}
			bead := beads.Bead{ID: "wr-1", Type: "task", Metadata: tc.meta}
			got, advisories := validateWorkRecordOnClose(bead, reachable, onRemote, containing)
			if tc.wantAdv == "" {
				if len(advisories) != 0 {
					t.Fatalf("expected no advisories, got %v", advisories)
				}
			} else {
				if len(advisories) == 0 {
					t.Fatalf("expected an advisory containing %q, got none", tc.wantAdv)
				}
				if joined := strings.Join(advisories, " | "); !strings.Contains(joined, tc.wantAdv) {
					t.Fatalf("advisory %q does not contain %q", joined, tc.wantAdv)
				}
			}
			if tc.wantViol == "" {
				if len(got) != 0 {
					t.Fatalf("expected no violations, got %v", got)
				}
				return
			}
			if len(got) == 0 {
				t.Fatalf("expected a violation containing %q, got none", tc.wantViol)
			}
			joined := strings.Join(got, " | ")
			if !strings.Contains(joined, tc.wantViol) {
				t.Fatalf("violation %q does not contain %q", joined, tc.wantViol)
			}
		})
	}
}

func TestIsWorkRecordGatedBead(t *testing.T) {
	tests := []struct {
		name string
		bead beads.Bead
		want bool
	}{
		{name: "plain task bead is gated", bead: beads.Bead{Type: "task"}, want: true},
		{name: "empty type defaults to gated", bead: beads.Bead{}, want: true},
		{
			name: "workflow root is not gated",
			bead: beads.Bead{Type: "task", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow}},
			want: false,
		},
		{
			name: "control run step is not gated",
			bead: beads.Bead{Type: "task", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindRun}},
			want: false,
		},
		{name: "convoy bead is not gated", bead: beads.Bead{Type: "convoy"}, want: false},
		{name: "message bead is not gated", bead: beads.Bead{Type: "message"}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWorkRecordGatedBead(tc.bead); got != tc.want {
				t.Fatalf("isWorkRecordGatedBead = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidWorkOutcome(t *testing.T) {
	for _, v := range []string{
		beadmeta.WorkOutcomeShipped, beadmeta.WorkOutcomeNoOp,
		beadmeta.WorkOutcomeBlocked, beadmeta.WorkOutcomeAbandoned,
	} {
		if !validWorkOutcome(v) {
			t.Errorf("validWorkOutcome(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "pass", "fail", "skipped", "done", "SHIPPED"} {
		if validWorkOutcome(v) {
			t.Errorf("validWorkOutcome(%q) = true, want false", v)
		}
	}
}

func TestWorkRecordCloseTargets(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantIDs []string
		wantOK  bool
	}{
		{"close subcommand", []string{"close", "wr-1"}, []string{"wr-1"}, true},
		{"close multiple", []string{"close", "wr-1", "wr-2"}, []string{"wr-1", "wr-2"}, true},
		{"update status=closed", []string{"update", "wr-1", "--status=closed"}, []string{"wr-1"}, true},
		{"update --status closed", []string{"update", "wr-1", "--status", "closed"}, []string{"wr-1"}, true},
		{"update -s closed", []string{"update", "wr-1", "-s", "closed"}, []string{"wr-1"}, true},
		{"last repeated status closes", []string{"update", "wr-1", "--status=open", "--status=closed"}, []string{"wr-1"}, true},
		{"last repeated status stays open", []string{"update", "wr-1", "--status=closed", "--status=open"}, nil, false},
		{"status-looking value is consumed", []string{"update", "wr-1", "--notes", "--status=open", "--status", "closed"}, []string{"wr-1"}, true},
		{"update to open is not a close", []string{"update", "wr-1", "--status=open"}, nil, false},
		{"update without status is not a close", []string{"update", "wr-1", "--notes", "x"}, nil, false},
		{"read subcommand is not a close", []string{"show", "wr-1"}, nil, false},
		{"empty args", nil, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ids, ok := workRecordCloseTargets(tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (ids=%v)", ok, tc.wantOK, ids)
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantIDs, ",") {
				t.Fatalf("ids = %v, want %v", ids, tc.wantIDs)
			}
		})
	}
}

// TestEvaluateWorkRecordCloseGate exercises the full gate plumbing (store read,
// scoping, warn vs enforce fork) over an in-memory store, covering ADR-0009
// acceptance (b)/(c) at the integration level.
func TestEvaluateWorkRecordCloseGate(t *testing.T) {
	beadsList := []beads.Bead{
		{ID: "wr-shipped-nocommit", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeShipped}},
		{ID: "wr-noop", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp}},
		{ID: "wr-atomic-noop", Type: "task", Status: "in_progress", Metadata: map[string]string{}},
		{ID: "wr-missing", Type: "task", Status: "in_progress", Metadata: map[string]string{}},
		{ID: "wr-control", Type: "task", Status: "in_progress", Metadata: map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow}},
	}
	newStore := func() beads.Store { return beads.NewMemStoreFrom(1, beadsList, nil) }

	tests := []struct {
		name      string
		args      []string
		enforce   bool
		wantBlock bool
		wantWarn  string // substring expected on stderr; "" ⇒ no output
	}{
		{"non-close subcommand is ignored", []string{"show", "wr-shipped-nocommit"}, true, false, ""},
		{"control bead is exempt", []string{"close", "wr-control"}, true, false, ""},
		{"no-op close passes", []string{"close", "wr-noop"}, true, false, ""},
		{"shipped-no-commit warns only by default", []string{"close", "wr-shipped-nocommit"}, false, false, "work-record gate (warn-only)"},
		{"shipped-no-commit blocks when enforced", []string{"close", "wr-shipped-nocommit"}, true, true, "work-record gate (enforced)"},
		{"missing outcome blocks when enforced", []string{"close", "wr-missing"}, true, true, "missing " + beadmeta.WorkOutcomeMetadataKey},
		{"update --status=closed is gated", []string{"update", "wr-shipped-nocommit", "--status=closed"}, true, true, "close of wr-shipped-nocommit"},
		{
			"atomic update validates submitted metadata",
			[]string{"update", "wr-atomic-noop", "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"metadata JSON validates submitted no-op",
			[]string{"update", "wr-missing", "--metadata", `{"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"metadata equals JSON validates submitted no-op",
			[]string{"update", "wr-missing", `--metadata={"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"last repeated metadata JSON value wins",
			[]string{"update", "wr-missing", `--metadata={"gc.work_outcome":"no-op"}`, `--metadata={"unrelated":"value"}`, "--status=closed"},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			"last repeated metadata JSON ignores an earlier malformed value",
			[]string{"update", "wr-missing", `--metadata={not-json}`, `--metadata={"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			false,
			"",
		},
		{
			"metadata JSON cannot hide shipped evidence requirements behind stored no-op",
			[]string{"update", "wr-noop", `--metadata={"gc.work_outcome":"shipped"}`, "--status=closed"},
			true,
			true,
			beadmeta.WorkCommitMetadataKey,
		},
		{
			"metadata JSON cannot combine with later set-metadata",
			[]string{"update", "wr-noop", `--metadata={"gc.work_outcome":"shipped"}`, "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			true,
			"cannot project metadata",
		},
		{
			"metadata JSON cannot combine with earlier set-metadata",
			[]string{"update", "wr-noop", "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, `--metadata={"gc.work_outcome":"shipped"}`, "--status=closed"},
			true,
			true,
			"cannot project metadata",
		},
		{
			"unset-metadata wins over set-metadata regardless of argv order",
			[]string{"update", "wr-missing", "--unset-metadata", beadmeta.WorkOutcomeMetadataKey, "--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			"metadata JSON cannot combine with unset-metadata",
			[]string{"update", "wr-noop", "--unset-metadata", beadmeta.WorkOutcomeMetadataKey, `--metadata={"gc.work_outcome":"no-op"}`, "--status=closed"},
			true,
			true,
			"cannot project metadata",
		},
		{
			"non-string metadata uses beads StringMap coercion",
			[]string{"update", "wr-noop", `--metadata={"gc.work_outcome":true}`, "--status=closed"},
			true,
			true,
			`invalid gc.work_outcome="true"`,
		},
		{
			"malformed metadata JSON fails closed",
			[]string{"update", "wr-noop", `--metadata={not-json}`, "--status=closed"},
			true,
			true,
			"cannot project --metadata",
		},
		{
			"metadata file input fails closed",
			[]string{"update", "wr-noop", "--metadata", "@work-record.json", "--status=closed"},
			true,
			true,
			"cannot project --metadata",
		},
		{
			"metadata-looking positional after terminator is not projected",
			[]string{"update", "wr-missing", "--status=closed", "--", "--set-metadata=" + beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
		{
			"metadata-like flag value is not submitted metadata",
			[]string{"update", "wr-missing", "--notes", "--set-metadata=" + beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeNoOp, "--status=closed"},
			true,
			true,
			"missing " + beadmeta.WorkOutcomeMetadataKey,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			block := evaluateWorkRecordCloseGate(tc.args, newStore(), t.TempDir(), tc.enforce, &stderr)
			if block != tc.wantBlock {
				t.Fatalf("block = %v, want %v; stderr=%s", block, tc.wantBlock, stderr.String())
			}
			out := stderr.String()
			if tc.wantWarn == "" {
				if out != "" {
					t.Fatalf("expected no gate output, got %q", out)
				}
				return
			}
			if !strings.Contains(out, tc.wantWarn) {
				t.Fatalf("gate output %q does not contain %q", out, tc.wantWarn)
			}
		})
	}
}

func TestEvaluateWorkRecordCloseGateAtomicShippedUpdate(t *testing.T) {
	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	artifactPath := filepath.Join(repoDir, "artifact.txt")
	if err := os.WriteFile(artifactPath, []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	runGit(t, repoDir, "add", "artifact.txt")
	runGit(t, repoDir, "commit", "-m", "test: integrate artifact")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID:     "wr-atomic-shipped",
		Type:   "task",
		Status: "in_progress",
		Metadata: map[string]string{
			beadmeta.WorkDirMetadataKey: repoDir,
		},
	}}, nil)
	args := []string{
		"update", "wr-atomic-shipped",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=main",
		"--status=closed",
	}
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, store, repoDir, true, &stderr); block {
		t.Fatalf("valid atomic shipped close blocked; stderr=%s", stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("valid atomic shipped close warned: %q", got)
	}
}

// TestEvaluateWorkRecordCloseGateUnpushedBranch is the az-6n75 regression test.
// It reproduces the kit-ccf incident end to end against a real repo: a polecat
// commits on its own branch, records a correct and internally-consistent work
// record, and closes — but never pushes. Every pre-existing rule passes. The
// commit IS reachable on the branch it names; the branch simply exists nowhere
// but this worktree, so a prune destroys the only copy.
//
// The paired assertion matters as much as the block: after the identical commit
// is pushed, the same close must be allowed. A rule that blocks unpushed work by
// blocking everything would pass a one-sided test.
func TestEvaluateWorkRecordCloseGateUnpushedBranch(t *testing.T) {
	originDir := t.TempDir()
	runGit(t, originDir, "init", "--bare", "--initial-branch=main")

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	runGit(t, repoDir, "remote", "add", "origin", originDir)
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runGit(t, repoDir, "add", "base.txt")
	runGit(t, repoDir, "commit", "-m", "test: base")
	runGit(t, repoDir, "push", "origin", "main")

	// The polecat's own branch, mirroring gc-gastown.<name>-<hash>.
	const workBranch = "gc-gastown.rictus-5bca5afe897d"
	runGit(t, repoDir, "checkout", "-b", workBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("quote stripper\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	runGit(t, repoDir, "add", "feature.txt")
	runGit(t, repoDir, "commit", "-m", "feat: quote stripper stage 1")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))

	newStore := func() beads.Store {
		return beads.NewMemStoreFrom(1, []beads.Bead{{
			ID:     "wr-unpushed",
			Type:   "task",
			Status: "in_progress",
			Metadata: map[string]string{
				beadmeta.WorkDirMetadataKey: repoDir,
			},
		}}, nil)
	}
	args := []string{
		"update", "wr-unpushed",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=" + workBranch,
		"--status=closed",
	}

	// Sanity: the pre-existing reachability rule cannot see this failure. If this
	// ever stops holding, the durability rule is no longer the thing under test.
	if !gitCommitReachableOnBranch(repoDir, commit, workBranch) {
		t.Fatalf("precondition: commit should be reachable on its own branch")
	}

	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, newStore(), repoDir, true, &stderr); !block {
		t.Fatalf("close of unpushed work was allowed; the branch exists on no remote")
	}
	if got := stderr.String(); !strings.Contains(got, "not present on any remote") {
		t.Fatalf("expected a durability violation, got %q", got)
	}

	// Same commit, same close, after publishing: must now be allowed.
	runGit(t, repoDir, "push", "origin", workBranch)
	var pushedStderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, newStore(), repoDir, true, &pushedStderr); block {
		t.Fatalf("close blocked after push; stderr=%s", pushedStderr.String())
	}
	if got := pushedStderr.String(); got != "" {
		t.Fatalf("pushed close warned: %q", got)
	}
}

// TestEvaluateWorkRecordCloseGateStaleClaimStamp is the ADR-0009 Defect C
// regression test. gc.work_branch is stamped at claim time — before the work
// exists — with the branch the claiming worktree happened to be on (the
// polecat's persistent gc-<agent>-<hash> branch, cut from the default branch).
// When the work then lands on a different branch, the stamp is stale, and the
// pre-fix gate reported the delivered commit "not reachable" — under
// GC_WORK_RECORD_ENFORCE that blocks every such honest close citywide, which is
// exactly why az-fuag/az-z4p1 blocked enforcement on this defect.
//
// Contract under test: a shipped commit that IS on a remote-tracking ref must
// close (delivery is the guarantee), with a precise advisory naming the branch
// the work actually landed on so the record can be corrected — while the same
// close with an unpushed commit must still block (the az-6n75 protection).
func TestEvaluateWorkRecordCloseGateStaleClaimStamp(t *testing.T) {
	originDir := t.TempDir()
	runGit(t, originDir, "init", "--bare", "--initial-branch=main")

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	runGit(t, repoDir, "remote", "add", "origin", originDir)
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runGit(t, repoDir, "add", "base.txt")
	runGit(t, repoDir, "commit", "-m", "test: base")
	runGit(t, repoDir, "push", "origin", "main")

	// The claim-time stamp: the polecat worktree branch, cut at base and never
	// advanced. This is what gc.work_branch carries when the close arrives.
	const claimBranch = "gc-gastown.nux-1a2b3c4d5e6f"
	runGit(t, repoDir, "branch", claimBranch)

	// The work lands on a different branch and is pushed — delivered.
	const landedBranch = "fix/wr-defect-c"
	runGit(t, repoDir, "checkout", "-b", landedBranch)
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("stale stamp fix\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	runGit(t, repoDir, "add", "feature.txt")
	runGit(t, repoDir, "commit", "-m", "feat: the work that satisfied the bead")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	runGit(t, repoDir, "push", "origin", landedBranch)

	newStore := func() beads.Store {
		return beads.NewMemStoreFrom(1, []beads.Bead{{
			ID:     "wr-stale-stamp",
			Type:   "task",
			Status: "in_progress",
			Metadata: map[string]string{
				beadmeta.WorkDirMetadataKey: repoDir,
			},
		}}, nil)
	}
	args := []string{
		"update", "wr-stale-stamp",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=" + claimBranch,
		"--status=closed",
	}

	// Precondition: the stale stamp genuinely does not contain the commit, so
	// this test exercises the stale-stamp rule and not reachability.
	if gitCommitReachableOnBranch(repoDir, commit, claimBranch) {
		t.Fatalf("precondition: commit must not be reachable on the claim-time branch")
	}

	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, newStore(), repoDir, true, &stderr); block {
		t.Fatalf("delivered close blocked on a stale claim-time stamp; stderr=%s", stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "advisory") {
		t.Fatalf("expected a stale-stamp advisory, got %q", out)
	}
	if !strings.Contains(out, landedBranch) {
		t.Fatalf("advisory %q does not name the landed branch %q", out, landedBranch)
	}
	if strings.Contains(out, "not reachable") {
		t.Fatalf("delivered close still reported unreachable: %q", out)
	}

	// Paired negative: the same stale-stamp close with an UNPUSHED commit is the
	// az-6n75 data-loss case and must still block under enforcement.
	if err := os.WriteFile(filepath.Join(repoDir, "unpushed.txt"), []byte("only copy\n"), 0o644); err != nil {
		t.Fatalf("write unpushed: %v", err)
	}
	runGit(t, repoDir, "add", "unpushed.txt")
	runGit(t, repoDir, "commit", "-m", "feat: never pushed")
	unpushed := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	unpushedArgs := []string{
		"update", "wr-stale-stamp",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + unpushed,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=" + claimBranch,
		"--status=closed",
	}
	var unpushedStderr strings.Builder
	if block := evaluateWorkRecordCloseGate(unpushedArgs, newStore(), repoDir, true, &unpushedStderr); !block {
		t.Fatalf("close of unpushed work was allowed through the stale-stamp path")
	}
	if got := unpushedStderr.String(); !strings.Contains(got, "not present on any remote") {
		t.Fatalf("expected a durability violation, got %q", got)
	}
}

// TestEvaluateWorkRecordCloseGateStaleLocalRef is the gastownhall/gascity#5037
// finding-1 regression test. In any topology where merges reach the target
// branch over the network (the refinery pushes from a detached worktree),
// nothing ever advances the local ref: refs/heads/main goes permanently stale
// while refs/remotes/origin/main is the truth. The pre-fix gate resolved the
// bare branch name by gitrevisions precedence — the stale local ref — and
// reported genuinely-merged commits unreachable.
//
// Per the issue's own verification guidance: do not verify by the warning
// stopping — assert the check resolves against refs/remotes/origin/<branch> and
// passes for a commit that is on origin/main but NOT on a deliberately-stale
// local main.
func TestEvaluateWorkRecordCloseGateStaleLocalRef(t *testing.T) {
	originDir := t.TempDir()
	runGit(t, originDir, "init", "--bare", "--initial-branch=main")

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "--initial-branch=main")
	runGit(t, repoDir, "config", "user.name", "Gas City Test")
	runGit(t, repoDir, "config", "user.email", "gc-test@test.local")
	runGit(t, repoDir, "remote", "add", "origin", originDir)
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	runGit(t, repoDir, "add", "base.txt")
	runGit(t, repoDir, "commit", "-m", "test: base")
	runGit(t, repoDir, "push", "origin", "main")

	// Land the work on origin/main without moving local main, the way the
	// refinery does: commit on a temporary branch, push it to origin's main,
	// return to the stale local main, and drop the temporary branch so the
	// commit exists locally only via the remote-tracking ref.
	runGit(t, repoDir, "checkout", "-b", "tmp-land")
	if err := os.WriteFile(filepath.Join(repoDir, "landed.txt"), []byte("merged over the network\n"), 0o644); err != nil {
		t.Fatalf("write landed: %v", err)
	}
	runGit(t, repoDir, "add", "landed.txt")
	runGit(t, repoDir, "commit", "-m", "feat: the work that satisfied the bead")
	commit := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	runGit(t, repoDir, "push", "origin", "tmp-land:main")
	runGit(t, repoDir, "checkout", "main")
	runGit(t, repoDir, "branch", "-D", "tmp-land")

	// Preconditions from the issue's repro: the local ref is genuinely stale
	// and the remote-tracking ref genuinely contains the commit.
	if err := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", commit, "refs/heads/main").Run(); err == nil {
		t.Fatalf("precondition: commit must NOT be reachable on the stale local main")
	}
	if err := exec.Command("git", "-C", repoDir, "merge-base", "--is-ancestor", commit, "refs/remotes/origin/main").Run(); err != nil {
		t.Fatalf("precondition: commit must be reachable on origin/main")
	}

	// The fixed resolver must answer via the remote-tracking ref.
	if !gitCommitReachableOnBranch(repoDir, commit, "main") {
		t.Fatalf("gitCommitReachableOnBranch resolved the stale local ref; want refs/remotes/origin/main")
	}

	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID:     "wr-stale-local",
		Type:   "task",
		Status: "in_progress",
		Metadata: map[string]string{
			beadmeta.WorkDirMetadataKey: repoDir,
		},
	}}, nil)
	args := []string{
		"update", "wr-stale-local",
		"--set-metadata", beadmeta.WorkOutcomeMetadataKey + "=" + beadmeta.WorkOutcomeShipped,
		"--set-metadata", beadmeta.WorkCommitMetadataKey + "=" + commit,
		"--set-metadata", beadmeta.WorkBranchMetadataKey + "=main",
		"--status=closed",
	}
	var stderr strings.Builder
	if block := evaluateWorkRecordCloseGate(args, store, repoDir, true, &stderr); block {
		t.Fatalf("close of work merged to origin/main blocked on a stale local ref; stderr=%s", stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("clean delivered close produced gate output: %q", got)
	}
}

func TestWorkRecordEnforceEnabled(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Setenv(workRecordEnforceEnvVar, v)
		if !workRecordEnforceEnabled() {
			t.Errorf("workRecordEnforceEnabled(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "off", "nope"} {
		t.Setenv(workRecordEnforceEnvVar, v)
		if workRecordEnforceEnabled() {
			t.Errorf("workRecordEnforceEnabled(%q) = true, want false", v)
		}
	}
}
