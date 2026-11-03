package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SecretPaths is the shared credential-location inventory for tool paths,
// directory grants, SSH transfers and the bash sandbox. Include both lexical
// paths and resolved aliases so a symlinked credential directory stays private.
func SecretPaths() []string {
	key := secretPathsCacheKey()
	secretPathsCache.mu.Lock()
	defer secretPathsCache.mu.Unlock()
	if secretPathsCache.key == key && time.Since(secretPathsCache.at) < secretPathsCacheTTL {
		return secretPathsCache.paths
	}
	paths := computeSecretPaths()
	secretPathsCache.key, secretPathsCache.at, secretPathsCache.paths = key, time.Now(), paths
	return paths
}

// Walkers consult the list once per directory, and every lookup resolves a
// dozen symlinks; cache briefly, keyed by the environment that defines the
// list so tests (and a changed HOME) never see a stale inventory.
const secretPathsCacheTTL = 2 * time.Second

var secretPathsCache struct {
	mu    sync.Mutex
	key   string
	at    time.Time
	paths []string
}

func secretPathsCacheKey() string {
	home, _ := os.UserHomeDir()
	configDir, _ := os.UserConfigDir()
	return strings.Join([]string{home, configDir, os.Getenv("XDG_CONFIG_HOME"), os.Getenv("CLAUDE_CONFIG_DIR")}, "\x00")
}

func computeSecretPaths() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// ~/.claude as a whole is NOT listed: it holds personal skills whose
		// reference files the model legitimately reads. Only its credential
		// files are. The bash sandbox still masks the whole directory.
		for _, rel := range []string{".ssh", ".aws", ".gnupg", ".kube", ".config/gcloud", ".docker/config.json", ".netrc", ".claude/.credentials.json", ".claude.json", ".config/gokin"} {
			paths = append(paths, filepath.Join(home, filepath.FromSlash(rel)))
		}
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		paths = append(paths, filepath.Join(dir, "gokin"))
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		paths = append(paths, filepath.Join(dir, "gokin"))
	}
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		paths = append(paths, filepath.Join(dir, ".credentials.json"))
	}
	return resolveSecretPaths(paths)
}

func resolveSecretPaths(paths []string) []string {
	var result []string
	seen := make(map[string]bool)
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		add(absolute)
		if resolved, err := evalSymlinksWithParentFallback(absolute); err == nil {
			add(resolved)
		}
	}
	return result
}

// IsSecretPath reports whether an absolute, resolved path is a credential
// location or one of its descendants. A similarly named project directory is
// not a credential location merely because its basename matches.
func IsSecretPath(resolved string) bool {
	for _, secret := range SecretPaths() {
		if pathWithin(resolved, secret) {
			return true
		}
	}
	return false
}

func secretPathError(path string) error {
	return fmt.Errorf("credential location %q withheld; copy the needed file into the workspace, or explicitly exempt its absolute path in tools.allow_secret_paths in your user config", filepath.Base(path))
}

// SetAllowSecretPaths installs user-configured exceptions in place, under the
// same lock as directory grants. Exceptions remove only the credential deny;
// they never add a workspace grant or change IsGrantableDir's restrictions.
func (v *PathValidator) SetAllowSecretPaths(paths []string) {
	var absolute []string
	for _, path := range paths {
		if filepath.IsAbs(path) {
			absolute = append(absolute, path)
		}
	}
	normalized := resolveSecretPaths(absolute)
	v.mu.Lock()
	v.allowSecretPaths = normalized
	v.secretPathRevision++
	v.mu.Unlock()
}

// AllowSecretPaths returns a defensive snapshot for cloned tool validators.
func (v *PathValidator) AllowSecretPaths() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return append([]string(nil), v.allowSecretPaths...)
}

// IsSecretPath applies the user-configured exceptions to the shared deny list.
func (v *PathValidator) IsSecretPath(resolved string) bool {
	if !IsSecretPath(resolved) {
		return false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, allowed := range v.allowSecretPaths {
		if pathWithin(resolved, allowed) {
			return false
		}
	}
	return true
}

// SecretPathRevision binds cached tool results to the credential policy used
// before dispatch. Revoking an exception cannot reuse a previously allowed read,
// even if an in-flight call publishes its result after the revocation.
func (v *PathValidator) SecretPathRevision() uint64 {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.secretPathRevision
}
