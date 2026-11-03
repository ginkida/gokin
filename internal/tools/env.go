package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"gokin/internal/security"

	"google.golang.org/genai"
)

// EnvTool provides access to environment variables.
type EnvTool struct{}

// NewEnvTool creates a new EnvTool instance.
func NewEnvTool() *EnvTool {
	return &EnvTool{}
}

func (t *EnvTool) Name() string {
	return "env"
}

func (t *EnvTool) Description() string {
	return EnvToolDeclaration().Description
}

func (t *EnvTool) Declaration() *genai.FunctionDeclaration {
	return EnvToolDeclaration()
}

func (t *EnvTool) Validate(args map[string]any) error {
	return nil
}

// envToolRedactor scrubs values that look like credentials even among the
// allowlisted variables (GOPROXY=https://user:token@proxy, a token pasted into
// GOFLAGS) and every registered secret literal.
var envToolRedactor = security.NewSecretRedactor()

// envToolExposed reports whether a variable is visible through the env tool.
// The tool shows exactly what gokin's commands receive: the SafeEnvVars
// allowlist bash/test/task children are started with. Everything else in
// gokin's own process environment (provider keys, the parent agent's session
// tokens, cloud credentials) is withheld — commands cannot see it either, so
// hiding it costs the model no capability.
func envToolExposed(name string) bool {
	for _, safe := range SafeEnvVars {
		if name == safe {
			return true
		}
	}
	return false
}

func envToolValue(value string) string {
	value = envToolRedactor.Redact(value)
	if runes := []rune(value); len(runes) > 200 {
		value = string(runes[:200]) + "..."
	}
	return value
}

func (t *EnvTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	name := strings.TrimSpace(GetStringDefault(args, "name", ""))
	prefix := GetStringDefault(args, "prefix", "")
	// mask_secrets is no longer part of the schema. Older transcripts may
	// still pass mask_secrets=false; it is ignored — the model never decides
	// whether a credential is revealed.

	if name != "" {
		if !envToolExposed(name) {
			return NewSuccessResult(fmt.Sprintf(
				"%s is not exposed to tools. gokin passes commands only an allowlisted environment (%s), so a command could not read it either. If you need its value, ask the user.",
				name, strings.Join(SafeEnvVars, ", "))), nil
		}
		value, exists := os.LookupEnv(name)
		if !exists {
			return NewSuccessResult(fmt.Sprintf("%s is not set", name)), nil
		}
		if value == "" {
			return NewSuccessResult(fmt.Sprintf("%s=(empty)", name)), nil
		}
		return NewSuccessResult(fmt.Sprintf("%s=%s", name, envToolValue(value))), nil
	}

	names := make([]string, 0, len(SafeEnvVars))
	for _, key := range SafeEnvVars {
		if !envToolExposed(key) {
			continue
		}
		if _, ok := os.LookupEnv(key); !ok {
			continue
		}
		if prefix != "" && !strings.HasPrefix(strings.ToUpper(key), strings.ToUpper(prefix)) {
			continue
		}
		names = append(names, key)
	}
	sort.Strings(names)

	if len(names) == 0 {
		if prefix != "" {
			return NewSuccessResult(fmt.Sprintf("No exposed environment variables with prefix '%s'", prefix)), nil
		}
		return NewSuccessResult("No exposed environment variables are set"), nil
	}

	var builder strings.Builder
	for _, key := range names {
		fmt.Fprintf(&builder, "%s=%s\n", key, envToolValue(os.Getenv(key)))
	}
	header := fmt.Sprintf("Environment visible to commands (%d variables; other variables are withheld):", len(names))
	if prefix != "" {
		header = fmt.Sprintf("Environment visible to commands with prefix '%s' (other variables are withheld):", prefix)
	}
	return NewSuccessResult(header + "\n\n" + builder.String()), nil
}
