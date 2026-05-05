package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/genai"

	"gokin/internal/logging"
	"gokin/internal/security"
	"gokin/internal/undo"
)

const editContextMaxChars = 5000

// EditTool performs search/replace operations in files.
type EditTool struct {
	undoManager   *undo.Manager
	diffHandler   DiffHandler
	diffEnabled   bool
	workDir       string
	pathValidator *security.PathValidator

	// readTracker + requireReadBeforeEdit implement the "Read before Edit"
	// safety invariant. When both are set, Execute refuses to edit a file
	// that has no active read record in the session — a classic Kimi
	// failure mode: grep shows 3 lines of context, model edits blindly.
	// Disabled by default in tests / minimal harnesses; enabled via config
	// in the real app.
	readTracker            *FileReadTracker
	requireReadBeforeEdit  bool
}

// NewEditTool creates a new EditTool instance.
func NewEditTool(workDir string) *EditTool {
	t := &EditTool{
		workDir: workDir,
	}
	if workDir != "" {
		t.pathValidator = security.NewPathValidator([]string{workDir}, false)
	}
	return t
}

// SetUndoManager sets the undo manager for tracking changes.
func (t *EditTool) SetUndoManager(manager *undo.Manager) {
	t.undoManager = manager
}

// SetDiffHandler sets the diff handler for preview approval.
func (t *EditTool) SetDiffHandler(handler DiffHandler) {
	t.diffHandler = handler
}

// SetDiffEnabled enables or disables diff preview.
func (t *EditTool) SetDiffEnabled(enabled bool) {
	t.diffEnabled = enabled
}

// SetWorkDir sets the working directory and initializes path validator.
func (t *EditTool) SetWorkDir(workDir string) {
	t.workDir = workDir
	t.pathValidator = security.NewPathValidator([]string{workDir}, false)
}

// SetAllowedDirs sets additional allowed directories for path validation.
func (t *EditTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

// SetReadTracker wires the session-wide file read tracker so Execute can
// enforce the Read-before-Edit invariant.
func (t *EditTool) SetReadTracker(tracker *FileReadTracker) {
	t.readTracker = tracker
}

// SetRequireReadBeforeEdit toggles the enforcement. When false, edit goes
// through even if the file wasn't read — matches the pre-invariant
// behavior for users who explicitly opt out.
func (t *EditTool) SetRequireReadBeforeEdit(require bool) {
	t.requireReadBeforeEdit = require
}

// checkReadBeforeEdit returns a non-empty error message when the edit
// must be blocked. Returns "" when the check passes, is disabled, or
// can't run (missing deps, unresolvable path, nonexistent file).
func (t *EditTool) checkReadBeforeEdit(filePath string) string {
	if !t.requireReadBeforeEdit || t.readTracker == nil || t.pathValidator == nil {
		return ""
	}
	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		// Path-validation error gets a better message downstream in the
		// real edit flow — don't shadow it with "read first".
		return ""
	}
	if _, err := os.Stat(validPath); err != nil {
		// File doesn't exist (or unreadable). Edit will fail with a more
		// specific "file not found" below — preserve that diagnostic.
		return ""
	}
	if t.readTracker.HasBeenRead(validPath) {
		return ""
	}
	return fmt.Sprintf(
		"read-before-edit: call the read tool on %s first so you have the full surrounding context. Editing based on grep snippets regularly clobbers nearby code. After reading, retry the edit.",
		filePath,
	)
}

func (t *EditTool) Name() string {
	return "edit"
}

func (t *EditTool) Description() string {
	return `Performs string replacement in a file. IMPORTANT: Read the file first to get exact text.

Modes:
1. Exact match: {"file_path": "f.go", "old_string": "func old()", "new_string": "func new()"}
2. Replace all:  add "replace_all": true to replace every occurrence
3. Regex:        add "regex": true, old_string is a regex pattern
4. Line range:   {"file_path": "f.go", "line_start": 10, "line_end": 15, "new_string": "..."}
5. Insert:       {"file_path": "f.go", "insert_after_line": 5, "new_string": "new line"}
6. Multi-edit:   {"file_path": "f.go", "edits": [{"old_string": "a", "new_string": "b"}, ...]}

The old_string must EXACTLY match file content (whitespace matters). If not unique, provide more surrounding context.`
}

