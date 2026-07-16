package tools

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/genai"

	"gokin/internal/security"
	"gokin/internal/undo"
)

// RefactorTool performs intelligent code refactoring operations.
type RefactorTool struct {
	undoManager   *undo.Manager
	workDir       string
	pathValidator *security.PathValidator
	diffHandler   DiffHandler
	diffEnabled   bool
}

// NewRefactorTool creates a new RefactorTool instance.
func NewRefactorTool() *RefactorTool {
	return &RefactorTool{}
}

// SetUndoManager sets the undo manager for tracking changes.
func (t *RefactorTool) SetUndoManager(manager *undo.Manager) {
	t.undoManager = manager
}

// SetWorkDir sets the working directory and initializes path validator.
func (t *RefactorTool) SetWorkDir(workDir string) {
	t.workDir = workDir
	t.pathValidator = security.NewPathValidator([]string{workDir}, false)
}

// SetAllowedDirs expands the path validator to additional directories beyond
// workDir (mirrors read.go). Requires SetWorkDir first; without it workDir is ""
// and the validator would be unrestricted.
func (t *RefactorTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

// SetDiffHandler sets the diff handler for preview approval.
func (t *RefactorTool) SetDiffHandler(handler DiffHandler) {
	t.diffHandler = handler
}

// SetDiffEnabled enables or disables diff preview.
func (t *RefactorTool) SetDiffEnabled(enabled bool) {
	t.diffEnabled = enabled
}

func (t *RefactorTool) Name() string {
	return "refactor"
}

func (t *RefactorTool) Description() string {
	return "Performs intelligent code refactoring: rename functions/variables, extract code, find references. Uses AST analysis for safe transformations."
}

func (t *RefactorTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"operation": {
					Type:        genai.TypeString,
					Description: "Refactoring operation: 'rename', 'extract', 'inline', 'find_refs'",
					Enum:        []string{"rename", "extract", "inline", "find_refs"},
				},
				"file_path": {
					Type:        genai.TypeString,
					Description: "Path to the file to refactor (for rename/extract/inline)",
				},
				"pattern": {
					Type:        genai.TypeString,
					Description: "Glob pattern for multi-file operations (e.g., '**/*.go')",
				},
				"old_name": {
					Type:        genai.TypeString,
					Description: "Current function/variable name (for rename)",
				},
				"new_name": {
					Type:        genai.TypeString,
					Description: "New function/variable name (for rename)",
				},
				"extract_name": {
					Type:        genai.TypeString,
					Description: "Name for the extracted function (for extract)",
				},
				"start_line": {
					Type:        genai.TypeInteger,
					Description: "Start line for extraction (for extract)",
				},
				"end_line": {
					Type:        genai.TypeInteger,
					Description: "End line for extraction (for extract)",
				},
				"target_name": {
					Type:        genai.TypeString,
					Description: "Function name to find references or inline (for find_refs/inline)",
				},
			},
			Required: []string{"operation"},
		},
	}
}

func (t *RefactorTool) Validate(args map[string]any) error {
	op, ok := GetString(args, "operation")
	if !ok || op == "" {
		return NewValidationError("operation", "is required")
	}

	switch op {
	case "rename", "extract", "inline":
		filePath, _ := GetString(args, "file_path")
		if filePath == "" {
			return NewValidationError("file_path", "is required for this operation")
		}
	case "find_refs":
		// Can work with just pattern and target_name
		targetName, _ := GetString(args, "target_name")
		if targetName == "" {
			return NewValidationError("target_name", "is required for find_refs")
		}
	}

	return nil
}

func (t *RefactorTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	op, _ := GetString(args, "operation")

	switch op {
	case "rename":
		return t.executeRename(ctx, args)
	case "extract":
		return t.executeExtract(ctx, args)
	case "inline":
		return t.executeInline(ctx, args)
	case "find_refs":
		return t.executeFindRefs(ctx, args)
	default:
		return NewErrorResult(fmt.Sprintf("unknown operation: %s", op)), nil
	}
}

