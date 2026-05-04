package tools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gokin/internal/logging"
	"gokin/internal/security"
	"gokin/internal/tools/readers"

	"google.golang.org/genai"
)

const (
	// DefaultChunkSize is the default number of lines to read per chunk.
	DefaultChunkSize = 1000
	// LargeFileSizeMB is the threshold for considering a file "large".
	LargeFileSizeMB = 10
)

// ChunkedReader provides efficient reading of large files in chunks.
type ChunkedReader struct {
	filePath    string
	totalLines  int
	currentLine int
	chunkSize   int
	file        *os.File
	scanner     *bufio.Scanner
	initialized bool
}

// NewChunkedReader creates a new chunked reader for large files.
func NewChunkedReader(path string, chunkSize int) (*ChunkedReader, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}

	// First, count total lines
	totalLines, err := countLines(path)
	if err != nil {
		return nil, fmt.Errorf("failed to count lines: %w", err)
	}

	return &ChunkedReader{
		filePath:   path,
		totalLines: totalLines,
		chunkSize:  chunkSize,
	}, nil
}

// countLines counts the total number of lines in a file efficiently.
func countLines(filePath string) (int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	buf := make([]byte, 32*1024)
	count := 0
	lineSep := []byte{'\n'}

	for {
		c, err := file.Read(buf)
		count += countBytes(buf[:c], lineSep[0])

		if err == io.EOF {
			// Add 1 if file doesn't end with newline
			if c > 0 && buf[c-1] != '\n' {
				count++
			}
			break
		}
		if err != nil {
			return 0, err
		}
	}

	return count, nil
}

// countBytes counts occurrences of a byte in a buffer.
func countBytes(buf []byte, b byte) int {
	count := 0
	for _, c := range buf {
		if c == b {
			count++
		}
	}
	return count
}

// TotalLines returns the total number of lines in the file.
func (r *ChunkedReader) TotalLines() int {
	return r.totalLines
}

// SeekToLine positions the reader at the specified line number (1-indexed).
func (r *ChunkedReader) SeekToLine(lineNum int) error {
	if lineNum < 1 {
		lineNum = 1
	}

	// Open new file first to ensure we can read it
	file, err := os.Open(r.filePath)
	if err != nil {
		return err
	}

	// Close existing file if open (after successful open of new file)
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			logging.Warn("error closing file", "path", r.filePath, "error", err)
		}
	}

	r.file = file

	// Skip lines until we reach the target
	r.scanner = bufio.NewScanner(file)
	r.scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for i := 1; i < lineNum && r.scanner.Scan(); i++ {
		// Just skip
	}

	r.currentLine = lineNum
	r.initialized = true
	return nil
}

// NextChunk reads the next chunk of lines.
// Returns the lines, starting line number, whether there are more lines, and any error.
func (r *ChunkedReader) NextChunk() (lines []string, startLine int, hasMore bool, err error) {
	if !r.initialized {
		if err := r.SeekToLine(1); err != nil {
			return nil, 0, false, err
		}
	}

	startLine = r.currentLine
	lines = make([]string, 0, r.chunkSize)

	for i := 0; i < r.chunkSize && r.scanner.Scan(); i++ {
		lines = append(lines, r.scanner.Text())
		r.currentLine++
	}

	if err := r.scanner.Err(); err != nil {
		return lines, startLine, false, err
	}

	hasMore = r.currentLine <= r.totalLines
	return lines, startLine, hasMore, nil
}

// Close closes the chunked reader and releases resources.
func (r *ChunkedReader) Close() error {
	if r.file != nil {
		return r.file.Close()
	}
	return nil
}

// ContextPredictorInterface defines the interface for context predictors.
type ContextPredictorInterface interface {
	RecordAccess(path, accessType, fromFile string)
	LearnImports(filePath string)
}

// ProactiveContextProvider is an optional dependency that returns a block of
// related-file previews to append to a successful Read result. Passing a
// non-nil value is the opt-in: a nil provider leaves Read output unchanged.
// The concrete implementation lives in internal/context to avoid an import
// cycle (tools → context), but the interface is defined here for clarity.
type ProactiveContextProvider interface {
	// RelatedBlock returns pre-formatted markdown to append to the Read
	// result, or "" when nothing's worth including. Receives the absolute
	// path that was just read and a set of already-read files in the
	// session (skip these to avoid duplication).
	RelatedBlock(readPath string, alreadyRead map[string]bool) string
}