func (t *EditTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"file_path": {
					Type:        genai.TypeString,
					Description: "The absolute path to the file to edit",
				},
				"old_string": {
					Type:        genai.TypeString,
					Description: "The text to find and replace",
				},
				"new_string": {
					Type:        genai.TypeString,
					Description: "The text to replace with (must be different from old_string)",
				},
				"replace_all": {
					Type:        genai.TypeBoolean,
					Description: "If true, replace all occurrences. If false (default), old_string must be unique.",
				},
				"regex": {
					Type:        genai.TypeBoolean,
					Description: "If true, treat old_string as a regular expression pattern.",
				},
				"line_start": {
					Type:        genai.TypeInteger,
					Description: "Start line (1-indexed). Alternative to old_string: replaces lines line_start..line_end with new_string.",
				},
				"line_end": {
					Type:        genai.TypeInteger,
					Description: "End line (1-indexed, inclusive). Used with line_start.",
				},
				"insert_after_line": {
					Type:        genai.TypeInteger,
					Description: "Line number after which to insert new_string (0 = beginning of file). No lines are deleted.",
				},
				"edits": {
					Type:        genai.TypeArray,
					Description: "Array of {old_string, new_string} pairs for multiple edits in one call. Each edit is applied sequentially to the result of the previous one.",
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"old_string": {
								Type:        genai.TypeString,
								Description: "The text to find",
							},
							"new_string": {
								Type:        genai.TypeString,
								Description: "The text to replace with",
							},
						},
						Required: []string{"old_string", "new_string"},
					},
				},
			},
			Required: []string{"file_path"},
		},
	}
}

func (t *EditTool) Validate(args map[string]any) error {
	filePath, ok := GetString(args, "file_path")
	if !ok || filePath == "" {
		return NewValidationError("file_path", "is required")
	}
	_ = filePath

	// Multi-edit mode: edits array takes precedence
	if edits, ok := args["edits"].([]any); ok && len(edits) > 0 {
		for i, e := range edits {
			editMap, ok := e.(map[string]any)
			if !ok {
				return NewValidationError("edits", fmt.Sprintf("edit[%d] is not an object", i))
			}
			oldStr, _ := editMap["old_string"].(string)
			newStr, _ := editMap["new_string"].(string)
			if oldStr == "" {
				return NewValidationError("edits", fmt.Sprintf("edit[%d].old_string is required", i))
			}
			if oldStr == newStr {
				return NewValidationError("edits", fmt.Sprintf("edit[%d]: new_string must differ from old_string", i))
			}
		}
		return nil
	}

	// Insert mode
	if insertLine, hasInsert := GetInt(args, "insert_after_line"); hasInsert {
		if insertLine < 0 {
			return NewValidationError("insert_after_line", "must be >= 0")
		}
		if _, ok := GetString(args, "new_string"); !ok {
			return NewValidationError("new_string", "required for insert mode")
		}
		return nil
	}

	// Line-based edit mode
	if lineStart, hasStart := GetInt(args, "line_start"); hasStart && lineStart > 0 {
		if _, hasEnd := GetInt(args, "line_end"); !hasEnd {
			return NewValidationError("line_end", "required when line_start is provided")
		}
		if _, ok := GetString(args, "new_string"); !ok {
			return NewValidationError("new_string", "required for line-based editing")
		}
		return nil
	}

	// Single edit mode
	oldStr, ok := GetString(args, "old_string")
	if !ok || oldStr == "" {
		return NewValidationError("old_string", "is required (or provide edits array or line_start/line_end)")
	}

	newStr, ok := GetString(args, "new_string")
	if !ok {
		return NewValidationError("new_string", "is required")
	}

	if oldStr == newStr {
		return NewValidationError("new_string", "must be different from old_string")
	}

	return nil
}

