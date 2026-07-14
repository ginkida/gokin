package ui

import (
	"strings"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestNotificationCenterShortcutOpensAndEscapeReturnsToComposer(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateInput
	m.toastManager.ShowError("Connection failed")

	cmd := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true})
	if cmd == nil || m.state != StateNotificationCenter {
		t.Fatalf("Alt+N should be consumed and open notification center, state=%v cmd=%v", m.state, cmd)
	}
	view := m.View()
	if strings.Count(view, "Connection failed") != 1 {
		t.Fatalf("active notification should render once inside center, got:\n%s", view)
	}
	if got := m.DebugState().State; got != "notification_center" {
		t.Fatalf("debug state=%q, want notification_center", got)
	}

	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Fatalf("Escape state=%v, want input", m.state)
	}
}

func TestNotificationCenterEmptyStateAndTextualSeverities(t *testing.T) {
	m := NewModel()
	m.width, m.height = 72, 24
	m.openNotificationCenter()

	empty := m.renderNotificationCenter()
	for _, want := range []string{"Notifications", "0 active · 0 earlier", "No notifications yet.", "Esc Back"} {
		if !strings.Contains(empty, want) {
			t.Fatalf("empty center missing %q:\n%s", want, empty)
		}
	}

	m.toastManager.ShowError("Failed")
	m.toastManager.ShowWarning("Needs attention")
	m.toastManager.ShowSuccess("Saved")
	m.toastManager.ShowInfo("Connected")
	activeOnly := stripAnsi(m.renderNotificationCenter())
	if strings.Contains(activeOnly, "Clear") {
		t.Fatalf("active-only center advertises unavailable history action:\n%s", activeOnly)
	}
	active := m.toastManager.Active()
	m.toastManager.Dismiss(active[len(active)-1].ID)

	view := m.renderNotificationCenter()
	for _, want := range []string{"3 active · 1 earlier", "ERROR", "WARN", "DONE", "INFO", "now"} {
		if !strings.Contains(view, want) {
			t.Fatalf("notification center missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(stripAnsi(view), "Clear") {
		t.Fatalf("center with history should advertise clear action:\n%s", view)
	}
}

func TestNotificationCenterPagesAndClearsOnlyEarlierItems(t *testing.T) {
	m := NewModel()
	m.width, m.height = 70, 12
	m.toastManager.maxToasts = 1
	for i := 0; i < 8; i++ {
		m.toastManager.Show(ToastInfo, "", strings.Repeat("x", i+1), time.Minute)
	}
	m.openNotificationCenter()

	initial := m.renderNotificationCenter()
	if !strings.Contains(initial, "older") {
		t.Fatalf("paged center should advertise older rows:\n%s", initial)
	}
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.notificationScroll == 0 || !strings.Contains(m.renderNotificationCenter(), "newer") {
		t.Fatalf("End should move to oldest page, scroll=%d", m.notificationScroll)
	}

	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if got := len(m.toastManager.History()); got != 0 {
		t.Fatalf("history length=%d after clear, want 0", got)
	}
	if got := m.toastManager.Count(); got != 1 {
		t.Fatalf("active count=%d after clear, want 1", got)
	}
	view := m.renderNotificationCenter()
	for _, want := range []string{"1 active · 0 earlier", "Earlier notifications cleared"} {
		if !strings.Contains(view, want) {
			t.Fatalf("cleared center missing %q:\n%s", want, view)
		}
	}
}

func TestNotificationCenterSanitizesPayloadAndFitsNarrowWidth(t *testing.T) {
	m := NewModel()
	m.width, m.height = 24, 10
	m.toastManager.Show(ToastError, "\x1b[31mBad\nTitle", "wide 界\tmessage\x00 tail", time.Minute)
	m.openNotificationCenter()

	view := m.renderNotificationCenter()
	if strings.Contains(view, "\x1b[31m") || strings.ContainsRune(view, '\x00') {
		t.Fatalf("center leaked payload controls: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > m.width {
			t.Fatalf("line width=%d exceeds terminal width=%d: %q", lipgloss.Width(line), m.width, line)
		}
		for _, r := range line {
			if unicode.IsControl(r) && r != '\t' {
				t.Fatalf("line contains control rune %U: %q", r, line)
			}
		}
	}
}

func TestNotificationCenterPaletteActionAndShortcutAreRegistered(t *testing.T) {
	m := NewModel()
	m.RegisterPaletteActions()
	m.commandPalette.Show()
	m.commandPalette.SetQuery("notification history")
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateNotificationCenter {
		t.Fatalf("palette notification action state=%v, want notification center", m.state)
	}

	found := false
	for _, category := range DefaultShortcuts() {
		for _, shortcut := range category.Shortcuts {
			if strings.Join(shortcut.Keys, "+") == "Alt+N" && strings.Contains(strings.ToLower(shortcut.Description), "notification") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("Alt+N notification shortcut is not discoverable")
	}
}

func TestNotificationCenterSelectionOpensFullDetailsAndEscapeReturnsToList(t *testing.T) {
	m := NewModel()
	m.width, m.height = 52, 40
	m.toastManager.ShowWarning("First warning")
	longMessage := "A long diagnostic with the recovery instruction: reconnect the provider, verify credentials, and retry the request after the connection is healthy."
	m.toastManager.Show(ToastError, "Request failed", longMessage, time.Minute)
	m.openNotificationCenter()

	list := stripAnsi(m.renderNotificationCenter())
	if !strings.Contains(list, "› ERROR") || !strings.Contains(list, "Enter Details") {
		t.Fatalf("list should expose selected row and detail action:\n%s", list)
	}
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.notificationSelected != 1 {
		t.Fatalf("selected=%d after Down, want 1", m.notificationSelected)
	}
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyUp})
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.notificationDetail || m.state != StateNotificationCenter {
		t.Fatalf("Enter should open details inside center, detail=%v state=%v", m.notificationDetail, m.state)
	}
	detail := stripAnsi(m.renderNotificationCenter())
	for _, want := range []string{"Notification details", "ERROR · active · now · 1 of 2", "Request failed", "reconnect the provider", "connection is healthy", "Esc Back to list"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail view missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "Scroll") || strings.Contains(detail, "PgUp") {
		t.Fatalf("short detail advertises unavailable scrolling:\n%s", detail)
	}

	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.notificationDetail || m.state != StateNotificationCenter {
		t.Fatalf("first Escape should return to list, detail=%v state=%v", m.notificationDetail, m.state)
	}
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Fatalf("second Escape state=%v, want input", m.state)
	}
}

