package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/lsp"
	"gokin/internal/permission"
	"gokin/internal/security"
	"google.golang.org/genai"
)

type LSPTool struct {
	workDir       string
	pathValidator *security.PathValidator
	mu            sync.RWMutex
	manager       *lsp.Manager
}

func NewLSPTool(workDir string) *LSPTool {
	workDir = canonicalToolWorkDir(workDir)
	return &LSPTool{workDir: workDir, pathValidator: newWorkspacePathValidator(workDir, nil)}
}
func (t *LSPTool) Name() string { return "lsp" }

func (t *LSPTool) SetAllowedDirs(dirs []string) {
	if t.pathValidator != nil {
		t.pathValidator.SetAllowedDirs(append([]string{t.workDir}, dirs...))
	}
}
func (t *LSPTool) Description() string {
	return "Navigate code through plugin language servers: definitions, references, hover types, symbols, implementations and call hierarchies. Positions use one-based lines and Unicode code point columns."
}
func (t *LSPTool) SetManager(manager *lsp.Manager) { t.mu.Lock(); t.manager = manager; t.mu.Unlock() }
func (t *LSPTool) Manager() *lsp.Manager           { t.mu.RLock(); defer t.mu.RUnlock(); return t.manager }
func (t *LSPTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.Description(), Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{
		"operation": {Type: genai.TypeString, Enum: []string{"goToDefinition", "findReferences", "hover", "documentSymbol", "workspaceSymbol", "goToImplementation", "prepareCallHierarchy", "incomingCalls", "outgoingCalls"}},
		"filePath":  {Type: genai.TypeString, Description: "Workspace source file; also selects the language server for workspaceSymbol."},
		"line":      {Type: genai.TypeInteger, Description: "One-based source line; required for position operations."},
		"character": {Type: genai.TypeInteger, Description: "One-based Unicode code point column; required for position operations."},
		"query":     {Type: genai.TypeString, Description: "Symbol search text for workspaceSymbol."},
	}, Required: []string{"operation", "filePath"}}}
}

func (t *LSPTool) Validate(args map[string]any) error {
	op, ok := args["operation"].(string)
	if !ok || !lsp.ValidOperation(op) {
		return fmt.Errorf("unsupported LSP operation")
	}
	path, ok := args["filePath"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return fmt.Errorf("filePath is required")
	}
	if op != "documentSymbol" && op != "workspaceSymbol" {
		for _, key := range []string{"line", "character"} {
			value := GetIntDefault(args, key, 0)
			if value < 1 || value > 2147483647 {
				return fmt.Errorf("%s must be a one-based positive integer", key)
			}
			switch n := args[key].(type) {
			case float64:
				if n != float64(value) {
					return fmt.Errorf("%s must be an integer", key)
				}
			case int, int32, int64:
			default:
				return fmt.Errorf("%s must be an integer", key)
			}
		}
	}
	if op == "workspaceSymbol" {
		query, ok := args["query"].(string)
		if !ok || strings.TrimSpace(query) == "" || len(query) > 4096 {
			return fmt.Errorf("workspaceSymbol requires a query of at most 4096 bytes")
		}
	}
	return nil
}
func (t *LSPTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if err := t.Validate(args); err != nil {
		return NewErrorResult(err.Error()), nil
	}
	path, err := validateWorkspacePath(t.workDir, GetStringDefault(args, "filePath", ""), t.pathValidator)
	if err != nil {
		return NewErrorResult("LSP rejected path: " + err.Error()), nil
	}
	if !lspReadAllowed(ctx, path) {
		return NewPolicyBlockedResult(PolicyBlockPermission, "LSP file is denied by current Read permissions"), nil
	}
	manager := t.Manager()
	if manager == nil {
		return NewErrorResult("No LSP plugin is active for this workspace"), nil
	}
	result, err := manager.Query(ctx, lsp.Request{Operation: GetStringDefault(args, "operation", ""), File: path, Line: GetIntDefault(args, "line", 0), Character: GetIntDefault(args, "character", 0), Query: GetStringDefault(args, "query", "")})
	if err != nil {
		if errors.Is(err, lsp.ErrPolicyChanged) {
			return NewHookStoppedResult(err.Error()), nil
		}
		return NewErrorResult(err.Error()), nil
	}
	var value any
	if err := json.Unmarshal(result.Value, &value); err != nil {
		return NewErrorResult("Invalid LSP result"), nil
	}
	filtered, _, omitted := t.filterResult(ctx, value)
	data := map[string]any{"server": result.Server, "operation": result.Operation, "position_encoding": result.PositionEncoding, "value": filtered, "omitted_locations": omitted}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return NewErrorResult(err.Error()), nil
	}
	// Keep both the human result and structured data bounded; returning an
	// unbounded Data payload would bypass ordinary text-output truncation.
	if len(encoded) > 64<<10 {
		return NewErrorResult("LSP result exceeds 64 KiB; narrow the query or source position"), nil
	}
	return NewSuccessResultWithData(string(encoded), data), nil
}