func (t *EditTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	filePath, _ := GetString(args, "file_path")

	// Read-before-Edit invariant. Must run before the sub-flow dispatch
	// so it covers string/regex/line/insert/multi-edit uniformly. The
	// helper is a no-op when the tracker isn't wired (test harnesses) or
	// when the file doesn't exist (Edit on new file → clearer error
	// downstream).
	if msg := t.checkReadBeforeEdit(filePath); msg != "" {
		return NewErrorResult(msg), nil
	}

	// Check for multi-edit mode
	if edits, ok := args["edits"].([]any); ok && len(edits) > 0 {
		return t.executeMultiEdit(ctx, filePath, edits)
	}

	// Check for insert mode
	if insertLine, hasInsert := GetInt(args, "insert_after_line"); hasInsert {
		newStr, _ := GetString(args, "new_string")
		return t.executeInsertAfterLine(ctx, filePath, insertLine, newStr)
	}

	// Check for line-based edit mode
	if lineStart, hasStart := GetInt(args, "line_start"); hasStart && lineStart > 0 {
		lineEnd := GetIntDefault(args, "line_end", lineStart)
		newStr, _ := GetString(args, "new_string")
		return t.executeLineEdit(ctx, filePath, lineStart, lineEnd, newStr)
	}

	oldStr, _ := GetString(args, "old_string")
	newStr, _ := GetString(args, "new_string")
	replaceAll := GetBoolDefault(args, "replace_all", false)

	// Validate path (mandatory for security)
	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}

	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
	}
	filePath = validPath

	if err := security.IsBlockedWritePath(filePath); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	// Read existing file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	// Detect binary files by checking for null bytes in the first 512 bytes
	if slices.Contains(data[:min(len(data), 512)], byte(0)) {
		return NewErrorResult(fmt.Sprintf("cannot edit binary file: %s", filePath)), nil
	}

	content := string(data)
	oldContent := data // Save for undo
	useRegex := GetBoolDefault(args, "regex", false)

	var newContent string
	var count int

	if useRegex {
		// Regex mode
		re, err := regexp.Compile(oldStr)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("invalid regex pattern: %s", err)), nil
		}

		// Count matches
		matches := re.FindAllStringIndex(content, -1)
		count = len(matches)

		if count == 0 {
			errMsg := fmt.Sprintf("regex pattern not found in file: %s", filePath)
			fileCtx := extractFileContext(content, editContextMaxChars)
			return NewErrorResultWithContext(errMsg, fileCtx), nil
		}

		if count > 1 && !replaceAll {
			return NewErrorResult(
				buildAmbiguousRegexEditError(filePath, content, matches, count),
			), nil
		}

		// Perform regex replacement
		if replaceAll {
			newContent = re.ReplaceAllString(content, newStr)
		} else {
			// Replace first match only
			loc := re.FindStringIndex(content)
			if loc != nil {
				newContent = content[:loc[0]] + re.ReplaceAllString(content[loc[0]:loc[1]], newStr) + content[loc[1]:]
			} else {
				newContent = content // Safety fallback: no match, no change
			}
		}
	} else {
		// Literal mode (existing behavior)
		count = strings.Count(content, oldStr)

		if count == 0 {
			// Try progressive fuzzy matching (auto-apply on unique match)
			if result, strategy, err := tryFuzzyReplace(content, oldStr, newStr, replaceAll); err == nil {
				newContent = result
				logging.Debug("edit fuzzy match applied", "strategy", strategy, "file", filePath)
				count = 1 // Mark as matched for status message below

				// Show diff preview and wait for approval if enabled
				if t.diffEnabled && t.diffHandler != nil && !ShouldSkipDiff(ctx) {
					approved, approveErr := maybePromptDiff(ctx, t.diffHandler, filePath, content, newContent, "edit", false)
					if approveErr != nil {
						return NewErrorResult(fmt.Sprintf("diff preview error: %s", approveErr)), nil
					}
					if !approved {
						return NewErrorResult("changes rejected by user"), nil
					}
				}

				// Write back atomically
				newContentBytes := []byte(newContent)
				if err := AtomicWrite(filePath, newContentBytes, 0644); err != nil {
					return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
				}
				if t.undoManager != nil {
					change := undo.NewFileChange(filePath, "edit", oldContent, newContentBytes, false)
					t.undoManager.Record(*change)
				}

				status := fmt.Sprintf("Edited (fuzzy: %s): %s", strategy, filePath)
				return NewSuccessResult(status), nil
			}

			// Fuzzy matching failed — build a helpful error message.
			errMsg := fmt.Sprintf("old_string not found in file: %s", filePath)

			// 1. Check whitespace-only mismatch
			if actual, line := findFuzzyMatch(content, oldStr); actual != "" {
				errMsg += fmt.Sprintf("\n\nFuzzy match at line %d (whitespace differs). Actual text:\n```\n%s\n```\nUse this exact text as old_string.", line, actual)
			} else {
				// 2. Find the closest matching line(s) to help the model self-correct
				if bestLine, bestLineNum, score := findClosestLines(content, oldStr); score > 0.4 {
					errMsg += fmt.Sprintf("\n\nClosest match at line %d (%.0f%% similar):\n```\n%s\n```\nRead the file first to get the exact text.", bestLineNum, score*100, bestLine)
				} else {
					errMsg += "\n\nHint: Read the file first with the read tool to see current content."
				}
			}
			fileCtx := extractFileContext(content, editContextMaxChars)
			return NewErrorResultWithContext(errMsg, fileCtx), nil
		}

		if count > 1 && !replaceAll {
			// Multi-match ambiguity. Kimi typically retries blindly here;
			// give it enough structured info to pick precisely on the
			// next attempt: line numbers + a few chars of surrounding
			// context per match, plus an explicit reminder that line_start/
			// line_end mode disambiguates without needing more context.
			return NewErrorResult(
				buildAmbiguousEditError(filePath, content, oldStr, count, false),
			), nil
		}

		// Perform replacement
		if replaceAll {
			newContent = strings.ReplaceAll(content, oldStr, newStr)
		} else {
			newContent = strings.Replace(content, oldStr, newStr, 1)
		}
	}

	// Show diff preview and wait for approval if enabled
	// Skip diff approval when running in delegated plan execution (context flag)
	if t.diffEnabled && t.diffHandler != nil && !ShouldSkipDiff(ctx) {
		approved, err := maybePromptDiff(ctx, t.diffHandler, filePath, content, newContent, "edit", false)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("diff preview error: %s", err)), nil
		}
		if !approved {
			return NewErrorResult("changes rejected by user"), nil
		}
	}

	// Write back atomically to prevent data corruption on interruption
	newContentBytes := []byte(newContent)
	if err := AtomicWrite(filePath, newContentBytes, 0644); err != nil {
		return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
	}

	// Record change for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "edit", oldContent, newContentBytes, false)
		t.undoManager.Record(*change)
	}

	var status string
	if replaceAll {
		status = fmt.Sprintf("Replaced %d occurrence(s) in %s", count, filePath)
	} else {
		status = fmt.Sprintf("Replaced 1 occurrence in %s", filePath)
	}

	// Emit FilePeek
	EmitFilePeek(ctx, filePath, "Editing", newContent, "edit")

	return NewSuccessResult(status), nil
}

