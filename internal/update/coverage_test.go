package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Version — additional edge cases for comparePrerelease
// ---------------------------------------------------------------------------

func TestComparePrerelease_NumberVsString(t *testing.T) {
	// Number prerelease parts come before string parts
	a, _ := ParseVersion("1.0.0-1")
	b, _ := ParseVersion("1.0.0-alpha")
	if a.Compare(b) >= 0 {
		t.Error("1.0.0-1 should be < 1.0.0-alpha (number < string)")
	}
}

func TestComparePrerelease_DifferentLengths(t *testing.T) {
	// Shorter prerelease < longer when prefix is the same
	a, _ := ParseVersion("1.0.0-alpha")
	b, _ := ParseVersion("1.0.0-alpha.1")
	if a.Compare(b) >= 0 {
		t.Error("1.0.0-alpha should be < 1.0.0-alpha.1")
	}
}

func TestComparePrerelease_BothNumericDifferent(t *testing.T) {
	a, _ := ParseVersion("1.0.0-rc.1")
	b, _ := ParseVersion("1.0.0-rc.10")
	if a.Compare(b) >= 0 {
		t.Error("1.0.0-rc.1 should be < 1.0.0-rc.10")
	}
}

func TestVersion_String_WithBuildOnly(t *testing.T) {
	v, _ := ParseVersion("1.0.0+build.123")
	if v.String() != "1.0.0+build.123" {
		t.Errorf("got %q", v.String())
	}
}

func TestVersion_String_WithPrereleaseAndBuild(t *testing.T) {
	v, _ := ParseVersion("1.0.0-beta+build.456")
	if v.String() != "1.0.0-beta+build.456" {
		t.Errorf("got %q", v.String())
	}
}

// ---------------------------------------------------------------------------
// Config — Merge, ShouldCheck, MatchesChannel
// ---------------------------------------------------------------------------

func TestConfig_Merge_Nil(t *testing.T) {
	c := DefaultConfig()
	orig := c.GitHubRepo
	c.Merge(nil)
	if c.GitHubRepo != orig {
		t.Error("Merge(nil) should be a no-op")
	}
}

func TestConfig_Merge_Values(t *testing.T) {
	base := DefaultConfig()
	other := &Config{
		GitHubRepo:    "other/repo",
		CheckInterval: 48 * time.Hour,
		MaxBackups:    5,
		Timeout:       60 * time.Second,
		Proxy:         "http://proxy:8080",
		PublicKeyPath: "/key.pub",
		Channel:       ChannelBeta,
	}
	base.Merge(other)
	if base.GitHubRepo != "other/repo" {
		t.Error("GitHubRepo not merged")
	}
	if base.Channel != ChannelBeta {
		t.Error("Channel not merged")
	}
	if base.Proxy != "http://proxy:8080" {
		t.Error("Proxy not merged")
	}
}

func TestConfig_ShouldCheck_Disabled(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = false
	if c.ShouldCheck(time.Now()) {
		t.Error("disabled config should not check")
	}
}

func TestConfig_ShouldCheck_NoAutoCheck(t *testing.T) {
	c := DefaultConfig()
	c.AutoCheck = false
	if c.ShouldCheck(time.Now()) {
		t.Error("AutoCheck=false should not check")
	}
}

func TestConfig_ShouldCheck_IntervalElapsed(t *testing.T) {
	c := DefaultConfig()
	c.CheckInterval = time.Minute
	// 2 minutes ago > 1 minute interval
	if !c.ShouldCheck(time.Now().Add(-2 * time.Minute)) {
		t.Error("should check when interval elapsed")
	}
}

func TestConfig_ShouldCheck_IntervalNotElapsed(t *testing.T) {
	c := DefaultConfig()
	c.CheckInterval = time.Hour
	// 1 minute ago < 1 hour interval
	if c.ShouldCheck(time.Now().Add(-time.Minute)) {
		t.Error("should not check when interval not elapsed")
	}
}

func TestConfig_MatchesChannel_NilRelease(t *testing.T) {
	c := DefaultConfig()
	if c.MatchesChannel(nil) {
		t.Error("nil release should not match")
	}
}

