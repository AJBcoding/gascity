package git

import (
	"os"
	"path/filepath"
	"testing"
)

// selfRemoteName is the remote the gascity clone carries in the wild, pointing
// at its own path. Tests reuse the real name so a failure reads like the
// incident.
const selfRemoteName = "herdr-src"

// addSelfRemote configures repo with a remote pointing back at repo itself —
// the herdr-src shape this package's durability oracle must reject — and
// fetches it so the self-referential snapshots exist under
// refs/remotes/herdr-src/. That fetch is the poisoning step: it is what makes a
// blanket --remotes read local-only branches as published.
func addSelfRemote(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "remote", "add", selfRemoteName, repo)
	runGit(t, repo, "fetch", selfRemoteName)
}

// newPushedClone returns a clone of a fresh bare repo with one pushed commit,
// so origin is a genuine off-repo publication remote.
func newPushedClone(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "init")
	runGit(t, clone, "push", "origin", "HEAD")
	return clone
}

// TestHasUnpushedCommitsSelfReferentialRemoteIsNotPublication is the gas-9sg
// regression test. A remote whose URL is this repository's own path publishes
// nothing off-host, yet fetching it snapshots every local branch into
// refs/remotes/<name>/*. A durability probe that consults refs/remotes wholesale
// reads those snapshots as delivery and reports genuinely local-only work as
// pushed — which is what cleared unpublished keystone commits for worktree
// reclamation.
func TestHasUnpushedCommitsSelfReferentialRemoteIsNotPublication(t *testing.T) {
	clone := newPushedClone(t)

	// Local-only work: committed, never pushed to the real origin.
	runGit(t, clone, "checkout", "-b", "feature")
	runGit(t, clone, "commit", "--allow-empty", "-m", "keystone fix")
	addSelfRemote(t, clone)

	// Precondition: the self remote really does satisfy a blanket --remotes,
	// so this test exercises the defect rather than a repo that never had it.
	if _, err := New(clone).run("rev-parse", "--verify", "--quiet", "refs/remotes/herdr-src/feature"); err != nil {
		t.Fatalf("precondition: refs/remotes/herdr-src/feature must exist after fetching the self remote: %v", err)
	}

	if !New(clone).HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = false, want true: a self-referential remote's snapshot is not publication")
	}
}

// TestHasUnpushedCommitsSelfRemoteOnlyRepoFailsClosed covers the repo whose only
// remote is self-referential. No publication remote exists, so nothing has been
// published and every commit is unpushed — the same answer this package already
// gives a repo with no remotes at all.
func TestHasUnpushedCommitsSelfRemoteOnlyRepoFailsClosed(t *testing.T) {
	repo := initTestRepo(t)
	addSelfRemote(t, repo)

	if !New(repo).HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = false, want true: a repo whose only remote is itself has published nothing")
	}
}

// TestHasUnpushedCommitsRelativeSelfRemoteIsNotPublication covers the relative
// spelling of the same self-remote trap. `git remote add herdr-src .` stores "."
// verbatim and git resolves it against the repository worktree. The publication
// classifier must use that repo-relative base, not the process working
// directory, or a fetch from "." snapshots local-only work under refs/remotes
// and makes it look pushed.
func TestHasUnpushedCommitsRelativeSelfRemoteIsNotPublication(t *testing.T) {
	repo := initTestRepo(t)

	runGit(t, repo, "checkout", "-b", "feature")
	runGit(t, repo, "commit", "--allow-empty", "-m", "local-only work")
	runGit(t, repo, "remote", "add", selfRemoteName, ".")
	runGit(t, repo, "fetch", selfRemoteName)

	if _, err := New(repo).run("rev-parse", "--verify", "--quiet", "refs/remotes/herdr-src/feature"); err != nil {
		t.Fatalf("precondition: refs/remotes/herdr-src/feature must exist after fetching the relative self remote: %v", err)
	}

	if !New(repo).HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = false, want true: a relative self-remote's snapshot is not publication")
	}
}

