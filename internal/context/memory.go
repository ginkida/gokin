package context

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gokin/internal/logging"
)

// ProjectMemory holds project-specific instructions loaded from files.
type ProjectMemory struct {
	workDir      string
	instructions string
	sourcePath   string // Path where instructions were found
	watchPaths   []string
	mu           sync.RWMutex
	loadMu       sync.Mutex
	watchMu      sync.Mutex

	// File watching
	watcher         *FileWatcher
	extraWatchers   map[string]*FileWatcher
	watcherCtx      context.Context
	watcherCancel   context.CancelFunc
	watchDebounce   int
	watchGeneration uint64
	onReload        func() // Callback when instructions are reloaded
}

const (
	// Instruction files become part of every model prompt. Keep both individual
	// reads and recursive expansion bounded so a repository cannot turn startup
	// into an unbounded read or silently consume the whole context window.
	maxInstructionFileBytes     = 256 << 10
	maxExpandedInstructionBytes = 1 << 20
	maxMergedInstructionBytes   = 2 << 20
	maxInstructionIncludeDepth  = 8
	maxInstructionIncludes      = 64
	maxInstructionWatchPaths    = 256
	maxProjectRuleFiles         = 128
)

var (
	errInstructionTooLarge = errors.New("instruction file exceeds size limit")
	errInstructionNotUTF8  = errors.New("instruction file is not valid UTF-8")
	errInstructionNotFile  = errors.New("instruction path is not a regular file")
)

type instructionOrigin uint8

const (
	userInstructionOrigin instructionOrigin = iota
	projectInstructionOrigin
)

type memoryLayer struct {
	label  string
	paths  []string
	origin instructionOrigin
}

type instructionDocument struct {
	label  string
	source string
	text   string
}

