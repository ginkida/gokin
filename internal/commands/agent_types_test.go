package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/agent"
)

// fakeAppForAgentTypes embeds fakeAppForMCP and overrides only
// GetAgentTypeRegistry, returning a real *agent.AgentTypeRegistry so the
// register/list/unregister commands have live state to mutate.
type fakeAppForAgentTypes struct {
	fakeAppForMCP
	registry *agent.AgentTypeRegistry
}

func (f *fakeAppForAgentTypes) GetAgentTypeRegistry() *agent.AgentTypeRegistry {
	return f.registry
}

func newAgentTypesApp() *fakeAppForAgentTypes {
	return &fakeAppForAgentTypes{
		registry: agent.NewAgentTypeRegistry(),
		fakeAppForMCP: fakeAppForMCP{
			cfg: nil, // not needed; panics avoided by overrides
		},
	}
}

// ─── RegisterAgentTypeCommand ────────────────────────────────────────────────

func TestRegisterAgentType_TooFewArgs(t *testing.T) {
	app := newAgentTypesApp()
	_, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(), []string{"only-name"}, app)
	if err == nil {
		t.Error("expected usage error when < 2 args")
	}
}

func TestRegisterAgentType_NilRegistry(t *testing.T) {
	// Default fakeAppForMCP returns nil for GetAgentTypeRegistry.
	app := &fakeAppForMCP{}
	_, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(), []string{"x", "desc"}, app)
	if err == nil {
		t.Error("expected error when registry is nil")
	}
}

func TestRegisterAgentType_Success(t *testing.T) {
	app := newAgentTypesApp()
	out, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(),
		[]string{"reviewer", "code reviewer", "--tools", "read,grep,bash"}, app)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.Contains(out, "✓ Registered") || !strings.Contains(out, "reviewer") {
		t.Errorf("output missing confirmation: %s", out)
	}
	if !strings.Contains(out, "read, grep, bash") {
		t.Errorf("output should list tools: %s", out)
	}
	// Verify it actually landed in the registry.
	if !app.registry.IsDynamic("reviewer") {
		t.Error("IsDynamic(reviewer) = false after register")
	}
}

func TestRegisterAgentType_CleansToolList(t *testing.T) {
	app := newAgentTypesApp()
	out, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(),
		[]string{"reviewer", "code reviewer", "--tools", " read, ,grep,, bash "}, app)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if strings.Contains(out, ", ,") {
		t.Fatalf("output should not include empty tool names: %s", out)
	}
	got, ok := app.registry.GetDynamic("reviewer")
	if !ok {
		t.Fatal("dynamic type not registered")
	}
	want := []string{"read", "grep", "bash"}
	if strings.Join(got.AllowedTools, ",") != strings.Join(want, ",") {
		t.Fatalf("AllowedTools = %v, want %v", got.AllowedTools, want)
	}
}

func TestRegisterAgentType_ParsedQuotedArgs(t *testing.T) {
	app := newAgentTypesApp()
	h := NewHandlerWithCommands(&RegisterAgentTypeCommand{})

	name, args, ok := h.Parse(`/register-agent-type reviewer "code reviewer" --tools "read, grep" --prompt 'be careful'`)
	if !ok {
		t.Fatal("Parse did not recognize register-agent-type")
	}

	if _, err := h.Execute(context.Background(), name, args, app); err != nil {
		t.Fatalf("execute parsed command: %v", err)
	}

	got, ok := app.registry.GetDynamic("reviewer")
	if !ok {
		t.Fatal("dynamic type not registered")
	}
	if got.Description != "code reviewer" {
		t.Fatalf("Description = %q, want %q", got.Description, "code reviewer")
	}
	if strings.Join(got.AllowedTools, ",") != "read,grep" {
		t.Fatalf("AllowedTools = %v, want [read grep]", got.AllowedTools)
	}
	if got.SystemPrompt != "be careful" {
		t.Fatalf("SystemPrompt = %q, want %q", got.SystemPrompt, "be careful")
	}
}

func TestRegisterAgentType_MalformedFlags(t *testing.T) {
	cases := [][]string{
		{"reviewer", "code reviewer", "--tools"},
		{"reviewer", "code reviewer", "--prompt"},
		{"reviewer", "code reviewer", "--bogus"},
	}
	for _, args := range cases {
		app := newAgentTypesApp()
		if _, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(), args, app); err == nil {
			t.Errorf("Execute(%v) should reject malformed flag", args)
		}
	}
}