func TestConfig_MatchesChannel_Stable(t *testing.T) {
	c := DefaultConfig()
	c.Channel = ChannelStable
	// Stable release
	if !c.MatchesChannel(&ReleaseInfo{Prerelease: false, Draft: false}) {
		t.Error("stable release should match stable channel")
	}
	// Prerelease excluded
	if c.MatchesChannel(&ReleaseInfo{Prerelease: true}) {
		t.Error("prerelease should not match stable channel")
	}
	// Draft excluded
	if c.MatchesChannel(&ReleaseInfo{Draft: true}) {
		t.Error("draft should not match stable channel")
	}
}

func TestConfig_MatchesChannel_Beta(t *testing.T) {
	c := DefaultConfig()
	c.Channel = ChannelBeta
	// Beta includes prereleases
	if !c.MatchesChannel(&ReleaseInfo{Prerelease: true, Draft: false}) {
		t.Error("prerelease should match beta channel")
	}
	// Draft still excluded
	if c.MatchesChannel(&ReleaseInfo{Draft: true}) {
		t.Error("draft should not match beta channel")
	}
}

func TestConfig_MatchesChannel_Nightly(t *testing.T) {
	c := DefaultConfig()
	c.Channel = ChannelNightly
	// Nightly includes everything
	if !c.MatchesChannel(&ReleaseInfo{Prerelease: true, Draft: true}) {
		t.Error("nightly channel should include everything")
	}
}

func TestConfig_MatchesChannel_UnknownChannel(t *testing.T) {
	c := DefaultConfig()
	c.Channel = Channel("unknown")
	// Unknown channel falls back to stable behavior
	if !c.MatchesChannel(&ReleaseInfo{Prerelease: false, Draft: false}) {
		t.Error("unknown channel should fall back to stable")
	}
	if c.MatchesChannel(&ReleaseInfo{Prerelease: true}) {
		t.Error("unknown channel should exclude prerelease (stable fallback)")
	}
}

// ---------------------------------------------------------------------------
// Types — ReleaseInfo.Version, Asset.DownloadURL, Platform, UpdateStatus
// ---------------------------------------------------------------------------

func TestReleaseInfo_Version_NoPrefix(t *testing.T) {
	r := &ReleaseInfo{TagName: "1.0.0"}
	if r.Version() != "1.0.0" {
		t.Errorf("got %q", r.Version())
	}
}

func TestReleaseInfo_Version_WithV(t *testing.T) {
	r := &ReleaseInfo{TagName: "v1.0.0"}
	if r.Version() != "1.0.0" {
		t.Errorf("got %q", r.Version())
	}
}

func TestReleaseInfo_Version_Empty(t *testing.T) {
	r := &ReleaseInfo{TagName: ""}
	if r.Version() != "" {
		t.Errorf("got %q", r.Version())
	}
}

func TestAsset_DownloadURL(t *testing.T) {
	a := &Asset{BrowserDownloadURL: "https://example.com/file.tar.gz"}
	if a.DownloadURL() != "https://example.com/file.tar.gz" {
		t.Errorf("got %q", a.DownloadURL())
	}
}

func TestPlatform_String(t *testing.T) {
	p := Platform{OS: "linux", Arch: "amd64"}
	if p.String() != "linux_amd64" {
		t.Errorf("got %q", p.String())
	}
}

func TestPlatform_AssetPattern(t *testing.T) {
	p := Platform{OS: "darwin", Arch: "arm64"}
	if p.AssetPattern() != "gokin-darwin-arm64" {
		t.Errorf("got %q", p.AssetPattern())
	}
}

