package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTarGzBinary(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o4777, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func TestDownloaderPreservesArchiveSuffixAndExtractsBinaryEndToEnd(t *testing.T) {
	binary := []byte("real executable payload")
	archive := makeTarGzBinary(t, "dist/gokin", binary)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	d := NewDownloader(DefaultConfig(), filepath.Join(t.TempDir(), "tmp"))
	d.validateURL = allowTestUpdateURL
	downloaded, err := d.Download(context.Background(), server.URL+"/gokin-linux-amd64.tar.gz?download=1", nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.HasSuffix(downloaded, ".tar.gz") {
		t.Fatalf("downloaded archive lost its suffix: %q", downloaded)
	}
	extracted, err := d.ExtractBinary(downloaded, "gokin")
	if err != nil {
		t.Fatalf("ExtractBinary: %v", err)
	}
	if extracted == downloaded {
		t.Fatal("archive was mistaken for a raw executable")
	}
	got, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(got) != string(binary) {
		t.Fatalf("extracted content = %q, want %q", got, binary)
	}
}

func TestUpdaterDownloadReturnsVerifiedExtractedBinary(t *testing.T) {
	binary := []byte("verified executable payload")
	archive := makeTarGzBinary(t, "release/gokin", binary)
	digest := sha256.Sum256(archive)
	checksum := hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gokin-linux-amd64.tar.gz":
			_, _ = w.Write(archive)
		case "/checksums.txt":
			fmt.Fprintf(w, "%s  gokin-linux-amd64.tar.gz\n", checksum)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := DefaultConfig()
	tempDir := filepath.Join(t.TempDir(), "tmp")
	d := NewDownloader(cfg, tempDir)
	d.validateURL = allowTestUpdateURL
	u := &Updater{config: cfg, downloader: d}
	info := &UpdateInfo{
		AssetURL:    server.URL + "/gokin-linux-amd64.tar.gz",
		AssetName:   "gokin-linux-amd64.tar.gz",
		AssetSize:   int64(len(archive)),
		ChecksumURL: server.URL + "/checksums.txt",
	}

	extracted, err := u.Download(context.Background(), info, nil)
	if err != nil {
		t.Fatalf("Updater.Download: %v", err)
	}
	if strings.HasSuffix(extracted, ".tar.gz") {
		t.Fatalf("Updater.Download returned archive instead of binary: %q", extracted)
	}
	got, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(got) != string(binary) {
		t.Fatalf("verified extracted content = %q, want %q", got, binary)
	}
}

func TestDownloaderRejectsUnsafeInitialURLs(t *testing.T) {
	d := NewDownloader(DefaultConfig(), t.TempDir())
	for _, rawURL := range []string{"file:///etc/passwd", "http://127.0.0.1/internal", "http://localhost/internal"} {
		if _, err := d.Download(context.Background(), rawURL, nil); err == nil || !strings.Contains(err.Error(), "unsafe update URL") {
			t.Errorf("Download(%q) error = %v, want SSRF rejection", rawURL, err)
		}
		if _, err := d.DownloadChecksum(context.Background(), rawURL); err == nil || !strings.Contains(err.Error(), "unsafe update URL") {
			t.Errorf("DownloadChecksum(%q) error = %v, want SSRF rejection", rawURL, err)
		}
	}
}

func TestDownloaderRejectsRedirectToPrivateAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/internal", http.StatusFound)
	}))
	defer server.Close()

	d := NewDownloader(DefaultConfig(), t.TempDir())
	// Permit only the test server as the initial URL; production redirect
	// validation remains installed on the HTTP client itself.
	d.validateURL = allowTestUpdateURL
	_, err := d.Download(context.Background(), server.URL+"/asset", nil)
	if err == nil || !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("redirect error = %v, want SSRF rejection", err)
	}
}

func TestDownloaderRejectsDeclaredOversizeBeforeCreatingStorage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxUpdateDownloadBytes+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tempDir := filepath.Join(t.TempDir(), "tmp")
	d := NewDownloader(DefaultConfig(), tempDir)
	d.validateURL = allowTestUpdateURL
	_, err := d.Download(context.Background(), server.URL+"/asset.zip", nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Download error = %v, want size rejection", err)
	}
	if _, statErr := os.Stat(tempDir); !os.IsNotExist(statErr) {
		t.Fatalf("oversized response created temp storage: %v", statErr)
	}
}

func TestCopyWithByteLimit(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyWithByteLimit(&dst, strings.NewReader("12345678"), 8)
	if err != nil || written != 8 || dst.String() != "12345678" {
		t.Fatalf("exact-limit copy = %d, %q, %v", written, dst.String(), err)
	}
	dst.Reset()
	written, err = copyWithByteLimit(&dst, strings.NewReader("123456789"), 8)
	if err == nil || !strings.Contains(err.Error(), "exceeds") || written != 9 {
		t.Fatalf("oversized copy = %d, %v", written, err)
	}
	if _, err := copyWithByteLimit(io.Discard, strings.NewReader("x"), 0); err == nil {
		t.Fatal("non-positive copy limit was accepted")
	}
}

func TestUpdaterRejectsImplausibleAssetSizeWithoutNetwork(t *testing.T) {
	for _, size := range []int64{-1, maxUpdateDownloadBytes + 1} {
		if err := validateUpdateAssetSize(size); err == nil {
			t.Errorf("asset size %d was accepted", size)
		}
	}
	if err := validateUpdateAssetSize(maxUpdateDownloadBytes); err != nil {
		t.Fatalf("exact maximum asset size: %v", err)
	}
}

func TestExtractTarRejectsDeclaredExpansionBomb(t *testing.T) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "gokin", Mode: 0o755, Size: maxExtractedBinaryBytes + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	// Intentionally do not close tar.Writer: its missing payload error is the
	// fixture. Closing gzip is enough to persist the already-written header.
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip fixture: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "bomb.tar.gz")
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	d := NewDownloader(DefaultConfig(), filepath.Join(t.TempDir(), "tmp"))
	if _, err := d.ExtractBinary(archivePath, "gokin"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ExtractBinary error = %v, want expansion limit", err)
	}
}

func TestDownloaderCleanupOnlyRemovesManagedFiles(t *testing.T) {
	dir := t.TempDir()
	managed := []string{"gokin-update-123.tar.gz", "gokin-bin-456"}
	for _, name := range append(managed, "keep.txt") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	d := NewDownloader(DefaultConfig(), dir)
	if err := d.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	for _, name := range managed {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("managed temp file %q remains: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "keep.txt")); err != nil || string(data) != "x" {
		t.Fatalf("unmanaged file changed: %q, %v", data, err)
	}
}

func TestPrivateUpdateStorageRejectsRootAndWorkingDirectory(t *testing.T) {
	root := string(filepath.Separator)
	if err := ensurePrivateUpdateDir(root, "test"); err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("root directory error = %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := ensurePrivateUpdateDir(cwd, "test"); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("working directory error = %v", err)
	}
}

func TestDownloadArchiveSuffix(t *testing.T) {
	tests := map[string]string{
		"https://example.com/a.TAR.GZ?q=1": ".tar.gz",
		"https://example.com/a.tgz":        ".tgz",
		"https://example.com/a.zip":        ".zip",
		"https://example.com/a.exe":        ".exe",
		"https://example.com/a.tar":        "",
		"not a URL":                        "",
	}
	for rawURL, want := range tests {
		if got := downloadArchiveSuffix(rawURL); got != want {
			t.Errorf("downloadArchiveSuffix(%q) = %q, want %q", rawURL, got, want)
		}
	}
}
