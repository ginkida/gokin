package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gokin/internal/config"
)

// TestWhatsNew_FormatsReleaseFromAPI exercises the full happy path:
// command runs, hits the API, parses the response, returns markdown
// the TUI can render. Uses an httptest.Server so we don't hammer
// GitHub or fail when offline.
func TestWhatsNew_FormatsReleaseFromAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity check the URL shape — confirms we built the right
		// /repos/owner/name/releases/tags/<tag> path.
		if !strings.Contains(r.URL.Path, "/repos/ginkida/gokin/releases/tags/v0.74.0") {
			t.Errorf("unexpected URL path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"name": "v0.74.0 — self-update actually works",
			"tag_name": "v0.74.0",
			"html_url": "https://github.com/ginkida/gokin/releases/tag/v0.74.0",
			"body": "## Headline fix\n\nFixed broken default repo.",
			"published_at": "2026-04-24T13:57:57Z"
		}`))
	}))
	defer srv.Close()

	origURL := whatsNewBaseURL
	origClient := whatsNewHTTPClient
	whatsNewBaseURL = srv.URL
	whatsNewHTTPClient = srv.Client()
	defer func() {
		whatsNewBaseURL = origURL
		whatsNewHTTPClient = origClient
	}()

	app := newAuthApp(&config.Config{})
	app.cfg.Update.GitHubRepo = "ginkida/gokin"

	cmd := &WhatsNewCommand{}
	out, err := cmd.Execute(context.Background(), []string{"v0.74.0"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	must := []string{
		"v0.74.0 — self-update actually works", // title
		"Published 2026-04-24",                  // formatted date
		"github.com/ginkida/gokin/releases/tag", // back-link
		"Headline fix",                          // body
		"broken default repo",                   // body content preserved
	}
	for _, s := range must {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n  got: %s", s, out)
		}
	}
}

// TestWhatsNew_AddsVPrefix verifies a user typing `/whats-new 0.74.0`
// (no `v` prefix) still resolves correctly. The version field on the
// binary is bare semver, but GitHub tags have the `v` — normalize.
func TestWhatsNew_AddsVPrefix(t *testing.T) {
	var capturedTag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path is /repos/.../releases/tags/<tag>
		parts := strings.Split(r.URL.Path, "/")
		capturedTag = parts[len(parts)-1]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.74.0","body":"x"}`))
	}))
	defer srv.Close()

	origURL := whatsNewBaseURL
	origClient := whatsNewHTTPClient
	whatsNewBaseURL = srv.URL
	whatsNewHTTPClient = srv.Client()
	defer func() {
		whatsNewBaseURL = origURL
		whatsNewHTTPClient = origClient
	}()

	app := newAuthApp(&config.Config{})
	app.cfg.Update.GitHubRepo = "ginkida/gokin"

	cmd := &WhatsNewCommand{}
	if _, err := cmd.Execute(context.Background(), []string{"0.74.0"}, app); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if capturedTag != "v0.74.0" {
		t.Errorf("expected `v0.74.0` tag in URL, got %q", capturedTag)
	}
}

// TestWhatsNew_404FallsBackGracefully — the most common error is
// "tag doesn't exist yet" (typo or release not published). The
// command must surface a useful message + a browser-friendly URL the
// user can paste, NOT crash or return a useless "API error 404".
func TestWhatsNew_404FallsBackGracefully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	origURL := whatsNewBaseURL
	origClient := whatsNewHTTPClient
	whatsNewBaseURL = srv.URL
	whatsNewHTTPClient = srv.Client()
	defer func() {
		whatsNewBaseURL = origURL
		whatsNewHTTPClient = origClient
	}()

	app := newAuthApp(&config.Config{})
	app.cfg.Update.GitHubRepo = "ginkida/gokin"

	cmd := &WhatsNewCommand{}
	out, err := cmd.Execute(context.Background(), []string{"v999.999.999"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "no release published") {
		t.Errorf("404 message should be human-friendly, got: %s", out)
	}
	if !strings.Contains(out, "https://github.com/ginkida/gokin/releases/tag/v999.999.999") {
		t.Errorf("output should include browser-fallback URL, got: %s", out)
	}
}

// TestWhatsNew_NoRepoConfigured surfaces a helpful message instead of
// hitting a malformed URL.
func TestWhatsNew_NoRepoConfigured(t *testing.T) {
	app := newAuthApp(&config.Config{}) // empty Update.GitHubRepo
	cmd := &WhatsNewCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "GitHub repo not configured") {
		t.Errorf("expected helpful message, got: %s", out)
	}
}
