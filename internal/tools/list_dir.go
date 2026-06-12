package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/genai"

	"gokin/internal/security"
)

const (
	// maxListDirEntries limits directory listing to prevent API payload overflow.
	maxListDirEntries = 2000
)

// ListDirTool lists the contents of a directory.
type ListDirTool struct {
	baseDir       string
	pathValidator *security.PathValidator
}

// NewListDirTool creates a new ListDirTool instance.
func NewListDirTool(baseDir string) *ListDirTool {
	return &ListDirTool{
		baseDir:       baseDir,
		pathValidator: security.NewPathValidator([]string{baseDir}, false),
	}
}

// SetAllowedDirs sets additional allowed directories for path validation.
func (t *ListDirTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.baseDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

func (t *ListDirTool) Name() string {
	return "list_dir"
}

func (t *ListDirTool) Description() string {
	return "Lists the contents of a directory, including files and subdirectories."
}

func (t *ListDirTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"directory_path": {
					Type:        genai.TypeString,
					Description: "The path to the directory to list (relative to the working directory or absolute). Defaults to current directory if empty or not provided.",
				},
			},
			Required: []string{}, // Not required - defaults to current directory
		},
	}
}

func (t *ListDirTool) Validate(args map[string]any) error {
	// Allow empty directory_path - will default to current directory
	return nil
}

func (t *ListDirTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	dirPath, _ := GetString(args, "directory_path")

	// Default to current directory if empty
	if dirPath == "" {
		dirPath = "."
	}

	// If path is relative, make it absolute based on baseDir
	absPath := dirPath
	if !filepath.IsAbs(dirPath) {
		absPath = filepath.Join(t.baseDir, dirPath)
	}

	// Validate path if validator is configured
	if t.pathValidator != nil {
		validPath, err := t.pathValidator.ValidateDir(absPath)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
		}
		absPath = validPath
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewErrorResult(fmt.Sprintf("directory not found: %s", dirPath)), nil
		}
		return NewErrorResult(fmt.Sprintf("error reading directory: %s", err)), nil
	}

	if len(entries) == 0 {
		return NewSuccessResult("(empty)"), nil
	}

	truncated := false
	if len(entries) > maxListDirEntries {
		truncated = true
		entries = entries[:maxListDirEntries]
	}

	// Sort: dirs first, then files, alpha within each group
	sort.SliceStable(entries, func(i, j int) bool {
		iDir, jDir := entries[i].IsDir(), entries[j].IsDir()
		if iDir != jDir {
			return iDir // dirs before files
		}
		return entries[i].Name() < entries[j].Name()
	})

	// Count dirs vs files
	var dirs, files []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name()+"/")
		} else {
			files = append(files, entry.Name())
		}
	}

	// Relative path for display
	displayPath := dirPath
	if filepath.IsAbs(dirPath) {
		if rel, err := filepath.Rel(t.baseDir, dirPath); err == nil && rel != "" {
			displayPath = rel
		}
	}

	var builder strings.Builder

	// Summary header
	if truncated {
		fmt.Fprintf(&builder, "%s: %d+ entries (showing %d — %d dirs, %d files):\n",
			displayPath, len(entries)+10 /* rough lower bound */, maxListDirEntries, len(dirs), len(files))
	} else {
		fmt.Fprintf(&builder, "%s: %d entries (%d dirs, %d files):\n",
			displayPath, len(dirs)+len(files), len(dirs), len(files))
	}

	// Actionable summary
	builder.WriteString(actionableListDirSummary(dirs, files, displayPath))

	// Directory listing (dirs first)
	for _, d := range dirs {
		builder.WriteString(d)
		builder.WriteByte('\n')
	}
	for _, f := range files {
		builder.WriteString(f)
		builder.WriteByte('\n')
	}

	if truncated {
		fmt.Fprintf(&builder, "\n... (output truncated: showing %d entries)", maxListDirEntries)
	}

	return NewSuccessResult(builder.String()), nil
}

// actionableListDirSummary provides a structured summary of a directory listing.
// Mirrors the actionableGrepSummary/actionableGlobSummary pattern.
func actionableListDirSummary(dirs, files []string, displayPath string) string {
	if len(dirs) == 0 && len(files) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Actionable summary:\n")

	// Top subdirectories
	if len(dirs) > 0 {
		top := dirs
		if len(top) > 5 {
			top = top[:5]
		}
		fmt.Fprintf(&b, "- %d subdir(s): %s\n", len(dirs), strings.Join(top, ", "))
	}

	// Key files (non-dotfiles first, then dotfiles)
	if len(files) > 0 {
		var keyFiles []string
		for _, f := range files {
			if !strings.HasPrefix(f, ".") {
				keyFiles = append(keyFiles, f)
			}
		}
		if len(keyFiles) == 0 {
			keyFiles = files // all dotfiles — show them
		}
		top := keyFiles
		if len(top) > 5 {
			top = top[:5]
		}
		fmt.Fprintf(&b, "- %d file(s): %s", len(files), strings.Join(top, ", "))
		if len(top) < len(files) {
			b.WriteString(", ...")
		}
		b.WriteString("\n")
	}

	// Next hint
	b.WriteString("- Next: ")
	switch {
	case len(dirs) > 0 && displayPath == ".":
		b.WriteString("cd into a subdirectory or read a key file (e.g. README.md, go.mod, package.json) to understand the project.\n")
	case len(dirs) > 0:
		b.WriteString("list a subdirectory or read a key file to understand this component.\n")
	default:
		b.WriteString("read the most relevant file to understand its contents before editing.\n")
	}
	b.WriteString("\n")
	return b.String()
}
