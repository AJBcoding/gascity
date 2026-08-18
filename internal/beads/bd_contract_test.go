package beads_test

import (
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/fsys"
)

// TestStoreUsesBDContract pins the store-identity answer the bounded
// shipped-close compatibility mode is scoped on. It never runs bd: the question
// is what the store IS, so a store value with a runner that is never called is
// the whole fixture.
func TestStoreUsesBDContract(t *testing.T) {
	bd := beads.NewBdStore(t.TempDir(), func(string, string, ...string) ([]byte, error) {
		t.Fatal("StoreUsesBDContract must answer from store identity, never by running bd")
		return nil, nil
	})
	file, err := beads.OpenFileStore(fsys.OSFS{}, filepath.Join(t.TempDir(), "beads.json"))
	if err != nil {
		t.Fatalf("opening file store: %v", err)
	}

	for name, tc := range map[string]struct {
		store beads.Store
		want  bool
	}{
		"bd store":              {store: bd, want: true},
		"cache over bd":         {store: beads.NewCachingStoreForTest(bd, nil), want: true},
		"file store":            {store: file, want: false},
		"cache over file store": {store: beads.NewCachingStoreForTest(file, nil), want: false},
		"mem store":             {store: beads.NewMemStore(), want: false},
		"absent store":          {store: nil, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := beads.StoreUsesBDContract(tc.store); got != tc.want {
				t.Fatalf("StoreUsesBDContract(%s) = %v, want %v", name, got, tc.want)
			}
		})
	}
}

// TestStoreUsesBDContractIsStrictForUnknownWrappers pins the fail direction: a
// wrapper this package has never seen answers "native", which keeps it on the
// enforced side of every compatibility decision rather than silently relaxed.
func TestStoreUsesBDContractIsStrictForUnknownWrappers(t *testing.T) {
	opaque := opaqueWrapper{Store: beads.NewBdStore(t.TempDir(), nil)}
	if beads.StoreUsesBDContract(opaque) {
		t.Fatal("an unrecognized wrapper claimed the bd contract without forwarding it")
	}
}

// opaqueWrapper embeds the Store interface, which is exactly how a wrapper
// loses a non-Store method: the method set promoted is the interface's, not the
// concrete value's.
type opaqueWrapper struct{ beads.Store }
