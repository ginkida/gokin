package tools

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/genai"

	"gokin/internal/security"
)

// GoSearchTool performs fuzzy workspace-symbol discovery through the managed
// semantic provider and falls back to a bounded declaration-only AST walk.
type GoSearchTool struct {
	workDir       string
	pathValidator *security.PathValidator
	providerMu    sync.RWMutex
	provider      SemanticProvider
}

func NewGoSearchTool(workDir string) *GoSearchTool {
	t := &GoSearchTool{workDir: workDir}
	if workDir != "" {
		t.pathValidator = security.NewPathValidator([]string{workDir}, false)
	}
	return t
}

func (t *GoSearchTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

func (t *GoSearchTool) SetSemanticProvider(provider SemanticProvider) {
	t.providerMu.Lock()
	t.provider = provider
	t.providerMu.Unlock()
}

func (t *GoSearchTool) semanticProvider() SemanticProvider {
	t.providerMu.RLock()
	defer t.providerMu.RUnlock()
	return t.provider
}

func (t *GoSearchTool) Name() string { return "go_search" }

func (t *GoSearchTool) Description() string {
	return "Fuzzy-searches Go declarations across the workspace using managed semantic intelligence."
}

func (t *GoSearchTool) Declaration() *genai.FunctionDeclaration {
	return GoSearchToolDeclaration()
}

func (t *GoSearchTool) Validate(args map[string]any) error {
	query, err := requiredSemanticString(args, "query")
	if err != nil {
		return err
	}
	if strings.IndexByte(query, 0) >= 0 || strings.ContainsAny(query, "\r\n") {
		return NewValidationError("query", "must be a single non-empty search expression")
	}
	limit, present, err := optionalPositiveSemanticInt(args, "limit")
	if err != nil {
		return err
	}
	if present && limit > symbolMatchCap {
		return NewValidationError("limit", fmt.Sprintf("must not exceed %d", symbolMatchCap))
	}
	return nil
}

func (t *GoSearchTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	query := strings.TrimSpace(GetStringDefault(args, "query", ""))
	limit := GetIntDefault(args, "limit", symbolMatchCap)
	if limit <= 0 || limit > symbolMatchCap {
		limit = symbolMatchCap
	}

	degradedReason := semanticProviderUnavailableReason
	if provider := t.semanticProvider(); provider != nil {
		queryResult, err := provider.SearchSymbols(ctx, SemanticSearchRequest{Query: query, Limit: limit})
		if err == nil {
			matches := normalizeProviderMatches(t.pathValidator, t.workDir, queryResult.Matches, limit)
			truncated := queryResult.Truncated || len(queryResult.Matches) > limit
			return formatSearchMatches(query, matches, SemanticSourceProvider, "", truncated), nil
		}
		degradedReason = semanticProviderFailure("symbol search", err)
	}

	return t.searchViaAST(ctx, query, limit, degradedReason)
}

func (t *GoSearchTool) searchViaAST(ctx context.Context, query string, limit int, degradedReason string) (ToolResult, error) {
	matches := make([]SemanticMatch, 0, limit)
	truncated := false
	err := filepath.WalkDir(t.workDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != t.workDir && shouldSkipSemanticDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		content, readErr := readFileContentBounded(path)
		if readErr != nil {
			return nil
		}
		fset := token.NewFileSet()
		file, _ := parser.ParseFile(fset, path, content, parser.SkipObjectResolution|parser.AllErrors)
		if file == nil {
			return nil
		}
		rel := semanticDisplayPath(t.workDir, path)
		remaining := limit - len(matches)
		fileMatches, more := declarationMatches(fset, file, rel, query, remaining)
		matches = append(matches, fileMatches...)
		if more {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return ToolResult{}, err
	}
	return formatSearchMatches(query, matches, SemanticSourceFallback, degradedReason, truncated), nil
}

func declarationMatches(fset *token.FileSet, file *ast.File, path, query string, limit int) ([]SemanticMatch, bool) {
	var matches []SemanticMatch
	truncated := false
	appendMatch := func(name, kind string, pos token.Pos) {
		if !fuzzySemanticNameMatch(name, query) {
			return
		}
		if len(matches) >= limit {
			truncated = true
			return
		}
		matches = append(matches, SemanticMatch{
			File: path,
			Line: fset.Position(pos).Line,
			Name: name,
			Kind: kind,
		})
	}
	for _, declaration := range file.Decls {
		if truncated {
			break
		}
		switch decl := declaration.(type) {
		case *ast.FuncDecl:
			name := decl.Name.Name
			kind := "function"
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				kind = "method"
				if receiver := semanticReceiverName(decl.Recv.List[0].Type); receiver != "" {
					name = receiver + "." + name
				}
			}
			appendMatch(name, kind, decl.Name.Pos())
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				if truncated {
					break
				}
				switch value := spec.(type) {
				case *ast.TypeSpec:
					appendMatch(value.Name.Name, "type", value.Name.Pos())
				case *ast.ValueSpec:
					kind := strings.ToLower(decl.Tok.String())
					for _, name := range value.Names {
						appendMatch(name.Name, kind, name.Pos())
						if truncated {
							break
						}
					}
				}
			}
		}
	}
	return matches, truncated
}

func semanticReceiverName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return semanticReceiverName(value.X)
	case *ast.IndexExpr:
		return semanticReceiverName(value.X)
	case *ast.IndexListExpr:
		return semanticReceiverName(value.X)
	case *ast.SelectorExpr:
		prefix := semanticReceiverName(value.X)
		if prefix == "" {
			return value.Sel.Name
		}
		return prefix + "." + value.Sel.Name
	default:
		return ""
	}
}

func fuzzySemanticNameMatch(name, query string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	query = strings.ToLower(strings.TrimSpace(query))
	if name == "" || query == "" {
		return false
	}
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return r == ' ' || r == '.' || r == '/' || r == '_' || r == '-'
	})
	for _, term := range terms {
		if !strings.Contains(name, term) && !isSemanticSubsequence(name, term) {
			return false
		}
	}
	return true
}

func isSemanticSubsequence(value, query string) bool {
	queryRunes := []rune(query)
	if len(queryRunes) == 0 {
		return true
	}
	index := 0
	for _, r := range value {
		if r == queryRunes[index] {
			index++
			if index == len(queryRunes) {
				return true
			}
		}
	}
	return false
}

func shouldSkipSemanticDir(name string) bool {
	return name == ".git" || name == "vendor" || name == "node_modules" || name == "testdata"
}

func formatSearchMatches(query string, matches []SemanticMatch, source SemanticResultSource, degradedReason string, truncated bool) ToolResult {
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
			fmt.Fprintf(&out, "AST fallback found no declaration names matching %q. Semantic search is unknown because %s.", query, data.DegradedReason)
		} else {
			fmt.Fprintf(&out, "Go symbol candidate(s) for %q via AST fallback (%d):\n", query, len(matches))
			writeSemanticMatches(&out, matches)
			if truncated {
				out.WriteString("  ... more declaration matches were omitted\n")
			}
			fmt.Fprintf(&out, "Degraded: %s.", data.DegradedReason)
		}
	} else if len(matches) == 0 {
		fmt.Fprintf(&out, "Managed semantic provider found no Go symbols matching %q.", query)
	} else {
		fmt.Fprintf(&out, "Go symbols matching %q via managed semantic provider (%d):\n", query, len(matches))
		writeSemanticMatches(&out, matches)
		if truncated {
			out.WriteString("  ... more provider matches were omitted\n")
		}
	}
	return NewSuccessResultWithData(strings.TrimSpace(out.String()), data)
}
