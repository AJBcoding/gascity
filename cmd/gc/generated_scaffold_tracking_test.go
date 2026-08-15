package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/materialize"
)

func TestGeneratedSessionScaffoldIsNotTracked(t *testing.T) {
	root := repoRootForScaffoldTracking(t)

	generated := append([]string{
		".claude/skills/.gc-skill-ownership.json",
		".claude/skills/core.gc-agents",
		".claude/skills/core.gc-city",
		".claude/skills/core.gc-dashboard",
		".claude/skills/core.gc-dispatch",
		".claude/skills/core.gc-mail",
		".claude/skills/core.gc-rigs",
		".claude/skills/core.gc-work",
		".claude/skills/gc-debug.debugging-gas-issues",
	}, managedMCPGitignoreEntries...)

	if tracked := trackedPathsForScaffoldTracking(t, root, generated...); len(tracked) > 0 {
		t.Fatalf("generated session scaffold is tracked and can be swept into salvage commits: %v", tracked)
	}
	for _, rel := range generated {
		if !gitCheckIgnoreForScaffoldTracking(t, root, rel) {
			t.Fatalf("%s is not ignored; untracked session scaffolding can still appear dirty", rel)
		}
	}

	for _, rel := range []string{
		".claude/skills/gascity-docs/SKILL.md",
		".claude/skills/gascity-docs/references/terminology.md",
	} {
		if tracked := trackedPathsForScaffoldTracking(t, root, rel); len(tracked) != 1 || tracked[0] != rel {
			t.Fatalf("canonical gascity docs skill %s is not tracked as source; tracked=%v", rel, tracked)
		}
		if gitCheckIgnoreForScaffoldTracking(t, root, rel) {
			t.Fatalf("canonical gascity docs skill %s is ignored", rel)
		}
	}

	if tracked := trackedPathsForScaffoldTracking(t, root, "mcp/excalidraw.toml"); len(tracked) != 1 || tracked[0] != "mcp/excalidraw.toml" {
		t.Fatalf("neutral Excalidraw MCP source should remain tracked; tracked=%v", tracked)
	}
	data, err := os.ReadFile(filepath.Join(root, "mcp", "excalidraw.toml"))
	if err != nil {
		t.Fatalf("read mcp/excalidraw.toml: %v", err)
	}
	if !bytes.Contains(data, []byte("mcp.excalidraw.com")) {
		t.Fatal("mcp/excalidraw.toml no longer carries the project Excalidraw MCP configuration")
	}
	servers, err := materialize.LoadMCPDir(filepath.Join(root, "mcp"), "repo", nil)
	if err != nil {
		t.Fatalf("load neutral MCP source: %v", err)
	}
	if len(servers) != 1 || servers[0].Name != "excalidraw" || servers[0].URL != "https://mcp.excalidraw.com" {
		t.Fatalf("neutral MCP source = %+v, want one Excalidraw HTTP server", servers)
	}
}

func trackedPathsForScaffoldTracking(t *testing.T, root string, rels ...string) []string {
	t.Helper()
	out := runGit(t, root, append([]string{"ls-files", "--"}, rels...)...)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func gitCheckIgnoreForScaffoldTracking(t *testing.T, root, rel string) bool {
	t.Helper()
	out := strings.TrimSpace(runGit(t, root, "check-ignore", "--no-index", "-v", "-n", "--", rel))
	fields := strings.SplitN(out, "\t", 2)
	if len(fields) == 0 {
		return false
	}
	sourcePattern := fields[0]
	lastColon := strings.LastIndex(sourcePattern, ":")
	if lastColon < 0 || lastColon == len(sourcePattern)-1 {
		return false
	}
	return !strings.HasPrefix(sourcePattern[lastColon+1:], "!")
}

func repoRootForScaffoldTracking(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from cmd/gc")
		}
		dir = parent
	}
}