// ReadTool reads files and returns their contents with line numbers.
type ReadTool struct {
	notebookReader *readers.NotebookReader
	imageReader    *readers.ImageReader
	pdfReader      *readers.PDFReader
	workDir        string
	pathValidator  *security.PathValidator
	predictor      ContextPredictorInterface
	proactive      ProactiveContextProvider
	lastReadMu     sync.Mutex
	lastReadFile   string // Track last file read for co-access learning
}

// NewReadTool creates a new ReadTool instance with the given working directory.
// If workDir is empty, no path validation is performed (not recommended for production).
func NewReadTool(workDir string) *ReadTool {
	t := &ReadTool{
		workDir:        workDir,
		notebookReader: readers.NewNotebookReader(),
		imageReader:    readers.NewImageReader(),
		pdfReader:      readers.NewPDFReader(),
	}
	if workDir != "" {
		t.pathValidator = security.NewPathValidator([]string{workDir}, false)
	}
	return t
}

// SetWorkDir sets the working directory and initializes path validator.
func (t *ReadTool) SetWorkDir(workDir string) {
	t.workDir = workDir
	t.pathValidator = security.NewPathValidator([]string{workDir}, false)
}

// SetProactiveContext installs the optional related-files appender. Pass
// nil to disable (e.g. in minimal test harnesses).
func (t *ReadTool) SetProactiveContext(p ProactiveContextProvider) {
	t.proactive = p
}

// SetAllowedDirs sets additional allowed directories for path validation.
func (t *ReadTool) SetAllowedDirs(dirs []string) {
	allDirs := append([]string{t.workDir}, dirs...)
	t.pathValidator = security.NewPathValidator(allDirs, false)
}

// SetPredictor sets the context predictor for access pattern learning.
func (t *ReadTool) SetPredictor(p ContextPredictorInterface) {
	t.predictor = p
}

func (t *ReadTool) Name() string {
	return "read"
}

func (t *ReadTool) Description() string {
	return `Reads a file from the filesystem and returns its contents with line numbers.

PARAMETERS:
- file_path (required): Absolute path to the file to read
- offset (optional): Line number to start reading from (1-indexed, default: 1)
- limit (optional): Maximum number of lines to read (default: 2000)

SUPPORTED FORMATS:
- Text files: Returns content with line numbers (cat -n style)
- PDF files (.pdf): Extracts and returns text content
- Images (.png, .jpg, .gif, etc.): Returns image metadata and can be analyzed
- Jupyter notebooks (.ipynb): Returns all cells with outputs

LIMITATIONS:
- Lines longer than 2000 characters are truncated
- Large files (>10MB) are read in chunks; use offset to continue reading
- Maximum 2000 lines per request (use offset for more)

USAGE TIPS:
- Always read files BEFORE editing them
- Use offset/limit for large files
- Check file exists before reading (use glob to find files first)

AFTER READING - YOU MUST:
1. Explain what the file contains
2. Highlight key sections with line numbers
3. Relate findings to the user's question`
}

func (t *ReadTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"file_path": {
					Type:        genai.TypeString,
					Description: "The absolute path to the file to read",
				},
				"offset": {
					Type:        genai.TypeInteger,
					Description: "The line number to start reading from (1-indexed). Optional.",
				},
				"limit": {
					Type:        genai.TypeInteger,
					Description: "The maximum number of lines to read. Optional, defaults to 2000.",
				},
			},
			Required: []string{"file_path"},
		},
	}
}

func (t *ReadTool) Validate(args map[string]any) error {
	filePath, ok := GetString(args, "file_path")
	if !ok || filePath == "" {
		return NewValidationError("file_path", "is required")
	}
	return nil
}

