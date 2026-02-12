package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gokin/internal/logging"
)

// Updater is the main orchestrator for the update system.
type Updater struct {
	config     *Config
	checker    *Checker
	downloader *Downloader
	installer  *Installer
	currentVer string
	cacheDir   string
	tempDir    string
	mu         sync.Mutex
	inProgress bool
	lastCheck  time.Time
	cachedInfo *UpdateInfo
}

func (u *Updater) beginOperation() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.inProgress {
		return ErrUpdateInProgress
	}
	u.inProgress = true
	return nil
}

func (u *Updater) endOperation() {
	u.mu.Lock()
	u.inProgress = false
	u.mu.Unlock()
}

// NewUpdater creates a new updater.
func NewUpdater(config *Config, currentVersion string) (*Updater, error) {
	if config == nil {
		config = DefaultConfig()
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Setup directories
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	cacheDir := filepath.Join(homeDir, ".config", "gokin", "update")
	tempDir := filepath.Join(cacheDir, "tmp")
	backupDir := filepath.Join(cacheDir, "backups")

	// Create updater
	u := &Updater{
		config:     config,
		currentVer: currentVersion,
		cacheDir:   cacheDir,
		tempDir:    tempDir,
	}

	// Initialize components
	u.checker = NewChecker(config, cacheDir)
	u.downloader = NewDownloader(config, tempDir)

	installer, err := NewInstaller(config, backupDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create installer: %w", err)
	}
	u.installer = installer

	return u, nil
}

// CheckForUpdate checks if an update is available.
func (u *Updater) CheckForUpdate(ctx context.Context) (*UpdateInfo, error) {
	if !u.config.Enabled {
		return nil, ErrUpdateDisabled
	}

	u.mu.Lock()
	// Try to use cache first
	if u.cachedInfo != nil && time.Since(u.lastCheck) < u.config.CheckInterval {
		info := u.cachedInfo
		u.mu.Unlock()
		return info, nil
	}
	u.mu.Unlock()

	// Fetch latest release without holding lock.
	release, err := u.checker.GetLatestRelease(ctx)
	if err != nil {
		return nil, err
	}

	// Compare versions
	if !IsNewerVersion(release.TagName, u.currentVer) {
		return nil, ErrSameVersion
	}

	// Find asset for current platform
	asset := u.checker.FindAssetForPlatform(release)
	if asset == nil {
		return nil, ErrNoAsset
	}

	// Find checksum asset
	checksumAsset := u.checker.FindChecksumAsset(release, asset)

	info := &UpdateInfo{
		CurrentVersion: u.currentVer,
		NewVersion:     release.TagName,
		ReleaseNotes:   release.Body,
		ReleaseURL:     release.HTMLURL,
		AssetURL:       asset.DownloadURL(),
		AssetName:      asset.Name,
		AssetSize:      asset.Size,
		PublishedAt:    release.PublishedAt,
	}

	if checksumAsset != nil {
		info.ChecksumURL = checksumAsset.DownloadURL()
	}

	// Cache the result
	u.mu.Lock()
	u.cachedInfo = info
	u.lastCheck = time.Now()
	u.mu.Unlock()

	// Save to persistent cache
	u.saveCache()

	return info, nil
}

// CheckForUpdateIfDue checks for updates only if enough time has passed.
func (u *Updater) CheckForUpdateIfDue(ctx context.Context) (*UpdateInfo, error) {
	cache, err := u.checker.LoadCache()
	if err == nil && u.checker.IsCacheValid(cache) {
		// Use cached info
		if cache.UpdateAvailable {
			return &UpdateInfo{
				CurrentVersion: u.currentVer,
				NewVersion:     cache.LatestVersion,
				ReleaseNotes:   cache.ReleaseNotes,
				ReleaseURL:     cache.ReleaseURL,
				AssetURL:       cache.AssetURL,
				AssetName:      cache.AssetName,
				PublishedAt:    cache.PublishedAt,
			}, nil
		}
		return nil, ErrNoUpdate
	}

	return u.CheckForUpdate(ctx)
}

// Download downloads the update.
func (u *Updater) Download(ctx context.Context, info *UpdateInfo, progress ProgressCallback) (string, error) {
	if info == nil {
		return "", fmt.Errorf("no update info provided")
	}

	if err := u.beginOperation(); err != nil {
		return "", err
	}
	defer u.endOperation()

	// Download the asset
	downloadedPath, err := u.downloader.Download(ctx, info.AssetURL, progress)
	if err != nil {
		return "", err
	}

	// Verify checksum
	if info.ChecksumURL != "" {
		// Checksum URL available - always verify regardless of config
		if progress != nil {
			progress(&UpdateProgress{
				Status:  StatusVerifying,
				Message: "Verifying checksum...",
			})
		}

		checksums, err := u.downloader.DownloadChecksum(ctx, info.ChecksumURL)
		if err != nil {
			os.Remove(downloadedPath)
			return "", fmt.Errorf("failed to download checksum: %w", err)
		}

		expectedChecksum, ok := checksums[info.AssetName]
		if !ok {
			// Try with different name patterns
			for name, sum := range checksums {
				if filepath.Base(name) == info.AssetName {
					expectedChecksum = sum
					ok = true
					break
				}
			}
		}

		if ok {
			if err := u.downloader.VerifyChecksum(downloadedPath, expectedChecksum); err != nil {
				os.Remove(downloadedPath)
				return "", err
			}
		}
	} else if u.config.VerifyChecksum {
		// Checksum required by config but not available from release
		os.Remove(downloadedPath)
		return "", fmt.Errorf("checksum verification required but no checksum URL available for this release")
	} else {
		logging.Warn("no checksum available for update, skipping verification")
	}

	// Extract binary if needed
	binaryPath, err := u.downloader.ExtractBinary(downloadedPath, "gokin")
	if err != nil {
		os.Remove(downloadedPath)
		return "", fmt.Errorf("failed to extract binary: %w", err)
	}

	// Clean up archive if different from binary
	if binaryPath != downloadedPath {
		os.Remove(downloadedPath)
	}

	return binaryPath, nil
}

// Install installs the downloaded update.
func (u *Updater) Install(ctx context.Context, binaryPath string, version string, progress ProgressCallback) error {
	u.installer.SetProgressCallback(progress)
	return u.installer.Install(ctx, binaryPath, version)
}

// Update performs a full update: check, download, install.
func (u *Updater) Update(ctx context.Context, progress ProgressCallback) (*UpdateInfo, error) {
	if err := u.beginOperation(); err != nil {
		return nil, err
	}
	defer u.endOperation()

	// Check for update
	if progress != nil {
		progress(&UpdateProgress{
			Status:  StatusChecking,
			Message: "Checking for updates...",
		})
	}

	info, err := u.CheckForUpdate(ctx)
	if err != nil {
		return nil, err
	}

	// Download (without nested operation guard)
	binaryPath, err := u.downloadWithChecksum(ctx, info, progress)
	if err != nil {
		return nil, err
	}
	defer os.Remove(binaryPath)

	// Install
	if err := u.Install(ctx, binaryPath, info.NewVersion, progress); err != nil {
		return nil, err
	}

	return info, nil
}

// downloadWithChecksum performs download path without operation locking.
// Used by Update() which already holds operation lock.
func (u *Updater) downloadWithChecksum(ctx context.Context, info *UpdateInfo, progress ProgressCallback) (string, error) {
	// Download the asset
	downloadedPath, err := u.downloader.Download(ctx, info.AssetURL, progress)
	if err != nil {
		return "", err
	}

	// Verify checksum
	if info.ChecksumURL != "" {
		if progress != nil {
			progress(&UpdateProgress{
				Status:  StatusVerifying,
				Message: "Verifying checksum...",
			})
		}

		checksums, err := u.downloader.DownloadChecksum(ctx, info.ChecksumURL)
		if err != nil {
			os.Remove(downloadedPath)
			return "", fmt.Errorf("failed to download checksum: %w", err)
		}

		expectedChecksum, ok := checksums[info.AssetName]
		if !ok {
			for name, sum := range checksums {
				if filepath.Base(name) == info.AssetName {
					expectedChecksum = sum
					ok = true
					break
				}
			}
		}
		if ok {
			if err := u.downloader.VerifyChecksum(downloadedPath, expectedChecksum); err != nil {
				os.Remove(downloadedPath)
				return "", err
			}
		}
	} else if u.config.VerifyChecksum {
		os.Remove(downloadedPath)
		return "", fmt.Errorf("checksum verification required but no checksum URL available for this release")
	}

	binaryPath, err := u.downloader.ExtractBinary(downloadedPath, "gokin")
	if err != nil {
		os.Remove(downloadedPath)
		return "", fmt.Errorf("failed to extract binary: %w", err)
	}
	if binaryPath != downloadedPath {
		os.Remove(downloadedPath)
	}
	return binaryPath, nil
}

// Rollback rolls back to the previous version.
func (u *Updater) Rollback() error {
	return u.installer.GetRollbackManager().RollbackToLatest()
}

// RollbackTo rolls back to a specific backup.
func (u *Updater) RollbackTo(backupID string) error {
	return u.installer.GetRollbackManager().Rollback(backupID)
}

// ListBackups returns available backups.
func (u *Updater) ListBackups() ([]*BackupInfo, error) {
	return u.installer.GetRollbackManager().ListBackups()
}

// Cleanup cleans up temporary files.
func (u *Updater) Cleanup() error {
	return u.downloader.Cleanup()
}

// GetConfig returns the current configuration.
func (u *Updater) GetConfig() *Config {
	return u.config
}

// GetCurrentVersion returns the current version.
func (u *Updater) GetCurrentVersion() string {
	return u.currentVer
}

// saveCache saves update information to persistent cache.
func (u *Updater) saveCache() {
	if u.cachedInfo == nil {
		return
	}

	cache := &UpdateCache{
		LastCheck:       u.lastCheck,
		LatestVersion:   u.cachedInfo.NewVersion,
		UpdateAvailable: true,
		ReleaseNotes:    u.cachedInfo.ReleaseNotes,
		ReleaseURL:      u.cachedInfo.ReleaseURL,
		AssetURL:        u.cachedInfo.AssetURL,
		AssetName:       u.cachedInfo.AssetName,
		PublishedAt:     u.cachedInfo.PublishedAt,
	}

	u.checker.SaveCache(cache)
}

// ShouldAutoCheck returns true if an automatic check should be performed.
func (u *Updater) ShouldAutoCheck() bool {
	if !u.config.Enabled || !u.config.AutoCheck {
		return false
	}

	cache, err := u.checker.LoadCache()
	if err != nil {
		return true
	}

	return !u.checker.IsCacheValid(cache)
}
