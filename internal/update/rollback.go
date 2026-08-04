package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/logging"
)

const maxBackupInfoBytes int64 = 64 << 10

// RollbackManager manages backups and rollbacks.
type RollbackManager struct {
	backupDir  string
	maxBackups int
}

// NewRollbackManager creates a new rollback manager.
func NewRollbackManager(backupDir string, maxBackups int) *RollbackManager {
	if maxBackups < 1 {
		maxBackups = 3
	}
	return &RollbackManager{
		backupDir:  backupDir,
		maxBackups: maxBackups,
	}
}

// CreateBackup creates a backup of the current binary.
func (rm *RollbackManager) CreateBackup(binaryPath string, version string) (*BackupInfo, error) {
	absBinaryPath, err := filepath.Abs(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("resolve binary path: %w", err)
	}
	binaryPath = filepath.Clean(absBinaryPath)

	// Ensure backup directory exists
	if err := rm.ensureBackupDir(); err != nil {
		return nil, fmt.Errorf("failed to create backup directory: %w", err)
	}

	// Preserve the human-readable timestamp IDs, but create the destination
	// exclusively. The version originates in remote release metadata and must
	// never become a path component verbatim.
	baseID := time.Now().Format("20060102-150405")
	versionComponent := safeBackupVersion(version)
	var backupID, backupPath string
	for attempt := 1; attempt <= 100; attempt++ {
		backupID = baseID
		if attempt > 1 {
			backupID = fmt.Sprintf("%s-%d", baseID, attempt)
		}
		candidate := filepath.Join(rm.backupDir, fmt.Sprintf("gokin-%s-%s", versionComponent, backupID))
		if err := copyBackupFileExclusive(binaryPath, candidate); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return nil, fmt.Errorf("failed to copy binary: %w", err)
		}
		backupPath = candidate
		break
	}
	if backupPath == "" {
		return nil, fmt.Errorf("failed to allocate a unique backup name after 100 attempts")
	}

	stat, checksum, err := inspectBackupFile(backupPath)
	if err != nil {
		_ = os.Remove(backupPath)
		return nil, fmt.Errorf("failed to inspect backup: %w", err)
	}

	// Create backup info
	info := &BackupInfo{
		ID:         backupID,
		Version:    version,
		Path:       backupPath,
		CreatedAt:  time.Now(),
		BinaryPath: binaryPath,
		Size:       stat.Size(),
		Checksum:   checksum,
	}

	// Save backup info
	if err := rm.SaveBackupInfo(info); err != nil {
		// Without its original binary path and integrity record this backup is
		// not a reliable rollback point. Do not report success for an artifact
		// that may later restore the wrong executable.
		_ = os.Remove(backupPath)
		_ = os.Remove(backupPath + ".json")
		return nil, fmt.Errorf("failed to save backup info: %w", err)
	}

	return info, nil
}

// SaveBackupInfo saves backup information to a JSON file.
func (rm *RollbackManager) SaveBackupInfo(info *BackupInfo) error {
	if info == nil {
		return fmt.Errorf("backup info is nil")
	}
	backupPath, err := rm.validateBackupPath(info.Path)
	if err != nil {
		return err
	}
	if err := rm.ensureBackupDir(); err != nil {
		return err
	}
	info.Path = backupPath
	if info.ID == "" || !isSafeBackupID(info.ID) {
		return fmt.Errorf("backup info has invalid id %q", info.ID)
	}
	if info.BinaryPath != "" && !filepath.IsAbs(info.BinaryPath) {
		return fmt.Errorf("backup target must be an absolute path")
	}
	stat, checksum, err := inspectBackupFile(backupPath)
	if err != nil {
		return err
	}
	if info.Size != 0 && info.Size != stat.Size() {
		return fmt.Errorf("backup size does not match metadata")
	}
	if info.Checksum != "" && !strings.EqualFold(info.Checksum, checksum) {
		return fmt.Errorf("backup checksum does not match metadata")
	}
	info.Size = stat.Size()
	info.Checksum = checksum
	infoPath := info.Path + ".json"
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup info: %w", err)
	}
	if int64(len(data)) > maxBackupInfoBytes {
		return fmt.Errorf("backup info exceeds %d-byte limit", maxBackupInfoBytes)
	}
	if err := fileutil.SecurePrivateFile(infoPath); err != nil {
		return fmt.Errorf("secure backup info: %w", err)
	}
	if err := fileutil.AtomicWrite(infoPath, data, 0o600); err != nil {
		return fmt.Errorf("write backup info: %w", err)
	}
	return nil
}

