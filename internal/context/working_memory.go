package context

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/logging"
)

const (
	workingMemoryMaxItems        = 4
	workingMemoryMaxListItems    = 4
	workingMemoryMaxLineChars    = 220
	workingMemoryMaxSummaryChars = 180
)

var workingMemoryMarkdownLinkPattern = regexp.MustCompile(`\[(.*?)\]\([^)]+\)`)

// WorkingMemoryTurn is the compact turn-level evidence used to refresh the
// persistent working memory snapshot between requests.
type WorkingMemoryTurn struct {
	UserMessage  string
	Response     string
	ToolsUsed    []string
	TouchedPaths []string
	Commands     []string
}

// WorkingMemoryProvider exposes compact turn-state memory for prompt injection.
type WorkingMemoryProvider interface {
	GetContent() string
}

// WorkingMemoryManager maintains a small, agent-managed markdown snapshot of
// the current task state. Unlike project memory, this is ephemeral and is
// rewritten from the latest successful turn.
type WorkingMemoryManager struct {
	workDir string
	content string
	mu      sync.RWMutex
}

// NewWorkingMemoryManager creates a new persistent working memory manager.
func NewWorkingMemoryManager(workDir string) *WorkingMemoryManager {
	return &WorkingMemoryManager{workDir: workDir}
}

// UpdateFromTurn refreshes the working memory from the latest successful turn.
// Returns true when content changed and was persisted.
func (w *WorkingMemoryManager) UpdateFromTurn(turn WorkingMemoryTurn) bool {
	content := renderWorkingMemory(turn)
	if content == "" {
		return false
	}

	w.mu.Lock()
	if content == w.content {
		w.mu.Unlock()
		return false
	}
	w.content = content
	w.mu.Unlock()

	w.writeToDisk()
	return true
}

// GetContent returns the latest persisted working memory markdown.
func (w *WorkingMemoryManager) GetContent() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.content
}

// LoadFromDisk restores previously persisted working memory.
func (w *WorkingMemoryManager) LoadFromDisk() {
	data, err := os.ReadFile(w.filePath())
	if err != nil {
		return
	}

	w.mu.Lock()
	w.content = string(data)
	w.mu.Unlock()
	logging.Debug("loaded working memory from disk", "path", w.filePath())
}

// Clear removes the persisted working memory snapshot.
func (w *WorkingMemoryManager) Clear() {
	w.mu.Lock()
	w.content = ""
	w.mu.Unlock()
	_ = os.Remove(w.filePath())
}

func (w *WorkingMemoryManager) filePath() string {
	dir := filepath.Join(w.workDir, ".gokin")
	return filepath.Join(dir, ".working-memory.md")
}

func (w *WorkingMemoryManager) writeToDisk() {
	dir := filepath.Join(w.workDir, ".gokin")
	if err := os.MkdirAll(dir, 0750); err != nil {
		logging.Debug("failed to create .gokin dir for working memory", "error", err)
		return
	}

	w.mu.RLock()
	content := w.content
	w.mu.RUnlock()

	if err := fileutil.AtomicWrite(w.filePath(), []byte(content), 0640); err != nil {
		logging.Debug("failed to write working memory", "error", err)
	}
}

func renderWorkingMemory(turn WorkingMemoryTurn) string {
	established := buildWorkingMemoryEstablished(turn)
	unknown := buildWorkingMemoryUnknown(turn.Response)
	next := buildWorkingMemoryNext(turn.Response)

	if len(established) == 0 && len(unknown) == 0 && len(next) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("# Working Memory\n")
	builder.WriteString(fmt.Sprintf("_Updated: %s_\n\n", time.Now().Format("15:04")))

	appendWorkingMemorySection(&builder, "Established", established)
	appendWorkingMemorySection(&builder, "Unknown", unknown)
	appendWorkingMemorySection(&builder, "Next", next)

	return strings.TrimSpace(builder.String())
}

