package ui

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

const (
	// Adaptive debounce intervals based on content size.
	// Short content gets faster updates for responsiveness.
	// Large content gets slower updates to reduce flicker.
	viewportUpdateFast   = 8 * time.Millisecond  // < 5K chars: near-instant (120Hz)
	viewportUpdateNormal = 16 * time.Millisecond // 5K-50K chars: smooth (60Hz)
	viewportUpdateSlow   = 32 * time.Millisecond // > 50K chars: reduced flicker (30Hz)
)

// outputState holds the mutable state protected by a mutex.
// This is kept separate so OutputModel can be copied by Bubble Tea without copying the mutex.
type outputState struct {
	mu             sync.Mutex
	content        *strings.Builder
	codeBlocks     *CodeBlockRegistry
	cachedWrapped  string
	lastContentLen int
	lastWrappedLen int
	width          int
	ready          bool
	frozen         bool // When true, viewport won't auto-scroll to bottom
	thinkingActive bool // True while streaming thinking content

	// Smooth scroll state: instead of jumping to bottom instantly,
	// we scroll a few lines per frame toward the target.
	scrollTarget int  // Target YOffset (usually total lines - viewport height)
	scrolling    bool // True while smooth scroll animation is in progress
}

// OutputModel represents the output/viewport component.
type OutputModel struct {
	viewport     viewport.Model
	styles       *Styles
	renderer     *glamour.TermRenderer
	streamParser *MarkdownStreamParser

	// state holds mutex-protected mutable state (pointer to avoid copy issues)
	state *outputState

	// Debouncing for viewport updates (using atomic int64 to avoid copy issues)
	lastUpdateNano int64 // atomic: last update time in nanoseconds
	contentDirty   int64 // atomic: 1 if content needs update, 0 otherwise
}

// NewOutputModel creates a new output model.
func NewOutputModel(styles *Styles) OutputModel {
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(0), // Disable fixed word wrap, handled by viewport
	)

	return OutputModel{
		styles:       styles,
		renderer:     renderer,
		streamParser: NewMarkdownStreamParser(styles),
		state: &outputState{
			content:    &strings.Builder{},
			codeBlocks: NewCodeBlockRegistry(styles),
		},
	}
}

// Init initializes the output model.
func (m OutputModel) Init() tea.Cmd {
	return nil
}

// Update handles output events.
func (m OutputModel) Update(msg tea.Msg) (OutputModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.state.mu.Lock()
		ready := m.state.ready
		m.state.mu.Unlock()

		if !ready {
			// Calculate viewport height dynamically - use all available space except for input (3 lines) and status bar (2 lines)
			availableHeight := msg.Height - 5
			m.viewport = viewport.New(msg.Width, availableHeight)
			m.viewport.YPosition = 0
			m.viewport.MouseWheelEnabled = true
			m.state.mu.Lock()
			m.state.ready = true
			m.state.mu.Unlock()
		} else {
			// Recalculate on resize
			availableHeight := msg.Height - 5
			m.viewport.Width = msg.Width
			m.viewport.Height = availableHeight
		}
	case tea.KeyMsg, tea.MouseMsg:
		// Forward key and mouse events to viewport for scrolling
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// View renders the output component.
func (m OutputModel) View() string {
	m.state.mu.Lock()
	ready := m.state.ready
	m.state.mu.Unlock()

	if !ready {
		return "Loading..."
	}
	return m.styles.Viewport.Render(m.viewport.View())
}

// AppendText appends text to the output.
func (m *OutputModel) AppendText(text string) {
	m.state.mu.Lock()
	m.state.content.WriteString(text)
	m.state.mu.Unlock()
	m.updateViewport()
}

// AppendTextStream appends streaming text with markdown parsing and syntax highlighting.
func (m *OutputModel) AppendTextStream(text string) {
	if m.streamParser == nil {
		// Fallback to regular append
		m.AppendText(text)
		return
	}

	blocks := m.streamParser.Feed(text)

	m.state.mu.Lock()
	for _, block := range blocks {
		if block.IsCode {
			// Register code block for later actions
			if m.state.codeBlocks != nil {
				lineNum := strings.Count(m.state.content.String(), "\n")
				m.state.codeBlocks.AddBlock(block.Language, block.Filename, block.Content, lineNum)
			}

			// Render with syntax highlighting and border
			rendered := m.streamParser.RenderCodeBlock(block, m.state.width)
			m.state.content.WriteString(rendered)
		} else {
			m.state.content.WriteString(block.Content)
		}
	}
	m.state.mu.Unlock()
	m.updateViewport()
}

// Thinking display — quiet, elegant, stays out of the way.
var thinkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Italic(true)
var thinkingLabelStyle = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)

