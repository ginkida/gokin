package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func notificationCenterWithEarlierRow(width, height int) *Model {
	m := NewModel()
	m.toastManager.history = []Toast{{
		ID:        1,
		Type:      ToastWarning,
		Message:   "distinct-target",
		CreatedAt: time.Now(),
	}}
	m.openNotificationCenter()
	m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestNotificationDetailsRequireReadableTargetAndVisibleAction(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{width: 27, height: 12}, // too narrow to distinguish the selected message
		{width: 28, height: 3},  // borderless target is cropped above recovery
		{width: 50, height: 4},  // bordered target is cropped above its bottom border
	} {
		m := notificationCenterWithEarlierRow(size.width, size.height)
		view := stripAnsi(m.View())
		if !strings.Contains(view, "Resize") || !strings.Contains(view, "Esc") {
			t.Fatalf("%dx%d hidden notification target lost resize/recovery guidance:\n%s", size.width, size.height, view)
		}
		if strings.Contains(view, "Enter") {
			t.Fatalf("%dx%d hidden notification target advertised Details:\n%s", size.width, size.height, view)
		}
		hints := plainShortcutHints(m.contextualShortcutHintPairs())
		if !strings.Contains(hints, "Resize") || !strings.Contains(hints, "esc Back") || strings.Contains(hints, "Enter") {
			t.Fatalf("%dx%d hidden notification contextual hints=%q", size.width, size.height, hints)
		}

		_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if m.notificationDetail {
			t.Fatalf("%dx%d Enter opened details for a hidden target", size.width, size.height)
		}
	}
}

func TestNotificationDetailsReadableGeometryKeepsPrimaryBeforeClear(t *testing.T) {
	for _, size := range []struct {
		width, height int
		target        string
	}{
		{width: 28, height: 4, target: "di"},
		{width: 50, height: 5, target: "distinct-target"},
	} {
		m := notificationCenterWithEarlierRow(size.width, size.height)
		view := stripAnsi(m.View())
		for _, want := range []string{size.target, "Enter", "Esc"} {
			if !strings.Contains(view, want) {
				t.Fatalf("%dx%d readable notification surface lost %q:\n%s", size.width, size.height, want, view)
			}
		}
		if size.width == 28 && strings.Contains(view, "Clear") {
			t.Fatalf("%dx%d lower-priority Clear displaced compact Details/recovery:\n%s", size.width, size.height, view)
		}

		_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if !m.notificationDetail {
			t.Fatalf("%dx%d visible Details action was blocked", size.width, size.height)
		}
	}

	// A failed attempt to use the deliberately hidden lower-priority Clear key
	// adds a notice row. That row changes the real geometry, so Details must
	// fail closed until the selected target and action are visible together.
	withNotice := notificationCenterWithEarlierRow(28, 4)
	_ = withNotice.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	view := stripAnsi(withNotice.View())
	if !strings.Contains(view, "Resize") || strings.Contains(view, "Enter") {
		t.Fatalf("notice-cropped notification target kept a primary action:\n%s", view)
	}
	_ = withNotice.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if withNotice.notificationDetail {
		t.Fatal("Enter opened details after a notice row cropped the selected target")
	}

	withOlder := notificationCenterWithEarlierRow(28, 4)
	withOlder.toastManager.history = append(withOlder.toastManager.history, Toast{
		ID: 2, Type: ToastInfo, Message: "older-target", CreatedAt: time.Now().Add(-time.Minute),
	})
	view = stripAnsi(withOlder.View())
	if !strings.Contains(view, "Resize") || strings.Contains(view, "Enter") {
		t.Fatalf("trailing older disclosure did not fail closed:\n%s", view)
	}
	_ = withOlder.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if withOlder.notificationDetail {
		t.Fatal("Enter opened details after the older disclosure cropped its target")
	}
	withOlder.applyResize(&tea.WindowSizeMsg{Width: 28, Height: 5})
	if view = stripAnsi(withOlder.View()); !strings.Contains(view, "Enter") || !strings.Contains(view, "di") {
		t.Fatalf("room for target plus older disclosure remained over-blocked:\n%s", view)
	}

	// Zero dimensions mean no WindowSizeMsg has arrived, not a one-cell
	// terminal. Preserve the headless/embedding contract.
	sentinel := notificationCenterWithEarlierRow(0, 0)
	_ = sentinel.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !sentinel.notificationDetail {
		t.Fatal("unspecified notification geometry was incorrectly gated")
	}
}