func (t *ReadTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	filePath, _ := GetString(args, "file_path")

	// Validate path (mandatory for security)
	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}

	validPath, err := t.pathValidator.ValidateFile(filePath)
	if err != nil {
		// Path-validation usually fires when a model fabricates an
		// absolute path outside the workdir (classic Kimi hallucination
		// mode). Suggest same-basename files under the workdir so it
		// can recover with one corrected read instead of a flailing
		// grep. Also surface the workdir so the model sees where its
		// assumption broke.
		errMsg := fmt.Sprintf("path validation failed: %s\n(working directory: %s)",
			err, t.workDir)
		if suggestions := suggestFilesInWorkDir(t.workDir, filepath.Base(filePath)); len(suggestions) > 0 {
			errMsg += "\n\nFiles with matching basename in the workdir:\n" + strings.Join(suggestions, "\n")
		}
		return NewErrorResult(errMsg), nil
	}
	filePath = validPath

	// Check if file exists
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			errMsg := fmt.Sprintf("file not found: %s", filePath)
			// Same-directory neighbours first (covers typos in the
			// filename), workdir-wide basename match second (covers
			// wrong-directory cases the first pass misses).
			if suggestions := suggestSimilarFiles(filePath); len(suggestions) > 0 {
				errMsg += "\n\nDid you mean:\n" + strings.Join(suggestions, "\n")
			} else if suggestions := suggestFilesInWorkDir(t.workDir, filepath.Base(filePath)); len(suggestions) > 0 {
				errMsg += "\n\nFiles with matching basename in the workdir:\n" + strings.Join(suggestions, "\n")
			}
			return NewErrorResult(errMsg), nil
		}
		// Non-IsNotExist stat errors (permission denied, I/O errors).
		// Still worth trying same-dir suggestions — a permission error
		// on one file doesn't imply the dir is unreadable.
		errMsg := fmt.Sprintf("error accessing file: %s", err)
		if suggestions := suggestSimilarFiles(filePath); len(suggestions) > 0 {
			errMsg += "\n\nNearby files:\n" + strings.Join(suggestions, "\n")
		}
		return NewErrorResult(errMsg), nil
	}

	if info.IsDir() {
		return NewErrorResult(fmt.Sprintf("%s is a directory, not a file", filePath)), nil
	}

	// Route based on file extension
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".pdf":
		return t.readPDF(filePath)
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".ico", ".tiff", ".tif":
		return t.readImage(filePath)
	case ".ipynb":
		return t.readNotebook(filePath)
	default:
		// Detect binary files by extension or null bytes in first 512
		// bytes. Return as ERROR (not success) so the model treats this
		// as a signal to do something else — previously Success=true
		// with a "Binary file" message caused Kimi to believe it had
		// successfully read the file and make confident claims based on
		// the (nonexistent) contents.
		if isBinaryFile(filePath) {
			return NewErrorResult(fmt.Sprintf(
				"Binary file: %s (%s, %d bytes). This tool returns text only. If you need the content, run a command that emits text (e.g. `strings`, `hexdump -C | head`, a decoder). Do not claim to have read this file.",
				filePath, ext, info.Size(),
			)), nil
		}

		// Check if file is large
		isLarge := info.Size() > LargeFileSizeMB*1024*1024
		if isLarge {
			return t.readLargeFile(ctx, filePath, args)
		}
		return t.readText(ctx, filePath, args)
	}
}

// readLargeFile reads a large file using chunked streaming.
func (t *ReadTool) readLargeFile(ctx context.Context, filePath string, args map[string]any) (ToolResult, error) {
	offset := GetIntDefault(args, "offset", 1)
	limit := GetIntDefault(args, "limit", DefaultChunkSize)

	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = DefaultChunkSize
	}

	// Create chunked reader
	reader, err := NewChunkedReader(filePath, limit)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error creating chunked reader: %s", err)), nil
	}
	defer reader.Close()

	// Seek to offset
	if err := reader.SeekToLine(offset); err != nil {
		return NewErrorResult(fmt.Sprintf("error seeking to line %d: %s", offset, err)), nil
	}

	// Read chunk
	lines, startLine, hasMore, err := reader.NextChunk()
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error reading chunk: %s", err)), nil
	}

	// Format output
	var builder strings.Builder
	maxLineLen := 2000

	for i, line := range lines {
		lineNum := startLine + i
		if runes := []rune(line); len(runes) > maxLineLen {
			line = string(runes[:maxLineLen]) + "..."
		}
		builder.WriteString(fmt.Sprintf("%6d\t%s\n", lineNum, line))
	}

	content := builder.String()
	if content == "" {
		content = "(empty file or reached end of file)"
	}

	// Add metadata about the large file
	metadata := map[string]any{
		"type":        "large_file",
		"total_lines": reader.TotalLines(),
		"start_line":  startLine,
		"lines_read":  len(lines),
		"has_more":    hasMore,
		"file_path":   filePath,
	}

	// Add hint if there's more content
	if hasMore {
		hint := fmt.Sprintf("\n[Large file: showing lines %d-%d of %d total. Use offset=%d to continue reading.]\n",
			startLine, startLine+len(lines)-1, reader.TotalLines(), startLine+len(lines))
		content = hint + content
	} else {
		hint := fmt.Sprintf("\n[Large file: showing lines %d-%d of %d total (end of file).]\n",
			startLine, startLine+len(lines)-1, reader.TotalLines())
		content = hint + content
	}

	return NewSuccessResultWithData(content, metadata), nil
}

