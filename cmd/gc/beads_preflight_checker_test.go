package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads/contract"
)

func TestNewBeadsPreflightCheckerEnforcesStorageSchemaContract(t *testing.T) {
	checker := newBeadsPreflightChecker(t.TempDir(), "bd")

	if got := checker.LinkedBeadsStorageSchemaVersion; got != contract.LinkedBeadsStorageSchemaVersion {
		t.Fatalf("LinkedBeadsStorageSchemaVersion = %d, want %d", got, contract.LinkedBeadsStorageSchemaVersion)
	}
	if got := checker.MaxExternalToolchainStorageSchemaVersion; got != contract.MaxExternalToolchainStorageSchemaVersion {
		t.Fatalf("MaxExternalToolchainStorageSchemaVersion = %d, want %d", got, contract.MaxExternalToolchainStorageSchemaVersion)
	}
}
