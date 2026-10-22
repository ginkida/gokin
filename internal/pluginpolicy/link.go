package pluginpolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var ErrLinkedWorkspace = errors.New("linked plugin cannot load from within its own directory")

// ReadLinkedManifest confines schema reads to the producer's root too. Unlike
// a copied tree, unrelated large files are deliberately never opened.
func ReadLinkedManifest(directory string) (map[string]json.RawMessage, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(".claude-plugin/plugin.json")
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("linked plugin manifest must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("linked plugin manifest exceeds byte limit")
	}
	if err := ValidateJSON(data); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("linked plugin manifest must be an object")
	}
	return fields, nil
}

func PathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckLinkedRoot only examines top-level metadata. Large SDK trees must not
// be recursively read or subjected to copied-plugin size limits.
func CheckLinkedRoot(root, workDir string) (string, []os.DirEntry, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command link mode is unavailable on Windows; use copy mode")
	}
	if !filepath.IsAbs(root) {
		return "", nil, fmt.Errorf("linked plugin path must be absolute")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	if workDir != "" {
		work, err := filepath.EvalSymlinks(workDir)
		if err != nil {
			return "", nil, err
		}
		if PathWithin(real, work) {
			return "", nil, ErrLinkedWorkspace
		}
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return "", nil, err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(real, entry.Name()))
		if err != nil {
			return "", nil, err
		}
		if !PathWithin(real, target) {
			return "", nil, fmt.Errorf("linked plugin top-level symlink escapes its directory")
		}
	}
	return real, entries, nil
}

// ResolveLinkedRoot authenticates a registry-declared link view. It never
// accepts link authority from a plugin's own files or a component manifest.
func ResolveLinkedRoot(cache, source, workDir string) (string, error) {
	real, _, err := CheckLinkedRoot(source, workDir)
	if err != nil {
		return "", err
	}
	if real != filepath.Clean(source) {
		return "", fmt.Errorf("linked plugin source real path changed; update the plugin")
	}
	info, err := os.Lstat(cache)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("linked plugin cache must be a real directory")
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("linked plugin cache is empty")
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("linked plugin cache contains a non-link entry")
		}
		target, err := os.Readlink(filepath.Join(cache, entry.Name()))
		if err != nil {
			return "", err
		}
		if target != filepath.Join(real, entry.Name()) {
			return "", fmt.Errorf("linked plugin cache target changed")
		}
	}
	return real, nil
}