// AppendThinkingStream appends streaming thinking content.
// Minimal style: dim italic text with a single soft label on first chunk.
//
//	◐ Thinking
//	reasoning text flows naturally here
//	continuing the thought...
func (m *OutputModel) AppendThinkingStream(text string) {
	m.state.mu.Lock()

	if !m.state.thinkingActive {
		m.state.thinkingActive = true
		if m.state.content.Len() > 0 {
			current := m.state.content.String()
			if !strings.HasSuffix(current, "\n\n") {
				if !strings.HasSuffix(current, "\n") {
					m.state.content.WriteString("\n")
				}
				m.state.content.WriteString("\n")
			}
		}
		m.state.content.WriteString("  ")
		m.state.content.WriteString(thinkingLabelStyle.Render("◐ Thinking"))
		m.state.content.WriteString("\n")
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			m.state.content.WriteString(thinkingStyle.Render(line))
		}
		if i < len(lines)-1 {
			m.state.content.WriteString("\n")
		}
	}

	m.state.mu.Unlock()
	m.updateViewport()
}

// EndThinking closes the thinking block with a blank line separator.
func (m *OutputModel) EndThinking() {
	m.state.mu.Lock()
	if m.state.thinkingActive {
		m.state.thinkingActive = false
		m.state.content.WriteString("\n\n")
	}
	m.state.mu.Unlock()
}

// FlushStream flushes any remaining content in the stream parser.
func (m *OutputModel) FlushStream() {
	if m.streamParser == nil {
		return
	}

	blocks := m.streamParser.Flush()

	m.state.mu.Lock()
	for _, block := range blocks {
		if block.IsCode {
			if m.state.codeBlocks != nil {
				lineNum := strings.Count(m.state.content.String(), "\n")
				m.state.codeBlocks.AddBlock(block.Language, block.Filename, block.Content, lineNum)
			}
			rendered := m.streamParser.RenderCodeBlock(block, m.state.width)
			m.state.content.WriteString(rendered)
		} else {
			m.state.content.WriteString(block.Content)
		}
	}
	m.state.mu.Unlock()
	// Force update on flush to ensure all content is visible
	m.ForceUpdateViewport()
}

// ResetStream resets the stream parser state.
func (m *OutputModel) ResetStream() {
	if m.streamParser != nil {
		m.streamParser.Reset()
	}
}

// GetCodeBlocks returns the code block registry.
func (m *OutputModel) GetCodeBlocks() *CodeBlockRegistry {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.codeBlocks
}

// AppendLine appends a line to the output.
func (m *OutputModel) AppendLine(text string) {
	m.state.mu.Lock()
	m.state.content.WriteString(text)
	m.state.content.WriteString("\n")
	m.state.mu.Unlock()
	m.updateViewport()
}

// AppendMarkdown appends and renders markdown.
func (m *OutputModel) AppendMarkdown(text string) {
	if m.renderer != nil {
		rendered, err := m.renderer.Render(text)
		if err == nil {
			text = rendered
		}
	}
	m.state.mu.Lock()
	m.state.content.WriteString(text)
	m.state.content.WriteString("\n")
	m.state.mu.Unlock()
	m.updateViewport()
}

// Clear clears the output.
func (m *OutputModel) Clear() {
	m.state.mu.Lock()
	m.state.content.Reset()
	// Reset cache on clear
	m.state.cachedWrapped = ""
	m.state.lastContentLen = 0
	m.state.lastWrappedLen = 0
	m.state.frozen = false // Unfreeze on clear to restore normal scroll behavior
	m.state.mu.Unlock()
	m.ForceUpdateViewport()
}

// SetSize sets the viewport size.
func (m *OutputModel) SetSize(width, height int) {
	m.viewport.Width = width
	m.viewport.Height = height
	// Use more conservative padding to ensure text never hits the right edge
	newWidth := width - 4

	m.state.mu.Lock()
	// If width changed, invalidate cache for full re-wrap
	if newWidth != m.state.width {
		m.state.cachedWrapped = ""
		m.state.lastContentLen = 0
		m.state.lastWrappedLen = 0
	}
	m.state.width = newWidth
	m.state.ready = true
	m.state.mu.Unlock()

	m.ForceUpdateViewport()
}

