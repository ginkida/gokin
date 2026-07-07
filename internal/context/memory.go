package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gokin/internal/logging"
)

// ProjectMemory holds project-specific instructions loaded from files.
type ProjectMemory struct {
	workDir      string
	instructions string
	sourcePath   string // Path where instructions were found
	mu           sync.RWMutex

	// File watching
	watcher       *FileWatcher
	watcherCancel context.CancelFunc
	onReload      func() // Callback when instructions are reloaded
}

// instructionFiles is the ordered list of files to search for instructions in the project directory.
var instructionFiles = []string{
	"GOKIN.md",
	"CLAUDE.md",
	"Claude.md",
	".gokin/rules.md",
	".gokin/instructions.md",
	".gokin/INSTRUCTIONS.md",
	".gokin.md",
	"rules.md",
}

// memoryLayerPaths returns paths for the multi-layer memory hierarchy.
// Layers are loaded in order (lowest to highest priority):
//  1. Global: ~/.config/gokin/GOKIN.md
//  2. User:   ~/.gokin/GOKIN.md
//  3. Project: ./GOKIN.md (or .gokin/GOKIN.md, .gokin/rules/*.md)
//  4. Local:   ./GOKIN.local.md (git-ignored, private)
func memoryLayerPaths(workDir string) []struct {
	label string
	paths []string // multiple paths per layer (e.g., rules/*.md)
} {
	homeDir, _ := os.UserHomeDir()
	layers := []struct {
		label string
		paths []string
	}{
		{
			label: "global",
			paths: []string{
				filepath.Join(homeDir, ".config", "gokin", "GOKIN.md"),
			},
		},
		{
			label: "user",
			paths: []string{
				filepath.Join(homeDir, ".gokin", "GOKIN.md"),
			},
		},
	}

	// Project layer: split into two sub-layers so that the "first match" break
	// in Load() only applies to instructionFiles, not to rules/*.md.
	projectMainPaths := []string{}
	for _, name := range instructionFiles {
		projectMainPaths = append(projectMainPaths, filepath.Join(workDir, name))
	}
	layers = append(layers, struct {
		label string
		paths []string
	}{
		label: "project",
		paths: projectMainPaths,
	})

	// Project rules layer: all .gokin/rules/*.md (always loaded, no "first match" break)
	rulesGlob := filepath.Join(workDir, ".gokin", "rules", "*.md")
	if matches, err := filepath.Glob(rulesGlob); err == nil && len(matches) > 0 {
		layers = append(layers, struct {
			label string
			paths []string
		}{
			label: "project-rules",
			paths: matches,
		})
	}

	// Agent-managed project memory layer (generated from memorize/project learning).
	layers = append(layers, struct {
		label string
		paths []string
	}{
		label: "project-memory",
		paths: []string{
			filepath.Join(workDir, ".gokin", "project-memory.md"),
		},
	})

	// Local layer (highest priority, git-ignored)
	layers = append(layers, struct {
		label string
		paths []string
	}{
		label: "local",
		paths: []string{
			filepath.Join(workDir, "GOKIN.local.md"),
		},
	})

	return layers
}

// InstructionFileNames returns the instruction discovery order.
func InstructionFileNames() []string {
	files := make([]string, len(instructionFiles))
	copy(files, instructionFiles)
	return files
}

// NewProjectMemory creates a new ProjectMemory instance.
func NewProjectMemory(workDir string) *ProjectMemory {
	return &ProjectMemory{
		workDir: workDir,
	}
}

// Load searches for and loads project instructions from the multi-layer hierarchy.
// Layers (lowest to highest priority): Global → User → Project → Local.
// All found layers are merged, with higher priority layers appended last.
// Returns nil error even if no file is found (instructions are optional).
func (m *ProjectMemory) Load() error {
	var merged strings.Builder
	var sources []string

	layers := memoryLayerPaths(m.workDir)
	for _, layer := range layers {
		for _, path := range layer.paths {
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			text := strings.TrimSpace(string(content))
			if text == "" {
				continue
			}
			// Process @include directives
			text = processIncludes(text, filepath.Dir(path))

			if merged.Len() > 0 {
				merged.WriteString("\n\n")
			}
			merged.WriteString(text)
			sources = append(sources, path)
			logging.Info("✓ Loaded instructions layer",
				"layer", layer.label,
				"path", path,
				"size_bytes", len(content))

			// For project layer, only load the FIRST matching file from instructionFiles
			// (but still load all rules/*.md)
			if layer.label == "project" {
				break
			}
		}
	}

	if merged.Len() > 0 {
		m.instructions = merged.String()
		m.sourcePath = strings.Join(sources, ", ")
		logging.Info("✓ Loaded project instructions",
			"sources", len(sources),
			"total_size", len(m.instructions))
	} else {
		logging.Debug("no project instructions found")
	}
	return nil
}

