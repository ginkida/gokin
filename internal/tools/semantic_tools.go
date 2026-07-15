package tools

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/genai"

	"gokin/internal/security"
)

// GoToDefinitionTool finds a definition through managed Go intelligence and
// falls back to a bounded, package-local AST search when it is unavailable.
type GoToDefinitionTool struct {
	workDir       string
	pathValidator *security.PathValidator
	providerMu    sync.RWMutex
	provider      SemanticProvider
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

// SetSemanticProvider wires a managed, workspace-bound semantic provider.
func (t *GoToDefinitionTool) SetSemanticProvider(provider SemanticProvider) {
	t.providerMu.Lock()
	t.provider = provider
	t.providerMu.Unlock()
}

func (t *GoToDefinitionTool) semanticProvider() SemanticProvider {
	t.providerMu.RLock()
	defer t.providerMu.RUnlock()
	return t.provider
}

func (t *GoToDefinitionTool) Name() string {
	return "go_to_definition"
}

func (t *GoToDefinitionTool) Description() string {
	return "Finds the definition of a Go symbol through managed semantic intelligence, " +
		"with an explicitly-labeled package-local AST fallback."
}

// Declaration delegates to the declarations.go copy so the eager and lazy
// registries can never serve divergent schemas (same pattern as mcp_admin).
func (t *GoToDefinitionTool) Declaration() *genai.FunctionDeclaration {
	return GoToDefinitionToolDeclaration()
}

func (t *GoToDefinitionTool) Validate(args map[string]any) error {
	return validateGoSemanticArgs(args, true)
}

func (t *GoToDefinitionTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	file := strings.TrimSpace(GetStringDefault(args, "file", ""))
	symbol := strings.TrimSpace(GetStringDefault(args, "symbol", ""))
	line := GetIntDefault(args, "line", 0)
	col := GetIntDefault(args, "column", 0)

	absFile, verr := validateSemanticPath(t.pathValidator, t.resolvePath(file))
	if verr != nil {
		return NewErrorResult(verr.Error()), nil
	}
	if err := validateSemanticSourceFile(absFile); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	if err := validateSemanticPositionInFile(absFile, line, col); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	provider := t.semanticProvider()
	if provider != nil {
		queryResult, err := provider.SearchSymbols(ctx, SemanticSearchRequest{
			Query: symbol,
			Limit: symbolMatchCap,
		})
		if err == nil {
			matches := t.definitionMatchesFromProvider(symbol, queryResult.Matches)
			truncated := queryResult.Truncated || len(queryResult.Matches) > symbolMatchCap
			return formatDefinitionMatches(symbol, matches, SemanticSourceProvider, "", truncated), nil
		}
		return t.definitionViaASTWithReason(absFile, symbol, semanticProviderFailure("symbol search", err))
	}

	return t.definitionViaASTWithReason(absFile, symbol, semanticProviderUnavailableReason)
}

// definitionViaAST locates DECLARATIONS of the symbol (func/type/var/const,
// including method declarations) in the symbol's package directory. Unlike a
// raw reference scan it does not emit every usage, so the output matches the
// tool's contract ("where is X defined").
func (t *GoToDefinitionTool) definitionViaAST(file, symbol string) (ToolResult, error) {
	return t.definitionViaASTWithReason(file, symbol, semanticProviderUnavailableReason)
}

func (t *GoToDefinitionTool) definitionViaASTWithReason(file, symbol, degradedReason string) (ToolResult, error) {
	dir := filepath.Dir(file)
	entries, err := readGoFilesInDir(dir)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("cannot list directory: %v", err)), nil
	}

	var matches []SemanticMatch
	for _, entry := range entries {
		filePath := filepath.Join(dir, entry)
		content, err := readFileContentBounded(filePath)
		if err != nil {
			continue
		}
		for _, ln := range findDeclsInGoFile(filePath, content, symbol) {
			matches = append(matches, SemanticMatch{
				File: filepath.Base(filePath),
				Line: ln,
				Name: symbol,
			})
			if len(matches) >= symbolMatchCap {
				break
			}
		}
		if len(matches) >= symbolMatchCap {
			break
		}
	}

	return formatDefinitionMatches(symbol, matches, SemanticSourceFallback, degradedReason, false), nil
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

