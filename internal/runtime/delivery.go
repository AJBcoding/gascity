package runtime

import (
	"regexp"
	"strings"
	"time"
)

// DeliveryConfirmation is the result of asking a transport whether a payload
// actually entered the conversation, as opposed to whether the transport
// accepted the request to send it.
//
// Probed distinguishes "this transport has no probe" from "the probe ran and
// says no". Callers must treat both as NOT-confirmed — but only the second is
// evidence of a stranded payload, and conflating them is how a confirmation
// contract quietly degrades into the defect it was built to catch.
type DeliveryConfirmation struct {
	// Confirmed is true only when the probe positively observed that the
	// payload is no longer waiting to be submitted.
	Confirmed bool
	// Probed is false when the transport implements no probe at all.
	Probed bool
	// Observed describes what the probe saw, for the operator-facing message.
	// It is non-empty whenever Probed is true and Confirmed is false.
	Observed string
}

// DeliveryConfirmer is implemented by transports that can answer the only
// question that matters after a nudge: did the payload leave the input box and
// enter the conversation?
//
// This exists because both transports report success from the absence of a
// transport error. herdr's `agent prompt` returns outcome=ok in ~0.5ms — that
// is acceptance of the request, not submission — and tmux's `send-keys`
// succeeds regardless of what the TUI does with the keys. Neither says anything
// about whether the agent ever saw the message (gas-jfy6, gas-q4jk).
type DeliveryConfirmer interface {
	// ConfirmDelivery reports whether sent has been submitted in name's
	// session, polling until within elapses. It must not block longer than
	// within: a confirmation probe that hangs converts a delivery bug into an
	// availability bug.
	ConfirmDelivery(name, sent string, within time.Duration) (DeliveryConfirmation, error)
}

// deliveryPollInterval is how often ConfirmViaPeek re-reads the screen. The
// composer drains within a frame or two of a real submit, so this is short; the
// bound is the caller's `within`, not this.
const deliveryPollInterval = 150 * time.Millisecond

// ConfirmViaPeek implements the confirmation predicate for any transport that
// can render its session's screen. peek reads the last n lines.
//
// It polls rather than sampling once because a submit is not instantaneous: the
// composer still holds the payload for a frame or two after the Enter lands, and
// a single immediate read would report a successful delivery as stranded.
//
// A peek error is NOT a confirmation and NOT a stranding: it is reported as
// unprobed, so the caller says "could not confirm" rather than inventing either
// verdict.
func ConfirmViaPeek(peek func(lines int) (string, error), sent string, within time.Duration) (DeliveryConfirmation, error) {
	if peek == nil {
		return DeliveryConfirmation{}, nil
	}
	deadline := time.Now().Add(within)
	var lastObserved string
	for {
		screen, err := peek(deliveryPeekLines)
		if err != nil {
			return DeliveryConfirmation{Observed: "screen unreadable"}, err
		}
		pending, observed := InputLineHoldsPending(screen, sent)
		if !pending {
			return DeliveryConfirmation{Confirmed: true, Probed: true}, nil
		}
		lastObserved = observed
		if !time.Now().Before(deadline) {
			return DeliveryConfirmation{
				Probed:   true,
				Observed: lastObserved,
			}, nil
		}
		time.Sleep(deliveryPollInterval)
	}
}

// deliveryPeekLines is how much screen the probe reads. The composer is at the
// bottom, so a short tail is enough and keeps the read cheap on a hot path.
const deliveryPeekLines = 12

// pastePlaceholderRe matches the collapsed form a TUI shows for pasted input
// that is still sitting in the composer, e.g. "[Pasted text #13 +15 lines]".
// Matching only the literal sent text would miss this case entirely — and it is
// the case actually observed on the fleet, because a multi-line nudge is
// delivered as a paste and never appears verbatim on the input line.
var pastePlaceholderRe = regexp.MustCompile(`\[Pasted text #\d+(\s+\+\d+\s+lines?)?\]`)

// promptMarkers are the leading glyphs a composer line starts with. The input
// line is the only place stranded text lives; the same text echoed into the
// transcript above means the message WAS submitted, so a probe that searches
// the whole screen would report a successful delivery as a failure.
var promptMarkers = []string{">", "│", "❯", "»"}

// InputLineHoldsPending reports whether screen's composer still holds unsent
// input — either a paste placeholder or a recognizable prefix of sent.
//
// The predicate is deliberately "is anything still waiting to be submitted",
// NOT "did the agent start working". An idle→working transition is unusable as
// a confirmation for a target that is ALREADY working: it matches immediately
// whether or not the payload was submitted, so it degrades to a no-op against
// exactly the busy agents most likely to strand an order. Observing the
// composer works identically for idle and busy targets, because a stranded
// payload sits in the composer in both states.
func InputLineHoldsPending(screen, sent string) (bool, string) {
	line := composerLine(screen)
	if line == "" {
		return false, ""
	}
	if pastePlaceholderRe.MatchString(line) {
		return true, line
	}
	probe := strings.TrimSpace(sent)
	if probe == "" {
		return false, ""
	}
	// Compare against the composer's own text, with the prompt glyph stripped.
	// A short leading run is enough: the TUI may wrap, truncate, or elide a long
	// payload, so requiring the whole string would miss precisely the long
	// orders that matter most.
	body := strings.TrimSpace(stripPromptMarker(line))
	if body == "" {
		return false, ""
	}
	if strings.HasPrefix(probe, body) || strings.HasPrefix(body, firstRunes(probe, 24)) {
		return true, line
	}
	return false, ""
}

// composerLine returns the last non-empty line of screen, which is where the
// input composer renders.
func composerLine(screen string) string {
	lines := strings.Split(screen, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if trimmed := strings.TrimRight(lines[i], " \t"); strings.TrimSpace(trimmed) != "" {
			return trimmed
		}
	}
	return ""
}

func stripPromptMarker(line string) string {
	trimmed := strings.TrimSpace(line)
	for _, marker := range promptMarkers {
		if strings.HasPrefix(trimmed, marker) {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, marker))
		}
	}
	return trimmed
}

func firstRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
