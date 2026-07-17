package update

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- NewInstaller / GetCurrentBinaryPath / GetRollbackManager (0%) ---

func TestNewInstaller(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	if inst == nil {
		t.Fatal("expected non-nil installer")
	}
	if inst.GetCurrentBinaryPath() == "" {
		t.Error("GetCurrentBinaryPath should return non-empty path")
	}
	if inst.GetRollbackManager() == nil {
		t.Error("GetRollbackManager should return non-nil manager")
	}
}

func TestNewInstaller_DefaultMaxBackups(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	cfg.MaxBackups = 0 // should be clamped by Validate
	_ = cfg.Validate()
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}
	rm := inst.GetRollbackManager()
	if rm.maxBackups < 1 {
		t.Errorf("maxBackups = %d, want >= 1", rm.maxBackups)
	}
}

// --- SetProgressCallback / reportProgress (0%) ---

func TestSetProgressCallback(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	var received *UpdateProgress
	inst.SetProgressCallback(func(p *UpdateProgress) {
		received = p
	})

	// reportProgress(StatusInstalling) fires at line 53 BEFORE CreateBackup,
	// so a non-cancelled context with a bad binary path still triggers the callback.
	_ = inst.Install(context.Background(), "/nonexistent/binary", "1.0.0")

	if received == nil {
		t.Fatal("expected progress callback to fire during Install")
	}
	if received.Status != StatusInstalling {
		t.Errorf("Status = %v, want StatusInstalling", received.Status)
	}
}

// --- Install cancelled context (0% → partial) ---

func TestInstall_CancelledContext(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = inst.Install(ctx, "/nonexistent", "1.0.0")
	if err == nil {
		t.Fatal("expected error from Install with cancelled context")
	}
}

// --- verifyBinary (0%) ---

func TestVerifyBinary_NonExistent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := inst.verifyBinary("/nonexistent/path/binary"); err == nil {
		t.Fatal("expected error for non-existent binary")
	}
}

func TestVerifyBinary_TooSmall(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "tiny")
	if err := os.WriteFile(binPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := inst.verifyBinary(binPath); err == nil {
		t.Fatal("expected error for too-small binary")
	}
}

func TestVerifyBinary_ValidExecutable(t *testing.T) {
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "valid-binary")
	// Write >1MB to pass the size check
	data := make([]byte, 2*1024*1024)
	if err := os.WriteFile(binPath, data, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := inst.verifyBinary(binPath); err != nil {
		t.Fatalf("verifyBinary on valid binary: %v", err)
	}
}