func TestRegisterAgentType_WithPrompt(t *testing.T) {
	app := newAgentTypesApp()
	longPrompt := "You are a meticulous code reviewer. Check for bugs, style, and security."
	out, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(),
		[]string{"reviewer", "reviews code", "--prompt", longPrompt}, app)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// Prompt is truncated to 50 chars in the output.
	if !strings.Contains(out, "Prompt:") {
		t.Errorf("output should include truncated prompt: %s", out)
	}
}

func TestRegisterAgentType_BuiltinConflict(t *testing.T) {
	app := newAgentTypesApp()
	// "explore" is a built-in type — registration must be refused.
	_, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(),
		[]string{"explore", "my explore"}, app)
	if err == nil {
		t.Error("expected error when overriding built-in type")
	}
}

func TestRegisterAgentType_QuotedDescription(t *testing.T) {
	app := newAgentTypesApp()
	// The shell would strip quotes; here we simulate by passing a quoted arg.
	out, err := (&RegisterAgentTypeCommand{}).Execute(context.Background(),
		[]string{"mybot", `"a quoted desc"`}, app)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !strings.Contains(out, "a quoted desc") {
		t.Errorf("quotes should be stripped from description: %s", out)
	}
}

// ─── ListAgentTypesCommand ───────────────────────────────────────────────────

func TestListAgentTypes_NilRegistry(t *testing.T) {
	app := &fakeAppForMCP{}
	_, err := (&ListAgentTypesCommand{}).Execute(context.Background(), nil, app)
	if err == nil {
		t.Error("expected error when registry is nil")
	}
}

func TestListAgentTypes_NoCustom(t *testing.T) {
	app := newAgentTypesApp()
	out, err := (&ListAgentTypesCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Built-in Agent Types") {
		t.Errorf("missing built-in header: %s", out)
	}
	if !strings.Contains(out, "No custom agent types") {
		t.Errorf("should report no custom types: %s", out)
	}
}

func TestListAgentTypes_WithCustom(t *testing.T) {
	app := newAgentTypesApp()
	if err := app.registry.RegisterDynamic("reviewer", "code reviewer", []string{"read"}, ""); err != nil {
		t.Fatal(err)
	}
	out, err := (&ListAgentTypesCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Custom Agent Types") {
		t.Errorf("missing custom header: %s", out)
	}
	if !strings.Contains(out, "reviewer") || !strings.Contains(out, "code reviewer") {
		t.Errorf("custom type not listed: %s", out)
	}
	if !strings.Contains(out, "Tools: read") {
		t.Errorf("custom type tools not shown: %s", out)
	}
}

// ─── UnregisterAgentTypeCommand ──────────────────────────────────────────────

func TestUnregisterAgentType_NoArgs(t *testing.T) {
	app := newAgentTypesApp()
	_, err := (&UnregisterAgentTypeCommand{}).Execute(context.Background(), nil, app)
	if err == nil {
		t.Error("expected usage error with no args")
	}
}

func TestUnregisterAgentType_NilRegistry(t *testing.T) {
	app := &fakeAppForMCP{}
	_, err := (&UnregisterAgentTypeCommand{}).Execute(context.Background(), []string{"x"}, app)
	if err == nil {
		t.Error("expected error when registry is nil")
	}
}

func TestUnregisterAgentType_BuiltinRejected(t *testing.T) {
	app := newAgentTypesApp()
	_, err := (&UnregisterAgentTypeCommand{}).Execute(context.Background(), []string{"explore"}, app)
	if err == nil {
		t.Error("cannot unregister built-in type")
	}
}

func TestUnregisterAgentType_NotFound(t *testing.T) {
	app := newAgentTypesApp()
	_, err := (&UnregisterAgentTypeCommand{}).Execute(context.Background(), []string{"nonexistent"}, app)
	if err == nil {
		t.Error("expected error for unknown custom type")
	}
}

func TestUnregisterAgentType_Success(t *testing.T) {
	app := newAgentTypesApp()
	if err := app.registry.RegisterDynamic("reviewer", "desc", nil, ""); err != nil {
		t.Fatal(err)
	}
	out, err := (&UnregisterAgentTypeCommand{}).Execute(context.Background(), []string{"reviewer"}, app)
	if err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if !strings.Contains(out, "✓ Unregistered") {
		t.Errorf("output missing confirmation: %s", out)
	}
	if app.registry.IsDynamic("reviewer") {
		t.Error("type still dynamic after unregister")
	}
}

// ─── truncate helper ─────────────────────────────────────────────────────────

