package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// MaxStructuredOutputSchemaBytes bounds CLI-controlled prompt growth and
	// schema compiler work before any provider connection is created.
	MaxStructuredOutputSchemaBytes = 64 << 10
	maxStructuredOutputRetries     = 2
	maxStructuredValidationHint    = 2000
)

// StructuredOutputSchema is a compiled, immutable invocation contract.
// Compile once during CLI validation and reuse it across stream-json turns.
type StructuredOutputSchema struct {
	canonical string
	compiled  *jsonschema.Schema
}

type closedSchemaResourceLoader struct{}

type structuredOutputCorrectionContextKey struct{}

func withStructuredOutputCorrection(ctx context.Context) context.Context {
	return context.WithValue(ctx, structuredOutputCorrectionContextKey{}, true)
}

func isStructuredOutputCorrection(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(structuredOutputCorrectionContextKey{}).(bool)
	return value
}

func (closedSchemaResourceLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf(
		"external JSON Schema resource %q is not allowed; use local $defs", url)
}

// CompileStructuredOutputSchema parses and compiles a self-contained JSON
// Schema. External file/network references are deliberately disabled: a CLI
// validation flag must never become an implicit filesystem or network loader.
func CompileStructuredOutputSchema(raw string) (*StructuredOutputSchema, error) {
	if len(raw) > MaxStructuredOutputSchemaBytes {
		return nil, fmt.Errorf(
			"--json-schema exceeds %d KiB limit",
			MaxStructuredOutputSchemaBytes>>10,
		)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("--json-schema requires a non-empty JSON Schema")
	}
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse --json-schema: %w", err)
	}
	canonicalBytes, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("canonicalize --json-schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(closedSchemaResourceLoader{})
	const schemaURL = "https://gokin.local/invocation-output.schema.json"
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, fmt.Errorf("load --json-schema: %w", err)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile --json-schema: %w", err)
	}
	return &StructuredOutputSchema{
		canonical: string(canonicalBytes),
		compiled:  compiled,
	}, nil
}

// installStructuredOutputInstruction appends the schema contract to the live
// client only. It neither mutates invocation prompt configuration nor writes
// the schema into resumable session state.
func (a *App) installStructuredOutputInstruction(
	schema *StructuredOutputSchema,
) func() {
	if a == nil || schema == nil {
		return func() {}
	}
	base := ""
	if a.session != nil {
		base = a.session.GetSystemInstruction()
	}
	a.runSystemPromptMu.Lock()
	a.runStructuredOutputPrompt = schema.Instruction()
	a.runSystemPromptMu.Unlock()
	if current := a.clientSnapshot(); current != nil {
		a.applySystemInstruction(current, base, false)
	}
	return func() {
		a.runSystemPromptMu.Lock()
		a.runStructuredOutputPrompt = ""
		a.runSystemPromptMu.Unlock()
		currentBase := base
		if a.session != nil {
			currentBase = a.session.GetSystemInstruction()
		}
		if current := a.clientSnapshot(); current != nil {
			a.applySystemInstruction(current, currentBase, false)
		}
	}
}

// Instruction returns the runtime-only system appendix that makes local
// validation achievable without changing the agent's tool workflow.
func (s *StructuredOutputSchema) Instruction() string {
	if s == nil {
		return ""
	}
	return "## Structured output contract\n\n" +
		"Complete the requested workflow normally, including any necessary tool use. " +
		"Your final assistant response must contain exactly one JSON value and no " +
		"Markdown fence, commentary, or trailing text. The value must validate against " +
		"this JSON Schema:\n\n" + s.canonical
}

// Validate parses one exact JSON value and validates it against the compiled
// schema. The returned value is safe to place directly in structured_output.
func (s *StructuredOutputSchema) Validate(result string) (any, error) {
	if s == nil || s.compiled == nil {
		return nil, fmt.Errorf("structured output schema is not initialized")
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(result))
	if err != nil {
		return nil, fmt.Errorf("final response is not exactly one JSON value: %w", err)
	}
	if err := s.compiled.Validate(value); err != nil {
		return nil, fmt.Errorf("final response does not match JSON Schema: %w", err)
	}
	return value, nil
}

func (s *StructuredOutputSchema) correctionPrompt(validationErr error) string {
	detail := "unknown validation error"
	if validationErr != nil {
		detail = strings.TrimSpace(validationErr.Error())
	}
	if len(detail) > maxStructuredValidationHint {
		detail = detail[:maxStructuredValidationHint] + "…"
	}
	return "Your previous final response did not satisfy the invocation's structured " +
		"output contract. This is a format-only correction: do not call tools or redo " +
		"the completed work. Return exactly one JSON value matching the system-provided " +
		"schema, with no Markdown fence or commentary.\n\nValidation error: " + detail
}
