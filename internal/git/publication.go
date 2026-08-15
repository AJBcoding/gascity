package git

import (
	"fmt"
	"net"
	"net/url"
	"os"
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
// publication evidence: a URL naming another host counts as publication, but a
// local URL whose target is this repository, is missing, or cannot be resolved
// does not get to clear a durability check.

// Remote is one configured remote, classified for durability purposes.
type Remote struct {
	// Name is the remote's configured name.
	Name string
	// URL is the remote's fetch URL, empty when it could not be read.
	URL string
	// Self reports that the URL resolves back into this same repository, so
	// the remote confers no durability.
	Self bool
	// Publication reports that the remote may be consulted as evidence that
	// work has been delivered. It is not simply !Self: a remote whose name
	// cannot be passed to git is neither self-classifiable nor usable as
	// evidence, so it is excluded from both.
	Publication bool
}

// Remotes returns every configured remote, classified. It is the single
// classification pass the durability accessors below project from; callers that
// need to describe the topology to a human (rather than just filter it) use it
// directly.
//
// An unreadable remote list is an error rather than an empty list, so no caller
// can mistake a broken probe for "nothing to publish to".
func (g *Git) Remotes() ([]Remote, error) {
	out, err := g.run("remote")
	if err != nil {
		return nil, fmt.Errorf("listing remotes: %w", err)
	}
	id, err := g.repoIdentity()
	if err != nil {
		return nil, err
	}
	remotes := []Remote{}
	for _, name := range strings.Fields(out) {
		if strings.HasPrefix(name, "-") {
			// Unprobeable as a git argument, so it cannot be cleared as
			// self-referential — it counts as configured (the repo is not
			// local-only) but never as publication.
			remotes = append(remotes, Remote{Name: name})
			continue
		}
		urlOut, urlErr := g.run("remote", "get-url", name)
		if urlErr != nil {
			// A remote whose URL cannot be read is not provably a publication
			// target. Keep it configured, but do not let it clear durability
			// checks.
			remotes = append(remotes, Remote{Name: name})
			continue
		}
		rawURL := strings.TrimSpace(urlOut)
		self, publication := classifyRemoteURL(rawURL, id)
		remotes = append(remotes, Remote{Name: name, URL: rawURL, Self: self, Publication: publication})
	}
	return remotes, nil
}

// PublicationRemotes returns the names of the repository's remotes that can
// confer durability — configured remotes whose URL can publish away from this
// repository — alongside the full configured list it was filtered from.
//
// Callers need both because an empty publication list is ambiguous on its own:
// no remotes at all is a local-only repo, while remotes that all filter out is
// the herdr-src misconfiguration, and callers may answer those differently.
func (g *Git) PublicationRemotes() (publication, configured []string, err error) {
	remotes, err := g.Remotes()
	if err != nil {
		return nil, nil, err
	}
	publication, configured = []string{}, []string{}
	for _, r := range remotes {
		configured = append(configured, r.Name)
		if r.Publication {
			publication = append(publication, r.Name)
		}
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
	id := repoIdentityFromCommonDir(selfCommon)
	self, _ := classifyRemoteURL(remoteURL, id)
	return self
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

// repoIdentity is the filesystem identity a remote URL is compared against.
// dirs contains paths that identify this repository, while urlBase is the
// directory git uses when resolving relative remote URLs.
type repoIdentity struct {
	dirs    []string
	urlBase string
}

func (g *Git) repoIdentity() (repoIdentity, error) {
	common, err := g.run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repoIdentity{}, fmt.Errorf("resolving git common dir: %w", err)
	}
	id := repoIdentityFromCommonDir(strings.TrimSpace(common))
	if top, err := g.run("rev-parse", "--path-format=absolute", "--show-toplevel"); err == nil {
		id.urlBase = cleanPath(strings.TrimSpace(top))
	}
	if id.urlBase == "" && len(id.dirs) > 0 {
		id.urlBase = id.dirs[0]
	}
	return id, nil
}

func repoIdentityFromCommonDir(common string) repoIdentity {
	common = cleanPath(common)
	id := repoIdentity{}
	if common == "" {
		return id
	}
	id.dirs = append(id.dirs, common)
	if filepath.Base(common) == ".git" {
		root := filepath.Dir(common)
		id.dirs = append(id.dirs, root)
		id.urlBase = root
	} else {
		id.urlBase = common
	}
	return id
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func classifyRemoteURL(rawURL string, id repoIdentity) (self, publication bool) {
	path, local := localPathForRemoteURL(rawURL)
	if !local {
		return false, true
	}
	if path == "" {
		return false, false
	}
	if !filepath.IsAbs(path) {
		if id.urlBase == "" {
			return false, false
		}
		path = filepath.Join(id.urlBase, path)
	}
	if remotePathIsSelf(path, id) {
		return true, false
	}
	if _, err := os.Stat(path); err != nil {
		return false, false
	}
	return false, true
}

func remotePathIsSelf(path string, id repoIdentity) bool {
	path = cleanPath(path)
	if path == "" {
		return false
	}
	if common := CommonDir(path); common != "" && identityContains(id, common) {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, dir := range id.dirs {
		selfInfo, err := os.Stat(dir)
		if err != nil {
			continue
		}
		if os.SameFile(info, selfInfo) {
			return true
		}
		gitInfo, err := os.Stat(filepath.Join(path, ".git"))
		if err == nil && os.SameFile(gitInfo, selfInfo) {
			return true
		}
	}
	return false
}

func identityContains(id repoIdentity, path string) bool {
	path = cleanPath(path)
	for _, dir := range id.dirs {
		if cleanPath(dir) == path {
			return true
		}
	}
	return false
}

// localPathForRemoteURL maps a remote URL to the filesystem path it addresses on
// this machine. The second return value is false when the URL names another
// machine or a server route whose path is not a local filesystem path.
func localPathForRemoteURL(remoteURL string) (string, bool) {
	if remoteURL == "" {
		return "", false
	}
	if strings.Contains(remoteURL, "://") {
		return schemeLocalPath(remoteURL)
	}
	if path, ok := scpLocalPath(remoteURL); ok {
		return path, true
	} else if looksLikeSCPRemote(remoteURL) {
		return "", false
	}
	return remoteURL, true
}

func schemeLocalPath(remoteURL string) (string, bool) {
	u, err := url.Parse(remoteURL)
	if err != nil {
		return "", true
	}
	switch strings.ToLower(u.Scheme) {
	case "file":
		if u.Host != "" && !isLoopbackHost(u.Hostname()) {
			return "", false
		}
		return u.Path, true
	case "ssh", "git", "git+ssh", "ssh+git":
		if !isLoopbackHost(u.Hostname()) {
			return "", false
		}
		return u.Path, true
	default:
		if isLoopbackHost(u.Hostname()) {
			return u.Path, true
		}
		return "", false
	}
}

func scpLocalPath(remoteURL string) (string, bool) {
	colon := strings.Index(remoteURL, ":")
	if colon <= 1 {
		return "", false
	}
	slash := strings.Index(remoteURL, "/")
	if slash >= 0 && slash < colon {
		return "", false
	}
	host := remoteURL[:colon]
	if _, after, ok := strings.Cut(host, "@"); ok {
		host = after
	}
	if !isLoopbackHost(host) {
		return "", false
	}
	path := remoteURL[colon+1:]
	if !filepath.IsAbs(path) {
		return "", true
	}
	return path, true
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
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || host == "localhost.localdomain" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
