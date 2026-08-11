package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// errDegradedSessionListing reproduces the exact wrapped chain a slow
// ephemeral (wisp) tier produces against the external MySQL beads endpoint.
// Captured verbatim from a live reproduction while investigating gas-5nr0.
var errDegradedSessionListing = errors.New(
	"bd list both tiers: bd query: bd query (wisps): mysql database anthony_beads: " +
		"gc does not manage external MySQL endpoints (no managed recovery attempted): timed out after 30s")

// sessionListingFailureStore wraps a working store and fails only List, which
// is how a degraded wisp tier presents: every other store operation keeps
// working while session enumeration cannot answer at all. Every live session
// mailbox lives in the ephemeral tier, so this leaves the enumeration empty
// rather than merely incomplete.
type sessionListingFailureStore struct {
	beads.Store
	err error
}

func (s *sessionListingFailureStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, s.err
}

// writeNamedSessionCity builds a minimal city whose only mailbox target is a
// configured named session, so recipient resolution has a store-free source to
// fall back to when session enumeration fails.
func writeNamedSessionCity(t *testing.T) string {
	t.Helper()
	cityPath := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(cityPath, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", rel, err)
		}
	}
	write("city.toml", `[workspace]
name = "test-city"

[[agent]]
name = "mayor"
provider = "missing-provider"

[providers.missing-provider]
command = "missing-provider"

[[named_session]]
template = "mayor"
scope = "city"
mode = "always"
`)
	return cityPath
}

// TestMailRecipientResolvesFromConfigWhenSessionListingFails is the gas-5nr0
// regression. Mail is the escalation channel, and every live session mailbox
// lives in the ephemeral tier, so a slow wisp query leaves session enumeration
// unable to answer at all. Resolution must treat that as "this source could
// not answer" and keep walking to the store-free city-config lookup, not
// report the recipient as unresolvable and strand the escalation.
func TestMailRecipientResolvesFromConfigWhenSessionListingFails(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_MAIL", "")
	t.Setenv("GC_ALIAS", "")
	t.Setenv("GC_SESSION_ID", "")
	t.Setenv("GC_AGENT", "")

	cityPath := writeNamedSessionCity(t)
	t.Setenv("GC_CITY", cityPath)

	base, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	store := &sessionListingFailureStore{Store: base, err: errDegradedSessionListing}

	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		t.Fatalf("loadCityConfig: %v", err)
	}

	got, err := resolveMailRecipientIdentityCached(cityPath, cfg, store, "mayor/", &mailIdentitySessionCache{})
	if err != nil {
		t.Fatalf("resolveMailRecipientIdentityCached(mayor/) with degraded session listing = error %v; want the city-config address", err)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("resolveMailRecipientIdentityCached(mayor/) returned an empty address; escalation mail would be undeliverable")
	}
}

// TestMailRecipientStillFailsWhenNoSourceCanResolve keeps the other half of the
// gas-5nr0 contract honest: degrading past an unavailable source must never
// manufacture an address. A recipient no source can resolve is still an error,
// so the command exits non-zero rather than reporting a delivery it did not
// make.
func TestMailRecipientStillFailsWhenNoSourceCanResolve(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_MAIL", "")
	t.Setenv("GC_ALIAS", "")
	t.Setenv("GC_SESSION_ID", "")
	t.Setenv("GC_AGENT", "")

	cityPath := writeNamedSessionCity(t)
	t.Setenv("GC_CITY", cityPath)

	base, err := openCityStoreAt(cityPath)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	store := &sessionListingFailureStore{Store: base, err: errDegradedSessionListing}

	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		t.Fatalf("loadCityConfig: %v", err)
	}

	if got, err := resolveMailRecipientIdentityCached(cityPath, cfg, store, "no-such-agent", &mailIdentitySessionCache{}); err == nil {
		t.Fatalf("resolveMailRecipientIdentityCached(no-such-agent) = %q, nil; want an error naming the unresolvable recipient", got)
	}
}