// LoadBackupInfo loads backup information from a JSON file.
func (rm *RollbackManager) LoadBackupInfo(backupPath string) (*BackupInfo, error) {
	if err := rm.ensureBackupDir(); err != nil {
		return nil, err
	}
	backupPath, err := rm.validateBackupPath(backupPath)
	if err != nil {
		return nil, err
	}
	infoPath := backupPath + ".json"
	data, err := fileutil.ReadPrivateFile(infoPath, maxBackupInfoBytes)
	if err != nil {
		// If no info file, create basic info from path
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read backup info: %w", err)
		}
		stat, checksum, statErr := inspectBackupFile(backupPath)
		if statErr != nil {
			return nil, statErr
		}
		return &BackupInfo{
			ID:        filepath.Base(backupPath),
			Path:      backupPath,
			CreatedAt: stat.ModTime(),
			Size:      stat.Size(),
			Checksum:  checksum,
		}, nil
	}

	var info BackupInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decode backup info: %w", err)
	}
	metadataPath, err := rm.validateBackupPath(info.Path)
	if err != nil || metadataPath != backupPath {
		return nil, fmt.Errorf("backup metadata path does not match %q", backupPath)
	}
	if info.ID == "" || !isSafeBackupID(info.ID) {
		return nil, fmt.Errorf("backup metadata has invalid id %q", info.ID)
	}
	if info.BinaryPath != "" && !filepath.IsAbs(info.BinaryPath) {
		return nil, fmt.Errorf("backup metadata target must be an absolute path")
	}
	stat, checksum, err := inspectBackupFile(backupPath)
	if err != nil {
		return nil, err
	}
	if info.Size == 0 {
		info.Size = stat.Size()
	}
	if info.Checksum == "" {
		info.Checksum = checksum
	}
	info.Path = backupPath
	return &info, nil
}

// ListBackups returns a list of available backups.
func (rm *RollbackManager) ListBackups() ([]*BackupInfo, error) {
	if _, err := os.Lstat(rm.backupDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if err := rm.ensureBackupDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(rm.backupDir)
	if err != nil {
		return nil, err
	}

	var backups []*BackupInfo
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		// Skip .json files
		if filepath.Ext(entry.Name()) == ".json" || !isManagedBackupName(entry.Name()) {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			continue
		}

		backupPath := filepath.Join(rm.backupDir, entry.Name())
		info, err := rm.LoadBackupInfo(backupPath)
		if err != nil {
			continue
		}
		backups = append(backups, info)
	}

	// Sort by creation time (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	return backups, nil
}

// GetLatestBackup returns the most recent backup.
func (rm *RollbackManager) GetLatestBackup() (*BackupInfo, error) {
	backups, err := rm.ListBackups()
	if err != nil {
		return nil, err
	}
	if len(backups) == 0 {
		return nil, ErrNoBackup
	}
	return backups[0], nil
}

// Rollback restores a backup by ID.
func (rm *RollbackManager) Rollback(backupID string) error {
	backups, err := rm.ListBackups()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRollbackFailed, err)
	}

	var backup *BackupInfo
	for _, b := range backups {
		if b.ID == backupID {
			backup = b
			break
		}
	}

	if backup == nil {
		return fmt.Errorf("%w: backup %q not found", ErrNoBackup, backupID)
	}

	return rm.RollbackToBackup(backup)
}

// RollbackToLatest restores the most recent backup.
func (rm *RollbackManager) RollbackToLatest() error {
	backup, err := rm.GetLatestBackup()
	if err != nil {
		return err
	}
	return rm.RollbackToBackup(backup)
}

