package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// fenceCloseCity configures a bd-backed city served by a fake PATH bd script
// (the same seam silentFallbackTestSetup uses) and returns the capture file
// the script appends every WRITE invocation to. The store reads (bd show
// --json), the four-verb capability probe (bd <verb> --help), and the
// passthrough write all flow through the one script, so a test observes
// exactly what the subprocess seam was handed.
func fenceCloseCity(t *testing.T, script string, warnOnly bool) string {
	t.Helper()

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	t.Cleanup(func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	})
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	port := strconv.Itoa(writeReachableManagedDoltState(t, cityDir))

	cityToml := "[workspace]\nname = \"demo\"\n"
	if warnOnly {
		cityToml += "\n[beads]\nshipped_close_warn_only = true\n"
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityToml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := "issue_prefix: demo\n" +
		"gc.endpoint_origin: city_canonical\n" +
		"gc.endpoint_status: verified\n" +
		"dolt.auto-start: false\n" +
		"dolt.host: 127.0.0.1\n" +
		"dolt.port: " + port + "\n"
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, ".beads", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "bd-writes.txt")
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_DOLT_PORT", port)
	return capture
}

// fenceWriteLines returns the "write:" lines the fake bd captured, in order.
// A missing capture file means no write invocation ever reached bd.
func fenceWriteLines(t *testing.T, capture string) []string {
	t.Helper()
	data, err := os.ReadFile(capture)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var writes []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "write: ") {
			writes = append(writes, line)
		}
	}
	return writes
}

// fenceNativeReader is the capability shape the production factory returns
// for an eligible NativeDoltStore: it is authoritative for reads but does not
// implement ConditionalWriter. Embedding only beads.Store deliberately hides
// the MemStore's optional conditional-write methods.
type fenceNativeReader struct {
	beads.Store
	row beads.Bead
}

func (s fenceNativeReader) Get(id string) (beads.Bead, error) {
	if id != s.row.ID {
		return beads.Bead{}, beads.ErrNotFound
	}
	return s.row, nil
}

// nativeFenceReaderFromFactory runs the real beads factory's eligible-native
// branch with an injected native-shaped reader. This exercises factory
// selection without opening a live Dolt server; the diagnostic proves the
// returned read authority is the NativeDoltStore branch rather than BdStore.
func nativeFenceReaderFromFactory(t *testing.T, row beads.Bead) beads.Store {
	t.Helper()
	t.Setenv("GC_BEADS_FORCE_FALLBACK", "")
	scope := t.TempDir()
	files := fsys.NewFake()
	files.Files[filepath.Join(scope, ".beads", "metadata.json")] = []byte(`{
  "backend": "dolt",
  "dolt_mode": "server",
  "dolt_database": "gascity",
  "project_id": "gc-local"
}`)
	reader := fenceNativeReader{Store: beads.NewMemStore(), row: row}
	result, err := beads.OpenStoreAtForCity(context.Background(), beads.StoreOpenOptions{
		ScopeRoot: scope,
		Provider:  "bd",
		PreflightChecker: contract.PreflightChecker{
			FS:                  files,
			Provider:            "bd",
			BeadsLibraryVersion: "1.1.0",
			BDContext: func(string) (contract.PreflightBDContext, error) {
				return contract.PreflightBDContext{Backend: "dolt", DoltMode: "server", BDVersion: "1.1.0", SchemaVersion: 1}, nil
			},
			DatabaseProjectID: func(string) (string, bool, error) {
				return "gc-local", true, nil
			},
		},
		OpenBdStore: func() (beads.Store, error) {
			t.Fatal("factory selected BdStore for an eligible native-reader fixture")
			return nil, nil
		},
		OpenNativeStore: func() (beads.Store, error) { return reader, nil },
	})
	if err != nil {
		t.Fatalf("OpenStoreAtForCity(native reader): %v", err)
	}
	if result.Diagnostic.Store != beads.BeadsStoreNameNativeDoltStore || !result.Diagnostic.NativeStoreEligible {
		t.Fatalf("factory diagnostic = %+v, want eligible %s", result.Diagnostic, beads.BeadsStoreNameNativeDoltStore)
	}
	return wrapStoreWithBeadPolicies(result.Store, &config.City{})
}

