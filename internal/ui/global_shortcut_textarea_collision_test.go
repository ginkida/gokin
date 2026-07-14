package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// This file pins round 8's fix for a class of bugs confirmed against the
// vendored bubbles/textarea@v0.21.0 DefaultKeyMap: several gokin global
// shortcuts (Ctrl+T, Ctrl+A, Ctrl+H close, Ctrl+B, Ctrl+F, Ctrl+G, Ctrl+S,
// Ctrl+X, Ctrl+L, Ctrl+D, Alt+C, Ctrl+K and Shift+Tab) returned a bare `nil`
// from handleGlobalKeys while m.state stayed
// StateInput — Update()'s KeyMsg case only skips forwarding the SAME
// keystroke to the compose textarea when the returned tea.Cmd is non-nil
// (case tea.KeyMsg: cmd := m.handleKeyMsg(msg); if cmd != nil { return m,
// cmd }). A bare nil let textarea's OWN default binding for that exact key
// ALSO fire, silently corrupting whatever the user was typing:
//   ctrl+t -> TransposeCharacterBackward, ctrl+a -> LineStart,
//   ctrl+h -> DeleteCharacterBackward, ctrl+b -> CharacterBackward,
//   ctrl+f -> CharacterForward, ctrl+d -> DeleteCharacterForward,
//   alt+c -> CapitalizeWordForward, ctrl+k -> DeleteAfterCursor.
// Each test goes through the FULL Update path (matching the established
// keystroke_double_process_test.go pattern) — calling handleGlobalKeys
// directly would miss the double-forward.

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return updated.(Model)
}

func TestUpdate_CtrlTDoesNotTransposeInput(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "ab")
	if v := m.input.Value(); v != "ab" {
		t.Fatalf("setup: input = %q, want %q", v, "ab")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m2 := updated.(Model)

	if !m2.todosVisible {
		t.Fatal("Ctrl+T must still toggle todosVisible")
	}
	if v := m2.input.Value(); v != "ab" {
		t.Fatalf("Ctrl+T corrupted the input (transposed): got %q, want %q", v, "ab")
	}
}

func TestUpdate_CtrlADoesNotMoveCursorToLineStart(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "hello")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m2 := updated.(Model)

	if m2.agentTreePanel == nil || !m2.agentTreePanel.IsVisible() {
		t.Fatal("Ctrl+A must still toggle the agent tree panel visible")
	}

	// If the cursor moved to line start, typing 'X' produces "Xhello";
	// if the cursor stayed at the end (correct), it produces "helloX".
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m3 := updated.(Model)
	if v := m3.input.Value(); v != "helloX" {
		t.Fatalf("Ctrl+A moved the compose cursor to line start: got %q, want %q", v, "helloX")
	}
}

func TestUpdate_CtrlHCloseDoesNotDeleteCharacter(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	// Force the panel into an already-visible state (as if opened on a
	// prior turn), so THIS Ctrl+H press exercises the close path — the one
	// that leaves m.state at StateInput and is where the collision bites.
	m.observatoryPanel.Toggle()
	if !m.observatoryPanel.IsVisible() {
		t.Fatal("setup: expected observatoryPanel to be visible")
	}
	m = typeText(t, m, "hello")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	m2 := updated.(Model)

	if m2.observatoryPanel.IsVisible() {
		t.Fatal("Ctrl+H must still close the observatory panel")
	}
	if v := m2.input.Value(); v != "hello" {
		t.Fatalf("Ctrl+H corrupted the input (deleted a character): got %q, want %q", v, "hello")
	}
}

func TestUpdate_CtrlBDoesNotMoveCursorBackward(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "hello")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlB})
	m2 := updated.(Model)

	// If the cursor moved backward, typing 'X' lands mid-string
	// ("hellXo"); if not (correct), it appends at the end ("helloX").
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m3 := updated.(Model)
	if v := m3.input.Value(); v != "helloX" {
		t.Fatalf("Ctrl+B moved the compose cursor backward: got %q, want %q", v, "helloX")
	}
}

