package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestConstrainedDependencyStartupUsesCacheAndBackoff(t *testing.T) {
	for _, mode := range []string{"cached", "backoff"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := startupFixture(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
			defer server.Close()
			market := Marketplace{Source: Source{Kind: "git", URL: server.URL + "/market.git"}}
			ctx := t.Context()
			if mode == "cached" {
				ctx = context.WithValue(ctx, cachedStartupKey{}, true)
			} else if err := m.recordMarketplaceAttempt("acme", market.Source, true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_, _, _, err := m.fetchConstrainedPlugin(ctx, "leaf@acme", Source{Kind: "relative", Path: "plugins/leaf"}, market, Entry{}, t.TempDir(), InstallOptions{Background: true, versionRanges: []string{"~1.2"}})
			if err == nil || requests.Load() != 0 {
				t.Fatalf("%s dependency contacted remote: requests=%d err=%v", mode, requests.Load(), err)
			}
		})
	}
}

func TestConstrainedDependencyFailurePersistsAndExplicitFetchBypassesBackoff(t *testing.T) {
	m, _, _ := startupFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	market := Marketplace{Source: Source{Kind: "git", URL: server.URL + "/market.git"}}
	fetch := func(m *Manager, background bool) {
		t.Helper()
		unlock, err := m.lock(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		_, _, _, err = m.fetchConstrainedPlugin(t.Context(), "leaf@acme", Source{Kind: "relative", Path: "plugins/leaf"}, market, Entry{}, t.TempDir(), InstallOptions{Background: background, versionRanges: []string{"~1.2"}})
		if err == nil {
			t.Fatal("failed remote accepted")
		}
	}
	fetch(m, true)
	failures, err := m.marketplaceFailures()
	if err != nil || failures["acme"].Failures != 1 || requests.Load() != 1 {
		t.Fatalf("failure not persisted: %v %v requests=%d", failures, err, requests.Load())
	}
	fresh, err := NewManager(m.opts)
	if err != nil {
		t.Fatal(err)
	}
	fetch(fresh, true)
	if requests.Load() != 1 {
		t.Fatal("dependency retry ignored persisted backoff")
	}
	fetch(fresh, false)
	if requests.Load() != 2 {
		t.Fatal("explicit fetch did not bypass backoff")
	}
}