// executeMultiEdit applies multiple edits to a single file sequentially.
// Each edit operates on the result of the previous one.
func (t *EditTool) executeMultiEdit(ctx context.Context, filePath string, edits []any) (ToolResult, error) {
	// Validate path
	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}
	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
	}
	filePath = validPath

	if err := security.IsBlockedWritePath(filePath); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	content := string(data)
	oldContent := data
	totalReplacements := 0

	// Apply each edit sequentially
	for i, e := range edits {
		editMap, ok := e.(map[string]any)
		if !ok {
			return NewErrorResult(fmt.Sprintf("edit[%d] is not an object", i)), nil
		}

		oldStr, ok1 := editMap["old_string"].(string)
		newStr, ok2 := editMap["new_string"].(string)
		if !ok1 || oldStr == "" {
			return NewErrorResult(fmt.Sprintf("edit[%d]: old_string is required and must be a non-empty string", i)), nil
		}
		if !ok2 {
			newStr = "" // Allow deletion (replace with nothing)
		}

		count := strings.Count(content, oldStr)
		if count == 0 {
			// Try progressive fuzzy matching
			if result, strategy, err := tryFuzzyReplace(content, oldStr, newStr, false); err == nil {
				content = result
				logging.Debug("multi-edit fuzzy match applied", "strategy", strategy, "edit_index", i, "file", filePath)
				totalReplacements++
				continue
			}

			errMsg := fmt.Sprintf("edit[%d]: old_string not found in file after previous edits", i)
			if actual, line := findFuzzyMatch(content, oldStr); actual != "" {
				errMsg += fmt.Sprintf("\n\nFuzzy match at line %d. Actual text:\n```\n%s\n```", line, actual)
			}
			fileCtx := extractFileContext(content, editContextMaxChars)
			return NewErrorResultWithContext(errMsg, fileCtx), nil
		}

		content = strings.Replace(content, oldStr, newStr, 1)
		totalReplacements++
	}

	// Show combined diff preview
	if t.diffEnabled && t.diffHandler != nil && !ShouldSkipDiff(ctx) {
		approved, err := maybePromptDiff(ctx, t.diffHandler, filePath, string(oldContent), content, "edit", false)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("diff preview error: %s", err)), nil
		}
		if !approved {
			return NewErrorResult("changes rejected by user"), nil
		}
	}

	// Write atomically
	newContentBytes := []byte(content)
	if err := AtomicWrite(filePath, newContentBytes, 0644); err != nil {
		return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
	}

	// Record single undo for all edits
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "edit", oldContent, newContentBytes, false)
		t.undoManager.Record(*change)
	}

	// Emit FilePeek
	EmitFilePeek(ctx, filePath, "Editing", content, "edit")

	return NewSuccessResult(fmt.Sprintf("Applied %d edit(s) to %s", totalReplacements, filePath)), nil
}

