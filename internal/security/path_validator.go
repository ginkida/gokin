package security

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// PathValidator validates file paths to prevent directory traversal attacks.
type PathValidator struct {
	allowedDirs   []string
	allowSymlinks bool
}

// NewPathValidator creates a new path validator.
func NewPathValidator(allowedDirs []string, allowSymlinks bool) *PathValidator {
	// Normalize allowed directories
	normalized := make([]string, len(allowedDirs))
	for i, dir := range allowedDirs {
		cleanDir := filepath.Clean(dir)
		if absDir, err := filepath.Abs(cleanDir); err == nil {
			cleanDir = absDir
		}
		if resolvedDir, err := filepath.EvalSymlinks(cleanDir); err == nil {
			cleanDir = resolvedDir
		}
		normalized[i] = cleanDir
	}
	return &PathValidator{
		allowedDirs:   normalized,
		allowSymlinks: allowSymlinks,
	}
}

// Validate validates that a path is safe and within allowed directories.
// Uses filepath.EvalSymlinks for atomic symlink resolution to prevent TOCTOU races.
func (v *PathValidator) Validate(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}

	// Additional security checks - do these first before any file operations
	if strings.Contains(path, "\x00") {
		return "", fmt.Errorf("null byte in path")
	}

	// Clean the path
	cleanPath := filepath.Clean(path)

	// Convert to absolute path for validation
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve absolute path: %w", err)
	}

	// Check for symlink if not allowed (check ORIGINAL path before resolution,
	// because EvalSymlinks resolves all symlinks and checkSymlink would find
	// nothing on the resolved path)
	if !v.allowSymlinks {
		if err := v.checkSymlink(absPath); err != nil {
			return "", err
		}
	}

	// Use EvalSymlinks for atomic symlink resolution (prevents TOCTOU race).
	resolvedPath, err := evalSymlinksWithParentFallback(absPath)
	if err != nil {
		return "", err
	}

	// Check if resolved path is within allowed directories
	if !v.isAllowed(resolvedPath) {
		return "", fmt.Errorf("path '%s' is outside allowed directories", filepath.Base(path))
	}

	return resolvedPath, nil
}

// evalSymlinksWithParentFallback resolves all symlinks in absPath atomically.
// If the path does not exist yet (new file), it resolves the PARENT directory
// and re-joins the base name — so a new file under an allowed dir validates,
// but a symlinked parent still resolves to its real target. This is the exact
// normalization Validate uses; IsPathWithinAny reuses it so the out-of-workspace
// access gate and real enforcement can never diverge (security invariant).
func evalSymlinksWithParentFallback(absPath string) (string, error) {
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return resolvedPath, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to resolve symlinks: %w", err)
	}
	// Path doesn't exist yet — resolve the parent dir to block symlink attacks.
	parentDir := filepath.Dir(absPath)
	resolvedParent, parentErr := filepath.EvalSymlinks(parentDir)
	if parentErr != nil && !os.IsNotExist(parentErr) {
		return "", fmt.Errorf("failed to resolve parent path: %w", parentErr)
	}
	if resolvedParent != "" {
		return filepath.Join(resolvedParent, filepath.Base(absPath)), nil
	}
	return absPath, nil
}

// IsPathWithinAny reports whether target resolves to a location inside ANY of
// dirs, using the identical normalization (Clean -> Abs -> EvalSymlinks with
// parent fallback) and containment logic (pathWithin) as PathValidator.Validate.
// It is the read-only pre-filter for the out-of-workspace access gate: it never
// grants access — it only answers "is this already inside an allowed area?" so
// the executor knows whether to prompt. The tool's own PathValidator remains the
// real enforcement boundary. An empty dirs slice means "no restriction" -> true.
func IsPathWithinAny(target string, dirs []string) (bool, error) {
	if target == "" {
		return false, fmt.Errorf("empty path")
	}
	if strings.Contains(target, "\x00") {
		return false, fmt.Errorf("null byte in path")
	}
	if len(dirs) == 0 {
		return true, nil
	}

	absTarget, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return false, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	resolvedTarget, err := evalSymlinksWithParentFallback(absTarget)
	if err != nil {
		return false, err
	}

	for _, dir := range dirs {
		absDir, err := filepath.Abs(filepath.Clean(dir))
		if err != nil {
			continue
		}
		// Resolve the dir the same way NewPathValidator stores it.
		if resolved, rErr := filepath.EvalSymlinks(absDir); rErr == nil {
			absDir = resolved
		}
		if pathWithin(resolvedTarget, absDir) {
			return true, nil
		}
	}
	return false, nil
}

// ValidateFile validates a file path for read/write operations.
func (v *PathValidator) ValidateFile(path string) (string, error) {
	absPath, err := v.Validate(path)
	if err != nil {
		return "", err
	}

	// Check if parent directory exists
	dir := filepath.Dir(absPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", fmt.Errorf("parent directory does not exist: %s", dir)
	}

	return absPath, nil
}

// ValidateDir validates a directory path.
func (v *PathValidator) ValidateDir(path string) (string, error) {
	absPath, err := v.Validate(path)
	if err != nil {
		return "", err
	}

	// Check if it's actually a directory
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}

	return absPath, nil
}

// isAllowed checks if the path is within allowed directories.
func (v *PathValidator) isAllowed(absPath string) bool {
	// If no restrictions, allow all (use with caution)
	if len(v.allowedDirs) == 0 {
		return true
	}

	for _, allowedDir := range v.allowedDirs {
		if v.isPathWithin(absPath, allowedDir) {
			return true
		}
	}
	return false
}

// isPathWithin checks if target is within base directory.
func (v *PathValidator) isPathWithin(target, base string) bool {
	return pathWithin(target, base)
}

