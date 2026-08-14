package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gokin/internal/git"
)

const (
	fileIndexCallbackMethod = "context.file_index"
	// Keep these limits in sync with MAX_SEARCH_FILES and the private index
	// reader in worker.py. The byte ceiling also keeps pathological file names
	// from turning an inventory request into an unbounded runtime artifact.
	maxFileIndexEntries  = 20_000
	maxFileIndexBytes    = 4 * 1024 * 1024
	maxIndexedPathBytes  = 16 * 1024
	fileIndexGitTimeout  = 10 * time.Second
	fileIndexRuntimeName = "visible-files.index"
	fileIndexTempPattern = "visible-files-*.tmp"
)

type fileIndexResult struct {
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"`
	Entries   int    `json:"entries,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// buildFileIndex publishes one fresh inventory for a worker callback. The
// worker may reuse that bounded snapshot for the same scope within one cell;
// it clears the cache between cells and before mutable callbacks. In a
// repository, git supplies its native ignore semantics (root/nested .gitignore,
// .git/info/exclude, global excludes, negations, and tracked ignored files).
// A matcher-backed walk preserves the same contract outside Git repositories.
// Only the fixed runtime file path crosses the JSON protocol.
func (m *Manager) buildFileIndex(ctx context.Context, params map[string]any) (fileIndexResult, error) {
	rel, root, err := m.validatedIndexRoot(params)
	if err != nil {
		return fileIndexResult{}, err
	}

	if m.opts.GitPath != "" {
		// An explicit ignored scope is authority to read that directory, not a
		// request to apply ignore filtering inside its root. Check the root even
		// when it contains tracked files: git ls-files would otherwise return the
		// tracked subset while silently omitting ignored untracked siblings.
		if rel != "." {
			ignored, checkErr := gitRootIgnored(ctx, m.opts.GitPath, m.opts.WorkDir, rel)
			if checkErr == nil && ignored {
				return m.writeExplicitFileIndex(root, "git-explicit")
			}
		}
		result, indexErr := m.writeGitFileIndex(ctx, rel)
		if indexErr == nil {
			// Root scope and any successfully classified explicit scope use Git's
			// complete native ignore semantics.
			if rel == "." || result.Entries > 0 || result.Truncated {
				return result, nil
			}
			// If check-ignore itself failed above, retain the old empty-scope
			// fallback before degrading to the matcher-backed inventory.
			ignored, checkErr := gitRootIgnored(ctx, m.opts.GitPath, m.opts.WorkDir, rel)
			if checkErr == nil {
				if ignored {
					return m.writeExplicitFileIndex(root, "git-explicit")
				}
				return result, nil
			}
		}
	}

	return m.writeMatcherFileIndex(root)
}

func (m *Manager) validatedIndexRoot(params map[string]any) (string, string, error) {
	raw, ok := params["path"].(string)
	if !ok || raw == "" || !utf8.ValidString(raw) || len(raw) > maxIndexedPathBytes {
		return "", "", fmt.Errorf("invalid file index path")
	}
	if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return "", "", fmt.Errorf("file index path must be workspace-relative")
	}
	clean := filepath.Clean(filepath.FromSlash(raw))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("file index path escapes workspace")
	}
	root := filepath.Join(m.opts.WorkDir, clean)
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve file index path: %w", err)
	}
	within, err := filepath.Rel(m.opts.WorkDir, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("file index path escapes workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", fmt.Errorf("stat file index path: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("file index path is not a directory")
	}
	if within == "." {
		return ".", resolved, nil
	}
	return filepath.ToSlash(within), resolved, nil
}

func gitRootIgnored(parent context.Context, gitPath, workDir, rel string) (bool, error) {
	if rel == "." {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(parent, fileIndexGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitPath,
		"--no-optional-locks", "-C", workDir,
		"-c", "core.fsmonitor=false",
		"check-ignore", "--no-index", "-q", "-z", "--stdin",
	)
	cmd.Env = fileIndexGitEnv()
	cmd.Stdin = strings.NewReader(rel + "\x00")
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return false, err
}

func (m *Manager) writeGitFileIndex(parent context.Context, rel string) (fileIndexResult, error) {
	ctx, cancel := context.WithTimeout(parent, fileIndexGitTimeout)
	defer cancel()
	args := []string{
		"--no-optional-locks", "-C", m.opts.WorkDir,
		"-c", "core.fsmonitor=false",
		"ls-files", "--cached", "--others", "--exclude-standard", "-z",
	}
	if rel != "." {
		// Git already scopes ls-files output/pathspecs to -C workDir. Do not use
		// :(top): workDir is allowed to be a subdirectory of a larger repository,
		// and top would incorrectly anchor this workspace-relative path at the
		// outer repository root.
		args = append(args, "--", ":(literal)"+rel)
	}
	cmd := exec.CommandContext(ctx, m.opts.GitPath, args...)
	cmd.Env = fileIndexGitEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fileIndexResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return fileIndexResult{}, err
	}

	result, streamErr := m.writeIndex(func(emit func(string) bool) error {
		reader := bufio.NewReaderSize(stdout, 64*1024)
		for {
			raw, readErr := reader.ReadString(0)
			if len(raw) > 0 {
				raw = strings.TrimSuffix(raw, "\x00")
				if raw != "" && !emit(raw) {
					cancel()
					return nil
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				return readErr
			}
		}
	})
	waitErr := cmd.Wait()
	if streamErr != nil {
		return fileIndexResult{}, streamErr
	}
	if waitErr != nil && !result.Truncated {
		if ctx.Err() != nil {
			return fileIndexResult{}, ctx.Err()
		}
		return fileIndexResult{}, waitErr
	}
	result.Source = "git"
	return result, nil
}

func (m *Manager) writeMatcherFileIndex(root string) (fileIndexResult, error) {
	matcher := git.NewGitIgnore(m.opts.WorkDir)
	if err := matcher.Load(); err != nil {
		return fileIndexResult{}, fmt.Errorf("load gitignore rules: %w", err)
	}
	if root != m.opts.WorkDir && matcher.IsIgnored(root) {
		return m.writeExplicitFileIndex(root, "matcher-explicit")
	}

	result, err := m.writeIndex(func(emit func(string) bool) error {
		return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if path == root {
				return nil
			}
			if matcher.IsIgnored(path) {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type().IsRegular() {
				rel, relErr := filepath.Rel(m.opts.WorkDir, path)
				if relErr != nil {
					return relErr
				}
				if !emit(filepath.ToSlash(rel)) {
					return errFileIndexFull
				}
			}
			return nil
		})
	})
	if err != nil {
		return fileIndexResult{}, err
	}
	result.Source = "matcher"
	return result, nil
}

// writeExplicitFileIndex preserves the documented ability to analyze a
// directly named ignored directory without falling back to an unbounded Python
// walk. The root is already canonical, within the workspace, and explicitly
// requested by the cell. Built-in metadata/vendor exclusions are applied again
// by the worker relative to this scope.
func (m *Manager) writeExplicitFileIndex(root, source string) (fileIndexResult, error) {
	result, err := m.writeIndex(func(emit func(string) bool) error {
		return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || path == root {
				return nil
			}
			if entry.Type().IsRegular() {
				rel, relErr := filepath.Rel(m.opts.WorkDir, path)
				if relErr != nil {
					return relErr
				}
				if !emit(filepath.ToSlash(rel)) {
					return errFileIndexFull
				}
			}
			return nil
		})
	})
	if err != nil {
		return fileIndexResult{}, err
	}
	result.Source = source
	return result, nil
}

var errFileIndexFull = errors.New("file index limit reached")

func (m *Manager) writeIndex(produce func(func(string) bool) error) (fileIndexResult, error) {
	temp, err := os.CreateTemp(m.runtimeDir, fileIndexTempPattern)
	if err != nil {
		return fileIndexResult{}, fmt.Errorf("create file index: %w", err)
	}
	tempPath := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fileIndexResult{}, err
	}
	// Large repositories may contribute 20,000 paths. Buffer publication so
	// that does not become one write syscall per path; the final flush/close and
	// atomic rename preserve the same fail-closed publication boundary.
	writer := bufio.NewWriterSize(temp, 64*1024)

	result := fileIndexResult{}
	written := 0
	emit := func(rel string) bool {
		if result.Entries >= maxFileIndexEntries || len(rel) > maxIndexedPathBytes ||
			written+len(rel)+1 > maxFileIndexBytes {
			result.Truncated = true
			return false
		}
		if !utf8.ValidString(rel) || strings.IndexByte(rel, 0) >= 0 {
			result.Truncated = true
			return true
		}
		if _, err = io.WriteString(writer, rel); err != nil {
			return false
		}
		if err = writer.WriteByte(0); err != nil {
			return false
		}
		written += len(rel) + 1
		result.Entries++
		return true
	}
	produceErr := produce(emit)
	if err != nil {
		return fileIndexResult{}, err
	}
	if produceErr != nil && !errors.Is(produceErr, errFileIndexFull) {
		return fileIndexResult{}, produceErr
	}
	if err := writer.Flush(); err != nil {
		return fileIndexResult{}, err
	}
	if err := temp.Close(); err != nil {
		return fileIndexResult{}, err
	}
	finalPath := filepath.Join(m.runtimeDir, fileIndexRuntimeName)
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fileIndexResult{}, fmt.Errorf("publish file index: %w", err)
	}
	keep = true
	result.Path = finalPath
	return result, nil
}

func fileIndexGitEnv() []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin",
		"LANG=C", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat",
	}
	// Do not inherit arbitrary GIT_* variables from the launcher: repository
	// inventory must not gain config injection or alternate-index behavior.
	// HOME/XDG_CONFIG_HOME are enough for Git's standard global excludes, and
	// only absolute values can name host configuration roots.
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME"} {
		if value := os.Getenv(name); filepath.IsAbs(value) {
			env = append(env, name+"="+value)
		}
	}
	return env
}