// executeLineEdit replaces a range of lines in a file.
func (t *EditTool) executeLineEdit(ctx context.Context, filePath string, lineStart, lineEnd int, newStr string) (ToolResult, error) {
	// Validate path
	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}
	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
	}
	filePath = validPath

	if err := security.IsBlockedWritePath(filePath); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Validate range
	if lineStart < 1 {
		return NewErrorResult("line_start must be >= 1"), nil
	}
	if lineEnd < lineStart {
		return NewErrorResult(fmt.Sprintf("line_end (%d) must be >= line_start (%d)", lineEnd, lineStart)), nil
	}
	if lineStart > totalLines {
		errMsg := fmt.Sprintf("line_start (%d) exceeds file length (%d lines)", lineStart, totalLines)
		fileCtx := extractFileContext(content, editContextMaxChars)
		return NewErrorResultWithContext(errMsg, fileCtx), nil
	}

	// Clamp lineEnd to file length
	if lineEnd > totalLines {
		lineEnd = totalLines
	}

	// Build new content: lines before + new text + lines after
	var parts []string
	if lineStart > 1 {
		parts = append(parts, lines[:lineStart-1]...)
	}
	if newStr != "" {
		parts = append(parts, strings.Split(newStr, "\n")...)
	}
	if lineEnd < totalLines {
		parts = append(parts, lines[lineEnd:]...)
	}

	newContent := strings.Join(parts, "\n")

	// Show diff preview
	if t.diffEnabled && t.diffHandler != nil && !ShouldSkipDiff(ctx) {
		approved, err := maybePromptDiff(ctx, t.diffHandler, filePath, content, newContent, "edit", false)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("diff preview error: %s", err)), nil
		}
		if !approved {
			return NewErrorResult("changes rejected by user"), nil
		}
	}

	// Write atomically
	newContentBytes := []byte(newContent)
	if err := AtomicWrite(filePath, newContentBytes, 0644); err != nil {
		return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
	}

	// Record change for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "edit", data, newContentBytes, false)
		t.undoManager.Record(*change)
	}

	// Emit FilePeek
	EmitFilePeek(ctx, filePath, "Editing", newContent, "edit")

	replacedCount := lineEnd - lineStart + 1
	return NewSuccessResult(fmt.Sprintf("Replaced lines %d-%d (%d lines) in %s", lineStart, lineEnd, replacedCount, filePath)), nil
}

// executeInsertAfterLine inserts new text after the specified line without deleting anything.
// afterLine=0 inserts before the first line; afterLine=totalLines appends at the end.
func (t *EditTool) executeInsertAfterLine(ctx context.Context, filePath string, afterLine int, newStr string) (ToolResult, error) {
	// Validate path
	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}
	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
	}
	filePath = validPath

	if err := security.IsBlockedWritePath(filePath); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewErrorResult(fmt.Sprintf("file not found: %s", filePath)), nil
		}
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	// Detect binary files
	if slices.Contains(data[:min(len(data), 512)], byte(0)) {
		return NewErrorResult(fmt.Sprintf("cannot edit binary file: %s", filePath)), nil
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Validate afterLine
	if afterLine < 0 {
		return NewErrorResult("insert_after_line must be >= 0"), nil
	}
	if afterLine > totalLines {
		return NewErrorResult(fmt.Sprintf("insert_after_line (%d) exceeds file length (%d lines)", afterLine, totalLines)), nil
	}

	// Build new content: lines[:afterLine] + newLines + lines[afterLine:]
	newLines := strings.Split(newStr, "\n")
	var parts []string
	parts = append(parts, lines[:afterLine]...)
	parts = append(parts, newLines...)
	parts = append(parts, lines[afterLine:]...)
	newContent := strings.Join(parts, "\n")

	// Show diff preview
	if t.diffEnabled && t.diffHandler != nil && !ShouldSkipDiff(ctx) {
		approved, err := maybePromptDiff(ctx, t.diffHandler, filePath, content, newContent, "edit", false)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("diff preview error: %s", err)), nil
		}
		if !approved {
			return NewErrorResult("changes rejected by user"), nil
		}
	}

	// Write atomically
	newContentBytes := []byte(newContent)
	if err := AtomicWrite(filePath, newContentBytes, 0644); err != nil {
		return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
	}

	// Record change for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "edit", data, newContentBytes, false)
		t.undoManager.Record(*change)
	}

	// Emit FilePeek
	EmitFilePeek(ctx, filePath, "Inserting", newContent, "edit")

	return NewSuccessResult(fmt.Sprintf("Inserted %d lines after line %d in %s", len(newLines), afterLine, filePath)), nil
}

// extractFileContext formats file content with line numbers for error context.
func extractFileContext(content string, maxChars int) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i, line := range lines {
		s := fmt.Sprintf("%6d\t%s\n", i+1, line)
		if b.Len()+len(s) > maxChars {
			fmt.Fprintf(&b, "... (showing %d of %d lines)", i, len(lines))
			break
		}
		b.WriteString(s)
	}
	return b.String()
}