func TestAutoCompactPlanCtrlXDoesNotMutateFutureDensity(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 15})
	m.planProgressPanel.StartPlan("plan", "Migration", "", planSteps(3))
	if view := stripAnsi(m.planProgressPanel.View(m.width, m.height)); !strings.Contains(view, "resize to expand") || strings.Contains(view, "Ctrl+X to expand") {
		t.Fatalf("setup is not height-driven compact mode:\n%s", view)
	}

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlX})
	if m.planProgressPanel.collapsed {
		t.Fatal("ineffective Ctrl+X silently changed collapsed state before resize")
	}
	if !toastHistoryContains(m.toastManager.Active(), "Resize") {
		t.Fatalf("ineffective Ctrl+X gave no resize feedback: %+v", m.toastManager.Active())
	}
	m.RegisterPaletteActions()
	m.RefreshPaletteCommands()
	shortAction := paletteActionForTest(t, m, paletteActionPlanPanel)
	if shortAction.Enabled || !strings.Contains(strings.ToLower(shortAction.Reason), "terminal too small") {
		t.Fatalf("short-terminal palette action remained runnable: enabled=%v reason=%q", shortAction.Enabled, shortAction.Reason)
	}

	// At the exact readable boundary Ctrl+X becomes a real, visible density
	// toggle again.
	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 16})
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !m.planProgressPanel.collapsed {
		t.Fatal("readable-height Ctrl+X did not collapse the expanded plan panel")
	}

	narrow := NewModel()
	narrow.state = StateProcessing
	narrow.applyResize(&tea.WindowSizeMsg{Width: planPanelDensityMinWidth - 1, Height: planPanelExpandedMinHeight})
	narrow.planProgressPanel.StartPlan("plan", "Migration", "", planSteps(3))
	if view := stripAnsi(narrow.View()); strings.Contains(view, "Ctrl+X") {
		t.Fatalf("narrow plan audit setup unexpectedly exposed Ctrl+X:\n%s", view)
	}
	_ = narrow.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlX})
	if narrow.planProgressPanel.collapsed {
		t.Fatal("Ctrl+X changed plan density while its key was horizontally hidden")
	}
	narrow.applyResize(&tea.WindowSizeMsg{Width: planPanelDensityMinWidth, Height: planPanelExpandedMinHeight})
	if view := stripAnsi(narrow.View()); !strings.Contains(view, "Ctrl+X") {
		t.Fatalf("exact readable-width plan frame lost Ctrl+X:\n%s", view)
	}
	_ = narrow.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !narrow.planProgressPanel.collapsed {
		t.Fatal("exact readable-width Ctrl+X boundary was over-blocked")
	}

	// As elsewhere in the UI, height 0 is an unspecified-geometry sentinel.
	sentinel := NewModel()
	sentinel.state = StateProcessing
	sentinel.planProgressPanel.StartPlan("plan", "Migration", "", planSteps(3))
	_ = sentinel.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlX})
	if !sentinel.planProgressPanel.collapsed {
		t.Fatal("unspecified plan geometry was incorrectly gated")
	}

	autoCollapsed := NewPlanProgressPanel(DefaultStyles())
	autoCollapsed.StartPlan("plan", "Migration", "", planSteps(planAutoCollapseSteps+1))
	if view := stripAnsi(autoCollapsed.View(60, planPanelExpandedMinHeight-1)); !strings.Contains(view, "resize to expand") || strings.Contains(view, "Ctrl+X to expand") {
		t.Fatalf("short auto-collapsed plan advertised an ineffective toggle:\n%s", view)
	}
}

func toastHistoryContains(toasts []Toast, needle string) bool {
	for _, toast := range toasts {
		if strings.Contains(toast.Message, needle) {
			return true
		}
	}
	return false
}
