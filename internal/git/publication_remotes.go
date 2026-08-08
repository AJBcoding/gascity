package git

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PublishableRemotes returns the names of remotes that can actually publish
// work off this repository, in sorted order.
//
// A remote whose URL resolves to this same repository is excluded. Pushing
// there moves nothing off the host, yet it creates refs/remotes/<name>/*
// entries, so every probe that consults remote-tracking refs — `git branch -r
// --contains`, `git log --not --remotes` — reports the work published. That
// false positive cost this city two keystone fixes, found single-copy on local
// disk after their beads had been closed as published (gas-9sg).
//
// Only self-reference is disqualifying. A remote at another path is a distinct
// repository and a legitimate publication target, and a URL carrying a scheme
// or an scp-style host names another machine outright.
func (g *Git) PublishableRemotes() ([]string, error) {
	out, err := g.run("remote")
	if err != nil {
		return nil, fmt.Errorf("listing remotes: %w", err)
	}
	names := strings.Fields(out)
	if len(names) == 0 {
		return nil, nil
	}
	selfDirs, err := g.repoIdentityDirs()
	if err != nil {
		return nil, err
	}

	publishable := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, "-") {
			// A remote name that parses as a git option can never be passed
			// safely to a later probe; treat it as unusable for publication.
			continue
		}
		url, err := g.run("remote", "get-url", name)
		if err != nil {
			// A remote we cannot resolve is not provably a publication target,
			// and counting it would restore the false positive this exists to
			// remove.
			continue
		}
		if remoteURLIsSelfReferential(strings.TrimSpace(url), selfDirs) {
			continue
		}
		publishable = append(publishable, name)
	}
	sort.Strings(publishable)
	return publishable, nil
}

// repoIdentityDirs returns the directories that identify this repository: the
// common git directory (shared by every linked worktree) and the main
// worktree's root. A self-referential remote in production names the MAIN repo
// path while the agent works in a linked worktree, so comparing against the
// working directory alone would miss it.
func (g *Git) repoIdentityDirs() ([]string, error) {
	commonDir, err := g.run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("resolving git common dir: %w", err)
	}
	dirs := []string{strings.TrimSpace(commonDir)}
	// The main worktree root is the common dir's parent for a standard
	// ".git" layout; a bare or separated git dir simply has no such pairing.
	if base := filepath.Base(dirs[0]); base == ".git" {
		dirs = append(dirs, filepath.Dir(dirs[0]))
	}
	return dirs, nil
}

// remoteURLIsSelfReferential reports whether a remote URL points at this same
// repository. URLs naming another host (scheme-bearing or scp-style) are never
// self-referential; local paths are compared by identity (os.SameFile), so
// symlinked and non-canonical spellings of the same directory still match.
func remoteURLIsSelfReferential(url string, selfDirs []string) bool {
	path, ok := localRemotePath(url)
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, dir := range selfDirs {
		selfInfo, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if os.SameFile(info, selfInfo) {
			return true
		}
		// A remote may name the repository through its .git directory
		// (…/repo/.git) while the identity dir is the worktree root, or the
		// reverse. Compare the pairing both ways.
		if gitDir, err := os.Stat(filepath.Join(path, ".git")); err == nil && os.SameFile(gitDir, selfInfo) {
			return true
		}
	}
	return false
}

// localRemotePath extracts a filesystem path from a remote URL, reporting
// false when the URL names another host. "file://" URLs are local paths spelled
// as a URL; scp-style "host:path" and any scheme-bearing URL are not.
func localRemotePath(url string) (string, bool) {
	if url == "" {
		return "", false
	}
	if rest, found := strings.CutPrefix(url, "file://"); found {
		return rest, true
	}
	if strings.Contains(url, "://") {
		return "", false
	}
	// scp-style syntax is "[user@]host:path"; a Windows drive letter ("C:\…")
	// and an absolute POSIX path never take that shape.
	if idx := strings.Index(url, ":"); idx > 1 {
		return "", false
	}
	return url, true
}
