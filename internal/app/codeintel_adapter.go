package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gokin/internal/codeintel"
	"gokin/internal/mcp"
	"gokin/internal/tools"
)

// codeIntelligenceAdapter translates gopls MCP's bounded, human-readable
// results into the structured contracts consumed by built-in semantic tools.
// Keeping this at the app boundary avoids coupling either package to the other.
type codeIntelligenceAdapter struct {
	provider codeintel.ReadOnlyProvider
}

var (
	_ tools.SemanticProvider      = (*codeIntelligenceAdapter)(nil)
	_ tools.GoDiagnosticsProvider = (*codeIntelligenceAdapter)(nil)
)

func (a *codeIntelligenceAdapter) Diagnose(ctx context.Context, files []string) (tools.GoDiagnosticsReport, error) {
	if a == nil || a.provider == nil {
		return tools.GoDiagnosticsReport{}, fmt.Errorf("managed Go intelligence is unavailable")
	}
	report, err := a.provider.Diagnose(ctx, files)
	return tools.GoDiagnosticsReport{
		Content: report.Content,
		Clean:   report.Clean,
		Source:  report.Source,
	}, err
}

func (a *codeIntelligenceAdapter) SearchSymbols(ctx context.Context, request tools.SemanticSearchRequest) (tools.SemanticQueryResult, error) {
	if a == nil || a.provider == nil {
		return tools.SemanticQueryResult{}, fmt.Errorf("managed Go intelligence is unavailable")
	}
	result, err := a.provider.CallReadOnly(ctx, "go_search", map[string]any{
		"query": request.Query,
	})
	if err != nil {
		return tools.SemanticQueryResult{}, err
	}
	text, err := successfulMCPText("go_search", result)
	if err != nil {
		return tools.SemanticQueryResult{}, err
	}
	matches, err := parseGoplsSearch(text)
	if err != nil {
		return tools.SemanticQueryResult{}, err
	}
	return limitSemanticMatches(matches, request.Limit), nil
}

func (a *codeIntelligenceAdapter) FindReferences(ctx context.Context, request tools.SemanticReferencesRequest) (tools.SemanticQueryResult, error) {
	if a == nil || a.provider == nil {
		return tools.SemanticQueryResult{}, fmt.Errorf("managed Go intelligence is unavailable")
	}
	// gopls' symbolic reference tool always includes the declaration. Falling
	// back is more honest than returning a definition after the caller explicitly
	// asked to exclude it.
	if !request.IncludeDefinition {
		return tools.SemanticQueryResult{}, fmt.Errorf("gopls symbolic references cannot exclude the definition")
	}
	result, err := a.provider.CallReadOnly(ctx, "go_symbol_references", map[string]any{
		"file":   request.File,
		"symbol": request.Symbol,
	})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no references found") {
			return tools.SemanticQueryResult{}, nil
		}
		return tools.SemanticQueryResult{}, err
	}
	text, err := successfulMCPText("go_symbol_references", result)
	if err != nil {
		return tools.SemanticQueryResult{}, err
	}
	matches, err := parseGoplsReferences(text)
	if err != nil {
		return tools.SemanticQueryResult{}, err
	}
	return limitSemanticMatches(matches, request.Limit), nil
}

func successfulMCPText(toolName string, result *mcp.CallToolResult) (string, error) {
	if result == nil {
		return "", fmt.Errorf("%s returned no result", toolName)
	}
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if block != nil && block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if result.IsError {
		if text == "" {
			text = "unknown MCP tool error"
		}
		return "", fmt.Errorf("%s failed: %s", toolName, text)
	}
	return text, nil
}

func parseGoplsSearch(text string) ([]tools.SemanticMatch, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("go_search returned an empty response")
	}
	if text == "No symbols found." {
		return nil, nil
	}

	var matches []tools.SemanticMatch
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || line == "Top symbol matches:" {
			continue
		}
		if !strings.HasSuffix(line, "`)") {
			return nil, fmt.Errorf("parse go_search response line %q", line)
		}
		line = strings.TrimSuffix(line, "`)")
		inAt := strings.LastIndex(line, " in `")
		if inAt < 0 {
			return nil, fmt.Errorf("parse go_search response line %q", strings.TrimSpace(rawLine))
		}
		left, file := line[:inAt], line[inAt+len(" in `"):]
		kindAt := strings.LastIndex(left, " (")
		if kindAt < 0 {
			return nil, fmt.Errorf("parse go_search response line %q", strings.TrimSpace(rawLine))
		}
		name := strings.TrimSpace(left[:kindAt])
		kind := strings.TrimSpace(left[kindAt+2:])
		if name == "" || kind == "" || strings.TrimSpace(file) == "" {
			return nil, fmt.Errorf("parse go_search response line %q", strings.TrimSpace(rawLine))
		}
		matches = append(matches, tools.SemanticMatch{
			File: strings.TrimSpace(file),
			Name: name,
			Kind: kind,
		})
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("go_search response contained no parseable matches")
	}
	return matches, nil
}

func parseGoplsReferences(text string) ([]tools.SemanticMatch, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("go_symbol_references returned an empty response")
	}

	var (
		matches     []tools.SemanticMatch
		currentFile string
	)
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "Located in the file: "):
			currentFile = strings.TrimSpace(strings.TrimPrefix(line, "Located in the file: "))
			if currentFile == "" {
				return nil, fmt.Errorf("go_symbol_references returned an empty file location")
			}
		case strings.HasPrefix(line, "The reference is located on line "):
			if currentFile == "" {
				return nil, fmt.Errorf("go_symbol_references returned a line before its file")
			}
			rest := strings.TrimPrefix(line, "The reference is located on line ")
			lineEnd := strings.Index(rest, ",")
			if lineEnd < 0 {
				return nil, fmt.Errorf("parse go_symbol_references line %q", line)
			}
			zeroBasedLine, err := strconv.Atoi(strings.TrimSpace(rest[:lineEnd]))
			if err != nil || zeroBasedLine < 0 {
				return nil, fmt.Errorf("parse go_symbol_references line %q", line)
			}
			snippet := ""
			if marker := strings.Index(rest[lineEnd+1:], "content `"); marker >= 0 {
				snippet = rest[lineEnd+1+marker+len("content `"):]
				snippet = strings.TrimSuffix(snippet, "`")
			}
			matches = append(matches, tools.SemanticMatch{
				File:    currentFile,
				Line:    zeroBasedLine + 1,
				Snippet: snippet,
			})
			currentFile = ""
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("go_symbol_references response contained no parseable references")
	}
	return matches, nil
}

func limitSemanticMatches(matches []tools.SemanticMatch, limit int) tools.SemanticQueryResult {
	if limit <= 0 || len(matches) <= limit {
		return tools.SemanticQueryResult{Matches: matches}
	}
	return tools.SemanticQueryResult{
		Matches:   append([]tools.SemanticMatch(nil), matches[:limit]...),
		Truncated: true,
	}
}
