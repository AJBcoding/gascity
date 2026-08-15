package builtinpacks

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
)

func TestBundledGastownEmbedsOverlayFiles(t *testing.T) {
	pack, ok := ByName("gastown")
	if !ok {
		t.Fatal("missing bundled gastown pack")
	}
	if _, err := fs.Stat(pack.FS, "overlay/per-provider/codex/.codex/hooks.json"); err != nil {
		t.Fatalf("bundled gastown pack missing codex overlay hooks: %v", err)
	}
	if _, err := fs.Stat(pack.FS, "embed.go"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("embed.go stat err = %v, want not exist in embedded pack data", err)
	}
}

func TestBundledGastownWitnessPatrolOverlayAppliesThroughOpen(t *testing.T) {
	pack, ok := ByName("gastown")
	if !ok {
		t.Fatal("missing bundled gastown pack")
	}
	file, err := pack.FS.Open("formulas/mol-witness-patrol.toml")
	if err != nil {
		t.Fatalf("opening witness patrol formula: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("closing witness patrol formula: %v", err)
		}
	}()
	opened, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("reading opened witness patrol formula: %v", err)
	}
	readFile, err := fs.ReadFile(pack.FS, "formulas/mol-witness-patrol.toml")
	if err != nil {
		t.Fatalf("ReadFile witness patrol formula: %v", err)
	}
	if !bytes.Equal(opened, readFile) {
		t.Fatal("Open and ReadFile returned different witness patrol formula bytes")
	}
	for _, want := range []string{
		`metadata."gc.session_id"`,
		`CLAIMING_SESSION_ID`,
		`CURRENT_ALIAS_SESSION_ID`,
		`stalled-not-orphaned`,
		`UNVERIFIABLE`,
	} {
		if !strings.Contains(string(opened), want) {
			t.Errorf("patched witness patrol formula missing %q", want)
		}
	}
}
