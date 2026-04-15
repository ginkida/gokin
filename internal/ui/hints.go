package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// HintSystem manages contextual hints for the user.
type HintSystem struct {
	enabled      bool
	hintsShown   map[string]int
	styles       *Styles
	lastHint     string
	lastHintTime time.Time
}

// NewHintSystem creates a new hint system.
func NewHintSystem(styles *Styles) *HintSystem {
	return &HintSystem{
		enabled:    true,
		hintsShown: make(map[string]int),
		styles:     styles,
	}
}

// GetContextualHint returns a contextual hint based on the current state.
func (h *HintSystem) GetContextualHint(state State, currentTool string, sessionDuration time.Duration) string {
	if !h.enabled {
		return ""
	}

	// Minimum delay between hints: 30 seconds (not 60 - that's too aggressive)
	if time.Since(h.lastHintTime) < 30*time.Second {
		return ""
	}

	var hint string
	hintID := ""

	// Context-aware hints with benefit-focused wording
	switch {
	case sessionDuration < 2*time.Minute:
		hint = "Shift+Tab — tackle complex tasks step-by-step with planning mode"
		hintID = "first_message"

	case state == StateStreaming:
		hint = "Esc — cancel current response"
		hintID = "cancel_streaming"

	default:
		// Rotate through general hints (benefit-focused)
		generalHints := []string{
			"? — show all keyboard shortcuts",
			"Shift+Tab — break complex tasks into reviewable steps",
			"Ctrl+P — quickly find any command",
			"Option+C — copy the last response to clipboard",
			"Ctrl+T — track background tasks",
			"Ctrl+O — see what agents are doing in real time",
			"Type / to explore all available commands",
			getTextSelectionHint(),
		}

		idx := len(h.hintsShown) % len(generalHints)
		hint = generalHints[idx]
		hintID = fmt.Sprintf("general_%d", idx)
	}

	// Check BEFORE incrementing (fix logic order)
	if h.hintsShown[hintID] >= 1 {
		return ""
	}

	// Mark as shown AFTER the check
	h.hintsShown[hintID]++
	h.lastHint = hint
	h.lastHintTime = time.Now()
	return hint
}

// RenderHint renders a hint with minimal styling (single line).
func (h *HintSystem) RenderHint(hint string) string {
	if hint == "" {
		return ""
	}

	hintStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	return hintStyle.Render("Tip: " + hint)
}

// ShouldShowHint checks if a hint should be shown based on frequency.
func (h *HintSystem) ShouldShowHint(hintID string, maxShows int) bool {
	count, exists := h.hintsShown[hintID]
	if !exists {
		return true
	}
	return count < maxShows
}

// MarkHintShown marks a hint as shown.
func (h *HintSystem) MarkHintShown(hintID string) {
	h.hintsShown[hintID]++
	h.lastHintTime = time.Now()
}

// Reset clears hint history.
func (h *HintSystem) Reset() {
	h.hintsShown = make(map[string]int)
}

// Disable turns off hints.
func (h *HintSystem) Disable() {
	h.enabled = false
}

// Enable turns on hints.
func (h *HintSystem) Enable() {
	h.enabled = true
}