const (
	semanticFallbackMaxFileBytes = 4 << 20
	semanticScannerMaxToken      = 1 << 20
	semanticSnippetMaxRunes      = 240
	semanticReasonMaxRunes       = 512
)

const semanticProviderUnavailableReason = "managed Go semantic provider is unavailable"

func validateGoSemanticArgs(args map[string]any, requireSymbol bool) error {
	file, err := requiredSemanticString(args, "file")
	if err != nil {
		return err
	}
	if strings.IndexByte(file, 0) >= 0 {
		return NewValidationError("file", "must not contain NUL bytes")
	}
	if !strings.HasSuffix(file, ".go") {
		return NewValidationError("file", "must name a .go source file; non-Go files are unsupported")
	}
	if requireSymbol {
		symbol, err := requiredSemanticString(args, "symbol")
		if err != nil {
			return err
		}
		if strings.IndexByte(symbol, 0) >= 0 || strings.ContainsAny(symbol, "\r\n") {
			return NewValidationError("symbol", "must be a single non-empty symbol name")
		}
	}

	line, hasLine, err := optionalPositiveSemanticInt(args, "line")
	if err != nil {
		return err
	}
	_, hasColumn, err := optionalPositiveSemanticInt(args, "column")
	if err != nil {
		return err
	}
	if hasColumn && !hasLine {
		return NewValidationError("column", "requires line")
	}
	if hasLine && line < 1 { // kept explicit as documentation of the contract
		return NewValidationError("line", "must be at least 1")
	}
	if raw, ok := args["include_definition"]; ok {
		if _, ok := raw.(bool); !ok {
			return NewValidationError("include_definition", "must be a boolean")
		}
	}
	return nil
}

func requiredSemanticString(args map[string]any, field string) (string, error) {
	raw, ok := args[field]
	if !ok {
		return "", NewValidationError(field, "is required")
	}
	value, ok := raw.(string)
	if !ok {
		return "", NewValidationError(field, "must be a string")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", NewValidationError(field, "must not be empty")
	}
	return value, nil
}

func optionalPositiveSemanticInt(args map[string]any, field string) (int, bool, error) {
	raw, ok := args[field]
	if !ok {
		return 0, false, nil
	}
	var value int64
	switch v := raw.(type) {
	case int:
		value = int64(v)
	case int64:
		value = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v > float64(math.MaxInt64) {
			return 0, true, NewValidationError(field, "must be an integer")
		}
		value = int64(v)
	default:
		return 0, true, NewValidationError(field, "must be an integer")
	}
	if value < 1 {
		return 0, true, NewValidationError(field, "must be at least 1")
	}
	if int64(int(value)) != value {
		return 0, true, NewValidationError(field, "is too large")
	}
	return int(value), true, nil
}

func validateSemanticSourceFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access Go source file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Go source path is not a regular file: %s", path)
	}
	return nil
}

func validateSemanticPositionInFile(path string, line, column int) error {
	if line == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot validate source position: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), semanticScannerMaxToken)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo != line {
			continue
		}
		if column > len(scanner.Bytes())+1 {
			return NewValidationError("column", fmt.Sprintf("%d exceeds line %d length", column, line))
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cannot validate source position: %w", err)
	}
	return NewValidationError("line", fmt.Sprintf("%d exceeds file length (%d lines)", line, lineNo))
}