type projectMemorySnapshot struct {
	instructions string
	sourcePath   string
	watchPaths   []string
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
func memoryLayerPaths(workDir string) []memoryLayer {
	layers := make([]memoryLayer, 0, 6)
	// A missing home directory must fail closed. Joining an empty home with
	// ".config/..." would reinterpret a repository-relative path as a trusted
	// global/user source and restore unrestricted import privileges.
	if homeDir, err := os.UserHomeDir(); err == nil && strings.TrimSpace(homeDir) != "" {
		layers = append(layers, memoryLayer{
			label:  "global",
			origin: userInstructionOrigin,
			paths: []string{
				filepath.Join(homeDir, ".config", "gokin", "GOKIN.md"),
			},
		}, memoryLayer{
			label:  "user",
			origin: userInstructionOrigin,
			paths: []string{
				filepath.Join(homeDir, ".gokin", "GOKIN.md"),
			},
		})
	}

	// Project layer: split into two sub-layers so that the "first match" break
	// in Load() only applies to instructionFiles, not to rules/*.md.
	projectMainPaths := []string{}
	for _, name := range instructionFiles {
		projectMainPaths = append(projectMainPaths, filepath.Join(workDir, name))
	}
	layers = append(layers, memoryLayer{
		label:  "project",
		paths:  projectMainPaths,
		origin: projectInstructionOrigin,
	})

	// Project rules layer: all .gokin/rules/*.md (always loaded, no "first
	// match" break). Prove the directory itself is contained before Glob walks
	// it, otherwise a repository-controlled `rules` symlink could enumerate an
	// external directory even though individual file reads later fail closed.
	if matches := containedProjectRulePaths(workDir); len(matches) > 0 {
		layers = append(layers, memoryLayer{
			label:  "project-rules",
			paths:  matches,
			origin: projectInstructionOrigin,
		})
	}

	// Agent-managed project memory layer (generated from memorize/project learning).
	layers = append(layers, memoryLayer{
		label:  "project-memory",
		origin: projectInstructionOrigin,
		paths: []string{
			filepath.Join(workDir, ".gokin", "project-memory.md"),
		},
	})

	// Local layer (highest priority, git-ignored)
	layers = append(layers, memoryLayer{
		label:  "local",
		origin: projectInstructionOrigin,
		paths: []string{
			filepath.Join(workDir, "GOKIN.local.md"),
		},
	})

	return layers
}

func containedProjectRulePaths(workDir string) []string {
	root, err := canonicalInstructionRoot(workDir)
	if err != nil {
		return nil
	}
	rulesDir := filepath.Join(workDir, ".gokin", "rules")
	resolvedRulesDir, err := canonicalInstructionRoot(rulesDir)
	if err != nil || !pathContained(resolvedRulesDir, root) {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(rulesDir, "*.md"))
	if err != nil {
		return nil
	}
	if len(matches) > maxProjectRuleFiles {
		matches = matches[:maxProjectRuleFiles]
	}
	return matches
}

// InstructionFileNames returns the instruction discovery order.
func InstructionFileNames() []string {
	files := make([]string, len(instructionFiles))
	copy(files, instructionFiles)
	return files
}

// NewProjectMemory creates a new ProjectMemory instance.
func NewProjectMemory(workDir string) *ProjectMemory {
	if abs, err := filepath.Abs(workDir); err == nil {
		workDir = filepath.Clean(abs)
	}
	return &ProjectMemory{
		workDir: workDir,
	}
}

// Load searches for and loads project instructions from the multi-layer hierarchy.
// Layers (lowest to highest priority): Global → User → Project → Local.
// All found layers are merged, with higher priority layers appended last.
// Returns nil error even if no file is found (instructions are optional).
func (m *ProjectMemory) Load() error {
	m.loadAndCommit(0)
	return nil
}

// loadAndCommit builds a complete disk snapshot and publishes it only if the
// originating watcher generation is still active. generation==0 is an
// explicit/direct load and is always allowed to commit.
func (m *ProjectMemory) loadAndCommit(generation uint64) (committed, changed bool) {
	// Serialize disk snapshots so an older, slower Load cannot overwrite a
	// newer one. The potentially slow IO happens without m.mu; readers keep the
	// previous complete snapshot until the new one is ready to commit.
	m.loadMu.Lock()
	snapshot := m.loadSnapshot()
	m.mu.Lock()
	if generation != 0 && !m.watchGenerationActiveLocked(generation) {
		m.mu.Unlock()
		m.loadMu.Unlock()
		return false, false
	}
	changed = m.instructions != snapshot.instructions || m.sourcePath != snapshot.sourcePath
	m.instructions = snapshot.instructions
	m.sourcePath = snapshot.sourcePath
	m.watchPaths = append(m.watchPaths[:0], snapshot.watchPaths...)
	m.mu.Unlock()
	m.loadMu.Unlock()

	// If live reload is active, atomically committed dependencies can now be
	// reconciled with supplemental watchers. This is intentionally outside both
	// state locks: watcher creation may start goroutines and must not block reads.
	m.reconcileInstructionWatchers()

	if snapshot.instructions == "" {
		logging.Debug("no project instructions found")
	} else {
		logging.Info("✓ Loaded project instructions",
			"sources", len(strings.Split(snapshot.sourcePath, ", ")),
			"total_size", len(snapshot.instructions))
	}
	return true, changed
}

func (m *ProjectMemory) loadSnapshot() projectMemorySnapshot {
	layers := memoryLayerPaths(m.workDir)
	watchSet := make(map[string]struct{})
	for _, layer := range layers {
		for _, path := range layer.paths {
			addInstructionWatchPath(watchSet, path)
		}
	}
	// New files in the globbed rules directory are not represented in layer
	// paths yet. Watching the directory catches create/delete/rename; each
	// discovered file is watched separately for content changes.
	addInstructionWatchPath(watchSet, filepath.Join(m.workDir, ".gokin", "rules"))

	projectRoot, rootErr := canonicalInstructionRoot(m.workDir)
	if rootErr != nil {
		logging.Warn("project instruction root is unavailable; repository instructions disabled",
			"work_dir", m.workDir, "error", rootErr)
	}

	documents := make([]instructionDocument, 0, len(layers))
	for _, layer := range layers {
		for _, path := range layer.paths {
			containmentRoot := ""
			if layer.origin == projectInstructionOrigin {
				if rootErr != nil {
					continue
				}
				containmentRoot = projectRoot
			}

			content, resolvedPath, err := readInstructionFile(path, containmentRoot)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					logging.Warn("skipping unsafe or invalid instruction file",
						"layer", layer.label, "path", path, "error", err)
				}
				continue
			}
			text := strings.TrimSpace(content)
			if text == "" {
				continue
			}

			policy := instructionIncludePolicy{
				containmentRoot: containmentRoot,
				watchPaths:      watchSet,
			}
			expander := newInstructionIncludeExpander(policy)
			expander.stack[resolvedPath] = struct{}{}
			expander.render(text, filepath.Dir(resolvedPath), 0)
			text = strings.TrimSpace(expander.String())
			if text == "" {
				continue
			}

			documents = append(documents, instructionDocument{
				label:  layer.label,
				source: path,
				text:   text,
			})
			logging.Info("✓ Loaded instructions layer",
				"layer", layer.label,
				"path", path,
				"size_bytes", len(content))

			// Project aliases are alternatives: only the first valid, non-empty
			// file wins. Invalid or unsafe higher-priority aliases fall through.
			if layer.label == "project" {
				break
			}
		}
	}

	merged, sources := mergeInstructionDocuments(documents)
	watchPaths := make([]string, 0, len(watchSet))
	for path := range watchSet {
		watchPaths = append(watchPaths, path)
	}
	sort.Strings(watchPaths)
	if len(watchPaths) > maxInstructionWatchPaths {
		watchPaths = watchPaths[:maxInstructionWatchPaths]
	}
	return projectMemorySnapshot{
		instructions: merged,
		sourcePath:   strings.Join(sources, ", "),
		watchPaths:   watchPaths,
	}
}

