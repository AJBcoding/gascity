package pgauth

import "github.com/gastownhall/gascity/internal/events"

// PostgresCredentialResolvedPayload is this lane's name for the payload of
// events.BackendCredentialResolved.
//
// It used to be a distinct type registered against a postgres-specific event
// (events.PostgresCredentialResolved, "pg.credential_resolved"). The gas-to2q
// upstream merge generalized that event into a backend-agnostic
// "backend.credential_resolved", and upstream now owns BOTH the payload type
// and its registration in internal/events/backend_payloads.go.
//
// So this is an alias, not a second type. Registering a second payload for one
// event name would fork the wire contract between two assemblies of the same
// product — the exact failure upstream's typed-wire invariant exists to
// prevent, and it says so in that file. Callers set Backend to the backend the
// scope's metadata names.
type PostgresCredentialResolvedPayload = events.BackendCredentialResolvedPayload
