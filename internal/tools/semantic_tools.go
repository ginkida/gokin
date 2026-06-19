package tools

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/genai"

	"gokin/internal/security"
)

// GoToDefinitionTool finds the definition of a symbol using gopls (LSP).
// Falls back to AST-based search for Go files when gopls is unavailable.
type GoToDefinitionTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

// NewGoToDefinitionTool creates a new GoToDefinitionTool instance.
func NewGoToDefinitionTool(workDir string) *GoToDefinitionTool {
	t := &GoToDefinitionTool{workDir: workDir}
	if workDir != "" {
		t.pathValidator = security.NewPathValidator([]string{workDir}, false)
	}
	return t
}

// SetAllowedDirs expands the path validator to additional directories beyond
// workDir (mirrors read.go), so a granted directory is navigable by this tool.
func (t *GoToDefinitionTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

func (t *GoToDefinitionTool) Name() string {
	return "go_to_definition"
}

func (t *GoToDefinitionTool) Description() string {
	return "Finds the definition of a symbol (function, type, variable) in the codebase. " +
		"Returns file:line location. Use this instead of grep+read to quickly jump to where something is defined."
}

// Declaration delegates to the declarations.go copy so the eager and lazy
// registries can never serve divergent schemas (same pattern as mcp_admin).
func (t *GoToDefinitionTool) Declaration() *genai.FunctionDeclaration {
	return GoToDefinitionToolDeclaration()
}

func (t *GoToDefinitionTool) Validate(args map[string]any) error {
	file := GetStringDefault(args, "file", "")
	if file == "" {
		return fmt.Errorf("file is required")
	}
	return nil
}

func (t *GoToDefinitionTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	file := GetStringDefault(args, "file", "")
	symbol := GetStringDefault(args, "symbol", "")
	line := GetIntDefault(args, "line", 0)
	col := GetIntDefault(args, "column", 0)

	absFile, verr := validateSemanticPath(t.pathValidator, t.resolvePath(file))
	if verr != nil {
		return NewErrorResult(verr.Error()), nil
	}

	// gopls "definition" needs a precise position. Without a line we cannot use
	// it, so fall straight through to the AST search (which resolves by name and
	// needs no position) — otherwise a machine WITH gopls installed would be
	// strictly worse than one without it for the common "where is X defined?"
	// call that supplies only {file, symbol}.
	if line > 0 && goplsAvailable() {
		result, err := t.definitionViaGopls(ctx, absFile, symbol, line, col)
		if err == nil && result.Success {
			return result, nil
		}
		// Fall through to the fallback on gopls failure.
	}

	// Fallback: AST-based search for Go files.
	if strings.HasSuffix(file, ".go") {
		return t.definitionViaAST(absFile, symbol)
	}

	return NewErrorResult("gopls not available and no fallback for non-Go files. Install gopls: go install golang.org/x/tools/gopls@latest"), nil
}

func (t *GoToDefinitionTool) definitionViaGopls(ctx context.Context, file, symbol string, line, col int) (ToolResult, error) {
	pos := fmt.Sprintf("%s:%d:%d", file, line, max(col, 1))
	// -remote=auto: reuse (or start) a shared gopls daemon — a one-shot gopls
	// re-type-checks the whole module from scratch on EVERY call (seconds on a
	// module this size); the daemon's warm cache makes repeat lookups ~instant.
	cmd := exec.CommandContext(ctx, "gopls", "-remote=auto", "definition", pos)
	cmd.Dir = t.workDir
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return ToolResult{}, fmt.Errorf("gopls definition: %s", stderr)
			}
		}
		return ToolResult{}, fmt.Errorf("gopls definition failed: %w", err)
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return NewSuccessResult(fmt.Sprintf("Definition of '%s' not found in the workspace.", symbol)), nil
	}

	// gopls returns file:line:col: description
	return NewSuccessResult(fmt.Sprintf("Definition of '%s':\n%s", symbol, result)), nil
}

// definitionViaAST locates DECLARATIONS of the symbol (func/type/var/const,
// including method declarations) in the symbol's package directory. Unlike a
// raw reference scan it does not emit every usage, so the output matches the
// tool's contract ("where is X defined").
func (t *GoToDefinitionTool) definitionViaAST(file, symbol string) (ToolResult, error) {
	dir := filepath.Dir(file)
	entries, err := readGoFilesInDir(dir)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("cannot list directory: %v", err)), nil
	}

	type decl struct {
		file string
		line int
	}
	var decls []decl
	for _, entry := range entries {
		filePath := filepath.Join(dir, entry)
		content, err := readFileContent(filePath)
		if err != nil {
			continue
		}
		for _, ln := range findDeclsInGoFile(filePath, content, symbol) {
			decls = append(decls, decl{filepath.Base(filePath), ln})
		}
	}

	if len(decls) == 0 {
		return NewSuccessResult(fmt.Sprintf(
			"Definition of '%s' not found in package %s (gopls unavailable; the definition may live in another package — pass line:col so gopls can resolve it).",
			symbol, filepath.Base(dir))), nil
	}

	var result strings.Builder
	fmt.Fprintf(&result, "Definition(s) of '%s' (gopls unavailable, AST fallback):\n", symbol)
	for _, d := range decls {
		fmt.Fprintf(&result, "  %s:%d\n", d.file, d.line)
	}
	return NewSuccessResult(result.String()), nil
}

