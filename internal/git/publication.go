package git

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// Publication-remote classification. "Has this work been published?" is a
// durability question, and the only honest evidence for it is a ref on a remote
// that is somewhere other than this repository. A remote whose URL resolves back
// into this same repo — the gascity clone carries herdr-src = its own path —
// publishes nothing off-host, yet fetching it snapshots every local branch into
// refs/remotes/<name>/*. Any probe that consults refs/remotes wholesale then
// reports local-only work as delivered (gas-9sg, gas-6tc, gas-6wq).
//
// The asymmetry that sets every fail direction here: a false "at risk" costs one
// push, a false "durable" costs the work. So classification is by positive
// identification only — a URL is self-referential solely when it resolves to a
// path that is this very repository. A URL naming another host, or one whose
// path resolves nowhere, is reported as a real remote, which keeps the
// durability rule armed rather than silently disabling it.

// PublicationRemotes returns the names of the repository's remotes that can
// confer durability — every configured remote except those whose URL resolves
// back into this same repository — alongside the full configured list it was
// filtered from.
//
// Callers need both because an empty publication list is ambiguous on its own:
// no remotes at all is a local-only repo, while remotes that all filter out is
// the herdr-src misconfiguration, and callers may answer those differently. An
// unreadable remote list is an error rather than an empty list, so no caller can
// mistake a broken probe for "nothing to publish to".
func (g *Git) PublicationRemotes() (publication, configured []string, err error) {
	out, err := g.run("remote")
	if err != nil {
		return nil, nil, fmt.Errorf("listing remotes: %w", err)
	}
	selfCommon := CommonDir(g.workDir)
	publication, configured = []string{}, []string{}
	for _, name := range strings.Fields(out) {
		configured = append(configured, name)
		if strings.HasPrefix(name, "-") {
			// Unprobeable as a git argument, so it cannot be cleared as
			// self-referential — it counts as configured (the repo is not
			// local-only) but never as publication.
			continue
		}
		urlOut, urlErr := g.run("remote", "get-url", name)
		if urlErr != nil {
			// Unreadable URL: treat as a publication remote so the durability
			// rule stays armed rather than silently skipped.
			publication = append(publication, name)
			continue
		}
		if IsSelfRemoteURL(strings.TrimSpace(urlOut), selfCommon) {
			continue
		}
		publication = append(publication, name)
	}
	return publication, configured, nil
}

// IsSelfRemoteURL reports whether a remote URL resolves to the repository whose
// git common dir is selfCommon. A same-disk remote — a plain path, a file://
// URL, or a network URL pointed back at this machine — snapshots local branches
// into refs/remotes/*, so counting one as a publication remote would read those
// snapshots as delivery evidence.
func IsSelfRemoteURL(remoteURL, selfCommon string) bool {
	if remoteURL == "" || selfCommon == "" {
		return false
	}
	path, ok := localPathForRemoteURL(remoteURL)
	if !ok {
		return false
	}
	common := CommonDir(path)
	return common != "" && common == selfCommon
}

// CommonDir resolves dir's git common directory (shared across worktrees) to an
// absolute, symlink-resolved path, or "" when dir is not a git repository. Two
// paths into the same repository — the root and any of its worktrees — resolve
// to the same common dir, which is what makes it the right identity for
// self-remote detection.
func CommonDir(dir string) string {
	if strings.TrimSpace(dir) == "" || strings.HasPrefix(dir, "-") {
		return ""
	}
	out, err := New(dir).run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(out)
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// localPathForRemoteURL maps a remote URL to the filesystem path it addresses on
// this machine, reporting false when the URL addresses another host. The path is
// not required to exist or to be a repository — the caller decides what it
// resolves to.
func localPathForRemoteURL(remoteURL string) (string, bool) {
	if strings.Contains(remoteURL, "://") {
		u, err := url.Parse(remoteURL)
		if err != nil || !remoteHostIsSelf(u) || u.Path == "" {
			return "", false
		}
		return u.Path, true
	}
	// scp-style user@host:path addresses another machine. A bare filesystem
	// path does not, even when one of its components contains '@'.
	if looksLikeSCPRemote(remoteURL) {
		return "", false
	}
	return remoteURL, true
}

// remoteHostIsSelf reports whether a remote URL's host names this machine. An
// empty host is this machine by definition (file:///path); host names are
// case-insensitive; and url.URL.Hostname strips the port, the IPv6 brackets,
// and any user@ prefix before the loopback test.
func remoteHostIsSelf(u *url.URL) bool {
	host := u.Hostname()
	return host == "" || isLoopbackHost(strings.ToLower(host))
}

// looksLikeSCPRemote reports whether remoteURL is scp-style (host:path) rather
// than a local path: a colon before the first slash.
func looksLikeSCPRemote(remoteURL string) bool {
	colon := strings.Index(remoteURL, ":")
	if colon < 0 {
		return false
	}
	slash := strings.Index(remoteURL, "/")
	return slash < 0 || colon < slash
}

// isLoopbackHost reports whether host names this machine's loopback interface.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