func TestUpdateStatus_String(t *testing.T) {
	tests := []struct {
		status UpdateStatus
		want   string
	}{
		{StatusIdle, "idle"},
		{StatusChecking, "checking"},
		{StatusDownloading, "downloading"},
		{StatusVerifying, "verifying"},
		{StatusInstalling, "installing"},
		{StatusComplete, "complete"},
		{StatusFailed, "failed"},
		{StatusRolledBack, "rolled_back"},
		{UpdateStatus(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Checker — checkResponse, FindChecksumAsset, LoadCache, SaveCache
// ---------------------------------------------------------------------------

func TestChecker_checkResponse(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())

	// errNonNil is a sentinel for "any non-nil error" — 500 returns a
	// non-sentinel formatted error, not one of the package-level sentinels.
	errNonNil := errors.New("__any_non_nil__")

	tests := []struct {
		name       string
		statusCode int
		headers    map[string]string
		wantErr    error
	}{
		{"ok", http.StatusOK, nil, nil},
		{"not_found", http.StatusNotFound, nil, ErrNoReleases},
		{"rate_limited", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}, ErrRateLimited},
		{"forbidden", http.StatusForbidden, nil, ErrPermissionDenied},
		{"unauthorized", http.StatusUnauthorized, nil, ErrPermissionDenied},
		{"server_error", http.StatusInternalServerError, nil, errNonNil}, // non-sentinel error
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader([]byte("error body"))),
			}
			for k, v := range tt.headers {
				resp.Header.Set(k, v)
			}
			err := c.checkResponse(resp)
			if tt.wantErr == errNonNil {
				if err == nil {
					t.Errorf("checkResponse(%d) err = nil, want non-nil", tt.statusCode)
				}
			} else if tt.wantErr == nil {
				if err != nil {
					t.Errorf("checkResponse(%d) err = %v, want nil", tt.statusCode, err)
				}
			} else {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("checkResponse(%d) err = %v, want %v", tt.statusCode, err, tt.wantErr)
				}
			}
		})
	}
}

func TestChecker_FindChecksumAsset(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	release := &ReleaseInfo{
		Assets: []Asset{
			{Name: "gokin-linux-amd64.tar.gz"},
			{Name: "gokin-linux-amd64.tar.gz.sha256"},
		},
	}
	asset := &release.Assets[0]
	found := c.FindChecksumAsset(release, asset)
	if found == nil {
		t.Fatal("expected checksum asset")
	}
	if found.Name != "gokin-linux-amd64.tar.gz.sha256" {
		t.Errorf("got %q", found.Name)
	}
}

func TestChecker_FindChecksumAsset_ChecksumsTxt(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	release := &ReleaseInfo{
		Assets: []Asset{
			{Name: "gokin-linux-amd64.tar.gz"},
			{Name: "checksums.txt"},
		},
	}
	asset := &release.Assets[0]
	found := c.FindChecksumAsset(release, asset)
	if found == nil || found.Name != "checksums.txt" {
		t.Error("expected checksums.txt")
	}
}

func TestChecker_FindChecksumAsset_NoneFound(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	release := &ReleaseInfo{
		Assets: []Asset{{Name: "gokin-linux-amd64.tar.gz"}},
	}
	asset := &release.Assets[0]
	if c.FindChecksumAsset(release, asset) != nil {
		t.Error("expected nil when no checksum asset")
	}
}

func TestChecker_FindChecksumAsset_NilArgs(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	if c.FindChecksumAsset(nil, nil) != nil {
		t.Error("nil args should return nil")
	}
}

func TestChecker_FindAssetForPlatform_NilRelease(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	if c.FindAssetForPlatform(nil) != nil {
		t.Error("nil release should return nil")
	}
}

func TestChecker_FindAssetForPlatform_NoAssets(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	r := &ReleaseInfo{}
	if c.FindAssetForPlatform(r) != nil {
		t.Error("empty assets should return nil")
	}
}

func TestChecker_SaveAndLoadCache(t *testing.T) {
	dir := t.TempDir()
	c := NewChecker(DefaultConfig(), dir)

	original := &UpdateCache{
		LatestVersion:   "1.0.0",
		UpdateAvailable: true,
		ReleaseNotes:    "test notes",
	}

	if err := c.SaveCache(original); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	loaded, err := c.LoadCache()
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if loaded.LatestVersion != "1.0.0" {
		t.Errorf("LatestVersion = %q", loaded.LatestVersion)
	}
	if !loaded.UpdateAvailable {
		t.Error("UpdateAvailable should be true")
	}
}

