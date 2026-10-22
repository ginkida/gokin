package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func parseHTTPTestDefinition(t *testing.T, handler string) ([]ClaudeCommand, error) {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(`{"PreToolUse":[{"hooks":[`+handler+`]}]}`), &document); err != nil {
		t.Fatal(err)
	}
	return ParseClaudeHooks(*document.Content[0])
}

func TestHTTPHookSchemaAndFingerprint(t *testing.T) {
	if !validHTTPHookOutput([]byte(`{"decision":"bl\u006fck","reason":"check"}`)) {
		t.Fatal("valid escaped JSON decision was rejected")
	}
	for _, handler := range []string{
		`{"type":"http","url":"file:///private"}`,
		`{"type":"http","url":"https://user:secret@example.test"}`,
		`{"type":"http","url":"https://example.test/#fragment"}`,
		`{"type":"http","url":"https://example.test","args":[]}`,
		`{"type":"http","url":"https://example.test","prompt":"x"}`,
		`{"type":"http","url":"https://example.test","headers":{"X":1}}`,
		`{"type":"http","url":"https://example.test","headers":{"X":"a","x":"b"}}`,
		`{"type":"http","url":"https://example.test","headers":{"Host":"elsewhere"}}`,
		`{"type":"http","url":"https://example.test","headers":{"X":"a\r\nb"}}`,
		`{"type":"http","url":"https://example.test","allowedEnvVars":["INVALID.NAME"]}`,
		`{"type":"http","url":"https://example.test","allowedEnvVars":[1]}`,
		`{"type":"http","url":"https://example.test","onFailure":"allow"}`,
		`{"type":"command","command":"true","url":"https://example.test"}`,
		`{"type":"prompt","prompt":"check","onFailure":"block"}`,
	} {
		if _, err := parseHTTPTestDefinition(t, handler); err == nil {
			t.Errorf("accepted invalid HTTP definition: %s", handler)
		}
	}
	definitions, err := parseHTTPTestDefinition(t, `{"type":"http","url":"https://example.test","headers":{"X":"$TOKEN"},"allowedEnvVars":["TOKEN"],"once":true,"onFailure":"block"}`)
	if err != nil {
		t.Fatal(err)
	}
	if definitions[0].Timeout != 600*time.Second || !definitions[0].Once {
		t.Fatalf("wrong defaults: %+v", definitions)
	}
	hash := CommandFingerprint(definitions)
	for _, mutate := range []func(*ClaudeCommand){
		func(h *ClaudeCommand) { h.URL += "/other" },
		func(h *ClaudeCommand) { h.Headers["X"] = "other" },
		func(h *ClaudeCommand) { h.AllowedEnvVars[0] = "OTHER" },
		func(h *ClaudeCommand) { h.OnFailure = "continue" },
	} {
		copy := CloneClaudeCommands(definitions)
		mutate(&copy[0])
		if CommandFingerprint(copy) == hash || CommandFingerprint(definitions) != hash {
			t.Fatal("HTTP fingerprint or defensive copy lost a field")
		}
	}
}

func TestHTTPHookDecisionsAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, body, failure string
		status              int
		block, stop, failed bool
	}{
		{"empty", "", "", 204, false, false, false},
		{"allow", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"file_path":"safe"},"additionalContext":"checked"}}`, "", 200, false, false, false},
		{"deny", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"policy"}}`, "", 200, true, false, false},
		{"stop", `{"continue":false,"stopReason":"finished"}`, "", 200, false, true, false},
		{"server-error", `{"continue":false}`, "", 503, false, false, true},
		{"closed-server-error", `{"hookSpecificOutput":{"permissionDecision":"allow"}}`, "block", 503, true, false, true},
		{"text", "not JSON", "", 200, false, false, true},
		{"closed-text", "not JSON", "block", 200, true, false, true},
		{"duplicate", `{"continue":true,"continue":false}`, "block", 200, true, false, true},
		{"null", `{"continue":null}`, "block", 200, true, false, true},
		{"array", `[]`, "", 200, false, false, true},
		{"wrong-event", `{"hookSpecificOutput":{"hookEventName":"Stop","permissionDecision":"allow"}}`, "", 200, false, false, true},
		{"wrong-decision", `{"decision":"allow"}`, "", 200, false, false, true},
		{"oversize", strings.Repeat("x", maxHookOutputBytes+1), "block", 200, true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			inputs := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				_ = json.NewDecoder(r.Body).Decode(&input)
				input["method"], input["content_type"] = r.Method, r.Header.Get("Content-Type")
				inputs <- input
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			manager := NewManager(true, t.TempDir())
			scope := &Scope{}
			registration, err := scope.Prepare("http", []ClaudeCommand{{Event: "PreToolUse", Handler: "http", URL: server.URL, Timeout: time.Second, Once: true, OnFailure: test.failure}})
			if err != nil {
				t.Fatal(err)
			}
			registration.Commit()
			ctx := ContextWithRuntimeInfo(ContextWithScope(t.Context(), scope), RuntimeInfo{SessionID: "session", ToolUseID: "call"})
			results := manager.RunPreTool(ctx, "write", map[string]any{"file_path": "original"})
			if len(results) != 1 {
				t.Fatalf("missing result: %+v", results)
			}
			result := results[0]
			if (result.Error != nil) != test.failed || (result.BlockReason != "") != test.block || (result.StopReason != "") != test.stop {
				t.Fatalf("wrong HTTP control: %+v", result)
			}
			if registration.Consumed()[0] == test.failed {
				t.Fatal("once consumed after failure or not consumed after success")
			}
			input := <-inputs
			if input["method"] != "POST" || input["content_type"] != "application/json" || input["hook_event_name"] != "PreToolUse" || input["tool_name"] != "Write" || input["tool_use_id"] != "call" || input["session_id"] != "session" {
				t.Fatalf("wrong HTTP input: %+v", input)
			}
			if test.name == "allow" && (result.UpdatedInput["file_path"] != "safe" || result.AdditionalContext != "checked") {
				t.Fatalf("missing HTTP controls: %+v", result)
			}
		})
	}
}

