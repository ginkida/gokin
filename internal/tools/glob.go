package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"google.golang.org/genai"

	"gokin/internal/cache"
	"gokin/internal/git"
	"gokin/internal/security"
)

// GlobPredictorInterface defines the interface for context predictors used by glob.
type GlobPredictorInterface interface {
	RecordAccess(path, accessType, fromFile string)
}

// GlobTool finds files matching a glob pattern.
type GlobTool struct {
	workDir       string
	gitIgnore     *git.GitIgnore
	cache         *cache.SearchCache
	pathValidator *security.PathValidator
	predictor     GlobPredictorInterface
}

// NewGlobTool creates a new GlobTool instance.
func NewGlobTool(workDir string) *GlobTool {
	gitIgnore := git.NewGitIgnore(workDir)
	_ = gitIgnore.Load() // Ignore error - gitignore is optional

	return &GlobTool{
		workDir:       workDir,
		gitIgnore:     gitIgnore,
		pathValidator: security.NewPathValidator([]string{workDir}, false),
	}
}

// SetGitIgnore sets the gitignore instance for the tool.
func (t *GlobTool) SetGitIgnore(gi *git.GitIgnore) {
	t.gitIgnore = gi
}

// SetCache sets the search cache for the tool.
func (t *GlobTool) SetCache(c *cache.SearchCache) {
	t.cache = c
}

// SetAllowedDirs sets additional allowed directories for path validation.
func (t *GlobTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

// SetPredictor sets the context predictor for access pattern learning.
func (t *GlobTool) SetPredictor(p GlobPredictorInterface) {
	t.predictor = p
}

func (t *GlobTool) Name() string {
	return "glob"
}

func (t *GlobTool) Description() string {
	return `Finds files matching a glob pattern. Returns file paths sorted by modification time (newest first).

PARAMETERS:
- pattern (required): Glob pattern to match files
- path (optional): Directory to search in (default: current working directory)

PATTERN SYNTAX:
- *: Matches any characters except /
- **: Matches any characters including / (recursive)
- ?: Matches single character
- [abc]: Matches any character in brackets
- {a,b}: Matches either a or b

COMMON PATTERNS:
- "**/*.go" - All Go files recursively
- "**/*.{ts,tsx}" - All TypeScript files
- "src/**/*" - All files in src directory
- "**/test*" - All files starting with "test"
- "**/*_test.go" - All Go test files
- "config.*" - All config files (any extension)
- "**/main.*" - All main files at any depth

LIMITATIONS:
- Maximum 1000 results returned
- Gitignored files are excluded
- Directories are not included (files only)
- Sorted by modification time (newest first)

AFTER FINDING FILES - YOU MUST:
1. Summarize what types of files were found
2. Group files by category (source, tests, config, etc.)
3. Highlight important/relevant files
4. Suggest which files to read next
5. If no results, suggest alternative patterns`
}

func (t *GlobTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"pattern": {
					Type:        genai.TypeString,
					Description: "The glob pattern to match (e.g., '**/*.go', 'src/**/*.ts')",
				},
				"path": {
					Type:        genai.TypeString,
					Description: "The directory to search in. Defaults to current working directory.",
				},
			},
			Required: []string{"pattern"},
		},
	}
}

func (t *GlobTool) Validate(args map[string]any) error {
	pattern, ok := GetString(args, "pattern")
	if !ok || pattern == "" {
		return NewValidationError("pattern", "is required")
	}
	return nil
}

