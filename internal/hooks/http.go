package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"
	"gopkg.in/yaml.v3"
)

var httpHookEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var httpHookEnvReference = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))`)

// Nil lists mean unrestricted; non-nil empty lists deny everything.
type httpHookPolicy struct{ urls, env []string }

func validateHTTPHook(h ClaudeCommand) error {
	if _, err := parseHTTPHookURL(h.URL); err != nil {
		return err
	}
	if len(h.Headers) > 64 || len(h.AllowedEnvVars) > 64 {
		return fmt.Errorf("HTTP hook exceeds 64 headers or environment variables")
	}
	seen := map[string]bool{}
	for name, value := range h.Headers {
		key := strings.ToLower(name)
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(value) || len(value) > 8192 || seen[key] || key == "host" || key == "content-length" || key == "transfer-encoding" || key == "connection" {
			return fmt.Errorf("invalid HTTP hook header")
		}
		seen[key] = true
	}
	for _, name := range h.AllowedEnvVars {
		if !httpHookEnvName.MatchString(name) || len(name) > 256 {
			return fmt.Errorf("invalid HTTP hook environment variable name")
		}
	}
	return nil
}

func parseHTTPHookURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n\t ") || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("HTTP hook requires an absolute http or https URL without credentials or fragment")
	}
	return u, nil
}

func normalizedHTTPHookURL(value string) string {
	// Normalize only DNS semantics, preserving case-sensitive paths and queries.
	start := strings.Index(value, "://")
	if start < 0 {
		return value
	}
	start += 3
	end := start + len(value[start:])
	if i := strings.IndexAny(value[start:], "/?#"); i >= 0 {
		end = start + i
	}
	authority := strings.ToLower(value[start:end])
	if !strings.HasPrefix(authority, "[") {
		host, port, ok := strings.Cut(authority, ":")
		authority = strings.TrimSuffix(host, ".")
		if ok {
			authority += ":" + port
		}
	}
	return value[:start] + authority + value[end:]
}

func (p httpHookPolicy) allowsURL(value string) bool {
	if p.urls == nil {
		return true
	}
	value = normalizedHTTPHookURL(value)
	for _, pattern := range p.urls {
		expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(normalizedHTTPHookURL(pattern)), `\*`, ".*") + "$"
		if matched, _ := regexp.MatchString(expression, value); matched {
			return true
		}
	}
	return false
}

func (p httpHookPolicy) headerValue(value string, allowed []string) string {
	names := map[string]bool{}
	for _, name := range allowed {
		if p.env == nil {
			names[name] = true
			continue
		}
		for _, permitted := range p.env {
			if name == permitted {
				names[name] = true
				break
			}
		}
	}
	return httpHookEnvReference.ReplaceAllStringFunc(value, func(reference string) string {
		parts := httpHookEnvReference.FindStringSubmatch(reference)
		name := parts[1]
		if name == "" {
			name = parts[2]
		}
		if names[name] {
			return os.Getenv(name)
		}
		return ""
	})
}

func (m *Manager) executeHTTPHook(ctx context.Context, hook *Hook, hctx *Context, timeout time.Duration) (result Result) {
	started := time.Now()
	result.Hook = hook
	defer func() { result.Elapsed = time.Since(started) }()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		result.Error = err
		return
	}
	revision := m.ExecutionRevision()
	if err := m.CheckReady(); err != nil {
		result.Error = err
		return
	}
	if !m.HookEnabled(hook) {
		result.Error = fmt.Errorf("HTTP hook is no longer enabled")
		return
	}
	// The owner also checks skill trust and its current catalog at request time.
	if binding, ok := ctx.Value(promptClientKey{}).(promptClientBinding); ok && binding.guard != nil {
		if err := binding.guard(); err != nil {
			result.Error = err
			return
		}
	}
	data, info, err := claudeInput(ctx, hook, hctx)
	if err != nil {
		result.Error = err
		return
	}
	target := pluginHookVariables(hook.Claude.URL, info, hook.claudePlugin)
	if _, err := parseHTTPHookURL(target); err != nil {
		result.Error = err
		return
	}
	m.mu.RLock()
	policy := m.claudeHTTPPolicy
	m.mu.RUnlock()
	if !policy.allowsURL(target) {
		result.skipped = true
		return
	} // Policy skips do not run or block the action.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		result.Error = fmt.Errorf("cannot create HTTP hook request")
		return
	}
	for name, value := range hook.Claude.Headers {
		value = pluginHookVariables(policy.headerValue(value, hook.Claude.AllowedEnvVars), info, hook.claudePlugin)
		if len(value) > 8192 || !httpguts.ValidHeaderFieldValue(value) {
			result.Error = fmt.Errorf("invalid expanded HTTP hook header")
			return
		}
		req.Header.Set(name, value)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := m.CheckExecutionRevision(revision); err != nil {
		result.Error = err
		return
	}
	// Never forward hook payloads or credentials to an unreviewed redirect target.
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(req)
	if err != nil {
		// net/url errors contain the URL, which may include plugin secrets.
		if ctx.Err() != nil {
			result.Error = ctx.Err()
		} else {
			var timeoutError net.Error
			if errors.As(err, &timeoutError) && timeoutError.Timeout() {
				result.Error = fmt.Errorf("HTTP hook request timed out")
			} else {
				result.Error = fmt.Errorf("HTTP hook connection failed")
			}
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.Error = fmt.Errorf("HTTP hook returned status %d", response.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHookOutputBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			result.Error = ctx.Err()
		} else {
			result.Error = fmt.Errorf("cannot read HTTP hook response")
		}
		return
	}
	if len(body) > maxHookOutputBytes {
		result.Error = fmt.Errorf("HTTP hook response exceeds 64 KiB")
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return
	}
	if !validHTTPHookOutput(body) {
		result.Error = fmt.Errorf("HTTP hook response must be a valid hook JSON object")
		return
	}
	parsed := interpretClaudeResult(result, string(body), "", 0)
	if parsed.Error != nil {
		result.Error = fmt.Errorf("HTTP hook response has invalid decision fields")
		return
	}
	return parsed
}

func validHTTPHookOutput(body []byte) bool {
	if len(body) == 0 || body[0] != '{' || !json.Valid(body) {
		return false
	}
	var document yaml.Node
	if yaml.Unmarshal(body, &document) != nil || validateSettingsNode(&document, 0) != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return false
	}
	stringFields := []string{"stopReason", "decision", "reason", "systemMessage", "terminalSequence"}
	if !validHTTPHookFields(fields, stringFields, []string{"continue", "suppressOutput"}) {
		return false
	}
	if raw, ok := fields["decision"]; ok {
		var decision string
		if json.Unmarshal(raw, &decision) != nil || decision != "block" {
			return false
		}
	}
	if raw, ok := fields["hookSpecificOutput"]; ok {
		var specific map[string]json.RawMessage
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &specific) != nil {
			return false
		}
		if !validHTTPHookFields(specific, []string{"hookEventName", "permissionDecision", "permissionDecisionReason", "additionalContext", "sessionTitle"}, []string{"suppressOriginalPrompt"}) {
			return false
		}
		if raw, ok := specific["updatedInput"]; ok {
			var object map[string]any
			if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &object) != nil {
				return false
			}
		}
	}
	return true
}

func validHTTPHookFields(fields map[string]json.RawMessage, stringsList, bools []string) bool {
	for _, name := range stringsList {
		if raw, ok := fields[name]; ok {
			var value string
			if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
				return false
			}
		}
	}
	for _, name := range bools {
		if raw, ok := fields[name]; ok {
			var value bool
			if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &value) != nil {
				return false
			}
		}
	}
	return true
}

func parseHTTPHookPolicyList(node *yaml.Node, name string) ([]string, error) {
	if node.Kind != yaml.SequenceNode || len(node.Content) > 256 {
		return nil, fmt.Errorf("%s must be a list of at most 256 strings", name)
	}
	values := make([]string, 0, len(node.Content))
	for _, value := range node.Content {
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" || len(value.Value) > 8192 || strings.ContainsAny(value.Value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid %s entry", name)
		}
		if name == "httpHookAllowedEnvVars" && (!httpHookEnvName.MatchString(value.Value) || len(value.Value) > 256) {
			return nil, fmt.Errorf("invalid HTTP hook environment variable name")
		}
		values = append(values, value.Value)
	}
	return values, nil
}

func ignoredSettingsHTTPPolicy(data []byte) (httpHookPolicy, error) {
	var policy httpHookPolicy
	if !json.Valid(data) {
		return policy, nil
	}
	var document yaml.Node
	if yaml.Unmarshal(data, &document) != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return policy, nil
	}
	root := document.Content[0]
	seen := map[string]bool{}
	for i := 0; i < len(root.Content); i += 2 {
		name := root.Content[i].Value
		if name != "allowedHttpHookUrls" && name != "httpHookAllowedEnvVars" {
			continue
		}
		if seen[name] {
			return policy, fmt.Errorf("duplicate HTTP hook policy field")
		}
		seen[name] = true
		values, err := parseHTTPHookPolicyList(root.Content[i+1], name)
		if err != nil {
			return policy, err
		}
		if name == "allowedHttpHookUrls" {
			policy.urls = values
		} else {
			policy.env = values
		}
	}
	return policy, nil
}