func (t *LSPTool) filterResult(ctx context.Context, value any) (any, bool, int) {
	omitted := 0
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"uri", "targetUri"} {
			if uri, ok := v[key].(string); ok {
				path, err := lsp.FilePath(uri)
				if err != nil {
					return nil, false, 1
				}
				path, err = validateWorkspacePath(t.workDir, path, t.pathValidator)
				if err != nil || !lspReadAllowed(ctx, path) {
					return nil, false, 1
				}
			}
		}
		out := map[string]any{}
		for key, item := range v {
			filtered, keep, count := t.filterResult(ctx, item)
			omitted += count
			if !keep && (key == "location" || key == "from" || key == "to" || key == "item" || key == "textDocument") {
				return nil, false, omitted
			}
			if keep {
				out[key] = filtered
			} else {
				out[key] = nil
			}
		}
		return out, true, omitted
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			filtered, keep, count := t.filterResult(ctx, item)
			omitted += count
			if keep {
				out = append(out, filtered)
			}
		}
		return out, true, omitted
	default:
		return value, true, 0
	}
}

type lspReadPolicyKey struct{}

type lspPermissionPolicy struct {
	read func(string) bool
	tool func(string) bool
}

// ContextWithLSPReadPolicy applies invocation-local Read denies to locations
// returned by a language server, without prompting for unsolicited locations.
func ContextWithLSPReadPolicy(ctx context.Context, manager *permission.Manager, registry ToolRegistry, workDir string) context.Context {
	policy := func(path string) bool {
		grants, denies := SkillPermissionRules(ctx, registry)
		args := map[string]any{"file_path": path}
		if manager == nil {
			return !permission.TemporaryToolRulesDeny(denies, "read", args, workDir)
		}
		checkCtx := permission.ContextWithoutPermissionPrompt(permission.ContextWithWorkDir(ctx, workDir))
		response, err := manager.CheckWithTemporaryToolRules(checkCtx, "read", args, grants, denies)
		return err == nil && response != nil && response.Allowed
	}
	toolPolicy := func(path string) bool {
		grants, denies := SkillPermissionRules(ctx, registry)
		args := map[string]any{"filePath": path, "operation": "documentSymbol"}
		if manager == nil {
			return !permission.TemporaryToolRulesDeny(denies, "lsp", args, workDir)
		}
		checkCtx := permission.ContextWithoutPermissionPrompt(permission.ContextWithWorkDir(ctx, workDir))
		response, err := manager.CheckWithTemporaryToolRules(checkCtx, "lsp", args, grants, denies)
		return err == nil && response != nil && response.Allowed
	}
	return context.WithValue(ctx, lspReadPolicyKey{}, lspPermissionPolicy{read: policy, tool: toolPolicy})
}
func lspReadAllowed(ctx context.Context, path string) bool {
	policy, _ := ctx.Value(lspReadPolicyKey{}).(lspPermissionPolicy)
	return policy.read == nil || policy.read(path)
}

// AppendLSPDiagnostics uses authoritative changed paths shared by foreground
// and delegated tool loops. Failures are attached to successful mutations so
// the model can distinguish unavailable checks from an explicit clean report.
func AppendLSPDiagnostics(ctx context.Context, registry ToolRegistry, call *genai.FunctionCall, result *ToolResult) {
	if registry == nil || call == nil || result == nil || !result.Success || !shouldRunDeltaCheckCall(call) {
		return
	}
	registered, ok := registry.Get("lsp")
	if !ok {
		return
	}
	tool, ok := registered.(*LSPTool)
	if !ok {
		return
	}
	manager := tool.Manager()
	if manager == nil {
		return
	}
	if ceiling, restricted := ToolCapabilityCeilingFromContext(ctx); restricted && !toolCapabilityAllows(ceiling, "lsp") {
		return
	}
	mutation := snapshotGoMutation(*result)
	paths := selfReviewMutationPaths(call, mutation)
	checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var lines []string
	checked := 0
	for _, raw := range paths {
		path, err := validateWorkspacePath(tool.workDir, raw, tool.pathValidator)
		policy, _ := ctx.Value(lspReadPolicyKey{}).(lspPermissionPolicy)
		if err != nil || !manager.HasFile(path) || !lspReadAllowed(ctx, path) || policy.tool != nil && !policy.tool(path) {
			continue
		}
		_, denies := SkillPermissionRules(ctx, registry)
		if permission.TemporaryToolRulesDeny(denies, "lsp", map[string]any{"filePath": path}, tool.workDir) {
			continue
		}
		if checked >= 32 {
			lines = append(lines, "Additional changed files were not checked (32-file limit).")
			break
		}
		checked++
		report, err := manager.Diagnose(checkCtx, path)
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: diagnostics unavailable: %s", filepath.Base(path), err))
			continue
		}
		if !report.Available {
			continue
		}
		for _, issue := range report.Diagnostics {
			if issue.Severity > 2 {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s:%d:%d: %s", filepath.Base(path), issue.Range.Start.Line+1, issue.Range.Start.Character+1, issue.Message))
		}
	}
	if len(lines) == 0 {
		return
	}
	message := "LSP diagnostics:\n" + strings.Join(lines, "\n")
	if len(message) > 6000 {
		message = message[:6000] + "\n[truncated]"
	}
	if result.Content != "" {
		result.Content += "\n\n"
	}
	result.Content += message
}
