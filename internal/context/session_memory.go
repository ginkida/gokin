package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gokin/internal/client"
	"gokin/internal/fileutil"
	"gokin/internal/logging"
	"gokin/internal/memory"

	"google.golang.org/genai"
)

// SessionMemoryConfig holds configuration for automatic session memory extraction.
type SessionMemoryConfig struct {
	Enabled                 bool `yaml:"enabled"`
	MinTokensToInit         int  `yaml:"min_tokens_to_init"`         // Start after N tokens (default: 10000)
	MinTokensBetweenUpdates int  `yaml:"min_tokens_between_updates"` // Update every N new tokens (default: 5000)
	ToolCallsBetweenUpdates int  `yaml:"tool_calls_between_updates"` // Or every N tool calls (default: 3)
}

// DefaultSessionMemoryConfig returns default session memory configuration.
func DefaultSessionMemoryConfig() SessionMemoryConfig {
	return SessionMemoryConfig{
		Enabled:                 true,
		MinTokensToInit:         10000,
		MinTokensBetweenUpdates: 5000,
		ToolCallsBetweenUpdates: 3,
	}
}

// SessionMemoryManager maintains a structured summary of the current session.
// It periodically extracts key information from the conversation history and
// writes it to .gokin/.session-memory.md for injection into the system prompt.
type SessionMemoryManager struct {
	workDir string
	config  SessionMemoryConfig

	// Tracking thresholds
	lastExtractionTokens int
	toolCallsSinceUpdate int
	extractionCount      int
	initialized          bool

	// LLM summarizer (optional — set via SetSummarizer)
	summarizer SessionSummarizer

	// Callback fired after each successful extraction (for UI notifications)
	onUpdate func()

	// Durable project learning store (optional). When configured, high-signal
	// findings from the session are promoted into project memory markdown.
	projectLearning *memory.ProjectLearning

	// Extracted content
	content string

	mu sync.RWMutex
}

// SetOnUpdate sets a callback invoked after each successful session memory extraction.
func (s *SessionMemoryManager) SetOnUpdate(cb func()) {
	s.mu.Lock()
	s.onUpdate = cb
	s.mu.Unlock()
}

// SetProjectLearning wires durable project learning promotion into the session
// extraction pipeline.
func (s *SessionMemoryManager) SetProjectLearning(pl *memory.ProjectLearning) {
	s.mu.Lock()
	s.projectLearning = pl
	s.mu.Unlock()
}

// SessionSummarizer generates a text summary from conversation history.
type SessionSummarizer interface {
	Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error)
}

// SetSummarizer enables LLM-based session memory extraction.
// When set, every Nth extraction uses the LLM for a higher-quality summary.
func (s *SessionMemoryManager) SetSummarizer(sum SessionSummarizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summarizer = sum
}

// NewSessionMemoryManager creates a new session memory manager.
func NewSessionMemoryManager(workDir string, config SessionMemoryConfig) *SessionMemoryManager {
	return &SessionMemoryManager{
		workDir: workDir,
		config:  config,
	}
}

// ShouldExtract checks whether extraction thresholds have been met.
func (s *SessionMemoryManager) ShouldExtract(currentTokens int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.config.Enabled {
		return false
	}

	// First extraction: wait for minimum tokens
	if !s.initialized {
		return currentTokens >= s.config.MinTokensToInit
	}

	// Subsequent: check token delta or tool call count
	tokenDelta := currentTokens - s.lastExtractionTokens
	if tokenDelta >= s.config.MinTokensBetweenUpdates {
		return true
	}
	if s.toolCallsSinceUpdate >= s.config.ToolCallsBetweenUpdates {
		return true
	}

	return false
}

// RecordToolCall increments the tool call counter.
func (s *SessionMemoryManager) RecordToolCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCallsSinceUpdate++
}

// ExtractAsync copies the history slice and runs extraction in a background goroutine.
// This is the safe entry point — callers don't need to worry about race conditions.
func (s *SessionMemoryManager) ExtractAsync(history []*genai.Content, currentTokens int) {
	snapshot := make([]*genai.Content, len(history))
	copy(snapshot, history)
	go s.Extract(snapshot, currentTokens)
}

