package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FilePredictor predicts related files based on access patterns and import graphs.
// Implemented by ContextPredictor via an adapter to avoid import cycles.
type FilePredictor interface {
	PredictFiles(currentFile string, limit int) []PredictedFile
}

// PredictedFile represents a predicted related file.
type PredictedFile struct {
	Path       string
	Confidence float64
	Reason     string
}

// ContextEnricher appends relevant project context to tool results
// after a write/edit, so the model can spot inconsistencies immediately.
type ContextEnricher struct {
	workDir   string
	predictor FilePredictor
}

// NewContextEnricher creates a new enricher for the given project root.
func NewContextEnricher(workDir string) *ContextEnricher {
	return &ContextEnricher{workDir: workDir}
}

// SetPredictor sets the file predictor for prediction-based enrichment.
func (e *ContextEnricher) SetPredictor(p FilePredictor) {
	e.predictor = p
}

// Enrich returns a context hint for the given file path, or empty string if not applicable.
func (e *ContextEnricher) Enrich(filePath string) string {
	var result string
	switch {
	case isGitHubWorkflow(filePath):
		result = e.enrichWorkflow(filePath)
	case strings.HasSuffix(filePath, "_test.go"):
		result = e.enrichTestFile(filePath)
	}

	// Append prediction-based context for any file type
	if prediction := e.enrichFromPredictions(filePath); prediction != "" {
		if result != "" {
			result += "\n" + prediction
		} else {
			result = prediction
		}
	}
	return result
}

// enrichFromPredictions uses the file predictor to find related files
// and include brief summaries of high-confidence predictions.
func (e *ContextEnricher) enrichFromPredictions(filePath string) string {
	if e.predictor == nil {
		return ""
	}
	predictions := e.predictor.PredictFiles(filePath, 3)
	if len(predictions) == 0 {
		return ""
	}

	var hints []string
	for _, p := range predictions {
		if p.Confidence < 0.3 {
			continue
		}
		summary := readFileSummary(p.Path, 3)
		if summary != "" {
			hints = append(hints, fmt.Sprintf("%s (%s): %s",
				filepath.Base(p.Path), p.Reason, summary))
		}
	}
	if len(hints) == 0 {
		return ""
	}
	return "[context:predicted] Related files: " + strings.Join(hints, "; ")
}

// readFileSummary reads the first N non-empty, non-comment lines from a file.
func readFileSummary(path string, lines int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var result []string
	scanner := bufio.NewScanner(f)
	for len(result) < lines && scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "//") && !strings.HasPrefix(line, "#") {
			result = append(result, line)
		}
	}
	return strings.Join(result, " | ")
}

// enrichWorkflow adds canonical version info from go.mod and other workflow files.
func (e *ContextEnricher) enrichWorkflow(filePath string) string {
	var hints []string

	if ver := readGoModVersion(e.workDir); ver != "" {
		hints = append(hints, fmt.Sprintf("go.mod specifies go %s", ver))
	}

	baseName := filepath.Base(filePath)
	workflowDir := filepath.Join(e.workDir, ".github", "workflows")
	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		return formatHints(hints)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == baseName {
			continue
		}
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workflowDir, name))
		if err != nil {
			continue
		}
		if m := goVersionInWorkflowRe.FindStringSubmatch(string(data)); m != nil {
			hints = append(hints, fmt.Sprintf("%s uses go-version: %s", name, m[1]))
		}
	}

	return formatHints(hints)
}

var exportedFuncRe = regexp.MustCompile(`^func\s+([A-Z]\w*)\s*\(([^)]*)\)`)

// enrichTestFile adds exported function signatures from the source file being tested.
func (e *ContextEnricher) enrichTestFile(testPath string) string {
	srcPath := strings.TrimSuffix(testPath, "_test.go") + ".go"
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return ""
	}

	var sigs []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if m := exportedFuncRe.FindStringSubmatch(line); m != nil {
			sigs = append(sigs, fmt.Sprintf("func %s(%s)", m[1], m[2]))
		}
	}

	if len(sigs) == 0 {
		return ""
	}

	result := "[context] Tested file signatures: " + strings.Join(sigs, ", ")
	if len(result) > 500 {
		result = result[:497] + "..."
	}
	return result
}

func formatHints(hints []string) string {
	if len(hints) == 0 {
		return ""
	}
	return "[context] " + strings.Join(hints, "; ")
}