// mergeInstructionDocuments enforces the total prompt budget without letting
// low-priority global content crowd out higher-priority project/local rules.
// Documents are selected from highest to lowest priority, then emitted in the
// original low-to-high order expected by the prompt.
func mergeInstructionDocuments(documents []instructionDocument) (string, []string) {
	selected := make([]bool, len(documents))
	used := 0
	for i := len(documents) - 1; i >= 0; i-- {
		separator := 0
		if used > 0 {
			separator = 2
		}
		if len(documents[i].text)+separator > maxMergedInstructionBytes-used {
			logging.Warn("skipping lower-priority instructions beyond merged size limit",
				"layer", documents[i].label, "path", documents[i].source)
			continue
		}
		selected[i] = true
		used += len(documents[i].text) + separator
	}

	var merged strings.Builder
	sources := make([]string, 0, len(documents))
	for i, document := range documents {
		if !selected[i] {
			continue
		}
		if merged.Len() > 0 {
			merged.WriteString("\n\n")
		}
		merged.WriteString(document.text)
		sources = append(sources, document.source)
	}
	return merged.String(), sources
}

// processIncludes resolves @include directives in instruction content.
// Supports: @path, @./relative/path, @~ (home dir), @~/home/path, @/absolute/path
// This compatibility helper represents an explicitly user-controlled source:
// absolute/home imports remain allowed, while relative imports remain confined
// to baseDir as before. ProjectMemory uses a stricter project-root policy for
// repository-origin files.
func processIncludes(content string, baseDir string) string {
	root, err := canonicalInstructionRoot(baseDir)
	if err != nil {
		// Keep the historical lexical traversal guard even when baseDir does
		// not currently exist (common in callers that only use absolute imports).
		root, _ = filepath.Abs(baseDir)
		root = filepath.Clean(root)
	}
	expander := newInstructionIncludeExpander(instructionIncludePolicy{})
	expander.render(content, root, 0)
	return expander.String()
}