// processIncludes resolves @include directives in instruction content.
// Supports: @path, @./relative/path, @~ (home dir), @~/home/path, @/absolute/path
// Relative includes (including a no-slash @~foo) are restricted to prevent
// directory traversal outside the project.
func processIncludes(content string, baseDir string) string {
	lines := strings.Split(content, "\n")
	var result strings.Builder
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "@") && !strings.HasPrefix(trimmed, "@[") {
			// Resolve include path
			includePath := strings.TrimPrefix(trimmed, "@")
			includePath = strings.TrimSpace(includePath)

			wasRelative := false
			// Only bare "~" and "~/..." are home-rooted (and intentionally
			// exempt from the traversal guard). A no-slash "~foo" is NOT
			// home/foo here — it falls through to the guarded relative branch
			// so it can't silently read $HOME/foo or escape via "~..".
			if includePath == "~" || strings.HasPrefix(includePath, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					if includePath == "~" {
						includePath = home
					} else {
						includePath = filepath.Join(home, includePath[2:])
					}
				}
			} else if strings.HasPrefix(includePath, "./") || !filepath.IsAbs(includePath) {
				wasRelative = true
				includePath = filepath.Join(baseDir, includePath)
			}

			// Security: for originally-relative includes, block directory traversal
			// that escapes baseDir. Absolute (@/path) and home (@~/path) are allowed.
			if wasRelative {
				resolvedInclude := includePath
				if resolved, err := filepath.EvalSymlinks(includePath); err == nil {
					resolvedInclude = resolved
				}
				resolvedBase := baseDir
				if resolved, err := filepath.EvalSymlinks(baseDir); err == nil {
					resolvedBase = resolved
				}
				if !strings.HasPrefix(filepath.Clean(resolvedInclude), filepath.Clean(resolvedBase)) {
					result.WriteString(line)
					result.WriteString("\n")
					continue
				}
				includePath = resolvedInclude
			}

			if data, err := os.ReadFile(includePath); err == nil {
				result.WriteString(strings.TrimSpace(string(data)))
				result.WriteString("\n")
				continue
			}
			// Include failed — keep the original line as-is
		}
		result.WriteString(line)
		result.WriteString("\n")
	}
	return result.String()
}

// GetInstructions returns the loaded instructions.
func (m *ProjectMemory) GetInstructions() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instructions
}

// GetSourcePath returns the path where instructions were loaded from.
func (m *ProjectMemory) GetSourcePath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sourcePath
}

// HasInstructions returns true if instructions were loaded.
func (m *ProjectMemory) HasInstructions() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.instructions != ""
}

// Reload reloads instructions from disk.
func (m *ProjectMemory) Reload() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.instructions = ""
	m.sourcePath = ""
	return m.Load()
}

// StartWatching enables automatic reloading when instruction files change.
func (m *ProjectMemory) StartWatching(ctx context.Context, debounceMs int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.watcher != nil {
		return nil // Already watching
	}

	watchPath, watchingDir := m.resolveInstructionWatchPath()

	watcherCtx, cancel := context.WithCancel(ctx)
	m.watcherCancel = cancel

	var err error
	m.watcher, err = NewFileWatcher(watcherCtx, watchPath, debounceMs, func(path string) {
		logging.Info("instruction file changed, reloading", "path", path)
		if reloadErr := m.Reload(); reloadErr != nil {
			logging.Warn("failed to reload instructions", "error", reloadErr)
		} else {
			logging.Info("✓ Reloaded project instructions", "source", m.GetSourcePath())
			if m.onReload != nil {
				m.onReload()
			}
		}

		// If we're still bound to the directory (no instruction file
		// existed when watching started), re-resolve now that a change
		// fired the callback — a directory's mtime only changes on child
		// add/remove/rename, so if a concrete file exists now, rebind the
		// watcher to it. Without this, every EDIT after the file's first
		// creation stays invisible: the directory's mtime doesn't move on
		// a content-only write to an already-existing child, so the live-
		// reload feature would silently stop working after the first pickup.
		m.mu.Lock()
		stillDirWatch := m.watcher != nil && m.watcher.Path() == m.workDir
		m.mu.Unlock()
		if stillDirWatch {
			if newPath, isDir := m.resolveInstructionWatchPath(); !isDir {
				m.watcher.UpdatePath(newPath)
				logging.Debug("file watcher rebound to concrete instruction file", "path", newPath)
			}
		}
	})

	if err != nil {
		cancel()
		return err
	}

	logging.Info("started watching instruction files", "path", watchPath, "directory_fallback", watchingDir)
	return nil
}

// resolveInstructionWatchPath finds the concrete instruction file to watch,
// falling back to the project directory itself when none exists yet (so a
// file created later is still detected). Returns (path, true) for the
// directory-fallback case, (path, false) once a real file is found — callers
// use the bool to know whether the watcher should later be rebound via
// FileWatcher.UpdatePath once a concrete file appears.
func (m *ProjectMemory) resolveInstructionWatchPath() (string, bool) {
	for _, filename := range instructionFiles {
		path := filepath.Join(m.workDir, filename)
		if _, err := os.Stat(path); err == nil {
			return path, false
		}
	}
	projectMemoryPath := filepath.Join(m.workDir, ".gokin", "project-memory.md")
	if _, err := os.Stat(projectMemoryPath); err == nil {
		return projectMemoryPath, false
	}
	// No file found yet, watch project root so newly created GOKIN.md/CLAUDE.md
	// and .gokin/* files are detected without restart.
	return m.workDir, true
}

// StopWatching disables automatic reloading.
func (m *ProjectMemory) StopWatching() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.watcherCancel != nil {
		m.watcherCancel()
		m.watcherCancel = nil
	}
	m.watcher = nil
}

// OnReload sets a callback function to be called when instructions are reloaded.
func (m *ProjectMemory) OnReload(callback func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onReload = callback
}