func semanticProviderFailure(operation string, err error) string {
	reason := fmt.Sprintf("managed Go semantic provider %s failed: %v", operation, err)
	return truncateRunes(reason, semanticReasonMaxRunes)
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
		if len(lines) >= symbolMatchCap {
			break
		}
		switch decl := d.(type) {
		case *ast.FuncDecl:
			if decl.Name != nil && decl.Name.Name == name {
				add(decl.Name.Pos())
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				if len(lines) >= symbolMatchCap {
					break
				}
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name != nil && s.Name.Name == name {
						add(s.Name.Pos())
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.Name == name {
							add(id.Pos())
							if len(lines) >= symbolMatchCap {
								break
							}
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

// FindReferencesTool finds references through managed Go intelligence and
// falls back to a bounded whole-word scan when it is unavailable.
type FindReferencesTool struct {
	workDir       string
	pathValidator *security.PathValidator
	providerMu    sync.RWMutex
	provider      SemanticProvider
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

// SetSemanticProvider wires a managed, workspace-bound semantic provider.
func (t *FindReferencesTool) SetSemanticProvider(provider SemanticProvider) {
	t.providerMu.Lock()
	t.provider = provider
	t.providerMu.Unlock()
}

func (t *FindReferencesTool) semanticProvider() SemanticProvider {
	t.providerMu.RLock()
	defer t.providerMu.RUnlock()
	return t.provider
}

func (t *FindReferencesTool) Name() string {
	return "find_references"
}

func (t *FindReferencesTool) Description() string {
	return "Finds Go symbol references through managed semantic intelligence. " +
		"If unavailable, returns a bounded lexical fallback with degraded metadata."
}

// Declaration delegates to the declarations.go copy so the eager and lazy
// registries can never serve divergent schemas (same pattern as mcp_admin).
func (t *FindReferencesTool) Declaration() *genai.FunctionDeclaration {
	return FindReferencesToolDeclaration()
}

func (t *FindReferencesTool) Validate(args map[string]any) error {
	return validateGoSemanticArgs(args, true)
}

func (t *FindReferencesTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	file := strings.TrimSpace(GetStringDefault(args, "file", ""))
	symbol := strings.TrimSpace(GetStringDefault(args, "symbol", ""))
	line := GetIntDefault(args, "line", 0)
	col := GetIntDefault(args, "column", 0)
	includeDef := GetBoolDefault(args, "include_definition", true)

	absFile, verr := validateSemanticPath(t.pathValidator, t.resolvePath(file))
	if verr != nil {
		return NewErrorResult(verr.Error()), nil
	}
	if err := validateSemanticSourceFile(absFile); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	if err := validateSemanticPositionInFile(absFile, line, col); err != nil {
		return NewErrorResult(err.Error()), nil
	}

	degradedReason := semanticProviderUnavailableReason
	if provider := t.semanticProvider(); provider != nil {
		queryResult, err := provider.FindReferences(ctx, SemanticReferencesRequest{
			File:              absFile,
			Symbol:            symbol,
			Line:              line,
			Column:            col,
			IncludeDefinition: includeDef,
			Limit:             symbolMatchCap,
		})
		if err == nil {
			matches := normalizeProviderMatches(t.pathValidator, t.workDir, queryResult.Matches, symbolMatchCap)
			truncated := queryResult.Truncated || len(queryResult.Matches) > symbolMatchCap
			return formatReferenceMatches(symbol, matches, SemanticSourceProvider, "", truncated, "managed provider"), nil
		}
		degradedReason = semanticProviderFailure("find references", err)
	}

	if !includeDef {
		degradedReason += "; lexical fallback cannot reliably exclude the definition"
	}
	return t.referencesViaGrep(ctx, symbol, degradedReason)
}

// referencesViaGrep searches for whole-word occurrences of the symbol in .go
// files. It prefers `git grep` (fast, .gitignore-aware) inside a repo and falls
// back to a filesystem walk anywhere else, so it works regardless of whether the
// working directory is a git checkout.
func (t *FindReferencesTool) referencesViaGrep(ctx context.Context, symbol string, degradedReason ...string) (ToolResult, error) {
	reason := semanticProviderUnavailableReason
	if len(degradedReason) > 0 && strings.TrimSpace(degradedReason[0]) != "" {
		reason = degradedReason[0]
	}

	matches, truncated, handled, err := t.gitGrepMatches(ctx, symbol)
	if err != nil {
		return ToolResult{}, err
	}
	if handled {
		return formatReferenceMatches(symbol, matches, SemanticSourceFallback, reason, truncated, "git grep --untracked"), nil
	}
	return t.referencesViaFileScanWithReason(ctx, symbol, reason)
}

// gitGrepMatches streams a bounded git-grep result set. --untracked is
// essential here: recently-created Go files are often the most relevant files
// after an edit and ordinary git grep silently omits them.
func (t *FindReferencesTool) gitGrepMatches(ctx context.Context, symbol string) ([]SemanticMatch, bool, bool, error) {
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "git", "grep", "--untracked", "--exclude-standard", "-n", "-F", "-w", "-e", symbol, "--", "*.go")
	cmd.Dir = t.workDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false, nil
	}
	stderr := &semanticCappedWriter{limit: 8 * 1024}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, false, false, nil
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), semanticScannerMaxToken)
	matches := make([]SemanticMatch, 0, symbolMatchCap)
	truncated := false
	for scanner.Scan() {
		if len(matches) >= symbolMatchCap {
			truncated = true
			cancel()
			break
		}
		if match, ok := parseGitGrepMatch(scanner.Text(), symbol); ok {
			matches = append(matches, match)
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return nil, false, false, ctx.Err()
	}
	if truncated {
		return matches, true, true, nil
	}
	if scanErr != nil {
		// A pathological source line exceeded the scanner cap. Use the
		// filesystem scanner instead of growing memory without a bound.
		return nil, false, false, nil
	}
	if waitErr == nil {
		return matches, false, true, nil
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && strings.TrimSpace(stderr.String()) == "" {
		return nil, false, true, nil
	}
	// Not a repository, unsupported git flags, or git unavailable: the bounded
	// filesystem walk below remains authoritative for the lexical fallback.
	return nil, false, false, nil
}

func parseGitGrepMatch(line, symbol string) (SemanticMatch, bool) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) != 3 {
		return SemanticMatch{}, false
	}
	lineNo, ok := parsePositiveDecimal(parts[1])
	if !ok {
		return SemanticMatch{}, false
	}
	return SemanticMatch{
		File:    parts[0],
		Line:    lineNo,
		Column:  strings.Index(parts[2], symbol) + 1,
		Name:    symbol,
		Snippet: truncateRunes(strings.TrimSpace(parts[2]), semanticSnippetMaxRunes),
	}, true
}

func parsePositiveDecimal(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
		digit := int(value[i] - '0')
		if n > (int(^uint(0)>>1)-digit)/10 {
			return 0, false
		}
		n = n*10 + digit
	}
	return n, n > 0
}

type semanticCappedWriter struct {
	buf   []byte
	limit int
}

func (w *semanticCappedWriter) Write(p []byte) (int, error) {
	written := len(p)
	remaining := w.limit - len(w.buf)
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		w.buf = append(w.buf, p[:remaining]...)
	}
	return written, nil
}

