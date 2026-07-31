package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

const structuredOutputTestSchema = `{
	"type":"object",
	"properties":{"answer":{"type":"string"}},
	"required":["answer"],
	"additionalProperties":false
}`

func TestCompileStructuredOutputSchemaAndValidateExactJSON(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	value, err := schema.Validate(`{"answer":"ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	object, ok := value.(map[string]any)
	if !ok || object["answer"] != "ok" {
		t.Fatalf("validated value = %#v", value)
	}

	for _, invalid := range []string{
		`{"answer":42}`,
		`{"answer":"ok","extra":true}`,
		"```json\n{\"answer\":\"ok\"}\n```",
		`{"answer":"ok"} trailing`,
	} {
		if _, err := schema.Validate(invalid); err == nil {
			t.Fatalf("invalid structured output accepted: %q", invalid)
		}
	}
}

func TestCompileStructuredOutputSchemaRejectsInvalidAndExternalReferences(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{name: "empty", schema: " ", want: "non-empty"},
		{name: "malformed JSON", schema: `{`, want: "parse"},
		{name: "invalid schema", schema: `{"type":42}`, want: "compile"},
		{
			name:   "remote reference",
			schema: `{"$ref":"https://example.com/external.json"}`,
			want:   "external JSON Schema resource",
		},
		{
			name:   "file reference",
			schema: `{"$ref":"file:///tmp/external.json"}`,
			want:   "external JSON Schema resource",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := CompileStructuredOutputSchema(test.schema); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	oversized := strings.Repeat(" ", MaxStructuredOutputSchemaBytes+1)
	if _, err := CompileStructuredOutputSchema(oversized); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestStructuredOutputInstructionIsRuntimeOnlyAndSurvivesRefresh(t *testing.T) {
	workDir := t.TempDir()
	mock := testkit.NewMockClient()
	session := newTestSessionWithSystemInstruction("canonical base")
	application := &App{
		client:        mock,
		session:       session,
		workDir:       workDir,
		config:        testConfig(),
		promptBuilder: appcontext.NewPromptBuilder(workDir, &appcontext.ProjectInfo{}),
	}
	if err := application.ConfigureRunSystemPrompt(nil, "RUN APPENDIX"); err != nil {
		t.Fatal(err)
	}
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}

	restore := application.installStructuredOutputInstruction(schema)
	if got := mock.SystemInstruction(); !strings.Contains(got, "RUN APPENDIX") ||
		!strings.Contains(got, "Structured output contract") {
		t.Fatalf("installed prompt = %q", got)
	}
	if got := session.GetSystemInstruction(); got != "canonical base" {
		t.Fatalf("schema leaked into session: %q", got)
	}

	application.refreshSystemInstruction()
	if got := mock.SystemInstruction(); !strings.Contains(got, "Structured output contract") {
		t.Fatalf("refresh dropped structured contract: %q", got)
	}
	if strings.Contains(session.GetSystemInstruction(), "Structured output contract") {
		t.Fatalf("refresh persisted structured contract: %q",
			session.GetSystemInstruction())
	}

	restore()
	if got := mock.SystemInstruction(); strings.Contains(got, "Structured output contract") ||
		!strings.Contains(got, "RUN APPENDIX") {
		t.Fatalf("restored prompt = %q", got)
	}
}

func TestRunHeadlessStructuredOutputRetriesAndReturnsValidatedValue(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().
		EnqueueText("not JSON").
		EnqueueText(`{"answer":"corrected"}`)
	application, _ := newHeadlessPolicyTestApp(
		t, mock, &appHeadlessScriptedTool{name: "unused"})

	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(
		context.Background(),
		"analyze the repository",
		HeadlessOptions{
			OutputFormat: HeadlessOutputJSON,
			Stdout:       &stdout,
			Stderr:       io.Discard,
			JSONSchema:   schema,
		},
	)
	if err != nil {
		t.Fatalf("RunHeadlessWithOptions: %v", err)
	}
	if result.Status != "success" || result.Result != `{"answer":"corrected"}` {
		t.Fatalf("result = %+v", result)
	}
	if result.StructuredOutput == nil {
		t.Fatal("structured output is nil")
	}
	object, ok := (*result.StructuredOutput).(map[string]any)
	if !ok || object["answer"] != "corrected" {
		t.Fatalf("structured output = %#v", result.StructuredOutput)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.StructuredOutput == nil {
		t.Fatal("encoded structured output is nil")
	}
	decodedObject, ok := (*decoded.StructuredOutput).(map[string]any)
	if !ok || decodedObject["answer"] != "corrected" {
		t.Fatalf("encoded structured output = %#v", decoded.StructuredOutput)
	}
	calls := mock.Calls()
	if len(calls) != 2 ||
		!strings.Contains(calls[1].Message, "format-only correction") {
		t.Fatalf("provider calls = %+v", calls)
	}
	if strings.Contains(application.session.GetSystemInstruction(), "Structured output contract") {
		t.Fatalf("structured contract leaked into session: %q",
			application.session.GetSystemInstruction())
	}
}

func TestRunHeadlessStructuredOutputFailsClosedAfterRetryLimit(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().
		EnqueueText("bad one").
		EnqueueText("bad two").
		EnqueueText("bad three")
	application, _ := newHeadlessPolicyTestApp(
		t, mock, &appHeadlessScriptedTool{name: "unused"})

	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(
		context.Background(),
		"return structured data",
		HeadlessOptions{
			OutputFormat: HeadlessOutputJSON,
			Stdout:       &stdout,
			Stderr:       io.Discard,
			JSONSchema:   schema,
		},
	)
	if err == nil {
		t.Fatal("invalid structured output unexpectedly succeeded")
	}
	if result.Status != "error" || result.Error == nil ||
		result.Error.Kind != "structured_output" ||
		result.StructuredOutput != nil {
		t.Fatalf("result = %+v", result)
	}
	if calls := len(mock.Calls()); calls != maxStructuredOutputRetries+1 {
		t.Fatalf("provider calls = %d", calls)
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Error == nil || decoded.Error.Kind != "structured_output" {
		t.Fatalf("encoded result = %+v", decoded)
	}
}

func TestStructuredOutputCorrectionCannotExecuteTools(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().
		EnqueueText("not JSON").
		EnqueueToolCall("mutate", map[string]any{"value": "dangerous"}).
		EnqueueText(`{"answer":"after blocked tool"}`)
	mutatingTool := &appHeadlessScriptedTool{
		name:    "mutate",
		results: []tools.ToolResult{tools.NewSuccessResult("must not execute")},
	}
	application, _ := newHeadlessPolicyTestApp(t, mock, mutatingTool)

	result, err := application.RunHeadlessWithOptions(
		context.Background(),
		"inspect only",
		HeadlessOptions{
			OutputFormat: HeadlessOutputJSON,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
			JSONSchema:   schema,
		},
	)
	if err == nil || result.Status != "policy_blocked" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if calls := mutatingTool.CallCount(); calls != 0 {
		t.Fatalf("format correction executed tool %d times", calls)
	}
	// The refusal comes from the empty ceiling the correction installs, not from
	// the operator's configuration — say so, or the report reads as if the
	// user's own policy fired.
	if result.Error == nil ||
		!strings.Contains(result.Error.Message, "structured-output format correction") {
		t.Fatalf("policy failure does not name the correction as the cause: %+v", result.Error)
	}
}

func TestRunHeadlessStructuredOutputSupportsNullAndStreamTerminal(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(`{"type":"null"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []HeadlessOutputFormat{
		HeadlessOutputJSON,
		HeadlessOutputStreamJSON,
	} {
		t.Run(string(format), func(t *testing.T) {
			mock := testkit.NewMockClient().EnqueueText("null")
			application, _ := newHeadlessPolicyTestApp(
				t, mock, &appHeadlessScriptedTool{name: "unused"})
			var stdout bytes.Buffer
			result, err := application.RunHeadlessWithOptions(
				context.Background(),
				"return no value",
				HeadlessOptions{
					OutputFormat: format,
					Stdout:       &stdout,
					Stderr:       io.Discard,
					JSONSchema:   schema,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.StructuredOutput == nil || *result.StructuredOutput != nil {
				t.Fatalf("structured null = %#v", result.StructuredOutput)
			}
			if !strings.Contains(stdout.String(), `"structured_output":null`) {
				t.Fatalf("encoded output lacks explicit null: %q", stdout.String())
			}
			if format == HeadlessOutputStreamJSON {
				lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
				if len(lines) < 2 ||
					!strings.Contains(lines[len(lines)-1], `"type":"result"`) ||
					!strings.Contains(lines[len(lines)-1], `"structured_output":null`) {
					t.Fatalf("stream terminal line = %q", lines[len(lines)-1])
				}
			}
		})
	}
}

func TestRunHeadlessStructuredOutputRejectsTextEnvelope(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	application, _ := newHeadlessPolicyTestApp(
		t, testkit.NewMockClient(), &appHeadlessScriptedTool{name: "unused"})
	result, err := application.RunHeadlessWithOptions(
		context.Background(),
		"answer",
		HeadlessOptions{
			OutputFormat: HeadlessOutputText,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
			JSONSchema:   schema,
		},
	)
	if err == nil || result.Error == nil || result.Error.Kind != "validation" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestIntersectCapabilityNamesCannotWidenCorrectionCeiling(t *testing.T) {
	if got := intersectCapabilityNames(
		[]string{"bash", "read", "write"}, []string{}); len(got) != 0 {
		t.Fatalf("empty inherited ceiling widened to %v", got)
	}
	got := intersectCapabilityNames(
		[]string{"write", "read", "bash"}, []string{"read", "grep", "bash"})
	if strings.Join(got, ",") != "bash,read" {
		t.Fatalf("intersection = %v", got)
	}
}

// Tiny local helpers keep this test independent of the large production
// builder while still exercising refreshSystemInstruction.
func newTestSessionWithSystemInstruction(instruction string) *chat.Session {
	session := chat.NewSession()
	session.SetSystemInstruction(instruction)
	return session
}

func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	return cfg
}

var _ tools.Tool = (*appHeadlessScriptedTool)(nil)
