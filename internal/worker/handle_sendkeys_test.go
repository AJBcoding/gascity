package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestRuntimeHandleSendKeysDeliversRawKeys covers gas-bjms: an agent parked on a
// select widget (a Claude Code AskUserQuestion menu) can only be answered with
// bare key events. Message/Nudge always append Enter after literal text, which
// the widget consumes and discards, so the boundary needs a verb that delivers
// keys and nothing else.
func TestRuntimeHandleSendKeysDeliversRawKeys(t *testing.T) {
	sp := runtime.NewFake()
	if err := sp.Start(context.Background(), "menu-worker", runtime.Config{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	handle, err := NewRuntimeHandle(RuntimeHandleConfig{
		Provider:     sp,
		SessionName:  "menu-worker",
		ProviderName: "stub",
	})
	if err != nil {
		t.Fatalf("NewRuntimeHandle: %v", err)
	}

	if err := handle.SendKeys(context.Background(), KeysRequest{Keys: []string{"Down", "Space", "Enter"}}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	var got []string
	for _, call := range sp.SnapshotCalls() {
		if call.Method == "SendKeys" {
			got = append(got, call.Message)
		}
	}
	if len(got) != 1 {
		t.Fatalf("SendKeys provider calls = %#v, want exactly one", got)
	}
	if got[0] != "Down Space Enter" {
		t.Fatalf("delivered keys = %q, want %q", got[0], "Down Space Enter")
	}
}

// TestRuntimeHandleSendKeysRejectsEmptyKeys keeps the verb from issuing a no-op
// provider call that would report success without delivering anything — the
// false-success shape gas-bjms was filed about.
func TestRuntimeHandleSendKeysRejectsEmptyKeys(t *testing.T) {
	sp := runtime.NewFake()
	if err := sp.Start(context.Background(), "menu-worker", runtime.Config{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	handle, err := NewRuntimeHandle(RuntimeHandleConfig{
		Provider:     sp,
		SessionName:  "menu-worker",
		ProviderName: "stub",
	})
	if err != nil {
		t.Fatalf("NewRuntimeHandle: %v", err)
	}

	for _, keys := range [][]string{nil, {}, {"   "}, {"Enter", ""}} {
		if err := handle.SendKeys(context.Background(), KeysRequest{Keys: keys}); err == nil {
			t.Fatalf("SendKeys(%#v) succeeded, want an error", keys)
		}
	}
	for _, call := range sp.SnapshotCalls() {
		if call.Method == "SendKeys" {
			t.Fatalf("rejected request still reached the provider: %#v", call)
		}
	}
}

// TestRuntimeHandleSendKeysRequiresRunningSession guards the provider's
// best-effort contract: tmux SendKeys swallows "no such session" and returns
// nil, so the boundary must reject a dead target itself rather than report a
// delivery that never happened.
func TestRuntimeHandleSendKeysRequiresRunningSession(t *testing.T) {
	sp := runtime.NewFake()
	handle, err := NewRuntimeHandle(RuntimeHandleConfig{
		Provider:     sp,
		SessionName:  "stopped-worker",
		ProviderName: "stub",
	})
	if err != nil {
		t.Fatalf("NewRuntimeHandle: %v", err)
	}

	err = handle.SendKeys(context.Background(), KeysRequest{Keys: []string{"Escape"}})
	if err == nil {
		t.Fatal("SendKeys on a stopped session succeeded, want ErrSessionInactive")
	}
	if !errors.Is(err, sessionpkg.ErrSessionInactive) {
		t.Fatalf("SendKeys error = %v, want %v", err, sessionpkg.ErrSessionInactive)
	}
	if !strings.Contains(err.Error(), "stopped-worker") {
		t.Fatalf("SendKeys error = %v, want it to name the target session", err)
	}
}
