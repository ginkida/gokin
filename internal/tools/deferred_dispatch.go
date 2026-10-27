package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gokin/internal/toolschema"
	"google.golang.org/genai"
)

type deferredCatalogKey struct{}
type deferredCatalog struct {
	registry ToolRegistry
	schema   []*genai.Tool
}

// ContextWithDeferredCatalog separates discovery presentation from the schema
// execution ceiling. In particular headless omissions do not bypass normal
// authorization/strict-denial handling for a hallucinated target.
func ContextWithDeferredCatalog(ctx context.Context, registry ToolRegistry, schema []*genai.Tool) context.Context {
	return context.WithValue(ctx, deferredCatalogKey{}, deferredCatalog{registry, schema})
}

func DeferredSchemasEnabled(ctx context.Context) bool {
	enabled, _ := DeferredSchemaModeFromContext(ctx)
	return enabled
}

// DeferredSchemaModeFromContext distinguishes an owning full-mode session from
// workers that should use the runner's configured default.
func DeferredSchemaModeFromContext(ctx context.Context) (enabled, present bool) {
	catalog, ok := ctx.Value(deferredCatalogKey{}).(deferredCatalog)
	return catalog.registry != nil, ok
}

// ContextWithoutDeferredCatalog masks a parent profile for a legacy/full child.
func ContextWithoutDeferredCatalog(ctx context.Context) context.Context {
	return context.WithValue(ctx, deferredCatalogKey{}, deferredCatalog{})
}

func ownedDeferredCatalog(ctx context.Context, registry ToolRegistry) (deferredCatalog, bool) {
	catalog, ok := ctx.Value(deferredCatalogKey{}).(deferredCatalog)
	return catalog, ok && catalog.registry != nil && catalog.registry == registry
}

// ResolveDeferredCalls creates execution envelopes without changing provider
// calls or IDs. Every downstream gate sees the real target, including hooks,
// dependency classification, loop detection, retry signatures and journals.
func ResolveDeferredCalls(ctx context.Context, registry ToolRegistry, calls []*genai.FunctionCall) []*genai.FunctionCall {
	if _, ok := ownedDeferredCatalog(ctx, registry); !ok {
		return calls
	}
	resolved := make([]*genai.FunctionCall, len(calls))
	for i, call := range calls {
		resolved[i] = resolveDeferredCall(call)
	}
	return resolved
}

func resolveDeferredCall(call *genai.FunctionCall) *genai.FunctionCall {
	effective := toolschema.EffectiveCall(call)
	if effective != call {
		effective.Args = ClonePermissionArgs(effective.Args)
	}
	return effective
}

func deferredArguments(args map[string]any) (string, map[string]any, error) {
	name, ok := args["name"].(string)
	if !ok || strings.TrimSpace(name) != name || name == "" {
		return "", nil, fmt.Errorf("use_tool requires an exact tool name")
	}
	if name == "use_tool" || name == "tool_search" {
		return "", nil, fmt.Errorf("recursive meta-tool dispatch is not allowed")
	}
	arguments, ok := args["arguments"].(map[string]any)
	if !ok || arguments == nil {
		return "", nil, fmt.Errorf("use_tool arguments must be a JSON object")
	}
	if len(args) != 2 {
		return "", nil, fmt.Errorf("use_tool accepts only name and arguments")
	}
	return name, arguments, nil
}

// RestoreWireResponseNames is called only at the history/provider boundary;
// execution observers retain the real target name and arguments.
func RestoreWireResponseNames(results []*genai.FunctionResponse, wire []*genai.FunctionCall) {
	for i, result := range results {
		if result != nil && i < len(wire) && wire[i] != nil {
			result.Name = wire[i].Name
		}
	}
}

// DeferredMetaTool supplies virtual declarations only for the owning loop.
// They are deliberately absent from execution registries/capability inheritance.
func DeferredMetaTool(ctx context.Context, registry ToolRegistry, name string) (Tool, bool) {
	_, enabled := ownedDeferredCatalog(ctx, registry)
	if !enabled || (name != "tool_search" && name != "use_tool") {
		return nil, false
	}
	return &deferredMetaTool{name: name, registry: registry}, true
}