func TestHTTPHookPolicyHeadersRefreshAndOnce(t *testing.T) {
	t.Setenv("HTTP_HOOK_TOKEN", "secret")
	t.Setenv("HTTP_HOOK_OTHER", "hidden")
	headers := make(chan http.Header, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { headers <- r.Header.Clone(); w.WriteHeader(204) }))
	defer server.Close()
	root, user := t.TempDir(), t.TempDir()
	manager := NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, true)
	path := filepath.Join(user, "settings.json")
	writeClaudeSettingsTestFile(t, path, map[string]any{"allowedHttpHookUrls": []string{}, "httpHookAllowedEnvVars": []string{"HTTP_HOOK_TOKEN"}})
	scope := &Scope{}
	registration, err := scope.Prepare("http", []ClaudeCommand{{Event: "PreToolUse", Handler: "http", URL: server.URL, Timeout: time.Second, Once: true, OnFailure: "block", Headers: map[string]string{"Authorization": "Bearer ${HTTP_HOOK_TOKEN}", "X-Other": "$HTTP_HOOK_OTHER", "X-Unlisted": "$HOME"}, AllowedEnvVars: []string{"HTTP_HOOK_TOKEN", "HTTP_HOOK_OTHER"}}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	ctx := ContextWithScope(t.Context(), scope)
	result := manager.RunPreTool(ctx, "write", nil)
	if len(result) != 1 || result[0].Error != nil || !result[0].skipped || registration.Consumed()[0] || len(headers) != 0 {
		t.Fatalf("denied URL ran or consumed once: %+v", result)
	}
	writeClaudeSettingsTestFile(t, path, map[string]any{"allowedHttpHookUrls": []string{server.URL + "*"}, "httpHookAllowedEnvVars": []string{"HTTP_HOOK_TOKEN"}})
	result = manager.RunPreTool(ctx, "write", nil)
	if len(result) != 1 || result[0].Error != nil || !registration.Consumed()[0] {
		t.Fatalf("refresh failed: %+v", result)
	}
	got := <-headers
	if got.Get("Authorization") != "Bearer secret" || got.Get("X-Other") != "" || got.Get("X-Unlisted") != "" {
		t.Fatalf("environment escaped allowlist: %+v", got)
	}
	if results := manager.RunPreTool(ctx, "write", nil); len(results) != 0 {
		t.Fatal("once ran twice")
	}
	if !((httpHookPolicy{urls: []string{"https://HOOKS.EXAMPLE./Case/*"}}).allowsURL("https://hooks.example/Case/path")) || (httpHookPolicy{urls: []string{"https://hooks.example/Case/*"}}).allowsURL("https://hooks.example/case/path") {
		t.Fatal("URL matching lost DNS normalization or path case")
	}
	if got := (httpHookPolicy{}).headerValue("${user_config.token}:$HTTP_HOOK_TOKEN", []string{"HTTP_HOOK_TOKEN"}); got != "${user_config.token}:secret" {
		t.Fatal("environment interpolation swallowed plugin option")
	}
}

func TestHTTPHookRedirectCancellationAndTrust(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			_, _ = io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	manager := NewManager(true, t.TempDir())
	for _, path := range []string{"/redirect", "/slow"} {
		hook := &Hook{Enabled: true, Type: PreTool, Claude: &ClaudeCommand{Handler: "http", Event: "PreToolUse", URL: server.URL + path, OnFailure: "block"}}
		result := manager.executeHook(t.Context(), hook, NewContext("write", nil, manager.workDir), 30*time.Millisecond)
		if result.Error == nil || result.BlockReason == "" || leaked.Load() {
			t.Fatalf("redirect leaked or failure allowed: %+v", result)
		}
		if path == "/slow" && !errors.Is(result.Error, context.DeadlineExceeded) {
			t.Fatalf("lost timeout: %v", result.Error)
		}
	}
	root, user := t.TempDir(), t.TempDir()
	manager = NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(root, ".claude", "settings.json"), map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "http", "url": target.URL}}}}}})
	manager.ConfigureClaudeSettings(root, user, false)
	if results := manager.RunPreTool(t.Context(), "write", nil); len(results) != 0 || leaked.Load() {
		t.Fatalf("untrusted HTTP hook ran: %+v", results)
	}
	ctx := ContextWithPromptClient(t.Context(), nil, func() error { return errors.New("skill trust revoked") })
	result := manager.executeHook(ctx, &Hook{Enabled: true, Type: PreTool, Claude: &ClaudeCommand{Handler: "http", Event: "PreToolUse", URL: target.URL}}, NewContext("write", nil, root), time.Second)
	if result.Error == nil || leaked.Load() {
		t.Fatal("revoked owner guard allowed HTTP request")
	}
}