// pathWithin checks if target is within base directory. Package-level so the
// validator and the IsPathWithinAny access gate share ONE containment
// definition (handles Windows cross-drive paths and "..": traversal).
func pathWithin(target, base string) bool {
	// Handle Windows cross-drive paths
	// filepath.Rel returns an error when target and base are on different drives
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}

	// If relative path starts with "..", target is outside base
	if strings.HasPrefix(rel, "..") {
		return false
	}

	// Double check: joined path must match exactly or be a subpath
	joined := filepath.Join(base, rel)
	// On Windows, paths are case-insensitive, so we need to compare accordingly
	if runtime.GOOS == "windows" {
		lowerJoined := strings.ToLower(joined)
		lowerBase := strings.ToLower(base)
		return lowerJoined == lowerBase || strings.HasPrefix(lowerJoined, lowerBase+string(filepath.Separator))
	}
	return joined == base || strings.HasPrefix(joined, base+string(filepath.Separator))
}

// checkSymlink checks if any component of the path is a symlink.
func (v *PathValidator) checkSymlink(path string) error {
	// Check each path component
	// Handle cross-platform paths
	sep := string(filepath.Separator)
	components := strings.Split(filepath.Clean(path), sep)

	current := ""
	if filepath.IsAbs(path) {
		if runtime.GOOS == "windows" {
			current = filepath.VolumeName(path) + sep
		} else {
			current = sep
		}
	}

	for _, comp := range components {
		if comp == "" {
			continue
		}
		current = filepath.Join(current, comp)

		info, err := os.Lstat(current)
		if err != nil {
			// Path doesn't exist yet, that's ok for new files
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		// Check if it's a symlink
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks not allowed: %s", current)
		}
	}

	return nil
}

// IsBlockedWritePath returns an error if the resolved path points to a location
// that must never be written to by tools. The .git/ directory contains hooks,
// config, and attributes that can execute arbitrary code on git operations.
func IsBlockedWritePath(path string) error {
	cleanPath := filepath.Clean(path)
	sep := string(filepath.Separator)
	gitComponent := sep + ".git" + sep
	gitSuffix := sep + ".git"
	// Block paths inside .git/ (hooks, config, attributes — all can execute code)
	// and the .git entry itself (in worktrees it's a file with gitdir: redirect
	// that can point to a directory with malicious hooks).
	if strings.Contains(cleanPath, gitComponent) || strings.HasSuffix(cleanPath, gitSuffix) {
		return fmt.Errorf("writing to .git/ directory is blocked for security reasons")
	}
	return nil
}

// IsGrantableDir reports an error if resolvedDir (an absolute, symlink-resolved
// directory path) must never be added to the allowed set by a user grant. It
// refuses the filesystem root, OS system directories, the .git directory, and
// well-known secret directories (~/.ssh, ~/.aws, ~/.gnupg, the gokin config dir).
// Granting a PARENT of these is still allowed (you can grant ~), so the check is
// "is the grant target itself inside a forbidden location", not "does it contain
// one". Writes into a granted subtree remain subject to IsBlockedWritePath and
// the normal per-tool permission prompt.
func IsGrantableDir(resolvedDir string) error {
	clean := filepath.Clean(resolvedDir)

	// Filesystem / volume root is too broad to ever grant.
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return fmt.Errorf("refusing to grant access to the filesystem root")
	}

	// .git is hard-blocked for writes anywhere; also refuse to grant it directly.
	if err := IsBlockedWritePath(clean); err != nil {
		return fmt.Errorf("refusing to grant access to a .git directory")
	}

	// OS system directories.
	systemDirs := []string{"/etc", "/sys", "/proc", "/dev", "/boot", "/var/run"}
	for _, d := range systemDirs {
		if withinBlockedDir(clean, d) {
			return fmt.Errorf("refusing to grant access to system directory %s", d)
		}
	}

	// Well-known secret/credential directories under the user's home.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		secretDirs := []string{
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".kube"),
			filepath.Join(home, ".config", "gokin"),
		}
		for _, d := range secretDirs {
			if withinBlockedDir(clean, d) {
				return fmt.Errorf("refusing to grant access to sensitive directory %s", d)
			}
		}
	}

	return nil
}

// withinBlockedDir reports whether clean is inside blocked, comparing against
// BOTH the literal blocked path and its symlink-resolved form. The grant target
// arrives already symlink-resolved (e.g. macOS /etc -> /private/etc), so a
// literal "/etc" blocklist entry would otherwise miss it.
func withinBlockedDir(clean, blocked string) bool {
	if pathWithin(clean, blocked) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(blocked); err == nil && resolved != blocked {
		return pathWithin(clean, resolved)
	}
	return false
}

// SanitizeFilename sanitizes a filename by removing dangerous characters.
func SanitizeFilename(name string) string {
	// Remove null bytes and other dangerous characters
	dangerous := []string{"\x00", "..", "/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	sanitized := name
	for _, dangerousChar := range dangerous {
		sanitized = strings.ReplaceAll(sanitized, dangerousChar, "_")
	}
	return sanitized
}

// JoinPathSafe joins path components safely.
func JoinPathSafe(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("base path cannot be empty")
	}

	cleanBase := filepath.Clean(base)
	cleanRel := filepath.Clean(rel)

	// Don't allow absolute paths in relative part
	if filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("relative path cannot be absolute")
	}

	joined := filepath.Join(cleanBase, cleanRel)

	// Verify the result is still within base (use separator-aware check to prevent prefix matching bugs)
	if joined != cleanBase && !strings.HasPrefix(joined, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal attempt detected")
	}

	return joined, nil
}