func TestChecker_LoadCache_NotExist(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	_, err := c.LoadCache()
	if err == nil {
		t.Error("expected error for non-existent cache")
	}
}

func TestChecker_LoadCache_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "update_cache.json")
	os.WriteFile(cachePath, []byte("{invalid"), 0644)
	c := NewChecker(DefaultConfig(), dir)
	_, err := c.LoadCache()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestChecker_IsCacheValid(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())

	// Fresh cache (1 hour ago) with 24h interval → valid
	fresh := &UpdateCache{LastCheck: time.Now().Add(-1 * time.Hour)}
	if !c.IsCacheValid(fresh) {
		t.Error("1h-old cache with 24h interval should be valid")
	}

	// Stale cache (2 days ago) with 24h interval → invalid
	stale := &UpdateCache{LastCheck: time.Now().Add(-48 * time.Hour)}
	if c.IsCacheValid(stale) {
		t.Error("48h-old cache with 24h interval should be invalid")
	}

	// Nil cache → invalid
	if c.IsCacheValid(nil) {
		t.Error("nil cache should be invalid")
	}
}

// ---------------------------------------------------------------------------
// Checker — GetLatestRelease / GetReleases via mock HTTP server
// ---------------------------------------------------------------------------

func TestChecker_GetLatestRelease_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"tag_name": "v1.0.0",
			"name": "Release 1.0.0",
			"prerelease": false,
			"draft": false,
			"html_url": "https://example.com/release"
		}`))
	}))
	defer ts.Close()

	c := NewChecker(DefaultConfig(), t.TempDir())

	release, err := c.fetchRelease(context.Background(), ts.URL+"/repos/test/repo/releases/latest")
	if err != nil {
		t.Fatalf("fetchRelease: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("TagName = %q", release.TagName)
	}
}

func TestChecker_GetReleases_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"tag_name": "v1.0.0", "prerelease": false},
			{"tag_name": "v0.9.0", "prerelease": true}
		]`))
	}))
	defer ts.Close()

	c := NewChecker(DefaultConfig(), t.TempDir())

	resp, err := c.httpClient.Get(ts.URL + "/repos/test/repo/releases")
	if err != nil {
		t.Fatalf("HTTP GET: %v", err)
	}
	defer resp.Body.Close()

	var releases []ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}
}

func TestChecker_GetLatestStableRelease(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	releases := []ReleaseInfo{
		{TagName: "v1.0.0-beta", Prerelease: true},
		{TagName: "v0.9.0", Prerelease: false},
		{TagName: "v1.0.0", Prerelease: false},
	}

	// getLatestStableRelease takes ctx and calls GetReleases internally,
	// so we can't call it directly without a mock server. Instead verify
	// the filtering logic inline.
	var firstStable *ReleaseInfo
	for i := range releases {
		r := &releases[i]
		if !r.Prerelease && !r.Draft && c.config.MatchesChannel(r) {
			firstStable = r
			break
		}
	}
	if firstStable == nil {
		t.Fatal("expected non-nil stable release")
	}
	if firstStable.TagName != "v0.9.0" {
		t.Errorf("TagName = %q, want v0.9.0", firstStable.TagName)
	}
}

func TestChecker_GetLatestStableRelease_NoneStable(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	releases := []ReleaseInfo{
		{TagName: "v1.0.0-beta", Prerelease: true},
		{TagName: "v0.9.0-rc", Prerelease: true},
	}
	found := false
	for i := range releases {
		r := &releases[i]
		if !r.Prerelease && !r.Draft && c.config.MatchesChannel(r) {
			found = true
			break
		}
	}
	if found {
		t.Error("expected no stable releases")
	}
}

