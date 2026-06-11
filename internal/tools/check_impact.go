package tools

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"google.golang.org/genai"
)

// CheckImpactTool analyzes the impact of changing a symbol.
type CheckImpactTool struct {
	workDir string
	// engine is the in-package pure-Go search engine (the grep tool's core).
	// Shelling out to system grep made the tool dead on hosts without grep
	// in PATH and flaky around unreadable directories (exit 2); the engine
	// is dependency-free, gitignore-aware, and skips binaries.
	engine *GrepTool
}

// NewCheckImpactTool creates a new CheckImpactTool instance.
func NewCheckImpactTool(workDir string) *CheckImpactTool {
	return &CheckImpactTool{
		workDir: workDir,
		engine:  NewGrepTool(workDir),
	}
}

func (t *CheckImpactTool) Name() string {
	return "check_impact"
}

func (t *CheckImpactTool) Description() string {
	return `Blast Radius Analysis tool. Finds all usages and potential impacts of changing a symbol (function, variable, etc.).
It categorizes findings into Imports, Definitions, and Usages to help assess the risk of modification.`
}

func (t *CheckImpactTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"symbol": {
					Type:        genai.TypeString,
					Description: "The symbol name to analyze (e.g., 'Agent', 'Run', 'executeTool')",
				},
			},
			Required: []string{"symbol"},
		},
	}
}

func (t *CheckImpactTool) Validate(args map[string]any) error {
	symbol, ok := GetString(args, "symbol")
	if !ok || symbol == "" {
		return NewValidationError("symbol", "is required")
	}
	return nil
}

func (t *CheckImpactTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	symbol, _ := GetString(args, "symbol")

	// 1. Search for usages with the in-package pure-Go engine (literal
	// substring match — QuoteMeta keeps parity with the old `grep -r symbol`
	// semantics). Engine errors must reach the model rather than masquerade
	// as "symbol is private or unused": this is a blast-radius tool, and a
	// false "unused" invites deleting a symbol that actually has callers.
	files, err := t.engine.getFiles(t.workDir, "")
	if err != nil {
		return NewErrorResult(fmt.Sprintf("check_impact: search failed for %q: %v", symbol, err)), nil
	}
	re, err := regexp.Compile(regexp.QuoteMeta(symbol))
	if err != nil {
		return NewErrorResult(fmt.Sprintf("check_impact: bad symbol %q: %v", symbol, err)), nil
	}
	found := t.engine.searchParallel(ctx, files, re, 0, nil)
	if ctx.Err() != nil {
		// A cancelled search returns PARTIAL matches — reporting them as the
		// blast radius would be a silent false "barely used".
		return NewErrorResult(fmt.Sprintf("check_impact: search cancelled for %q: %v", symbol, ctx.Err())), nil
	}
	// Deterministic report order: searchParallel collects from goroutines.
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })

	var lines []string
	for _, fm := range found {
		for _, m := range fm.matches {
			lines = append(lines, fmt.Sprintf("%s:%d:%s", fm.path, m.lineNum, m.line))
		}
	}

	var report strings.Builder
	fmt.Fprintf(&report, "# Impact Report for symbol: %s\n\n", symbol)

	categories := map[string][]string{
		"Definitions": {},
		"Imports":     {},
		"Usages":      {},
	}

	for _, line := range lines {
		if line == "" {
			continue
		}

		// Simple heuristic categorization
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "func ") || strings.Contains(lowerLine, "type ") || strings.Contains(lowerLine, "var ") {
			categories["Definitions"] = append(categories["Definitions"], line)
		} else if strings.Contains(lowerLine, "import ") || strings.Contains(lowerLine, "require(") {
			categories["Imports"] = append(categories["Imports"], line)
		} else {
			categories["Usages"] = append(categories["Usages"], line)
		}
	}

	// Fixed section order — ranging the map directly made the report
	// nondeterministic across runs (same fix class as run_tests' sorted
	// failedPkgs).
	for _, cat := range []string{"Definitions", "Imports", "Usages"} {
		matches := categories[cat]
		if len(matches) > 0 {
			fmt.Fprintf(&report, "## %s (%d)\n", cat, len(matches))
			// Limit display to 10 per category
			limit := min(10, len(matches))
			for i := range limit {
				// Clean path for readability
				cleanLine := strings.TrimPrefix(matches[i], t.workDir)
				fmt.Fprintf(&report, "- %s\n", cleanLine)
			}
			if len(matches) > limit {
				fmt.Fprintf(&report, "- ... and %d more\n", len(matches)-limit)
			}
			report.WriteString("\n")
		}
	}

	if report.Len() < 100 { // Just the header
		return NewSuccessResult(fmt.Sprintf("No significant impact found for symbol: %s. It might be private or unused.", symbol)), nil
	}

	return NewSuccessResult(report.String()), nil
}
