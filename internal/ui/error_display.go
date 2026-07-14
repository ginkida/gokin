package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gokin/internal/config"

	"github.com/charmbracelet/lipgloss"
)

// EnhancedError provides rich error information with context and suggestions.
type EnhancedError struct {
	OriginalError error
	Category      ErrorCategory
	Context       string   // What operation was being performed
	Suggestions   []string // Actionable suggestions
	RetryInfo     *RetryInfo
	RelatedFiles  []string // Files involved in the error
	Documentation string   // Link or reference to documentation
}

// ErrorCategory categorizes errors for appropriate handling and display.
type ErrorCategory string

const (
	ErrorCategoryNetwork    ErrorCategory = "network"
	ErrorCategoryPermission ErrorCategory = "permission"
	ErrorCategorySyntax     ErrorCategory = "syntax"
	ErrorCategoryFile       ErrorCategory = "file"
	ErrorCategoryTimeout    ErrorCategory = "timeout"
	ErrorCategoryAuth       ErrorCategory = "auth"
	ErrorCategoryConfig     ErrorCategory = "config"
	ErrorCategoryRateLimit  ErrorCategory = "rate_limit"
	ErrorCategoryAPI        ErrorCategory = "api"
	ErrorCategoryGit        ErrorCategory = "git"
	ErrorCategoryUnknown    ErrorCategory = "unknown"
)

// RetryInfo contains information about retry attempts.
type RetryInfo struct {
	AttemptNumber int
	MaxAttempts   int
	NextRetryIn   time.Duration
	CanRetry      bool
	RetryReason   string
}

// ErrorDisplayModel renders enhanced error information.
type ErrorDisplayModel struct {
	error    *EnhancedError
	width    int
	expanded bool
}

// NewErrorDisplayModel creates a new error display model.
func NewErrorDisplayModel(err *EnhancedError) ErrorDisplayModel {
	if err == nil {
		return ErrorDisplayModel{width: 80}
	}
	snapshot := *err
	snapshot.Context = strings.TrimSpace(snapshot.Context)
	snapshot.Documentation = strings.TrimSpace(snapshot.Documentation)
	snapshot.Suggestions = compactNonEmptyStrings(snapshot.Suggestions)
	snapshot.RelatedFiles = compactNonEmptyStrings(snapshot.RelatedFiles)
	if err.RetryInfo != nil {
		retry := *err.RetryInfo
		retry.RetryReason = strings.TrimSpace(retry.RetryReason)
		snapshot.RetryInfo = &retry
	}
	return ErrorDisplayModel{error: &snapshot, width: 80}
}

func compactNonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

// SetWidth sets the display width.
func (m *ErrorDisplayModel) SetWidth(width int) {
	m.width = max(width, 1)
}

// SetExpanded sets whether to show full details.
func (m *ErrorDisplayModel) SetExpanded(expanded bool) {
	m.expanded = expanded
}

// View renders the enhanced error display — compact, no box.
// Format:
//
//	✗ API error: rate limit exceeded
//	  ↳ Wait 30s and retry, or check your API quota
func (m ErrorDisplayModel) View() string {
	if m.error == nil {
		return ""
	}

	errorStyle := lipgloss.NewStyle().Foreground(ColorRose)
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var rows []string
	width := max(m.width, 1)
	rows = append(rows, renderErrorRow(MessageIcons["error"]+" "+m.getCategoryTitle()+": ", m.errorMessage(), errorStyle, hintStyle, width))
	if context := strings.TrimSpace(m.error.Context); context != "" {
		rows = append(rows, renderErrorRow("  ↳ While: ", context, markerStyle, hintStyle, width))
	}
	if len(m.error.Suggestions) > 0 {
		rows = append(rows, renderErrorRow("  ↳ ", m.error.Suggestions[0], markerStyle, hintStyle, width))
	}
	if ri := m.error.RetryInfo; ri != nil && ri.CanRetry {
		attempt := max(ri.AttemptNumber, 0)
		maxAttempts := max(ri.MaxAttempts, attempt)
		retry := "Retry available"
		if maxAttempts > 0 {
			retry = fmt.Sprintf("Retry %d/%d", attempt, maxAttempts)
		}
		if ri.RetryReason != "" {
			retry += " · " + ri.RetryReason
		}
		if ri.NextRetryIn > 0 {
			next := ri.NextRetryIn.Round(time.Second)
			if next == 0 {
				retry += " · next in <1s"
			} else {
				retry += fmt.Sprintf(" · next in %s", next)
			}
		}
		rows = append(rows, renderErrorRow("  ↳ ", retry, markerStyle, hintStyle, width))
	}
	if m.expanded {
		for i := 1; i < len(m.error.Suggestions); i++ {
			rows = append(rows, renderErrorRow("  ↳ ", m.error.Suggestions[i], markerStyle, hintStyle, width))
		}
		for _, file := range m.error.RelatedFiles {
			rows = append(rows, renderErrorRow("    ", file, markerStyle, hintStyle, width))
		}
		if m.error.Documentation != "" {
			rows = append(rows, renderErrorRow("  See: ", m.error.Documentation, markerStyle, hintStyle, width))
		}
	}
	return strings.Join(rows, "\n") + "\n"
}

