package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ToastType represents the type of toast notification.
type ToastType int

const (
	ToastInfo ToastType = iota
	ToastSuccess
	ToastWarning
	ToastError
)

// Toast represents a single toast notification.
type Toast struct {
	ID        int
	Type      ToastType
	Title     string
	Message   string
	Duration  time.Duration
	CreatedAt time.Time
	FadeOut   bool
	// Tag groups related toasts. When non-empty, ShowTagged replaces the
	// existing toast with the same tag instead of stacking a new one, so
	// repeated status updates (e.g. retry attempts) collapse to a single
	// live toast rather than spamming the user.
	Tag string
}

// IsExpired returns true if the toast should be removed.
func (t *Toast) IsExpired() bool {
	return time.Since(t.CreatedAt) > t.Duration
}

const maxToastHistory = 50

// ToastManager manages toast notifications.
// All methods are only called from the Bubble Tea event loop (single goroutine),
// so no mutex is needed.
type ToastManager struct {
	toasts    []Toast
	history   []Toast // ring buffer of expired toasts
	maxToasts int
	styles    *Styles
	nextID    int
}

// NewToastManager creates a new toast manager.
func NewToastManager(styles *Styles) *ToastManager {
	return &ToastManager{
		toasts:    make([]Toast, 0),
		maxToasts: 5, // Allow enough for error bursts
		styles:    styles,
		nextID:    1,
	}
}

// Show displays a new toast notification.
func (m *ToastManager) Show(toastType ToastType, title, message string, duration time.Duration) {
	duration = normalizeToastDuration(toastType, duration)
	key := toastContentKey(toastType, title, message)
	for i := range m.toasts {
		if m.toasts[i].Tag == "" && toastContentKey(m.toasts[i].Type, m.toasts[i].Title, m.toasts[i].Message) == key {
			toast := m.toasts[i]
			toast.Type = toastType
			toast.Title = title
			toast.Message = message
			toast.Duration = duration
			toast.CreatedAt = time.Now()
			toast.FadeOut = false
			m.promoteToast(i, toast)
			return
		}
	}

	toast := Toast{
		ID:        m.nextID,
		Type:      toastType,
		Title:     title,
		Message:   message,
		Duration:  duration,
		CreatedAt: time.Now(),
		FadeOut:   false,
	}
	m.nextID++

	// Add to the beginning (newest first)
	m.toasts = append([]Toast{toast}, m.toasts...)
	m.enforceLimit()
}

// ShowSuccess displays a success toast.
func (m *ToastManager) ShowSuccess(message string) {
	m.Show(ToastSuccess, "", message, 1500*time.Millisecond)
}

// ShowError displays an error toast. Errors stay longer than info/success
// so the user has time to read them (15s vs the old 6s).
func (m *ToastManager) ShowError(message string) {
	m.Show(ToastError, "", message, 15*time.Second)
}

// ShowErrorWithHint displays an error toast with optional actionable hint.
// If a matching error pattern is found, appends hint: "Error message → Hint"
func (m *ToastManager) ShowErrorWithHint(message string) {
	hint := GetCompactHint(message)
	if hint != "" {
		message = message + " → " + hint
	}
	m.Show(ToastError, "Error", message, 15*time.Second)
}

// ShowInfo displays an info toast.
func (m *ToastManager) ShowInfo(message string) {
	m.Show(ToastInfo, "", message, 1500*time.Millisecond)
}

// ShowWarning displays a warning toast.
func (m *ToastManager) ShowWarning(message string) {
	m.Show(ToastWarning, "", message, 4*time.Second)
}

