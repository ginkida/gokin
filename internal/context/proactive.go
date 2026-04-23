package context

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProactiveReader surfaces sibling / test-paired files alongside a just-
// read source file so the model gets package-level context without having
// to spend a tool call per neighbour.
//
// Heuristics, by priority (first few applied until the budget fills up):
//  1. Paired test file (`foo.go` ↔ `foo_test.go`, or `foo.py` ↔ `test_foo.py`)
//  2. Sibling files in the same directory with the same extension,
//     sorted alphabetically. We skip the file that was just read and
//     any file that's already been read in this session (tracked by
//     the caller via the `alreadyRead` set) so we never duplicate.
//
// What we DON'T do on purpose (scope kept minimal):
//   - Follow imports / "go to definition" — too language-specific,
//     needs parser per language, high maintenance cost. The package-
//     siblings heuristic covers ~80% of "what's around this file" for
//     free, no AST required.
//   - Read entire files — 40 lines of preview is enough for the model
//     to learn names/shape without blowing the context window.
//   - Chase nested directories — siblings only, direct children.
type ProactiveReader struct {
	workDir         string
	maxFiles        int
	maxLinesPerFile int
}

// NewProactiveReader constructs a reader with defensive defaults when the
// caller passes zero values (e.g. fresh Config with missing section).
func NewProactiveReader(workDir string, maxFiles, maxLinesPerFile int) *ProactiveReader {
	if maxFiles <= 0 {
		maxFiles = 3
	}
	if maxLinesPerFile <= 0 {
		maxLinesPerFile = 40
	}
	return &ProactiveReader{
		workDir:         workDir,
		maxFiles:        maxFiles,
		maxLinesPerFile: maxLinesPerFile,
	}
}

// RelatedFile is one entry in the proactive-context block appended to a
// Read result.
type RelatedFile struct {
	Path    string // absolute path
	Preview string // first maxLinesPerFile lines
	Reason  string // e.g. "paired test", "package sibling"
}

// RelatedFor returns related files for the given source path. alreadyRead
// is a set of file paths the agent has already consumed this session —
// we skip those to avoid duplicating context. Returns nil when nothing
// relevant is found (new file, orphan script, directory we can't read).
func (p *ProactiveReader) RelatedFor(path string, alreadyRead map[string]bool) []RelatedFile {
	if path == "" {
		return nil
	}
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(p.workDir, path)
	}
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	ext := filepath.Ext(base)
	if ext == "" {
		return nil // no extension → probably not a source file
	}

	// Short-circuit: only source-ish extensions get enriched. Reading a
	// .md / .json / .yaml doesn't have meaningful "package siblings".
	if !isSourceExtension(ext) {
		return nil
	}

	// Collect candidates.
	var candidates []RelatedFile
	seen := map[string]bool{absPath: true} // don't re-include the file itself

	// 1. Paired test file first (strongest signal).
	if paired := pairedTestPath(absPath); paired != "" && !seen[paired] {
		if !alreadyRead[paired] {
			if preview, ok := readPreview(paired, p.maxLinesPerFile); ok {
				candidates = append(candidates, RelatedFile{
					Path:    paired,
					Preview: preview,
					Reason:  "paired test",
				})
				seen[paired] = true
			}
		}
	}

	// 2. Package siblings — same directory, same extension, excluding
	// already-seen ones and the original file.
	entries, err := os.ReadDir(dir)
	if err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if filepath.Ext(name) != ext {
				continue
			}
			if name == base {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if len(candidates) >= p.maxFiles {
				break
			}
			sibPath := filepath.Join(dir, name)
			if seen[sibPath] || alreadyRead[sibPath] {
				continue
			}
			preview, ok := readPreview(sibPath, p.maxLinesPerFile)
			if !ok {
				continue
			}
			candidates = append(candidates, RelatedFile{
				Path:    sibPath,
				Preview: preview,
				Reason:  "package sibling",
			})
			seen[sibPath] = true
		}
	}

	// Cap to max — paired-test may have used slot 1; siblings fill the rest.
	if len(candidates) > p.maxFiles {
		candidates = candidates[:p.maxFiles]
	}
	if len(candidates) == 0 {
		return nil
	}
	return candidates
}

// RelatedBlock implements tools.ProactiveContextProvider. Returns the
// pre-formatted markdown block to append to a Read result, or "" when
// nothing's worth including. Matches the small interface declared on
// the tools side so we don't need an import cycle.
func (p *ProactiveReader) RelatedBlock(readPath string, alreadyRead map[string]bool) string {
	files := p.RelatedFor(readPath, alreadyRead)
	return FormatBlock(files, p.workDir)
}

// FormatBlock renders RelatedFor's output as a compact markdown block that
// can be appended to a Read tool's result. Callers pass this back to the
// agent as part of the tool response content.
func FormatBlock(files []RelatedFile, workDir string) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n--- Related files in this package ---\n")
	b.WriteString("(auto-included so you don't need extra read calls; use `read` for full content)\n\n")
	for _, f := range files {
		rel := f.Path
		if workDir != "" {
			if r, err := filepath.Rel(workDir, f.Path); err == nil && !strings.HasPrefix(r, "..") {
				rel = r
			}
		}
		fmt.Fprintf(&b, "### %s  (%s)\n```\n%s\n```\n\n", rel, f.Reason, f.Preview)
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// isSourceExtension returns true for extensions where "package siblings"
// actually make sense. Configs, docs, data files don't get enriched —
// they stand alone.
func isSourceExtension(ext string) bool {
	switch strings.ToLower(ext) {
	case ".go",
		".py",
		".ts", ".tsx", ".js", ".jsx",
		".rs",
		".java", ".kt",
		".rb",
		".swift",
		".c", ".h", ".cpp", ".cc", ".hpp":
		return true
	}
	return false
}

// pairedTestPath returns the test-file sibling for a source file, or ""
// when no convention matches. We handle the two most common patterns:
//
//	Go / Rust / Swift:  foo.go      → foo_test.go
//	Python / pytest:    foo.py      → test_foo.py
//	JS/TS (Jest, etc.): foo.ts      → foo.test.ts
//
// Returns absolute paths only. Does not stat the file — caller does the
// readiness check via readPreview.
func pairedTestPath(srcPath string) string {
	dir := filepath.Dir(srcPath)
	base := filepath.Base(srcPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	// Already a test file → no further pairing needed.
	if strings.HasSuffix(name, "_test") ||
		strings.HasPrefix(name, "test_") ||
		strings.HasSuffix(name, ".test") {
		return ""
	}

	var candidates []string
	switch ext {
	case ".go", ".rs", ".swift":
		candidates = []string{filepath.Join(dir, name+"_test"+ext)}
	case ".py":
		candidates = []string{filepath.Join(dir, "test_"+name+ext)}
	case ".ts", ".tsx", ".js", ".jsx":
		candidates = []string{
			filepath.Join(dir, name+".test"+ext),
			filepath.Join(dir, name+".spec"+ext),
		}
	default:
		return ""
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// readPreview returns the first n lines of path (at most), or (nil, false)
// if the file is unreadable or empty. Falls back to returning the whole
// file verbatim when it's shorter than n lines.
func readPreview(path string, maxLines int) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if len(data) == 0 {
		return "", false
	}
	// Soft cap on bytes too — defensive against binary files sneaking in
	// via weird extensions (e.g. a vendored `.so.go`-named artifact).
	if len(data) > 64*1024 {
		data = data[:64*1024]
	}
	lines := strings.SplitN(string(data), "\n", maxLines+1)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, "\n"), true
}