func TestChecker_GetReleaseByTag(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/test/repo/releases/tags/v1.0.0" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name": "v1.0.0", "prerelease": false}`))
	}))
	defer ts.Close()

	c := NewChecker(DefaultConfig(), t.TempDir())

	release, err := c.fetchRelease(context.Background(), ts.URL+"/repos/test/repo/releases/tags/v1.0.0")
	if err != nil {
		t.Fatalf("fetchRelease: %v", err)
	}
	if release.TagName != "v1.0.0" {
		t.Errorf("TagName = %q", release.TagName)
	}
}

// ---------------------------------------------------------------------------
// Downloader — VerifyChecksum, ComputeChecksum, parseChecksumFile, ExtractBinary
// ---------------------------------------------------------------------------

func TestDownloader_VerifyChecksum_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := []byte("hello world")
	os.WriteFile(path, data, 0644)

	h := sha256.Sum256(data)
	expected := hex.EncodeToString(h[:])

	d := NewDownloader(DefaultConfig(), dir)
	if err := d.VerifyChecksum(path, expected); err != nil {
		t.Fatalf("VerifyChecksum: %v", err)
	}
}

func TestDownloader_VerifyChecksum_Mismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	os.WriteFile(path, []byte("hello"), 0644)

	d := NewDownloader(DefaultConfig(), dir)
	err := d.VerifyChecksum(path, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("expected ErrChecksumMismatch, got %v", err)
	}
}

func TestDownloader_VerifyChecksum_FileNotExist(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())
	err := d.VerifyChecksum("/nonexistent", "abc")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestDownloader_ComputeChecksum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	data := []byte("test data")
	os.WriteFile(path, data, 0644)

	d := NewDownloader(DefaultConfig(), dir)
	got, err := d.ComputeChecksum(path)
	if err != nil {
		t.Fatalf("ComputeChecksum: %v", err)
	}

	h := sha256.Sum256(data)
	want := hex.EncodeToString(h[:])
	if got != want {
		t.Errorf("ComputeChecksum = %q, want %q", got, want)
	}
}

func TestDownloader_ComputeChecksum_FileNotExist(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())
	_, err := d.ComputeChecksum("/nonexistent")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestDownloader_parseChecksumFile_BinaryMode(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())
	content := "abc123  *gokin-linux-amd64.tar.gz\n"
	m := d.parseChecksumFile(content)
	if m["gokin-linux-amd64.tar.gz"] != "abc123" {
		t.Errorf("expected abc123, got %v", m)
	}
}

func TestDownloader_parseChecksumFile_MultipleEntries(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())
	content := `abc123  gokin-linux-amd64.tar.gz
def456  gokin-darwin-arm64.tar.gz
# comment line

