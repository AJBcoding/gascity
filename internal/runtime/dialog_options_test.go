package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

// capturedClaudeTrustInverted is the workspace-trust dialog exactly as Claude
// Code 2.1.261 rendered it on 2026-09-04, captured through herdr from a real
// pane in a directory the agent had never been trusted for.
//
// The decline option is FIRST and PRE-SELECTED, and there are no ordinals. A
// bare Enter here quits the agent. That is gas-193q.
const capturedClaudeTrustInverted = ` Quick safety check: Is this a project you created or
 one you trust? (Like your own code, a well-known
 open source project, or work from your team). If
 not, take a moment to review what's in this folder
 first.

 Claude Code'll be able to read, edit, and execute
 files here.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel`

// capturedClaudeTrustPreApproval is the same dialog for a work_dir carrying
// .claude/settings.local.json. Captured in the same run as the control above.
// The warning paragraph is the ONLY difference: the option block is identical,
// which is what refutes the original diagnosis that pre-approval inverts the
// order.
const capturedClaudeTrustPreApproval = ` open source project, or work from your team). If
 not, take a moment to review what's in this folder
 first.

 Claude Code'll be able to read, edit, and execute
 files here.

 ⚠ This folder pre-approves 9 tool permissions in
 .claude/settings.local.json:
   Bash(python3 -), Bash(python3 -m pytest -q -k
 "byte_for_byte or no_op"), Bash(git status:*),
 Bash(git diff:*), Bash(ls:*), Bash(cat:*),
 Bash(grep:*), Bash(rg:*), and 1 more
 These will apply without asking. Only proceed if you
 trust this configuration.

 Security guide

 ❯ No, exit
   Yes, I trust this folder

 Enter to confirm · Esc to cancel`

// capturedCodexTrust is codex 0.153.2's trust prompt from the same run. Accept
// is first, pre-selected, and numbered — the opposite layout to Claude's, at
// the same moment, which is why no fixed key sequence can be correct for both.
const capturedCodexTrust = `>─You are in /private/var/folders/nz/lvjgdpvx1g9420rrw

  Do you trust the contents of this directory? Working
  with untrusted contents comes with higher risk of
  prompt injection. Trusting the directory allows
  project-local config, hooks, and exec policies to
  load.

› 1. Yes, continue
  2. No, quit

  Press enter to continue`