func appendWorkingMemorySection(builder *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	builder.WriteString("## ")
	builder.WriteString(title)
	builder.WriteString("\n")
	for _, item := range items {
		builder.WriteString("- ")
		builder.WriteString(item)
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
}

func buildWorkingMemoryEstablished(turn WorkingMemoryTurn) []string {
	items := make([]string, 0, workingMemoryMaxItems)

	if summary := extractWorkingMemorySummary(turn.Response); summary != "" {
		items = append(items, "Latest result: "+summary)
	}
	if len(turn.TouchedPaths) > 0 {
		items = append(items, "Files changed: "+formatWorkingMemoryList(turn.TouchedPaths, workingMemoryMaxListItems))
	}
	if len(turn.Commands) > 0 {
		label := "Successful commands"
		if workingMemoryCommandsContainVerificationSignals(turn.Commands) {
			label = "Verification already run"
		}
		items = append(items, label+": "+formatWorkingMemoryList(turn.Commands, 2))
	} else if len(turn.ToolsUsed) > 0 {
		items = append(items, "Tools already used: "+formatWorkingMemoryList(uniqueWorkingMemoryItems(turn.ToolsUsed), workingMemoryMaxListItems))
	}

	return uniqueWorkingMemoryItems(items)
}

func buildWorkingMemoryUnknown(response string) []string {
	lines := workingMemoryCandidateLines(response)
	items := make([]string, 0, workingMemoryMaxItems)
	for _, line := range lines {
		if !looksLikeWorkingMemoryUnknown(line) {
			continue
		}
		items = append(items, compactWorkingMemoryLine(line, workingMemoryMaxLineChars))
		if len(items) >= workingMemoryMaxItems {
			break
		}
	}
	return uniqueWorkingMemoryItems(items)
}

func buildWorkingMemoryNext(response string) []string {
	lines := workingMemoryCandidateLines(response)
	items := make([]string, 0, workingMemoryMaxItems)
	for _, line := range lines {
		if !looksLikeWorkingMemoryNext(line) {
			continue
		}
		items = append(items, compactWorkingMemoryLine(line, workingMemoryMaxLineChars))
		if len(items) >= workingMemoryMaxItems {
			break
		}
	}
	return uniqueWorkingMemoryItems(items)
}

func extractWorkingMemorySummary(response string) string {
	for _, line := range workingMemoryCandidateLines(response) {
		if looksLikeWorkingMemoryUnknown(line) || looksLikeWorkingMemoryNext(line) {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "verification") || strings.HasPrefix(lower, "провер") {
			continue
		}
		return compactWorkingMemoryLine(line, workingMemoryMaxSummaryChars)
	}
	return ""
}

func workingMemoryCandidateLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, raw := range rawLines {
		line := cleanWorkingMemoryLine(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "```") {
			continue
		}
		lines = append(lines, line)
	}

	if len(lines) > workingMemoryMaxItems*3 {
		return lines[:workingMemoryMaxItems*3]
	}
	return lines
}

func cleanWorkingMemoryLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	line = workingMemoryMarkdownLinkPattern.ReplaceAllString(line, "$1")
	line = strings.TrimLeft(line, "#")
	line = strings.TrimSpace(line)

	for {
		switch {
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case len(line) >= 3 && line[0] >= '0' && line[0] <= '9' && line[1] == '.' && line[2] == ' ':
			line = strings.TrimSpace(line[3:])
		default:
			line = strings.TrimSpace(strings.ReplaceAll(line, "`", ""))
			line = strings.Join(strings.Fields(line), " ")
			return line
		}
	}
}

func looksLikeWorkingMemoryUnknown(line string) bool {
	lower := strings.ToLower(line)
	markers := []string{
		"not verified", "not run", "unable", "couldn't", "could not", "cannot confirm", "can't confirm",
		"unclear", "unknown", "remaining risk", "risk:", "left unverified",
		"не провер", "не запуск", "не смог", "не удалось", "неяс", "риск",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeWorkingMemoryNext(line string) bool {
	lower := strings.ToLower(line)
	markers := []string{
		"next step", "next:", "follow-up", "follow up", "remaining step", "if you want",
		"can also", "i can also", "could also", "next i can", "from here",
		"дальше", "следующ", "если хочешь", "могу еще", "следующим шагом", "потом можно",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func workingMemoryCommandsContainVerificationSignals(commands []string) bool {
	keywords := []string{
		" test", "go test", "pytest", "cargo test", "npm test", "pnpm test", "yarn test", "bun test",
		"lint", "typecheck", "check", "verify", "vet", "build", "compile",
	}
	for _, command := range commands {
		lower := " " + strings.ToLower(strings.TrimSpace(command))
		for _, keyword := range keywords {
			if strings.Contains(lower, keyword) {
				return true
			}
		}
	}
	return false
}

func formatWorkingMemoryList(items []string, limit int) string {
	items = uniqueWorkingMemoryItems(items)
	if len(items) == 0 {
		return ""
	}
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:limit], ", ") + fmt.Sprintf(", ...(+%d)", len(items)-limit)
}

func compactWorkingMemoryLine(line string, maxChars int) string {
	line = cleanWorkingMemoryLine(line)
	if len(line) <= maxChars {
		return line
	}
	return strings.TrimSpace(line[:maxChars]) + "..."
}

func uniqueWorkingMemoryItems(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}
