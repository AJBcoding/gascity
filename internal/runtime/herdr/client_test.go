package herdr

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// socketPath must resolve the SAME directory herdr itself uses for its
// config/socket state, or this client dials a path no herdr server ever
// binds: serverAlive() then always reads false against a healthy server
// ("did not become ready"), and every retry launches a redundant herdr
// server contending for the same pane ("agent_pane_busy") — ga-nqlb8q.
// Verified empirically against the herdr binary itself: `XDG_CONFIG_HOME=X
// herdr --help` prints "Config: X/herdr/config.toml" regardless of $HOME,
// and with XDG_CONFIG_HOME unset it falls back to "$HOME/.config/herdr/…" —
// standard XDG Base Directory precedence. When that root would make Herdr's
// API or client Unix socket exceed the platform limit, gc must give the Herdr
// subprocesses a bounded XDG root and dial the same bounded location.

func TestSocketPathHonorsXDGConfigHomeOverHome(t *testing.T) {
	xdg := shortConfigHome(t)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", t.TempDir()) // deliberately different; must be ignored

	c := newClient("xdgtest", "")
	if got, want := c.socketPath(), filepath.Join(xdg, "herdr", "sessions", "xdgtest", "herdr.sock"); got != want {
		t.Errorf("socketPath() = %q; want %q", got, want)
	}

	c.session = "default"
	if got, want := c.socketPath(), filepath.Join(xdg, "herdr", "herdr.sock"); got != want {
		t.Errorf("socketPath() (default session) = %q; want %q", got, want)
	}
}

func TestSocketPathFallsBackToHomeConfigWhenXDGUnset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := shortConfigHome(t)
	t.Setenv("HOME", home)

	c := newClient("hometest", "")
	if got, want := c.socketPath(), filepath.Join(home, ".config", "herdr", "sessions", "hometest", "herdr.sock"); got != want {
		t.Errorf("socketPath() = %q; want %q", got, want)
	}
}

func shortConfigHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hdr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestSocketPathUsesBoundedConfigWhenHomeWouldExceedUnixSocketLimit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := filepath.Join(t.TempDir(), strings.Repeat("long-home-segment", 8))
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	c := newClient("gctest-live-12345", "")
	got := c.socketPath()
	if strings.HasPrefix(got, home) {
		t.Fatalf("socketPath() = %q; want path independent of long HOME %q", got, home)
	}
	clientSock := filepath.Join(filepath.Dir(got), "herdr-client.sock")
	if len(clientSock) > len(syscall.RawSockaddrUnix{}.Path)-1 {
		t.Fatalf("client socket path length = %d; want <= %d: %s", len(clientSock), len(syscall.RawSockaddrUnix{}.Path)-1, clientSock)
	}
}

func TestRunPassesBoundedConfigHomeToHerdrCLI(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := filepath.Join(t.TempDir(), strings.Repeat("long-home-segment", 8))
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '{\"result\":{\"xdg_config_home\":\"%s\"}}\\n' \"$XDG_CONFIG_HOME\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := newClient("gctest-live-12345", "")
	c.bin = bin
	result, err := c.run(context.Background(), "agent", "list")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		XDGConfigHome string `json:"xdg_config_home"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	if got.XDGConfigHome == "" {
		t.Fatal("herdr CLI saw empty XDG_CONFIG_HOME; want bounded config home")
	}
	if strings.HasPrefix(got.XDGConfigHome, home) {
		t.Fatalf("herdr CLI XDG_CONFIG_HOME = %q; want independent of long HOME %q", got.XDGConfigHome, home)
	}
}

func TestStartServerReportsStartupStderr(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'local socket name length exceeds capacity of sun_path of sockaddr_un' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	c := newClient("diagnostic", "")
	c.bin = bin
	err := c.startServer()
	if err == nil {
		t.Fatal("startServer() = nil; want startup error")
	}
	if !strings.Contains(err.Error(), "local socket name length exceeds capacity") {
		t.Fatalf("startServer() error = %q; want startup stderr", err)
	}
}