// ViewCompact renders a compact single-line error display.
func (m ErrorDisplayModel) ViewCompact() string {
	if m.error == nil {
		return ""
	}

	errorStyle := lipgloss.NewStyle().Foreground(ColorRose)
	msgStyle := lipgloss.NewStyle().Foreground(ColorDim)

	message := m.errorMessage()
	if len(m.error.Suggestions) > 0 {
		message += " → " + strings.TrimSpace(m.error.Suggestions[0])
	}
	return renderErrorRow("✗ ", message, errorStyle, msgStyle, max(m.width, 1))
}

func (m ErrorDisplayModel) errorMessage() string {
	if m.error == nil || m.error.OriginalError == nil {
		return "Unknown error"
	}
	message := strings.TrimSpace(m.error.OriginalError.Error())
	if message == "" {
		return "Unknown error"
	}
	return strings.Join(strings.Fields(message), " ")
}

func renderErrorRow(prefix, text string, prefixStyle, textStyle lipgloss.Style, width int) string {
	width = max(width, 1)
	prefix = truncateForWidth(prefix, width)
	prefixWidth := lipgloss.Width(prefix)
	if prefixWidth >= width {
		return prefixStyle.Render(prefix)
	}
	text = strings.Join(strings.Fields(text), " ")
	return prefixStyle.Render(prefix) + textStyle.Render(truncateForWidth(text, width-prefixWidth))
}

func (m ErrorDisplayModel) getCategoryTitle() string {
	titles := map[ErrorCategory]string{
		ErrorCategoryNetwork:    "Network Error",
		ErrorCategoryPermission: "Permission Denied",
		ErrorCategorySyntax:     "Syntax Error",
		ErrorCategoryFile:       "File Error",
		ErrorCategoryTimeout:    "Timeout",
		ErrorCategoryAuth:       "Authentication Failed",
		ErrorCategoryConfig:     "Configuration Error",
		ErrorCategoryRateLimit:  "Rate Limited",
		ErrorCategoryAPI:        "API Error",
		ErrorCategoryGit:        "Git Error",
		ErrorCategoryUnknown:    "Error",
	}
	if title, ok := titles[m.error.Category]; ok {
		return title
	}
	return "Error"
}

// ClassifyError analyzes an error and returns an EnhancedError with context.
func ClassifyError(err error, context string) *EnhancedError {
	if err == nil {
		return nil
	}

	enhanced := &EnhancedError{
		OriginalError: err,
		Context:       context,
		Category:      ErrorCategoryUnknown,
	}

	errStr := strings.ToLower(err.Error())

	// Classify based on error message patterns
	switch {
	case containsAny(errStr, "permission denied", "access denied", "eacces", "operation not permitted"):
		enhanced.Category = ErrorCategoryPermission
		enhanced.Suggestions = []string{
			"Check file permissions with: ls -la <path>",
			"Fix with: chmod u+rw <path> (or chmod -R for directories)",
			"If system file: use sudo or check ownership with ls -la",
		}

	case containsAny(errStr, "connection refused", "no such host", "network unreachable", "dial tcp", "dns"):
		enhanced.Category = ErrorCategoryNetwork
		enhanced.Suggestions = []string{
			"Check your internet connection",
			"Verify the API endpoint is correct",
			"Check if a firewall is blocking the connection",
		}

	case containsAny(errStr, "timeout", "deadline exceeded", "context deadline"):
		enhanced.Category = ErrorCategoryTimeout
		enhanced.Suggestions = []string{
			"The operation took too long - try again",
			"Consider increasing the timeout in config",
			"Check if the server is responding slowly",
		}

	case containsAny(errStr, "no such file", "not found", "does not exist", "enoent"):
		enhanced.Category = ErrorCategoryFile
		enhanced.Suggestions = []string{
			"Verify the file path is correct (typo?)",
			"Search for it: use /glob or /grep to locate the file",
			"The file may have been moved or deleted",
		}

	case containsAny(errStr, "unauthorized", "401", "invalid.*key", "api.*key.*invalid"):
		enhanced.Category = ErrorCategoryAuth
		enhanced.Suggestions = getAuthSuggestions()
		enhanced.Documentation = ""

	case containsAny(errStr, "rate limit", "429", "too many requests", "quota"):
		enhanced.Category = ErrorCategoryRateLimit
		enhanced.Suggestions = []string{
			"Wait a moment before trying again",
			"Reduce request frequency",
			"Consider upgrading your API plan",
		}

	case containsAny(errStr, "syntax", "parse", "unexpected token", "invalid json"):
		enhanced.Category = ErrorCategorySyntax
		enhanced.Suggestions = []string{
			"Check the syntax of your input",
			"Verify JSON/YAML formatting is correct",
			"Look for missing brackets or quotes",
		}

	case containsAny(errStr, "config", "configuration", "yaml", "invalid option"):
		enhanced.Category = ErrorCategoryConfig
		enhanced.Suggestions = []string{
			"Check your config file: ~/.config/gokin/config.yaml",
			"Run /setup to reconfigure interactively",
			"Run /doctor to diagnose setup issues",
		}
		enhanced.Documentation = "~/.config/gokin/config.yaml"

	case containsAny(errStr, "git", "not a git repository", "fatal:"):
		enhanced.Category = ErrorCategoryGit
		enhanced.Suggestions = []string{
			"Make sure you're in a git repository",
			"Initialize with 'git init' if needed",
			"Check if git is installed and in PATH",
		}

	case containsAny(errStr, "api", "500", "502", "503", "server error", "internal error"):
		enhanced.Category = ErrorCategoryAPI
		enhanced.Suggestions = []string{
			"The API server may be experiencing issues",
			"Try again in a few moments",
			"Check the API status page for outages",
		}
	}

	return enhanced
}

