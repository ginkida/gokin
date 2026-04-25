package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gokin/internal/config"
)

// TestChangelog_HappyPath proves the command formats a list of
// releases the way users expect: tag, date, title (or first body
// line), with a "← you" marker on the running version. Uses an
// httptest.Server so we don't hit GitHub or fail offline.
func TestChangelog_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the per_page query param made it through.
		if r.URL.Query().Get("per_page") != "5" {
			t.Errorf("expected per_page=5, got %q", r.URL.Query().Get("per_page"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"tag_name": "v0.76.0", "name": "v0.76.0 — /whats-new + post-upgrade banner",
			 "published_at": "2026-04-25T10:00:00Z", "body": "Closes the upgrade-feedback loop."},
			{"tag_name": "v0.75.0", "name": "v0.75.0 — welcome banner",
			 "published_at": "2026-04-24T17:30:00Z", "body": "Mode awareness."},
			{"tag_name": "v0.74.1", "name": "",
			 "published_at": "2026-04-24T16:00:00Z", "body": "First-line title fallback\nMore details below."}
		]`))
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

	app := newAuthApp(&config.Config{Version: "0.75.0"})
	app.cfg.Update.GitHubRepo = "ginkida/gokin"

	cmd := &ChangelogCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	must := []string{
		"v0.76.0",                  // newest tag
		"2026-04-25",               // newest date (formatted)
		"/whats-new + post-upgrade", // newest title
		"v0.75.0",                  // running version
		"← you",                    // running-version marker
		"v0.74.1",                  // empty-name fallback
		"First-line title fallback", // body's first line as title
		"/whats-new <tag>",         // pointer to detail command
	}
	for _, s := range must {
		if !strings.Contains(out, s) {
			t.Errorf("changelog output missing %q\n  got: %s", s, out)
		}
	}
}

// TestChangelog_CountArgRespectsCap — `/changelog 9999` clamps to
// changelogMaxCount so we don't fetch a giant list and freeze the TUI.
func TestChangelog_CountArgRespectsCap(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query().Get("per_page")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
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

	cmd := &ChangelogCommand{}
	if _, err := cmd.Execute(context.Background(), []string{"9999"}, app); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := "50" // changelogMaxCount
	if captured != want {
		t.Errorf("count clamp failed: per_page = %q, want %q", captured, want)
	}
}

// TestChangelog_InvalidCount surfaces a useful nudge instead of a
// confusing API error.
func TestChangelog_InvalidCount(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.cfg.Update.GitHubRepo = "ginkida/gokin"

	cmd := &ChangelogCommand{}
	out, err := cmd.Execute(context.Background(), []string{"abc"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "positive integer") {
		t.Errorf("invalid-count message should explain expectations, got: %s", out)
	}
}

// TestChangelog_EmptyResponseGivesGuidance — repo exists but has zero
// releases. Should tell the user where to look, not silently succeed.
func TestChangelog_EmptyResponseGivesGuidance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
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

	cmd := &ChangelogCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "No releases published") {
		t.Errorf("empty-response message should be human-friendly, got: %s", out)
	}
	if !strings.Contains(out, "github.com/ginkida/gokin/releases") {
		t.Errorf("output should include browser URL, got: %s", out)
	}
}

// TestTruncateChangelogTitle pins the cut-at-word-boundary behavior.
func TestTruncateChangelogTitle(t *testing.T) {
	cases := []struct {
		in, want string
		max      int
	}{
		{"short", "short", 80},
		{"exactly fifty characters of text right at the limit", "exactly fifty characters of text right at the limit", 80},
		{"this title is comfortably longer than the eighty character cap so it should be truncated cleanly", "this title is comfortably longer than the eighty character cap so it should be…", 80},
		{"unbreakablestringofcharsthatcannotbecutatawordboundarycleanly", "unbreakablestringo…", 18},
	}
	for _, tc := range cases {
		got := truncateChangelogTitle(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("truncateChangelogTitle(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
