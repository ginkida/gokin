package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupCachedDoesNotContactUnreachableMarketplace(t *testing.T) {
	m, _, _ := startupFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("cache-only startup contacted network") }))
	endpoint := server.URL
	server.Close()
	path, fields, err := m.settings("user")
	if err != nil {
		t.Fatal(err)
	}
	fields["extraKnownMarketplaces"], _ = json.Marshal(map[string]any{"acme": marketplaceDeclaration{Source: Source{Kind: "url", URL: endpoint}}})
	writeTestJSON(t, path, fields)
	started := time.Now()
	report, err := m.SyncCached(t.Context())
	if err != nil || time.Since(started) > 500*time.Millisecond || len(report.Registered) != 0 {
		t.Fatalf("cached startup=%+v %v duration=%s", report, err, time.Since(started))
	}
}

func TestMarketplaceBackoffPersistsAndExplicitUpdateBypassesIt(t *testing.T) {
	m, _, _ := startupFixture(t)
	var calls atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"acme","owner":{"name":"Test"},"plugins":[]}`))
	}))
	defer server.Close()
	path, fields, err := m.settings("user")
	if err != nil {
		t.Fatal(err)
	}
	on := true
	source := Source{Kind: "url", URL: server.URL}
	fields["extraKnownMarketplaces"], _ = json.Marshal(map[string]any{"acme": marketplaceDeclaration{Source: source, AutoUpdate: &on}})
	writeTestJSON(t, path, fields)
	report, err := m.RefreshStartup(t.Context())
	if err != nil || calls.Load() != 1 || len(report.Errors) != 1 {
		t.Fatalf("initial=%+v %v calls=%d", report, err, calls.Load())
	}
	failures, err := m.marketplaceFailures()
	if err != nil || failures["acme"].Failures != 1 || failures["acme"].LastFailed.IsZero() {
		t.Fatalf("failures=%+v %v", failures, err)
	}
	fresh, err := NewManager(m.opts)
	if err != nil {
		t.Fatal(err)
	}
	report, err = fresh.RefreshStartup(t.Context())
	if err != nil || calls.Load() != 1 || !strings.Contains(strings.Join(report.Skipped, " "), "backoff") {
		t.Fatalf("backoff=%+v %v calls=%d", report, err, calls.Load())
	}
	// Explicit sync bypasses backoff and a successful fetch resets it.
	healthy.Store(true)
	if report, err := fresh.SyncStartup(t.Context()); err != nil || len(report.Errors) != 0 || len(report.Registered) != 1 {
		t.Fatalf("explicit sync did not bypass backoff: %+v %v", report, err)
	}
	failures, err = fresh.marketplaceFailures()
	if err != nil || len(failures) != 0 {
		t.Fatalf("successful sync did not reset failures: %+v %v", failures, err)
	}
	healthy.Store(false)
	if _, err := fresh.UpdateMarketplace(t.Context(), "acme", nil); err == nil {
		t.Fatal("expected unavailable server")
	}
	before := calls.Load()
	report, err = fresh.AutoUpdate(t.Context())
	if err != nil || calls.Load() != before {
		t.Fatalf("automatic update ignored backoff: %+v %v", report, err)
	}
	// Expired state permits another automatic attempt, doubling the window.
	failures, _ = fresh.marketplaceFailures()
	entry := failures["acme"]
	entry.LastFailed = time.Now().Add(-7 * time.Hour)
	failures["acme"] = entry
	writeTestJSON(t, filepath.Join(m.pluginDir, "gokin", "marketplace_failures.json"), failures)
	if _, err := fresh.AutoUpdate(context.Background()); err != nil {
		t.Fatal(err)
	}
	failures, _ = fresh.marketplaceFailures()
	if failures["acme"].Failures != 2 || marketplaceBackoff(2) != 12*time.Hour || marketplaceBackoff(3) != 24*time.Hour || marketplaceBackoff(100) != 24*time.Hour {
		t.Fatalf("backoff did not increase/cap: %+v", failures)
	}
	healthy.Store(true)
	if _, err := fresh.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	failures, _ = fresh.marketplaceFailures()
	if len(failures) != 0 {
		t.Fatalf("explicit update did not clear backoff: %+v", failures)
	}
}
