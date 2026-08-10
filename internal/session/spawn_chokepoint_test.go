package session

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spawnChokepointFunc is the only function in this package permitted to call
// the runtime provider's Start. Everything else routes through it.
const spawnChokepointFunc = "startRuntime"

// TestEveryProviderStartRoutesThroughTheSpawnChokepoint is the gate that keeps
// the spawn preflight unmissable.
//
// gas-wnq guarded session CREATION and deliberately left the wake/restart paths
// unguarded, so a session woken onto a full disk still hit the ENOSPC stall the
// original incident described: the agent parks at a prompt it cannot get past,
// which from outside is indistinguishable from a hang, while it holds a pool
// slot (gas-9nx). Adding the check at each of those sites fixes today and rots
// tomorrow — the next spawn path added silently skips it.
//
// This gate closes that by construction instead: m.sp.Start may be called from
// exactly one function, which applies the preflight. A new spawn site fails the
// build here, with the reason, rather than shipping an unguarded path.
//
// The shape originally proposed for this — a runtime.Provider decorator that
// preflights every Start — was rejected. A decorator embedding runtime.Provider
// promotes only that interface's methods, silently dropping the optional
// capability interfaces production code type-asserts off m.sp: IdleWaitProvider,
// InterruptBoundaryWaitProvider, InterruptedTurnResetProvider,
// ImmediateNudgeProvider, InteractionProvider, DialogProvider,
// ProcessTableScanner, and the package-private transportDetector and
// acpRouteRegistrar — sixteen assertion sites in this package alone.
// cmd/gc/status_provider.go already carries that scar: its Relaunch exists
// solely "so the reconciler's RelaunchProvider type-assert is not masked by the
// status wrapper". Trading one silent failure for a new class of silent
// capability loss is not a fix.
func TestEveryProviderStartRoutesThroughTheSpawnChokepoint(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "internal", "session")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	var offenders []string
	chokepointCalls := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if !isProviderStartCall(n) {
					return true
				}
				if fn.Name.Name == spawnChokepointFunc {
					chokepointCalls++
					return true
				}
				offenders = append(offenders, fmt.Sprintf("%s:%d (in %s)",
					name, fset.Position(n.Pos()).Line, fn.Name.Name))
				return true
			})
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("provider Start called outside %s(): %s\n\n"+
			"Every spawn must route through %s() so the disk preflight cannot be "+
			"skipped: a session started on a full disk stalls silently and holds a "+
			"pool slot (gas-wnq/gas-9nx). Call m.%s(ctx, sessName, cfg) instead.",
			spawnChokepointFunc, strings.Join(offenders, ", "),
			spawnChokepointFunc, spawnChokepointFunc)
	}
	// Guards the gate itself: if the chokepoint stopped calling Start, the scan
	// above would pass vacuously while nothing spawned through the preflight.
	if chokepointCalls == 0 {
		t.Fatalf("no provider Start call found in %s(); the chokepoint must be the "+
			"one place that spawns, so this gate has something to enforce",
			spawnChokepointFunc)
	}
}

// isProviderStartCall reports whether n is a `<recv>.sp.Start(...)` call, the
// shape every runtime spawn in this package takes.
func isProviderStartCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Start" {
		return false
	}
	provider, ok := sel.X.(*ast.SelectorExpr)
	return ok && provider.Sel.Name == "sp"
}
