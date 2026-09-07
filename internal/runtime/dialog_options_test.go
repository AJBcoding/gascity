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
	header    []string
	options   []string
	ordinals  bool
	cursor    int
	confirmed string
	keys      []string
}

func (f *fakeTUI) render() string {
	var b strings.Builder
	for _, h := range f.header {
		b.WriteString(" " + h + "\n")
	}
	b.WriteString("\n")
	for i, opt := range f.options {
		marker := "  "
		if i == f.cursor {
			marker = "❯ "
		}
		if f.ordinals {
			b.WriteString(" " + marker + string(rune('1'+i)) + ". " + opt + "\n")
		} else {
			b.WriteString(" " + marker + opt + "\n")
		}
	}
	b.WriteString("\n Enter to confirm · Esc to cancel")
	return b.String()
}

func (f *fakeTUI) peek(int) (string, error) { return f.render(), nil }

func (f *fakeTUI) sendKeys(keys ...string) error {
	f.keys = append(f.keys, keys...)
	for _, k := range keys {
		switch k {
		case "Down":
			if f.cursor < len(f.options)-1 {
				f.cursor++
			}
		case "Up":
			if f.cursor > 0 {
				f.cursor--
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
			ok, err := confirmDialogOptionByText(context.Background(), tc.tui.peek, tc.tui.sendKeys,
				tc.tui.render(), workspaceTrustAcceptPatterns)
			if err != nil {
				t.Fatalf("confirmDialogOptionByText: %v", err)
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
			ok, err := confirmDialogOptionByText(context.Background(), tc.tui.peek, tc.tui.sendKeys,
				tc.tui.render(), workspaceTrustAcceptPatterns)
			if err != nil {
				t.Fatalf("confirmDialogOptionByText: %v", err)
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
	ok, err := confirmDialogOptionByText(context.Background(),
		func(int) (string, error) { return noCursor, nil },
		func(keys ...string) error { sent = append(sent, keys...); return nil },
		noCursor, workspaceTrustAcceptPatterns)
	if err != nil {
		t.Fatalf("confirmDialogOptionByText: %v", err)
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
	ok, err := confirmDialogOptionByText(context.Background(),
		func(int) (string, error) { return frozen, nil },
		func(keys ...string) error { sent = append(sent, keys...); return nil },
		frozen, workspaceTrustAcceptPatterns)
	if err != nil {
		t.Fatalf("confirmDialogOptionByText: %v", err)
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