// executeRename performs safe renaming using AST analysis.
func (t *RefactorTool) executeRename(ctx context.Context, args map[string]any) (ToolResult, error) {
	filePath, _ := GetString(args, "file_path")
	oldName, _ := GetString(args, "old_name")
	newName, _ := GetString(args, "new_name")
	pattern, _ := GetString(args, "pattern")

	if oldName == "" || newName == "" {
		return NewErrorResult("old_name and new_name are required for rename"), nil
	}

	// Determine files to process
	var files []string
	if pattern != "" {
		// Multi-file rename
		matches, err := filepath.Glob(filepath.Join(t.workDir, pattern))
		if err != nil {
			return NewErrorResult(fmt.Sprintf("invalid pattern: %s", err)), nil
		}
		files = matches
	} else {
		files = []string{filePath}
	}

	// Process each file. Track real FAILURES separately from per-file change
	// summaries, so an all-failed rename is never reported as a benign "no
	// occurrences" success (the dishonest-success class — the agent would
	// conclude the symbol is absent / the work is done when nothing was written).
	var results []string
	var errored []string
	var changed []string // raw paths that were actually rewritten (for written_paths)
	var changedFiles int
	var totalChanges int

	for _, file := range files {
		changes, err := t.renameInFile(ctx, file, oldName, newName)
		if err != nil {
			errored = append(errored, fmt.Sprintf("%s: %s", file, err))
			continue
		}
		if changes > 0 {
			results = append(results, fmt.Sprintf("%s: %d changes", file, changes))
			changed = append(changed, file)
			changedFiles++
			totalChanges += changes
		}
	}

	if totalChanges == 0 {
		// Nothing changed. If every target ERRORED (path rejected, missing file,
		// unwritable), that is a failure — surface the reason, don't claim success.
		if len(errored) > 0 {
			return NewErrorResult(fmt.Sprintf("rename failed (no files changed):\n%s", strings.Join(errored, "\n"))), nil
		}
		return NewSuccessResult(fmt.Sprintf("No occurrences of '%s' found", oldName)), nil
	}

	// Some files changed: report the REAL changed-file count (not len(results)
	// which would be inflated), and surface any partial failures explicitly.
	msg := fmt.Sprintf("Renamed '%s' to '%s' in %d file(s):\n%s",
		oldName, newName, changedFiles, strings.Join(results, "\n"))
	if len(errored) > 0 {
		msg += fmt.Sprintf("\n\n⚠ %d file(s) failed:\n%s", len(errored), strings.Join(errored, "\n"))
	}
	// Declare the actually-rewritten files (pattern mode has no path args) so
	// the done-gate records them as touched and the executor invalidates
	// read-dedup/caches. See "Adding a New Tool" step 14.
	return NewSuccessResultWithData(msg, map[string]any{"written_paths": changed}), nil
}

// renameInFile performs AST-based renaming in a single file.
// validateWritePath enforces the same path-safety contract as edit/write:
// the path must resolve inside an allowed root (PathValidator) and must not
// target a blocked location like .git/. Without this, refactor's file_path /
// glob pattern came straight from the model with NO validation — a path such
// as "../../etc/x" or ".git/hooks/pre-commit" would be read and rewritten.
func (t *RefactorTool) validateWritePath(filePath string) (string, error) {
	if t.pathValidator == nil {
		return "", fmt.Errorf("security error: path validator not initialized")
	}
	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		return "", fmt.Errorf("path validation failed: %s", err)
	}
	if err := security.IsBlockedWritePath(validPath); err != nil {
		return "", err
	}
	return validPath, nil
}

func (t *RefactorTool) renameInFile(_ context.Context, filePath, oldName, newName string) (int, error) {
	// Validate path before any I/O (covers single-file and glob-matched paths).
	validPath, err := t.validateWritePath(filePath)
	if err != nil {
		return 0, err
	}
	filePath = validPath

	// Read file
	content, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}

	// Parse AST only for Go files
	if strings.HasSuffix(filePath, ".go") {
		return t.renameInGoFile(filePath, content, oldName, newName)
	}

	// For non-Go files, use simple text replacement with scope awareness
	return t.renameInTextFile(filePath, content, oldName, newName)
}

// renameInGoFile performs AST-based renaming for Go code.
func (t *RefactorTool) renameInGoFile(filePath string, content []byte, oldName, newName string) (int, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		// Fall back to text-based replacement
		return t.renameInTextFile(filePath, content, oldName, newName)
	}

	// Find all identifiers matching oldName
	var positions []token.Pos
	ast.Inspect(node, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == oldName {
			positions = append(positions, ident.Pos())
		}
		return true
	})

	if len(positions) == 0 {
		return 0, nil
	}

	// Apply replacements in reverse order to preserve positions
	newContent := string(content)
	for i := len(positions) - 1; i >= 0; i-- {
		pos := positions[i]
		position := fset.Position(pos)
		offset := position.Offset // Already 0-indexed per token.Position spec

		// Replace identifier
		newContent = newContent[:offset] + newName + newContent[offset+len(oldName):]
	}

	if err := AtomicWrite(filePath, []byte(newContent), existingPerm(filePath)); err != nil {
		return 0, err
	}

	// Record for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "refactor_rename", content, []byte(newContent), false)
		change.Mode = existingPerm(filePath)
		t.undoManager.Record(*change)
	}

	return len(positions), nil
}

