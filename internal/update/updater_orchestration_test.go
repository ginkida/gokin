package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestUpdater builds an Updater wired to temp dirs, with the checker's
// GitHub API root pointed at the given httptest server (nil handler leaves
// the default baseURL — use only for paths that must not touch the network).
func newTestUpdater(t *testing.T, cfg *Config, currentVersion string, handler http.Handler) *Updater {
	t.Helper()
	dir := t.TempDir()
	u := &Updater{
		config:     cfg,
		currentVer: currentVersion,
		cacheDir:   filepath.Join(dir, "cache"),
		tempDir:    filepath.Join(dir, "tmp"),
	}
	u.checker = NewChecker(cfg, u.cacheDir)
	if handler != nil {
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)
		u.checker.baseURL = ts.URL
	}
	u.downloader = NewDownloader(cfg, u.tempDir)
	inst, err := NewInstaller(cfg, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	u.installer = inst
	return u
}

// platformAssetName returns the canonical release asset name for the test
// runner's platform — the exact-match branch of FindAssetForPlatform.
func platformAssetName() string {
	return fmt.Sprintf("gokin-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

func TestNewUpdater(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	t.Run("nil config uses defaults", func(t *testing.T) {
		u, err := NewUpdater(nil, "1.2.3")
		if err != nil {
			t.Fatalf("NewUpdater: %v", err)
		}
		if u.GetConfig() == nil {
			t.Fatal("GetConfig() = nil")
		}
		if got := u.GetCurrentVersion(); got != "1.2.3" {
			t.Errorf("GetCurrentVersion() = %q, want 1.2.3", got)
		}
		if !strings.HasPrefix(u.cacheDir, tmpHome) {
			t.Errorf("cacheDir = %q, want under temp HOME %q", u.cacheDir, tmpHome)
		}
	})

	t.Run("invalid config rejected", func(t *testing.T) {
		_, err := NewUpdater(&Config{GitHubRepo: ""}, "1.0.0")
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("err = %v, want ErrInvalidConfig", err)
		}
	})
}

func TestUpdater_CheckForUpdate_Disabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = false
	u := newTestUpdater(t, cfg, "1.0.0", nil)

	_, err := u.CheckForUpdate(context.Background())
	if !errors.Is(err, ErrUpdateDisabled) {
		t.Fatalf("err = %v, want ErrUpdateDisabled", err)
	}
}

// The full success path: fetch → version compare → platform asset match →
// checksum asset match → in-memory + persistent cache. A second call inside
// CheckInterval must be served from the in-memory cache (no extra HTTP hit).
func TestUpdater_CheckForUpdate_EndToEnd_CachesResult(t *testing.T) {
	var hits int32
	assetName := platformAssetName()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/repos/test/repo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"tag_name": "v1.1.0",
			"body": "release notes here",
			"html_url": "https://example.com/release",
			"published_at": "2026-07-01T00:00:00Z",
			"assets": [
				{"name": %q, "browser_download_url": "https://example.com/%s", "size": 1234},
				{"name": "checksums.txt", "browser_download_url": "https://example.com/checksums.txt", "size": 100}
			]
		}`, assetName, assetName)
	})

	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	u := newTestUpdater(t, cfg, "1.0.0", handler)

	info, err := u.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate: %v", err)
	}
	if info.CurrentVersion != "1.0.0" || info.NewVersion != "v1.1.0" {
		t.Errorf("versions = %q → %q, want 1.0.0 → v1.1.0", info.CurrentVersion, info.NewVersion)
	}
	if info.AssetName != assetName {
		t.Errorf("AssetName = %q, want %q", info.AssetName, assetName)
	}
	if info.AssetURL != "https://example.com/"+assetName {
		t.Errorf("AssetURL = %q", info.AssetURL)
	}
	if info.AssetSize != 1234 {
		t.Errorf("AssetSize = %d, want 1234", info.AssetSize)
	}
	if info.ChecksumURL != "https://example.com/checksums.txt" {
		t.Errorf("ChecksumURL = %q, want checksums.txt URL", info.ChecksumURL)
	}
	if info.ReleaseNotes != "release notes here" {
		t.Errorf("ReleaseNotes = %q", info.ReleaseNotes)
	}

	// Second call within CheckInterval: in-memory cache, no new HTTP hit.
	info2, err := u.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("cached CheckForUpdate: %v", err)
	}
	if info2 != info {
		t.Error("cached call should return the same *UpdateInfo")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("HTTP hits = %d, want 1 (second call served from cache)", got)
	}

	// Persistent cache written by saveCache.
	cache, err := u.checker.LoadCache()
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if !u.checker.IsCacheValid(cache) {
		t.Error("saved cache should be valid within CheckInterval")
	}
	if !cache.UpdateAvailable || cache.LatestVersion != "v1.1.0" {
		t.Errorf("cache = %+v, want UpdateAvailable with v1.1.0", cache)
	}
}

func TestUpdater_CheckForUpdate_SameVersion(t *testing.T) {
	u := newTestUpdater(t, DefaultConfig(), "1.0.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name": "v1.0.0", "prerelease": false, "draft": false}`)
	}))

	_, err := u.CheckForUpdate(context.Background())
	if !errors.Is(err, ErrSameVersion) {
		t.Fatalf("err = %v, want ErrSameVersion", err)
	}
}