// fenceCapableBdScript serves the gated work row at revision 7, advertises
// --if-revision on every probed verb, and accepts whatever write it is
// handed, recording the exact argv.
const fenceCapableBdScript = `#!/bin/sh
set -eu
if [ "${1:-}" = "show" ]; then
  printf '[{"id":"%s","title":"work row","status":"open","issue_type":"task","metadata":{"gc.work_outcome":"no-op"},"revision":7}]\n' "${3:-demo-abc}"
  exit 0
fi
if [ "${2:-}" = "--help" ]; then
  printf 'Flags:\n      --if-revision int   refuse unless revision matches\n'
  exit 0
fi
echo "write: $*" >> "${CAPTURE_PATH}"
exit 0
`

// fenceContestedBdScript models the review's race: policy evaluated the row
// at revision 7, and by write time a concurrent writer moved it to 9. A
// fenced write is refused with bd's precondition envelope; an UNFENCED write
// commits silently — the pre-fix behavior the regression pins.
const fenceContestedBdScript = `#!/bin/sh
set -eu
if [ "${1:-}" = "show" ]; then
  printf '[{"id":"%s","title":"work row","status":"open","issue_type":"task","metadata":{"gc.work_outcome":"no-op"},"revision":7}]\n' "${3:-demo-abc}"
  exit 0
fi
if [ "${2:-}" = "--help" ]; then
  printf 'Flags:\n      --if-revision int   refuse unless revision matches\n'
  exit 0
fi
echo "write: $*" >> "${CAPTURE_PATH}"
fence=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--if-revision" ]; then fence="$a"; fi
  prev="$a"
done
if [ -z "$fence" ]; then
  echo "UNFENCED-COMMIT" >> "${CAPTURE_PATH}"
  exit 0
fi
if [ "$fence" != "9" ]; then
  printf '{"error":"precondition failed","code":"precondition-failed","expected_revision":%s,"current_revision":9}\n' "$fence"
  exit 1
fi
exit 0
`

// fenceIncapableBdScript is a pre-fence bd: no --if-revision anywhere in its
// help output, and every write it receives commits unconditionally.
const fenceIncapableBdScript = `#!/bin/sh
set -eu
if [ "${1:-}" = "show" ]; then
  printf '[{"id":"%s","title":"work row","status":"open","issue_type":"task","metadata":{"gc.work_outcome":"no-op"},"revision":7}]\n' "${3:-demo-abc}"
  exit 0
fi
if [ "${2:-}" = "--help" ]; then
  printf 'Flags:\n      --json   machine output\n'
  exit 0
fi
echo "write: $*" >> "${CAPTURE_PATH}"
echo "UNFENCED-COMMIT" >> "${CAPTURE_PATH}"
exit 0
`

// fenceControlBeadBdScript is the incapable bd again, but the row is a
// control bead (gc.kind) outside the work-record contract — the population
// whose closes must stay byte-identical whatever bd is installed.
const fenceControlBeadBdScript = `#!/bin/sh
set -eu
if [ "${1:-}" = "show" ]; then
  printf '[{"id":"%s","title":"control row","status":"open","issue_type":"task","metadata":{"gc.kind":"run"},"revision":7}]\n' "${3:-demo-abc}"
  exit 0
fi
if [ "${2:-}" = "--help" ]; then
  printf 'Flags:\n      --json   machine output\n'
  exit 0
fi
echo "write: $*" >> "${CAPTURE_PATH}"
exit 0
`