// readPDF reads a PDF file and extracts text.
func (t *ReadTool) readPDF(filePath string) (ToolResult, error) {
	content, err := t.pdfReader.Read(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error reading PDF: %s", err)), nil
	}

	return NewSuccessResultWithData(content, map[string]any{
		"type":      "pdf",
		"file_path": filePath,
	}), nil
}

// readImage reads an image file and returns metadata plus multimodal data for LLM vision.
func (t *ReadTool) readImage(filePath string) (ToolResult, error) {
	result, err := t.imageReader.Read(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error reading image: %s", err)), nil
	}

	displayText := fmt.Sprintf("[Image: %s, %d bytes]\nMIME Type: %s\nFile: %s",
		result.MimeType, result.Size, result.MimeType, filePath)

	tr := NewSuccessResultWithData(displayText, map[string]any{
		"type":      "image",
		"mime_type": result.MimeType,
		"size":      result.Size,
		"file_path": filePath,
	})
	tr.MultimodalParts = []*MultimodalPart{{
		MimeType: result.MimeType,
		Data:     result.Data,
	}}
	return tr, nil
}

// readNotebook reads a Jupyter notebook file.
func (t *ReadTool) readNotebook(filePath string) (ToolResult, error) {
	content, err := t.notebookReader.Read(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error reading notebook: %s", err)), nil
	}

	return NewSuccessResultWithData(content, map[string]any{
		"type":      "notebook",
		"file_path": filePath,
	}), nil
}

// readText reads a regular text file with line numbers.
func (t *ReadTool) readText(ctx context.Context, filePath string, args map[string]any) (ToolResult, error) {
	offset := GetIntDefault(args, "offset", 1)
	limit := GetIntDefault(args, "limit", 2000)

	// Ensure offset is at least 1
	if offset < 1 {
		offset = 1
	}

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("error opening file: %s", err)), nil
	}
	defer file.Close()

	// Read lines with line numbers
	var builder strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer for long lines

	lineNum := 0
	linesRead := 0
	maxLineLen := 2000
	// Tracks whether the scanner saw any further line past `limit`. Without
	// this, a file that has exactly `limit` lines and a file with 10× that
	// count produce indistinguishable output — the model cannot tell
	// whether to keep paginating. The footer below uses this to emit a
	// truthful "file has more" hint.
	hasMoreAfterLimit := false
	// Last line number observed by the scanner, even after the read loop
	// exits early. Used below to surface the total-lines hint without
	// re-opening the file.
	lastScannedLine := 0

	for scanner.Scan() {
		lineNum++
		lastScannedLine = lineNum

		// Skip lines before offset
		if lineNum < offset {
			continue
		}

		// Stop if we've read enough lines, but keep scanning once more so
		// we can tell the caller whether truncation actually happened.
		if linesRead >= limit {
			hasMoreAfterLimit = true
			break
		}

		line := scanner.Text()

		// Truncate long lines
		if runes := []rune(line); len(runes) > maxLineLen {
			line = string(runes[:maxLineLen]) + "..."
		}

		// Format with line number (cat -n style)
		builder.WriteString(fmt.Sprintf("%6d\t%s\n", lineNum, line))
		linesRead++
	}

	if err := scanner.Err(); err != nil {
		return NewErrorResult(fmt.Sprintf("error reading file: %s", err)), nil
	}

	// Truncation hint. Emitted only when we actually stopped early —
	// partial-read indistinguishable from a complete read is a documented
	// Kimi pitfall that leads to "I read the file, nothing else to do"
	// followed by confident-but-wrong claims. Placed before the Related-
	// files block so it sits close to the numbered content.
	if hasMoreAfterLimit {
		nextOffset := offset + linesRead
		builder.WriteString(fmt.Sprintf(
			"\n[Truncated: showed lines %d-%d (%d of at least %d). Use offset=%d to continue.]\n",
			offset, offset+linesRead-1, linesRead, lastScannedLine, nextOffset,
		))
	}

	content := builder.String()
	if content == "" {
		if offset > 1 && lineNum > 0 {
			content = fmt.Sprintf("(offset %d is beyond end of file — file has %d lines)", offset, lineNum)
		} else {
			content = "(empty file)"
		}
	}

	// Record access pattern for predictive loading
	if t.predictor != nil {
		t.lastReadMu.Lock()
		prev := t.lastReadFile
		t.lastReadFile = filePath
		t.lastReadMu.Unlock()
		t.predictor.RecordAccess(filePath, "read", prev)
		t.predictor.LearnImports(filePath)
	}

	// Emit FilePeek
	EmitFilePeek(ctx, filePath, "Reading", content, "read")

	// Proactive context: append sibling/test-paired file previews so the
	// model gets package-level context without needing follow-up reads.
	// No-op when provider is nil (not wired or disabled by config). We
	// just pass a single-entry alreadyRead set with the current file —
	// the provider's own filtering (seen map) prevents duplicating
	// within one call. Session-level dedup could be added later via the
	// package-level FileReadTracker if needed.
	if t.proactive != nil {
		alreadyRead := map[string]bool{filePath: true}
		if block := t.proactive.RelatedBlock(filePath, alreadyRead); block != "" {
			content += block
		}
	}

	return NewSuccessResult(content), nil
}

