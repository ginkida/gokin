package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gokin/internal/fileutil"
)

// MaxConfigFileBytes bounds both configuration reads and writes. The wizard
// and the runtime loader share this limit so a file accepted by one path is
// never rejected solely because the other path used a different ceiling.
const MaxConfigFileBytes int64 = 2 << 20

// ReadConfigFile performs a bounded, symlink-safe read. The default global
// config is owner-only; explicit --config and repository config files retain
// their user-managed parent/file modes while receiving the same integrity
// checks.
func ReadConfigFile(path string) ([]byte, error) {
	return readConfigFile(path)
}

// WriteConfigFile atomically writes one complete owner-only config file.
func WriteConfigFile(path string, data []byte) error {
	configSaveMu.Lock()
	defer configSaveMu.Unlock()
	return writeConfigFile(path, data)
}

// UpdateConfigFile serializes a bounded read-modify-write transaction with all
// Config.Save and setup-wizard writers in this process. A missing file is
// represented by nil existing data.
func UpdateConfigFile(path string, update func(existing []byte) ([]byte, error)) error {
	if update == nil {
		return fmt.Errorf("config update callback is nil")
	}
	configSaveMu.Lock()
	defer configSaveMu.Unlock()

	existing, err := readConfigFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		existing = nil
	}
	data, err := update(existing)
	if err != nil {
		return err
	}
	return writeConfigFile(path, data)
}

func readConfigFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	// A config symlinked into a dotfiles repository is a first-class pattern.
	// The strict readers reject a symlinked final component — correct for
	// Gokin's own durable state, but here it silently reverted the user to
	// defaults (no provider, no key) on a file they deliberately placed. Follow
	// the link, keep every integrity check against the resolved target, and
	// leave its mode alone: it belongs to the user's repository.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		data, err := fileutil.ReadResolvedRegularFile(path, MaxConfigFileBytes)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, configNotExistError(path)
			}
			return nil, fmt.Errorf("read config file: %w", err)
		}
		return data, nil
	}
	if usesPrivateConfigParent(path) {
		dir := filepath.Dir(path)
		if _, err := os.Lstat(dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, configNotExistError(path)
			}
			return nil, fmt.Errorf("inspect config directory: %w", err)
		}
		if err := fileutil.EnsureUserPrivateDir(dir); err != nil {
			return nil, fmt.Errorf("secure config directory: %w", err)
		}
		data, err := fileutil.ReadPrivateFile(path, MaxConfigFileBytes)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, configNotExistError(path)
			}
			return nil, fmt.Errorf("read config file: %w", err)
		}
		return data, nil
	}
	data, err := fileutil.ReadRegularFile(path, MaxConfigFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, configNotExistError(path)
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}
	return data, nil
}

func configNotExistError(path string) error {
	return &os.PathError{Op: "read config", Path: path, Err: os.ErrNotExist}
}

func writeConfigFile(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	if int64(len(data)) > MaxConfigFileBytes {
		return fmt.Errorf("config file exceeds %d-byte limit", MaxConfigFileBytes)
	}
	dir := filepath.Dir(path)
	if usesPrivateConfigParent(path) {
		if err := fileutil.EnsureUserPrivateDir(dir); err != nil {
			return fmt.Errorf("secure config directory: %w", err)
		}
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	// A symlinked config is written THROUGH the link, not over it: the atomic
	// temp+rename targets the resolved file, so the user's link survives and
	// their dotfiles copy is the one updated. Refusing instead would leave a
	// dotfiles user unable to run /login, the setup wizard, or any /set.
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return fmt.Errorf(
				"config file %q is a symlink that does not resolve: %w", path, resolveErr)
		}
		target, statErr := os.Stat(resolved)
		if statErr != nil {
			return fmt.Errorf("inspect config symlink target: %w", statErr)
		}
		if !target.Mode().IsRegular() {
			return fmt.Errorf(
				"config file %q resolves to %q, which is not a regular file", path, resolved)
		}
		// Keep the target's own mode: it belongs to the user's repository, and
		// tightening it here would surface as an unexplained change there.
		if err := fileutil.AtomicWrite(resolved, data, target.Mode().Perm()); err != nil {
			return fmt.Errorf("write config file: %w", err)
		}
		return nil
	}
	if err := fileutil.SecurePrivateFile(path); err != nil {
		return fmt.Errorf("secure config file: %w", err)
	}
	if err := fileutil.AtomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

func usesPrivateConfigParent(path string) bool {
	if configuredExplicitPath() != "" {
		return false
	}
	defaultPath := getConfigPath()
	return defaultPath != "" && filepath.Clean(path) == filepath.Clean(defaultPath)
}