func (w *semanticCappedWriter) String() string { return string(w.buf) }

// referencesViaFileScan walks the working directory for .go files and reports
// whole-word matches of the symbol. Used when git grep is unavailable.
func (t *FindReferencesTool) referencesViaFileScan(ctx context.Context, symbol string) (ToolResult, error) {
	return t.referencesViaFileScanWithReason(ctx, symbol, semanticProviderUnavailableReason)
}

func (t *FindReferencesTool) referencesViaFileScanWithReason(ctx context.Context, symbol, degradedReason string) (ToolResult, error) {
	var matches []SemanticMatch
	truncated := false
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
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, rerr := os.Open(path)
		if rerr != nil {
			return nil
		}
		defer f.Close()
		rel, relErr := filepath.Rel(t.workDir, path)
		if relErr != nil {
			rel = path
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), semanticScannerMaxToken)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			lineText := scanner.Text()
			if containsWholeWord(lineText, symbol) {
				if len(matches) >= symbolMatchCap {
					truncated = true
					return filepath.SkipAll
				}
				matches = append(matches, SemanticMatch{
					File:    rel,
					Line:    lineNo,
					Column:  strings.Index(lineText, symbol) + 1,
					Name:    symbol,
					Snippet: truncateRunes(strings.TrimSpace(lineText), semanticSnippetMaxRunes),
				})
			}
		}
		return nil
	})
	if walkErr != nil {
		return ToolResult{}, walkErr
	}
	return formatReferenceMatches(symbol, matches, SemanticSourceFallback, degradedReason, truncated, "file scan"), nil
}

func (t *FindReferencesTool) resolvePath(path string) string {
	return resolveSemanticPath(t.workDir, path)
}