// Extract extracts session memory from conversation history using heuristic analysis.
// This does NOT make any LLM calls — it uses pattern matching on the history.
func (s *SessionMemoryManager) Extract(history []*genai.Content, currentTokens int) {
	if len(history) < 4 {
		return // Too little history
	}

	var builder strings.Builder
	builder.WriteString("# Session Memory\n")
	fmt.Fprintf(&builder, "_Updated: %s_\n\n", time.Now().Format("15:04"))

	// Extract current task from the last few user messages
	if task := extractCurrentTask(history); task != "" {
		builder.WriteString("## Current State\n")
		builder.WriteString(task)
		builder.WriteString("\n\n")
	}

	// Extract files mentioned (read/written/edited)
	files := extractFileActivity(history)
	if len(files) > 0 {
		builder.WriteString("## Files and Functions\n")
		for _, f := range files {
			fmt.Fprintf(&builder, "- %s (%s)\n", f.Path, f.Action)
		}
		builder.WriteString("\n")
	}

	// Extract tool usage patterns
	toolCounts := extractToolUsage(history)
	if len(toolCounts) > 0 {
		builder.WriteString("## Workflow\n")
		// Sort by count descending
		type tc struct {
			name  string
			count int
		}
		var sorted []tc
		for k, v := range toolCounts {
			sorted = append(sorted, tc{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })
		var parts []string
		for _, t := range sorted {
			parts = append(parts, fmt.Sprintf("%s (%dx)", t.name, t.count))
		}
		builder.WriteString("Tools used: ")
		builder.WriteString(strings.Join(parts, ", "))
		builder.WriteString("\n\n")
	}

	// Extract errors encountered
	errors := extractErrors(history)
	if len(errors) > 0 {
		builder.WriteString("## Errors & Corrections\n")
		for _, e := range errors {
			fmt.Fprintf(&builder, "- %s\n", e)
		}
		builder.WriteString("\n")
	}

	heuristicContent := builder.String()

	durableLearnings := extractDurableSessionLearnings(history)

	s.mu.Lock()
	s.extractionCount++
	s.lastExtractionTokens = currentTokens
	s.toolCallsSinceUpdate = 0
	s.initialized = true
	useLLM := s.summarizer != nil && s.extractionCount%3 == 0 && len(history) >= 10
	onUpdate := s.onUpdate
	projectLearning := s.projectLearning

	// Every 3rd extraction, try LLM-based summarization for higher quality
	if useLLM {
		s.mu.Unlock()
		if projectLearning != nil {
			s.flushPromotedSessionLearnings(projectLearning, durableLearnings)
		}
		go s.extractWithLLM(history, heuristicContent)
		logging.Debug("session memory extracted",
			"files", len(files),
			"tools", len(toolCounts),
			"errors", len(errors),
			"tokens", currentTokens,
			"promoted", len(durableLearnings))
		return
	} else {
		s.content = heuristicContent
	}
	s.mu.Unlock()

	s.writeToDisk()

	if projectLearning != nil {
		s.flushPromotedSessionLearnings(projectLearning, durableLearnings)
	}

	logging.Debug("session memory extracted",
		"files", len(files),
		"tools", len(toolCounts),
		"errors", len(errors),
		"tokens", currentTokens,
		"promoted", len(durableLearnings))

	// Notify UI about session memory update
	if onUpdate != nil {
		onUpdate()
	}
}

// extractWithLLM uses the Summarizer to create a higher-quality session summary.
// Runs in a goroutine; falls back to heuristic content on failure.
func (s *SessionMemoryManager) extractWithLLM(history []*genai.Content, fallback string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prompt := `Summarize this coding session into a structured markdown format:

# Session Memory
## Current State
(What is being worked on right now?)
## Files and Functions
(List important files mentioned, with what was done to each)
## Workflow
(Key tools used, commands run)
## Errors & Corrections
(Errors encountered and how they were resolved)
## Key Decisions
(Important architectural or design decisions made)

Be concise. Each section should be 1-5 bullet points maximum.`

	summary, err := s.summarizer.Summarize(ctx, history, prompt)
	if err != nil || summary == "" {
		logging.Debug("LLM session memory extraction failed, using heuristic", "error", err)
		s.mu.Lock()
		s.content = fallback
		onUpdate := s.onUpdate
		s.mu.Unlock()
		s.writeToDisk()
		if onUpdate != nil {
			onUpdate()
		}
		return
	}

	s.mu.Lock()
	s.content = summary
	s.mu.Unlock()
	s.writeToDisk()
	logging.Debug("LLM session memory extraction completed", "size", len(summary))

	s.mu.RLock()
	onUpdate := s.onUpdate
	s.mu.RUnlock()

	if onUpdate != nil {
		onUpdate()
	}
}

func (s *SessionMemoryManager) flushPromotedSessionLearnings(pl *memory.ProjectLearning, learnings []sessionDurableLearning) {
	if pl == nil || len(learnings) == 0 {
		return
	}
	promoted := 0
	for _, learning := range learnings {
		switch learning.kind {
		case "fact", "convention":
			prefKey := learning.kind + ":" + learning.key
			if existing := pl.GetPreference(prefKey); existing == learning.value {
				continue
			}
			pl.SetPreference(prefKey, learning.value)
			promoted++
		}
	}
	if promoted == 0 {
		return
	}
	if err := pl.Flush(); err != nil {
		logging.Debug("failed to flush promoted session learning", "error", err)
	}
}

// GetContent returns the current session memory content for prompt injection.
func (s *SessionMemoryManager) GetContent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.content
}

