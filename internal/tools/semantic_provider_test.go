package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type stubSemanticProvider struct {
	referencesResult SemanticQueryResult
	referencesErr    error
	searchResult     SemanticQueryResult
	searchErr        error
	referenceCalls   atomic.Int64
	searchCalls      atomic.Int64
}

func (p *stubSemanticProvider) FindReferences(context.Context, SemanticReferencesRequest) (SemanticQueryResult, error) {
	p.referenceCalls.Add(1)
	return p.referencesResult, p.referencesErr
}

func (p *stubSemanticProvider) SearchSymbols(context.Context, SemanticSearchRequest) (SemanticQueryResult, error) {
	p.searchCalls.Add(1)
	return p.searchResult, p.searchErr
}

func semanticData(t *testing.T, result ToolResult) SemanticResultData {
	t.Helper()
	data, ok := result.Data.(SemanticResultData)
	if !ok {
		t.Fatalf("expected SemanticResultData, got %T (%#v)", result.Data, result.Data)
	}
	return data
}

func TestFindReferencesProviderTransportErrorIsNotNotFound(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	provider := &stubSemanticProvider{referencesErr: errors.New("transport closed")}
	tool := NewFindReferencesTool(dir)
	tool.SetSemanticProvider(provider)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file": srcFile, "symbol": "DefinitelyMissing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("bounded fallback should execute, got %q", result.Error)
	}
	data := semanticData(t, result)
	if data.Source != SemanticSourceFallback {
		t.Fatalf("source=%q, want fallback", data.Source)
	}
	if !strings.Contains(data.DegradedReason, "transport closed") {
		t.Fatalf("missing provider failure in degraded_reason: %#v", data)
	}
	if strings.Contains(result.Content, "Managed semantic provider found no references") {
		t.Fatalf("transport error was misreported as semantic not-found: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Semantic references are unknown") {
		t.Fatalf("fallback must state uncertainty after provider failure: %s", result.Content)
	}
}