// RollbackToBackup restores a specific backup.
func (rm *RollbackManager) RollbackToBackup(backup *BackupInfo) error {
	if backup == nil {
		return ErrNoBackup
	}
	if err := rm.ensureBackupDir(); err != nil {
		return fmt.Errorf("%w: %w", ErrRollbackFailed, err)
	}

	backupPath, err := rm.validateBackupPath(backup.Path)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRollbackFailed, err)
	}
	backup.Path = backupPath

	// Get target path (original binary location)
	targetPath := backup.BinaryPath
	if targetPath == "" {
		// Try to determine from current executable
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("%w: cannot determine target path", ErrRollbackFailed)
		}
		resolved, evalErr := filepath.EvalSymlinks(exe)
		if evalErr != nil {
			logging.Warn("failed to resolve symlinks for rollback target", "path", exe, "error", evalErr)
			targetPath = exe
		} else {
			targetPath = resolved
		}
	}
	if !filepath.IsAbs(targetPath) {
		return fmt.Errorf("%w: rollback target must be an absolute path", ErrRollbackFailed)
	}

	// Restore the backup
	if err := rm.restoreBackup(backup, targetPath); err != nil {
		return fmt.Errorf("%w: %w", ErrRollbackFailed, err)
	}

	return nil
}

// restoreBackup copies a backup to the target location.
func (rm *RollbackManager) restoreBackup(backup *BackupInfo, targetPath string) error {
	backupFile, err := fileutil.OpenRegularRead(backup.Path)
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	defer backupFile.Close()

	backupInfo, err := backupFile.Stat()
	if err != nil {
		return fmt.Errorf("stat backup: %w", err)
	}
	if !backupInfo.Mode().IsRegular() {
		return fmt.Errorf("backup is not a regular file")
	}
	if backup.Size > 0 && backupInfo.Size() != backup.Size {
		return fmt.Errorf("backup size mismatch: expected %d, got %d", backup.Size, backupInfo.Size())
	}
	h := sha256.New()
	written, err := io.Copy(h, backupFile)
	if err != nil {
		return fmt.Errorf("verify backup checksum: %w", err)
	}
	if written != backupInfo.Size() {
		return fmt.Errorf("backup changed while verifying")
	}
	actualChecksum := hex.EncodeToString(h.Sum(nil))
	if backup.Checksum == "" || !strings.EqualFold(actualChecksum, backup.Checksum) {
		return fmt.Errorf("backup checksum mismatch")
	}
	afterVerify, err := backupFile.Stat()
	if err != nil || !os.SameFile(backupInfo, afterVerify) ||
		backupInfo.Size() != afterVerify.Size() || !backupInfo.ModTime().Equal(afterVerify.ModTime()) {
		return fmt.Errorf("backup changed while verifying")
	}
	if _, err := backupFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind backup: %w", err)
	}

	// Create temp file in target directory
	dir := filepath.Dir(targetPath)
	tmpFile, err := os.CreateTemp(dir, ".gokin-rollback-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Copy the exact descriptor that was checksummed above, closing the usual
	// verify-then-open race where the path could be swapped between operations.
	copyHash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(tmpFile, copyHash), backupFile)
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to copy backup: %w", err)
	}
	copyChecksum := hex.EncodeToString(copyHash.Sum(nil))
	afterCopy, statErr := backupFile.Stat()
	if copied != backupInfo.Size() || !strings.EqualFold(copyChecksum, backup.Checksum) ||
		statErr != nil || !os.SameFile(backupInfo, afterCopy) ||
		backupInfo.Size() != afterCopy.Size() || !backupInfo.ModTime().Equal(afterCopy.ModTime()) {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("backup changed while copying")
	}

	// Apply mode before syncing so both bytes and executable permissions are
	// durable before the temporary file becomes the live binary.
	if err := tmpFile.Chmod(backupInfo.Mode().Perm()); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to set permissions: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	return nil
}