// ScrollToBottom scrolls to the bottom of the output.
func (m *OutputModel) ScrollToBottom() {
	m.viewport.GotoBottom()
}

func (m *OutputModel) updateViewport() {
	m.state.mu.Lock()
	ready := m.state.ready
	contentSize := m.state.content.Len()
	m.state.mu.Unlock()

	if !ready {
		return
	}

	// Adaptive debounce: faster updates for small content, slower for large.
	// First ~1KB always renders immediately — makes the first tokens of a
	// response feel instant instead of waiting for the 8ms quantum.
	var interval time.Duration
	switch {
	case contentSize < 1024:
		interval = 0 // no debounce — render every chunk
	case contentSize < 5000:
		interval = viewportUpdateFast
	case contentSize > 50000:
		interval = viewportUpdateSlow
	default:
		interval = viewportUpdateNormal
	}

	now := time.Now().UnixNano()
	lastUpdate := atomic.LoadInt64(&m.lastUpdateNano)
	timeSinceLastUpdate := time.Duration(now - lastUpdate)

	if interval > 0 && timeSinceLastUpdate < interval {
		atomic.StoreInt64(&m.contentDirty, 1)
		return
	}

	m.doViewportUpdate()
	atomic.StoreInt64(&m.lastUpdateNano, now)
	atomic.StoreInt64(&m.contentDirty, 0)
}

// ForceUpdateViewport forces an immediate viewport update, bypassing debounce.
// Use sparingly - mainly for final flush operations.
func (m *OutputModel) ForceUpdateViewport() {
	m.state.mu.Lock()
	ready := m.state.ready
	m.state.mu.Unlock()

	if !ready {
		return
	}

	m.doViewportUpdate()
	atomic.StoreInt64(&m.lastUpdateNano, time.Now().UnixNano())
	atomic.StoreInt64(&m.contentDirty, 0)
}

// FlushPendingUpdate applies any pending viewport update.
// Called periodically (e.g., on spinner tick) to ensure content is eventually shown.
func (m *OutputModel) FlushPendingUpdate() {
	m.state.mu.Lock()
	ready := m.state.ready
	m.state.mu.Unlock()

	if atomic.LoadInt64(&m.contentDirty) == 1 && ready {
		m.doViewportUpdate()
		atomic.StoreInt64(&m.lastUpdateNano, time.Now().UnixNano())
		atomic.StoreInt64(&m.contentDirty, 0)
	}
}

// doViewportUpdate performs the actual viewport update with incremental wrapping.
func (m *OutputModel) doViewportUpdate() {
	m.state.mu.Lock()
	content := m.state.content.String()
	contentLen := len(content)
	lastContentLen := m.state.lastContentLen
	cachedWrapped := m.state.cachedWrapped
	lastWrappedLen := m.state.lastWrappedLen
	width := m.state.width
	m.state.mu.Unlock()

	// Optimization: if content unchanged, skip wrapping
	if contentLen == lastContentLen && cachedWrapped != "" {
		return
	}

	// Incremental wrapping: only wrap new content if possible
	var wrapped string
	if width > 20 {
		if contentLen > lastContentLen && lastWrappedLen > 0 && cachedWrapped != "" {
			// Append only new content wrapping
			newContent := content[lastContentLen:]
			newWrapped := wrapText(newContent, width)
			wrapped = cachedWrapped + newWrapped
		} else {
			// Full re-wrap (on resize, clear, or first time)
			wrapped = wrapText(content, width)
		}
	} else {
		wrapped = content
	}

	m.state.mu.Lock()
	m.state.cachedWrapped = wrapped
	m.state.lastContentLen = contentLen
	m.state.lastWrappedLen = len(wrapped)
	m.state.mu.Unlock()

	m.viewport.SetContent(wrapped)
	m.state.mu.Lock()
	frozen := m.state.frozen

	if !frozen {
		// Set smooth scroll target to bottom instead of jumping instantly.
		// The actual scrolling happens in TickSmoothScroll() called every frame.
		totalLines := m.viewport.TotalLineCount()
		target := totalLines - m.viewport.Height
		if target < 0 {
			target = 0
		}
		m.state.scrollTarget = target
		m.state.scrolling = true
		m.state.mu.Unlock()

		// If the gap is small (≤3 lines), jump directly to avoid visible lag
		// on character-by-character streaming
		gap := target - m.viewport.YOffset
		if gap <= 3 {
			m.viewport.GotoBottom()
			m.state.mu.Lock()
			m.state.scrolling = false
			m.state.mu.Unlock()
		}
	} else {
		// Smart unfreeze: if user is near the bottom (within 3 lines), auto-unfreeze
		totalLines := m.viewport.TotalLineCount()
		visibleBottom := m.viewport.YOffset + m.viewport.Height
		if totalLines > 0 && totalLines-visibleBottom <= 3 {
			m.state.frozen = false
			m.state.scrollTarget = totalLines - m.viewport.Height
			m.state.scrolling = true
		}
		m.state.mu.Unlock()
	}
}

