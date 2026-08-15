package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/mail/beadmail"
	"github.com/gastownhall/gascity/internal/session"
)

// Sender-authority tests for gas-j2t7.
//
// `gc mail send` used to fall back to the reserved operator identity "human"
// whenever GC_SESSION_ID, GC_ALIAS and GC_AGENT were all absent. A crew session
// that lost its ambient env was therefore stamped From:human — it acquired the
// operator's voice in the one channel this city uses to carry operator rulings,
// silently and without failing. These tests pin the fail-closed behavior:
// absent identity refuses to send, and "human" is claimable only by a process
// that is demonstrably not an agent.

// newMailAuthorityCity builds an identity-free city fixture with one live
// recipient session, so a refusal in these tests can only come from the sender
// gate and never from an unresolvable recipient.
func newMailAuthorityCity(t *testing.T) string {
	t.Helper()
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_MAIL", "")
	t.Setenv("GC_ALIAS", "")
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
	if _, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":        "recipient",
			"session_name": "recipient-gc-1",
		},
	}); err != nil {
		t.Fatalf("Create recipient: %v", err)
	}
	return cityPath
}

// mailAuthorityMessages returns every message bead in the city.
func mailAuthorityMessages(t *testing.T, cityPath string) []beads.Bead {
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

func TestAmbientMailIdentityCandidates_EmptyWhenIdentityEnvAbsent(t *testing.T) {
	t.Setenv("GC_ALIAS", "")
	_ = os.Unsetenv("GC_SESSION_ID")
	_ = os.Unsetenv("GC_AGENT")

	if got := ambientMailIdentityCandidates(); len(got) != 0 {
		t.Fatalf("ambientMailIdentityCandidates() = %#v, want empty (no identity may be invented)", got)
	}
}

func TestAmbientMailIdentityCandidates_OrdersSessionIDFirstThenAliasThenAgent(t *testing.T) {
	t.Setenv("GC_SESSION_ID", "gc-1941")
	t.Setenv("GC_ALIAS", "worker-1")
	t.Setenv("GC_AGENT", "worker")

	got := ambientMailIdentityCandidates()
	want := []string{"gc-1941", "worker-1", "worker"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ambientMailIdentityCandidates() = %#v, want %#v", got, want)
	}
}

// The read paths (gc mail inbox/check) still resolve to the operator mailbox
// when no agent identity is ambient. Only the authority-writing paths fail
// closed, so this fallback must survive the gas-j2t7 fix.
func TestDefaultMailIdentityCandidates_ReadPathStillFallsBackToHuman(t *testing.T) {
	t.Setenv("GC_ALIAS", "")
	_ = os.Unsetenv("GC_SESSION_ID")
	_ = os.Unsetenv("GC_AGENT")

	got := defaultMailIdentityCandidates()
	want := []string{"human"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("defaultMailIdentityCandidates() = %#v, want %#v", got, want)
	}
}

// The P1 regression: a session with no ambient identity must not be stamped
// as the operator. az-wisp-9ft3r reached the mayor's inbox From:human this way.
func TestCmdMailSend_RefusesToStampHumanWhenIdentityEnvAbsent(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "", "", "", "", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailSend() = 0, want refusal; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no sender identity") {
		t.Fatalf("stderr = %q, want a no-sender-identity refusal", stderr.String())
	}

	for _, msg := range mailAuthorityMessages(t, cityPath) {
		t.Fatalf("message was delivered despite missing identity: From=%q Assignee=%q", msg.From, msg.Assignee)
	}
}

