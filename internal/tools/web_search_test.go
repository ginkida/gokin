package tools

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Sample DuckDuckGo Lite HTML (simplified but structurally identical to real output).
const ddgLiteHTML = `<html><body>
<table>
<tr><td><a class="result-link" href="https://go.dev">The Go Programming Language</a></td></tr>
<tr><td class="result-snippet">Go is an open source programming language supported by Google.</td></tr>
<tr><td><span class="link-text">go.dev</span></td></tr>
<tr><td></td></tr>

<tr><td><a class="result-link" href="https://golang.org/doc/">Documentation - The Go Programming Language</a></td></tr>
<tr><td class="result-snippet">Official Go documentation and tutorials.</td></tr>
<tr><td><span class="link-text">golang.org/doc/</span></td></tr>
<tr><td></td></tr>

<tr><td><a class="result-link" href="https://github.com/golang/go">GitHub - golang/go</a></td></tr>
<tr><td class="result-snippet">The Go programming language repository.</td></tr>
<tr><td><span class="link-text">github.com/golang/go</span></td></tr>
<tr><td></td></tr>
</table>
</body></html>`

func TestParseDuckDuckGoLite(t *testing.T) {
	results, err := parseDuckDuckGoLite(strings.NewReader(ddgLiteHTML), 10)
	if err != nil {
		t.Fatalf("parseDuckDuckGoLite() error = %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	want := []SearchResult{
		{Title: "The Go Programming Language", URL: "https://go.dev", Snippet: "Go is an open source programming language supported by Google."},
		{Title: "Documentation - The Go Programming Language", URL: "https://golang.org/doc/", Snippet: "Official Go documentation and tutorials."},
		{Title: "GitHub - golang/go", URL: "https://github.com/golang/go", Snippet: "The Go programming language repository."},
	}

	for i, got := range results {
		if got.Title != want[i].Title {
			t.Errorf("result[%d].Title = %q, want %q", i, got.Title, want[i].Title)
		}
		if got.URL != want[i].URL {
			t.Errorf("result[%d].URL = %q, want %q", i, got.URL, want[i].URL)
		}
		if got.Snippet != want[i].Snippet {
			t.Errorf("result[%d].Snippet = %q, want %q", i, got.Snippet, want[i].Snippet)
		}
	}
}

func TestParseDuckDuckGoLiteMaxResults(t *testing.T) {
	results, err := parseDuckDuckGoLite(strings.NewReader(ddgLiteHTML), 2)
	if err != nil {
		t.Fatalf("parseDuckDuckGoLite() error = %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestParseDuckDuckGoLiteEmptyHTML(t *testing.T) {
	results, err := parseDuckDuckGoLite(strings.NewReader("<html><body></body></html>"), 5)
	if err != nil {
		t.Fatalf("parseDuckDuckGoLite() error = %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

func TestWebSearchValidateDuckDuckGoNoKey(t *testing.T) {
	tool := NewWebSearchTool()
	tool.SetProvider(SearchProviderDuckDuckGo)

	err := tool.Validate(map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("Validate() should pass for DuckDuckGo without API key, got: %v", err)
	}
}

func TestWebSearchValidateSerpAPINoKeyFallback(t *testing.T) {
	tool := NewWebSearchTool()
	// Default provider is SerpAPI, but no key — Execute() will fallback to DDG.
	// Validate should pass because the fallback handles it.
	err := tool.Validate(map[string]any{"query": "golang"})
	if err != nil {
		t.Fatalf("Validate() should pass for SerpAPI without key (DDG fallback), got: %v", err)
	}
}

func TestWebSearchValidateGoogleNoKeyFails(t *testing.T) {
	tool := NewWebSearchTool()
	tool.SetProvider(SearchProviderGoogle)

	err := tool.Validate(map[string]any{"query": "golang"})
	if err == nil {
		t.Fatal("Validate() should fail for Google without API key")
	}
}

func TestDuckDuckGoIntegration(t *testing.T) {
	if os.Getenv("GOKIN_INTEGRATION") == "" {
		t.Skip("set GOKIN_INTEGRATION=1 to run integration tests")
	}

	tool := NewWebSearchTool()
	tool.SetProvider(SearchProviderDuckDuckGo)

	result, err := tool.Execute(context.Background(), map[string]any{
		"query":       "golang programming language",
		"num_results": 5,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() failed: %s", result.Error)
	}

	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatal("expected map[string]any data")
	}
	count, ok := data["count"].(int)
	if !ok || count == 0 {
		t.Fatalf("expected results, got count=%v", data["count"])
	}
	t.Logf("Got %d results", count)
	t.Logf("Output:\n%s", result.Content)
}