// TestHasUnpushedCommitsRealRemoteStillPublishes guards the fix's blast radius:
// excluding self-referential remotes must not stop a genuine remote from
// clearing work. A repo carrying both reports pushed work as pushed.
func TestHasUnpushedCommitsRealRemoteStillPublishes(t *testing.T) {
	clone := newPushedClone(t)
	addSelfRemote(t, clone)

	if New(clone).HasUnpushedCommits() {
		t.Error("HasUnpushedCommits() = true, want false: work pushed to a real origin is published")
	}
}

// TestPublicationRemotesExcludesSelfReferential pins the classifier through the
// accessor its callers use: the self remote is reported as configured (so the
// repo is not mistaken for local-only) but never as publication.
func TestPublicationRemotesExcludesSelfReferential(t *testing.T) {
	clone := newPushedClone(t)
	addSelfRemote(t, clone)

	publication, configured, err := New(clone).PublicationRemotes()
	if err != nil {
		t.Fatalf("PublicationRemotes() error = %v", err)
	}
	if want := []string{"origin"}; !equalStrings(publication, want) {
		t.Errorf("publication = %v, want %v (a same-repo remote confers no durability)", publication, want)
	}
	if want := []string{"herdr-src", "origin"}; !equalStrings(configured, want) {
		t.Errorf("configured = %v, want %v (the self remote is still configured)", configured, want)
	}
}

// TestPublicationRemotesNoRemotes distinguishes the two empty-publication causes
// its callers must tell apart: a repo with no remotes at all reports both lists
// empty and no error.
func TestPublicationRemotesNoRemotes(t *testing.T) {
	repo := initTestRepo(t)

	publication, configured, err := New(repo).PublicationRemotes()
	if err != nil {
		t.Fatalf("PublicationRemotes() error = %v", err)
	}
	if len(publication) != 0 || len(configured) != 0 {
		t.Errorf("PublicationRemotes() = (%v, %v), want two empty lists for a repo with no remotes", publication, configured)
	}
}

// TestPublicationRemotesExcludesLoopbackSCPAndMissingLocalPath pins two
// fail-closed publication edges. Loopback scp syntax can name this repository
// just like ssh://localhost can, and a missing local path cannot have received
// work at all. Neither may clear an at-risk or worktree-prune check.
func TestPublicationRemotesExcludesLoopbackSCPAndMissingLocalPath(t *testing.T) {
	repo := initTestRepo(t)
	runGit(t, repo, "remote", "add", "loopback", "git@localhost:"+repo)
	runGit(t, repo, "remote", "add", "missing", filepath.Join(repo, "missing.git"))

	publication, configured, err := New(repo).PublicationRemotes()
	if err != nil {
		t.Fatalf("PublicationRemotes() error = %v", err)
	}
	if len(configured) != 2 {
		t.Fatalf("configured = %v, want two configured remotes", configured)
	}
	if len(publication) != 0 {
		t.Errorf("publication = %v, want none: loopback self-remotes and missing local paths confer no durability", publication)
	}
}

// TestPublicationRemotesProbeError checks the fail-closed path: a directory that
// is not a repository yields an error rather than an empty publication list a
// caller could read as "nothing to publish to".
func TestPublicationRemotesProbeError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	if _, _, err := New(dir).PublicationRemotes(); err == nil {
		t.Error("PublicationRemotes() error = nil, want an error for a non-repository")
	}
}

