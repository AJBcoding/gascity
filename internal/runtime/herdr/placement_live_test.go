package herdr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// TestEnsurePlacementLiveSingleTabRecycle drives the herdr 0.8.0 upgrade
// blocker scenario against a real herdr server: a workspace holding exactly
// one tab whose label matches the session being restarted. ensurePlacement
// must replace the tab without the workspace dying underneath it — under
// herdr ≥0.8.0 closing a workspace's last tab closes the workspace itself,
// so close-before-create destroys the workspace the create needs. Skipped
// when herdr is unavailable or in -short mode.
//
// GC_TEST_HERDR_BIN aims the test at a specific herdr binary (with HOME
// overridden for state isolation) so the same scenario can be proven against
// a candidate herdr version before any system upgrade.
func TestEnsurePlacementLiveSingleTabRecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live herdr placement test in -short mode")
	}
	bin := os.Getenv("GC_TEST_HERDR_BIN")
	if bin == "" {
		bin = "herdr"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("herdr not installed")
	}

	c := newClient(fmt.Sprintf("gctest-place-%d", os.Getpid()), "")
	c.bin = bin
	if err := c.startServer(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = c.stopServer() })

	ctx := context.Background()
	cwd := t.TempDir()
	// upstream dropped wsID from workspaceCreate's returns; the workspace id
	// is resolved by label instead, the same way client.go does it.
	oldTab, _, err := c.workspaceCreate(ctx, "placews", cwd, nil)
	if err != nil {
		t.Fatalf("workspace create: %v", err)
	}
	wsID, err := c.findWorkspace(ctx, "placews")
	if err != nil || wsID == "" {
		t.Fatalf("find workspace after create = (%q, %v); want a live workspace id", wsID, err)
	}
	if err := c.tabRename(ctx, oldTab, "witness"); err != nil {
		t.Fatalf("tab rename: %v", err)
	}

	newTab, paneID, err := c.ensurePlacement(ctx, "placews", "witness", cwd, nil)
	if err != nil {
		t.Fatalf("ensurePlacement on a live single-tab workspace: %v", err)
	}
	if newTab == oldTab || newTab == "" || paneID == "" {
		t.Fatalf("ensurePlacement = (%q, %q); want a fresh tab replacing %q", newTab, paneID, oldTab)
	}

	if ws, err := c.findWorkspace(ctx, "placews"); err != nil || ws != wsID {
		t.Fatalf("workspace after placement = (%q, %v); want %q still alive", ws, err, wsID)
	}
	tabs, err := c.listTabs(ctx, wsID)
	if err != nil {
		t.Fatalf("tab list: %v", err)
	}
	if len(tabs) != 1 || tabs[0].TabID != newTab {
		t.Fatalf("tabs after placement = %+v; want exactly the replacement %q", tabs, newTab)
	}
}