// TestGcBdPassthroughFencesGatedWorkRecordClose pins the join between the
// close gate's policy read and the subprocess mutation: the exact revision
// the gate evaluated (7) rides the exec'd argv as --if-revision, exactly
// once, for both close spellings the gate recognizes.
func TestGcBdPassthroughFencesGatedWorkRecordClose(t *testing.T) {
	capture := fenceCloseCity(t, fenceCapableBdScript, false)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if got := doBd([]string{"update", "demo-abc", "--set-metadata", "gc.work_outcome=no-op", "--status", "closed"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(update --status closed) = %d, want 0; stderr=%q", got, stderr.String())
	}

	writes := fenceWriteLines(t, capture)
	if len(writes) != 2 {
		t.Fatalf("bd write invocations = %d (%q), want 2", len(writes), writes)
	}
	if writes[0] != "write: close demo-abc --if-revision 7" {
		t.Fatalf("close argv = %q, want the evaluated-revision fence appended", writes[0])
	}
	if want := "write: update demo-abc --set-metadata gc.work_outcome=no-op --status closed --if-revision 7"; writes[1] != want {
		t.Fatalf("update argv = %q, want %q", writes[1], want)
	}
	for _, line := range writes {
		if strings.Count(line, "--if-revision") != 1 {
			t.Fatalf("argv %q carries the fence more than once", line)
		}
	}
}

// TestGcBdPassthroughFencesNativeFactoryReaderWithSelectedBdTransport catches
// the production mutation that asks the authoritative reader (NativeDoltStore)
// whether the separately exec'd bd binary supports --if-revision. The reader
// intentionally has no ConditionalWriter; capability belongs to the exact bd
// write transport. Its advertised support must admit the close, carrying the
// native row's literal revision 73 to that selected binary exactly once.
func TestGcBdPassthroughFencesNativeFactoryReaderWithSelectedBdTransport(t *testing.T) {
	capture := fenceCloseCity(t, fenceCapableBdScript, false)
	reader := nativeFenceReaderFromFactory(t, beads.Bead{
		ID: "demo-native", Title: "native-read work row", Type: "task", Status: "open", Revision: 73,
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	})

	originalOpen := openBdMutationReadStore
	openBdMutationReadStore = func(_, _ string, _ *config.City) (beads.Store, error) { return reader, nil }
	t.Cleanup(func() { openBdMutationReadStore = originalOpen })

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-native"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(native-reader close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	writes := fenceWriteLines(t, capture)
	if len(writes) != 1 || writes[0] != "write: close demo-native --if-revision 73" {
		t.Fatalf("selected bd writes = %q, want native revision fenced on exact argv", writes)
	}
}

// TestGcBdPassthroughFencedCloseRefusedWhenRowMovesAfterEvaluation is the
// mutation-sensitive regression for the gas-dq28 policy/write race: the row
// changes AFTER the gate evaluated it and BEFORE the bd write, and the close
// must not land. The fence gives bd the evaluated revision, bd refuses, and
// the operator sees a non-zero exit instead of a close validated against a
// row that no longer exists.
func TestGcBdPassthroughFencedCloseRefusedWhenRowMovesAfterEvaluation(t *testing.T) {
	capture := fenceCloseCity(t, fenceContestedBdScript, false)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-abc"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd(close) = 0 after the row moved mid-close; stdout=%q capture=%q", stdout.String(), fenceWriteLines(t, capture))
	}

	writes := fenceWriteLines(t, capture)
	if len(writes) != 1 || !strings.Contains(writes[0], "--if-revision 7") {
		t.Fatalf("bd writes = %q, want one invocation fenced on the evaluated revision 7", writes)
	}
	data, _ := os.ReadFile(capture)
	if strings.Contains(string(data), "UNFENCED-COMMIT") {
		t.Fatalf("an unfenced close committed after the row moved: %q", string(data))
	}
}

// TestGcBdPassthroughRefusesGatedCloseWhenBdCannotFence pins the fail-closed
// rule under enforcement: when the pinned bd cannot honor --if-revision (too
// old, probe failure, runtime latch), the gated close is refused with an
// actionable message BEFORE any subprocess write — never executed unfenced.
func TestGcBdPassthroughRefusesGatedCloseWhenBdCannotFence(t *testing.T) {
	capture := fenceCloseCity(t, fenceIncapableBdScript, false)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-abc"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd(close) = 0 through a bd that cannot fence; capture=%q", fenceWriteLines(t, capture))
	}
	if writes := fenceWriteLines(t, capture); len(writes) != 0 {
		t.Fatalf("refused close still reached bd: %q", writes)
	}
	if !strings.Contains(stderr.String(), "--if-revision") {
		t.Fatalf("stderr = %q, want the missing fence capability named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "shipped_close_warn_only") {
		t.Fatalf("stderr = %q, want the compatibility remedy named", stderr.String())
	}
}

// TestGcBdPassthroughLeavesNonWorkRecordCloseUnfenced pins the blast radius:
// a close outside the work-record contract (control beads and every other
// exempt population) is byte-identical to today's passthrough — no fence, no
// capability probe requirement — even on a bd that cannot fence at all.
func TestGcBdPassthroughLeavesNonWorkRecordCloseUnfenced(t *testing.T) {
	capture := fenceCloseCity(t, fenceControlBeadBdScript, false)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-abc", "-r", "duplicate"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(control close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	writes := fenceWriteLines(t, capture)
	if len(writes) != 1 || writes[0] != "write: close demo-abc -r duplicate" {
		t.Fatalf("bd writes = %q, want the untouched original argv", writes)
	}
}

