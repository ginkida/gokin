package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/security"
)

const (
	maxChecksumFileBytes    int64 = 1 << 20
	maxUpdateDownloadBytes  int64 = 1 << 30
	maxExtractedBinaryBytes int64 = 512 << 20
)

// Downloader handles downloading update files.
type Downloader struct {
	httpClient  *http.Client
	config      *Config
	tempDir     string
	validateURL func(string) error
}

// NewDownloader creates a new downloader.
func NewDownloader(config *Config, tempDir string) *Downloader {
	tlsConfig := security.DefaultTLSConfig()
	httpClient, err := security.CreateSecureHTTPClient(tlsConfig, 10*time.Minute)
	if err != nil {
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	httpClient = security.WithSSRFRedirectProtection(httpClient)
	return &Downloader{
		httpClient:  httpClient,
		config:      config,
		tempDir:     tempDir,
		validateURL: validateUpdateDownloadURL,
	}
}

// Download downloads a file from the given URL with progress reporting.
// Returns the path to the downloaded file.
func (d *Downloader) Download(ctx context.Context, url string, progress ProgressCallback) (string, error) {
	if err := d.validateDownloadURL(url); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}
	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "gokin-updater/1.0")
	req.Header.Set("Accept", "application/octet-stream")

	addGitHubTokenHeader(req)

	// Send request
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d", ErrDownloadFailed, resp.StatusCode)
	}
	if resp.ContentLength > maxUpdateDownloadBytes {
		return "", fmt.Errorf("%w: response exceeds %d-byte limit", ErrDownloadFailed, maxUpdateDownloadBytes)
	}

	// Create temp directory if needed
	if err := d.ensureTempDir(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}

	// Preserve only known archive suffixes from the URL. Without this, a
	// downloaded .tar.gz received a random extensionless name and was later
	// mistaken for an already-extracted executable.
	tmpFile, err := os.CreateTemp(d.tempDir, "gokin-update-*"+downloadArchiveSuffix(url))
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = tmpFile.Close()
		}
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()

	// Get total size for progress
	totalSize := resp.ContentLength

	// Create progress writer
	pw := &progressWriter{
		writer:   tmpFile,
		total:    totalSize,
		callback: progress,
	}

	// Download with progress
	written, err := copyWithByteLimit(pw, resp.Body, maxUpdateDownloadBytes)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDownloadFailed, err)
	}
	if totalSize > 0 && written != totalSize {
		return "", fmt.Errorf("%w: incomplete download (%d/%d bytes)", ErrDownloadFailed, written, totalSize)
	}

	// Explicit close before returning success: a deferred-flush error on a
	// near-full filesystem would otherwise silently produce a truncated
	// binary that passes the rest of the install path (the deferred
	// `tmpFile.Close()` swallows the error). Caught early here means a
	// clean error message and a removed temp file instead of a corrupt
	// installed binary.
	if err := tmpFile.Close(); err != nil {
		closed = true
		return "", fmt.Errorf("%w: close: %w", ErrDownloadFailed, err)
	}
	closed = true
	committed = true

	return tmpPath, nil
}

// DownloadChecksum downloads and parses a checksum file.
func (d *Downloader) DownloadChecksum(ctx context.Context, url string) (map[string]string, error) {
	if err := d.validateDownloadURL(url); err != nil {
		return nil, fmt.Errorf("failed to download checksum: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "gokin-updater/1.0")
	addGitHubTokenHeader(req)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download checksum: HTTP %d", resp.StatusCode)
	}

	// Reject rather than silently truncate an oversized checksum document: a
	// valid-looking entry in the first MiB must not make ignored trailing data
	// disappear from the integrity decision.
	data, err := readBoundedResponseBody(resp.Body, resp.ContentLength, maxChecksumFileBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read checksum file: %w", err)
	}

	return d.parseChecksumFile(string(data)), nil
}

func (d *Downloader) validateDownloadURL(rawURL string) error {
	validator := d.validateURL
	if validator == nil {
		validator = validateUpdateDownloadURL
	}
	return validator(rawURL)
}

func validateUpdateDownloadURL(rawURL string) error {
	result := security.ValidateURLForSSRF(rawURL)
	if !result.Valid {
		return fmt.Errorf("unsafe update URL: %s", result.Reason)
	}
	return nil
}

func (d *Downloader) ensureTempDir() error {
	return ensurePrivateUpdateDir(d.tempDir, "update temp")
}

func downloadArchiveSuffix(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	lowerPath := strings.ToLower(parsed.Path)
	for _, suffix := range []string{".tar.gz", ".tgz", ".zip", ".exe"} {
		if strings.HasSuffix(lowerPath, suffix) {
			return suffix
		}
	}
	return ""
}

func copyWithByteLimit(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	if maxBytes <= 0 {
		return 0, fmt.Errorf("copy limit must be positive")
	}
	written, err := io.Copy(dst, io.LimitReader(src, maxBytes+1))
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, fmt.Errorf("content exceeds %d-byte limit", maxBytes)
	}
	return written, nil
}

