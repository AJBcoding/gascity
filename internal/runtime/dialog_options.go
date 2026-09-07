package runtime

import (
	"context"
	"strconv"
	"strings"
	"unicode"
)

// Option-menu parsing and text-addressed selection for TUI startup dialogs.
//
// WHY THIS EXISTS. Every dismissal in dialog.go used to answer by POSITION: a
// bare Enter for "the pre-selected option", or Down+Enter to reach the second
// one. Position is a property of the renderer, not of the decision. On
// 2026-09-04 Claude Code's workspace-trust dialog was measured rendering
//
//	❯ No, exit
//	  Yes, I trust this folder
//
// — decline first AND pre-selected, with no ordinals. Every bare-Enter
// dismissal therefore DECLINED TRUST AND QUIT THE AGENT, in every directory the
// agent had not already been trusted for, which is every polecat worktree by
// construction (gas-193q).
//
// The failure is worse than not dismissing at all. An unanswered modal leaves
// the agent parked, which is recoverable and eventually loud; a wrongly answered
// one terminates it, and terminating during startup reads as a spawn problem
// rather than a dialog problem.
//
// So selection here is addressed by OPTION TEXT and committed only after the
// cursor is OBSERVED on the chosen option. The two properties that buys:
//
//   - A renderer that reorders options cannot make us choose the opposite one;
//     it can only make us fail to find ours, which is a verdict, not an answer.
//   - A renderer that changes how selection is drawn cannot make us confirm
//     blind: no observation, no Enter.
//
// Failing closed means leaving the modal UP. That is deliberate. A parked agent
// is a page; a dead one is a mystery.

// dialogCursorGlyphs are the leading marks TUIs use for the selected row. The
// glyph must be followed by a space, which is what keeps ordinary output from
// being read as a menu — codex draws a ">─You are in /path" banner whose '>' is
// followed by a box-drawing rune, not a space.
var dialogCursorGlyphs = map[rune]bool{
	'❯': true, '›': true, '>': true, '▶': true, '▸': true, '»': true, '→': true,
	'●': true, '•': true, '◆': true, '*': true,
}

// dialogOption is one selectable row of an on-screen menu.
type dialogOption struct {
	// Label is the row's text with any cursor glyph and any "N." ordinal
	// removed, so it compares against what a human reads.
	Label string
	// Ordinal is the "N." prefix when the renderer numbers its options, 0 when
	// it does not. Recorded, deliberately unused for selection: claude's trust
	// dialog dropped its ordinals in the same change that inverted the order,
	// so a selector that needed them would have broken too.
	Ordinal int
	// Selected reports that the cursor glyph sits on this row.
	Selected bool
}