// TestGcBdPassthroughWarnOnlyProceedsUnfencedWhenBdCannotFence pins the
// bounded compatibility escape: under beads.shipped_close_warn_only the
// gated close on an unfenceable bd proceeds exactly as before the fence
// existed — unfenced — but says so, because a silent unfenced write is the
// shape this whole seam exists to remove.
func TestGcBdPassthroughWarnOnlyProceedsUnfencedWhenBdCannotFence(t *testing.T) {
	capture := fenceCloseCity(t, fenceIncapableBdScript, true)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"close", "demo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(warn-only close) = %d, want 0; stderr=%q", got, stderr.String())
	}
	writes := fenceWriteLines(t, capture)
	if len(writes) != 1 || writes[0] != "write: close demo-abc" {
		t.Fatalf("bd writes = %q, want the original unfenced argv", writes)
	}
	if !strings.Contains(stderr.String(), "without a revision fence") {
		t.Fatalf("stderr = %q, want the unfenced-close warning", stderr.String())
	}
}

// fenceUnitStores builds the store shapes the unit tests inject: a bd store
// whose fake runner advertises the fence, one that does not, and one that
// must never be probed at all.
func fenceCapableUnitStore() beads.Store {
	return beads.NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		return []byte("Flags:\n      --if-revision int\n"), nil
	})
}

func fenceIncapableUnitStore() beads.Store {
	return beads.NewBdStore("/city", func(_, _ string, _ ...string) ([]byte, error) {
		return []byte("Flags:\n      --json\n"), nil
	})
}

func fenceGatedRow(id string, revision int64) beads.Bead {
	return beads.Bead{
		ID: id, Type: "task", Status: "open", Revision: revision,
		Metadata: beads.StringMap{beadmeta.WorkOutcomeMetadataKey: beadmeta.WorkOutcomeNoOp},
	}
}

func TestApplyWorkRecordCloseFenceAppendsEvaluatedRevision(t *testing.T) {
	evaluated := map[string]beads.Bead{"gc-1": fenceGatedRow("gc-1", 41)}
	var stderr strings.Builder
	args, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1"}, fenceCapableUnitStore(), evaluated, true, &stderr)
	if blocked {
		t.Fatalf("fence blocked a capable close: %s", stderr.String())
	}
	want := []string{"close", "gc-1", beads.ConditionalWriteFenceFlag, "41"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %q, want %q", args, want)
	}
}

// TestApplyWorkRecordCloseFenceFencesThroughPolicyWrapper proves the fence
// probes THROUGH the cmd/gc bead-policy wrapper doBd's store factory applies:
// interface embedding hides the bd store's capability, and without the
// resolve-target walk every gated close would read as unfenceable.
func TestApplyWorkRecordCloseFenceFencesThroughPolicyWrapper(t *testing.T) {
	wrapped := wrapStoreWithBeadPolicies(fenceCapableUnitStore(), &config.City{})
	evaluated := map[string]beads.Bead{"gc-1": fenceGatedRow("gc-1", 7)}
	var stderr strings.Builder
	args, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1"}, wrapped, evaluated, true, &stderr)
	if blocked || strings.Count(strings.Join(args, " "), beads.ConditionalWriteFenceFlag) != 1 {
		t.Fatalf("policy-wrapped store not fenced: blocked=%v argv=%q stderr=%s", blocked, args, stderr.String())
	}
}

func TestApplyWorkRecordCloseFenceLeavesNonCloseAndNonGatedUntouched(t *testing.T) {
	// A non-close invocation must not even consult the evaluated rows.
	var stderr strings.Builder
	original := []string{"update", "gc-1", "--set-metadata", "k=v"}
	args, blocked := applyWorkRecordCloseFence(original, nil, nil, true, &stderr)
	if blocked || strings.Join(args, " ") != strings.Join(original, " ") {
		t.Fatalf("non-close invocation touched: blocked=%v argv=%q", blocked, args)
	}

	// A close of a control bead must not probe capability at all: the store's
	// runner fails the test if the fence path shells out for an exempt close.
	panicking := beads.NewBdStore("/city", func(_, _ string, args ...string) ([]byte, error) {
		t.Fatalf("capability probe ran for a non-gated close: bd %v", args)
		return nil, nil
	})
	control := beads.Bead{
		ID: "gc-ctl", Type: "task", Status: "open", Revision: 3,
		Metadata: beads.StringMap{beadmeta.KindMetadataKey: "run"},
	}
	args, blocked = applyWorkRecordCloseFence([]string{"close", "gc-ctl"}, panicking, map[string]beads.Bead{"gc-ctl": control}, true, &stderr)
	if blocked || strings.Join(args, " ") != "close gc-ctl" {
		t.Fatalf("control-bead close touched: blocked=%v argv=%q", blocked, args)
	}
}

