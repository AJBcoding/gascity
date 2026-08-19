package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

// Body-source tests for gas-ugam.
//
// `gc mail send` accepted a body only through -m/--message, so every agent
// composing a markdown body in a shell invocation had to inline it. Mail
// bodies routinely carry backticks around command names and paths, and inside
// a double-quoted shell string a backtick is command substitution — the shell
// executes the body before gc ever sees it. On 2026-08-19 that ran `make
// install` from a deploy report, the one command this city's deploy path bans.
//
// --body-file removes the shell from the path between the author and the body:
// the bytes travel by file descriptor, never through argv quoting. These tests
// pin that the content arrives verbatim, and that a body source which yields
// nothing fails loudly instead of sending an empty message.

// newMailBodyFileCity builds a city fixture with a live sender and recipient,
// so a failure in these tests can only come from the body-source path.
func newMailBodyFileCity(t *testing.T) string {
	t.Helper()
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_MAIL", "")
	t.Setenv("GC_SESSION_ID", "")
	t.Setenv("GC_AGENT", "")

	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"test-city\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(city.toml): %v", err)
	}
	t.Setenv("GC_CITY", cityPath)

	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	for _, alias := range []string{"sender", "recipient"} {
		if _, err := store.Create(beads.Bead{
			Type:   session.BeadType,
			Labels: []string{session.LabelSession},
			Metadata: map[string]string{
				"alias":        alias,
				"session_name": alias + "-gc-1",
			},
		}); err != nil {
			t.Fatalf("Create %s: %v", alias, err)
		}
	}
	t.Setenv("GC_ALIAS", "sender")
	return cityPath
}

// mailBodyFileMessages returns every message bead in the city.
func mailBodyFileMessages(t *testing.T, cityPath string) []beads.Bead {
	t.Helper()
	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	all, err := store.List(beads.ListQuery{
		Type:     "message",
		Status:   "open",
		TierMode: beads.TierBoth,
	})
	if err != nil {
		t.Fatalf("List messages: %v", err)
	}
	var msgs []beads.Bead
	for _, b := range all {
		if b.Type == "message" {
			msgs = append(msgs, b)
		}
	}
	return msgs
}

// runMailSend executes the send command with args and returns what it wrote
// and whether it failed.
func runMailSend(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd := newMailSendCmd(&outBuf, &errBuf)
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// The body that caused the incident: backticks around a command name, a
// dollar-paren, and a double quote — every one of which the shell rewrites
// inside an inlined -m argument.
const mailBodyFileHostileBody = "Deploy report\n\n" +
	"Do NOT run `make install` on this host.\n" +
	"The shim lives at `~/.local/bin/gc` and $(go env GOPATH)/bin is wrong.\n" +
	"Quoting check: \"double\" and 'single'.\n"

func TestMailSendBodyFileDeliversContentsVerbatim(t *testing.T) {
	cityPath := newMailBodyFileCity(t)

	bodyPath := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyPath, []byte(mailBodyFileHostileBody), 0o644); err != nil {
		t.Fatalf("WriteFile(body.md): %v", err)
	}

	stdout, stderr, err := runMailSend(t, "recipient", "-s", "Deploy report", "--body-file", bodyPath)
	if err != nil {
		t.Fatalf("mail send --body-file: %v; stdout=%s stderr=%s", err, stdout, stderr)
	}

	msgs := mailBodyFileMessages(t, cityPath)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1; %#v", len(msgs), msgs)
	}
	want := strings.TrimRight(mailBodyFileHostileBody, "\n")
	if msgs[0].Description != want {
		t.Errorf("body = %q, want %q", msgs[0].Description, want)
	}
	if msgs[0].Title != "Deploy report" {
		t.Errorf("subject = %q, want %q", msgs[0].Title, "Deploy report")
	}
}

