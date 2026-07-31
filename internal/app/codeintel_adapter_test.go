package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/codeintel"
	"gokin/internal/mcp"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type fakeManagedCodeIntel struct {
	mu            sync.Mutex
	calls         []string
	closed        int
	diagnoseFiles []string
	workDir       string
}

func (f *fakeManagedCodeIntel) Capabilities(context.Context) ([]codeintel.Capability, error) {
	return nil, nil
}

func (f *fakeManagedCodeIntel) CallReadOnly(_ context.Context, name string, _ map[string]any) (*mcp.CallToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()

	var text string
	mainFile := filepath.Join(f.workDir, "main.go")
	switch name {
	case "go_search":
		text = fmt.Sprintf("Top symbol matches:\n\tFoo (Function in `%s`)\n", mainFile)
	case "go_symbol_references":
		text = "The object has 2 references. Their locations are listed below\n" +
			fmt.Sprintf("Reference 1\nLocated in the file: %s\n", mainFile) +
			"The reference is located on line 2, which has content `func Foo() {}`\n\n" +
			fmt.Sprintf("Reference 2\nLocated in the file: %s\n", mainFile) +
			"The reference is located on line 3, which has content `var _ = Foo`\n"
	default:
		return nil, fmt.Errorf("unexpected tool %q", name)
	}
	return &mcp.CallToolResult{Content: []*mcp.ContentBlock{{
		Type: "text",
		Text: text,
	}}}, nil
}

func (f *fakeManagedCodeIntel) Diagnose(_ context.Context, files []string) (codeintel.DiagnosticsReport, error) {
	f.mu.Lock()
	f.diagnoseFiles = append([]string(nil), files...)
	f.mu.Unlock()
	return codeintel.DiagnosticsReport{Clean: true, Source: codeintel.DiagnosticsSource}, nil
}

func (f *fakeManagedCodeIntel) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

func (f *fakeManagedCodeIntel) snapshot() (calls []string, closed int, files []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), f.closed, append([]string(nil), f.diagnoseFiles...)
}

func TestWireCodeIntelligenceActivatesSemanticToolsAndDiagnostics(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	source := "package sample\n\nfunc Foo() {}\nvar _ = Foo\n"
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := tools.DefaultRegistry(workDir)
	executor := tools.NewExecutor(registry, nil, 0)
	executor.SetWorkDir(workDir)
	provider := &fakeManagedCodeIntel{workDir: workDir}
	builder := &Builder{workDir: workDir, registry: registry, executor: executor}
	builder.wireCodeIntelligence(provider)

	if builder.codeIntelProvider != provider {
		t.Fatal("builder did not retain managed provider for shutdown")
	}
	rawSearch, err := (&codeIntelligenceAdapter{provider: provider}).SearchSymbols(
		context.Background(), tools.SemanticSearchRequest{Query: "Foo", Limit: 10})
	if err != nil || len(rawSearch.Matches) != 1 {
		t.Fatalf("raw managed search=%#v err=%v", rawSearch, err)
	}

	searchTool, ok := registry.Get("go_search")
	if !ok {
		t.Fatal("go_search not registered")
	}
	searchResult, err := searchTool.Execute(context.Background(), map[string]any{"query": "Foo"})
	if err != nil || !searchResult.Success {
		t.Fatalf("go_search result=%+v err=%v", searchResult, err)
	}
	searchData, ok := searchResult.Data.(tools.SemanticResultData)
	if !ok || searchData.Source != tools.SemanticSourceProvider || searchData.MatchCount != 1 {
		t.Fatalf("go_search did not use managed provider: %#v", searchResult.Data)
	}

	definitionTool, ok := registry.Get("go_to_definition")
	if !ok {
		t.Fatal("go_to_definition not registered")
	}
	definitionResult, err := definitionTool.Execute(context.Background(), map[string]any{
		"file": "main.go", "symbol": "Foo",
	})
	if err != nil || !definitionResult.Success {
		t.Fatalf("go_to_definition result=%+v err=%v", definitionResult, err)
	}
	definitionData, ok := definitionResult.Data.(tools.SemanticResultData)
	if !ok || definitionData.Source != tools.SemanticSourceProvider ||
		definitionData.MatchCount != 1 || definitionData.Matches[0].Line != 3 {
		t.Fatalf("go_to_definition did not refine managed result: %#v", definitionResult.Data)
	}

	referencesTool, ok := registry.Get("find_references")
	if !ok {
		t.Fatal("find_references not registered")
	}
	referencesResult, err := referencesTool.Execute(context.Background(), map[string]any{
		"file": "main.go", "symbol": "Foo",
	})
	if err != nil || !referencesResult.Success {
		t.Fatalf("find_references result=%+v err=%v", referencesResult, err)
	}
	referencesData, ok := referencesResult.Data.(tools.SemanticResultData)
	if !ok || referencesData.Source != tools.SemanticSourceProvider ||
		referencesData.MatchCount != 2 || referencesData.Matches[0].Line != 3 {
		t.Fatalf("find_references did not use one-based managed locations: %#v", referencesResult.Data)
	}

	adapter := &codeIntelligenceAdapter{provider: provider}
	report, err := adapter.Diagnose(context.Background(), []string{"main.go"})
	if err != nil || !report.Clean || report.Source != codeintel.DiagnosticsSource {
		t.Fatalf("diagnostics adapter report=%+v err=%v", report, err)
	}
	calls, _, files := provider.snapshot()
	if strings.Join(calls, ",") != "go_search,go_search,go_search,go_symbol_references" {
		t.Fatalf("managed calls=%v", calls)
	}
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("diagnostic files=%v", files)
	}
}

