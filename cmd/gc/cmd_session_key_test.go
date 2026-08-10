package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestEmitSessionKeyResultText covers the operator-facing confirmation for
// gas-bjms: the line has to name the keys that were delivered, because the
// whole defect class is a transport reporting a success the recipient never
// saw.
func TestEmitSessionKeyResultText(t *testing.T) {
	var stdout bytes.Buffer
	emitSessionKeyResult(&stdout, io.Discard, "furiosa", []string{"Down", "Space", "Enter"}, false)
	got := stdout.String()
	if !strings.Contains(got, "furiosa") {
		t.Fatalf("stdout = %q, want the target named", got)
	}
	if !strings.Contains(got, "Down Space Enter") {
		t.Fatalf("stdout = %q, want the delivered keys echoed", got)
	}
}

func TestEmitSessionKeyResultJSON(t *testing.T) {
	var stdout bytes.Buffer
	emitSessionKeyResult(&stdout, io.Discard, "furiosa", []string{"Escape"}, true)
	got := stdout.String()
	for _, want := range []string{`"schema_version":"1"`, `"ok":true`, `"target":"furiosa"`, `"keys":["Escape"]`} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout = %q, missing %s", got, want)
		}
	}
}

// TestParseSessionKeysRejectsBlanks keeps a blank argument from reaching the
// provider, where SendKeys is best-effort and would report success for a
// delivery of nothing.
func TestParseSessionKeysRejectsBlanks(t *testing.T) {
	if _, err := parseSessionKeys([]string{"Enter", "  "}); err == nil {
		t.Fatal("parseSessionKeys with a blank key succeeded, want an error")
	}
	if _, err := parseSessionKeys(nil); err == nil {
		t.Fatal("parseSessionKeys with no keys succeeded, want an error")
	}
	got, err := parseSessionKeys([]string{"Down", "C-c"})
	if err != nil {
		t.Fatalf("parseSessionKeys: %v", err)
	}
	if len(got) != 2 || got[0] != "Down" || got[1] != "C-c" {
		t.Fatalf("parseSessionKeys = %#v, want the keys passed through verbatim", got)
	}
}

// TestSessionCmdRegistersKeySubcommand guards the discovery path: an operator
// facing a blocked agent finds this verb through `gc session` itself.
func TestSessionCmdRegistersKeySubcommand(t *testing.T) {
	cmd := newSessionCmd(io.Discard, io.Discard)
	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Name() == "key" {
			found = true
			if sub.Short == "" {
				t.Error("key subcommand has no Short description")
			}
		}
	}
	if !found {
		t.Fatal("gc session has no `key` subcommand")
	}

	var stderr bytes.Buffer
	cmd = newSessionCmd(io.Discard, &stderr)
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("bare `gc session` succeeded, want the missing-subcommand error")
	}
	if !strings.Contains(stderr.String(), "key") {
		t.Fatalf("missing-subcommand hint = %q, want it to list key", stderr.String())
	}
}
