package evals

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFaultProfilesAreStableAndParseable(t *testing.T) {
	profiles := FaultProfiles()
	if len(profiles) != 12 {
		t.Fatalf("FaultProfiles() returned %d profiles, want 12", len(profiles))
	}
	seen := map[string]bool{}
	for _, profile := range profiles {
		if seen[profile] {
			t.Fatalf("duplicate profile %q", profile)
		}
		seen[profile] = true
		if _, err := parseFaultProfile(profile); err != nil {
			t.Fatalf("parseFaultProfile(%q): %v", profile, err)
		}
	}
	if _, err := parseFaultProfile("not-a-profile"); err == nil {
		t.Fatal("unknown profile was accepted")
	}
}

func TestFaultProxyAfterToolInjectsExactlyOnceThenForwards(t *testing.T) {
	forwarded := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	proxy, err := StartFaultProxy(upstream.URL+"/api/anthropic", "after-tool-429-once")
	if err != nil {
		t.Fatalf("StartFaultProxy: %v", err)
	}
	defer proxy.Close(t.Context())

	post := func(body string) *http.Response {
		t.Helper()
		resp, err := http.Post(proxy.URL()+"/v1/messages", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("POST proxy: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}

	// An initial model request is not eligible for an after-tool profile.
	if resp := post(`{"messages":[{"role":"user","content":"fix it"}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("initial status = %d, want 200", resp.StatusCode)
	}
	if resp := post(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("injected status = %d, want 429", resp.StatusCode)
	}
	if resp := post(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", resp.StatusCode)
	}

	summary := proxy.Snapshot()
	if summary.Injected != 1 || summary.EligibleRequests != 2 {
		t.Fatalf("summary = %+v, want one injection and two eligible requests", summary)
	}
	if summary.MessageRequestsAfterInjection != 1 {
		t.Fatalf("post-injection message requests = %d, want 1", summary.MessageRequestsAfterInjection)
	}
	if forwarded != 2 {
		t.Fatalf("upstream requests = %d, want 2", forwarded)
	}
}

func TestValidateFaultUpstreamRejectsSensitiveURLParts(t *testing.T) {
	for _, raw := range []string{"", "ftp://example.test", "https://user:secret@example.test", "https://example.test?q=secret", "https://example.test/#fragment"} {
		if _, err := validateFaultUpstream(raw); err == nil {
			t.Errorf("validateFaultUpstream(%q) accepted unsafe URL", raw)
		}
	}
}

func TestFaultProxyRejectsUpstreamRedirect(t *testing.T) {
	destinationHits := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()

	proxy, err := StartFaultProxy(upstream.URL, "after-tool-429-once")
	if err != nil {
		t.Fatalf("StartFaultProxy: %v", err)
	}
	defer proxy.Close(t.Context())
	response, err := http.Post(proxy.URL()+"/v1/messages", "application/json",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("POST proxy: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("redirect response status = %d, want 502", response.StatusCode)
	}
	if destinationHits != 0 {
		t.Fatalf("redirect destination received %d requests", destinationHits)
	}
}