func TestParseDialogOptionBlocksReadsRealCaptures(t *testing.T) {
	tests := []struct {
		name   string
		screen string
		want   []dialogOption
	}{
		{
			name:   "claude trust, decline first and selected",
			screen: capturedClaudeTrustInverted,
			want: []dialogOption{
				{Label: "No, exit", Selected: true},
				{Label: "Yes, I trust this folder"},
			},
		},
		{
			name:   "claude trust with pre-approval warning",
			screen: capturedClaudeTrustPreApproval,
			want: []dialogOption{
				{Label: "No, exit", Selected: true},
				{Label: "Yes, I trust this folder"},
			},
		},
		{
			name:   "codex trust, numbered, accept first",
			screen: capturedCodexTrust,
			want: []dialogOption{
				{Label: "Yes, continue", Ordinal: 1, Selected: true},
				{Label: "No, quit", Ordinal: 2},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := parseDialogOptionBlocks(tc.screen)
			if len(blocks) != 1 {
				t.Fatalf("parseDialogOptionBlocks() found %d menus, want exactly 1: %+v", len(blocks), blocks)
			}
			got := blocks[0]
			if len(got) != len(tc.want) {
				t.Fatalf("options = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("option %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseDialogOptionBlocksIgnoresProseAndBanners guards the two ways this
// parser could eat something that is not a menu: codex's ">─You are in ..."
// banner (a cursor glyph not followed by a space) and the prose above the
// options (text starting left of the option column).
func TestParseDialogOptionBlocksIgnoresProseAndBanners(t *testing.T) {
	blocks := parseDialogOptionBlocks(capturedCodexTrust)
	if len(blocks) != 1 {
		t.Fatalf("found %d menus, want 1 (the banner must not read as one): %+v", len(blocks), blocks)
	}
	for _, opt := range blocks[0] {
		if strings.Contains(opt.Label, "You are in") || strings.Contains(opt.Label, "prompt injection") {
			t.Fatalf("prose leaked into the menu: %+v", blocks[0])
		}
	}
}

// TestParseDialogOptionBlocksFoldsWrappedOptions pins codex's update dialog,
// whose first option wraps over three lines at a deeper indent. A parser that
// split those into separate options would compute the wrong distance to the
// option below them.
func TestParseDialogOptionBlocksFoldsWrappedOptions(t *testing.T) {
	const screen = `  Release notes: https://github.com/openai/codex/relea

› 1. Update now (runs ` + "`sh -c 'curl -fsSL" + `
     https://chatgpt.com/codex/install.sh |
     CODEX_NON_INTERACTIVE=1 sh'` + "`" + `)
  2. Skip
  3. Skip until next version

  Press enter to continue`
	blocks := parseDialogOptionBlocks(screen)
	if len(blocks) != 1 {
		t.Fatalf("found %d menus, want 1: %+v", len(blocks), blocks)
	}
	got := blocks[0]
	if len(got) != 3 {
		t.Fatalf("options = %+v, want 3 (the wrapped first option must fold into one)", got)
	}
	if !got[0].Selected || got[0].Ordinal != 1 || !strings.Contains(got[0].Label, "install.sh") {
		t.Errorf("option 1 = %+v, want the selected, wrapped 'Update now' row", got[0])
	}
	if got[1].Label != "Skip" || got[2].Label != "Skip until next version" {
		t.Errorf("options 2,3 = %+v, %+v", got[1], got[2])
	}
}

// ── the disposition oracle ───────────────────────────────────────────────────

// fakeTUI is a menu that actually responds to keystrokes, so a test can assert
// WHICH OPTION WAS CONFIRMED rather than which keys were sent. That distinction
// is the point: an assertion on keys passes for any implementation that happens
// to emit the expected bytes, including one that reverts to a fixed sequence
// and is right by luck on one layout. An assertion on the confirmed option
// fails for every implementation that lands on the wrong row, whatever it sent.
type fakeTUI struct {
	header   []string
	options  []string
	ordinals bool
	cursor   int
	// wrap models a menu whose ends join. Claude Code's trust menu does this —
	// measured live 2026-09-07: from "No, exit", Down then Down returns the
	// cursor to "No, exit". Modeling only a clamping menu is what let a
	// stale-read defect pass this suite.
	wrap bool
	// lag models read-your-writes latency: the TUI advances its cursor when it
	// RECEIVES a key, while the screen repaints some polls later. Every real
	// terminal does this; neither oracle used to.
	//
	// lag means "trails by one poll". lagPolls sets the depth explicitly and
	// wins when non-zero. The depth matters: a lag of one catches the original
	// stale-read defect, but the per-call invariant bug (gas-4sdq) only appears
	// when the repaint trails by MORE than the selector's observe bound, so an
	// oracle that can only express one poll cannot reach it.
	lag       bool
	lagPolls  int
	frames    []int
	confirmed string
	keys      []string
	// footer is the line the renderer prints under the options. Several
	// matchers key on it ("Press enter to continue" for codex's update dialog
	// vs "Enter to confirm" for claude's), so a menu model that hardcodes one
	// cannot stand in for the other.
	footer string
}

func (f *fakeTUI) render() string { return f.renderAt(f.cursor) }

func (f *fakeTUI) renderAt(cursor int) string {
	var b strings.Builder
	for _, h := range f.header {
		b.WriteString(" " + h + "\n")
	}
	b.WriteString("\n")
	for i, opt := range f.options {
		marker := "  "
		if i == cursor {
			marker = "❯ "
		}
		if f.ordinals {
			b.WriteString(" " + marker + string(rune('1'+i)) + ". " + opt + "\n")
		} else {
			b.WriteString(" " + marker + opt + "\n")
		}
	}
	footer := f.footer
	if footer == "" {
		footer = "Enter to confirm · Esc to cancel"
	}
	b.WriteString("\n " + footer)
	return b.String()
}

// peek renders the cursor position a viewer would SEE, which under lag trails
// the position the TUI has actually moved to.
func (f *fakeTUI) peek(int) (string, error) {
	depth := f.lagPolls
	if depth == 0 && f.lag {
		depth = 1
	}
	if depth <= 0 {
		return f.renderAt(f.cursor), nil
	}
	f.frames = append(f.frames, f.cursor)
	i := len(f.frames) - 1 - depth
	if i < 0 {
		i = 0
	}
	return f.renderAt(f.frames[i]), nil
}

func (f *fakeTUI) sendKeys(keys ...string) error {
	f.keys = append(f.keys, keys...)
	for _, k := range keys {
		switch k {
		case "Down":
			switch {
			case f.cursor < len(f.options)-1:
				f.cursor++
			case f.wrap:
				f.cursor = 0
			}
		case "Up":
			switch {
			case f.cursor > 0:
				f.cursor--
			case f.wrap:
				f.cursor = len(f.options) - 1
			}
		case "Enter":
			if f.confirmed == "" {
				f.confirmed = f.options[f.cursor]
			}
		}
	}
	return nil
}

func TestConfirmDialogOptionByTextConfirmsTheNamedOption(t *testing.T) {
	withZeroDialogTimings(t)
	tests := []struct {
		name          string
		tui           *fakeTUI
		wantConfirmed string
	}{
		{
			// The regression. Decline first and pre-selected, no ordinals: this
			// is the layout that killed CIPcodes/anthony.
			name: "claude, decline first and pre-selected",
			tui: &fakeTUI{
				header:  []string{"Quick safety check: Is this a project you created?"},
				options: []string{"No, exit", "Yes, I trust this folder"},
			},
			wantConfirmed: "Yes, I trust this folder",
		},
		{
			// The layout gas-vs0e was measured on. Must keep working.
			name: "claude, accept first and pre-selected",
			tui: &fakeTUI{
				header:  []string{"Quick safety check: Is this a project you created?"},
				options: []string{"Yes, I trust this folder", "No, exit"},
			},
			wantConfirmed: "Yes, I trust this folder",
		},
		{
			name: "codex, numbered, accept first",
			tui: &fakeTUI{
				header:   []string{"Do you trust the contents of this directory?"},
				options:  []string{"Yes, continue", "No, quit"},
				ordinals: true,
			},
			wantConfirmed: "Yes, continue",
		},
		{
			// Position is not even stable within one renderer: a third option
			// inserted above moves the accept row without renaming it.
			name: "accept option third, cursor on the first",
			tui: &fakeTUI{
				header:  []string{"Quick safety check"},
				options: []string{"No, exit", "Show me the files first", "Yes, I trust this folder"},
			},
			wantConfirmed: "Yes, I trust this folder",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := newDialogSelector().confirmOptionByText(context.Background(), tc.tui.peek, tc.tui.sendKeys,
				tc.tui.render(), workspaceTrustAcceptPatterns)
			if err != nil {
				t.Fatalf("confirmOptionByText: %v", err)
			}
			if !ok {
				t.Fatalf("declined to answer a dialog it should have answered (keys=%v)", tc.tui.keys)
			}
			if tc.tui.confirmed != tc.wantConfirmed {
				t.Fatalf("CONFIRMED %q, want %q (keys=%v)", tc.tui.confirmed, tc.wantConfirmed, tc.tui.keys)
			}
		})
	}
}

// TestConfirmDialogOptionByTextParksRatherThanGuessing is the fail-closed half
// of the contract. In each of these states the function must send NOTHING that
// commits a choice, leaving the modal up for a human.
func TestConfirmDialogOptionByTextParksRatherThanGuessing(t *testing.T) {
	withZeroDialogTimings(t)
	tests := []struct {
		name string
		tui  *fakeTUI
	}{
		{
			name: "no option means what we are looking for",
			tui: &fakeTUI{
				header:  []string{"Quick safety check"},
				options: []string{"No, exit", "Read the security guide"},
			},
		},
		{
			// gemini's accept row embeds the folder name, so it is matched by
			// prefix. A menu offering two such rows is a menu where the prefix
			// no longer identifies one option, and a coin flip between two
			// grants of different scope is not an improvement on parking.
			name: "one pattern matches two rows, so the choice is ambiguous",
			tui: &fakeTUI{
				header:  []string{"Do you trust the files in this folder?"},
				options: []string{"Trust folder (alpha)", "Trust folder (beta)", "Don't trust"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, err := newDialogSelector().confirmOptionByText(context.Background(), tc.tui.peek, tc.tui.sendKeys,
				tc.tui.render(), workspaceTrustAcceptPatterns)
			if err != nil {
				t.Fatalf("confirmOptionByText: %v", err)
			}
			if ok {
				t.Fatalf("reported a confirmation it could not have made safely")
			}
			if tc.tui.confirmed != "" {
				t.Fatalf("CONFIRMED %q on a dialog it could not disposition", tc.tui.confirmed)
			}
		})
	}
}

// TestConfirmDialogOptionByTextRefusesToConfirmWhatItCannotSee covers the
// renderer that stops drawing a cursor glyph at all (selection by color only).
// Nothing on screen then says which row is selected, so nothing may be pressed.
func TestConfirmDialogOptionByTextRefusesToConfirmWhatItCannotSee(t *testing.T) {
	withZeroDialogTimings(t)
	const noCursor = ` Quick safety check: is this a project you trust?

   No, exit
   Yes, I trust this folder

 Enter to confirm`
	var sent []string
	ok, err := newDialogSelector().confirmOptionByText(context.Background(),
		func(int) (string, error) { return noCursor, nil },
		func(keys ...string) error { sent = append(sent, keys...); return nil },
		noCursor, workspaceTrustAcceptPatterns)
	if err != nil {
		t.Fatalf("confirmOptionByText: %v", err)
	}
	if ok {
		t.Fatal("confirmed a selection on a screen with no visible cursor")
	}
	if len(sent) != 0 {
		t.Fatalf("sent %v to a dialog whose selection it could not read", sent)
	}
}

// TestConfirmDialogOptionByTextWillNotConfirmAnUnverifiedMove is the guard on
// the move/observe/commit order. The pane here accepts movement keys but never
// repaints, which is indistinguishable from a pane that ignored them. Pressing
// Enter would confirm whatever is actually selected — the exact defect.
func TestConfirmDialogOptionByTextWillNotConfirmAnUnverifiedMove(t *testing.T) {
	withZeroDialogTimings(t)
	frozen := (&fakeTUI{
		header:  []string{"Quick safety check"},
		options: []string{"No, exit", "Yes, I trust this folder"},
	}).render()
	var sent []string
	ok, err := newDialogSelector().confirmOptionByText(context.Background(),
		func(int) (string, error) { return frozen, nil },
		func(keys ...string) error { sent = append(sent, keys...); return nil },
		frozen, workspaceTrustAcceptPatterns)
	if err != nil {
		t.Fatalf("confirmOptionByText: %v", err)
	}
	if ok {
		t.Fatal("reported confirmation against a pane that never showed the cursor move")
	}
	for _, k := range sent {
		if k == "Enter" {
			t.Fatalf("sent Enter without ever observing the cursor on the wanted option: %v", sent)
		}
	}
}

// TestAcceptWorkspaceTrustDialogAnswersTheInvertedLayout drives the phase
// function the providers actually call, not just the helper.
func TestAcceptWorkspaceTrustDialogAnswersTheInvertedLayout(t *testing.T) {
	withZeroDialogTimings(t)
	tui := &fakeTUI{
		header:  []string{"Quick safety check: Is this a project you created or one you trust?"},
		options: []string{"No, exit", "Yes, I trust this folder"},
	}
	budget := newStartupDialogBudget(5 * time.Second)
	if err := acceptWorkspaceTrustDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
		t.Fatalf("acceptWorkspaceTrustDialog: %v", err)
	}
	if tui.confirmed != "Yes, I trust this folder" {
		t.Fatalf("CONFIRMED %q, want %q (keys=%v)", tui.confirmed, "Yes, I trust this folder", tui.keys)
	}
}

// TestAcceptWorkspaceTrustDialogLeavesAnUnanswerableModalUp pins the mayor's
// second requirement: recognized but not safely answerable must PARK, never
// exit, and must not burn the whole budget in a tight spin.
func TestAcceptWorkspaceTrustDialogLeavesAnUnanswerableModalUp(t *testing.T) {
	tui := &fakeTUI{
		header:  []string{"Quick safety check: Is this a project you created or one you trust?"},
		options: []string{"No, exit", "Read the security guide"},
	}
	budget := newStartupDialogBudget(750 * time.Millisecond)
	start := time.Now()
	if err := acceptWorkspaceTrustDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
		t.Fatalf("acceptWorkspaceTrustDialog: %v", err)
	}
	if tui.confirmed != "" {
		t.Fatalf("CONFIRMED %q on a modal it could not answer", tui.confirmed)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("spun for %v on an unanswerable modal; the budget must bound it", elapsed)
	}
}

// TestAcceptWorkspaceTrustDialogWaitsForOptionsToPaint pins the case that
// separates "cannot answer this" from "cannot answer this YET".
//
// A TUI draws its trust question before its option rows. The old
// position-based code never noticed, because it answered on the question
// alone. Reading the options means the first frames are unanswerable, and
// treating that as a verdict would park a perfectly healthy agent — which is
// exactly what the live codex arm did before this guard. Progress on screen
// buys more budget; a static screen does not.
func TestAcceptWorkspaceTrustDialogWaitsForOptionsToPaint(t *testing.T) {
	withZeroDialogTimings(t)

	tui := &fakeTUI{
		header:  []string{"Do you trust the contents of this directory?"},
		options: []string{"Yes, continue", "No, quit"},
	}
	frames := 0
	peek := func(int) (string, error) {
		frames++
		if frames <= 3 {
			// Question drawn, options not yet.
			return " Do you trust the contents of this directory?\n\n banner line " +
				strings.Repeat("x", frames), nil
		}
		return tui.render(), nil
	}
	budget := newStartupDialogBudget(2 * time.Second)
	if err := acceptWorkspaceTrustDialog(context.Background(), budget, peek, tui.sendKeys); err != nil {
		t.Fatalf("acceptWorkspaceTrustDialog: %v", err)
	}
	if tui.confirmed != "Yes, continue" {
		t.Fatalf("CONFIRMED %q, want %q — a modal still painting was treated as unanswerable (keys=%v)",
			tui.confirmed, "Yes, continue", tui.keys)
	}
}

// TestAcceptWorkspaceTrustDialogSurvivesRepaintLag is the guard for the defect
// the first version of this selector shipped with.
//
// A TUI advances its cursor when it RECEIVES a key while its screen repaints a
// poll later, so a read taken after sending a key can still describe the state
// before it. The old loop sent every step at once, slept a fixed 200ms, re-read
// and re-decided — and on a stale frame it moved AGAIN. Where that extra step
// lands is decided by menu layout, which is not a property we control:
//
//   - WRAPPING menu (claude's, measured live 2026-09-07): the duplicate step
//     returns the real cursor to "No, exit" while a stale frame shows the
//     accept row selected. Enter then confirms the DECLINE and exits the agent.
//   - CLAMPING menu: the overshoot is absorbed only while the accept option
//     sits at a menu end. Move it off the boundary and clamping stops saving
//     us.
//
// So the matrix crosses lag with both layouts AND puts the accept option
// somewhere other than the last row. "The accept option is at the end" is a
// positional assumption of exactly the kind this file exists to delete; a suite
// that only ever places it last cannot tell a correct implementation from one
// that is right by coincidence.
func TestAcceptWorkspaceTrustDialogSurvivesRepaintLag(t *testing.T) {
	withZeroDialogTimings(t)

	const accept = "Yes, I trust this folder"
	for _, tc := range []struct {
		name    string
		options []string
		lag     bool
		wrap    bool
	}{
		{"lag + wrapping menu, accept last (claude today)", []string{"No, exit", accept}, true, true},
		{"lag + clamping menu, accept last", []string{"No, exit", accept}, true, false},
		{"lag + clamping menu, accept NOT last", []string{"No, exit", accept, "No, and don't ask again"}, true, false},
		{"lag + wrapping menu, accept NOT last", []string{"No, exit", accept, "No, and don't ask again"}, true, true},
		{"no lag, wrapping menu", []string{"No, exit", accept}, false, true},
		{"no lag, clamping menu", []string{"No, exit", accept}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tui := &fakeTUI{
				header:  []string{"Quick safety check: Is this a project you created or one you trust?"},
				options: tc.options,
				lag:     tc.lag,
				wrap:    tc.wrap,
			}
			budget := newStartupDialogBudget(5 * time.Second)
			if err := acceptWorkspaceTrustDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
				t.Fatalf("acceptWorkspaceTrustDialog: %v", err)
			}
			if tui.confirmed != accept {
				t.Fatalf("CONFIRMED %q, want %q (keys=%v) — a frame was acted on that predates the keystrokes already sent",
					tui.confirmed, accept, tui.keys)
			}
		})
	}
}

// TestDialogSelectorBoundsMovementKeysAcrossTheWholePhase pins the bound that
// the phase's own retry loop used to defeat.
//
// dialogSelectionMaxMoves is per PHASE, not per call. It used to be per call,
// with the outer poll loop calling again for as long as the budget lived — a
// pane that ignored Down was measured taking nine movement keys against a
// documented bound of three. Keystrokes into a pane whose state we have already
// concluded we cannot verify are exactly what must not be unbounded.
func TestDialogSelectorBoundsMovementKeysAcrossTheWholePhase(t *testing.T) {
	withZeroDialogTimings(t)
	dialogPollInterval = 2 * time.Millisecond

	// A pane that renders the modal and never responds to a keystroke.
	frozen := (&fakeTUI{
		header:  []string{"Quick safety check"},
		options: []string{"No, exit", "Yes, I trust this folder"},
	}).render()
	var sent []string
	peek := func(int) (string, error) { return frozen, nil }
	sendKeys := func(keys ...string) error { sent = append(sent, keys...); return nil }

	budget := newStartupDialogBudget(300 * time.Millisecond)
	if err := acceptWorkspaceTrustDialog(context.Background(), budget, peek, sendKeys); err != nil {
		t.Fatalf("acceptWorkspaceTrustDialog: %v", err)
	}

	moves := 0
	for _, k := range sent {
		switch k {
		case "Down", "Up":
			moves++
		case "Enter":
			t.Fatalf("confirmed against a pane whose cursor was never observed to move: %v", sent)
		}
	}
	if moves > dialogSelectionMaxMoves {
		t.Fatalf("sent %d movement keys (%v), bound is %d per phase", moves, sent, dialogSelectionMaxMoves)
	}
}

// codexUpdateMenu is the codex self-update dialog as captured live 2026-09-04.
// Option 1 is pre-selected and runs a remote installer piped to a shell, which
// is why this dialog's disposition is not a matter of taste.
func codexUpdateMenu(lag, wrap bool) *fakeTUI {
	return &fakeTUI{
		header: []string{"✨ Update available! 0.153.2 -> 0.153.4"},
		options: []string{
			"Update now (runs sh -c 'curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh')",
			"Skip",
			"Skip until next version",
		},
		ordinals: true,
		footer:   "Press enter to continue",
		lag:      lag,
		wrap:     wrap,
	}
}

// TestAcceptCodexUpdateDialogNeverConfirmsTheInstaller is the guard that makes
// this dialog's safety independent of where its options sit.
//
// It was dismissed with a bare Down then Enter, which lands on "Skip" only
// because "Skip" is currently second. A reorder, or a repaint lag on a wrapping
// menu, and the same two keystrokes confirm "Update now" — an unattended remote
// install inside an agent pane, on every spawn, with nobody watching. That is a
// worse outcome than the agent-exit that gas-193q was filed for, reached by the
// identical mechanism.
func TestAcceptCodexUpdateDialogNeverConfirmsTheInstaller(t *testing.T) {
	withZeroDialogTimings(t)

	const installer = "Update now (runs sh -c 'curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh')"

	for _, tc := range []struct {
		name      string
		options   []string
		lag, wrap bool
	}{
		// Today's layout. Down+Enter is CORRECT here, which is exactly why
		// these cases cannot tell a text-addressed selector from a positional
		// one — they are the coincidence, not the test.
		{"today's layout", []string{installer, "Skip", "Skip until next version"}, false, false},
		{"today's layout, repaint lag on a wrapping menu", []string{installer, "Skip", "Skip until next version"}, true, true},

		// The cases that decide it. "The safe option is second" is a property
		// of codex's renderer, not one we own, and it can change in a release
		// we do not control. Down+Enter confirms whatever is second — and in
		// the first two that is the installer.
		{"REORDERED so the installer is second", []string{"Skip", installer, "Skip until next version"}, false, false},
		{"REORDERED so the installer is second, with lag", []string{"Skip", installer, "Skip until next version"}, true, true},
		{"REORDERED so Skip is last", []string{installer, "Skip until next version", "Skip"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tui := codexUpdateMenu(tc.lag, tc.wrap)
			tui.options = tc.options
			budget := newStartupDialogBudget(5 * time.Second)
			if err := acceptCodexUpdateDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
				t.Fatalf("acceptCodexUpdateDialog: %v", err)
			}
			if strings.HasPrefix(tui.confirmed, "Update now") {
				t.Fatalf("CONFIRMED THE INSTALLER: %q (keys=%v)", tui.confirmed, tui.keys)
			}
			if tui.confirmed != "Skip" {
				t.Fatalf("CONFIRMED %q, want %q (keys=%v)", tui.confirmed, "Skip", tui.keys)
			}
		})
	}
}

// TestAcceptCodexUpdateDialogParksWhenNoSafeOptionIsNamed covers the renderer
// that drops both options we know how to ask for. Parking costs a stalled
// spawn, which is loud and recoverable; guessing costs an unattended install.
func TestAcceptCodexUpdateDialogParksWhenNoSafeOptionIsNamed(t *testing.T) {
	withZeroDialogTimings(t)

	tui := &fakeTUI{
		header:   []string{"✨ Update available! 0.153.2 -> 0.153.4"},
		options:  []string{"Update now (runs an installer)", "Remind me later", "Skip until next version"},
		ordinals: true,
		footer:   "Press enter to continue",
	}
	// "Skip until next version" is still present, so this menu IS answerable —
	// swap it out to make nothing match.
	tui.options[2] = "Not now"
	budget := newStartupDialogBudget(400 * time.Millisecond)
	if err := acceptCodexUpdateDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
		t.Fatalf("acceptCodexUpdateDialog: %v", err)
	}
	if tui.confirmed != "" {
		t.Fatalf("CONFIRMED %q on a menu naming no option we know to be safe", tui.confirmed)
	}
}

