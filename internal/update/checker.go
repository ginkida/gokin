package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/security"
)

const (
	maxReleaseResponseBytes int64 = 4 << 20
	maxUpdateCacheBytes     int64 = 2 << 20
)

// Checker handles version checking against GitHub releases.
type Checker struct {
	httpClient *http.Client
	repo       string
	cacheDir   string
	config     *Config
	// baseURL is the GitHub API root. It is a field (not a const) so tests
	// can point the checker at an httptest server and exercise the real
	// GetLatestRelease/GetReleases/GetReleaseByTag wrappers instead of
	// reimplementing their logic inline.
	baseURL string
}

// NewChecker creates a new version checker.
func NewChecker(config *Config, cacheDir string) *Checker {
	if config == nil {
		config = DefaultConfig()
	}
	tlsConfig := security.DefaultTLSConfig()
	secureClient, err := security.CreateSecureHTTPClient(tlsConfig, config.Timeout)
	if err != nil {
		// Fall back to basic client if secure creation fails
		secureClient = &http.Client{Timeout: config.Timeout}
	}

	// Set proxy if configured
	if config.Proxy != "" {
		proxyURL, err := url.Parse(config.Proxy)
		if err == nil {
			if transport, ok := secureClient.Transport.(*http.Transport); ok {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
		}
	}

	return &Checker{
		httpClient: secureClient,
		repo:       config.GitHubRepo,
		cacheDir:   cacheDir,
		config:     config,
		baseURL:    "https://api.github.com",
	}
}

// GetLatestRelease fetches the latest release from GitHub.
func (c *Checker) GetLatestRelease(ctx context.Context) (*ReleaseInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.baseURL, c.repo)

	release, err := c.fetchRelease(ctx, url)
	if err != nil {
		return nil, err
	}

	// If prereleases not included and this is a prerelease, fetch all and find latest stable
	if !c.config.IncludePrerelease && release.Prerelease {
		return c.getLatestStableRelease(ctx)
	}

	return release, nil
}

// getLatestStableRelease finds the latest stable (non-prerelease) release.
func (c *Checker) getLatestStableRelease(ctx context.Context) (*ReleaseInfo, error) {
	releases, err := c.GetReleases(ctx, 20)
	if err != nil {
		return nil, err
	}

	for _, release := range releases {
		if !release.Prerelease && !release.Draft && c.config.MatchesChannel(&release) {
			r := release // Copy to avoid reference issues
			return &r, nil
		}
	}

	return nil, ErrNoReleases
}

// GetReleases fetches multiple releases from GitHub.
func (c *Checker) GetReleases(ctx context.Context, limit int) ([]ReleaseInfo, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("release list limit must be between 1 and 100")
	}
	url := fmt.Sprintf("%s/repos/%s/releases?per_page=%d", c.baseURL, c.repo, limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNetworkError, err)
	}
	defer resp.Body.Close()

	if err := c.checkResponse(resp); err != nil {
		return nil, err
	}

	var releases []ReleaseInfo
	if err := decodeBoundedJSON(resp.Body, resp.ContentLength, maxReleaseResponseBytes, &releases); err != nil {
		return nil, fmt.Errorf("failed to parse releases: %w", err)
	}

	return releases, nil
}

// GetReleaseByTag fetches a specific release by tag.
func (c *Checker) GetReleaseByTag(ctx context.Context, tag string) (*ReleaseInfo, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", c.baseURL, c.repo, tag)
	return c.fetchRelease(ctx, url)
}

// fetchRelease performs the HTTP request to fetch a release.
func (c *Checker) fetchRelease(ctx context.Context, url string) (*ReleaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNetworkError, err)
	}
	defer resp.Body.Close()

	if err := c.checkResponse(resp); err != nil {
		return nil, err
	}

	var release ReleaseInfo
	if err := decodeBoundedJSON(resp.Body, resp.ContentLength, maxReleaseResponseBytes, &release); err != nil {
		return nil, fmt.Errorf("failed to parse release: %w", err)
	}

	return &release, nil
}

// setHeaders sets common headers for GitHub API requests.
func (c *Checker) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "gokin-updater/1.0")

	// Add GitHub credentials only to trusted GitHub HTTPS endpoints. The same
	// helper is used by asset/checksum downloads so release metadata or a
	// tampered cache cannot exfiltrate GITHUB_TOKEN to an arbitrary host.
	addGitHubTokenHeader(req)
}

// checkResponse checks the HTTP response for errors.
func (c *Checker) checkResponse(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNoReleases
	case http.StatusForbidden:
		// Check if rate limited
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return ErrRateLimited
		}
		return ErrPermissionDenied
	case http.StatusUnauthorized:
		return ErrPermissionDenied
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub API error: %s - %s", resp.Status, string(body))
	}
}