type deferredMetaTool struct {
	name     string
	registry ToolRegistry
}

func (t *deferredMetaTool) Name() string { return t.name }
func (t *deferredMetaTool) Declaration() *genai.FunctionDeclaration {
	if t.name == "tool_search" {
		return toolschema.SearchDeclaration()
	}
	return toolschema.UseDeclaration()
}
func (t *deferredMetaTool) Description() string { return t.Declaration().Description }
func (t *deferredMetaTool) Validate(args map[string]any) error {
	if t.name == "use_tool" {
		_, _, err := deferredArguments(args)
		return err
	}
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" || len(query) > 256 || len(args) != 1 {
		return fmt.Errorf("tool_search requires a query of 1..256 bytes")
	}
	return nil
}
func (t *deferredMetaTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.name == "use_tool" {
		return NewErrorResult("invalid deferred dispatch"), nil
	}
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	catalog, ok := ownedDeferredCatalog(ctx, t.registry)
	if !ok {
		return NewErrorResult("tool discovery is unavailable"), nil
	}
	query := strings.ToLower(strings.TrimSpace(args["query"].(string)))
	var exact, matches []*genai.FunctionDeclaration
	for _, env := range catalog.schema {
		if env == nil {
			continue
		}
		for _, d := range env.FunctionDeclarations {
			if d == nil || d.Name == "tool_search" || d.Name == "use_tool" {
				continue
			}
			if ceiling, restricted := ToolCapabilityCeilingFromContext(ctx); restricted && !toolCapabilityAllows(ceiling, d.Name) {
				continue
			}
			tool, exists := t.registry.Get(d.Name)
			if !exists {
				continue
			}
			// Catalog membership is policy-filtered; descriptions/parameters
			// are live, so a skill/task or replaced MCP tool is described accurately.
			if current := tool.Declaration(); current != nil && current.Name == d.Name {
				d = current
			}
			if strings.EqualFold(d.Name, query) {
				exact = append(exact, d)
				continue
			}
			haystack := strings.ToLower(d.Name + " " + d.Description)
			if source, ok := tool.(mcpServerSource); ok {
				haystack += " mcp " + strings.ToLower(source.GetServerName())
			}
			match := true
			for _, word := range strings.Fields(query) {
				match = match && strings.Contains(haystack, word)
			}
			if match {
				matches = append(matches, d)
			}
		}
	}
	if len(exact) > 0 {
		matches = exact
	}
	sortDeclarationsByName(matches)
	// Keep every schema complete and below ToolResult.ToMap's truncation cap.
	const byteBudget = toolschema.MaxDiscoveryResultBytes
	var output strings.Builder
	output.WriteString("Call these tools with use_tool. Normal permissions apply.\n")
	count := 0
	for _, d := range matches {
		// Use the complete SDK parameter schema; no property/constraint trimming.
		// The example names this exact target instead of another meta-tool.
		encoded, err := json.Marshal(struct {
			*genai.FunctionDeclaration
			Example map[string]any `json:"use_tool_example"`
		}{d, map[string]any{"name": d.Name, "arguments": map[string]any{}}})
		if err != nil || len(encoded)+output.Len()+128 > byteBudget {
			continue
		}
		if count == 4 {
			break
		}
		output.Write(encoded)
		output.WriteByte('\n')
		count++
	}
	if count < len(matches) {
		output.WriteString("More matches omitted; search an exact name or narrower keywords.\n")
	}
	if count == 0 {
		if len(exact) > 0 {
			return NewErrorResult("The matching tool schema exceeds the discovery output limit. Do not repeat this search; choose another tool for this task."), nil
		}
		output.WriteString("No complete matching schema fits. Try another exact name or narrower keywords.\n")
	}
	result := NewSuccessResult(output.String())
	result.completeToolSchema = true
	return result, nil
}
