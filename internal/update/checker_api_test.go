package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newAPITestChecker builds a Checker whose GitHub API root is the given
// httptest server, so the real GetLatestRelease/GetReleases/GetReleaseByTag
// wrappers run end-to-end instead of being reimplemented inline in tests.
func newAPITestChecker(t *testing.T, handler http.Handler) (*Checker, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	c := NewChecker(cfg, t.TempDir())
	c.baseURL = ts.URL
	return c, ts
}

func TestChecker_GetLatestRelease_StableEndToEnd(t *testing.T) {
	c, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/test/repo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name": "v1.0.0", "name": "Release 1.0.0", "prerelease": false, "draft": false}`)
	}))

	release, err := c.GetLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("TagName = %q, want v1.0.0", release.TagName)
	}
	if release.Version() != "1.0.0" {
		t.Errorf("Version() = %q, want 1.0.0", release.Version())
	}
}

// GetLatestRelease on a prerelease with prereleases disabled must fall back
// to GetReleases and return the first non-prerelease, non-draft entry —
// exercising the whole GetLatestRelease → getLatestStableRelease →
// GetReleases chain through the real code.
func TestChecker_GetLatestRelease_PrereleaseFallbackEndToEnd(t *testing.T) {
	c, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/test/repo/releases/latest":
			fmt.Fprint(w, `{"tag_name": "v2.0.0-beta", "prerelease": true}`)
		case "/repos/test/repo/releases":
			fmt.Fprint(w, `[
				{"tag_name": "v2.0.0-beta", "prerelease": true},
				{"tag_name": "v1.9.5", "prerelease": false, "draft": true},
				{"tag_name": "v1.9.0", "prerelease": false, "draft": false}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))

	release, err := c.GetLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("GetLatestRelease: %v", err)
	}
	if release.TagName != "v1.9.0" {
		t.Errorf("TagName = %q, want v1.9.0 (first stable, drafts skipped)", release.TagName)
	}
}

func TestChecker_GetLatestRelease_AllPrereleaseReturnsErrNoReleases(t *testing.T) {
	c, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/test/repo/releases/latest":
			fmt.Fprint(w, `{"tag_name": "v2.0.0-beta", "prerelease": true}`)
		case "/repos/test/repo/releases":
			fmt.Fprint(w, `[
				{"tag_name": "v2.0.0-beta", "prerelease": true},
				{"tag_name": "v2.0.0-rc", "prerelease": true}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))

	_, err := c.GetLatestRelease(context.Background())
	if !errors.Is(err, ErrNoReleases) {
		t.Fatalf("err = %v, want ErrNoReleases", err)
	}
}

func TestChecker_GetReleases_EndToEnd(t *testing.T) {
	c, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/test/repo/releases" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("per_page"); got != "5" {
			t.Errorf("per_page = %q, want 5", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[
			{"tag_name": "v1.1.0", "prerelease": false},
			{"tag_name": "v1.0.0", "prerelease": false}
		]`)
	}))

	releases, err := c.GetReleases(context.Background(), 5)
	if err != nil {
		t.Fatalf("GetReleases: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("len(releases) = %d, want 2", len(releases))
	}
	if releases[0].TagName != "v1.1.0" {
		t.Errorf("releases[0].TagName = %q, want v1.1.0", releases[0].TagName)
	}
}

func TestChecker_GetReleases_NetworkError(t *testing.T) {
	c, ts := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	ts.Close() // Nothing listening → connection refused.

	_, err := c.GetReleases(context.Background(), 5)
	if !errors.Is(err, ErrNetworkError) {
		t.Fatalf("err = %v, want ErrNetworkError", err)
	}
}

func TestChecker_GetReleaseByTag_EndToEnd(t *testing.T) {
	c, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/test/repo/releases/tags/v1.2.3" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name": "v1.2.3", "prerelease": false}`)
	}))

	release, err := c.GetReleaseByTag(context.Background(), "v1.2.3")
	if err != nil {
		t.Fatalf("GetReleaseByTag: %v", err)
	}
	if release.TagName != "v1.2.3" {
		t.Errorf("TagName = %q, want v1.2.3", release.TagName)
	}
}

func TestChecker_GetReleaseByTag_NotFound(t *testing.T) {
	c, _ := newAPITestChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))

	_, err := c.GetReleaseByTag(context.Background(), "v9.9.9")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}
