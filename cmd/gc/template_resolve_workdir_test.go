package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

func writeTemplateResolveCityConfig(t *testing.T, cityPath, beadsProvider string) {
	t.Helper()

	content := "[workspace]\nname = \"city\"\n"
	if beadsProvider != "" {
		content += "\n[beads]\nprovider = \"" + beadsProvider + "\"\n"
	}
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
}

func TestResolveTemplateUsesWorkDirWithoutChangingRigIdentity(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(cityPath, "demo")
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "demo", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{
		Name:    "witness",
		Dir:     "demo",
		WorkDir: ".gc/agents/{{.Rig}}/witness",
	}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	wantWorkDir := filepath.Join(cityPath, ".gc", "agents", "demo", "witness")
	if tp.WorkDir != wantWorkDir {
		t.Fatalf("WorkDir = %q, want %q", tp.WorkDir, wantWorkDir)
	}
	if tp.RigName != "demo" {
		t.Fatalf("RigName = %q, want demo", tp.RigName)
	}
	if tp.RigRoot != rigRoot {
		t.Fatalf("RigRoot = %q, want %q", tp.RigRoot, rigRoot)
	}
	if tp.Env["GC_RIG"] != "demo" {
		t.Fatalf("GC_RIG = %q, want demo", tp.Env["GC_RIG"])
	}
	if tp.Env["GC_RIG_ROOT"] != rigRoot {
		t.Fatalf("GC_RIG_ROOT = %q, want %q", tp.Env["GC_RIG_ROOT"], rigRoot)
	}
	if tp.Env["BEADS_DIR"] != filepath.Join(rigRoot, ".beads") {
		t.Fatalf("BEADS_DIR = %q, want %q", tp.Env["BEADS_DIR"], filepath.Join(rigRoot, ".beads"))
	}
	if tp.Env["GT_ROOT"] != cityPath {
		t.Fatalf("GT_ROOT = %q, want city root %q", tp.Env["GT_ROOT"], cityPath)
	}
}

func TestResolveTemplateUsesWorkDirForCityScopedAgents(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{
		Name:    "mayor",
		WorkDir: ".gc/agents/mayor",
	}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	wantWorkDir := filepath.Join(cityPath, ".gc", "agents", "mayor")
	if tp.WorkDir != wantWorkDir {
		t.Fatalf("WorkDir = %q, want %q", tp.WorkDir, wantWorkDir)
	}
	if tp.RigName != "" {
		t.Fatalf("RigName = %q, want empty", tp.RigName)
	}
	if got, ok := tp.Env["GC_RIG"]; !ok || got != "" {
		t.Fatalf("GC_RIG = %q present=%v, want explicit empty", got, ok)
	}
	if got, ok := tp.Env["GC_RIG_ROOT"]; !ok || got != "" {
		t.Fatalf("GC_RIG_ROOT = %q present=%v, want explicit empty", got, ok)
	}
	if got, ok := tp.Env["BEADS_DIR"]; !ok || got != "" {
		t.Fatalf("BEADS_DIR = %q present=%v, want explicit empty", got, ok)
	}
	if tp.Env["GT_ROOT"] != cityPath {
		t.Fatalf("GT_ROOT = %q, want %q", tp.Env["GT_ROOT"], cityPath)
	}
	if tp.Env["GC_BEADS"] != "file" {
		t.Fatalf("GC_BEADS = %q, want file", tp.Env["GC_BEADS"])
	}
}

func TestResolveTemplateDefaultsRigScopedAgentsToRigRootWithoutWorkDir(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "demo", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{
		Name: "refinery",
		Dir:  "demo",
	}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if tp.WorkDir != rigRoot {
		t.Fatalf("WorkDir = %q, want %q", tp.WorkDir, rigRoot)
	}
	if tp.RigRoot != rigRoot {
		t.Fatalf("RigRoot = %q, want %q", tp.RigRoot, rigRoot)
	}
	if tp.Env["BEADS_DIR"] != filepath.Join(rigRoot, ".beads") {
		t.Fatalf("BEADS_DIR = %q, want %q", tp.Env["BEADS_DIR"], filepath.Join(rigRoot, ".beads"))
	}
	if tp.Env["GT_ROOT"] != cityPath {
		t.Fatalf("GT_ROOT = %q, want city root %q", tp.Env["GT_ROOT"], cityPath)
	}
}