// findFuzzyMatch tries to find old_string in content after normalizing trailing whitespace.
// Returns the actual (unnormalized) text from the file and its starting line number.
// Returns ("", 0) if no unique normalized match is found.
func findFuzzyMatch(content, oldStr string) (string, int) {
	// Normalize both sides: trim trailing whitespace from each line
	normalizeLines := func(s string) string {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimRight(line, " \t\r")
		}
		return strings.Join(lines, "\n")
	}

	normalizedOld := normalizeLines(oldStr)
	normalizedContent := normalizeLines(content)

	// If normalization doesn't change either string, whitespace isn't the issue
	if normalizedOld == oldStr && normalizedContent == content {
		return "", 0
	}

	// Count normalized matches
	count := strings.Count(normalizedContent, normalizedOld)
	if count != 1 {
		return "", 0
	}

	// Find position in normalized content
	normPrefix, _, found := strings.Cut(normalizedContent, normalizedOld)
	if !found {
		return "", 0
	}

	// Map normalized position back to original content.
	// The line number of the match start in normalized content
	// equals the line number in original content.
	startLine := strings.Count(normPrefix, "\n")

	// Count how many lines the old_string spans
	oldLines := strings.Count(normalizedOld, "\n")
	endLine := startLine + oldLines

	// Extract the original lines
	contentLines := strings.Split(content, "\n")
	if startLine >= len(contentLines) {
		return "", 0
	}
	if endLine >= len(contentLines) {
		endLine = len(contentLines) - 1
	}

	actual := strings.Join(contentLines[startLine:endLine+1], "\n")
	return actual, startLine + 1 // 1-indexed line number
}

// findClosestLines finds the most similar contiguous block in content to oldStr.
// Uses a simple line-level similarity score (longest common subsequence ratio).
// Returns the best matching block, its 1-indexed line number, and similarity score (0-1).
func findClosestLines(content, oldStr string) (string, int, float64) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldStr, "\n")
	oldLineCount := len(oldLines)

	if oldLineCount == 0 || len(contentLines) == 0 {
		return "", 0, 0
	}

	// For single-line search, find best matching line
	if oldLineCount == 1 {
		target := strings.TrimSpace(oldLines[0])
		if target == "" {
			return "", 0, 0
		}
		bestScore := 0.0
		bestIdx := 0
		for i, line := range contentLines {
			score := lineSimilarity(target, strings.TrimSpace(line))
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestScore > 0.4 {
			return contentLines[bestIdx], bestIdx + 1, bestScore
		}
		return "", 0, 0
	}

	// For multi-line search, slide a window and score each position
	bestScore := 0.0
	bestIdx := 0
	for i := 0; i <= len(contentLines)-oldLineCount; i++ {
		score := blockSimilarity(oldLines, contentLines[i:i+oldLineCount])
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestScore > 0.4 {
		block := strings.Join(contentLines[bestIdx:bestIdx+oldLineCount], "\n")
		return block, bestIdx + 1, bestScore
	}
	return "", 0, 0
}

// lineSimilarity returns a similarity score (0-1) between two strings
// based on longest common subsequence length ratio.
func lineSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	lcs := lcsLength(a, b)
	return float64(lcs) / float64(max(len(a), len(b)))
}

// blockSimilarity returns the average line similarity between two same-length blocks.
func blockSimilarity(a, b []string) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var total float64
	for i := range a {
		total += lineSimilarity(strings.TrimSpace(a[i]), strings.TrimSpace(b[i]))
	}
	return total / float64(len(a))
}

// lcsLength computes the length of the longest common subsequence.
// Uses O(min(m,n)) space with two-row DP.
func lcsLength(a, b string) int {
	if len(a) > len(b) {
		a, b = b, a // ensure a is shorter
	}
	prev := make([]int, len(a)+1)
	curr := make([]int, len(a)+1)
	for j := 1; j <= len(b); j++ {
		for i := 1; i <= len(a); i++ {
			if a[i-1] == b[j-1] {
				curr[i] = prev[i-1] + 1
			} else if prev[i] > curr[i-1] {
				curr[i] = prev[i]
			} else {
				curr[i] = curr[i-1]
			}
		}
		prev, curr = curr, prev
		for i := range curr {
			curr[i] = 0
		}
	}
	return prev[len(a)]
}

// fuzzyStrategy defines a normalization strategy for progressive edit matching.
type fuzzyStrategy struct {
	name      string
	normalize func(string) string
}