func TestVerifyBinary_NotRegularFile(t *testing.T) {
	tmp := t.TempDir()
	dirPath := filepath.Join(tmp, "somedir")
	if err := os.Mkdir(dirPath, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.GitHubRepo = "test/repo"
	inst, err := NewInstaller(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("NewInstaller: %v", err)
	}

	if err := inst.verifyBinary(dirPath); err == nil {
		t.Fatal("expected error for directory (not regular file)")
	}
}

// --- copyFile (64.7% → 100%) ---

func TestCopyFile_NonExistentSource(t *testing.T) {
	if err := copyFile("/nonexistent/src", filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("expected error for non-existent source")
	}
}

func TestCopyFile_Success(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// --- copyFileWithContext (0%) ---

func TestCopyFileWithContext_Success(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	content := []byte("context copy test")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyFileWithContext(context.Background(), src, dst); err != nil {
		t.Fatalf("copyFileWithContext: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestCopyFileWithContext_Cancelled(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copyFileWithContext(ctx, src, dst); err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

func TestCopyFileWithContext_NonExistentSource(t *testing.T) {
	if err := copyFileWithContext(context.Background(), "/nonexistent", filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("expected error for non-existent source")
	}
}

// --- Checker cache methods (LoadCache 0%, SaveCache 71.4%) ---

func TestSaveAndLoadCache(t *testing.T) {
	cacheDir := t.TempDir()
	c := NewChecker(DefaultConfig(), cacheDir)

	cache := &UpdateCache{
		LatestVersion: "1.2.3",
		LastCheck:     time.Now(),
		ReleaseInfo: &ReleaseInfo{
			TagName: "v1.2.3",
			Name:    "Release 1.2.3",
		},
	}

	if err := c.SaveCache(cache); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}

	loaded, err := c.LoadCache()
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	if loaded.LatestVersion != "1.2.3" {
		t.Errorf("LatestVersion = %q, want 1.2.3", loaded.LatestVersion)
	}
}

func TestLoadCache_NoFile(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	if _, err := c.LoadCache(); err == nil {
		t.Fatal("expected error when no cache file exists")
	}
}

func TestLoadCache_InvalidJSON(t *testing.T) {
	cacheDir := t.TempDir()
	cachePath := filepath.Join(cacheDir, "update_cache.json")
	if err := os.WriteFile(cachePath, []byte("{invalid json}"), 0644); err != nil {
		t.Fatal(err)
	}
	c := NewChecker(DefaultConfig(), cacheDir)
	if _, err := c.LoadCache(); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestIsCacheValid_Extras(t *testing.T) {
	cfg := DefaultConfig()
	c := NewChecker(cfg, t.TempDir())

	// Fresh cache is valid
	fresh := &UpdateCache{LastCheck: time.Now()}
	if !c.IsCacheValid(fresh) {
		t.Error("fresh cache should be valid")
	}

	// Stale cache is invalid
	stale := &UpdateCache{LastCheck: time.Now().Add(-2 * cfg.CheckInterval)}
	if c.IsCacheValid(stale) {
		t.Error("stale cache should be invalid")
	}

	// Nil cache is invalid
	if c.IsCacheValid(nil) {
		t.Error("nil cache should be invalid")
	}
}

// --- checkResponse edge cases ---

func TestCheckResponse_RateLimited(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
	}
	resp.Header.Set("X-RateLimit-Remaining", "0")
	if err := c.checkResponse(resp); err != ErrRateLimited {
		t.Errorf("expected ErrRateLimited, got %v", err)
	}
}

func TestCheckResponse_PermissionDenied(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
	}
	if err := c.checkResponse(resp); err != ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestCheckResponse_NotFound(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
	}
	if err := c.checkResponse(resp); err != ErrNoReleases {
		t.Errorf("expected ErrNoReleases, got %v", err)
	}
}

func TestCheckResponse_Unauthorized(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     make(http.Header),
	}
	if err := c.checkResponse(resp); err != ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestCheckResponse_ServerError(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("server error")),
	}
	if err := c.checkResponse(resp); err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// --- RollbackManager lifecycle ---

func TestRollbackManager_CreateAndListBackups(t *testing.T) {
	backupDir := t.TempDir()
	rm := NewRollbackManager(backupDir, 3)

	// Create a fake binary to back up
	tmp := t.TempDir()
	binPath := filepath.Join(tmp, "gokin")
	data := make([]byte, 2*1024*1024)
	if err := os.WriteFile(binPath, data, 0755); err != nil {
		t.Fatal(err)
	}

	info, err := rm.CreateBackup(binPath, "1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info == nil || info.Path == "" {
		t.Fatal("expected non-empty backup info")
	}

	backups, err := rm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 1 {
		t.Errorf("len(backups) = %d, want 1", len(backups))
	}
}

func TestRollbackManager_GetLatestBackup_Empty(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	if _, err := rm.GetLatestBackup(); err != ErrNoBackup {
		t.Errorf("expected ErrNoBackup, got %v", err)
	}
}

func TestRollbackManager_Rollback_NotFound(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	if err := rm.Rollback("nonexistent-id"); err == nil {
		t.Fatal("expected error for non-existent backup ID")
	}
}

func TestRollbackManager_RollbackToBackup_Nil(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	if err := rm.RollbackToBackup(nil); err != ErrNoBackup {
		t.Errorf("expected ErrNoBackup, got %v", err)
	}
}

func TestRollbackManager_DeleteBackup_Nil(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	if err := rm.DeleteBackup(nil); err != nil {
		t.Errorf("DeleteBackup(nil) = %v, want nil", err)
	}
}

func TestRollbackManager_LoadBackupInfo_NoJSON(t *testing.T) {
	tmp := t.TempDir()
	// Create a backup file without JSON metadata
	binPath := filepath.Join(tmp, "gokin-1.0.0-backup")
	if err := os.WriteFile(binPath, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(tmp, 3)
	info, err := rm.LoadBackupInfo(binPath)
	if err != nil {
		t.Fatalf("LoadBackupInfo without JSON: %v", err)
	}
	if info.Path != binPath {
		t.Errorf("Path = %q, want %q", info.Path, binPath)
	}
}

func TestRollbackManager_CleanupOldBackups(t *testing.T) {
	backupDir := t.TempDir()
	rm := NewRollbackManager(backupDir, 2) // keep only 2

	// Create 3 backups with different timestamps
	for i := range 3 {
		name := filepath.Join(backupDir, "gokin-1.0.0-"+time.Now().Add(time.Duration(i)*time.Second).Format("150405"))
		if err := os.WriteFile(name, []byte("binary"), 0755); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := rm.CleanupOldBackups(); err != nil {
		t.Fatalf("CleanupOldBackups: %v", err)
	}

	backups, err := rm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) > 2 {
		t.Errorf("len(backups) = %d, want <= 2", len(backups))
	}
}

func TestRollbackManager_ListBackups_NoDir(t *testing.T) {
	rm := NewRollbackManager(filepath.Join(t.TempDir(), "nonexistent"), 3)
	backups, err := rm.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups on nonexistent dir: %v", err)
	}
	if backups != nil {
		t.Errorf("expected nil backups, got %v", backups)
	}
}

// --- FindChecksumAsset (0%) ---

func TestFindChecksumAsset_NilArgs(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	if got := c.FindChecksumAsset(nil, nil); got != nil {
		t.Error("expected nil for nil args")
	}
}

func TestFindChecksumAsset_Found(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	asset := &Asset{Name: "gokin-linux-amd64.tar.gz"}
	release := &ReleaseInfo{
		Assets: []Asset{
			{Name: "gokin-linux-amd64.tar.gz"},
			{Name: "gokin-linux-amd64.tar.gz.sha256"},
		},
	}
	got := c.FindChecksumAsset(release, asset)
	if got == nil {
		t.Fatal("expected to find checksum asset")
	}
	if got.Name != "gokin-linux-amd64.tar.gz.sha256" {
		t.Errorf("Name = %q, want gokin-linux-amd64.tar.gz.sha256", got.Name)
	}
}

func TestFindChecksumAsset_ChecksumsTxt(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	asset := &Asset{Name: "gokin-linux-amd64.tar.gz"}
	release := &ReleaseInfo{
		Assets: []Asset{
			{Name: "gokin-linux-amd64.tar.gz"},
			{Name: "checksums.txt"},
		},
	}
	got := c.FindChecksumAsset(release, asset)
	if got == nil {
		t.Fatal("expected to find checksums.txt")
	}
}

func TestFindChecksumAsset_NotFound(t *testing.T) {
	c := NewChecker(DefaultConfig(), t.TempDir())
	asset := &Asset{Name: "gokin-linux-amd64.tar.gz"}
	release := &ReleaseInfo{
		Assets: []Asset{
			{Name: "gokin-linux-amd64.tar.gz"},
		},
	}
	if got := c.FindChecksumAsset(release, asset); got != nil {
		t.Error("expected nil when no checksum asset found")
	}
}

// --- setHeaders with GITHUB_TOKEN (75% → 100%) ---

func TestSetHeaders_WithToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token-123")
	c := NewChecker(DefaultConfig(), t.TempDir())
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.setHeaders(req)
	if got := req.Header.Get("Authorization"); got != "token test-token-123" {
		t.Errorf("Authorization = %q, want 'token test-token-123'", got)
	}
}

func TestSetHeaders_WithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	c := NewChecker(DefaultConfig(), t.TempDir())
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.setHeaders(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty", got)
	}
}

// --- NewChecker with proxy (50% → 100%) ---

func TestNewChecker_WithProxy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Proxy = "http://proxy.example.com:8080"
	c := NewChecker(cfg, t.TempDir())
	if c == nil {
		t.Fatal("expected non-nil checker with proxy")
	}
}

func TestNewChecker_WithInvalidProxy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Proxy = "://invalid-proxy"
	c := NewChecker(cfg, t.TempDir())
	if c == nil {
		t.Fatal("expected non-nil checker even with invalid proxy URL")
	}
}