func TestTruncate_ShortString(t *testing.T) {
	got := truncate("hello", 10)
	if got != "hello" {
		t.Errorf("truncate(short) = %q, want %q", got, "hello")
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	got := truncate("hello", 5)
	if got != "hello" {
		t.Errorf("truncate(exact) = %q, want %q", got, "hello")
	}
}

func TestTruncate_LongString(t *testing.T) {
	got := truncate("abcdefghij", 6)
	if got != "abc..." {
		t.Errorf("truncate(long) = %q, want %q", got, "abc...")
	}
}

func TestTruncate_TinyMaxLen(t *testing.T) {
	cases := []struct {
		maxLen int
		want   string
	}{
		{0, ""},
		{1, "a"},
		{2, "ab"},
		{3, "abc"},
	}
	for _, tc := range cases {
		if got := truncate("abcdef", tc.maxLen); got != tc.want {
			t.Errorf("truncate tiny maxLen=%d = %q, want %q", tc.maxLen, got, tc.want)
		}
	}
}

// ─── AddDirCommand metadata (the Usage/GetMetadata 0% arms) ───────────────────

func TestAddDirCommand_Metadata(t *testing.T) {
	c := &AddDirCommand{}
	if c.Name() != "add-dir" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage should not be empty")
	}
	md := c.GetMetadata()
	if md.Category == "" {
		t.Error("GetMetadata Category should not be empty")
	}
}

func TestRemoveDirCommand_Metadata(t *testing.T) {
	c := &RemoveDirCommand{}
	if c.Name() != "remove-dir" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Usage() == "" {
		t.Error("Usage should not be empty")
	}
	md := c.GetMetadata()
	if md.Category == "" {
		t.Error("GetMetadata Category should not be empty")
	}
}

// ─── Metadata arms for the agent-type commands (value-receiver methods) ───────
// These are trivial but were 0%-covered; pin them so a future rename or
// category change is caught.

func TestRegisterAgentTypeCommand_Metadata(t *testing.T) {
	c := &RegisterAgentTypeCommand{}
	if c.Name() != "register-agent-type" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Description() == "" {
		t.Error("Description should not be empty")
	}
	if c.Usage() == "" {
		t.Error("Usage should not be empty")
	}
	md := c.GetMetadata()
	if md.Category == "" || md.Icon == "" {
		t.Errorf("GetMetadata incomplete: %+v", md)
	}
}

func TestListAgentTypesCommand_Metadata(t *testing.T) {
	c := &ListAgentTypesCommand{}
	if c.Name() != "list-agent-types" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Description() == "" {
		t.Error("Description should not be empty")
	}
	if c.Usage() == "" {
		t.Error("Usage should not be empty")
	}
	md := c.GetMetadata()
	if md.Category == "" {
		t.Errorf("GetMetadata incomplete: %+v", md)
	}
}

func TestUnregisterAgentTypeCommand_Metadata(t *testing.T) {
	c := &UnregisterAgentTypeCommand{}
	if c.Name() != "unregister-agent-type" {
		t.Errorf("Name = %q", c.Name())
	}
	if c.Description() == "" {
		t.Error("Description should not be empty")
	}
	if c.Usage() == "" {
		t.Error("Usage should not be empty")
	}
	md := c.GetMetadata()
	if md.Category == "" {
		t.Errorf("GetMetadata incomplete: %+v", md)
	}
}

// ─── AddDirCommand error paths ────────────────────────────────────────────────

func TestAddDirCommand_EmptyArgsAfterFlagStripped(t *testing.T) {
	// Only flags, no path → usage error.
	_, err := (&AddDirCommand{}).Execute(context.Background(), []string{"--persist"}, &fakeAppForAddDir{})
	if err == nil {
		t.Error("expected usage error when only flags given (no path)")
	}
}

func TestAddDirCommand_GrantErrorWithResolvedSoftMessage(t *testing.T) {
	// A persistence failure that still resolved the path → soft message, not hard error.
	app := &fakeAppForAddDir{}
	app.grantErr = errors.New("config save failed")
	// Override the fake to return a resolved path alongside the error.
	app2 := &addDirSoftFailApp{}
	out, err := (&AddDirCommand{}).Execute(context.Background(), []string{"/dir"}, app2)
	if err != nil {
		t.Fatalf("soft-fail should not return hard error: %v", err)
	}
	if !strings.Contains(out, "this session") || !strings.Contains(out, "Note:") {
		t.Errorf("soft-fail output should mention session grant + note: %s", out)
	}
}

// addDirSoftFailApp returns a resolved path AND an error, to exercise the
// "persistence-only failure" branch in AddDirCommand.Execute.
type addDirSoftFailApp struct {
	fakeAppForAddDir
}

func (f *addDirSoftFailApp) GrantAllowedDir(path string, persist bool) (string, error) {
	return "/resolved/" + path, errors.New("config save failed")
}