// parseChecksumFile parses a checksum file in common formats.
// Supports: "checksum  filename" and "checksum filename" formats.
func (d *Downloader) parseChecksumFile(content string) map[string]string {
	checksums := make(map[string]string)

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Try "checksum  filename" format (sha256sum output)
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			checksum := parts[0]
			filename := parts[len(parts)-1]
			// Remove leading * from binary mode indicator
			filename = strings.TrimPrefix(filename, "*")
			checksums[filename] = strings.ToLower(checksum)
		}
	}

	return checksums
}

// VerifyChecksum verifies the checksum of a file.
func (d *Downloader) VerifyChecksum(filePath, expectedChecksum string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actualChecksum := hex.EncodeToString(h.Sum(nil))

	if !strings.EqualFold(actualChecksum, expectedChecksum) {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expectedChecksum, actualChecksum)
	}

	return nil
}

// ComputeChecksum computes the SHA256 checksum of a file.
func (d *Downloader) ComputeChecksum(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExtractBinary extracts the binary from an archive.
// Supports: .tar.gz, .tgz, .zip, and raw binaries.
func (d *Downloader) ExtractBinary(archivePath, binaryName string) (string, error) {
	if binaryName == "" || path.Base(binaryName) != binaryName || binaryName == "." {
		return "", fmt.Errorf("invalid binary name %q", binaryName)
	}
	ext := strings.ToLower(filepath.Ext(archivePath))

	// Check for .tar.gz
	if strings.HasSuffix(strings.ToLower(archivePath), ".tar.gz") || ext == ".tgz" {
		return d.extractTarGz(archivePath, binaryName)
	}

	if ext == ".zip" {
		return d.extractZip(archivePath, binaryName)
	}

	// Assume it's a raw binary, but still reject symlinks/special files and
	// implausibly large artifacts supplied through the public API.
	file, err := fileutil.OpenRegularRead(archivePath)
	if err != nil {
		return "", fmt.Errorf("open raw update binary: %w", err)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return "", fmt.Errorf("stat raw update binary: %w", statErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close raw update binary: %w", closeErr)
	}
	if info.Size() < 0 || info.Size() > maxExtractedBinaryBytes {
		return "", fmt.Errorf("raw update binary exceeds %d-byte limit", maxExtractedBinaryBytes)
	}
	return archivePath, nil
}

// extractTarGz extracts a binary from a tar.gz archive.
func (d *Downloader) extractTarGz(archivePath, binaryName string) (string, error) {
	if err := d.ensureTempDir(); err != nil {
		return "", err
	}

	f, err := fileutil.OpenRegularRead(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	archiveInfo, err := f.Stat()
	if err != nil {
		return "", err
	}
	if archiveInfo.Size() < 0 || archiveInfo.Size() > maxUpdateDownloadBytes {
		return "", fmt.Errorf("update archive exceeds %d-byte limit", maxUpdateDownloadBytes)
	}

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		// Skip symlinks and hard links to prevent path traversal attacks
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			continue
		}

		// Archive paths always use slash separators, regardless of host OS.
		baseName := path.Base(strings.ReplaceAll(header.Name, "\\", "/"))
		if baseName == binaryName || baseName == binaryName+".exe" {
			if header.FileInfo().Mode().IsRegular() {
				if header.Size < 0 || header.Size > maxExtractedBinaryBytes {
					return "", fmt.Errorf("extracted binary exceeds %d-byte limit", maxExtractedBinaryBytes)
				}
				return d.writeExtractedBinary(tr, header.Size)
			}
		}
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

// extractZip extracts a binary from a zip archive.
func (d *Downloader) extractZip(archivePath, binaryName string) (string, error) {
	if err := d.ensureTempDir(); err != nil {
		return "", err
	}

	archiveFile, err := fileutil.OpenRegularRead(archivePath)
	if err != nil {
		return "", err
	}
	defer archiveFile.Close()
	archiveInfo, err := archiveFile.Stat()
	if err != nil {
		return "", err
	}
	if archiveInfo.Size() < 0 || archiveInfo.Size() > maxUpdateDownloadBytes {
		return "", fmt.Errorf("update archive exceeds %d-byte limit", maxUpdateDownloadBytes)
	}
	r, err := zip.NewReader(archiveFile, archiveInfo.Size())
	if err != nil {
		return "", err
	}

	for _, f := range r.File {
		// Skip symlinks to prevent path traversal attacks
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			continue
		}

		baseName := path.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		if baseName == binaryName || baseName == binaryName+".exe" {
			if f.FileInfo().Mode().IsRegular() {
				if f.UncompressedSize64 > uint64(maxExtractedBinaryBytes) {
					return "", fmt.Errorf("extracted binary exceeds %d-byte limit", maxExtractedBinaryBytes)
				}
				rc, err := f.Open()
				if err != nil {
					return "", err
				}
				outPath, extractErr := d.writeExtractedBinary(rc, int64(f.UncompressedSize64))
				closeErr := rc.Close()
				if extractErr != nil {
					return "", extractErr
				}
				if closeErr != nil {
					_ = os.Remove(outPath)
					return "", fmt.Errorf("close zip entry: %w", closeErr)
				}
				return outPath, nil
			}
		}
	}

	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func (d *Downloader) writeExtractedBinary(reader io.Reader, expectedSize int64) (string, error) {
	if expectedSize < 0 || expectedSize > maxExtractedBinaryBytes {
		return "", fmt.Errorf("extracted binary exceeds %d-byte limit", maxExtractedBinaryBytes)
	}
	outFile, err := os.CreateTemp(d.tempDir, "gokin-bin-*")
	if err != nil {
		return "", err
	}
	outPath := outFile.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = outFile.Close()
		}
		if !committed {
			_ = os.Remove(outPath)
		}
	}()

	written, err := copyWithByteLimit(outFile, reader, maxExtractedBinaryBytes)
	if err != nil {
		return "", err
	}
	if written != expectedSize {
		return "", fmt.Errorf("extracted binary size mismatch: expected %d, got %d", expectedSize, written)
	}
	// Release archives do not get to select setuid/sticky or non-executable
	// modes. The installer only needs an owner-readable executable artifact.
	if err := outFile.Chmod(0o755); err != nil {
		return "", err
	}
	if err := outFile.Sync(); err != nil {
		return "", fmt.Errorf("sync extracted binary: %w", err)
	}
	if err := outFile.Close(); err != nil {
		closed = true
		return "", fmt.Errorf("close extracted binary: %w", err)
	}
	closed = true
	committed = true
	return outPath, nil
}

// Cleanup removes temporary files.
func (d *Downloader) Cleanup() error {
	if d.tempDir == "" {
		return nil
	}
	info, err := os.Lstat(d.tempDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("update temp path is not a real directory")
	}
	if err := d.ensureTempDir(); err != nil {
		return err
	}
	entries, err := os.ReadDir(d.tempDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isManagedUpdateTempName(entry.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(d.tempDir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove update temp file: %w", err)
		}
	}
	return nil
}

func isManagedUpdateTempName(name string) bool {
	return strings.HasPrefix(name, "gokin-update-") || strings.HasPrefix(name, "gokin-bin-")
}

// progressWriter wraps an io.Writer to report progress.
type progressWriter struct {
	writer   io.Writer
	total    int64
	written  int64
	callback ProgressCallback
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.writer.Write(p)
	pw.written += int64(n)

	if pw.callback != nil {
		var percent float64
		if pw.total > 0 {
			percent = float64(pw.written) / float64(pw.total) * 100
		}

		pw.callback(&UpdateProgress{
			Status:          StatusDownloading,
			BytesDownloaded: pw.written,
			TotalBytes:      pw.total,
			Percent:         percent,
		})
	}

	return n, err
}