// fuzzyStrategies is the ordered chain of normalization strategies.
var fuzzyStrategies = []fuzzyStrategy{
	{
		name: "TrailingWhitespace",
		normalize: func(s string) string {
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				lines[i] = strings.TrimRight(line, " \t\r")
			}
			return strings.Join(lines, "\n")
		},
	},
	{
		name: "LeadingWhitespace",
		normalize: func(s string) string {
			lines := strings.Split(s, "\n")
			for i, line := range lines {
				lines[i] = strings.TrimSpace(line)
			}
			return strings.Join(lines, "\n")
		},
	},
	{
		name: "WhitespaceCollapse",
		normalize: func(s string) string {
			lines := strings.Split(s, "\n")
			wsRun := regexp.MustCompile(`[ \t]+`)
			for i, line := range lines {
				lines[i] = strings.TrimSpace(wsRun.ReplaceAllString(line, " "))
			}
			return strings.Join(lines, "\n")
		},
	},
	{
		name: "BlankLines",
		normalize: func(s string) string {
			lines := strings.Split(s, "\n")
			wsRun := regexp.MustCompile(`[ \t]+`)
			var result []string
			for _, line := range lines {
				normalized := strings.TrimSpace(wsRun.ReplaceAllString(line, " "))
				if normalized != "" {
					result = append(result, normalized)
				}
			}
			return strings.Join(result, "\n")
		},
	},
}

// tryFuzzyReplace tries a chain of normalization strategies to find and replace old in content.
// Returns (newContent, strategyName, error).
// If no strategy finds a match, or a strategy finds ambiguous matches (>1 && !replaceAll), returns an error.
func tryFuzzyReplace(content, old, new string, replaceAll bool) (string, string, error) {
	contentLines := strings.Split(content, "\n")

	for _, strategy := range fuzzyStrategies {
		normalizedOld := strategy.normalize(old)
		normalizedContent := strategy.normalize(content)
		// Skip only if the strategy is a no-op for BOTH sides — then
		// there's nothing it can reconcile. Pre-fix: we skipped if old
		// was unchanged, which wrongly bailed on the common case where
		// `old` is clean (from the model) but `content` has dirty
		// whitespace (from disk). That scenario is exactly what this
		// fuzzy chain is supposed to handle.
		if normalizedOld == old && normalizedContent == content {
			continue
		}

		count := strings.Count(normalizedContent, normalizedOld)
		if count == 0 {
			continue
		}
		if count > 1 && !replaceAll {
			return "", strategy.name, fmt.Errorf(
				"fuzzy match (%s) found %d occurrences — ambiguous. Provide more context or set replace_all=true",
				strategy.name, count)
		}

		// Map normalized positions back to original lines and replace.
		normalizedLines := strings.Split(normalizedContent, "\n")
		normalizedOldLines := strings.Split(normalizedOld, "\n")
		normalizedNewLines := strings.Split(new, "\n")
		oldLineCount := len(normalizedOldLines)

		// Find all match start positions (by line index in normalizedLines)
		var matchStarts []int
		for i := 0; i <= len(normalizedLines)-oldLineCount; i++ {
			match := true
			for j := range oldLineCount {
				if normalizedLines[i+j] != normalizedOldLines[j] {
					match = false
					break
				}
			}
			if match {
				matchStarts = append(matchStarts, i)
			}
		}

		if len(matchStarts) == 0 {
			continue
		}
		if len(matchStarts) > 1 && !replaceAll {
			return "", strategy.name, fmt.Errorf(
				"fuzzy match (%s) found %d occurrences — ambiguous",
				strategy.name, len(matchStarts))
		}

		// Build result by replacing matched line ranges in the original content.
		// Process matches in reverse order so earlier indices stay valid.
		resultLines := make([]string, len(contentLines))
		copy(resultLines, contentLines)

		for mi := len(matchStarts) - 1; mi >= 0; mi-- {
			start := matchStarts[mi]
			end := start + oldLineCount
			// Replace the original lines [start:end) with new lines
			var newLines []string
			newLines = append(newLines, resultLines[:start]...)
			newLines = append(newLines, normalizedNewLines...)
			newLines = append(newLines, resultLines[end:]...)
			resultLines = newLines
		}

		return strings.Join(resultLines, "\n"), strategy.name, nil
	}

	return "", "", fmt.Errorf("no fuzzy strategy matched")
}

// ambiguousMatchContextLines is how many surrounding lines to show
// around each ambiguous match. Three each side is enough to show the
// enclosing function/block boundary without flooding the error.
const ambiguousMatchContextLines = 2

// buildAmbiguousEditError renders a structured error for string-mode
// edits that matched multiple times. Includes per-match line number +
// content snippet so the model can pick immediately using line_start/
// line_end mode, without having to re-read the file.
func buildAmbiguousEditError(filePath, content, oldStr string, count int, regex bool) string {
	lines := strings.Split(content, "\n")
	hits := findStringOccurrenceLines(lines, oldStr)
	return formatAmbiguousEditMessage(filePath, lines, hits, count, regex)
}

