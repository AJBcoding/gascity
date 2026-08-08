package git

import (
	"path/filepath"
	"testing"
)

// selfReferentialRemoteRepo builds the shape that cost this city two keystone
// fixes (gas-9sg): a repo carrying a remote whose URL is the repo itself.
// Fetching it mirrors every local head into refs/remotes/<name>/*, so
// publication probes that consult all remote-tracking refs report success while
// nothing has left the host. It returns the repo path.
func selfReferentialRemoteRepo(t *testing.T) string {
	const remoteName = "herdr-src" // the real remote name from the incident
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "work")
	runGit(t, dir, "remote", "add", remoteName, dir)
	runGit(t, dir, "fetch", remoteName)
	return dir
}

// TestHasUnpushedCommitsResult_TrueWhenOnlyRemoteIsSelfReferential is the
// gas-9sg regression: a commit that exists nowhere but this host must read as
// unpushed. Before the fix, `git log HEAD --not --remotes` counted the
// self-referential remote's mirrored refs and reported the work published.
func TestHasUnpushedCommitsResult_TrueWhenOnlyRemoteIsSelfReferential(t *testing.T) {
	dir := selfReferentialRemoteRepo(t)

	has, err := New(dir).HasUnpushedCommitsResult()
	if err != nil {
		t.Fatalf("HasUnpushedCommitsResult() error = %v, want nil", err)
	}
	if !has {
		t.Error("HasUnpushedCommitsResult() = false for a repo whose only remote is itself, want true: pushing there publishes nothing off-host")
	}
}

// TestHasUnpushedCommitsResult_TrueInWorktreeWhenOnlyRemoteIsSelfReferential
// pins the production shape exactly: agents work in linked worktrees, and the
// self-referential remote's URL is the MAIN repo path, not the worktree path.
// A probe that compares the remote URL against the worktree directory alone
// would miss it and keep reporting the work published.
func TestHasUnpushedCommitsResult_TrueInWorktreeWhenOnlyRemoteIsSelfReferential(t *testing.T) {
	main := selfReferentialRemoteRepo(t)
	wtPath := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-b", "polecat/gas-9sg", wtPath)
	runGit(t, wtPath, "config", "user.email", "test@test.com")
	runGit(t, wtPath, "config", "user.name", "Test")
	runGit(t, wtPath, "commit", "--allow-empty", "-m", "unpublished work")
	runGit(t, wtPath, "push", "herdr-src", "polecat/gas-9sg")

	has, err := New(wtPath).HasUnpushedCommitsResult()
	if err != nil {
		t.Fatalf("HasUnpushedCommitsResult() error = %v, want nil", err)
	}
	if !has {
		t.Error("HasUnpushedCommitsResult() = false in a worktree whose only remote is the main repo itself, want true: this is the shape that let the pruner delete unpublished work")
	}
}

// TestHasUnpushedCommitsResult_FalseWhenPushedToASeparateRepo guards the fix
// from over-reach. A remote living at another path is a real, distinct
// publication target — only a remote resolving to THIS repo is the trap — so
// work pushed there must still read as published.
func TestHasUnpushedCommitsResult_FalseWhenPushedToASeparateRepo(t *testing.T) {
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")
	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "work")
	runGit(t, clone, "push", "origin", "HEAD")

	has, err := New(clone).HasUnpushedCommitsResult()
	if err != nil {
		t.Fatalf("HasUnpushedCommitsResult() error = %v, want nil", err)
	}
	if has {
		t.Error("HasUnpushedCommitsResult() = true for work pushed to a separate repo, want false")
	}
}

// TestHasUnpushedCommitsResult_TrueWhenRealRemoteLacksTheCommit proves the
// self-referential remote cannot mask an unpushed commit even when a genuine
// remote is also configured: the answer must come from the real remote alone.
func TestHasUnpushedCommitsResult_TrueWhenRealRemoteLacksTheCommit(t *testing.T) {
	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")
	clone := t.TempDir()
	runGit(t, clone, "clone", bare, ".")
	runGit(t, clone, "config", "user.email", "test@test.com")
	runGit(t, clone, "config", "user.name", "Test")
	runGit(t, clone, "commit", "--allow-empty", "-m", "published")
	runGit(t, clone, "push", "origin", "HEAD")
	runGit(t, clone, "remote", "add", "herdr-src", clone)
	runGit(t, clone, "commit", "--allow-empty", "-m", "only on this host")
	runGit(t, clone, "fetch", "herdr-src")

	has, err := New(clone).HasUnpushedCommitsResult()
	if err != nil {
		t.Fatalf("HasUnpushedCommitsResult() error = %v, want nil", err)
	}
	if !has {
		t.Error("HasUnpushedCommitsResult() = false while the real remote lacks the newest commit, want true")
	}
}

// TestHasUnpushedCommitsResult_TrueWithNoRemotesAtAll pins the fail-safe
// direction: with nothing to publish to, everything is unpublished.
func TestHasUnpushedCommitsResult_TrueWithNoRemotesAtAll(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@test.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "commit", "--allow-empty", "-m", "work")

	has, err := New(dir).HasUnpushedCommitsResult()
	if err != nil {
		t.Fatalf("HasUnpushedCommitsResult() error = %v, want nil", err)
	}
	if !has {
		t.Error("HasUnpushedCommitsResult() = false for a repo with no remotes, want true")
	}
}

// --- PublishableRemotes ---

// TestPublishableRemotesExcludesSelfReferentialRemote pins the classifier the
// probe is built on: a remote pointing at this repo is not a publication
// target, while one pointing elsewhere is.
func TestPublishableRemotesExcludesSelfReferentialRemote(t *testing.T) {
	other := t.TempDir()
	runGit(t, other, "init", "--bare")
	dir := selfReferentialRemoteRepo(t)
	runGit(t, dir, "remote", "add", "origin", other)

	names, err := New(dir).PublishableRemotes()
	if err != nil {
		t.Fatalf("PublishableRemotes() error = %v, want nil", err)
	}
	if len(names) != 1 || names[0] != "origin" {
		t.Errorf("PublishableRemotes() = %v, want [origin]", names)
	}
}

// TestPublishableRemotesKeepsNetworkRemotes proves a URL with a scheme is
// always publishable — it names another host, so no path comparison applies.
func TestPublishableRemotesKeepsNetworkRemotes(t *testing.T) {
	dir := selfReferentialRemoteRepo(t)
	runGit(t, dir, "remote", "add", "ajb", "https://github.com/AJBcoding/gascity.git")

	names, err := New(dir).PublishableRemotes()
	if err != nil {
		t.Fatalf("PublishableRemotes() error = %v, want nil", err)
	}
	if len(names) != 1 || names[0] != "ajb" {
		t.Errorf("PublishableRemotes() = %v, want [ajb]", names)
	}
}