type instructionIncludePolicy struct {
	// containmentRoot is set only for repository-origin instructions. When
	// present, every relative, absolute, home, and symlink-resolved import must
	// remain under this canonical workspace root. This deliberately does not
	// reuse executable-hook workspace trust as a filesystem-read escape hatch:
	// trusting repo hooks and sending arbitrary local files to the model are
	// separate capabilities. Cross-root imports belong in user/global sources.
	containmentRoot string
	watchPaths      map[string]struct{}
}

type instructionIncludeExpander struct {
	policy    instructionIncludePolicy
	stack     map[string]struct{}
	result    strings.Builder
	includes  int
	truncated bool
}

func newInstructionIncludeExpander(policy instructionIncludePolicy) *instructionIncludeExpander {
	return &instructionIncludeExpander{
		policy: policy,
		stack:  make(map[string]struct{}),
	}
}

func (e *instructionIncludeExpander) String() string { return e.result.String() }

func (e *instructionIncludeExpander) render(content, baseDir string, depth int) {
	for _, line := range strings.Split(content, "\n") {
		if e.truncated {
			return
		}
		include, ok := parseInstructionInclude(line)
		if !ok || depth >= maxInstructionIncludeDepth || e.includes >= maxInstructionIncludes {
			e.writeBounded(line + "\n")
			continue
		}

		e.includes++
		candidate, relative, ok := resolveInstructionIncludeCandidate(include, baseDir)
		if !ok {
			e.writeBounded(line + "\n")
			continue
		}

		containmentRoot := e.policy.containmentRoot
		if containmentRoot == "" && relative {
			// User/global sources may explicitly import external absolute/home
			// files. A relative import is confined to the directory of the file
			// that declared it, including inside such an explicit import.
			containmentRoot = baseDir
		}
		// Keep an allowed missing dependency watched so delete/recreate and
		// atomic-save workflows recover automatically. Resolve the nearest
		// existing ancestor before accepting it, which prevents a symlinked
		// parent from smuggling an external project path into the watch set.
		if instructionCandidateWatchable(candidate, containmentRoot) {
			addInstructionWatchPath(e.policy.watchPaths, candidate)
		}
		included, resolvedPath, err := readInstructionFile(candidate, containmentRoot)
		if err != nil {
			e.writeBounded(line + "\n")
			continue
		}
		addInstructionWatchPath(e.policy.watchPaths, candidate)
		addInstructionWatchPath(e.policy.watchPaths, resolvedPath)
		if _, cyclic := e.stack[resolvedPath]; cyclic {
			e.writeBounded(line + "\n")
			continue
		}

		e.stack[resolvedPath] = struct{}{}
		e.render(strings.TrimSpace(included), filepath.Dir(resolvedPath), depth+1)
		delete(e.stack, resolvedPath)
	}
}

func (e *instructionIncludeExpander) writeBounded(text string) {
	if e.truncated || text == "" {
		return
	}
	remaining := maxExpandedInstructionBytes - e.result.Len()
	if len(text) <= remaining {
		e.result.WriteString(text)
		return
	}
	const marker = "\n[... instruction content truncated ...]\n"
	prefixBytes := remaining - len(marker)
	if prefixBytes > 0 {
		prefix := text[:prefixBytes]
		for !utf8.ValidString(prefix) && len(prefix) > 0 {
			prefix = prefix[:len(prefix)-1]
		}
		e.result.WriteString(prefix)
	}
	if maxExpandedInstructionBytes-e.result.Len() >= len(marker) {
		e.result.WriteString(marker)
	}
	e.truncated = true
}

func parseInstructionInclude(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "@") || strings.HasPrefix(trimmed, "@[") {
		return "", false
	}
	include := strings.TrimSpace(strings.TrimPrefix(trimmed, "@"))
	return include, include != ""
}