// ClassifyErrorWithRetry creates an enhanced error with retry information.
func ClassifyErrorWithRetry(err error, context string, attempt, maxAttempts int, nextRetry time.Duration) *EnhancedError {
	enhanced := ClassifyError(err, context)
	if enhanced == nil {
		return nil
	}

	enhanced.RetryInfo = &RetryInfo{
		AttemptNumber: attempt,
		MaxAttempts:   maxAttempts,
		NextRetryIn:   nextRetry,
		CanRetry:      attempt < maxAttempts,
	}

	// Add retry-specific reason based on category
	switch enhanced.Category {
	case ErrorCategoryNetwork:
		enhanced.RetryInfo.RetryReason = "Network issues are often transient"
	case ErrorCategoryTimeout:
		enhanced.RetryInfo.RetryReason = "Server may be temporarily overloaded"
	case ErrorCategoryRateLimit:
		enhanced.RetryInfo.RetryReason = "Waiting for rate limit reset"
	case ErrorCategoryAPI:
		enhanced.RetryInfo.RetryReason = "Server error - may be temporary"
	}

	return enhanced
}

// AddRelatedFiles adds file information to an enhanced error.
func (e *EnhancedError) AddRelatedFiles(files ...string) *EnhancedError {
	e.RelatedFiles = append(e.RelatedFiles, files...)
	return e
}

// AddSuggestion adds a suggestion to the error.
func (e *EnhancedError) AddSuggestion(suggestion string) *EnhancedError {
	e.Suggestions = append(e.Suggestions, suggestion)
	return e
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// getAuthSuggestions returns auth error suggestions with env var detection.
func getAuthSuggestions() []string {
	suggestions := []string{}

	found := false
	for _, p := range config.Providers {
		for _, envVar := range p.EnvVars {
			if os.Getenv(envVar) != "" {
				suggestions = append(suggestions, fmt.Sprintf("Found %s (%s) — verify it is valid", envVar, p.DisplayName))
				found = true
				break
			}
		}
	}
	if os.Getenv("GOKIN_API_KEY") != "" {
		suggestions = append(suggestions, "Found GOKIN_API_KEY (legacy) — verify it is valid")
		found = true
	}

	if !found {
		// Default fallback was "GEMINI_API_KEY" — leftover from v0.65 when
		// Gemini was removed. Now defaults to the first registered
		// provider's primary env var (typically GOKIN_GLM_KEY).
		envHint := "GOKIN_GLM_KEY"
		if ps := config.Providers; len(ps) > 0 && len(ps[0].EnvVars) > 0 {
			envHint = ps[0].EnvVars[0]
		}
		suggestions = append(suggestions, fmt.Sprintf("No API key env vars detected. Set %s or run gokin --setup", envHint))
	}

	suggestions = append(suggestions,
		"Check if the API key has expired",
		"Run 'gokin --setup' to reconfigure credentials",
	)

	return suggestions
}

// FormatEnhancedError formats an enhanced error for display using the given styles.
func FormatEnhancedError(styles *Styles, err *EnhancedError) string {
	model := NewErrorDisplayModel(err)
	model.SetWidth(80)
	return model.View()
}

// FormatEnhancedErrorCompact formats an enhanced error in compact form.
func FormatEnhancedErrorCompact(styles *Styles, err *EnhancedError) string {
	model := NewErrorDisplayModel(err)
	return model.ViewCompact()
}