func TestApplyWorkRecordCloseFenceRefusesBatchedGatedCloseUnderEnforcement(t *testing.T) {
	evaluated := map[string]beads.Bead{
		"gc-1": fenceGatedRow("gc-1", 5),
		"gc-2": fenceGatedRow("gc-2", 9),
	}
	var stderr strings.Builder
	_, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1", "gc-2"}, fenceCapableUnitStore(), evaluated, true, &stderr)
	if !blocked {
		t.Fatalf("batched gated close not refused; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "own invocation") {
		t.Fatalf("stderr = %q, want the split-the-batch remedy", stderr.String())
	}

	stderr.Reset()
	args, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1", "gc-2"}, fenceCapableUnitStore(), evaluated, false, &stderr)
	if blocked || strings.Contains(strings.Join(args, " "), beads.ConditionalWriteFenceFlag) {
		t.Fatalf("warn-only batch = blocked=%v argv=%q, want unfenced proceed", blocked, args)
	}
	if !strings.Contains(stderr.String(), "without a revision fence") {
		t.Fatalf("stderr = %q, want unfenced warning", stderr.String())
	}
}

func TestApplyWorkRecordCloseFenceHonorsMatchingOperatorFence(t *testing.T) {
	evaluated := map[string]beads.Bead{"gc-1": fenceGatedRow("gc-1", 41)}
	var stderr strings.Builder
	original := []string{"close", "gc-1", beads.ConditionalWriteFenceFlag, "41"}
	args, blocked := applyWorkRecordCloseFence(original, fenceCapableUnitStore(), evaluated, true, &stderr)
	if blocked || strings.Join(args, " ") != strings.Join(original, " ") {
		t.Fatalf("matching operator fence not honored: blocked=%v argv=%q", blocked, args)
	}
	if strings.Count(strings.Join(args, " "), beads.ConditionalWriteFenceFlag) != 1 {
		t.Fatalf("operator fence double-appended: %q", args)
	}
}

func TestApplyWorkRecordCloseFenceRefusesMismatchedOperatorFenceUnderEnforcement(t *testing.T) {
	evaluated := map[string]beads.Bead{"gc-1": fenceGatedRow("gc-1", 41)}
	var stderr strings.Builder
	_, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1", beads.ConditionalWriteFenceFlag + "=40"}, fenceCapableUnitStore(), evaluated, true, &stderr)
	if !blocked {
		t.Fatalf("stale operator fence not refused; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "41") {
		t.Fatalf("stderr = %q, want the validated revision named", stderr.String())
	}
}

func TestApplyWorkRecordCloseFenceFailsClosedOnUnreadableEvaluationRow(t *testing.T) {
	var stderr strings.Builder
	_, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1"}, fenceCapableUnitStore(), nil, true, &stderr)
	if !blocked {
		t.Fatalf("close with no evaluated row not refused under enforcement; stderr=%s", stderr.String())
	}

	stderr.Reset()
	args, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1"}, fenceCapableUnitStore(), nil, false, &stderr)
	if blocked || strings.Contains(strings.Join(args, " "), beads.ConditionalWriteFenceFlag) {
		t.Fatalf("warn-only unreadable row = blocked=%v argv=%q, want unfenced proceed", blocked, args)
	}
	if !strings.Contains(stderr.String(), "without a revision fence") {
		t.Fatalf("stderr = %q, want unfenced warning", stderr.String())
	}
}

func TestApplyWorkRecordCloseFenceRefusesWhenBdCannotFenceUnderEnforcement(t *testing.T) {
	evaluated := map[string]beads.Bead{"gc-1": fenceGatedRow("gc-1", 41)}
	var stderr strings.Builder
	_, blocked := applyWorkRecordCloseFence([]string{"close", "gc-1"}, fenceIncapableUnitStore(), evaluated, true, &stderr)
	if !blocked {
		t.Fatalf("unfenceable close not refused; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), beads.ConditionalWriteFenceFlag) || !strings.Contains(stderr.String(), "shipped_close_warn_only") {
		t.Fatalf("stderr = %q, want capability and remedy named", stderr.String())
	}
}