func (t *GoToDefinitionTool) resolvePath(path string) string {
	return resolveSemanticPath(t.workDir, path)
}

// resolveSemanticPath is the single workDir-relative resolver shared by both
// semantic tools — one site for any future normalization fix.
func resolveSemanticPath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workDir, path)
}

// validateSemanticPath enforces the same workspace boundary read/grep enforce.
// The semantic tools are RiskLow + auto-allowed, so without this they would be
// a prompt-free read primitive over arbitrary paths — inconsistent with every
// other read-class tool (read.go errors on a nil validator for the same reason).
func validateSemanticPath(v *security.PathValidator, abs string) (string, error) {
	if v == nil {
		return "", fmt.Errorf("security error: path validator not initialized")
	}
	valid, err := v.ValidateFile(abs)
	if err != nil {
		return "", fmt.Errorf("security error: %v", err)
	}
	return valid, nil
}

// goplsAvailable checks if gopls is in PATH.
func goplsAvailable() bool {
	_, err := exec.LookPath("gopls")
	return err == nil
}

// findDeclsInGoFile returns the 1-indexed line numbers where the named symbol is
// DECLARED (top-level func/type/var/const or method) in a single Go file — as
// opposed to findRefsInGoFile (refactor.go) which returns every occurrence.
func findDeclsInGoFile(filePath string, content []byte, name string) []int {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, content, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var lines []int
	add := func(p token.Pos) { lines = append(lines, fset.Position(p).Line) }
	for _, d := range node.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Name != nil && decl.Name.Name == name {
				add(decl.Name.Pos())
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil && s.Name.Name == name {
						add(s.Name.Pos())
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.Name == name {
							add(id.Pos())
						}
					}
				}
			}
		}
	}
	return lines
}

// ---------------------------------------------------------------------------
// FindReferencesTool
// ---------------------------------------------------------------------------

// FindReferencesTool finds all references to a symbol using gopls (LSP).
type FindReferencesTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

// NewFindReferencesTool creates a new FindReferencesTool instance.
func NewFindReferencesTool(workDir string) *FindReferencesTool {
	t := &FindReferencesTool{workDir: workDir}
	if workDir != "" {
		t.pathValidator = security.NewPathValidator([]string{workDir}, false)
	}
	return t
}

// SetAllowedDirs expands the path validator to additional directories beyond
// workDir (mirrors read.go), so a granted directory is navigable by this tool.
func (t *FindReferencesTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

func (t *FindReferencesTool) Name() string {
	return "find_references"
}

func (t *FindReferencesTool) Description() string {
	return "Finds all references to a symbol (function, type, variable) across the codebase. " +
		"Returns file:line locations. Much faster than grep for codebase-wide symbol search."
}

// Declaration delegates to the declarations.go copy so the eager and lazy
// registries can never serve divergent schemas (same pattern as mcp_admin).
func (t *FindReferencesTool) Declaration() *genai.FunctionDeclaration {
	return FindReferencesToolDeclaration()
}

func (t *FindReferencesTool) Validate(args map[string]any) error {
	file := GetStringDefault(args, "file", "")
	if file == "" {
		return fmt.Errorf("file is required")
	}
	return nil
}

func (t *FindReferencesTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	file := GetStringDefault(args, "file", "")
	symbol := GetStringDefault(args, "symbol", "")
	line := GetIntDefault(args, "line", 0)
	col := GetIntDefault(args, "column", 0)
	includeDef := GetBoolDefault(args, "include_definition", true)

	absFile, verr := validateSemanticPath(t.pathValidator, t.resolvePath(file))
	if verr != nil {
		return NewErrorResult(verr.Error()), nil
	}

	// gopls "references" needs a precise position; without a line, skip it and
	// use the text-search fallback (which resolves by name) so behaviour does
	// not depend on whether gopls happens to be installed.
	if line > 0 && goplsAvailable() {
		result, err := t.referencesViaGopls(ctx, absFile, symbol, line, col, includeDef)
		if err == nil && result.Success {
			return result, nil
		}
	}

	// Fallback: whole-word text search for Go files.
	if strings.HasSuffix(file, ".go") {
		return t.referencesViaGrep(ctx, symbol)
	}

	return NewErrorResult("gopls not available (or no line:col given) and no fallback for non-Go files. Install gopls: go install golang.org/x/tools/gopls@latest, or pass a .go file with a line number."), nil
}

func (t *FindReferencesTool) referencesViaGopls(ctx context.Context, file, symbol string, line, col int, includeDef bool) (ToolResult, error) {
	// -remote=auto: shared warm daemon, see definitionViaGopls.
	args := []string{"-remote=auto", "references"}
	if includeDef {
		args = append(args, "-d")
	}
	pos := fmt.Sprintf("%s:%d:%d", file, line, max(col, 1))
	args = append(args, pos)

	cmd := exec.CommandContext(ctx, "gopls", args...)
	cmd.Dir = t.workDir
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
				return ToolResult{}, fmt.Errorf("gopls references: %s", stderr)
			}
		}
		return ToolResult{}, fmt.Errorf("gopls references failed: %w", err)
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return NewSuccessResult(fmt.Sprintf("No references to '%s' found in the workspace.", symbol)), nil
	}

	return formatSymbolMatches(symbol, strings.Split(result, "\n"), "gopls"), nil
}