// parseDialogOptionBlocks returns every contiguous menu found on screen.
//
// A block is anchored on a cursor row and extends over the neighboring rows
// whose text begins in the same column, so a wrapped option (rendered with
// deeper indentation) folds into the option above it rather than splitting the
// menu. A blank row, or a row starting left of the option column, ends it —
// that is what separates the options from the prose above them.
//
// Multiple blocks are returned rather than one guess: the caller knows which
// option text it is looking for and can pick the block that contains it.
func parseDialogOptionBlocks(content string) [][]dialogOption {
	lines := strings.Split(content, "\n")
	runes := make([][]rune, len(lines))
	indent := make([]int, len(lines))
	for i, ln := range lines {
		r := []rune(strings.TrimRight(ln, "\r"))
		runes[i] = r
		indent[i] = -1
		for c, ch := range r {
			if !unicode.IsSpace(ch) {
				indent[i] = c
				break
			}
		}
	}

	// cursorTextCol reports the column an option's text starts in when line i is
	// a cursor row, and -1 when it is not one.
	cursorTextCol := func(i int) int {
		c := indent[i]
		if c < 0 || c+2 >= len(runes[i]) {
			return -1
		}
		if !dialogCursorGlyphs[runes[i][c]] || runes[i][c+1] != ' ' {
			return -1
		}
		for c2 := c + 2; c2 < len(runes[i]); c2++ {
			if !unicode.IsSpace(runes[i][c2]) {
				return c2
			}
		}
		return -1
	}

	var blocks [][]dialogOption
	claimed := map[int]bool{}
	for i := range lines {
		if claimed[i] {
			continue
		}
		optCol := cursorTextCol(i)
		if optCol < 0 {
			continue
		}
		// effectiveIndent treats the cursor row as if it began at the option
		// column, so the marked row and its unmarked siblings compare equal.
		effective := func(j int) int {
			if j == i {
				return optCol
			}
			return indent[j]
		}
		inBlock := func(j int) bool {
			return j >= 0 && j < len(lines) && indent[j] >= optCol && effective(j) >= optCol
		}
		lo := i
		for inBlock(lo - 1) {
			lo--
		}
		hi := i
		for inBlock(hi + 1) {
			hi++
		}

		var block []dialogOption
		for j := lo; j <= hi; j++ {
			claimed[j] = true
			text := strings.TrimSpace(string(runes[j][min(len(runes[j]), optCol):]))
			if j == i {
				text = strings.TrimSpace(string(runes[j][optCol:]))
			}
			if text == "" {
				continue
			}
			if effective(j) > optCol && len(block) > 0 {
				// A wrapped continuation of the option above it.
				block[len(block)-1].Label += " " + text
				continue
			}
			ord, label := splitDialogOrdinal(text)
			block = append(block, dialogOption{Label: label, Ordinal: ord, Selected: j == i})
		}
		if len(block) > 0 {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// splitDialogOrdinal strips a leading "N." or "N)" list marker.
func splitDialogOrdinal(text string) (int, string) {
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 || end >= len(text) {
		return 0, text
	}
	if text[end] != '.' && text[end] != ')' {
		return 0, text
	}
	n, err := strconv.Atoi(text[:end])
	if err != nil {
		return 0, text
	}
	return n, strings.TrimSpace(text[end+1:])
}

// dialogOptionMatch is the resolved position of a wanted option within a menu.
type dialogOptionMatch struct {
	// Steps is how far the cursor must move: positive is Down, negative is Up,
	// zero means the wanted option is already selected.
	Steps int
	Label string
}

// dialogOptionPattern names one option precisely enough to tell it apart from
// its neighbors. Exactly one of Exact or Prefix is set.
//
// Substring matching was tried first and is wrong here, and pi's real trust
// menu is why: its five options are "Trust", "Trust parent folder (...)",
// "Trust (this session only)", "Do not trust" and "Do not trust (this session
// only)". A substring "trust" matches all five — including both declines. An
// exact "Trust" matches exactly one. Prefix exists for the case where the label
// embeds a variable, as gemini's "Trust folder (city)" embeds the folder name.
type dialogOptionPattern struct {
	Exact  string
	Prefix string
}

func (p dialogOptionPattern) matches(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	switch {
	case p.Exact != "":
		return l == strings.ToLower(p.Exact)
	case p.Prefix != "":
		return strings.HasPrefix(l, strings.ToLower(p.Prefix))
	default:
		return false
	}
}

// findDialogOption locates the option to choose, trying patterns in order and
// taking the first one that identifies EXACTLY ONE row.
//
// Order matters and ambiguity is a failure rather than a tie-break. Both rules
// exist for the same reason: on a menu this code has never seen, the row that
// "sort of" matches is as likely to be a decline as an accept, and the caller's
// alternative — leave the modal up — costs a page rather than an agent.
func findDialogOption(content string, patterns []dialogOptionPattern) (dialogOptionMatch, bool) {
	blocks := parseDialogOptionBlocks(content)
	for _, pat := range patterns {
		var (
			found   dialogOptionMatch
			matches int
		)
		for _, block := range blocks {
			sel := -1
			for i, opt := range block {
				if opt.Selected {
					sel = i
				}
			}
			if sel < 0 {
				continue
			}
			for i, opt := range block {
				if !pat.matches(opt.Label) {
					continue
				}
				matches++
				found = dialogOptionMatch{Steps: i - sel, Label: opt.Label}
			}
		}
		if matches == 1 {
			return found, true
		}
	}
	return dialogOptionMatch{}, false
}

// dialogSelectionAttempts bounds the move/observe loop. Each pass sends at most
// one batch of movement keys and then re-reads, so a renderer that repaints
// slowly gets a few chances without the loop becoming a retry storm.
const dialogSelectionAttempts = 3

// confirmDialogOptionByText moves the cursor onto the option whose label
// matches want and presses Enter — but only once the cursor has been OBSERVED
// there on a fresh read of the screen.
//
// Returns true when Enter was sent. Returns false, nil when the option could
// not be found, was ambiguous, or could not be observed as selected: in that
// case NOTHING is confirmed and the modal is deliberately left up for a human.
// The contract is one-directional on purpose — this function may decline to
// answer, but it may never answer wrongly.
func confirmDialogOptionByText(
	ctx context.Context,
	peek func(lines int) (string, error),
	sendKeys func(keys ...string) error,
	content string,
	patterns []dialogOptionPattern,
) (bool, error) {
	for attempt := 0; attempt < dialogSelectionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		match, ok := findDialogOption(content, patterns)
		if !ok {
			return false, nil
		}
		if match.Steps == 0 {
			if err := sendKeys("Enter"); err != nil {
				return false, err
			}
			sleep(ctx, startupDialogAcceptDelay)
			return true, nil
		}
		key := "Down"
		steps := match.Steps
		if steps < 0 {
			key = "Up"
			steps = -steps
		}
		keys := make([]string, 0, steps)
		for i := 0; i < steps; i++ {
			keys = append(keys, key)
		}
		if err := sendKeys(keys...); err != nil {
			return false, err
		}
		sleep(ctx, bypassDialogConfirmDelay)
		next, err := peek(startupDialogPeekLines)
		if err != nil {
			return false, err
		}
		content = next
	}
	// The cursor never came to rest on the wanted option. Confirming now would
	// be confirming whatever it landed on instead, which is the defect this
	// whole file exists to remove.
	return false, nil
}