// buildAmbiguousRegexEditError is the regex equivalent. Takes the
// already-computed regex match byte-index pairs and maps them to line
// numbers for context extraction.
func buildAmbiguousRegexEditError(filePath, content string, matches [][]int, count int) string {
	lines := strings.Split(content, "\n")
	hits := lineNumbersForByteOffsets(content, matches)
	return formatAmbiguousEditMessage(filePath, lines, hits, count, true)
}

// findStringOccurrenceLines returns 1-based line numbers where the
// substring appears. A single "cross-line" old_string (contains "\n")
// is reported by its first line; the caller can still use line_start
// to address the match unambiguously.
func findStringOccurrenceLines(lines []string, substr string) []int {
	if strings.Contains(substr, "\n") {
		// Multi-line substring — scan by reconstructing the original
		// content and walking char-by-char. Cheap because content is
		// already bounded by edit-tool limits.
		content := strings.Join(lines, "\n")
		var hits []int
		offset := 0
		for {
			idx := strings.Index(content[offset:], substr)
			if idx < 0 {
				return hits
			}
			abs := offset + idx
			// Count newlines before `abs` → that's the 0-based line; +1
			// for humans.
			hits = append(hits, strings.Count(content[:abs], "\n")+1)
			offset = abs + len(substr)
		}
	}
	var hits []int
	for i, line := range lines {
		if strings.Contains(line, substr) {
			hits = append(hits, i+1)
		}
	}
	return hits
}

// lineNumbersForByteOffsets converts regex match byte-offsets to
// 1-based line numbers. Deduplicates consecutive matches that land on
// the same line (regex can match multiple times per line — the model
// only needs one anchor per line to disambiguate).
func lineNumbersForByteOffsets(content string, matches [][]int) []int {
	if len(matches) == 0 {
		return nil
	}
	var hits []int
	last := -1
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		line := strings.Count(content[:m[0]], "\n") + 1
		if line != last {
			hits = append(hits, line)
			last = line
		}
	}
	return hits
}

// formatAmbiguousEditMessage builds the final error string shared by
// string-mode and regex-mode ambiguity paths. The model sees:
//   - count and file path (problem summary)
//   - per-match line number + 2 lines of surrounding context
//   - two concrete next-action options (add more context OR switch to
//     line_start/line_end mode with a specific line number)
func formatAmbiguousEditMessage(filePath string, lines []string, hits []int, count int, regex bool) string {
	kind := "old_string"
	if regex {
		kind = "regex pattern"
	}
	if len(hits) == 0 {
		return fmt.Sprintf(
			"%s matches %d times in %s. Provide more surrounding context to make the match unique, or set replace_all=true.",
			kind, count, filePath,
		)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s matches %d times in %s — ambiguous. Matches at lines: ",
		kind, count, filePath)
	numStrs := make([]string, 0, len(hits))
	for _, h := range hits {
		numStrs = append(numStrs, fmt.Sprintf("%d", h))
	}
	sb.WriteString(strings.Join(numStrs, ", "))
	sb.WriteString("\n\n")

	// Cap the rendered-contexts at 5 to keep error size bounded on
	// pathological cases where the pattern matches 50+ times.
	renderLimit := min(len(hits), 5)
	for i := range renderLimit {
		ln := hits[i]
		sb.WriteString(renderLineContext(lines, ln, ambiguousMatchContextLines))
		sb.WriteString("\n")
	}
	if len(hits) > renderLimit {
		fmt.Fprintf(&sb, "(showing first %d of %d matches)\n\n", renderLimit, len(hits))
	}

	sb.WriteString("Next action (pick one):\n")
	sb.WriteString("  A) Re-issue edit with more surrounding context in old_string so it uniquely matches one site.\n")
	fmt.Fprintf(&sb, "  B) Switch to line-range mode: {file_path, line_start: %d, line_end: ..., new_string: ...}.\n", hits[0])
	sb.WriteString("  C) If every occurrence should change, set replace_all=true.\n")
	return sb.String()
}

// renderLineContext extracts `before+1+after` lines around 1-based
// `lineNum`, each prefixed with its line number and a marker ("→") on
// the target line. Used by the ambiguity error to give the model
// enough context to pick precisely.
func renderLineContext(lines []string, lineNum, around int) string {
	if lineNum < 1 || lineNum > len(lines) {
		return ""
	}
	start := max(lineNum-around, 1)
	end := min(lineNum+around, len(lines))
	var sb strings.Builder
	for i := start; i <= end; i++ {
		marker := "  "
		if i == lineNum {
			marker = "→ "
		}
		fmt.Fprintf(&sb, "%s%d: %s\n", marker, i, lines[i-1])
	}
	return sb.String()
}