// referencesViaGrep searches for whole-word occurrences of the symbol in .go
// files. It prefers `git grep` (fast, .gitignore-aware) inside a repo and falls
// back to a filesystem walk anywhere else, so it works regardless of whether the
// working directory is a git checkout.
func (t *FindReferencesTool) referencesViaGrep(ctx context.Context, symbol string) (ToolResult, error) {
	// -F: literal match (so a dotted/qualified name isn't treated as a regex);
	// -w: whole-word (so "Get" doesn't match "GetValue").
	cmd := exec.CommandContext(ctx, "git", "grep", "-n", "-F", "-w", "--", symbol, "--", "*.go")
	cmd.Dir = t.workDir
	output, err := cmd.Output()
	if err != nil {
		// exit 1 with EMPTY stderr inside a valid repo = no matches — same
		// stderr guard as bash.go's benignNonZeroExit: stderr present means a
		// real failure that must not be reported as "no references".
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 &&
			strings.TrimSpace(string(exitErr.Stderr)) == "" {
			return NewSuccessResult(fmt.Sprintf("No references to '%s' found.", symbol)), nil
		}
		// Not a git repo / git missing / other failure: fall back to a
		// filesystem scan so the tool still works outside a checkout.
		return t.referencesViaFileScan(ctx, symbol)
	}

	return formatSymbolMatches(symbol, strings.Split(strings.TrimSpace(string(output)), "\n"), "git grep"), nil
}

// referencesViaFileScan walks the working directory for .go files and reports
// whole-word matches of the symbol. Used when git grep is unavailable.
func (t *FindReferencesTool) referencesViaFileScan(ctx context.Context, symbol string) (ToolResult, error) {
	var matches []string
	walkErr := filepath.WalkDir(t.workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if path != t.workDir && (name == ".git" || name == "vendor" || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(t.workDir, path)
		if relErr != nil {
			rel = path
		}
		for i, lineText := range strings.Split(string(content), "\n") {
			if containsWholeWord(lineText, symbol) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, strings.TrimSpace(lineText)))
				if len(matches) > symbolMatchCap {
					// Output is capped anyway — stop walking the tree.
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		return ToolResult{}, walkErr
	}
	if len(matches) == 0 {
		return NewSuccessResult(fmt.Sprintf("No references to '%s' found.", symbol)), nil
	}
	return formatSymbolMatches(symbol, matches, "file scan"), nil
}

func (t *FindReferencesTool) resolvePath(path string) string {
	return resolveSemanticPath(t.workDir, path)
}

// symbolMatchCap bounds how many reference matches are rendered (and, for the
// file-scan fallback, collected at all).
const symbolMatchCap = 50

// formatSymbolMatches renders a capped list of file:line matches.
func formatSymbolMatches(symbol string, lines []string, source string) ToolResult {
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return NewSuccessResult(fmt.Sprintf("No references to '%s' found.", symbol))
	}
	var out strings.Builder
	if len(lines) > symbolMatchCap {
		fmt.Fprintf(&out, "References to '%s' via %s (%d+ matches):\n", symbol, source, symbolMatchCap)
	} else {
		fmt.Fprintf(&out, "References to '%s' via %s (%d match(es)):\n", symbol, source, len(lines))
	}
	for i, l := range lines {
		if i >= symbolMatchCap {
			fmt.Fprintf(&out, "  ... and more (pass file + line for a precise gopls lookup)\n")
			break
		}
		fmt.Fprintf(&out, "  %s\n", strings.TrimSpace(l))
	}
	return NewSuccessResult(out.String())
}

// containsWholeWord reports whether word appears in line bounded by non-identifier
// characters on both sides (ASCII identifier semantics — sufficient for the
// text-search fallback; the precise path is gopls).
func containsWholeWord(line, word string) bool {
	if word == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(line[from:], word)
		if i < 0 {
			return false
		}
		i += from
		beforeOK := i == 0 || !isIdentByte(line[i-1])
		end := i + len(word)
		afterOK := end >= len(line) || !isIdentByte(line[end])
		if beforeOK && afterOK {
			return true
		}
		from = i + 1
		if from >= len(line) {
			return false
		}
	}
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// readFileContent reads a file and returns its content as []byte for AST parsing.
func readFileContent(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// readGoFilesInDir returns Go source files (excluding test files) in a directory.
func readGoFilesInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			files = append(files, e.Name())
		}
	}
	return files, nil
}