// FindAssetForPlatform finds the appropriate asset for the current platform.
func (c *Checker) FindAssetForPlatform(release *ReleaseInfo) *Asset {
	if release == nil || len(release.Assets) == 0 {
		return nil
	}

	platform := Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	pattern := platform.AssetPattern()

	// Try exact match first
	for i := range release.Assets {
		asset := &release.Assets[i]
		if strings.EqualFold(stripArchiveExtensions(asset.Name), pattern) {
			return asset
		}
	}

	// Try alternative patterns
	alternatives := c.getAlternativePatterns(platform)
	for _, alt := range alternatives {
		for i := range release.Assets {
			asset := &release.Assets[i]
			if strings.EqualFold(stripArchiveExtensions(asset.Name), alt) {
				return asset
			}
		}
	}

	return nil
}

// getAlternativePatterns returns alternative asset name patterns.
func (c *Checker) getAlternativePatterns(platform Platform) []string {
	var patterns []string

	// Common alternative naming conventions
	// gokin-linux-amd64, gokin_linux_amd64, gokin-linux-x86_64
	patterns = append(patterns, fmt.Sprintf("gokin-%s-%s", platform.OS, platform.Arch))

	// Map amd64 to x86_64
	if platform.Arch == "amd64" {
		patterns = append(patterns, fmt.Sprintf("gokin_%s_x86_64", platform.OS))
		patterns = append(patterns, fmt.Sprintf("gokin-%s-x86_64", platform.OS))
	}

	// Map arm64 to aarch64
	if platform.Arch == "arm64" {
		patterns = append(patterns, fmt.Sprintf("gokin_%s_aarch64", platform.OS))
		patterns = append(patterns, fmt.Sprintf("gokin-%s-aarch64", platform.OS))
	}

	// Darwin -> macos/macOS
	if platform.OS == "darwin" {
		patterns = append(patterns, fmt.Sprintf("gokin_macos_%s", platform.Arch))
		patterns = append(patterns, fmt.Sprintf("gokin-macos-%s", platform.Arch))
		patterns = append(patterns, fmt.Sprintf("gokin_macOS_%s", platform.Arch))
	}

	return patterns
}

// FindChecksumAsset finds the checksum file for the given asset.
func (c *Checker) FindChecksumAsset(release *ReleaseInfo, asset *Asset) *Asset {
	if release == nil || asset == nil {
		return nil
	}

	// Common checksum file patterns
	checksumPatterns := []string{
		asset.Name + ".sha256",
		asset.Name + ".sha256sum",
		"checksums.txt",
		"SHA256SUMS",
		"sha256sums.txt",
	}

	for _, pattern := range checksumPatterns {
		for i := range release.Assets {
			a := &release.Assets[i]
			if strings.EqualFold(a.Name, pattern) {
				return a
			}
		}
	}

	return nil
}

// LoadCache loads cached update information.
func (c *Checker) LoadCache() (*UpdateCache, error) {
	if err := c.ensureCacheDir(); err != nil {
		return nil, err
	}
	cachePath := c.getCachePath()
	data, err := fileutil.ReadPrivateFile(cachePath, maxUpdateCacheBytes)
	if err != nil {
		return nil, fmt.Errorf("read update cache: %w", err)
	}

	var cache UpdateCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("decode update cache: %w", err)
	}

	return &cache, nil
}

// SaveCache saves update information to cache.
func (c *Checker) SaveCache(cache *UpdateCache) error {
	if cache == nil {
		return fmt.Errorf("update cache is nil")
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update cache: %w", err)
	}
	if int64(len(data)) > maxUpdateCacheBytes {
		return fmt.Errorf("update cache exceeds %d-byte limit", maxUpdateCacheBytes)
	}
	if err := c.ensureCacheDir(); err != nil {
		return err
	}

	cachePath := c.getCachePath()
	if err := fileutil.SecurePrivateFile(cachePath); err != nil {
		return fmt.Errorf("secure update cache: %w", err)
	}
	if err := fileutil.AtomicWrite(cachePath, data, 0o600); err != nil {
		return fmt.Errorf("write update cache: %w", err)
	}
	return nil
}

func (c *Checker) ensureCacheDir() error {
	return ensurePrivateUpdateDir(c.cacheDir, "update cache")
}

// getCachePath returns the path to the cache file.
func (c *Checker) getCachePath() string {
	return filepath.Join(c.cacheDir, "update_cache.json")
}

// stripArchiveExtensions removes known archive extensions from a filename.
func stripArchiveExtensions(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range []string{".tar.gz", ".tgz", ".zip", ".exe"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

// IsCacheValid returns true if cached data is still valid.
func (c *Checker) IsCacheValid(cache *UpdateCache) bool {
	if cache == nil || cache.LastCheck.IsZero() {
		return false
	}
	age := time.Since(cache.LastCheck)
	return age >= 0 && age < c.config.CheckInterval
}

func decodeBoundedJSON(body io.Reader, contentLength, maxBytes int64, dst any) error {
	data, err := readBoundedResponseBody(body, contentLength, maxBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	return nil
}

func readBoundedResponseBody(body io.Reader, contentLength, maxBytes int64) ([]byte, error) {
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("response body limit must be positive")
	}
	if contentLength > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d-byte limit", maxBytes)
	}

	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d-byte limit", maxBytes)
	}
	return data, nil
}