// TickSmoothScroll advances the smooth scroll animation by one step.
// Call this on every frame tick (e.g., spinner tick at 60Hz).
// Returns true if the viewport moved (caller should re-render).
func (m *OutputModel) TickSmoothScroll() bool {
	m.state.mu.Lock()
	scrolling := m.state.scrolling
	target := m.state.scrollTarget
	m.state.mu.Unlock()

	if !scrolling {
		return false
	}

	current := m.viewport.YOffset
	gap := target - current

	if gap <= 0 {
		// Already at or past target
		m.state.mu.Lock()
		m.state.scrolling = false
		m.state.mu.Unlock()
		return false
	}

	// Scroll speed: proportional to gap, with minimum step of 1 and max of 8.
	// This creates an ease-out effect: fast when far, slow when close.
	step := gap / 3
	if step < 1 {
		step = 1
	}
	if step > 8 {
		step = 8
	}

	m.viewport.SetYOffset(current + step)

	// Check if we've arrived
	if m.viewport.YOffset >= target {
		m.viewport.GotoBottom()
		m.state.mu.Lock()
		m.state.scrolling = false
		m.state.mu.Unlock()
	}

	return true
}

// IsScrolling returns true if smooth scroll animation is in progress.
func (m *OutputModel) IsScrolling() bool {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.scrolling
}

// wrapText wraps text to the specified width using lipgloss for ANSI-aware wrapping.
// Optimized to avoid expensive ANSI parsing when possible.
func wrapText(text string, width int) string {
	if width <= 0 {
		return text
	}

	style := lipgloss.NewStyle().Width(width)

	var result strings.Builder
	// Pre-allocate approximate capacity
	result.Grow(len(text) + len(text)/width*2)

	lines := strings.Split(text, "\n")

	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}

		// Fast path: if line is shorter than width in bytes, it's definitely shorter in runes
		// (since each rune is at least 1 byte). Skip expensive ANSI parsing.
		if len(line) <= width {
			result.WriteString(line)
			continue
		}

		// Medium path: check if line has ANSI codes. If not, use simple rune count.
		if !strings.Contains(line, "\x1b[") {
			if len([]rune(line)) <= width {
				result.WriteString(line)
			} else {
				result.WriteString(style.Render(line))
			}
			continue
		}

		// Slow path: line has ANSI codes, use full lipgloss width calculation
		if lipgloss.Width(line) > width {
			result.WriteString(style.Render(line))
		} else {
			result.WriteString(line)
		}
	}

	return result.String()
}

// Ready returns whether the viewport is ready.
func (m OutputModel) Ready() bool {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.ready
}

// Content returns the current content.
func (m *OutputModel) Content() string {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.content.String()
}

// IsEmpty returns whether the output has no content.
func (m OutputModel) IsEmpty() bool {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.content.Len() == 0
}

// SetMouseEnabled enables or disables mouse wheel scrolling in the viewport.
func (m *OutputModel) SetMouseEnabled(enabled bool) {
	m.viewport.MouseWheelEnabled = enabled
}

// ScrollPercent returns the scroll position as a percentage (0-100).
func (m OutputModel) ScrollPercent() int {
	return int(m.viewport.ScrollPercent() * 100)
}

// IsAtBottom returns whether the viewport is scrolled to the bottom.
func (m OutputModel) IsAtBottom() bool {
	return m.viewport.AtBottom()
}

// IsFrozen returns whether the viewport auto-scroll is frozen.
func (m OutputModel) IsFrozen() bool {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.frozen
}

// SetFrozen freezes or unfreezes the viewport auto-scroll.
// When frozen, new content won't cause the viewport to jump to the bottom.
func (m *OutputModel) SetFrozen(frozen bool) {
	m.state.mu.Lock()
	m.state.frozen = frozen
	m.state.mu.Unlock()
}