func TestCmdMailReply_RefusesToStampHumanWhenIdentityEnvAbsent(t *testing.T) {
	newMailAuthorityCity(t)

	var stdout, stderr bytes.Buffer
	code := cmdMailReplyFrom([]string{"msg-does-not-matter", "body"}, "", "", "", false, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailReplyFrom() = 0, want refusal; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	// The gate must precede delivery: the refusal is about identity, not about
	// the message being unresolvable.
	if !strings.Contains(stderr.String(), "no sender identity") {
		t.Fatalf("stderr = %q, want a no-sender-identity refusal before any reply attempt", stderr.String())
	}
}

// The operator keeps their path: with no agent identity ambient, an explicit
// --from human is a deliberate claim rather than a silent default.
func TestCmdMailSend_AllowsExplicitHumanWhenIdentityEnvAbsent(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "human", "", "", "", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdMailSend(--from human) = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	msgs := mailAuthorityMessages(t, cityPath)
	if len(msgs) != 1 {
		t.Fatalf("message count = %d, want 1; msgs=%#v", len(msgs), msgs)
	}
	if msgs[0].From != "human" {
		t.Fatalf("message From = %q, want human", msgs[0].From)
	}
}

// Reply is the operator's only way back into a thread, and it has no ambient
// identity to fall back on once the default is gone — so --from must work here
// exactly as it does for send.
func TestCmdMailReply_AllowsExplicitHumanWhenIdentityEnvAbsent(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	mp := beadmail.New(store)
	original, err := mp.Send("recipient", "human", "Hello", "first")
	if err != nil {
		t.Fatalf("mp.Send(): %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdMailReplyFrom([]string{original.ID, "reply body"}, "human", "", "", false, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdMailReplyFrom(--from human) = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "to recipient") {
		t.Fatalf("stdout = %q, want the reply addressed back to recipient", stdout.String())
	}
}

func TestCmdMailSend_RejectsForeignExplicitSenderFromAgentSession(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	if _, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":        "courier",
			"session_name": "courier-gc-9",
		},
	}); err != nil {
		t.Fatalf("Create courier: %v", err)
	}

	t.Setenv("GC_AGENT", "recipient")

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "courier", "", "", "", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailSend(--from courier) = 0, want refusal; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "refusing --from courier") {
		t.Fatalf("stderr = %q, want forged-sender refusal", stderr.String())
	}
	if !strings.Contains(stderr.String(), "recipient") {
		t.Fatalf("stderr = %q, want ambient agent identity", stderr.String())
	}
	for _, msg := range mailAuthorityMessages(t, cityPath) {
		t.Fatalf("foreign explicit sender was delivered: From=%q", msg.From)
	}
}

func TestCmdMailSend_AllowsExplicitSenderOwnedByAmbientSession(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ambientKey  string
		ambient     func(beads.Bead) string
		explicit    func(beads.Bead) string
		wantDisplay string
	}{
		{
			name:       "explicit alias owned by ambient session id",
			ambientKey: "GC_SESSION_ID",
			ambient: func(sender beads.Bead) string {
				return sender.ID
			},
			explicit: func(beads.Bead) string {
				return "sender"
			},
			wantDisplay: "sender",
		},
		{
			name:       "explicit session id owned by ambient alias",
			ambientKey: "GC_ALIAS",
			ambient: func(beads.Bead) string {
				return "sender"
			},
			explicit: func(sender beads.Bead) string {
				return sender.ID
			},
			wantDisplay: "sender",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cityPath := newMailAuthorityCity(t)
			store, err := openCityStoreAt(cityPath)
			if err != nil {
				t.Fatalf("openCityStoreAt: %v", err)
			}
			sender, err := store.Create(beads.Bead{
				Type:   session.BeadType,
				Labels: []string{session.LabelSession},
				Metadata: map[string]string{
					"alias":        "sender",
					"session_name": "sender-gc-9",
				},
			})
			if err != nil {
				t.Fatalf("Create sender: %v", err)
			}
			t.Setenv(tc.ambientKey, tc.ambient(sender))

			var stdout, stderr bytes.Buffer
			code := cmdMailSend([]string{"recipient", "hello"}, false, false, tc.explicit(sender), "", "", "", &stdout, &stderr)
			if code != 0 {
				t.Fatalf("cmdMailSend() = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
			}

			msgs := mailAuthorityMessages(t, cityPath)
			if len(msgs) != 1 {
				t.Fatalf("message count = %d, want 1; msgs=%#v", len(msgs), msgs)
			}
			if msgs[0].From != tc.wantDisplay {
				t.Fatalf("message From = %q, want %q", msgs[0].From, tc.wantDisplay)
			}
		})
	}
}