// ShowTagged shows a toast that replaces any existing toast with the same tag.
// Use this for progress-style updates (retries, failover attempts, etc.) where
// a burst of near-identical messages would otherwise spam the stack — each new
// message updates the live toast in place.
func (m *ToastManager) ShowTagged(tag string, toastType ToastType, message string, duration time.Duration) {
	duration = normalizeToastDuration(toastType, duration)
	if tag != "" {
		for i := range m.toasts {
			if m.toasts[i].Tag == tag {
				toast := m.toasts[i]
				toast.Type = toastType
				toast.Title = ""
				toast.Message = message
				toast.Duration = duration
				toast.CreatedAt = time.Now()
				toast.FadeOut = false
				m.promoteToast(i, toast)
				return
			}
		}
	}
	toast := Toast{
		ID:        m.nextID,
		Type:      toastType,
		Message:   message,
		Duration:  duration,
		CreatedAt: time.Now(),
		Tag:       tag,
	}
	m.nextID++
	m.toasts = append([]Toast{toast}, m.toasts...)
	m.enforceLimit()
}

func normalizeToastDuration(toastType ToastType, duration time.Duration) time.Duration {
	if duration > 0 {
		return duration
	}
	switch toastType {
	case ToastError:
		return 15 * time.Second
	case ToastWarning:
		return 4 * time.Second
	default:
		return 1500 * time.Millisecond
	}
}

func toastContentKey(toastType ToastType, title, message string) string {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	return string(rune(toastType)) + "\x00" + normalize(title) + "\x00" + normalize(message)
}

func (m *ToastManager) promoteToast(index int, toast Toast) {
	copy(m.toasts[1:index+1], m.toasts[:index])
	m.toasts[0] = toast
}

func (m *ToastManager) enforceLimit() {
	limit := max(m.maxToasts, 0)
	for len(m.toasts) > limit {
		// Start with the oldest item, then prefer evicting a lower-severity
		// notification. Within one severity the oldest still goes first.
		removeIndex := len(m.toasts) - 1
		removePriority := toastPriority(m.toasts[removeIndex].Type)
		for i := len(m.toasts) - 2; i >= 0; i-- {
			if priority := toastPriority(m.toasts[i].Type); priority < removePriority {
				removeIndex = i
				removePriority = priority
			}
		}
		removed := m.toasts[removeIndex]
		m.toasts = append(m.toasts[:removeIndex], m.toasts[removeIndex+1:]...)
		m.archive(removed)
	}
}

func toastPriority(toastType ToastType) int {
	switch toastType {
	case ToastError:
		return 3
	case ToastWarning:
		return 2
	case ToastSuccess:
		return 1
	default:
		return 0
	}
}

func (m *ToastManager) archive(toast Toast) {
	m.history = append([]Toast{toast}, m.history...)
	if len(m.history) > maxToastHistory {
		m.history = m.history[:maxToastHistory]
	}
}

// Dismiss removes a toast by ID.
func (m *ToastManager) Dismiss(id int) {
	for i, toast := range m.toasts {
		if toast.ID == id {
			m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
			m.archive(toast)
			return
		}
	}
}

// DismissTag removes the active toast for a transient interaction state. It is
// used when the state is cancelled before the toast expires, so stale guidance
// (for example "press again to quit") does not remain visible.
func (m *ToastManager) DismissTag(tag string) {
	if tag == "" {
		return
	}
	for i := len(m.toasts) - 1; i >= 0; i-- {
		if m.toasts[i].Tag != tag {
			continue
		}
		toast := m.toasts[i]
		m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
		m.archive(toast)
	}
}

// Update removes expired toasts, archiving them to history.
func (m *ToastManager) Update() {
	var active []Toast
	var expired []Toast
	for _, toast := range m.toasts {
		if !toast.IsExpired() {
			active = append(active, toast)
		} else {
			expired = append(expired, toast)
		}
	}
	m.toasts = active
	for i := len(expired) - 1; i >= 0; i-- {
		m.archive(expired[i])
	}
}

// History returns a copy of the toast history (newest first).
func (m *ToastManager) History() []Toast {
	result := make([]Toast, len(m.history))
	copy(result, m.history)
	return result
}

// Active returns a snapshot of currently visible notifications (newest first).
func (m *ToastManager) Active() []Toast {
	result := make([]Toast, len(m.toasts))
	copy(result, m.toasts)
	return result
}

// ClearHistory removes archived notifications without dismissing active ones.
func (m *ToastManager) ClearHistory() { m.history = nil }

// Count returns the number of active toasts.
func (m *ToastManager) Count() int {
	return len(m.toasts)
}