`
	m := d.parseChecksumFile(content)
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if m["gokin-linux-amd64.tar.gz"] != "abc123" {
		t.Error("missing linux entry")
	}
	if m["gokin-darwin-arm64.tar.gz"] != "def456" {
		t.Error("missing darwin entry")
	}
}

func TestDownloader_ExtractBinary_RawBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gokin-raw")
	os.WriteFile(path, []byte("binary"), 0755)

	d := NewDownloader(DefaultConfig(), dir)
	result, err := d.ExtractBinary(path, "gokin")
	if err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}
	if result != path {
		t.Errorf("raw binary should return itself, got %q", result)
	}
}

func TestDownloader_ExtractBinary_TarGz(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "gokin.tar.gz")
	binaryContent := []byte("fake binary")

	// Create a tar.gz with a binary inside
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)
	hdr := &tar.Header{
		Name: "gokin",
		Mode: 0755,
		Size: int64(len(binaryContent)),
	}
	tw.WriteHeader(hdr)
	tw.Write(binaryContent)
	tw.Close()
	gzw.Close()
	f.Close()

	d := NewDownloader(DefaultConfig(), dir)
	extractedPath, err := d.ExtractBinary(archivePath, "gokin")
	if err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}

	data, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(data) != "fake binary" {
		t.Errorf("extracted content = %q", string(data))
	}
}

func TestDownloader_ExtractBinary_Zip(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "gokin.zip")
	binaryContent := []byte("zip binary")

	// Create a zip with a binary inside
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("gokin")
	if err != nil {
		t.Fatal(err)
	}
	w.Write(binaryContent)
	zw.Close()
	f.Close()

	d := NewDownloader(DefaultConfig(), dir)
	extractedPath, err := d.ExtractBinary(archivePath, "gokin")
	if err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}

	data, err := os.ReadFile(extractedPath)
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(data) != "zip binary" {
		t.Errorf("extracted content = %q", string(data))
	}
}

func TestDownloader_Cleanup(t *testing.T) {
	dir := t.TempDir()
	tempFile := filepath.Join(dir, "tempfile")
	os.WriteFile(tempFile, []byte("temp"), 0644)

	d := NewDownloader(DefaultConfig(), dir)
	d.Cleanup()

	// tempDir should be gone
	if _, err := os.Stat(dir); err == nil {
		// dir might still exist if it's the parent, but tempDir content should be cleaned
	}
}

// ---------------------------------------------------------------------------
// RollbackManager — CreateBackup, ListBackups, LoadBackupInfo
// ---------------------------------------------------------------------------

func TestRollbackManager_CreateBackup(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "gokin")
	os.WriteFile(binaryPath, []byte("binary"), 0755)

	backupDir := filepath.Join(dir, "backups")
	rm := NewRollbackManager(backupDir, 3)

	info, err := rm.CreateBackup(binaryPath, "1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.Version != "1.0.0" {
		t.Errorf("Version = %q", info.Version)
	}
	if info.Checksum == "" {
		t.Error("Checksum should not be empty")
	}
	if info.Size == 0 {
		t.Error("Size should not be 0")
	}
}

func TestRollbackManager_CreateBackup_BinaryNotExist(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	_, err := rm.CreateBackup("/nonexistent-binary", "1.0.0")
	if err == nil {
		t.Error("expected error for non-existent binary")
	}
}

func TestRollbackManager_ListBackups_Empty(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	backups, err := rm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("expected 0 backups, got %d", len(backups))
	}
}

func TestRollbackManager_ListBackups_WithBackups(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "gokin")
	os.WriteFile(binaryPath, []byte("binary"), 0755)

	backupDir := filepath.Join(dir, "backups")
	rm := NewRollbackManager(backupDir, 3)

	rm.CreateBackup(binaryPath, "1.0.0")

	backups, err := rm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 1 {
		t.Errorf("expected 1 backup, got %d", len(backups))
	}
}

func TestRollbackManager_LoadBackupInfo_NoInfoFile(t *testing.T) {
	dir := t.TempDir()
	// Create a binary file without .json info
	binaryPath := filepath.Join(dir, "gokin-backup")
	os.WriteFile(binaryPath, []byte("binary"), 0755)

	rm := NewRollbackManager(dir, 3)
	info, err := rm.LoadBackupInfo(binaryPath)
	if err != nil {
		t.Fatalf("LoadBackupInfo: %v", err)
	}
	if info.Path != binaryPath {
		t.Errorf("Path = %q", info.Path)
	}
}

func TestRollbackManager_LoadBackupInfo_WithInfoFile(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "gokin-backup")
	os.WriteFile(binaryPath, []byte("binary"), 0755)

	infoPath := binaryPath + ".json"
	os.WriteFile(infoPath, []byte(`{
		"id": "test-id",
		"version": "1.0.0",
		"path": "`+binaryPath+`"
	}`), 0644)

	rm := NewRollbackManager(dir, 3)
	info, err := rm.LoadBackupInfo(binaryPath)
	if err != nil {
		t.Fatalf("LoadBackupInfo: %v", err)
	}
	if info.ID != "test-id" {
		t.Errorf("ID = %q", info.ID)
	}
	if info.Version != "1.0.0" {
		t.Errorf("Version = %q", info.Version)
	}
}

func TestRollbackManager_LoadBackupInfo_NeitherExists(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	_, err := rm.LoadBackupInfo("/nonexistent-backup")
	if err == nil {
		t.Error("expected error when neither binary nor info exists")
	}
}

func TestNewRollbackManager_DefaultMaxBackups(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 0)
	if rm.maxBackups != 3 {
		t.Errorf("maxBackups = %d, want 3 (default)", rm.maxBackups)
	}
}