func TestCmdMailReply_RejectsForeignExplicitSenderFromAgentSession(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	store, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	if _, err := store.Create(beads.Bead{
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":        "courier",
			"session_name": "courier-gc-9",
		},
	}); err != nil {
		t.Fatalf("Create courier: %v", err)
	}
	mp := beadmail.New(store)
	original, err := mp.Send("recipient", "courier", "Hello", "first")
	if err != nil {
		t.Fatalf("mp.Send(): %v", err)
	}

	t.Setenv("GC_AGENT", "recipient")

	var stdout, stderr bytes.Buffer
	code := cmdMailReplyFrom([]string{original.ID, "reply body"}, "courier", "", "", false, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailReplyFrom(--from courier) = 0, want refusal; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "refusing --from courier") {
		t.Fatalf("stderr = %q, want forged-sender refusal", stderr.String())
	}
	for _, m := range mailAuthorityMessages(t, cityPath) {
		if m.ID != original.ID {
			t.Fatalf("foreign explicit reply was delivered: From=%q", m.From)
		}
	}
}

// A process that demonstrably IS an agent may not claim the operator identity.
// Without this, the fix above is bypassed by adding one flag.
func TestCmdMailSend_RejectsExplicitHumanFromAgentSession(t *testing.T) {
	cityPath := newMailAuthorityCity(t)
	t.Setenv("GC_AGENT", "gascity/bob")

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "human", "", "", "", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailSend(--from human) = 0, want refusal from an agent session; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "gascity/bob") {
		t.Fatalf("stderr = %q, want the refusal to name the ambient agent identity", stderr.String())
	}

	for _, msg := range mailAuthorityMessages(t, cityPath) {
		t.Fatalf("agent-forged operator mail was delivered: From=%q", msg.From)
	}
}

// The sender is finally resolved through reservedMailSenderIdentity, which
// trims whitespace and a trailing slash. A guard keyed on plain equality with
// "human" would pass these variants through and still deliver them as human.
func TestCmdMailSend_RejectsOperatorClaimVariantsFromAgentSession(t *testing.T) {
	for _, from := range []string{"human/", " human ", "human"} {
		t.Run(from, func(t *testing.T) {
			cityPath := newMailAuthorityCity(t)
			t.Setenv("GC_AGENT", "gascity/bob")

			var stdout, stderr bytes.Buffer
			code := cmdMailSend([]string{"recipient", "hello"}, false, false, from, "", "", "", &stdout, &stderr)
			if code == 0 {
				t.Fatalf("cmdMailSend(--from %q) = 0, want refusal; stdout=%s stderr=%s", from, stdout.String(), stderr.String())
			}
			for _, msg := range mailAuthorityMessages(t, cityPath) {
				t.Fatalf("--from %q was delivered as From=%q", from, msg.From)
			}
		})
	}
}

func TestCmdMailSend_RejectsExplicitControllerFromAgentSession(t *testing.T) {
	cityPath := newMailAuthorityCity(t)
	t.Setenv("GC_AGENT", "gascity/bob")

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "controller", "", "", "", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailSend(--from controller) = 0, want refusal from an agent session; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "refusing --from controller") {
		t.Fatalf("stderr = %q, want forged-controller refusal", stderr.String())
	}
	for _, msg := range mailAuthorityMessages(t, cityPath) {
		t.Fatalf("agent-forged controller mail was delivered: From=%q", msg.From)
	}
}

func TestCmdMailSend_AllowsExplicitControllerWhenIdentityEnvAbsent(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "controller", "", "", "", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("cmdMailSend(--from controller) = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	msgs := mailAuthorityMessages(t, cityPath)
	if len(msgs) != 1 {
		t.Fatalf("message count = %d, want 1; msgs=%#v", len(msgs), msgs)
	}
	if msgs[0].From != "controller" {
		t.Fatalf("message From = %q, want controller", msgs[0].From)
	}
}

func TestCmdMailSend_RejectsExplicitConfiguredSenderWhenIdentityEnvAbsent(t *testing.T) {
	cityPath := newMailAuthorityCity(t)

	var stdout, stderr bytes.Buffer
	code := cmdMailSend([]string{"recipient", "hello"}, false, false, "recipient", "", "", "", &stdout, &stderr)
	if code == 0 {
		t.Fatalf("cmdMailSend(--from recipient) = 0, want refusal without ambient identity; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no sender identity") {
		t.Fatalf("stderr = %q, want no-sender-identity refusal", stderr.String())
	}
	for _, msg := range mailAuthorityMessages(t, cityPath) {
		t.Fatalf("agentless configured sender was delivered: From=%q", msg.From)
	}
}