func TestUpdate_CtrlFDoesNotMoveCursorForward(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "hello")
	// Move left twice so the cursor sits mid-string ("hel|lo") — if Ctrl+F
	// leaks to the textarea it would move right again.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m2 := updated.(Model)
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m3 := updated.(Model)

	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	m4 := updated.(Model)

	// Cursor is at "hel|lo" (position 3) after the two Lefts. If Ctrl+F
	// leaked to the textarea's CharacterForward, the cursor advances to
	// "hell|o" (position 4) and typing 'X' produces "hellXo". If correctly
	// consumed (cursor stays at position 3), typing 'X' produces "helXlo".
	updated, _ = m4.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m5 := updated.(Model)
	if v := m5.input.Value(); v != "helXlo" {
		t.Fatalf("Ctrl+F moved the compose cursor forward: got %q, want %q", v, "helXlo")
	}
}

func TestUpdate_AltCDoesNotCapitalizeInput(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "hello")
	// lastResponseText stays EMPTY on purpose: a non-empty value makes the
	// Alt+C handler run copyViaOSC52 — an OSC 52 escape written to stderr
	// that silently OVERWRITES the developer's system clipboard when the
	// test runs in an OSC52-capable terminal (iTerm2/kitty). The collision
	// assertion doesn't need it — the handler's return value (and pre-fix
	// fall-through) is the same either way.

	// CapitalizeWordForward acts on the word AT OR AFTER the cursor — with
	// the cursor at the end of "hello" (right after typing) there's no
	// forward word to capitalize, so the collision wouldn't be observable.
	// Move the cursor to the start first (Home) so the whole word is ahead
	// of it.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Alt: true, Runes: []rune{'c'}})
	m2 := updated.(Model)

	if v := m2.input.Value(); v != "hello" {
		t.Fatalf("Alt+C corrupted the input (capitalized a word): got %q, want %q", v, "hello")
	}
}

// TestUpdate_CtrlKWithNoModelsDoesNotDeleteAfterCursor pins the narrower,
// edge-case collision: the old openModelSelector() early-return left m.state
// at StateInput when availableModels was empty, so Ctrl+K fell through to
// textarea's DeleteAfterCursor. The recoverable empty selector now consumes
// the chord while preserving the draft.
func TestUpdate_CtrlKWithNoModelsDoesNotDeleteAfterCursor(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m.availableModels = nil
	m = typeText(t, m, "hello")
	// Move left twice so the cursor sits mid-string ("hel|lo") — DeleteAfterCursor
	// would remove "lo" if the key leaks through.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m2 := updated.(Model)
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m3 := updated.(Model)

	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m4 := updated.(Model)

	if m4.state != StateModelSelector {
		t.Fatalf("with no models loaded, state should show the empty selector, got %v", m4.state)
	}
	if v := m4.input.Value(); v != "hello" {
		t.Fatalf("Ctrl+K with no models deleted text after the cursor: got %q, want %q", v, "hello")
	}
}

// TestUpdate_CtrlUEmptyInputScrollDoesNotTouchInput pins the round-8
// consistency change: Ctrl+U with an EMPTY input is the scroll-half-page-up
// shortcut and must be fully consumed (textarea's ctrl+u DeleteBeforeCursor
// was harmless on an empty buffer, but the key must not leak). The NON-empty
// case is deliberately NOT consumed — textarea's DeleteBeforeCursor IS the
// documented "Ctrl+U clears the input" behavior — pinned here too so a
// future blanket-consume sweep doesn't break it.
func TestUpdate_CtrlUEmptyInputScrollDoesNotTouchInput(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput

	// Empty input: Ctrl+U scrolls; typing afterwards must start fresh.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m2 := updated.(Model)
	m2 = typeText(t, m2, "abc")
	if v := m2.input.Value(); v != "abc" {
		t.Fatalf("Ctrl+U on empty input corrupted subsequent typing: got %q, want %q", v, "abc")
	}

	// Non-empty input: Ctrl+U must still clear it (the deliberate
	// fall-through to textarea's DeleteBeforeCursor).
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	m3 := updated.(Model)
	if v := m3.input.Value(); v != "" {
		t.Fatalf("Ctrl+U with text should clear the input via the textarea fall-through: got %q", v)
	}
}

// TestUpdate_CtrlJInsertsExactlyOneNewline guards the OTHER direction of the
// collision class: Ctrl+J's handler inserts the newline explicitly and then
// consumes the key. A future textarea ctrl+j binding therefore cannot add a
// second newline through Update's fall-through path.
func TestUpdate_CtrlJInsertsExactlyOneNewline(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "ab")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m2 := updated.(Model)
	// Assert on the RAW textarea value — InputModel.Value() TrimSpaces, which
	// would hide both a missing newline and a doubled one at the end.
	if v := m2.input.textarea.Value(); v != "ab\n" {
		t.Fatalf("Ctrl+J should insert exactly one newline: got %q, want %q", v, "ab\n")
	}
}

