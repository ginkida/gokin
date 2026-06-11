package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"google.golang.org/genai"
)

// CheckImpactTool analyzes the impact of changing a symbol.
type CheckImpactTool struct {
	workDir string
}

// NewCheckImpactTool creates a new CheckImpactTool instance.
func NewCheckImpactTool(workDir string) *CheckImpactTool {
	return &CheckImpactTool{
		workDir: workDir,
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

	// 1. Search for usages with grep. Output() keeps stdout (matches) separate
	// from stderr (warnings) — CombinedOutput would mix permission-denied noise
	// into the match list AND hide matches behind a non-zero exit.
	cmd := exec.CommandContext(ctx, "grep", "-r", "--exclude-dir=.git", "-n", symbol, t.workDir)
	output, err := cmd.Output()
	var searchWarning string
	if err != nil {
		if ctx.Err() != nil {
			return NewErrorResult(fmt.Sprintf("check_impact: search cancelled for %q: %v", symbol, ctx.Err())), nil
		}
		exitErr, _ := err.(*exec.ExitError)
		stderr := ""
		if exitErr != nil {
			stderr = strings.TrimSpace(string(exitErr.Stderr))
		}
		switch {
		case len(strings.TrimSpace(string(output))) > 0:
			// grep found matches AND failed (exit 2: one unreadable dir or a
			// dangling symlink under workDir). The matches are real — report
			// them, but disclose that coverage may be partial. Erroring here
			// made the tool permanently broken in any repo with one locked
			// directory (review-confirmed on macOS).
			searchWarning = stderr
		case exitErr != nil && exitErr.ExitCode() == 1 && stderr == "":
			// Benign "no matches" — fall through to the honest zero-usage report.
		default:
			// No matches and a real failure (grep missing, bad invocation):
			// must reach the model rather than masquerade as "symbol is private
			// or unused" — this is a blast-radius tool, and a false "unused"
			// invites deleting a symbol that actually has callers.
			detail := stderr
			if detail == "" {
				detail = err.Error()
			}
			return NewErrorResult(fmt.Sprintf("check_impact: grep failed for %q: %s", symbol, detail)), nil
		}
	}

	lines := strings.Split(string(output), "\n")

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

	if searchWarning != "" {
		fmt.Fprintf(&report, "⚠️ Search coverage may be partial — grep reported:\n%s\n\n", searchWarning)
	}

	if report.Len() < 100 { // Just the header
		return NewSuccessResult(fmt.Sprintf("No significant impact found for symbol: %s. It might be private or unused.", symbol)), nil
	}

	return NewSuccessResult(report.String()), nil
}