// View renders all active toasts in the right upper corner.
func (m *ToastManager) View(width int) string {
	return m.ViewLimit(width, len(m.toasts))
}

// ViewLimit renders at most maxLines active notifications. When constrained,
// severity outranks recency so a burst of informational events cannot hide the
// error/warning that explains what needs attention. Hidden count is folded into
// the last visible row instead of consuming another scarce terminal line.
func (m *ToastManager) ViewLimit(width, maxLines int) string {
	if len(m.toasts) == 0 {
		return ""
	}
	maxLines = min(max(maxLines, 0), len(m.toasts))
	if maxLines == 0 {
		return ""
	}

	visible := append([]Toast(nil), m.toasts...)
	if maxLines < len(visible) {
		sort.SliceStable(visible, func(i, j int) bool {
			return toastPriority(visible[i].Type) > toastPriority(visible[j].Type)
		})
		visible = visible[:maxLines]
	}

	var lines []string
	for _, toast := range visible {
		line := m.renderToast(toast, width)
		lines = append(lines, line)
	}
	if hidden := len(m.toasts) - len(visible); hidden > 0 && len(lines) > 0 {
		suffix := lipgloss.NewStyle().Foreground(ColorDim).Render(" · +" + fmt.Sprintf("%d", hidden) + " more")
		budget := max(width-lipgloss.Width(suffix), 0)
		lines[len(lines)-1] = fitStatusText(lines[len(lines)-1], budget) + suffix
		lines[len(lines)-1] = fitStatusText(lines[len(lines)-1], width)
	}

	return strings.Join(lines, "\n")
}

// renderToast renders a single toast as compact single line.
// Format: ✓ Message — fades to dim in last 500ms.
func (m *ToastManager) renderToast(toast Toast, width int) string {
	if width <= 0 {
		width = 80
	}
	icon, ok := ToastIcons[toast.Type]
	if !ok {
		icon = ToastIcons[ToastInfo]
	}
	iconColor, ok := ToastColors[toast.Type]
	if !ok {
		iconColor = ToastColors[ToastInfo]
	}

	// Fade entire toast when nearing expiration
	elapsed := time.Since(toast.CreatedAt)
	remaining := toast.Duration - elapsed
	fading := remaining < 500*time.Millisecond

	msgColor := ColorMuted
	if fading {
		iconColor = ColorDim
		msgColor = ColorDim
	}

	iconStyle := lipgloss.NewStyle().Foreground(iconColor)
	msgStyle := lipgloss.NewStyle().Foreground(msgColor)

	// Toasts are deliberately one visual line. Collapse embedded whitespace
	// from provider errors and include the title when supplied instead of
	// silently discarding it.
	title := strings.Join(strings.Fields(toast.Title), " ")
	message := strings.Join(strings.Fields(toast.Message), " ")
	msg := message
	if title != "" && message != "" {
		msg = title + ": " + message
	} else if title != "" {
		msg = title
	}
	if msg == "" {
		msg = "Notification"
	}

	// Strip machine wrapper taxonomy ("model response error (other): ") the
	// same way the error card does — it can sit mid-string here because toast
	// messages compose prefixes of their own ("Loop x #3: Iteration error: …").
	msg = stripMachineErrorWrappers(msg)

	iconWidth := lipgloss.Width(icon)
	if width <= iconWidth {
		return iconStyle.Render(truncateForWidth(icon, width))
	}
	// Middle-elide, never tail-cut: a toast is one line by design, and its
	// TAIL is where this app puts the action ("… — check /hooks",
	// "… /loop resume <id>", the "→ <hint>" ShowErrorWithHint appends). The
	// old tail truncation amputated exactly that on narrow terminals; the
	// head (what happened) and tail (what to do) now both survive. The full
	// text stays readable in the notification center.
	msg = truncateMiddleForWidth(msg, width-iconWidth-1)
	return iconStyle.Render(icon) + " " + msgStyle.Render(msg)
}

// Clear removes all toasts.
func (m *ToastManager) Clear() {
	m.toasts = m.toasts[:0]
}