// TestUpdate_ConsumedGlobalActionsPreserveDraftCursor pins shortcuts that
// currently happen not to collide with textarea's DefaultKeyMap. They still
// perform global actions, so forwarding them is an unstable contract: a
// dependency upgrade could silently mutate the draft or cursor. Typing after
// each shortcut proves the compose cursor remains at "hel|lo".
func TestUpdate_ConsumedGlobalActionsPreserveDraftCursor(t *testing.T) {
	tests := []struct {
		name   string
		key    tea.KeyMsg
		setup  func(*Model)
		verify func(*testing.T, *Model)
	}{
		{
			name: "Ctrl+S",
			key:  tea.KeyMsg{Type: tea.KeyCtrlS},
			verify: func(t *testing.T, m *Model) {
				if !activeToastContains(m, "Settings are unavailable") {
					t.Fatal("Ctrl+S did not execute the settings action")
				}
			},
		},
		{
			name: "Ctrl+X",
			key:  tea.KeyMsg{Type: tea.KeyCtrlX},
			verify: func(t *testing.T, m *Model) {
				if !activeToastContains(m, "No plan panel") {
					t.Fatal("Ctrl+X did not explain why the plan panel is unavailable")
				}
			},
		},
		{
			name: "Ctrl+G",
			key:  tea.KeyMsg{Type: tea.KeyCtrlG},
			verify: func(t *testing.T, m *Model) {
				if !m.output.IsFrozen() {
					t.Fatal("Ctrl+G did not toggle output freeze")
				}
			},
		},
		{
			name: "Ctrl+L",
			key:  tea.KeyMsg{Type: tea.KeyCtrlL},
			setup: func(m *Model) {
				m.output.AppendLine("content")
			},
			verify: func(t *testing.T, m *Model) {
				if !activeToastContains(m, "Output cleared") {
					t.Fatal("Ctrl+L did not execute clear-screen feedback")
				}
			},
		},
		{
			name: "Shift+Tab",
			key:  tea.KeyMsg{Type: tea.KeyShiftTab},
			verify: func(t *testing.T, m *Model) {
				if !activeToastContains(m, "Session mode switching is unavailable") {
					t.Fatal("Shift+Tab did not explain why session modes are unavailable")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := *NewModel()
			m.width = 80
			m.state = StateInput
			if tt.setup != nil {
				tt.setup(&m)
			}
			m = typeText(t, m, "hello")
			for range 2 {
				updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
				m = updated.(Model)
			}

			updated, _ := m.Update(tt.key)
			m = updated.(Model)
			if tt.verify != nil {
				tt.verify(t, &m)
			}

			m = typeText(t, m, "X")
			if got := m.input.textarea.Value(); got != "helXlo" {
				t.Fatalf("shortcut moved or mutated compose draft: got %q, want %q", got, "helXlo")
			}
		})
	}
}

// TestUpdate_CtrlKWithModelsOpensSelectorAndConsumesKey confirms the
// mainline (non-edge-case) behavior is unaffected by the keyConsumed switch.
func TestUpdate_CtrlKWithModelsOpensSelectorAndConsumesKey(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m.availableModels = []ModelInfo{{ID: "test-model", Name: "Test Model"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m2 := updated.(Model)

	if m2.state != StateModelSelector {
		t.Fatalf("Ctrl+K with models loaded should open the model selector, state = %v", m2.state)
	}
}

// --- KeyEnter members of the same collision class (round 8, follow-up
// adversarial review finding). bubbles/textarea's DefaultKeyMap binds
// "enter" to InsertNewline, and NewInputModel never disables it — so every
// KeyEnter path in handleGlobalKeys that returned bare nil while leaving the
// model in a typeAheadActive state let the SAME Enter keystroke ALSO insert
// a newline into the compose textarea. The submit paths leaked an invisible
// "\n" into the just-Reset input (InputModel.Value() TrimSpaces it away, so
// it was never SEEN — but the per-keystroke slash-suggestion refresh checks
// strings.HasPrefix on the RAW textarea value, so autocomplete silently died
// for the rest of the session after the first submit). ---

// TestUpdate_EnterSubmitDoesNotLeakNewlineIntoInput reproduces the
// user-visible symptom end-to-end: submit a message, then start typing a
// slash command — the suggestions dropdown must appear.
func TestUpdate_EnterSubmitDoesNotLeakNewlineIntoInput(t *testing.T) {
	m := *NewModel()
	m.SetCallbacks(func(string) {}, nil)
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "hello")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(Model)

	if m2.state != StateProcessing {
		t.Fatalf("Enter should submit and enter StateProcessing, state = %v", m2.state)
	}
	if v := m2.input.textarea.Value(); v != "" {
		t.Fatalf("submit leaked into the compose textarea: raw value = %q, want empty", v)
	}

	// The downstream casualty: with a leaked "\n" the raw value becomes
	// "\n/he", strings.HasPrefix(value, "/") fails, and slash autocomplete
	// never fires again this session.
	m3 := typeText(t, m2, "/he")
	if !m3.input.ShowingSuggestions() {
		t.Fatalf("slash-command suggestions dead after submit (raw input %q)", m3.input.textarea.Value())
	}
}

// TestUpdate_EmptyEnterDoesNotAccumulateNewlines: bare Enter presses on an
// empty input (no suggestions) used to fall through to the textarea and pile
// up invisible newlines.
func TestUpdate_EmptyEnterDoesNotAccumulateNewlines(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput

	for i := 0; i < 3; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
	}
	if v := m.input.textarea.Value(); v != "" {
		t.Fatalf("empty-input Enter presses accumulated newlines: raw value = %q", v)
	}
}

// TestUpdate_DebouncedEnterDoesNotInsertInteriorNewline: an Enter swallowed
// by the 500ms submit debounce must be fully consumed — pre-fix it fell
// through to the textarea and inserted a newline AT THE CURSOR, which for a
// mid-string cursor lands an INTERIOR "\n" that Value()'s TrimSpace does NOT
// hide, corrupting the message the user eventually submits.
func TestUpdate_DebouncedEnterDoesNotInsertInteriorNewline(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m.lastSubmitTime = time.Now() // as if a submit just happened
	m = typeText(t, m, "hello")
	// Move the cursor mid-string: "hel|lo".
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m2 := updated.(Model)
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m3 := updated.(Model)

	updated, _ = m3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m4 := updated.(Model)

	if m4.state != StateInput {
		t.Fatalf("debounced Enter must not submit, state = %v", m4.state)
	}
	if v := m4.input.textarea.Value(); v != "hello" {
		t.Fatalf("debounced Enter corrupted the compose buffer: raw value = %q, want %q", v, "hello")
	}
}

// TestUpdate_TypeAheadEnterDoesNotLeakNewlineIntoInput: the type-ahead
// submit path (composing while Processing/Streaming) had the same leak.
func TestUpdate_TypeAheadEnterDoesNotLeakNewlineIntoInput(t *testing.T) {
	m := *NewModel()
	m.SetCallbacks(func(string) {}, nil)
	m.width = 80
	m.state = StateProcessing
	m = typeText(t, m, "queued message")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(Model)

	if v := m2.input.textarea.Value(); v != "" {
		t.Fatalf("type-ahead submit leaked into the compose textarea: raw value = %q, want empty", v)
	}
}

// TestUpdate_EnterWithSuggestionsStillAcceptsSuggestion pins the
// LOAD-BEARING forward path the fix must NOT consume: when the suggestions
// dropdown is showing, the KeyEnter global branch is skipped (gated on
// !ShowingSuggestions) and the key forwards to InputModel, whose own
// KeyEnter case accepts the highlighted suggestion.
func TestUpdate_EnterWithSuggestionsStillAcceptsSuggestion(t *testing.T) {
	m := *NewModel()
	m.width = 80
	m.state = StateInput
	m = typeText(t, m, "/he")
	if !m.input.ShowingSuggestions() {
		t.Fatal("setup: expected suggestions to be showing for /he")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := updated.(Model)

	if m2.state != StateInput {
		t.Fatalf("Enter with suggestions showing must accept, not submit; state = %v", m2.state)
	}
	v := m2.input.Value()
	if !strings.HasPrefix(v, "/help") {
		t.Fatalf("Enter should have accepted the /help suggestion, input = %q", v)
	}
}
