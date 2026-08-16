package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type landingGitCall struct {
	dir      string
	env      []string
	args     []string
	deadline time.Time
}

type landingGitSpy struct {
	calls []landingGitCall
	run   func(context.Context, string, []string, ...string) ([]byte, error)
}

func (s *landingGitSpy) command(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	deadline, _ := ctx.Deadline()
	s.calls = append(s.calls, landingGitCall{
		dir:      dir,
		env:      append([]string(nil), env...),
		args:     append([]string(nil), args...),
		deadline: deadline,
	})
	if s.run != nil {
		return s.run(ctx, dir, env, args...)
	}
	return nil, nil
}

func landingTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Landing Test",
		"GIT_AUTHOR_EMAIL=landing@example.invalid",
		"GIT_COMMITTER_NAME=Landing Test",
		"GIT_COMMITTER_EMAIL=landing@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func landingRepositoryFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	landingTestGit(t, root, "init", "--bare", bare)
	landingTestGit(t, root, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("verified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	landingTestGit(t, work, "add", "README.md")
	landingTestGit(t, work, "commit", "-m", "fixture")
	sha := landingTestGit(t, work, "rev-parse", "HEAD")
	landingTestGit(t, work, "remote", "add", "origin", bare)
	landingTestGit(t, work, "push", "origin", "HEAD:refs/heads/main")
	return work, filepath.Clean(bare), sha
}

func TestGitLandingObserverReadsExactHeadRefAndRepositoryIdentity(t *testing.T) {
	repo, remoteIdentity, wantSHA := landingRepositoryFixture(t)
	spy := &landingGitSpy{run: runLandingGitCommand}
	observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}

	got, err := observer.Observe(context.Background(), repo, "origin", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Repository != remoteIdentity || got.SHA != wantSHA {
		t.Fatalf("observation = %#v, want repository=%q sha=%q", got, remoteIdentity, wantSHA)
	}
	if len(spy.calls) != 3 {
		t.Fatalf("git calls = %d, want 3", len(spy.calls))
	}
	if got, want := strings.Join(spy.calls[2].args, " "), "ls-remote --exit-code origin refs/heads/main"; got != want {
		t.Fatalf("network argv = %q, want %q", got, want)
	}
	for _, call := range spy.calls {
		joined := strings.Join(call.args, " ")
		for _, forbidden := range []string{" push", "fetch", "update-ref", "sh -c"} {
			if strings.Contains(" "+joined, forbidden) {
				t.Fatalf("forbidden git operation in %q", joined)
			}
		}
	}
}

func TestGitLandingObserverRejectsUnsafeRemoteNameBeforeGit(t *testing.T) {
	spy := &landingGitSpy{}
	observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}
	if _, err := observer.Observe(context.Background(), t.TempDir(), "-upload-pack=bad", "refs/heads/main"); err == nil {
		t.Fatal("Observe error = nil")
	}
	if len(spy.calls) != 0 {
		t.Fatalf("git calls = %d, want 0", len(spy.calls))
	}
}

func TestGitLandingObserverRejectsNonHeadTargetBeforeGit(t *testing.T) {
	spy := &landingGitSpy{}
	observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}
	if _, err := observer.Observe(context.Background(), t.TempDir(), "origin", "refs/tags/v1"); err == nil {
		t.Fatal("Observe error = nil")
	}
	if len(spy.calls) != 0 {
		t.Fatalf("git calls = %d, want 0", len(spy.calls))
	}
}

func TestGitLandingObserverRejectsEmbeddedRemoteCredentials(t *testing.T) {
	secret := "never-log-this-token"
	spy := &landingGitSpy{run: func(_ context.Context, dir string, _ []string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "rev-parse --show-toplevel":
			return []byte(dir + "\n"), nil
		case "remote get-url --all origin":
			return []byte("https://user:" + secret + "@example.invalid/acme/repo.git\n"), nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return nil, nil
		}
	}}
	observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}
	_, err := observer.Observe(context.Background(), t.TempDir(), "origin", "refs/heads/main")
	if err == nil {
		t.Fatal("Observe error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked credential: %v", err)
	}
	if len(spy.calls) != 2 {
		t.Fatalf("git calls = %d, want 2", len(spy.calls))
	}
}

func TestGitLandingObserverRejectsMissingOrAmbiguousRef(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
	}{
		{name: "missing", err: errors.New("exit status 2")},
		{name: "ambiguous", output: strings.Repeat("a", 40) + "\trefs/heads/main\n" + strings.Repeat("b", 40) + "\trefs/heads/main\n"},
		{name: "mismatched ref", output: strings.Repeat("a", 40) + "\trefs/heads/other\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			spy := &landingGitSpy{run: func(_ context.Context, dir string, _ []string, args ...string) ([]byte, error) {
				switch strings.Join(args, " ") {
				case "rev-parse --show-toplevel":
					return []byte(dir + "\n"), nil
				case "remote get-url --all origin":
					return []byte("https://example.invalid/acme/repo.git\n"), nil
				case "ls-remote --exit-code origin refs/heads/main":
					return []byte(tt.output), tt.err
				default:
					t.Fatalf("unexpected git args: %v", args)
					return nil, nil
				}
			}}
			observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}
			if _, err := observer.Observe(context.Background(), repo, "origin", "refs/heads/main"); err == nil {
				t.Fatal("Observe error = nil")
			}
		})
	}
}

func TestGitLandingObserverUsesSanitizedEnvironmentAndDeadline(t *testing.T) {
	t.Setenv("GIT_DIR", "/poisoned")
	repo := t.TempDir()
	spy := &landingGitSpy{run: func(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
		for _, entry := range env {
			if strings.HasPrefix(entry, "GIT_DIR=") {
				t.Fatalf("sanitized environment leaked %q", entry)
			}
		}
		switch strings.Join(args, " ") {
		case "rev-parse --show-toplevel":
			return []byte(dir + "\n"), nil
		case "remote get-url --all origin":
			return []byte("https://example.invalid/acme/repo.git\n"), nil
		case "ls-remote --exit-code origin refs/heads/main":
			return []byte(strings.Repeat("a", 40) + "\trefs/heads/main\n"), nil
		default:
			t.Fatalf("unexpected git args: %v", args)
			return nil, nil
		}
	}}
	observer := gitLandingObserver{run: spy.command, timeout: 30 * time.Second}
	started := time.Now()
	if _, err := observer.Observe(context.Background(), repo, "origin", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	if len(spy.calls) != 3 {
		t.Fatalf("git calls = %d, want 3", len(spy.calls))
	}
	for _, call := range spy.calls {
		if call.deadline.IsZero() {
			t.Fatal("git call has no deadline")
		}
		remaining := call.deadline.Sub(started)
		if remaining < 29*time.Second || remaining > 31*time.Second {
			t.Fatalf("deadline offset = %s, want about 30s", remaining)
		}
	}
}