// TestAcceptClaudeResumeDialogKeepsTheSessionIntact pins the resume selector.
//
// Lower stakes than the installer — the wrong option summarizes a session
// instead of resuming it, losing in-flight context rather than running code —
// but the identical mechanism, and its captured layout is the OLDEST of the
// three (a repository fixture from 2026-07-16, never re-captured). Being
// text-addressed is what makes that staleness survivable: if the labels have
// moved, this parks instead of confirming whatever now sits second.
func TestAcceptClaudeResumeDialogKeepsTheSessionIntact(t *testing.T) {
	withZeroDialogTimings(t)

	const asIs = "Resume full session as-is"

	for _, tc := range []struct {
		name      string
		options   []string
		lag, wrap bool
	}{
		{"today's layout", []string{"Resume from summary (recommended)", asIs, "Don't ask me again"}, false, false},
		{"today's layout, repaint lag on a wrapping menu", []string{"Resume from summary (recommended)", asIs, "Don't ask me again"}, true, true},
		// The discriminating case. This fixture is seven weeks old and has
		// never been re-captured, so a reorder here is less hypothetical than
		// unobserved.
		{"REORDERED so as-is is no longer second", []string{"Resume from summary (recommended)", "Don't ask me again", asIs}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tui := &fakeTUI{
				header:   []string{"This session is 1h 55m old and 212.7k tokens."},
				options:  tc.options,
				ordinals: true,
				lag:      tc.lag,
				wrap:     tc.wrap,
			}
			budget := newStartupDialogBudget(5 * time.Second)
			if err := acceptClaudeResumeDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
				t.Fatalf("acceptClaudeResumeDialog: %v", err)
			}
			if tui.confirmed != "Resume full session as-is" {
				t.Fatalf("CONFIRMED %q, want %q (keys=%v)", tui.confirmed, "Resume full session as-is", tui.keys)
			}
		})
	}
}

