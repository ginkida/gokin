package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestKeyEntryModal_SubmitAndCancel: open enters the modal, typed runes land in
// the (masked) field, Enter submits the real key via the callback, Esc cancels
// without submitting. The key value is never the masked form — masking is
// display-only.
func TestKeyEntryModal_SubmitAndCancel(t *testing.T) {
	m := NewModel()

	var gotProvider, gotKey string
	var calls int
	m.SetKeyEntrySubmitCallback(func(provider, key string) { gotProvider = provider; gotKey = key; calls++ })

	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM", SetupURL: "https://z.ai"})
	if m.state != StateAPIKeyEntry {
		t.Fatal("openKeyEntry should enter StateAPIKeyEntry")
	}

	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-secret-123456")})
	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})

	if calls != 1 || gotProvider != "glm" || gotKey != "sk-secret-123456" {
		t.Errorf("submit: calls=%d provider=%q key=%q, want 1/glm/sk-secret-123456", calls, gotProvider, gotKey)
	}
	if m.state != StateInput {
		t.Errorf("submit should return to StateInput, got %v", m.state)
	}
	if m.keyEntryInput.Value() != "" || m.keyEntryProvider != "" || m.keyEntryDisplayName != "" {
		t.Fatal("submitted secret metadata remained reachable after close")
	}
	if m.keyEntryPendingProvider != "glm" {
		t.Fatalf("submission does not expose pending provider: %q", m.keyEntryPendingProvider)
	}
	_ = m.handleMessageTypes(KeyEntryResultMsg{Provider: "glm", Success: true, Message: "GLM API key saved"})
	if m.keyEntryPendingProvider != "" || m.toastManager.Count() != 1 || m.toastManager.toasts[0].Type != ToastSuccess {
		t.Fatalf("success did not resolve pending login: pending=%q toasts=%+v", m.keyEntryPendingProvider, m.toastManager.toasts)
	}

	// Re-open, type, then Esc — must NOT submit.
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm"})
	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("partial")})
	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if calls != 1 {
		t.Errorf("esc must not submit a key; callback calls=%d", calls)
	}
	if m.state != StateInput {
		t.Errorf("esc should return to StateInput, got %v", m.state)
	}
}

func TestKeyEntryFailureReopensEmptyRetryWithoutSecret(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetKeyEntrySubmitCallback(func(string, string) {})
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM", SetupURL: "https://z.ai/keys"})
	secret := "sk-secret-never-restored"
	m.keyEntryInput.SetValue(secret)
	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})

	// A result for another provider cannot steal or resolve this attempt.
	_ = m.handleMessageTypes(KeyEntryResultMsg{Provider: "kimi", Success: true, Message: "saved"})
	if m.keyEntryPendingProvider != "glm" || m.state != StateInput {
		t.Fatalf("stale result changed login lifecycle: pending=%q state=%v", m.keyEntryPendingProvider, m.state)
	}

	reason := "Invalid API key format (too short)."
	_ = m.handleMessageTypes(KeyEntryResultMsg{Provider: "glm", Message: reason})
	if m.state != StateAPIKeyEntry || m.keyEntryPendingProvider != "" {
		t.Fatalf("failure did not reopen retry: state=%v pending=%q", m.state, m.keyEntryPendingProvider)
	}
	if m.keyEntryInput.Value() != "" || strings.Contains(m.renderKeyEntry(), secret) {
		t.Fatal("failed login restored or rendered the submitted secret")
	}
	view := stripAnsi(m.renderKeyEntry())
	for _, want := range []string{"Invalid API key format", "https://z.ai/keys", "Enter Save"} {
		if !strings.Contains(view, want) {
			t.Fatalf("retry modal missing %q:\n%s", want, view)
		}
	}
	if m.toastManager.Count() != 1 || m.toastManager.toasts[0].Type != ToastError {
		t.Fatalf("failure toast=%+v", m.toastManager.toasts)
	}
	if output := stripAnsi(m.output.Content()); !strings.Contains(output, reason) {
		t.Fatalf("failure reason is not durable: %q", output)
	}
}

func TestKeyEntryModalRejectsEmptyAndUnavailableSubmission(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.SetKeyEntrySubmitCallback(func(string, string) {})
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM"})

	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateAPIKeyEntry {
		t.Fatalf("empty Enter closed modal: state=%v", m.state)
	}
	if got := stripAnsi(m.renderKeyEntry()); !strings.Contains(got, "API key cannot be empty") {
		t.Fatalf("empty-key validation missing:\n%s", got)
	}
	_ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.keyEntryError != "" {
		t.Fatalf("editing should clear stale validation, got %q", m.keyEntryError)
	}

	unavailable := NewModel()
	unavailable.width = 80
	unavailable.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM"})
	if unavailable.keyEntryInput.Focused() {
		t.Fatal("unavailable login must not focus a secret field")
	}
	_ = unavailable.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("secret")})
	if unavailable.keyEntryInput.Value() != "" {
		t.Fatal("unavailable login captured a secret")
	}
	if got := stripAnsi(unavailable.renderKeyEntry()); !strings.Contains(got, "Login is unavailable") || !strings.Contains(got, "No key was captured") {
		t.Fatalf("unavailable-login state missing:\n%s", got)
	}
}

func TestKeyEntryModalMasksSecretAndFitsCard(t *testing.T) {
	for _, width := range []int{20, 50, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := NewModel()
			m.width = width
			m.openKeyEntry(OpenKeyEntryMsg{
				Provider:    "glm",
				DisplayName: "A Provider With An Extremely Long Display Name",
				SetupURL:    "https://example.com/account/api-keys",
			})
			secret := "sk-do-not-render-this-secret"
			m.keyEntryInput.SetValue(secret)

			view := m.renderKeyEntry()
			if strings.Contains(view, secret) || strings.Contains(stripAnsi(view), secret) {
				t.Fatal("raw secret appeared in rendered modal")
			}
			for row, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("row %d width=%d exceeds terminal width=%d:\n%s", row, got, width, stripAnsi(view))
				}
			}
		})
	}
}

func TestKeyEntryModalResizeTracksCardWidth(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm"})
	if got, want := m.keyEntryInput.Width, keyEntryInputWidth(100); got != want {
		t.Fatalf("initial input width=%d, want %d", got, want)
	}

	m.applyResize(&tea.WindowSizeMsg{Width: 40, Height: 20})
	if got, want := m.keyEntryInput.Width, keyEntryInputWidth(40); got != want {
		t.Fatalf("resized input width=%d, want %d", got, want)
	}
}