func resolveInstructionIncludeCandidate(includePath, baseDir string) (string, bool, bool) {
	if includePath == "~" || strings.HasPrefix(includePath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, false
		}
		if includePath == "~" {
			return filepath.Clean(home), false, true
		}
		return filepath.Join(home, includePath[2:]), false, true
	}
	if filepath.IsAbs(includePath) {
		return filepath.Clean(includePath), false, true
	}
	// A no-slash "~foo" is deliberately just a relative filename. It never
	// aliases $HOME/foo and therefore cannot bypass containment.
	return filepath.Join(baseDir, includePath), true, true
}

func readInstructionFile(path, containmentRoot string) (string, string, error) {
	resolvedPath, err := canonicalInstructionFile(path)
	if err != nil {
		return "", "", err
	}
	if containmentRoot != "" && !pathContained(resolvedPath, containmentRoot) {
		return "", "", fmt.Errorf("instruction path escapes allowed root: %s", path)
	}

	file, err := openInstructionFile(resolvedPath, containmentRoot)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", errInstructionNotFile
	}
	data, err := io.ReadAll(io.LimitReader(file, maxInstructionFileBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > maxInstructionFileBytes {
		return "", "", errInstructionTooLarge
	}
	if !utf8.Valid(data) {
		return "", "", errInstructionNotUTF8
	}
	return string(data), resolvedPath, nil
}

func openInstructionFile(resolvedPath, containmentRoot string) (*os.File, error) {
	if containmentRoot == "" {
		info, err := os.Lstat(resolvedPath)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errInstructionNotFile
		}
		return os.Open(resolvedPath)
	}

	// os.Root is the actual security boundary, rather than the preceding path
	// comparison alone. On supported platforms it prevents a repository from
	// winning a symlink-swap race between containment validation and Open.
	root, err := os.OpenRoot(containmentRoot)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(containmentRoot, resolvedPath)
	if err != nil {
		root.Close()
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		root.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		root.Close()
		return nil, errInstructionNotFile
	}
	file, err := root.Open(rel)
	root.Close()
	return file, err
}

func canonicalInstructionFile(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Clean(abs))
}

func canonicalInstructionRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("instruction root is not a directory: %s", path)
	}
	return resolved, nil
}