func (t *GlobTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	pattern, _ := GetString(args, "pattern")
	searchPath := GetStringDefault(args, "path", t.workDir)

	// Make path absolute if relative
	if !filepath.IsAbs(searchPath) {
		searchPath = filepath.Join(t.workDir, searchPath)
	}

	// Validate path if validator is configured
	if t.pathValidator != nil {
		validPath, err := t.pathValidator.ValidateDir(searchPath)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
		}
		searchPath = validPath
	}

	// Check cache first
	var cacheKey string
	if t.cache != nil {
		cacheKey = cache.GlobKey(pattern, searchPath)
		if cached, ok := t.cache.GetGlob(cacheKey); ok {
			// Return cached results
			if len(cached.Files) == 0 {
				return NewSuccessResult("(no matches)"), nil
			}
			relPaths := make([]string, len(cached.Files))
			for i, f := range cached.Files {
				relPath, err := filepath.Rel(t.workDir, f)
				if err != nil {
					relPath = f
				}
				relPaths[i] = relPath
			}
			var builder strings.Builder
			fmt.Fprintf(&builder, "Found %d file(s) matching '%s' (cached):\n", len(cached.Files), pattern)
			builder.WriteString(actionableGlobSummary(relPaths))
			for _, p := range relPaths {
				builder.WriteString(p)
				builder.WriteString("\n")
			}
			return NewSuccessResult(builder.String()), nil
		}
	}

	// Check if search path exists
	if _, err := os.Stat(searchPath); err != nil {
		if os.IsNotExist(err) {
			return NewErrorResult(fmt.Sprintf("path not found: %s", searchPath)), nil
		}
		return NewErrorResult(fmt.Sprintf("error accessing path: %s", err)), nil
	}

	// Build full pattern
	fullPattern := filepath.Join(searchPath, pattern)

	// Find matches
	matches, err := doublestar.FilepathGlob(fullPattern)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("invalid pattern: %s", err)), nil
	}

	// Filter out directories, gitignored files, and sort by modification time
	type fileInfo struct {
		path    string
		modTime int64
	}
	var files []fileInfo

	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			// Filter by gitignore
			if t.gitIgnore != nil && t.gitIgnore.IsIgnored(match) {
				continue
			}
			files = append(files, fileInfo{
				path:    match,
				modTime: info.ModTime().Unix(),
			})
		}
	}

	// Rank by relevance first (non-test/non-vendor/non-generated files
	// outrank test/vendor hits even if the latter are newer). mtime
	// stays as the tiebreaker so recently-touched files still float to
	// the top within a tier.
	sort.SliceStable(files, func(i, j int) bool {
		si := fileRelevanceScore(files[i].path, 1)
		sj := fileRelevanceScore(files[j].path, 1)
		if si != sj {
			return si > sj
		}
		if files[i].modTime != files[j].modTime {
			return files[i].modTime > files[j].modTime
		}
		return files[i].path < files[j].path
	})

	// Limit results
	const maxResults = 1000
	totalFound := len(files)
	if len(files) > maxResults {
		files = files[:maxResults]
	}

	// Cache the results
	if t.cache != nil && cacheKey != "" {
		filePaths := make([]string, len(files))
		for i, f := range files {
			filePaths[i] = f.path
		}
		t.cache.SetGlob(cacheKey, cache.GlobResult{
			Files: filePaths,
		})
	}

	// Build output
	if len(files) == 0 {
		return NewSuccessResult("(no matches)"), nil
	}

	// Collect relative paths for summary
	relPaths := make([]string, len(files))
	for i, f := range files {
		relPath, err := filepath.Rel(t.workDir, f.path)
		if err != nil {
			relPath = f.path
		}
		relPaths[i] = relPath

		// Record access pattern for predictive loading
		if t.predictor != nil {
			t.predictor.RecordAccess(f.path, "glob", "")
		}
	}

	var builder strings.Builder

	// Summary header
	displayCount := len(files)
	capped := totalFound > maxResults
	if capped {
		fmt.Fprintf(&builder, "Found %d+ files matching '%s' (showing %d):\n", totalFound, pattern, maxResults)
	} else {
		fmt.Fprintf(&builder, "Found %d file(s) matching '%s':\n", displayCount, pattern)
	}

	// Actionable summary: group by category
	builder.WriteString(actionableGlobSummary(relPaths))

	// File list
	for _, p := range relPaths {
		builder.WriteString(p)
		builder.WriteString("\n")
	}

	return NewSuccessResult(builder.String()), nil
}

