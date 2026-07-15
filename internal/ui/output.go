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
	// cachedCompleteWrapped contains only raw content through the last newline.
	// The unfinished logical line is rewrapped as it grows across stream chunks;
	// treating each chunk as an independent line can create an over-wide row.
	cachedCompleteWrapped string
	completeContentLen    int
	width                 int
	ready                 bool
	frozen                bool // When true, viewport won't auto-scroll to bottom
	thinkingActive        bool // True while streaming thinking content
	reducedMotion         bool // Jump auto-follow directly instead of smooth scrolling

	// The compositor can make the transcript substantially shorter than the
	// resize-time terminalHeight-5 estimate (multi-line composer, toasts and
	// panels all consume rows). ViewWithHeight renders a viewport copy, so keep
	// the geometry and position the user actually saw in shared state. The next
	// wheel/key/tick resumes from these metrics instead of a stale taller view.
	renderedHeight        int
	renderedYOffset       int
	renderedAtBottom      bool
	renderedScrollPercent int
	renderedValid         bool

	// pendingCodeStartLine is the content line where the currently-streaming
	// code block opened — the registry entry is recorded at BlockCodeEnd but
	// must point at the block's start.
	pendingCodeStartLine int

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
		// Keep the standalone tea.Model path equivalent to the top-level
		// compositor's SetSize path. Previously this only resized the viewport:
		// state.width remained zero, pending pre-resize content was never wrapped,
		// and terminals shorter than five rows produced a negative viewport.
		availableHeight := max(msg.Height-5, 1)
		m.SetSize(max(msg.Width, 0), availableHeight)
	case tea.KeyMsg, tea.MouseMsg:
		// Forward key and mouse events to viewport for scrolling
		m.syncViewportToRendered()
		m.viewport, cmd = m.viewport.Update(msg)
		m.rememberRenderedViewport(m.viewport)
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

// ViewWithHeight renders a copy of the viewport at the requested height
// without mutating the live model. The top-level compositor uses this to leave
// room for dynamic panels/input while keeping the status bar on the terminal's
// final row.
//
// CONTRACT: the result is EXACTLY `height` rows. The compositor's frame math
// (View's outputBudget) depends on it: with SHORT content the styled viewport
// used to render height+1 rows, View's shrink loop "corrected" by walking the
// budget down one row per pass all the way to zero, the output vanished
// entirely, and the whole footer (input box) floated to the TOP of the screen
// with the slack inserted before the status bar — the "input at the top" field
// regression. exactRows pads/trims at the source so no caller ever sees a
// row-count surprise.
func (m OutputModel) ViewWithHeight(height int) string {
	if height <= 0 {
		return ""
	}
	m.state.mu.Lock()
	ready := m.state.ready
	frozen := m.state.frozen
	reducedMotion := m.state.reducedMotion
	renderedHeight := m.state.renderedHeight
	renderedYOffset := m.state.renderedYOffset
	renderedValid := m.state.renderedValid
	m.state.mu.Unlock()
	if !ready {
		return exactRows("Loading...", height)
	}

	if renderedValid {
		m.viewport.Height = renderedHeight
		m.viewport.SetYOffset(renderedYOffset)
	}
	heightChanged := !renderedValid || renderedHeight != height
	m.viewport.Height = height
	if !frozen && (heightChanged || reducedMotion) {
		// Following remains bottom-anchored when surrounding chrome changes
		// height. With stable geometry, normal-motion frames retain the offset
		// advanced by TickSmoothScroll instead of discarding the animation.
		m.viewport.GotoBottom()
	} else {
		maxOffset := max(m.viewport.TotalLineCount()-height, 0)
		if m.viewport.YOffset > maxOffset {
			m.viewport.YOffset = maxOffset
		}
	}
	m.rememberRenderedViewport(m.viewport)
	return exactRows(m.styles.Viewport.Render(m.viewport.View()), height)
}

// syncViewportToRendered makes the last frame's actual transcript geometry
// authoritative before navigation, content updates, or animation ticks.
func (m *OutputModel) syncViewportToRendered() {
	m.state.mu.Lock()
	height := m.state.renderedHeight
	yOffset := m.state.renderedYOffset
	valid := m.state.renderedValid
	m.state.mu.Unlock()
	if !valid || height <= 0 {
		return
	}
	// With matching geometry the live model already owns the latest key/tick
	// offset. This also preserves deliberate direct positioning by callers
	// (tests and restore flows). The divergence bug is specifically created by
	// ViewWithHeight rendering a different height than the live viewport.
	if m.viewport.Height == height {
		return
	}
	m.viewport.Height = height
	m.viewport.SetYOffset(yOffset)
}

