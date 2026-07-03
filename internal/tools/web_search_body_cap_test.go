package tools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// oversizedSerpAPIBody builds a well-formed SerpAPI-shaped JSON body whose
// total size comfortably exceeds the 2MB read cap — many organic_results
// entries padded with a long snippet each.
func oversizedSerpAPIBody() []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"organic_results":[`)
	pad := strings.Repeat("x", 4096)
	for i := 0; i < 700; i++ { // ~700 * ~4.1KB ≈ 2.9MB, over the 2MB cap
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`{"title":"t","link":"https://example.com","snippet":"` + pad + `"}`)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

func oversizedGoogleBody() []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"items":[`)
	pad := strings.Repeat("x", 4096)
	for i := 0; i < 700; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`{"title":"t","link":"https://example.com","snippet":"` + pad + `"}`)
	}
	buf.WriteString(`]}`)
	return buf.Bytes()
}

func fakeJSONClient(body []byte) *http.Client {
	return &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
}

// TestSearchSerpAPI_OversizedBodyIsCapped (round 4) pins the fix: an
// oversized 200-OK response body must be truncated by a size cap rather than
// decoded in full — proven by asserting decode FAILS on a well-formed
// document that exceeds the cap (without the cap, this same document decodes
// successfully). This is the same class of guard CLAUDE.md documents for
// HTTP error bodies (64KB) and the DuckDuckGo success path (2MB) — those
// caps existed everywhere in this file except the SerpAPI/Google success
// paths, which decoded resp.Body directly with no bound.
func TestSearchSerpAPI_OversizedBodyIsCapped(t *testing.T) {
	body := oversizedSerpAPIBody()
	if len(body) <= 2<<20 {
		t.Fatalf("test fixture body is %d bytes, want > 2MB to actually exercise the cap", len(body))
	}

	tool := &WebSearchTool{client: fakeJSONClient(body), apiKey: "test-key"}
	_, err := tool.searchSerpAPI(context.Background(), "golang", 10)
	if err == nil {
		t.Fatal("expected a decode error from the capped reader on an oversized body, got nil (the cap is not being applied)")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Fatalf("error = %q, want a JSON parse failure (proves the LimitReader truncated the document)", err.Error())
	}
}

func TestSearchGoogle_OversizedBodyIsCapped(t *testing.T) {
	body := oversizedGoogleBody()
	if len(body) <= 2<<20 {
		t.Fatalf("test fixture body is %d bytes, want > 2MB to actually exercise the cap", len(body))
	}

	tool := &WebSearchTool{client: fakeJSONClient(body), apiKey: "test-key", googleCX: "test-cx"}
	_, err := tool.searchGoogle(context.Background(), "golang", 10)
	if err == nil {
		t.Fatal("expected a decode error from the capped reader on an oversized body, got nil (the cap is not being applied)")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Fatalf("error = %q, want a JSON parse failure (proves the LimitReader truncated the document)", err.Error())
	}
}

// TestSearchSerpAPI_NormalBodyStillWorks is the happy-path regression check —
// an ordinary, well-under-the-cap response must still decode successfully.
func TestSearchSerpAPI_NormalBodyStillWorks(t *testing.T) {
	body := []byte(`{"organic_results":[{"title":"Go","link":"https://go.dev","snippet":"The Go language"}]}`)
	tool := &WebSearchTool{client: fakeJSONClient(body), apiKey: "test-key"}
	results, err := tool.searchSerpAPI(context.Background(), "golang", 10)
	if err != nil {
		t.Fatalf("searchSerpAPI: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Go" {
		t.Fatalf("results = %+v, want one Go result", results)
	}
}

func TestSearchGoogle_NormalBodyStillWorks(t *testing.T) {
	body := []byte(`{"items":[{"title":"Go","link":"https://go.dev","snippet":"The Go language"}]}`)
	tool := &WebSearchTool{client: fakeJSONClient(body), apiKey: "test-key", googleCX: "test-cx"}
	results, err := tool.searchGoogle(context.Background(), "golang", 10)
	if err != nil {
		t.Fatalf("searchGoogle: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Go" {
		t.Fatalf("results = %+v, want one Go result", results)
	}
}