// symbolMatchCap bounds how many reference matches are rendered (and, for the
// file-scan fallback, collected at all).
const symbolMatchCap = 50

func (t *GoToDefinitionTool) definitionMatchesFromProvider(symbol string, providerMatches []SemanticMatch) []SemanticMatch {
	candidates := normalizeProviderMatches(t.pathValidator, t.workDir, providerMatches, symbolMatchCap)
	matches := make([]SemanticMatch, 0, min(len(candidates), symbolMatchCap))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		if len(matches) >= symbolMatchCap {
			break
		}
		exactName := semanticNameMatches(candidate.Name, symbol)
		abs := resolveSemanticPath(t.workDir, candidate.File)
		content, err := readFileContentBounded(abs)
		if err == nil {
			declLines := findDeclsInGoFile(abs, content, symbol)
			for _, line := range declLines {
				refined := candidate
				refined.Line = line
				refined.Column = 0
				refined.Name = symbol
				key := semanticMatchKey(refined)
				if _, duplicate := seen[key]; duplicate {
					continue
				}
				seen[key] = struct{}{}
				matches = append(matches, refined)
				if len(matches) >= symbolMatchCap {
					break
				}
			}
			if len(declLines) > 0 {
				continue
			}
		}
		// If the provider supplied an exact, positioned symbol, retain it even
		// when AST refinement was impossible (build tags or temporarily-invalid
		// source can make a correct semantic answer unparsable locally).
		if exactName && candidate.Line > 0 {
			key := semanticMatchKey(candidate)
			if _, duplicate := seen[key]; !duplicate {
				seen[key] = struct{}{}
				matches = append(matches, candidate)
			}
		}
	}
	return matches
}

func semanticNameMatches(name, query string) bool {
	name = strings.TrimSpace(name)
	query = strings.TrimSpace(query)
	if name == "" || query == "" {
		return false
	}
	return name == query || strings.HasSuffix(name, "."+query)
}

func normalizeProviderMatches(v *security.PathValidator, workDir string, matches []SemanticMatch, limit int) []SemanticMatch {
	if limit <= 0 || limit > symbolMatchCap*4 {
		limit = symbolMatchCap
	}
	normalized := make([]SemanticMatch, 0, min(len(matches), limit))
	seen := make(map[string]struct{})
	for _, match := range matches {
		if len(normalized) >= limit {
			break
		}
		if strings.TrimSpace(match.File) == "" || !strings.HasSuffix(match.File, ".go") {
			continue
		}
		valid, err := validateSemanticPath(v, resolveSemanticPath(workDir, match.File))
		if err != nil || validateSemanticSourceFile(valid) != nil {
			continue
		}
		match.File = semanticDisplayPath(workDir, valid)
		if match.Line < 0 {
			match.Line = 0
		}
		if match.Column < 0 {
			match.Column = 0
		}
		match.Name = truncateRunes(strings.TrimSpace(match.Name), semanticSnippetMaxRunes)
		match.Kind = truncateRunes(strings.TrimSpace(match.Kind), 64)
		match.Snippet = truncateRunes(strings.TrimSpace(match.Snippet), semanticSnippetMaxRunes)
		key := semanticMatchKey(match)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, match)
	}
	return normalized
}

func semanticDisplayPath(workDir, path string) string {
	rel, err := filepath.Rel(workDir, path)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return path
}

func semanticMatchKey(match SemanticMatch) string {
	return fmt.Sprintf("%s\x00%d\x00%d\x00%s", match.File, match.Line, match.Column, match.Name)
}