// suggestFilesInWorkDir walks the workdir looking for files whose
// basename matches `base` (case-insensitive, substring). Bounded to
// avoid blowing up on large repos: visits at most suggestScanLimit
// entries total and skips vendor/build/generated directories.
// Returns up to 5 paths, formatted like suggestSimilarFiles output.
//
// Designed as the fallback when the exact path doesn't exist — the
// common "wrong dir, right filename" model failure mode Kimi hits.
func suggestFilesInWorkDir(workDir, base string) []string {
	if workDir == "" || base == "" {
		return nil
	}
	baseLower := strings.ToLower(base)
	nameOnly := strings.TrimSuffix(baseLower, filepath.Ext(baseLower))
	if len(nameOnly) < 2 {
		// Too-short names (e.g. "x" for the query path "/tmp/x") match
		// anything and drown the model in noise. Skip.
		return nil
	}

	const suggestScanLimit = 5000
	visited := 0
	var suggestions []string

	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"build": true, "dist": true, "target": true,
		".venv": true, "venv": true, "__pycache__": true,
	}

	_ = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // swallow per-entry errors, keep walking
		}
		// SkipAll (not SkipDir) is load-bearing: from a file entry,
		// SkipDir only skips remaining files in the PARENT directory —
		// it does not abort the walk. On a 50k-file monorepo that
		// means the "limit" is not actually enforced. SkipAll (Go
		// 1.20+) terminates WalkDir cleanly.
		if visited >= suggestScanLimit || len(suggestions) >= 5 {
			return filepath.SkipAll
		}
		visited++
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		entryLower := strings.ToLower(d.Name())
		entryNameOnly := strings.TrimSuffix(entryLower, filepath.Ext(entryLower))
		if entryLower == baseLower ||
			strings.Contains(entryLower, nameOnly) ||
			strings.Contains(nameOnly, entryNameOnly) {
			suggestions = append(suggestions, "  - "+path)
		}
		return nil
	})
	return suggestions
}

// suggestSimilarFiles returns up to 5 file paths similar to the given (non-existent) path.
// Uses case-insensitive substring matching on filenames in the same directory.
func suggestSimilarFiles(path string) []string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	baseLower := strings.ToLower(base)
	nameOnly := strings.TrimSuffix(baseLower, filepath.Ext(baseLower))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var suggestions []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		entryLower := strings.ToLower(e.Name())
		entryNameOnly := strings.TrimSuffix(entryLower, filepath.Ext(entryLower))
		// Case-insensitive exact match, or basename substring containment
		if entryLower == baseLower ||
			strings.Contains(entryLower, nameOnly) ||
			strings.Contains(nameOnly, entryNameOnly) {
			suggestions = append(suggestions, "  - "+filepath.Join(dir, e.Name()))
		}
		if len(suggestions) >= 5 {
			break
		}
	}
	return suggestions
}

// Note: isBinaryFile is defined in grep.go and shared across the tools package.