func TestUpdater_CheckForUpdate_NoMatchingAsset(t *testing.T) {
	u := newTestUpdater(t, DefaultConfig(), "1.0.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"tag_name": "v2.0.0",
			"assets": [{"name": "gokin-plan9-mips.tar.gz", "browser_download_url": "https://example.com/x"}]
		}`)
	}))

	_, err := u.CheckForUpdate(context.Background())
	if !errors.Is(err, ErrNoAsset) {
		t.Fatalf("err = %v, want ErrNoAsset", err)
	}
}

func TestUpdater_CheckForUpdateIfDue_ValidCacheSkipsNetwork(t *testing.T) {
	var hits int32
	counting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	})

	t.Run("update available", func(t *testing.T) {
		u := newTestUpdater(t, DefaultConfig(), "1.0.0", counting)
		if err := u.checker.SaveCache(&UpdateCache{
			LastCheck:       time.Now(),
			LatestVersion:   "v9.9.9",
			UpdateAvailable: true,
			ReleaseNotes:    "cached notes",
			AssetName:       "gokin-x",
		}); err != nil {
			t.Fatalf("SaveCache: %v", err)
		}

		info, err := u.CheckForUpdateIfDue(context.Background())
		if err != nil {
			t.Fatalf("CheckForUpdateIfDue: %v", err)
		}
		if info.NewVersion != "v9.9.9" || info.ReleaseNotes != "cached notes" {
			t.Errorf("info from cache = %+v", info)
		}
	})

	t.Run("no update available", func(t *testing.T) {
		u := newTestUpdater(t, DefaultConfig(), "1.0.0", counting)
		if err := u.checker.SaveCache(&UpdateCache{
			LastCheck:       time.Now(),
			UpdateAvailable: false,
		}); err != nil {
			t.Fatalf("SaveCache: %v", err)
		}

		_, err := u.CheckForUpdateIfDue(context.Background())
		if !errors.Is(err, ErrNoUpdate) {
			t.Fatalf("err = %v, want ErrNoUpdate", err)
		}
	})

	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("HTTP hits = %d, want 0 (valid cache must skip network)", got)
	}
}

func TestUpdater_CheckForUpdateIfDue_NoCacheFallsThrough(t *testing.T) {
	u := newTestUpdater(t, DefaultConfig(), "1.0.0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{
			"tag_name": "v1.1.0",
			"assets": [{"name": %q, "browser_download_url": "https://example.com/a"}]
		}`, platformAssetName())
	}))

	info, err := u.CheckForUpdateIfDue(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdateIfDue: %v", err)
	}
	if info.NewVersion != "v1.1.0" {
		t.Errorf("NewVersion = %q, want v1.1.0", info.NewVersion)
	}
}

func TestUpdater_ShouldAutoCheck(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Enabled = false
		u := newTestUpdater(t, cfg, "1.0.0", nil)
		if u.ShouldAutoCheck() {
			t.Error("disabled updater should not auto-check")
		}
	})

	t.Run("auto-check off", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.AutoCheck = false
		u := newTestUpdater(t, cfg, "1.0.0", nil)
		if u.ShouldAutoCheck() {
			t.Error("AutoCheck=false should not auto-check")
		}
	})

	t.Run("no cache → check", func(t *testing.T) {
		u := newTestUpdater(t, DefaultConfig(), "1.0.0", nil)
		if !u.ShouldAutoCheck() {
			t.Error("missing cache should trigger auto-check")
		}
	})

	t.Run("valid cache → skip", func(t *testing.T) {
		u := newTestUpdater(t, DefaultConfig(), "1.0.0", nil)
		if err := u.checker.SaveCache(&UpdateCache{LastCheck: time.Now()}); err != nil {
			t.Fatalf("SaveCache: %v", err)
		}
		if u.ShouldAutoCheck() {
			t.Error("valid cache should suppress auto-check")
		}
	})
}

func TestUpdater_DelegatorsAndDownloadGuard(t *testing.T) {
	cfg := DefaultConfig()
	u := newTestUpdater(t, cfg, "2.3.4", nil)

	if u.GetConfig() != cfg {
		t.Error("GetConfig should return the configured *Config")
	}
	if got := u.GetCurrentVersion(); got != "2.3.4" {
		t.Errorf("GetCurrentVersion() = %q, want 2.3.4", got)
	}

	backups, err := u.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("ListBackups = %d entries, want 0", len(backups))
	}

	if err := u.Rollback(); !errors.Is(err, ErrNoBackup) {
		t.Errorf("Rollback() err = %v, want ErrNoBackup", err)
	}
	if err := u.RollbackTo("no-such-id"); err == nil {
		t.Error("RollbackTo with unknown ID should fail")
	}

	// Cleanup removes the temp dir contents; create it first.
	if err := os.MkdirAll(u.tempDir, 0755); err != nil {
		t.Fatalf("MkdirAll tempDir: %v", err)
	}
	if err := u.Cleanup(); err != nil {
		t.Errorf("Cleanup: %v", err)
	}

	// Download with nil info is rejected before any operation starts.
	if _, err := u.Download(context.Background(), nil, nil); err == nil {
		t.Error("Download(nil) should fail")
	}
}