func (m OutputModel) rememberRenderedViewport(v viewport.Model) {
	atBottom := true
	scrollPercent := 100
	if v.Height > 0 {
		atBottom = v.AtBottom()
		scrollPercent = int(v.ScrollPercent() * 100)
	}
	m.state.mu.Lock()
	m.state.renderedHeight = max(v.Height, 0)
	m.state.renderedYOffset = max(v.YOffset, 0)
	m.state.renderedAtBottom = atBottom
	m.state.renderedScrollPercent = scrollPercent
	m.state.renderedValid = v.Height > 0
	m.state.mu.Unlock()
}

// exactRows normalizes s to exactly the requested number of rows: extra
// TRAILING rows (style padding overshoot) are dropped, missing rows are added
// as trailing blanks. Content rows are never touched from the top, so the
// scroll position the viewport computed stays intact.
func exactRows(s string, rows int) string {
	if rows <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
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
		// Preserve the same external-content safety contract even if a caller
		// constructs an OutputModel without the Markdown parser.
		m.AppendText(safeTerminalDisplayText(text))
		return
	}

	blocks := m.streamParser.Feed(text)

	m.state.mu.Lock()
	m.appendStreamBlocksLocked(blocks)
	m.state.mu.Unlock()
	m.updateViewport()
}

// appendStreamBlocksLocked writes parser blocks into the content buffer.
// Code blocks arrive incrementally (start border → lines → end border) so
// long code renders AS it streams instead of after the closing fence.
// Caller holds m.state.mu.
func (m *OutputModel) appendStreamBlocksLocked(blocks []RenderedBlock) {
	for _, block := range blocks {
		switch block.Kind {
		case BlockCodeStart:
			// Remember where the block began for the registry entry.
			m.state.pendingCodeStartLine = strings.Count(m.state.content.String(), "\n")
			m.state.content.WriteString(m.streamParser.RenderCodeFenceTop(block, m.state.width))
		case BlockCodeLine:
			m.state.content.WriteString(m.streamParser.RenderCodeLine(block))
		case BlockCodeEnd:
			m.state.content.WriteString(m.streamParser.RenderCodeFenceBottom(m.state.width))
			if m.state.codeBlocks != nil && block.Content != "" {
				m.state.codeBlocks.AddBlock(block.Language, block.Filename, block.Content, m.state.pendingCodeStartLine)
			}
		default:
			m.state.content.WriteString(block.Content)
		}
	}
}

// Thinking display — quiet, elegant, stays out of the way.
//
// Captures ColorMuted at package-init time — fine in the locked
// single-theme world. If theme switching ever returns, turn this into a
// helper function so the colour follows ApplyTheme.
var thinkingStyle = lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)

// thinkingLabelStyle matches the body's dim-italic register — thinking is a
// quiet aside, not a banner. The bold-violet label used to be the loudest line
// in an otherwise calm reasoning block; the ◐ glyph alone marks it now.
var thinkingLabelStyle = lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)