func TestNotificationDetailPagesLongContentAndFitsNarrowWidth(t *testing.T) {
	m := NewModel()
	m.width, m.height = 28, 12
	message := strings.Repeat("diagnostic segment with recovery step ", 12) + "FINAL-INSTRUCTION"
	m.toastManager.Show(ToastError, "Failure details with a complete title tail", message, time.Minute)
	m.openNotificationCenter()
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})

	first := stripAnsi(m.renderNotificationCenter())
	if !strings.Contains(first, "lines below") {
		t.Fatalf("long detail should advertise hidden content:\n%s", first)
	}
	if !strings.Contains(first, "Scroll") {
		t.Fatalf("long detail should advertise scrolling:\n%s", first)
	}
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.notificationDetailScroll == 0 {
		t.Fatal("End should move detail viewport to the final page")
	}
	last := stripAnsi(m.renderNotificationCenter())
	if !strings.Contains(last, "lines above") || !strings.Contains(last, "FINAL-INSTRUCTION") {
		t.Fatalf("final detail page should expose the recovery tail:\n%s", last)
	}
	lines := notificationDetailLines(m.notificationRows()[0], notificationContentWidth(m.width))
	if got := strings.Join(lines, " "); !strings.Contains(got, "complete title tail") {
		t.Fatalf("wrapped detail lines lost the title tail: %q", got)
	}
	for _, line := range strings.Split(m.renderNotificationCenter(), "\n") {
		if got := lipgloss.Width(line); got > m.width {
			t.Fatalf("detail row width=%d exceeds terminal width=%d: %q", got, m.width, stripAnsi(line))
		}
	}

	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyHome})
	if m.notificationDetailScroll != 0 {
		t.Fatalf("Home detail scroll=%d, want 0", m.notificationDetailScroll)
	}
}

func TestNotificationSelectionClampsWhenHistoryIsCleared(t *testing.T) {
	m := NewModel()
	m.width, m.height = 70, 14
	m.toastManager.maxToasts = 1
	for _, message := range []string{"old one", "old two", "active"} {
		m.toastManager.Show(ToastInfo, "", message, time.Minute)
	}
	m.openNotificationCenter()
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.notificationSelected != 2 {
		t.Fatalf("selected=%d before clear, want oldest row", m.notificationSelected)
	}
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.notificationSelected != 0 || m.notificationScroll != 0 {
		t.Fatalf("selection not clamped after clear: selected=%d scroll=%d", m.notificationSelected, m.notificationScroll)
	}
	if got := stripAnsi(m.renderNotificationCenter()); !strings.Contains(got, "› INFO") || !strings.Contains(got, "active") {
		t.Fatalf("remaining active notification should stay selected:\n%s", got)
	}
}

func TestNotificationSelectionFollowsToastAcrossActiveHistoryReorder(t *testing.T) {
	m := NewModel()
	m.width, m.height = 70, 18
	m.toastManager.Show(ToastInfo, "", "stays active", time.Minute)
	m.toastManager.Show(ToastWarning, "", "expires while selected", time.Millisecond)
	m.openNotificationCenter()

	rows := m.notificationRows()
	if len(rows) != 2 || rows[0].toast.Message != "expires while selected" {
		t.Fatalf("unexpected initial rows=%+v", rows)
	}
	selectedID := rows[0].toast.ID
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.notificationDetail || m.notificationSelectedID != selectedID {
		t.Fatalf("detail did not retain selected toast: detail=%v id=%d want=%d", m.notificationDetail, m.notificationSelectedID, selectedID)
	}

	// Expire only the selected toast. It moves behind the still-active row,
	// which used to make index 0 display an unrelated notification.
	for i := range m.toastManager.toasts {
		if m.toastManager.toasts[i].ID == selectedID {
			m.toastManager.toasts[i].CreatedAt = time.Now().Add(-time.Minute)
		}
	}
	m.toastManager.Update()

	rows = m.notificationRows()
	if got := selectedNotificationIndex(rows, m.notificationSelected, m.notificationSelectedID); got != 1 {
		t.Fatalf("selected toast index=%d after reorder, want 1; rows=%+v", got, rows)
	}
	detail := stripAnsi(m.renderNotificationCenter())
	for _, want := range []string{"expires while selected", "WARN · earlier", "2 of 2"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("reordered detail lost %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "stays active") {
		t.Fatalf("detail jumped to a different notification:\n%s", detail)
	}

	// The next navigation event synchronizes the stored array index too.
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEscape})
	_ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyHome})
	if m.notificationSelected != 0 || m.notificationSelectedID == selectedID {
		t.Fatalf("Home did not select the first logical row: index=%d id=%d", m.notificationSelected, m.notificationSelectedID)
	}
}