// TestIsSelfRemoteURL pins the self-remote classifier (gas-6tc) against the two
// ways a same-disk remote used to escape it (gas-f64): a network-shaped URL
// whose host is this machine, and a plain local path containing '@'.
//
// Direction matters. Misreading a self-remote as a publication remote is the
// unsafe error — its fetched refs then read as delivery evidence and the
// durability rule is silently disarmed. Misreading a real remote as self only
// costs a blocked close, so the classifier claims "self" solely when the URL
// resolves to a path that positively *is* this repository.
func TestIsSelfRemoteURL(t *testing.T) {
	repoDir := initTestRepo(t)
	selfCommon := CommonDir(repoDir)
	if selfCommon == "" {
		t.Fatalf("precondition: CommonDir(%s) must resolve", repoDir)
	}

	// A second, unrelated repository: same host, different repo.
	otherDir := newRepoAt(t, filepath.Join(t.TempDir(), "other"))

	// A repository whose path legitimately contains '@'.
	atDir := newRepoAt(t, filepath.Join(t.TempDir(), "no@such", "path"))
	atCommon := CommonDir(atDir)
	if atCommon == "" {
		t.Fatalf("precondition: CommonDir(%s) must resolve", atDir)
	}

	tests := []struct {
		name       string
		url        string
		selfCommon string
		want       bool
	}{
		{"plain local path to self", repoDir, selfCommon, true},
		{"local path containing @ to self", atDir, atCommon, true},
		{"file URL to self", "file://" + repoDir, selfCommon, true},
		{"file URL with localhost host to self", "file://localhost" + repoDir, selfCommon, true},
		{"ssh loopback by name to self", "ssh://localhost" + repoDir, selfCommon, true},
		{"ssh loopback by IPv4 to self", "ssh://127.0.0.1" + repoDir, selfCommon, true},
		{"ssh loopback by IPv6 to self", "ssh://[::1]" + repoDir, selfCommon, true},
		{"ssh loopback with user to self", "ssh://git@localhost" + repoDir, selfCommon, true},
		{"ssh loopback with port to self", "ssh://localhost:2222" + repoDir, selfCommon, true},
		{"git protocol loopback to self", "git://127.0.0.1" + repoDir, selfCommon, true},
		{"loopback host is case-insensitive", "ssh://LocalHost" + repoDir, selfCommon, true},

		{"ssh loopback to a different repo", "ssh://localhost" + otherDir, selfCommon, false},
		{"local path to a different repo", otherDir, selfCommon, false},
		{"ssh to a real host", "ssh://github.com/gastownhall/gascity.git", selfCommon, false},
		{"scp-style real remote", "git@github.com:gastownhall/gascity.git", selfCommon, false},
		{"https real remote", "https://github.com/gastownhall/gascity.git", selfCommon, false},
		{"local path that is not a repo", filepath.Join(t.TempDir(), "nope"), selfCommon, false},
		{"empty url", "", selfCommon, false},
		{"empty selfCommon", repoDir, "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSelfRemoteURL(tc.url, tc.selfCommon); got != tc.want {
				t.Errorf("IsSelfRemoteURL(%q, %q) = %v, want %v", tc.url, tc.selfCommon, got, tc.want)
			}
		})
	}
}

// TestCommonDirSharedAcrossWorktrees pins the identity the classifier compares:
// a repository and its linked worktrees must resolve to one common dir, or a
// polecat worktree would not recognize its own repo as self-referential.
func TestCommonDirSharedAcrossWorktrees(t *testing.T) {
	repo := initTestRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repo, "worktree", "add", "-b", "feature", wt)

	if got, want := CommonDir(wt), CommonDir(repo); got != want || got == "" {
		t.Errorf("CommonDir(worktree) = %q, want %q (shared across worktrees)", got, want)
	}
}

// TestCommonDirNonRepo reports "" for anything that is not a repository, which
// callers treat as "cannot classify" rather than as a match.
func TestCommonDirNonRepo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	if got := CommonDir(dir); got != "" {
		t.Errorf("CommonDir(non-repo) = %q, want empty", got)
	}
}

// newRepoAt initializes a git repository at dir, creating dir (and any parent)
// first. Unlike initTestRepo it takes the path, so a test can put a repository
// somewhere with a chosen name — an '@' in a path component is
// indistinguishable from scp-style user@host to a naive classifier.
func newRepoAt(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	runGit(t, dir, "init", "--initial-branch=main")
	return dir
}

// equalStrings reports whether two string slices have the same elements in the
// same order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
