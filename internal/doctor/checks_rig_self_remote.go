package doctor

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

// RigSelfRemoteCheck reports remotes in a rig repository whose URL resolves back
// into that same repository.
//
// Such a remote publishes nothing off-host, but pushing to it succeeds and
// fetching it snapshots every local branch into refs/remotes/<name>/*. Every
// habitual publication check — `git branch -r --contains`, `git log --not
// --remotes` — is then satisfied by refs that never left the machine, so work
// that exists in exactly one place reports as safely published. The gascity
// clone carried exactly this (herdr-src = its own path) and two keystone fixes
// were found single-copy on local disk (gas-9sg).
//
// gc's own durability probes now exclude these remotes (internal/git's
// publication-remote classification), so this check does not exist to protect
// them. It exists because the trap is SILENT: a defended probe fixes gc's
// answers while leaving every human and every ad-hoc shell check still reading
// the wrong evidence. Naming the remote is what lets an operator remove it, or
// keep it knowingly.
//
// SeverityAdvisory; WarmupEligible.
type RigSelfRemoteCheck struct {
	rig config.Rig
}

// NewRigSelfRemoteCheck creates a self-referential-remote check for the given
// rig.
func NewRigSelfRemoteCheck(rig config.Rig) *RigSelfRemoteCheck {
	return &RigSelfRemoteCheck{rig: rig}
}

// Name returns the check identifier.
func (c *RigSelfRemoteCheck) Name() string { return "rig:" + c.rig.Name + ":self-remote" }

// WarmupEligible returns true so this check runs during gc start warm-up.
func (c *RigSelfRemoteCheck) WarmupEligible() bool { return true }

// CanFix returns false. A same-repo remote may be deliberate — a local mirror
// used for fetching — and deciding to delete one is an operator judgment about
// intent, not a mechanical repair.
func (c *RigSelfRemoteCheck) CanFix() bool { return false }

// Fix is a no-op.
func (c *RigSelfRemoteCheck) Fix(_ *CheckContext) error { return nil }

// Run classifies the rig repository's remotes and reports any that resolve back
// into the repository itself.
//
// A rig with no remotes at all passes: publishing nowhere is a topology choice,
// and the durability probes already treat such a repo's work as unpushed. It is
// the remote that only LOOKS like publication that this check is here to name.
func (c *RigSelfRemoteCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name(), Severity: SeverityAdvisory}

	remotes, err := git.New(c.rig.Path).Remotes()
	if err != nil {
		// Almost always "not a git repository", which rig:<name>:git already
		// reports. Raising a second warning for one condition inflates every
		// warmup tally and teaches operators to skim the list, so this check
		// stays quiet about a finding it does not own and says why.
		r.Status = StatusOK
		r.Message = fmt.Sprintf("rig %q: remotes not readable in %s (%v) — see rig:%s:git", c.rig.Name, c.rig.Path, err, c.rig.Name)
		return r
	}

	var self, publication []git.Remote
	for _, remote := range remotes {
		if remote.Self {
			self = append(self, remote)
		}
		if remote.Publication {
			publication = append(publication, remote)
		}
	}

	if len(self) == 0 {
		r.Status = StatusOK
		r.Message = fmt.Sprintf("rig %q: no self-referential remotes (%d configured)", c.rig.Name, len(remotes))
		return r
	}

	for _, remote := range self {
		r.Details = append(r.Details, fmt.Sprintf("%s = %s resolves to this repository — pushing there publishes nothing off-host", remote.Name, remote.URL))
	}
	r.Details = append(r.Details,
		"a fetch from it snapshots local branches into refs/remotes/, so `git branch -r --contains` and `git log --not --remotes` report unpublished work as published",
		"verify publication against a named remote instead: git ls-remote <remote> refs/heads/<branch>")
	r.FixHint = fmt.Sprintf("git -C %q remote remove %s   # if it is not deliberately kept as a local mirror", c.rig.Path, self[0].Name)

	if len(publication) == 0 {
		r.Status = StatusError
		r.Message = fmt.Sprintf("rig %q has no remote it can publish to: %s resolves to the repository itself", c.rig.Name, remoteNames(self))
		r.Details = append(r.Details, "every commit in this rig exists in exactly one place; add a real remote before relying on any published-work check")
		return r
	}

	r.Status = StatusWarning
	r.Message = fmt.Sprintf("rig %q: %s resolves to the repository itself and confers no durability", c.rig.Name, remoteNames(self))
	r.Details = append(r.Details, fmt.Sprintf("publication remotes that do confer durability: %s", remoteNames(publication)))
	return r
}

// remoteNames renders remote names for a one-line message.
func remoteNames(remotes []git.Remote) string {
	names := make([]string, 0, len(remotes))
	for _, remote := range remotes {
		names = append(names, remote.Name)
	}
	return strings.Join(names, ", ")
}