func TestHTTPHookManagedAllowlistAndFailureStopSemantics(t *testing.T) {
	m, _, user, managed := managedTestManager(t, true, true)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"decision":"block","reason":"keep working"}`)
	}))
	defer server.Close()
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"allowManagedHooksOnly": true, "hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "http", "url": server.URL, "onFailure": "block"}}}}}})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"allowedHttpHookUrls": []string{}})
	results := m.RunStop(t.Context(), "done")
	if len(results) != 1 || results[0].Error != nil || calls.Load() != 0 {
		t.Fatalf("managed hook bypassed URL restriction: %+v", results)
	}
	// Native hooks may be disabled while mandatory hooks remain active.
	m.SetEnabled(false)
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "http", "url": server.URL, "onFailure": "block"}}}}}})
	results = m.RunStop(t.Context(), "done")
	if len(results) != 1 || results[0].Error != nil || calls.Load() != 0 {
		t.Fatalf("disabled native hooks bypassed HTTP restriction: %+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"allowedHttpHookUrls": []string{server.URL}})
	results = m.RunStop(t.Context(), "done")
	if len(results) != 1 || results[0].BlockReason != "keep working" || calls.Load() != 1 {
		t.Fatalf("Stop HTTP verdict lost: %+v", results)
	}
	server.Close()
	results = m.RunStop(t.Context(), "done")
	if len(results) != 1 || results[0].Error == nil || results[0].BlockReason != "" || results[0].StopReason != "" {
		t.Fatalf("onFailure incorrectly forced Stop continuation: %+v", results)
	}
}

func TestHTTPHookPluginHeadersAndEnvironmentInjection(t *testing.T) {
	headers := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Get("X-Plugin")
		io.WriteString(w, `{"decision":"block","reason":"review post result"}`)
	}))
	defer server.Close()
	m, _, _, _ := managedTestManager(t, true, true)
	plugin := pluginTestRoot(t, "http-review", "")
	definition := map[string]any{"type": "http", "url": server.URL, "headers": map[string]string{"X-Plugin": "$HTTP_PLUGIN_UNLISTED"}, "allowedEnvVars": []string{}}
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, "hooks", "hooks.json"), map[string]any{"hooks": map[string]any{"PostToolUse": []any{map[string]any{"hooks": []any{definition}}}}})
	t.Setenv("HTTP_PLUGIN_UNLISTED", "secret")
	m.ConfigureClaudePlugins([]string{plugin})
	results := m.RunPostTool(t.Context(), "write", nil, "done")
	if len(results) != 1 || results[0].Error != nil || results[0].AdditionalContext != "review post result" {
		t.Fatalf("plugin HTTP result: %+v", results)
	}
	if got := <-headers; got != "" {
		t.Fatalf("unlisted env placeholder was interpolated: %q", got)
	}
	t.Setenv("HTTP_HEADER_INJECTION", "a\r\nX-Injected: true")
	hook := &Hook{Type: PreTool, Enabled: true, Claude: &ClaudeCommand{Handler: "http", Event: "PreToolUse", URL: server.URL, Headers: map[string]string{"X-Value": "$HTTP_HEADER_INJECTION"}, AllowedEnvVars: []string{"HTTP_HEADER_INJECTION"}, OnFailure: "block"}}
	result := m.executeHook(t.Context(), hook, NewContext("write", nil, m.workDir), time.Second)
	if result.Error == nil || result.BlockReason == "" || len(headers) != 0 {
		t.Fatalf("expanded header injection allowed: %+v", result)
	}
}

func TestClaudeCommandOnFailureAndHTTPPolicySchema(t *testing.T) {
	m := NewManager(true, t.TempDir())
	for _, test := range []struct {
		command string
		block   bool
		reason  string
	}{
		{"exit 1", true, ""},
		{"printf '{\"continue\":null}'", true, ""},
		{"printf ordinary-output", false, ""},
		{"printf explicit-denial >&2; exit 2", true, "explicit-denial"},
	} {
		hook := &Hook{Type: PreTool, Enabled: true, Command: test.command, Claude: &ClaudeCommand{Event: "PreToolUse", Command: test.command, OnFailure: "block"}}
		result := m.executeHook(t.Context(), hook, NewContext("write", nil, m.workDir), time.Second)
		if (result.BlockReason != "") != test.block || test.reason != "" && result.BlockReason != test.reason {
			t.Fatalf("command onFailure lost verdict: %+v", result)
		}
	}
	for _, text := range []string{`{"allowedHttpHookUrls":null}`, `{"httpHookAllowedEnvVars":["INVALID.NAME"]}`, `{"allowedHttpHookUrls":[1]}`, `{"httpHookAllowedEnvVars":true}`} {
		if _, err := settingsFields([]byte(text), false); err == nil {
			t.Fatalf("invalid HTTP policy accepted: %s", text)
		}
	}
}