func TestFindReferencesProviderHonestEmptyDoesNotFallBack(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	provider := &stubSemanticProvider{}
	tool := NewFindReferencesTool(dir)
	tool.SetSemanticProvider(provider)

	// GetValue has lexical matches in the fixture. An honest empty provider
	// result must still remain empty instead of being silently replaced by grep.
	result, err := tool.Execute(context.Background(), map[string]any{
		"file": srcFile, "symbol": "GetValue",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := semanticData(t, result)
	if data.Source != SemanticSourceProvider || data.MatchCount != 0 || data.DegradedReason != "" {
		t.Fatalf("honest empty provider result lost: %#v", data)
	}
	if provider.referenceCalls.Load() != 1 {
		t.Fatalf("provider calls=%d, want 1", provider.referenceCalls.Load())
	}
	if !strings.Contains(result.Content, "found no references") {
		t.Fatalf("expected authoritative empty result, got %s", result.Content)
	}
}

func TestGoToDefinitionProviderFailureFallsBackWithReason(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	provider := &stubSemanticProvider{searchErr: errors.New("connection reset")}
	tool := NewGoToDefinitionTool(dir)
	tool.SetSemanticProvider(provider)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file": srcFile, "symbol": "NewMyStruct",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := semanticData(t, result)
	if data.Source != SemanticSourceFallback || !strings.Contains(data.DegradedReason, "connection reset") {
		t.Fatalf("expected typed degraded AST result, got %#v", data)
	}
	if data.MatchCount == 0 {
		t.Fatalf("AST fallback should refine the local declaration: %s", result.Content)
	}
}

func TestGoToDefinitionProviderExactSearchIsRefinedByAST(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	provider := &stubSemanticProvider{searchResult: SemanticQueryResult{Matches: []SemanticMatch{{
		File: srcFile,
		Name: "NewMyStruct",
		Kind: "function",
	}}}}
	tool := NewGoToDefinitionTool(dir)
	tool.SetSemanticProvider(provider)

	result, err := tool.Execute(context.Background(), map[string]any{
		"file": srcFile, "symbol": "NewMyStruct",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := semanticData(t, result)
	if data.Source != SemanticSourceProvider || data.MatchCount != 1 {
		t.Fatalf("unexpected provider definition result: %#v", data)
	}
	if data.Matches[0].Line != 7 {
		t.Fatalf("AST refinement line=%d, want 7", data.Matches[0].Line)
	}
}

func TestFindReferencesGitFallbackIncludesUntrackedGoFiles(t *testing.T) {
	dir := setupGitRepo(t)
	untracked := filepath.Join(dir, "new_work.go")
	if err := os.WriteFile(untracked, []byte("package main\n\nfunc NewWork() {}\nvar _ = NewWork\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewFindReferencesTool(dir).referencesViaGrep(context.Background(), "NewWork")
	if err != nil {
		t.Fatal(err)
	}
	data := semanticData(t, result)
	if data.Source != SemanticSourceFallback || data.MatchCount != 2 {
		t.Fatalf("unexpected untracked fallback result: %#v\n%s", data, result.Content)
	}
	for _, match := range data.Matches {
		if match.File != "new_work.go" {
			t.Fatalf("expected untracked new_work.go, got %#v", match)
		}
	}
	if !strings.Contains(result.Content, "git grep --untracked") {
		t.Fatalf("expected streamed git fallback label: %s", result.Content)
	}
}

func TestFindReferencesFileFallbackIsBounded(t *testing.T) {
	dir := t.TempDir()
	var source strings.Builder
	source.WriteString("package bounded\n")
	for i := 0; i < symbolMatchCap+10; i++ {
		fmt.Fprintf(&source, "var Bounded%d = BoundedNeedle\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "bounded.go"), []byte(source.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewFindReferencesTool(dir).referencesViaFileScan(context.Background(), "BoundedNeedle")
	if err != nil {
		t.Fatal(err)
	}
	data := semanticData(t, result)
	if data.MatchCount != symbolMatchCap || !data.Truncated || len(data.Matches) != symbolMatchCap {
		t.Fatalf("fallback was not bounded: %#v", data)
	}
}

func TestSemanticToolValidationIsStrict(t *testing.T) {
	tools := []struct {
		name string
		tool Tool
	}{
		{"definition", NewGoToDefinitionTool(t.TempDir())},
		{"references", NewFindReferencesTool(t.TempDir())},
	}
	invalid := []struct {
		name string
		args map[string]any
	}{
		{"missing file", map[string]any{"symbol": "X"}},
		{"blank file", map[string]any{"file": "  ", "symbol": "X"}},
		{"non-string file", map[string]any{"file": 1, "symbol": "X"}},
		{"non-Go file", map[string]any{"file": "README.md", "symbol": "X"}},
		{"missing symbol", map[string]any{"file": "x.go"}},
		{"blank symbol", map[string]any{"file": "x.go", "symbol": " \t"}},
		{"zero line", map[string]any{"file": "x.go", "symbol": "X", "line": 0}},
		{"fractional line", map[string]any{"file": "x.go", "symbol": "X", "line": 1.5}},
		{"column without line", map[string]any{"file": "x.go", "symbol": "X", "column": 1}},
		{"zero column", map[string]any{"file": "x.go", "symbol": "X", "line": 1, "column": 0}},
		{"wrong include type", map[string]any{"file": "x.go", "symbol": "X", "include_definition": "yes"}},
	}
	for _, toolCase := range tools {
		for _, testCase := range invalid {
			t.Run(toolCase.name+"/"+testCase.name, func(t *testing.T) {
				if err := toolCase.tool.Validate(testCase.args); err == nil {
					t.Fatalf("Validate(%#v) unexpectedly succeeded", testCase.args)
				}
			})
		}
		if err := toolCase.tool.Validate(map[string]any{
			"file": "x.go", "symbol": "X", "line": float64(1), "column": float64(1),
		}); err != nil {
			t.Fatalf("%s valid position rejected: %v", toolCase.name, err)
		}
	}
}

func TestSemanticToolRejectsOutOfRangePosition(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	result, err := NewFindReferencesTool(dir).Execute(context.Background(), map[string]any{
		"file": srcFile, "symbol": "GetValue", "line": 999, "column": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "exceeds file length") {
		t.Fatalf("out-of-range position was accepted: %#v", result)
	}
}

func TestGoSearchProviderHonestEmptyAndASTFallback(t *testing.T) {
	dir, _ := setupGoModule(t)
	untracked := filepath.Join(dir, "untracked_symbol.go")
	if err := os.WriteFile(untracked, []byte("package main\n\ntype BrilliantCache struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &stubSemanticProvider{}
	managed := NewGoSearchTool(dir)
	managed.SetSemanticProvider(provider)
	empty, err := managed.Execute(context.Background(), map[string]any{"query": "BrilliantCache"})
	if err != nil {
		t.Fatal(err)
	}
	emptyData := semanticData(t, empty)
	if emptyData.Source != SemanticSourceProvider || emptyData.MatchCount != 0 {
		t.Fatalf("honest provider empty must not AST-fallback: %#v", emptyData)
	}

	fallback, err := NewGoSearchTool(dir).Execute(context.Background(), map[string]any{"query": "brill cache"})
	if err != nil {
		t.Fatal(err)
	}
	fallbackData := semanticData(t, fallback)
	if fallbackData.Source != SemanticSourceFallback || fallbackData.MatchCount != 1 {
		t.Fatalf("AST fallback did not find untracked fuzzy symbol: %#v\n%s", fallbackData, fallback.Content)
	}
	if fallbackData.Matches[0].Name != "BrilliantCache" {
		t.Fatalf("unexpected search match: %#v", fallbackData.Matches[0])
	}
}

func TestSemanticProviderSetterIsRaceSafe(t *testing.T) {
	dir, srcFile := setupGoModule(t)
	tool := NewFindReferencesTool(dir)
	providers := []*stubSemanticProvider{{}, {referencesErr: errors.New("offline")}}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				tool.SetSemanticProvider(providers[(index+j)%len(providers)])
				_, _ = tool.Execute(context.Background(), map[string]any{
					"file": srcFile, "symbol": "GetValue",
				})
			}
		}(i)
	}
	wg.Wait()
}