func TestResolveTemplateUsesRigScopeBeadsProviderForBdBackedRig(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(cityPath, "demo")
	if err := os.MkdirAll(filepath.Join(rigRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigRoot, ".beads", "metadata.json"), []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"embedded","dolt_database":"de"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "demo", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{Name: "worker", Dir: "demo"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if got := tp.Env["GC_BEADS"]; got != "bd" {
		t.Fatalf("GC_BEADS = %q, want bd for bd-backed rig", got)
	}
	if got := tp.Env["GC_BEADS_SCOPE_ROOT"]; got != rigRoot {
		t.Fatalf("GC_BEADS_SCOPE_ROOT = %q, want %q", got, rigRoot)
	}
}

// TestResolveTemplatePreStartResolvesRigRootForCityLevelRigScopedAgent guards
// gascity#1940: the pre_start substitution path must resolve {{.RigRoot}} and
// {{.AgentBase}} for a city-level rig-scoped agent — one with Scope="rig" whose
// Dir is not stamped to the rig (its work_dir is a city-level worktree), so the
// rig association lives only in the qualified-name prefix. The session-setup
// context flows from workdirutil.PathContextForQualifiedName, so the #2070
// qualified-name-prefix fallback reaches pre_start templates too, not just
// work_dir and the agent env. Without the unified context these would expand
// empty.
func TestResolveTemplatePreStartResolvesRigRootForCityLevelRigScopedAgent(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(cityPath, "rigs", "thriva")
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "thriva", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	// No Dir stamp; rig association lives only in the qualified-name prefix and
	// the work_dir is a city-level worktree outside the rig filesystem.
	agent := &config.Agent{
		Name:     "my_impl",
		Scope:    "rig",
		WorkDir:  ".gc/worktrees/my_impl",
		PreStart: []string{"echo rig={{.RigRoot}} base={{.AgentBase}}"},
	}
	tp, err := resolveTemplate(params, agent, "thriva/my_impl", nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if len(tp.Hints.PreStart) != 1 {
		t.Fatalf("PreStart = %v, want one expanded command", tp.Hints.PreStart)
	}
	want := "echo rig=" + rigRoot + " base=my_impl"
	if tp.Hints.PreStart[0] != want {
		t.Fatalf("PreStart[0] = %q, want %q", tp.Hints.PreStart[0], want)
	}
}

// TestResolveTemplatePreStartExposesRigDefaultBranch guards gas-e6r: a rig's
// configured default_branch must reach pre_start templates. Agent worktrees
// are created by a pack pre_start script, and that script has no other way to
// learn the rig's mainline — so without {{.DefaultBranch}} in the session-setup
// context every worktree is cut from whatever origin/HEAD happens to point at.
// A rig pinned to a non-default integration branch then gets worktrees based
// on main, and agents debug a tree that does not match the deployed binary.
func TestResolveTemplatePreStartExposesRigDefaultBranch(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(cityPath, "rigs", "gascity")
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "gascity", Path: rigRoot, DefaultBranch: "feat/mysql-first-class-backend"}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{
		Name:     "worker",
		Scope:    "rig",
		WorkDir:  ".gc/worktrees/worker",
		PreStart: []string{"worktree-setup.sh {{.RigRoot}} {{.WorkDir}} {{.AgentBase}} --base {{.DefaultBranch}}"},
	}
	tp, err := resolveTemplate(params, agent, "gascity/worker", nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if len(tp.Hints.PreStart) != 1 {
		t.Fatalf("PreStart = %v, want one expanded command", tp.Hints.PreStart)
	}
	want := "worktree-setup.sh " + rigRoot + " " + filepath.Join(cityPath, ".gc", "worktrees", "worker") +
		" worker --base feat/mysql-first-class-backend"
	if tp.Hints.PreStart[0] != want {
		t.Fatalf("PreStart[0] = %q, want %q", tp.Hints.PreStart[0], want)
	}
}

// TestResolveTemplatePreStartDefaultBranchFallsBackToProbe pins the
// no-configured-branch edge. Two properties matter to callers:
//
//   - The template still expands. A missing struct field makes tmpl.Execute
//     error, and expandSessionSetup's graceful fallback then keeps the RAW
//     command — handing the literal "{{.DefaultBranch}}" to the shell.
//   - The value falls through to defaultBranchFor's probe, which ends at the
//     git.DefaultBranch "main" backstop. So a pre_start script always receives
//     a concrete branch name, never an empty argument it has to special-case.
func TestResolveTemplatePreStartDefaultBranchFallsBackToProbe(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(cityPath, "rigs", "nobranch")
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "nobranch", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{
		Name:     "worker",
		Scope:    "rig",
		WorkDir:  ".gc/worktrees/worker",
		PreStart: []string{"setup.sh --base {{.DefaultBranch}}"},
	}
	tp, err := resolveTemplate(params, agent, "nobranch/worker", nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if len(tp.Hints.PreStart) != 1 {
		t.Fatalf("PreStart = %v, want one expanded command", tp.Hints.PreStart)
	}
	if got := tp.Hints.PreStart[0]; got != "setup.sh --base main" {
		t.Fatalf("PreStart[0] = %q, want %q (expanded to the probe fallback, not raw)", got, "setup.sh --base main")
	}
}

func TestResolveTemplateRigScopedEnvCarriesRigRoots(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	rigRoot := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(rigRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "demo", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{Name: "witness", Dir: "demo"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if tp.Env["GC_RIG_ROOT"] != rigRoot {
		t.Fatalf("GC_RIG_ROOT = %q, want %q", tp.Env["GC_RIG_ROOT"], rigRoot)
	}
	if tp.Env["BEADS_DIR"] != filepath.Join(rigRoot, ".beads") {
		t.Fatalf("BEADS_DIR = %q, want %q", tp.Env["BEADS_DIR"], filepath.Join(rigRoot, ".beads"))
	}
	if tp.Env["GT_ROOT"] != cityPath {
		t.Fatalf("GT_ROOT = %q, want city root %q", tp.Env["GT_ROOT"], cityPath)
	}
}

func TestResolveTemplateUsesCityManagedDoltPort(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "")
	stateDir := filepath.Join(cityPath, ".gc", "runtime", "packs", "dolt")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck // test cleanup

	port := ln.Addr().(*net.TCPAddr).Port
	state := doltRuntimeState{
		Running:   true,
		PID:       os.Getpid(),
		Port:      port,
		DataDir:   filepath.Join(cityPath, ".beads", "dolt"),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal dolt state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "dolt-state.json"), data, 0o644); err != nil {
		t.Fatalf("write dolt state: %v", err)
	}

	t.Setenv("GC_DOLT_PORT", "9999")

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{Name: "worker"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if got := tp.Env["GC_DOLT_PORT"]; got != strconv.Itoa(port) {
		t.Fatalf("GC_DOLT_PORT = %q, want %q", got, strconv.Itoa(port))
	}
	if got := tp.Env["GC_BIN"]; got == "" {
		t.Fatalf("GC_BIN = %q, want non-empty", got)
	}
	if got := tp.Env["GC_BEADS"]; got != "bd" {
		t.Fatalf("GC_BEADS = %q, want raw bd provider", got)
	}
}

func TestResolveTemplatePreservesLogicalAgentNameWhenSessionBeadExists(t *testing.T) {
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	sessionBead, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:worker"},
		Metadata: map[string]string{
			"template":     "worker",
			"agent_name":   "worker",
			"session_name": "worker",
			"alias":        "worker",
		},
	})
	if err != nil {
		t.Fatalf("Create session bead: %v", err)
	}
	snapshot, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("loadSessionBeadSnapshot: %v", err)
	}

	params := &agentBuildParams{
		cityName:     "city",
		cityPath:     cityPath,
		workspace:    &config.Workspace{Provider: "test"},
		providers:    map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:     func(string) (string, error) { return "/bin/echo", nil },
		fs:           fsys.OSFS{},
		beaconTime:   time.Unix(0, 0),
		beadStore:    store,
		sessionBeads: snapshot,
		beadNames:    make(map[string]string),
		stderr:       io.Discard,
	}

	agent := &config.Agent{Name: "worker"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if got := tp.SessionName; got != "worker" {
		t.Fatalf("SessionName = %q, want worker", got)
	}
	if got := tp.Env["GC_SESSION_ID"]; got != sessionBead.ID {
		t.Fatalf("GC_SESSION_ID = %q, want %q", got, sessionBead.ID)
	}
	if got := tp.Env["GC_AGENT"]; got != "worker" {
		t.Fatalf("GC_AGENT = %q, want worker", got)
	}
	if got := tp.Env["GC_ALIAS"]; got != "worker" {
		t.Fatalf("GC_ALIAS = %q, want worker", got)
	}
}

func TestResolveTemplateUsesCanonicalRigTargetAndPinsHome(t *testing.T) {
	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".gc", "runtime", "packs", "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	rigRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(rigRoot, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}

	wantPort := strconv.Itoa(writeReachableManagedDoltState(t, cityPath))
	if err := os.WriteFile(filepath.Join(rigRoot, ".beads", "config.yaml"), []byte(`issue_prefix: repo
gc.endpoint_origin: inherited_city
gc.endpoint_status: verified
dolt.auto-start: false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigRoot, ".beads", "dolt-server.port"), []byte("31364"), 0o644); err != nil {
		t.Fatal(err)
	}

	gcHome := filepath.Join(t.TempDir(), "gc-home")
	t.Setenv("GC_HOME", gcHome)
	t.Setenv("GC_DOLT_PORT", "9999")

	params := &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  &config.Workspace{Provider: "test"},
		providers:  map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		rigs:       []config.Rig{{Name: "repo", Path: rigRoot}},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}

	agent := &config.Agent{Name: "polecat", Dir: "repo"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}

	if got := tp.Env["GC_DOLT_PORT"]; got != wantPort {
		t.Fatalf("GC_DOLT_PORT = %q, want %q", got, wantPort)
	}
	if got := tp.Env["BEADS_DOLT_SERVER_PORT"]; got != wantPort {
		t.Fatalf("BEADS_DOLT_SERVER_PORT = %q, want %q", got, wantPort)
	}
	if got := tp.Env["GC_DOLT_HOST"]; got != "" {
		t.Fatalf("GC_DOLT_HOST = %q, want empty for managed target", got)
	}
	// HOME is intentionally passed through to agents (PR #272:
	// HOME/USER/XDG env passthrough for macOS Keychain and config access).
	// Verify it's present and matches the parent process.
	if got := tp.Env["HOME"]; got == "" {
		t.Fatalf("HOME should be passed through to agent env")
	}
}