// actionableGlobSummary groups files by category and provides a "Next:" hint.
// Mirrors the actionableGrepSummary pattern — turns a raw file listing into
// something the model can act on without re-parsing the whole list.
func actionableGlobSummary(paths []string) string {
	if len(paths) == 0 {
		return ""
	}

	// Group by category
	var (
		source []string
		tests  []string
		config []string
		docs   []string
		other  []string
	)
	for _, p := range paths {
		switch {
		case isTestPath(p):
			tests = append(tests, p)
		case isConfigPath(p):
			config = append(config, p)
		case isDocPath(p):
			docs = append(docs, p)
		case isVendorPath(p) || isGeneratedPath(p):
			other = append(other, p)
		default:
			source = append(source, p)
		}
	}

	var b strings.Builder
	b.WriteString("Actionable summary:\n")

	// Per-category counts with top items
	printCategory(&b, "- Source files", source, 3)
	printCategory(&b, "- Test files", tests, 2)
	printCategory(&b, "- Config files", config, 2)
	printCategory(&b, "- Docs", docs, 2)
	printCategory(&b, "- Other (vendor/generated)", other, 0)

	// Next hint
	b.WriteString("- Next: ")
	switch {
	case len(source) > 0:
		b.WriteString("read the most relevant source file(s) to understand the code before editing.\n")
	case len(tests) > 0:
		b.WriteString("read a test file to understand expected behaviour, then find the corresponding source.\n")
	case len(config) > 0:
		b.WriteString("read a config file to understand the project setup.\n")
	default:
		b.WriteString("read a file to understand its contents before editing.\n")
	}
	b.WriteString("\n")
	return b.String()
}

func printCategory(b *strings.Builder, label string, files []string, showTop int) {
	if len(files) == 0 {
		return
	}
	if showTop == 0 {
		fmt.Fprintf(b, "%s: %d file(s)\n", label, len(files))
		return
	}
	if len(files) <= showTop {
		fmt.Fprintf(b, "%s: %d — %s\n", label, len(files), strings.Join(files, ", "))
	} else {
		fmt.Fprintf(b, "%s: %d — %s, ...\n", label, len(files), strings.Join(files[:showTop], ", "))
	}
}

// isConfigPath flags configuration files.
func isConfigPath(path string) bool {
	lower := strings.ToLower(path)
	base := lower
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	// Common config file names
	configNames := []string{
		"dockerfile", "docker-compose.yml", "docker-compose.yaml",
		"makefile", "gnumakefile",
		".env", ".env.example", ".envrc",
		"package.json", "tsconfig.json", "jsconfig.json",
		"pyproject.toml", "setup.cfg", "setup.py",
		"go.mod", "go.sum",
		"cargo.toml", "cargo.lock",
		"cmakelists.txt",
	}
	for _, name := range configNames {
		if base == name {
			return true
		}
	}
	// Config extensions
	configExts := []string{".yml", ".yaml", ".toml", ".ini", ".cfg", ".conf", ".json", ".xml"}
	for _, ext := range configExts {
		if strings.HasSuffix(base, ext) {
			// But exclude test fixtures and data files that happen to have these extensions
			if strings.Contains(lower, "/testdata/") || strings.Contains(lower, "/fixtures/") {
				return false
			}
			return true
		}
	}
	// Config directories
	return strings.Contains(lower, "/.github/") || strings.Contains(lower, "/.gokin/")
}

// isDocPath flags documentation files.
func isDocPath(path string) bool {
	lower := strings.ToLower(path)
	base := lower
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	docExts := []string{".md", ".mdx", ".rst", ".txt", ".adoc", ".asciidoc"}
	for _, ext := range docExts {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	// Doc directory patterns
	docDirs := []string{"/docs/", "/doc/", "/documentation/", "/readme"}
	for _, d := range docDirs {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return strings.HasPrefix(base, "readme") || strings.HasPrefix(base, "changelog") ||
		strings.HasPrefix(base, "contributing") || strings.HasPrefix(base, "license")
}