func formatDefinitionMatches(symbol string, matches []SemanticMatch, source SemanticResultSource, degradedReason string, truncated bool) ToolResult {
	if len(matches) > symbolMatchCap {
		matches = matches[:symbolMatchCap]
		truncated = true
	}
	data := SemanticResultData{
		Source:         source,
		DegradedReason: truncateRunes(degradedReason, semanticReasonMaxRunes),
		Matches:        matches,
		MatchCount:     len(matches),
		Truncated:      truncated,
	}
	var out strings.Builder
	if source == SemanticSourceFallback {
		if len(matches) == 0 {
			fmt.Fprintf(&out, "AST fallback could not confirm a local definition of %q. Semantic result is unknown because %s.", symbol, data.DegradedReason)
		} else {
			fmt.Fprintf(&out, "Definition candidate(s) of %q via AST fallback (%d):\n", symbol, len(matches))
			writeSemanticMatches(&out, matches)
			fmt.Fprintf(&out, "Degraded: %s.", data.DegradedReason)
		}
	} else if len(matches) == 0 {
		fmt.Fprintf(&out, "Managed semantic provider found no exact definition of %q in the workspace.", symbol)
	} else {
		fmt.Fprintf(&out, "Definition(s) of %q via managed semantic provider (%d):\n", symbol, len(matches))
		writeSemanticMatches(&out, matches)
		if truncated {
			out.WriteString("  ... more provider matches were omitted\n")
		}
	}
	return NewSuccessResultWithData(strings.TrimSpace(out.String()), data)
}

func formatReferenceMatches(symbol string, matches []SemanticMatch, source SemanticResultSource, degradedReason string, truncated bool, method string) ToolResult {
	if len(matches) > symbolMatchCap {
		matches = matches[:symbolMatchCap]
		truncated = true
	}
	data := SemanticResultData{
		Source:         source,
		DegradedReason: truncateRunes(degradedReason, semanticReasonMaxRunes),
		Matches:        matches,
		MatchCount:     len(matches),
		Truncated:      truncated,
	}
	var out strings.Builder
	if source == SemanticSourceFallback {
		if len(matches) == 0 {
			fmt.Fprintf(&out, "Fallback %s found no whole-word lexical matches for %q. Semantic references are unknown because %s.", method, symbol, data.DegradedReason)
		} else {
			fmt.Fprintf(&out, "Reference candidate(s) to %q via fallback %s (%d):\n", symbol, method, len(matches))
			writeSemanticMatches(&out, matches)
			if truncated {
				out.WriteString("  ... more lexical matches were omitted\n")
			}
			fmt.Fprintf(&out, "Degraded: %s.", data.DegradedReason)
		}
	} else if len(matches) == 0 {
		fmt.Fprintf(&out, "Managed semantic provider found no references to %q in the workspace.", symbol)
	} else {
		fmt.Fprintf(&out, "References to %q via managed semantic provider (%d):\n", symbol, len(matches))
		writeSemanticMatches(&out, matches)
		if truncated {
			out.WriteString("  ... more provider matches were omitted\n")
		}
	}
	return NewSuccessResultWithData(strings.TrimSpace(out.String()), data)
}

func writeSemanticMatches(out *strings.Builder, matches []SemanticMatch) {
	for _, match := range matches {
		fmt.Fprintf(out, "  %s", match.File)
		if match.Line > 0 {
			fmt.Fprintf(out, ":%d", match.Line)
			if match.Column > 0 {
				fmt.Fprintf(out, ":%d", match.Column)
			}
		}
		if match.Name != "" && match.Snippet == "" {
			fmt.Fprintf(out, " — %s", match.Name)
		}
		if match.Snippet != "" {
			fmt.Fprintf(out, " — %s", match.Snippet)
		}
		out.WriteByte('\n')
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
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

// readFileContent reads a file through the same hard cap used by semantic AST
// fallbacks. Kept as a small compatibility wrapper for package-local callers.
func readFileContent(path string) ([]byte, error) {
	return readFileContentBounded(path)
}

func readFileContentBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > semanticFallbackMaxFileBytes {
		return nil, fmt.Errorf("Go source file exceeds semantic fallback cap (%d bytes)", semanticFallbackMaxFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, semanticFallbackMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > semanticFallbackMaxFileBytes {
		return nil, fmt.Errorf("Go source file exceeds semantic fallback cap (%d bytes)", semanticFallbackMaxFileBytes)
	}
	return content, nil
}

// readGoFilesInDir returns regular Go source files in a directory. Test and
// untracked files are intentionally included: either may contain the definition
// relevant to the current edit.
func readGoFilesInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 && strings.HasSuffix(e.Name(), ".go") {
			files = append(files, e.Name())
		}
	}
	return files, nil
}