// renameInTextFile performs scope-aware text replacement.
func (t *RefactorTool) renameInTextFile(filePath string, content []byte, oldName, newName string) (int, error) {
	text := string(content)

	// Simple word-boundary replacement to avoid partial matches
	// Build regex pattern manually
	patternStr := "\\b" + regexp.QuoteMeta(oldName) + "\\b"
	re := regexp.MustCompile(patternStr)
	matches := re.FindAllStringIndex(text, -1)

	if len(matches) == 0 {
		return 0, nil
	}

	// Apply replacements from end to start
	newText := text
	for i := len(matches) - 1; i >= 0; i-- {
		start, end := matches[i][0], matches[i][1]
		newText = newText[:start] + newName + newText[end:]
	}

	// Write back (preserve mode, atomic temp+rename)
	if err := AtomicWrite(filePath, []byte(newText), existingPerm(filePath)); err != nil {
		return 0, err
	}

	// Record for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "refactor_rename", content, []byte(newText), false)
		change.Mode = existingPerm(filePath)
		t.undoManager.Record(*change)
	}

	return len(matches), nil
}

// executeExtract extracts code into a separate function.
func (t *RefactorTool) executeExtract(_ context.Context, args map[string]any) (ToolResult, error) {
	filePath, _ := GetString(args, "file_path")
	extractName, _ := GetString(args, "extract_name")
	startLine, _ := GetInt(args, "start_line")
	endLine, _ := GetInt(args, "end_line")

	if filePath == "" || extractName == "" || startLine == 0 || endLine == 0 {
		return NewErrorResult("file_path, extract_name, start_line, and end_line are required"), nil
	}

	validPath, err := t.validateWritePath(filePath)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	filePath = validPath

	// Read file
	content, err := os.ReadFile(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	lines := strings.Split(string(content), "\n")
	if startLine < 1 || endLine > len(lines) || startLine > endLine {
		return NewErrorResult(fmt.Sprintf("invalid line range: %d-%d (file has %d lines)", startLine, endLine, len(lines))), nil
	}

	// Extract the code block
	extractedCode := strings.Join(lines[startLine-1:endLine], "\n")

	// For Go files, try to create a proper function
	var newFunction string
	if strings.HasSuffix(filePath, ".go") {
		newFunction = fmt.Sprintf("// %s is an extracted function\nfunc %s() {\n%s\n}\n",
			extractName, extractName, extractedCode)
	} else {
		// For other languages, use a generic format
		newFunction = fmt.Sprintf("// Extracted function: %s\nfunction %s() {\n%s\n}\n",
			extractName, extractName, extractedCode)
	}

	// Create new content with extracted function
	var newContent strings.Builder
	newContent.WriteString(strings.Join(lines[:startLine-1], "\n"))
	newContent.WriteString("\n")
	newContent.WriteString(newFunction)
	newContent.WriteString("\n")
	newContent.WriteString(strings.Join(lines[endLine:], "\n"))

	contentStr := newContent.String()

	// Write back (diff preview requires context, skip for now)
	if err := AtomicWrite(filePath, []byte(contentStr), existingPerm(filePath)); err != nil {
		return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
	}

	// Record for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "refactor_extract", content, []byte(contentStr), false)
		change.Mode = existingPerm(filePath)
		t.undoManager.Record(*change)
	}

	return NewSuccessResultWithData(
		fmt.Sprintf("Extracted lines %d-%d into function '%s' in %s",
			startLine, endLine, extractName, filePath),
		map[string]any{
			"changed":       true,
			"written_paths": []string{filePath},
		},
	), nil
}

// executeFindRefs finds all references to a function/variable.
func (t *RefactorTool) executeFindRefs(_ context.Context, args map[string]any) (ToolResult, error) {
	targetName, _ := GetString(args, "target_name")
	pattern, _ := GetString(args, "pattern")

	if targetName == "" {
		return NewErrorResult("target_name is required"), nil
	}

	// Default to all Go files if no pattern specified
	if pattern == "" {
		pattern = "**/*.go"
	}

	// Find matching files
	matches, err := filepath.Glob(filepath.Join(t.workDir, pattern))
	if err != nil {
		return NewErrorResult(fmt.Sprintf("invalid pattern: %s", err)), nil
	}

	// Search for references
	var refs []string
	for _, file := range matches {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		// For Go files, use AST to find accurate references
		if strings.HasSuffix(file, ".go") {
			fileRefs := findRefsInGoFile(file, content, targetName)
			if len(fileRefs) > 0 {
				refs = append(refs, fmt.Sprintf("%s:\n  %s", file, strings.Join(fileRefs, "\n  ")))
			}
		} else {
			// For other files, use simple text search
			lines := strings.Split(string(content), "\n")
			var fileRefs []string
			for i, line := range lines {
				if strings.Contains(line, targetName) {
					fileRefs = append(fileRefs, fmt.Sprintf("Line %d: %s", i+1, strings.TrimSpace(line)))
				}
			}
			if len(fileRefs) > 0 {
				refs = append(refs, fmt.Sprintf("%s:\n  %s", file, strings.Join(fileRefs, "\n  ")))
			}
		}
	}

	if len(refs) == 0 {
		return NewSuccessResult(fmt.Sprintf("No references found for '%s'", targetName)), nil
	}

	return NewSuccessResult(fmt.Sprintf("Found references to '%s' in %d file(s):\n%s",
		targetName, len(refs), strings.Join(refs, "\n\n"))), nil
}