// LoadFromDisk loads previously saved session memory.
func (s *SessionMemoryManager) LoadFromDisk() {
	path := s.filePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return // File doesn't exist yet
	}
	s.mu.Lock()
	s.content = string(data)
	s.initialized = true
	s.mu.Unlock()
	logging.Debug("loaded session memory from disk", "path", path)
}

// Clear resets session memory (e.g., on new session).
func (s *SessionMemoryManager) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content = ""
	s.initialized = false
	s.lastExtractionTokens = 0
	s.toolCallsSinceUpdate = 0
	os.Remove(s.filePath())
}

func (s *SessionMemoryManager) filePath() string {
	dir := filepath.Join(s.workDir, ".gokin")
	return filepath.Join(dir, ".session-memory.md")
}

func (s *SessionMemoryManager) writeToDisk() {
	dir := filepath.Join(s.workDir, ".gokin")
	if err := os.MkdirAll(dir, 0750); err != nil {
		logging.Debug("failed to create .gokin dir for session memory", "error", err)
		return
	}
	if err := fileutil.AtomicWrite(s.filePath(), []byte(s.content), 0640); err != nil {
		logging.Debug("failed to write session memory", "error", err)
	}
}

// --- Heuristic extraction helpers ---

// fileActivity represents a file path and what happened to it.
type fileActivity struct {
	Path   string
	Action string // "read", "edited", "created", "searched"
}

type sessionDurableLearning struct {
	kind  string
	key   string
	value string
}

func extractCurrentTask(history []*genai.Content) string {
	// Find the last substantive user message (skip short "ok", "yes" etc.)
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != genai.RoleUser {
			continue
		}
		for _, part := range msg.Parts {
			if part.Text != "" && len(part.Text) > 20 {
				text := part.Text
				if runes := []rune(text); len(runes) > 200 {
					text = string(runes[:200]) + "..."
				}
				return text
			}
		}
	}
	return ""
}

func extractFileActivity(history []*genai.Content) []fileActivity {
	seen := make(map[string]string) // path -> action

	for _, msg := range history {
		for _, part := range msg.Parts {
			if part.FunctionCall != nil {
				fc := part.FunctionCall
				path, _ := fc.Args["file_path"].(string)
				if path == "" {
					path, _ = fc.Args["path"].(string)
				}
				if path == "" {
					continue
				}
				switch fc.Name {
				case "read":
					if _, exists := seen[path]; !exists {
						seen[path] = "read"
					}
				case "write":
					seen[path] = "created"
				case "edit":
					seen[path] = "edited"
				case "grep", "glob":
					if _, exists := seen[path]; !exists {
						seen[path] = "searched"
					}
				}
			}
		}
	}

	var result []fileActivity
	for path, action := range seen {
		result = append(result, fileActivity{Path: path, Action: action})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })

	// Limit to most important files
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

