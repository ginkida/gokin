package tools

import (
	"context"
	"fmt"
	"strings"
)

// ValidationWarning represents a single warning from a semantic validator.
type ValidationWarning struct {
	Validator string // e.g. "test_quality", "ci_workflow"
	Severity  string // "error", "warning", "info"
	File      string
	Line      int // 0 if not line-specific
	Message   string
}

// SemanticValidator checks written/edited files for logical errors
// that compile but are semantically wrong.
type SemanticValidator interface {
	Name() string
	Matches(filePath string) bool
	Validate(ctx context.Context, filePath string, content []byte, workDir string) []ValidationWarning
}

// SemanticValidatorRegistry holds registered validators and runs them.
type SemanticValidatorRegistry struct {
	validators []SemanticValidator
}

// NewSemanticValidatorRegistry creates an empty registry.
func NewSemanticValidatorRegistry() *SemanticValidatorRegistry {
	return &SemanticValidatorRegistry{}
}

// Register adds a validator to the registry.
func (r *SemanticValidatorRegistry) Register(v SemanticValidator) {
	r.validators = append(r.validators, v)
}

// RunAll runs all matching validators for the given file and returns collected warnings.
func (r *SemanticValidatorRegistry) RunAll(ctx context.Context, filePath string, content []byte, workDir string) []ValidationWarning {
	var all []ValidationWarning
	for _, v := range r.validators {
		if !v.Matches(filePath) {
			continue
		}
		// Per-validator timeout via context (caller sets overall budget)
		warnings := v.Validate(ctx, filePath, content, workDir)
		all = append(all, warnings...)
	}
	return all
}

// FormatWarnings formats a list of warnings into a string the model will see
// in the tool result, prompting it to self-correct.
func FormatWarnings(warnings []ValidationWarning) string {
	if len(warnings) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[smart-validation] Found issues in written file(s):\n")
	for _, w := range warnings {
		prefix := "WARNING"
		if w.Severity == "error" {
			prefix = "ERROR"
		} else if w.Severity == "info" {
			prefix = "INFO"
		}
		if w.Line > 0 {
			fmt.Fprintf(&sb, "  %s [%s] %s:%d — %s\n", prefix, w.Validator, w.File, w.Line, w.Message)
		} else {
			fmt.Fprintf(&sb, "  %s [%s] %s — %s\n", prefix, w.Validator, w.File, w.Message)
		}
	}
	sb.WriteString("Please fix the issues above.")
	return sb.String()
}