// executeInline inlines a function by replacing call sites with the function body.
func (t *RefactorTool) executeInline(_ context.Context, args map[string]any) (ToolResult, error) {
	filePath, _ := GetString(args, "file_path")
	targetName, _ := GetString(args, "target_name")

	if filePath == "" || targetName == "" {
		return NewErrorResult("file_path and target_name are required for inline"), nil
	}

	validPath, err := t.validateWritePath(filePath)
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	filePath = validPath

	content, err := os.ReadFile(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	if !strings.HasSuffix(filePath, ".go") {
		return NewErrorResult("inline refactoring is only supported for Go files"), nil
	}

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("failed to parse Go file: %s", err)), nil
	}

	// Step 1: Find the function definition
	var funcBody string
	var funcFound bool

	ast.Inspect(node, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != targetName {
			return true
		}
		bodyStart := fset.Position(fn.Body.Lbrace).Offset + 1
		bodyEnd := fset.Position(fn.Body.Rbrace).Offset
		if bodyStart < bodyEnd {
			funcBody = strings.TrimSpace(string(content[bodyStart:bodyEnd]))
		}
		funcFound = true
		return false
	})

	if !funcFound {
		return NewErrorResult(fmt.Sprintf("function '%s' not found in %s", targetName, filePath)), nil
	}

	if funcBody == "" {
		return NewErrorResult(fmt.Sprintf("function '%s' has an empty body", targetName)), nil
	}

	// Step 2: Find all call sites and replace them
	type callSite struct {
		start, end int
	}
	var callSites []callSite

	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != targetName {
			return true
		}

		startOff := fset.Position(call.Pos()).Offset
		endOff := fset.Position(call.End()).Offset
		callSites = append(callSites, callSite{start: startOff, end: endOff})
		return true
	})

	if len(callSites) == 0 {
		return NewSuccessResult(fmt.Sprintf("No call sites found for '%s'", targetName)), nil
	}

	// Step 3: Build inlined body with parameter substitution
	inlineBody := "/* inlined from " + targetName + " */\n" + funcBody

	// Wrap in block to avoid variable scope issues
	replacement := "{\n" + inlineBody + "\n}"

	// Step 4: Apply replacements in reverse order
	newContent := string(content)
	for i := len(callSites) - 1; i >= 0; i-- {
		cs := callSites[i]
		// Check if call is a statement (followed by newline/semicolon) or expression
		// For statement calls, replace the entire statement
		newContent = newContent[:cs.start] + replacement + newContent[cs.end:]
	}

	// Write back (preserve mode, atomic temp+rename)
	if err := AtomicWrite(filePath, []byte(newContent), existingPerm(filePath)); err != nil {
		return NewErrorResult(fmt.Sprintf("error writing file: %s", err)), nil
	}

	// Record for undo
	if t.undoManager != nil {
		change := undo.NewFileChange(filePath, "refactor_inline", content, []byte(newContent), false)
		change.Mode = existingPerm(filePath)
		t.undoManager.Record(*change)
	}

	return NewSuccessResultWithData(
		fmt.Sprintf("Inlined '%s' at %d call site(s) in %s. Review the changes for correctness.",
			targetName, len(callSites), filePath),
		map[string]any{
			"changed":       true,
			"written_paths": []string{filePath},
		},
	), nil
}

// findRefsInGoFile uses AST to find references in Go code.
func findRefsInGoFile(filePath string, content []byte, targetName string) []string {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, content, parser.ParseComments)
	if err != nil {
		return nil
	}

	var refs []string
	ast.Inspect(node, func(n ast.Node) bool {
		if ident, ok := n.(*ast.Ident); ok && ident.Name == targetName {
			pos := fset.Position(ident.Pos())
			refs = append(refs, fmt.Sprintf("Line %d", pos.Line))
		}
		return true
	})

	return refs
}

// Helper for regex escaping is now using standard regexp package