// AppendThinkingStream appends streaming thinking content.
// Minimal style: dim italic text with a single soft label on first chunk.
//
//	◐ Thinking
//	reasoning text flows naturally here
//	continuing the thought...
func (m *OutputModel) AppendThinkingStream(text string) {
	// Reasoning tokens are the same untrusted model stream as answer tokens,
	// just rendered through a different path. Keep controls inert and make tab
	// stops deterministic before adding our own styling.
	text = expandDisplayTabs(safeTerminalDisplayText(text), 4)

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

// IsThinkingActive reports whether a thinking block is currently streaming
// (opened by AppendThinkingStream, not yet closed by EndThinking). Used by
// the live activity card to label a reasoning phase honestly — thinking
// chunks never enter currentResponseBuf, so without this signal the card
// showed "Writing: <stale last text line>" while the model was actually
// reasoning silently.
func (m *OutputModel) IsThinkingActive() bool {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.thinkingActive
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
	m.appendStreamBlocksLocked(blocks)
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
	// Glamour adds trusted styling after this point; discard any styling or
	// terminal controls supplied by the external Markdown itself first.
	text = safeTerminalDisplayText(text)
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
	// Clear is a transcript boundary, not just a visual erase. A half-open
	// markdown fence must not classify the next response as old code.
	if m.streamParser != nil {
		m.streamParser.Reset()
	}
	m.state.mu.Lock()
	m.state.content.Reset()
	if m.state.codeBlocks != nil {
		m.state.codeBlocks.Clear()
	}
	// Reset cache on clear
	m.state.cachedWrapped = ""
	m.state.lastContentLen = 0
	m.state.cachedCompleteWrapped = ""
	m.state.completeContentLen = 0
	m.state.frozen = false // Unfreeze on clear to restore normal scroll behavior
	m.state.thinkingActive = false
	m.state.pendingCodeStartLine = 0
	m.state.scrollTarget = 0
	m.state.scrolling = false
	m.state.renderedHeight = 0
	m.state.renderedYOffset = 0
	m.state.renderedAtBottom = true
	m.state.renderedScrollPercent = 100
	m.state.renderedValid = false
	m.state.mu.Unlock()
	m.ForceUpdateViewport()
}

// SetSize sets the viewport size.
func (m *OutputModel) SetSize(width, height int) {
	width = max(width, 0)
	height = max(height, 1)
	m.viewport.Width = width
	m.viewport.Height = height
	// Use more conservative padding to ensure text never hits the right edge
	newWidth := max(width-4, 1)
	if width > 0 && width < 5 {
		// At degenerate widths the four-cell comfort margin would consume the
		// whole transcript and disable wrapping. Use every physical cell so text
		// remains vertically navigable and recovers cleanly after a resize.
		newWidth = width
	}
	if m.streamParser != nil {
		m.streamParser.SetWidth(newWidth)
	}

	m.state.mu.Lock()
	// If width changed, invalidate cache for full re-wrap
	if newWidth != m.state.width {
		m.state.cachedWrapped = ""
		m.state.lastContentLen = 0
		m.state.cachedCompleteWrapped = ""
		m.state.completeContentLen = 0
	}
	m.state.width = newWidth
	m.state.ready = true
	// SetSize is an authoritative geometry transition (terminal resize or
	// compact-mode toggle). Do not let metrics from the preceding layout
	// overwrite its requested height during the immediate content refresh.
	m.state.renderedValid = false
	m.state.mu.Unlock()

	m.ForceUpdateViewport()
	m.state.mu.Lock()
	frozen := m.state.frozen
	if !frozen {
		m.state.scrolling = false
	}
	m.state.mu.Unlock()
	if !frozen {
		// A resize/layout toggle changes which rows constitute the visible
		// bottom. Following users stay anchored there; frozen readers retain
		// their offset and regain ownership on the next rendered frame.
		m.viewport.GotoBottom()
		m.rememberRenderedViewport(m.viewport)
	}
}

// ScrollToBottom scrolls to the bottom of the output.
func (m *OutputModel) ScrollToBottom() {
	m.syncViewportToRendered()
	m.viewport.GotoBottom()
	m.rememberRenderedViewport(m.viewport)
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
	cachedCompleteWrapped := m.state.cachedCompleteWrapped
	completeContentLen := m.state.completeContentLen
	width := m.state.width
	m.state.mu.Unlock()

	// Optimization: if content unchanged, skip wrapping
	if contentLen == lastContentLen && cachedWrapped != "" {
		return
	}

	// Incremental wrapping is safe only through complete logical lines. Stream
	// chunks routinely split a word or paragraph; appending wrapText(newChunk)
	// to the old result leaves that unfinished row unwrapped across the boundary.
	// Cache the newline-terminated prefix and rewrap only the current tail.
	var wrapped string
	completeEnd := strings.LastIndex(content, "\n") + 1
	canExtendCompletePrefix := contentLen >= lastContentLen &&
		completeContentLen >= 0 && completeContentLen <= completeEnd &&
		completeContentLen <= lastContentLen &&
		(completeContentLen == 0 || cachedCompleteWrapped != "")
	completeWrapped := ""
	if canExtendCompletePrefix {
		completeWrapped = cachedCompleteWrapped + wrapText(content[completeContentLen:completeEnd], width)
	} else {
		completeWrapped = wrapText(content[:completeEnd], width)
	}
	wrapped = completeWrapped + wrapText(content[completeEnd:], width)

	m.state.mu.Lock()
	m.state.cachedWrapped = wrapped
	m.state.lastContentLen = contentLen
	m.state.cachedCompleteWrapped = completeWrapped
	m.state.completeContentLen = completeEnd
	m.state.mu.Unlock()

	m.syncViewportToRendered()
	m.viewport.SetContent(wrapped)
	m.state.mu.Lock()
	frozen := m.state.frozen
	reducedMotion := m.state.reducedMotion

	if !frozen {
		// Set smooth scroll target to bottom instead of jumping instantly.
		// The actual scrolling happens in TickSmoothScroll() called every frame.
		totalLines := m.viewport.TotalLineCount()
		target := totalLines - m.viewport.Height
		if target < 0 {
			target = 0
		}
		m.state.scrollTarget = target
		m.state.scrolling = !reducedMotion
		m.state.mu.Unlock()
		if reducedMotion {
			m.viewport.GotoBottom()
			m.rememberRenderedViewport(m.viewport)
			return
		}

		// If the gap is small (≤3 lines), jump directly to avoid visible lag
		// on character-by-character streaming
		gap := target - m.viewport.YOffset
		if gap <= 3 {
			m.viewport.GotoBottom()
			m.state.mu.Lock()
			m.state.scrolling = false
			m.state.mu.Unlock()
		}
		m.rememberRenderedViewport(m.viewport)
	} else {
		// Frozen = the user scrolled up to read; leave the viewport exactly where
		// it is (no auto-scroll) so they can read WHILE the agent streams.
		// Following resumes when they scroll back to the bottom (the scroll
		// handlers clear frozen at AtBottom) or send a new message.
		//
		// (Previously a heuristic auto-unfroze within 3 lines of the bottom — but
		// a single mouse-wheel notch IS 3 lines, so one wheel-up got snapped right
		// back down mid-read. Following users never reach this branch; they use
		// the !frozen path above, so dropping the heuristic doesn't affect them.)
		m.state.mu.Unlock()
		m.rememberRenderedViewport(m.viewport)
	}
}

// TickSmoothScroll advances the smooth scroll animation by one step.
// Call this on every frame tick (e.g., spinner tick at 60Hz).
// Returns true if the viewport moved (caller should re-render).
func (m *OutputModel) TickSmoothScroll() bool {
	m.syncViewportToRendered()
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
		m.rememberRenderedViewport(m.viewport)
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
	m.rememberRenderedViewport(m.viewport)

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

		// Byte/rune counts are not terminal-cell widths: CJK and emoji may occupy
		// two cells, while ANSI controls occupy none. Measure the actual rendered
		// width whenever the byte-length fast path cannot prove the row fits.
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

// SetReducedMotion switches auto-follow between eased scrolling and an
// immediate jump. Enabling it also finishes any animation already in flight.
func (m *OutputModel) SetReducedMotion(enabled bool) {
	m.state.mu.Lock()
	m.state.reducedMotion = enabled
	frozen := m.state.frozen
	if enabled {
		m.state.scrolling = false
	}
	m.state.mu.Unlock()
	if enabled && !frozen {
		m.syncViewportToRendered()
		m.viewport.GotoBottom()
		m.rememberRenderedViewport(m.viewport)
	}
}

// ScrollPercent returns the scroll position as a percentage (0-100).
func (m OutputModel) ScrollPercent() int {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if m.state.renderedValid {
		return m.state.renderedScrollPercent
	}
	return int(m.viewport.ScrollPercent() * 100)
}

// IsAtBottom returns whether the viewport is scrolled to the bottom.
func (m OutputModel) IsAtBottom() bool {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if m.state.renderedValid {
		return m.state.renderedAtBottom
	}
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
// Freezing also cancels any in-flight smooth auto-scroll (TickSmoothScroll)
// so a scroll-up the user just made isn't dragged back toward the bottom by an
// animation that was already running when they froze.
func (m *OutputModel) SetFrozen(frozen bool) {
	m.state.mu.Lock()
	m.state.frozen = frozen
	if frozen {
		m.state.scrolling = false
	}
	m.state.mu.Unlock()
	m.rememberRenderedViewport(m.viewport)
}