// CleanupOldBackups removes old backups, keeping only maxBackups.
func (rm *RollbackManager) CleanupOldBackups() error {
	backups, err := rm.ListBackups()
	if err != nil {
		return err
	}

	// Keep newest maxBackups, remove the rest
	if len(backups) <= rm.maxBackups {
		return nil
	}

	for _, backup := range backups[rm.maxBackups:] {
		if err := rm.DeleteBackup(backup); err != nil {
			return err
		}
	}

	return nil
}

// DeleteBackup removes a backup.
func (rm *RollbackManager) DeleteBackup(backup *BackupInfo) error {
	if backup == nil {
		return nil
	}
	if err := rm.ensureBackupDir(); err != nil {
		return err
	}

	backupPath, err := rm.validateBackupPath(backup.Path)
	if err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove backup: %w", err)
	}
	if err := os.Remove(backupPath + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove backup info: %w", err)
	}
	return nil
}

// GetBackupDir returns the backup directory path.
func (rm *RollbackManager) GetBackupDir() string {
	return rm.backupDir
}

func (rm *RollbackManager) ensureBackupDir() error {
	return ensurePrivateUpdateDir(rm.backupDir, "backup")
}

func (rm *RollbackManager) validateBackupPath(path string) (string, error) {
	if strings.TrimSpace(rm.backupDir) == "" {
		return "", fmt.Errorf("backup directory is empty")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("backup path is empty")
	}
	dir, err := filepath.Abs(rm.backupDir)
	if err != nil {
		return "", fmt.Errorf("resolve backup directory: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve backup path: %w", err)
	}
	dir = filepath.Clean(dir)
	absolute = filepath.Clean(absolute)
	relative, err := filepath.Rel(dir, absolute)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		strings.ContainsRune(relative, filepath.Separator) {
		return "", fmt.Errorf("backup path %q is outside backup directory", path)
	}
	if !isManagedBackupName(relative) {
		return "", fmt.Errorf("backup path %q is not a managed backup", path)
	}
	return absolute, nil
}

func safeBackupVersion(version string) string {
	version = strings.TrimSpace(version)
	var builder strings.Builder
	for _, r := range version {
		if builder.Len() >= 64 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), ".-_")
	if result == "" {
		return "unknown"
	}
	return result
}

func isManagedBackupName(name string) bool {
	if len(name) <= len("gokin-") || len(name) > 240 || !strings.HasPrefix(name, "gokin-") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func isSafeBackupID(id string) bool {
	if id == "." || id == ".." || len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func copyBackupFileExclusive(src, dst string) error {
	srcFile, err := fileutil.OpenRegularRead(src)
	if err != nil {
		return fmt.Errorf("open source binary: %w", err)
	}
	defer srcFile.Close()
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat source binary: %w", err)
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("source binary is not a regular file")
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, srcInfo.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = dstFile.Close()
		}
		if !committed {
			_ = os.Remove(dst)
		}
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy source binary: %w", err)
	}
	if err := dstFile.Chmod(srcInfo.Mode().Perm()); err != nil {
		return fmt.Errorf("set backup permissions: %w", err)
	}
	if err := dstFile.Sync(); err != nil {
		return fmt.Errorf("sync backup file: %w", err)
	}
	if err := dstFile.Close(); err != nil {
		closed = true
		return fmt.Errorf("close backup file: %w", err)
	}
	closed = true
	committed = true
	return nil
}

func inspectBackupFile(path string) (os.FileInfo, string, error) {
	file, err := fileutil.OpenRegularRead(path)
	if err != nil {
		return nil, "", fmt.Errorf("open backup: %w", err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("stat backup: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, "", fmt.Errorf("backup is not a regular file")
	}

	h := sha256.New()
	written, err := io.Copy(h, file)
	if err != nil {
		return nil, "", fmt.Errorf("checksum backup: %w", err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) || written != before.Size() {
		return nil, "", fmt.Errorf("backup changed while reading")
	}
	return before, hex.EncodeToString(h.Sum(nil)), nil
}