func extractToolUsage(history []*genai.Content) map[string]int {
	counts := make(map[string]int)
	for _, msg := range history {
		for _, part := range msg.Parts {
			if part.FunctionCall != nil {
				counts[part.FunctionCall.Name]++
			}
		}
	}
	return counts
}

var errorPatterns = []string{
	"error:", "Error:", "ERROR:",
	"failed:", "Failed:", "FAILED:",
	"panic:", "PANIC:",
	"permission denied",
	"no such file",
	"compilation failed",
	"test failed",
	"syntax error",
}

func extractErrors(history []*genai.Content) []string {
	var errors []string
	seen := make(map[string]bool)

	for _, msg := range history {
		for _, part := range msg.Parts {
			if part.FunctionResponse == nil {
				continue
			}
			resp := part.FunctionResponse.Response
			if resp == nil {
				continue
			}
			errMsg, _ := resp["error"].(string)
			content, _ := resp["content"].(string)

			text := errMsg + " " + content
			for _, pattern := range errorPatterns {
				if strings.Contains(text, pattern) {
					// Extract the relevant line
					line := extractErrorLine(text, pattern)
					if line != "" && !seen[line] {
						seen[line] = true
						errors = append(errors, line)
					}
					break
				}
			}
		}
	}

	// Limit to last 20 errors
	if len(errors) > 20 {
		errors = errors[len(errors)-20:]
	}
	return errors
}

type sessionFunctionCall struct {
	name string
	args map[string]any
}

func extractDurableSessionLearnings(history []*genai.Content) []sessionDurableLearning {
	pending := make(map[string][]sessionFunctionCall)
	latest := make(map[string]sessionDurableLearning)

	for _, msg := range history {
		for _, part := range msg.Parts {
			if part.FunctionCall != nil {
				call := part.FunctionCall
				pending[call.Name] = append(pending[call.Name], sessionFunctionCall{
					name: call.Name,
					args: call.Args,
				})
			}
			if part.FunctionResponse == nil {
				continue
			}
			resp := part.FunctionResponse.Response
			if !isSuccessfulFunctionResponse(resp) {
				continue
			}

			queue := pending[part.FunctionResponse.Name]
			if len(queue) == 0 {
				continue
			}
			call := queue[0]
			pending[part.FunctionResponse.Name] = queue[1:]

			for _, learning := range classifyDurableSessionLearnings(call, resp) {
				latest[learning.kind+":"+learning.key] = learning
			}
		}
	}

	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]sessionDurableLearning, 0, len(keys))
	for _, key := range keys {
		result = append(result, latest[key])
	}
	return result
}

func isSuccessfulFunctionResponse(resp map[string]any) bool {
	if resp == nil {
		return false
	}
	if success, ok := resp["success"].(bool); ok {
		return success
	}
	errMsg, _ := resp["error"].(string)
	return strings.TrimSpace(errMsg) == ""
}

func classifyDurableSessionLearnings(call sessionFunctionCall, resp map[string]any) []sessionDurableLearning {
	switch call.name {
	case "bash":
		cmd, _ := call.args["command"].(string)
		return classifyCommandLearnings(normalizeDurableCommandForPromotion(cmd))
	case "verify_code":
		content, _ := resp["content"].(string)
		return classifyCommandLearnings(extractVerifyCommand(content))
	default:
		return nil
	}
}

func normalizeDurableCommandForPromotion(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	if strings.HasPrefix(cmd, "cd ") {
		_, after, ok := strings.Cut(cmd, " && ")
		if !ok {
			return ""
		}
		cmd = strings.TrimSpace(after)
		if cmd == "" {
			return ""
		}
	}
	if len(cmd) < 5 || len(cmd) > 500 {
		return ""
	}
	return cmd
}

