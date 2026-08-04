//go:build !windows

package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloaderTempStoragePermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("raw binary"))
	}))
	defer server.Close()
	tempDir := filepath.Join(t.TempDir(), "tmp")
	d := NewDownloader(DefaultConfig(), tempDir)
	d.validateURL = allowTestUpdateURL
	downloaded, err := d.Download(context.Background(), server.URL+"/gokin", nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	dirInfo, err := os.Stat(tempDir)
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("temp dir mode = %o, want 700", got)
	}
	fileInfo, err := os.Stat(downloaded)
	if err != nil {
		t.Fatalf("stat download: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("download mode = %o, want 600", got)
	}
}

func TestDownloaderRejectsSymlinkTempDirAndRawBinary(t *testing.T) {
	external := t.TempDir()
	symlinkDir := filepath.Join(t.TempDir(), "tmp")
	if err := os.Symlink(external, symlinkDir); err != nil {
		t.Fatalf("symlink temp dir: %v", err)
	}
	d := NewDownloader(DefaultConfig(), symlinkDir)
	if err := d.ensureTempDir(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink temp error = %v", err)
	}
	if err := d.Cleanup(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink cleanup error = %v", err)
	}

	realBinary := filepath.Join(external, "real-gokin")
	if err := os.WriteFile(realBinary, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write real binary: %v", err)
	}
	symlinkBinary := filepath.Join(t.TempDir(), "gokin")
	if err := os.Symlink(realBinary, symlinkBinary); err != nil {
		t.Fatalf("symlink binary: %v", err)
	}
	if _, err := NewDownloader(DefaultConfig(), t.TempDir()).ExtractBinary(symlinkBinary, "gokin"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink raw binary error = %v", err)
	}
}