func pathContained(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	return !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func addInstructionWatchPath(paths map[string]struct{}, path string) {
	if paths == nil || strings.TrimSpace(path) == "" || len(paths) >= maxInstructionWatchPaths {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return
	}
	paths[filepath.Clean(abs)] = struct{}{}
}

func instructionCandidateWatchable(path, containmentRoot string) bool {
	if containmentRoot == "" {
		return true
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)

	// EvalSymlinks requires the full target to exist. Walk up to the nearest
	// existing ancestor, resolve it, then re-append the missing suffix. This
	// proves containment for both existing and not-yet-created dependencies.
	probe := abs
	missing := make([]string, 0, 4)
	for {
		if resolved, resolveErr := filepath.EvalSymlinks(probe); resolveErr == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return pathContained(filepath.Clean(resolved), containmentRoot)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return false
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

// resolveContainedRelativeInclude canonicalizes a relative import and proves
// that its resolved target is inside baseDir. A string-prefix comparison is
// not a path boundary: `/repo-secrets` starts with `/repo`, and a symlink can
// create the same collision. EvalSymlinks errors fail closed so an unresolved
// target never weakens the containment check.
func resolveContainedRelativeInclude(includePath, baseDir string) (string, bool) {
	resolvedBase, err := canonicalInstructionRoot(baseDir)
	if err != nil {
		return "", false
	}
	resolvedInclude, err := canonicalInstructionFile(includePath)
	if err != nil || !pathContained(resolvedInclude, resolvedBase) {
		return "", false
	}
	return resolvedInclude, true
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
	return m.Load()
}

// StartWatching enables automatic reloading when instruction files change.
func (m *ProjectMemory) StartWatching(ctx context.Context, debounceMs int) error {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	m.mu.RLock()
	alreadyWatching := m.watcher != nil
	m.mu.RUnlock()
	if alreadyWatching {
		return nil // Already watching
	}

	watchPath, watchingDir := m.resolveInstructionWatchPath()

	watcherCtx, cancel := context.WithCancel(ctx)
	m.mu.RLock()
	generation := m.watchGeneration + 1
	m.mu.RUnlock()
	watcher, err := NewFileWatcher(watcherCtx, watchPath, debounceMs, func(path string) {
		m.handleInstructionFileChange(path, generation)
	})
	if err != nil {
		cancel()
		return err
	}
	m.mu.Lock()
	m.watcher = watcher
	m.extraWatchers = make(map[string]*FileWatcher)
	m.watcherCtx = watcherCtx
	m.watcherCancel = cancel
	m.watchDebounce = debounceMs
	m.watchGeneration = generation
	m.mu.Unlock()
	m.reconcileInstructionWatchersLocked()
	go func() {
		<-watcherCtx.Done()
		m.stopInstructionWatchers(generation)
	}()

	logging.Info("started watching instruction files", "path", watchPath, "directory_fallback", watchingDir)
	return nil
}

func (m *ProjectMemory) handleInstructionFileChange(path string, generation uint64) {
	m.mu.RLock()
	active := m.watchGenerationActiveLocked(generation)
	m.mu.RUnlock()
	if !active {
		return
	}
	logging.Info("instruction file changed, reloading", "path", path)
	committed, changed := m.loadAndCommit(generation)
	if !committed || !changed {
		return
	}
	m.mu.RLock()
	active = m.watchGenerationActiveLocked(generation)
	onReload := m.onReload
	source := m.sourcePath
	m.mu.RUnlock()
	if !active {
		return
	}
	logging.Info("✓ Reloaded project instructions", "source", source)
	if onReload != nil && m.claimActiveWatchCallback(generation) {
		onReload()
	}
}

// claimActiveWatchCallback closes the check/invoke scheduling gap as far as a
// callback without a lifetime token can: callbacks that have become stale are
// rejected immediately before invocation. StopWatching invalidates the
// generation under the same lock before returning, so debounce callbacks that
// start after stop/restart cannot reach onReload.
func (m *ProjectMemory) claimActiveWatchCallback(generation uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.watchGenerationActiveLocked(generation)
}

func (m *ProjectMemory) watchGenerationActiveLocked(generation uint64) bool {
	return generation != 0 &&
		m.watcher != nil &&
		m.watchGeneration == generation &&
		m.watcherCtx != nil &&
		m.watcherCtx.Err() == nil
}

// reconcileInstructionWatchers keeps one stable primary watcher (for backward
// compatibility and priority rebinding) plus bounded supplemental watchers for
// every layer and successfully resolved include dependency. This makes edits to
// GOKIN.local.md, rules, global/user files, and includes visible without restart.
func (m *ProjectMemory) reconcileInstructionWatchers() {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()
	m.reconcileInstructionWatchersLocked()
}

func (m *ProjectMemory) reconcileInstructionWatchersLocked() {
	m.mu.RLock()
	primary := m.watcher
	watcherCtx := m.watcherCtx
	debounceMs := m.watchDebounce
	generation := m.watchGeneration
	paths := append([]string(nil), m.watchPaths...)
	m.mu.RUnlock()
	if primary == nil || watcherCtx == nil {
		return
	}

	newPrimaryPath, _ := m.resolveInstructionWatchPath()
	if primary.Path() != newPrimaryPath {
		primary.UpdatePath(newPrimaryPath)
		logging.Debug("instruction watcher rebound", "path", newPrimaryPath)
	}
	primaryPath := filepath.Clean(newPrimaryPath)

	desired := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if path != primaryPath {
			desired[path] = struct{}{}
		}
	}

	m.mu.Lock()
	if m.extraWatchers == nil {
		m.extraWatchers = make(map[string]*FileWatcher)
	}
	existing := make(map[string]*FileWatcher, len(m.extraWatchers))
	for path, watcher := range m.extraWatchers {
		existing[path] = watcher
	}
	m.mu.Unlock()

	for path, watcher := range existing {
		if _, keep := desired[path]; keep {
			continue
		}
		m.mu.Lock()
		if m.extraWatchers[path] == watcher {
			delete(m.extraWatchers, path)
		}
		m.mu.Unlock()
		watcher.Close()
	}

	for path := range desired {
		m.mu.RLock()
		_, exists := m.extraWatchers[path]
		stillActive := m.watcher == primary && m.watcherCtx == watcherCtx
		m.mu.RUnlock()
		if exists || !stillActive {
			continue
		}
		watcher, err := NewFileWatcher(watcherCtx, path, debounceMs, func(changedPath string) {
			m.handleInstructionFileChange(changedPath, generation)
		})
		if err != nil {
			logging.Debug("supplemental instruction watcher not started", "path", path, "error", err)
			continue
		}
		m.mu.Lock()
		if m.watcher == primary && m.watcherCtx == watcherCtx {
			m.extraWatchers[path] = watcher
			watcher = nil
		}
		m.mu.Unlock()
		if watcher != nil {
			watcher.Close()
		}
	}
}

// resolveInstructionWatchPath finds the concrete instruction file to watch,
// falling back to the project directory itself when none exists yet (so a
// file created later is still detected). Returns (path, true) for the
// directory-fallback case, (path, false) once a real file is found — callers
// use the bool to know whether the watcher should later be rebound via
// FileWatcher.UpdatePath once a concrete file appears.
func (m *ProjectMemory) resolveInstructionWatchPath() (string, bool) {
	root, rootErr := canonicalInstructionRoot(m.workDir)
	for _, filename := range instructionFiles {
		path := filepath.Join(m.workDir, filename)
		if rootErr == nil && safeProjectInstructionPath(path, root) {
			return path, false
		}
	}
	projectMemoryPath := filepath.Join(m.workDir, ".gokin", "project-memory.md")
	if rootErr == nil && safeProjectInstructionPath(projectMemoryPath, root) {
		return projectMemoryPath, false
	}
	// No file found yet, watch project root so newly created GOKIN.md/CLAUDE.md
	// and .gokin/* files are detected without restart.
	return m.workDir, true
}

func safeProjectInstructionPath(path, root string) bool {
	resolved, err := canonicalInstructionFile(path)
	if err != nil || !pathContained(resolved, root) {
		return false
	}
	info, err := os.Stat(resolved)
	return err == nil && info.Mode().IsRegular()
}

// StopWatching disables automatic reloading.
func (m *ProjectMemory) StopWatching() {
	m.stopInstructionWatchers(0)
}

// stopInstructionWatchers stops the current generation. A non-zero expected
// generation is used by context-cancellation cleanup so an old context cannot
// tear down a newer StartWatching call.
func (m *ProjectMemory) stopInstructionWatchers(expectedGeneration uint64) {
	m.watchMu.Lock()
	defer m.watchMu.Unlock()

	m.mu.Lock()
	if expectedGeneration != 0 && m.watchGeneration != expectedGeneration {
		m.mu.Unlock()
		return
	}
	cancel := m.watcherCancel
	watcher := m.watcher
	extraWatchers := m.extraWatchers
	m.watcherCancel = nil
	m.watcher = nil
	m.watcherCtx = nil
	m.extraWatchers = nil
	m.watchGeneration++
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if watcher != nil {
		watcher.Close()
	}
	for _, extraWatcher := range extraWatchers {
		extraWatcher.Close()
	}
}

// OnReload sets a callback function to be called when instructions are reloaded.
func (m *ProjectMemory) OnReload(callback func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onReload = callback
}