func extractVerifyCommand(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	const prefix = "Verification successful ("
	_, afterPrefix, ok := strings.Cut(content, prefix)
	if !ok {
		return ""
	}
	text, _, ok := strings.Cut(afterPrefix, "):")
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func classifyCommandLearnings(cmd string) []sessionDurableLearning {
	if cmd == "" {
		return nil
	}

	segments := splitPromotableCommandSegments(cmd)
	latest := make(map[string]sessionDurableLearning)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		lower := strings.ToLower(segment)
		switch {
		case isPromotableTestCommand(lower):
			latest["fact:test_command"] = sessionDurableLearning{kind: "fact", key: "test_command", value: segment}
		case isPromotableBuildCommand(lower):
			latest["fact:build_command"] = sessionDurableLearning{kind: "fact", key: "build_command", value: segment}
		case isPromotableLintCommand(lower):
			latest["fact:lint_command"] = sessionDurableLearning{kind: "fact", key: "lint_command", value: segment}
		case isPromotableFormatCommand(lower):
			latest["convention:format_command"] = sessionDurableLearning{kind: "convention", key: "format_command", value: segment}
		}
	}

	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]sessionDurableLearning, 0, len(keys))
	for _, key := range keys {
		result = append(result, latest[key])
	}
	return result
}

func splitPromotableCommandSegments(cmd string) []string {
	fields := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';'
	})
	var segments []string
	for _, field := range fields {
		for part := range strings.SplitSeq(field, "&&") {
			part = strings.TrimSpace(part)
			if part != "" {
				segments = append(segments, part)
			}
		}
	}
	return segments
}

func isPromotableTestCommand(lower string) bool {
	testMarkers := []string{
		"go test", "pytest", "python -m pytest", "python3 -m pytest",
		"npm test", "npm run test", "pnpm test", "pnpm run test",
		"yarn test", "cargo test", "bundle exec rspec", "jest", "vitest",
	}
	for _, marker := range testMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isPromotableBuildCommand(lower string) bool {
	buildMarkers := []string{
		"go build", "npm run build", "pnpm build", "pnpm run build",
		"yarn build", "cargo build", "make build",
	}
	for _, marker := range buildMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isPromotableLintCommand(lower string) bool {
	lintMarkers := []string{
		"go vet", "golangci-lint", "npm run lint", "pnpm lint", "pnpm run lint",
		"yarn lint", "cargo clippy", "ruff check", "eslint",
	}
	for _, marker := range lintMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isPromotableFormatCommand(lower string) bool {
	formatMarkers := []string{
		"gofmt", "goimports", "prettier", "cargo fmt", "ruff format",
		"black", "eslint --fix",
	}
	for _, marker := range formatMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func extractErrorLine(text, pattern string) string {
	idx := strings.Index(text, pattern)
	if idx < 0 {
		return ""
	}

	// Extract from pattern to end of line
	start := idx
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		end = len(text) - start
	}
	line := strings.TrimSpace(text[start : start+end])
	if runes := []rune(line); len(runes) > 150 {
		line = string(runes[:150]) + "..."
	}
	return line
}

// ClientSessionSummarizer wraps a client.Client to implement SessionSummarizer.
// It sends the conversation history with a summarization prompt to the LLM
// and returns the generated text summary.
type ClientSessionSummarizer struct {
	client client.Client
}

// NewClientSessionSummarizer creates a summarizer adapter from a Client.
func NewClientSessionSummarizer(c client.Client) *ClientSessionSummarizer {
	return &ClientSessionSummarizer{client: c}
}

func (s *ClientSessionSummarizer) Summarize(ctx context.Context, history []*genai.Content, prompt string) (string, error) {
	// Take last 20 messages for summarization (avoid sending entire history)
	msgs := history
	if len(msgs) > 20 {
		msgs = msgs[len(msgs)-20:]
	}

	stream, err := s.client.SendMessageWithHistory(ctx, msgs, prompt)
	if err != nil {
		return "", err
	}

	resp, err := stream.Collect()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Text), nil
}