// Without -s the subject is derived from the body's first line, exactly as it
// is for an inlined -m body. --body-file is a body source, not a second mode.
func TestMailSendBodyFileWithoutSubjectDerivesSubjectFromFirstLine(t *testing.T) {
	cityPath := newMailBodyFileCity(t)

	bodyPath := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyPath, []byte(mailBodyFileHostileBody), 0o644); err != nil {
		t.Fatalf("WriteFile(body.md): %v", err)
	}

	stdout, stderr, err := runMailSend(t, "recipient", "--body-file", bodyPath)
	if err != nil {
		t.Fatalf("mail send --body-file without -s: %v; stdout=%s stderr=%s", err, stdout, stderr)
	}

	msgs := mailBodyFileMessages(t, cityPath)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1; %#v", len(msgs), msgs)
	}
	if msgs[0].Title != "Deploy report" {
		t.Errorf("subject = %q, want the body's first line %q", msgs[0].Title, "Deploy report")
	}
	if want := strings.TrimRight(mailBodyFileHostileBody, "\n"); msgs[0].Description != want {
		t.Errorf("body = %q, want %q", msgs[0].Description, want)
	}
}

func TestMailSendBodyFileDashReadsStdin(t *testing.T) {
	cityPath := newMailBodyFileCity(t)

	orig := mailSendStdin
	t.Cleanup(func() { mailSendStdin = orig })
	mailSendStdin = func() io.Reader { return strings.NewReader(mailBodyFileHostileBody) }

	stdout, stderr, err := runMailSend(t, "recipient", "-s", "Deploy report", "--body-file", "-")
	if err != nil {
		t.Fatalf("mail send --body-file -: %v; stdout=%s stderr=%s", err, stdout, stderr)
	}

	msgs := mailBodyFileMessages(t, cityPath)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1; %#v", len(msgs), msgs)
	}
	if want := strings.TrimRight(mailBodyFileHostileBody, "\n"); msgs[0].Description != want {
		t.Errorf("body = %q, want %q", msgs[0].Description, want)
	}
}

// A body file that does not exist must not degrade into an empty-bodied send:
// the operator asked for those bytes and did not get them.
func TestMailSendBodyFileMissingFileRefusesToSend(t *testing.T) {
	cityPath := newMailBodyFileCity(t)

	missing := filepath.Join(t.TempDir(), "absent.md")
	stdout, stderr, err := runMailSend(t, "recipient", "-s", "Deploy report", "--body-file", missing)
	if err == nil {
		t.Fatalf("mail send --body-file <missing> = nil error, want refusal; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "--body-file") {
		t.Errorf("stderr = %q, want it to name --body-file", stderr)
	}
	if msgs := mailBodyFileMessages(t, cityPath); len(msgs) != 0 {
		t.Errorf("got %d messages, want 0 — a failed body read must not send", len(msgs))
	}
}

// An empty body source is a truncated write or an empty pipe, not an intent to
// send nothing. Silently delivering an empty body is how the city accumulated
// enough junk mail to need `gc mail archive --empty-body`.
func TestMailSendBodyFileEmptyContentRefusesToSend(t *testing.T) {
	cityPath := newMailBodyFileCity(t)

	bodyPath := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(bodyPath, []byte("\n\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(empty.md): %v", err)
	}

	stdout, stderr, err := runMailSend(t, "recipient", "-s", "Deploy report", "--body-file", bodyPath)
	if err == nil {
		t.Fatalf("mail send --body-file <empty> = nil error, want refusal; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "no body content") {
		t.Errorf("stderr = %q, want a no-body-content refusal", stderr)
	}
	if msgs := mailBodyFileMessages(t, cityPath); len(msgs) != 0 {
		t.Errorf("got %d messages, want 0 — an empty body source must not send", len(msgs))
	}
}

// Two body sources at once is ambiguous, and silently preferring one is how a
// caller ships the wrong bytes without noticing.
func TestMailSendBodyFileAndMessageAreMutuallyExclusive(t *testing.T) {
	cityPath := newMailBodyFileCity(t)

	bodyPath := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyPath, []byte("from the file\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(body.md): %v", err)
	}

	stdout, stderr, err := runMailSend(t, "recipient", "-s", "Subject", "-m", "inline", "--body-file", bodyPath)
	if err == nil {
		t.Fatalf("mail send -m ... --body-file ... = nil error, want refusal; stdout=%s stderr=%s", stdout, stderr)
	}
	if msgs := mailBodyFileMessages(t, cityPath); len(msgs) != 0 {
		t.Errorf("got %d messages, want 0 — an ambiguous body source must not send", len(msgs))
	}
}

func TestMailSendDeclaresBodyFileFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newMailSendCmd(&stdout, &stderr)
	if cmd.Flags().Lookup("body-file") == nil {
		t.Fatal("send command missing --body-file flag")
	}
}
