package main

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	internalgit "github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/gitcred"
	"github.com/gastownhall/gascity/internal/landing"
)

const landingGitTimeout = 30 * time.Second

var (
	landingRemotePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	landingSHAPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type landingGitRunFunc func(context.Context, string, []string, ...string) ([]byte, error)

type gitLandingObserver struct {
	run     landingGitRunFunc
	timeout time.Duration
}

func newGitLandingObserver() gitLandingObserver {
	return gitLandingObserver{run: runLandingGitCommand, timeout: landingGitTimeout}
}

func (o gitLandingObserver) Observe(ctx context.Context, repositoryPath, remote, targetRef string) (landing.RemoteObservation, error) {
	if !filepath.IsAbs(repositoryPath) {
		return landing.RemoteObservation{}, fmt.Errorf("landing git: repository path must be absolute")
	}
	if !landingRemotePattern.MatchString(remote) {
		return landing.RemoteObservation{}, fmt.Errorf("landing git: remote is not a safe Git remote name")
	}
	if !strings.HasPrefix(targetRef, "refs/heads/") || targetRef == "refs/heads/" {
		return landing.RemoteObservation{}, fmt.Errorf("landing git: target ref must name a branch under refs/heads/")
	}
	if o.run == nil {
		return landing.RemoteObservation{}, fmt.Errorf("landing git: command runner is required")
	}
	timeout := o.timeout
	if timeout <= 0 {
		timeout = landingGitTimeout
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	env := internalgit.SanitizedEnv()

	topOutput, err := o.run(commandCtx, filepath.Clean(repositoryPath), env, "rev-parse", "--show-toplevel")
	if err != nil {
		return landing.RemoteObservation{}, landingGitError("resolving repository top-level", topOutput, err)
	}
	topLevel, err := singleLandingGitLine(topOutput, "repository top-level")
	if err != nil {
		return landing.RemoteObservation{}, err
	}
	if !filepath.IsAbs(topLevel) {
		return landing.RemoteObservation{}, fmt.Errorf("landing git: repository top-level is not absolute")
	}
	topLevel = filepath.Clean(topLevel)

	remoteOutput, err := o.run(commandCtx, topLevel, env, "remote", "get-url", "--all", remote)
	if err != nil {
		return landing.RemoteObservation{}, landingGitError("resolving remote identity", remoteOutput, err)
	}
	remoteURL, err := singleLandingGitLine(remoteOutput, "remote URL")
	if err != nil {
		return landing.RemoteObservation{}, err
	}
	repositoryIdentity, err := landingRepositoryIdentity(topLevel, remoteURL)
	if err != nil {
		return landing.RemoteObservation{}, err
	}

	refOutput, err := o.run(commandCtx, topLevel, env, "ls-remote", "--exit-code", remote, targetRef)
	if err != nil {
		return landing.RemoteObservation{}, landingGitError("observing authoritative target ref", refOutput, err)
	}
	sha, err := parseLandingRemoteRef(refOutput, targetRef)
	if err != nil {
		return landing.RemoteObservation{}, err
	}
	return landing.RemoteObservation{Repository: repositoryIdentity, SHA: sha}, nil
}

func runLandingGitCommand(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	return cmd.CombinedOutput()
}

func singleLandingGitLine(output []byte, label string) (string, error) {
	text := strings.TrimSuffix(string(output), "\n")
	if text == "" || strings.Contains(text, "\n") || strings.Contains(text, "\r") {
		return "", fmt.Errorf("landing git: %s must contain exactly one non-empty line", label)
	}
	if strings.TrimSpace(text) != text {
		return "", fmt.Errorf("landing git: %s contains surrounding whitespace", label)
	}
	return text, nil
}

func landingRepositoryIdentity(topLevel, rawRemote string) (string, error) {
	redacted := gitcred.RedactUserinfo(rawRemote)
	parsed, parseErr := url.Parse(rawRemote)
	if redacted != rawRemote || (parseErr == nil && parsed.User != nil) {
		return "", fmt.Errorf("landing git: remote URL %q contains embedded credentials", redacted)
	}
	if parseErr != nil {
		return "", fmt.Errorf("landing git: remote URL %q is invalid", redacted)
	}

	if parsed.Scheme == "file" && (parsed.Host == "" || parsed.Host == "localhost") {
		return cleanLandingLocalPath(topLevel, parsed.Path)
	}
	if parsed.Scheme == "" && !looksLikeSCPLandingRemote(rawRemote) {
		return cleanLandingLocalPath(topLevel, rawRemote)
	}
	return rawRemote, nil
}

func cleanLandingLocalPath(topLevel, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(topLevel, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("landing git: resolving local remote path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func looksLikeSCPLandingRemote(remote string) bool {
	colon := strings.IndexByte(remote, ':')
	return colon > 0 && !strings.Contains(remote[:colon], string(filepath.Separator))
}

func parseLandingRemoteRef(output []byte, targetRef string) (string, error) {
	line, err := singleLandingGitLine(output, "ls-remote response")
	if err != nil {
		return "", err
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 2 {
		return "", fmt.Errorf("landing git: ls-remote response must contain one SHA and one ref")
	}
	if !landingSHAPattern.MatchString(fields[0]) {
		return "", fmt.Errorf("landing git: ls-remote returned a malformed SHA")
	}
	if fields[1] != targetRef {
		return "", fmt.Errorf("landing git: ls-remote returned ref %q, want %q", fields[1], targetRef)
	}
	return fields[0], nil
}

func landingGitError(action string, output []byte, err error) error {
	detail := strings.TrimSpace(gitcred.RedactUserinfo(string(output)))
	if detail == "" {
		return fmt.Errorf("landing git: %s: %w", action, err)
	}
	return fmt.Errorf("landing git: %s: %s: %w", action, detail, err)
}
