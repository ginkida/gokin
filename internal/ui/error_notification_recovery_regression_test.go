package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCompactErrorHintPrefersExplicitRecoveryCommand(t *testing.T) {
	const raw = "HTTP 401 unauthorized: invalid API key"
	if got := GetCompactHint(raw); got != "Try: /login" {
		t.Fatalf("compact auth hint=%q, want the usable recovery command", got)
	}

	manager := NewToastManager(DefaultStyles())
	manager.ShowErrorWithHint(raw)
	active := manager.Active()
	if len(active) != 1 {
		t.Fatalf("active toasts=%d, want 1", len(active))
	}
	if got := active[0].Message; !strings.Contains(got, "→ Try: /login") || strings.Contains(got, "<key>") {
		t.Fatalf("auth toast should expose masked-login recovery without secret-in-command syntax: %q", got)
	}
}

func TestErrorRecoveryCopyUsesCurrentSafeEntryPoints(t *testing.T) {
	auth := GetErrorGuidance("401 unauthorized")
	if auth == nil {
		t.Fatal("missing auth guidance")
	}
	authCopy := strings.Join(auth.Suggestions, " ")
	if !strings.Contains(authCopy, "masked") || strings.Contains(authCopy, "<key>") {
		t.Fatalf("auth guidance should route secrets through the masked prompt: %q", authCopy)
	}

	file := GetErrorGuidance("open config: no such file")
	if file == nil || file.Command != "/browse" {
		t.Fatalf("file guidance=%+v, want /browse recovery", file)
	}
	if copy := strings.Join(file.Suggestions, " "); strings.Contains(copy, "/glob") {
		t.Fatalf("file guidance references an unregistered slash command: %q", copy)
	}

	classifiedFile := ClassifyError(errors.New("open config: no such file or directory"), "loading config")
	if classifiedFile == nil || classifiedFile.Category != ErrorCategoryFile {
		t.Fatalf("classified file error=%+v", classifiedFile)
	}
	if copy := strings.Join(classifiedFile.Suggestions, " "); !strings.Contains(copy, "/browse") || strings.Contains(copy, "/glob") {
		t.Fatalf("classified file recovery is stale: %q", copy)
	}

	classifiedConfig := ClassifyError(errors.New("configuration is invalid"), "startup")
	if copy := strings.Join(classifiedConfig.Suggestions, " "); !strings.Contains(copy, "/settings") || strings.Contains(copy, "Run /setup") {
		t.Fatalf("configuration recovery is stale: %q", copy)
	}
}

func TestClassifyErrorAvoidsTelemetryAndGenericNotFoundFalsePositives(t *testing.T) {
	cases := []struct {
		message string
		not     ErrorCategory
	}{
		{"request completed in 401ms", ErrorCategoryAuth},
		{"backoff lasted 429ms", ErrorCategoryRateLimit},
		{"provider latency was 500ms", ErrorCategoryAPI},
		{"rapid retry failed", ErrorCategoryAPI},
		{"model not found", ErrorCategoryFile},
	}
	for _, tc := range cases {
		got := ClassifyError(errors.New(tc.message), "")
		if got == nil {
			t.Fatalf("ClassifyError(%q)=nil", tc.message)
		}
		if got.Category == tc.not {
			t.Errorf("ClassifyError(%q)=%q, false-positive category", tc.message, got.Category)
		}
	}

	auth := ClassifyError(errors.New("provider rejected an invalid API key"), "login")
	if auth == nil || auth.Category != ErrorCategoryAuth {
		t.Fatalf("plain invalid-API-key wording classified as %+v", auth)
	}
	if len(auth.Suggestions) == 0 || !strings.Contains(auth.Suggestions[0], "masked") {
		t.Fatalf("compact auth recovery does not lead with the masked flow: %v", auth.Suggestions)
	}
}

func TestFailureBodyPreservesRecoveryTailWhenCapped(t *testing.T) {
	detail := strings.Join([]string{
		"request failed",
		"provider returned an error",
		"stack frame one",
		"stack frame two",
		"stack frame three",
		"credentials can be refreshed",
		"run /login to recover",
	}, "\n")
	plain := stripAnsi(failureBody(detail, "read"))
	for _, want := range []string{"request failed", "provider returned an error", "⋯ 3 more lines", "credentials can be refreshed", "run /login to recover"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("capped failure body lost %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "stack frame") {
		t.Fatalf("capped failure body retained hidden middle rows:\n%s", plain)
	}
	if marker, tail := strings.Index(plain, "⋯ 3 more lines"), strings.Index(plain, "credentials can be refreshed"); marker < 0 || tail < marker {
		t.Fatalf("omission marker should sit between cause and recovery tail:\n%s", plain)
	}
}

func TestDismissTagDiscardsCancelledTransientGuidance(t *testing.T) {
	manager := NewToastManager(DefaultStyles())
	manager.ShowTagged(" quit-confirm\x1b[2J ", ToastWarning, "Press Ctrl+C again to quit", time.Minute)
	manager.DismissTag(" quit-confirm\x1b[2J ")

	if manager.Count() != 0 {
		t.Fatalf("cancelled tagged toast remained active: %+v", manager.Active())
	}
	if history := manager.History(); len(history) != 0 {
		t.Fatalf("cancelled lifecycle guidance leaked into notification history: %+v", history)
	}
}

func TestNotificationNoticeTracksResizeAndNewHistory(t *testing.T) {
	t.Run("resize recovery clears when action becomes visible", func(t *testing.T) {
		m := NewModel()
		m.width, m.height = 24, 10
		m.toastManager.ShowInfo("earlier event")
		m.toastManager.Dismiss(m.toastManager.Active()[0].ID)
		m.openNotificationCenter()

		_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		if got := stripAnsi(m.renderNotificationCenter()); !strings.Contains(got, "Resize to clear earlier notifications") {
			t.Fatalf("small layout omitted honest resize recovery:\n%s", got)
		}

		m.width, m.height = 80, 24
		resized := stripAnsi(m.renderNotificationCenter())
		if strings.Contains(resized, "Resize to clear earlier notifications") || !strings.Contains(resized, "Clear") {
			t.Fatalf("resized center retained stale notice or hid available action:\n%s", resized)
		}
		_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		if got := len(m.toastManager.History()); got != 0 {
			t.Fatalf("visible clear action left %d history rows", got)
		}
	})

	t.Run("no-history notice clears when a toast expires", func(t *testing.T) {
		m := NewModel()
		m.width, m.height = 80, 24
		m.toastManager.ShowInfo("active event")
		m.openNotificationCenter()
		_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
		if got := stripAnsi(m.renderNotificationCenter()); !strings.Contains(got, "No earlier notifications to clear") {
			t.Fatalf("missing empty-history feedback:\n%s", got)
		}

		m.toastManager.toasts[0].CreatedAt = time.Now().Add(-time.Minute)
		m.toastManager.Update()
		updated := stripAnsi(m.renderNotificationCenter())
		if strings.Contains(updated, "No earlier notifications to clear") || !strings.Contains(updated, "Clear") {
			t.Fatalf("center retained stale empty-history notice after expiry:\n%s", updated)
		}
	})
}
