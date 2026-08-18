package beads

// Store identity for the one policy question that turns on it: is this store
// served through the bd CLI contract, or is it a natively fence-capable store?
//
// This is deliberately NOT the conditional-write capability probe. That probe
// asks the live bd whether it parses --if-revision and costs four subprocesses;
// this asks what the store IS, costs an interface assertion, and is the
// question a close-policy decision can afford on every mutation.

// BDContractStore is the optional capability a store implements when it is — or
// wraps — the bd-CLI-backed store. *BdStore answers for itself, so any type
// that embeds it (DoltliteReadStore) carries the answer without restating it;
// wrappers forward to what they wrap.
type BDContractStore interface {
	UsesBDContract() bool
}

var (
	_ BDContractStore = (*BdStore)(nil)
	_ BDContractStore = (*CachingStore)(nil)
)

// StoreUsesBDContract reports whether store is served through the bd CLI
// contract. A store that does not answer is reported as native: an
// unrecognized wrapper must fall to the STRICT side of every compatibility
// decision, never to the relaxed one.
func StoreUsesBDContract(store Store) bool {
	carrier, ok := store.(BDContractStore)
	return ok && carrier.UsesBDContract()
}

// UsesBDContract reports the bd contract for the store that is the bd contract.
func (s *BdStore) UsesBDContract() bool { return s != nil }

// UsesBDContract forwards to the backing store: a cache in front of bd is still
// bd, and a cache in front of a native store is still native.
func (c *CachingStore) UsesBDContract() bool {
	if c == nil {
		return false
	}
	return StoreUsesBDContract(c.Backing())
}
