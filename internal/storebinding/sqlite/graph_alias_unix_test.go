//go:build unix

package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/storebinding"
)

func TestGraphPathCanonicalizesRootSymlink(t *testing.T) {
	root := t.TempDir()
	alias := root + "-alias"
	if err := os.Symlink(root, alias); err != nil {
		t.Fatalf("creating Graph root symlink: %v", err)
	}

	direct, err := GraphPath(root)
	if err != nil {
		t.Fatalf("GraphPath(real root): %v", err)
	}
	throughAlias, err := GraphPath(alias)
	if err != nil {
		t.Fatalf("GraphPath(symlink root): %v", err)
	}
	if throughAlias != direct {
		t.Fatalf("GraphPath(symlink root) = %q, want canonical path %q", throughAlias, direct)
	}
	// t.TempDir() is not itself guaranteed to be symlink-free: on macOS it lands
	// under /var/folders/..., and /var is a symlink to /private/var, so the raw
	// root is a different spelling of the same directory GraphPath canonicalizes
	// to. Compare against the resolved root instead of assuming the platform
	// handed back an already-canonical one. The check keeps its teeth — a
	// GraphPath that retained the alias still answers ...-alias here and fails.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving Graph root: %v", err)
	}
	if filepath.Dir(filepath.Dir(throughAlias)) != realRoot {
		t.Fatalf("GraphPath(symlink root) = %q, want a path rooted at the real root %q", throughAlias, realRoot)
	}
}

func TestInspectGraphRejectsHardLinkedDatabase(t *testing.T) {
	root := t.TempDir()
	graphDir := filepath.Join(root, graphDirectoryName)
	source := openGraphSource(t, graphDir)
	t.Cleanup(func() { _ = source.CloseStore() })
	databasePath := filepath.Join(graphDir, graphFilename)
	if err := source.CloseStore(); err != nil {
		t.Fatalf("closing Graph source: %v", err)
	}
	if err := os.Link(databasePath, filepath.Join(root, "database-alias.sqlite")); err != nil {
		t.Fatalf("creating hard-linked Graph database alias: %v", err)
	}

	_, err := InspectGraph(context.Background(), storebinding.BindingSpec{
		Name:     storebinding.BindingName("infra"),
		Provider: ProviderID,
		Path:     root,
	})
	if err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("InspectGraph() error = %v, want hard-linked database rejection", err)
	}
}

func TestPhysicalIdentityDoesNotDependOnPathSpelling(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.sqlite")
	second := filepath.Join(directory, "second.sqlite")
	if err := os.WriteFile(first, sqliteRollbackHeaderForTest(), 0o600); err != nil {
		t.Fatalf("writing first database: %v", err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatalf("creating hard-link alias: %v", err)
	}
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatalf("stating first database: %v", err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatalf("stating second database: %v", err)
	}
	if physicalIdentity(first, firstInfo) != physicalIdentity(second, secondInfo) {
		t.Fatalf("physical identity differs across hard-link aliases: %q != %q", physicalIdentity(first, firstInfo), physicalIdentity(second, secondInfo))
	}
}

func sqliteRollbackHeaderForTest() []byte {
	header := make([]byte, 100)
	copy(header, "SQLite format 3\x00")
	header[18] = 1
	header[19] = 1
	return header
}