// TestDialogSelectorPoisonsItselfWhenAKeystrokeIsNeverObserved pins the
// per-PHASE scope of the one-key-in-flight invariant.
//
// The invariant used to hold per CALL. awaitSelectionMoved gives one key a
// bounded number of polls; if the repaint never arrives within them the call
// returned false and said nothing about the key it had already sent. The phase
// loop then re-peeked — still the stale frame — called again, derived the same
// Steps, and sent a SECOND key. The first key's repaint then landed, the
// selected label changed, and that change was credited to the second key. On a
// wrapping menu the real cursor was by then back on the decline row, and Enter
// confirmed it: keys=[Down Down Enter], confirmed "No, exit" (gas-4sdq).
//
// The lag here is deeper than dialogSelectionObserveAttempts on purpose. That
// depth is the whole precondition, and it is why the one-poll lag model that
// catches the original defect cannot express this one.
func TestDialogSelectorPoisonsItselfWhenAKeystrokeIsNeverObserved(t *testing.T) {
	withZeroDialogTimings(t)

	tui := &fakeTUI{
		header:   []string{"Quick safety check: Is this a project you created or one you trust?"},
		options:  []string{"No, exit", "Yes, I trust this folder"},
		wrap:     true,
		lagPolls: dialogSelectionObserveAttempts + 4,
	}
	budget := newStartupDialogBudget(2 * time.Second)
	if err := acceptWorkspaceTrustDialog(context.Background(), budget, tui.peek, tui.sendKeys); err != nil {
		t.Fatalf("acceptWorkspaceTrustDialog: %v", err)
	}
	if tui.confirmed == "No, exit" {
		t.Fatalf("CONFIRMED THE DECLINE (keys=%v) — a late repaint was credited to a newer keystroke", tui.keys)
	}
	if tui.confirmed != "" {
		t.Fatalf("CONFIRMED %q from a screen with a keystroke outstanding (keys=%v)", tui.confirmed, tui.keys)
	}
	downs := 0
	for _, k := range tui.keys {
		if k == "Down" || k == "Up" {
			downs++
		}
	}
	if downs > 1 {
		t.Fatalf("sent %d movement keys (%v); an unobserved key must stop the phase, not license another", downs, tui.keys)
	}
}