func TestCodeIntelligenceAdapterRejectsMalformedProviderOutput(t *testing.T) {
	if _, err := parseGoplsSearch("Top symbol matches:\nnot a location"); err == nil {
		t.Fatal("malformed go_search output was accepted as authoritative")
	}
	if _, err := parseGoplsReferences("Reference 1\nThe reference is located on line 4"); err == nil {
		t.Fatal("reference without a file was accepted")
	}
}

func TestGracefulShutdownClosesManagedCodeIntelligence(t *testing.T) {
	provider := &fakeManagedCodeIntel{}
	application := &App{codeIntelProvider: provider}
	application.gracefulShutdown(context.Background())
	_, closed, _ := provider.snapshot()
	if closed != 1 {
		t.Fatalf("managed provider close calls=%d, want 1", closed)
	}
}

func TestCodeIntelligenceAdapterInstalledGoplsIntegration(t *testing.T) {
	if os.Getenv("GOKIN_TEST_REAL_GOPLS_MCP") != "1" {
		t.Skip("set GOKIN_TEST_REAL_GOPLS_MCP=1 to exercise the installed gopls")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls not installed: %v", err)
	}
	workspace, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := codeintel.NewGoplsProvider(workspace, codeintel.Options{
		StartupTimeout: 15 * time.Second,
		CallTimeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	adapter := &codeIntelligenceAdapter{provider: provider}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	search, err := adapter.SearchSymbols(ctx, tools.SemanticSearchRequest{
		Query: "codeIntelligenceAdapter",
		Limit: 10,
	})
	if err != nil || len(search.Matches) == 0 {
		t.Fatalf("real gopls search=%#v err=%v", search, err)
	}
	references, err := adapter.FindReferences(ctx, tools.SemanticReferencesRequest{
		File:              filepath.Join(workspace, "internal", "app", "codeintel_adapter.go"),
		Symbol:            "codeIntelligenceAdapter",
		IncludeDefinition: true,
		Limit:             20,
	})
	if err != nil || len(references.Matches) == 0 {
		t.Fatalf("real gopls references=%#v err=%v", references, err)
	}
	for _, match := range references.Matches {
		if match.Line < 1 || !strings.HasSuffix(match.File, ".go") {
			t.Fatalf("invalid real gopls reference: %#v", match)
		}
	}
	if _, err := adapter.Diagnose(ctx, []string{
		filepath.Join(workspace, "internal", "app", "codeintel_adapter.go"),
	}); err != nil {
		t.Fatalf("real gopls diagnostics: %v", err)
	}
}
