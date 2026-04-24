package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"

	"gopkg.in/yaml.v3"
)

// newFakeProvider builds a ProviderDef pointing at a local httptest server
// so validation tests don't hit real provider endpoints.
func newFakeProvider(t *testing.T, url string, successStatuses []int) *config.ProviderDef {
	t.Helper()
	return &config.ProviderDef{
		Name:         "fake-test",
		DisplayName:  "Fake",
		DefaultModel: "fake-model",
		EnvVars:      []string{"FAKE_TEST_KEY"},
		GetKey:       func(api *config.APIConfig) string { return "" },
		SetKey:       func(api *config.APIConfig, key string) {},
		KeyValidation: config.KeyValidationDef{
			URL:             url,
			AuthMode:        "bearer",
			SuccessStatuses: successStatuses,
		},
	}
}

func TestValidateWithProviderConfig_BadKeyOn401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, nil)
	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, "sk-whatever-1234567890")
	if ve == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	if ve.Kind != ValidationBadKey {
		t.Errorf("Kind = %v, want ValidationBadKey", ve.Kind)
	}
	if ve.StatusCode != 401 {
		t.Errorf("StatusCode = %d, want 401", ve.StatusCode)
	}
}

func TestValidateWithProviderConfig_BadKeyOn403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, nil)
	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, "sk-whatever-1234567890")
	if ve == nil || ve.Kind != ValidationBadKey {
		t.Fatalf("expected BadKey, got %+v", ve)
	}
}

func TestValidateWithProviderConfig_TransientOn500(t *testing.T) {
	// 5xx is not a bad-key signal — the server just had a hiccup.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, nil)
	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, "sk-whatever-1234567890")
	if ve == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	if ve.Kind != ValidationTransient {
		t.Errorf("Kind = %v, want ValidationTransient (500 should not hard-fail setup)", ve.Kind)
	}
}

func TestValidateWithProviderConfig_TransientOn404NotInAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, nil) // 404 not in SuccessStatuses
	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, "sk-whatever-1234567890")
	if ve == nil || ve.Kind != ValidationTransient {
		t.Fatalf("expected Transient for 404, got %+v", ve)
	}
}

func TestValidateWithProviderConfig_Ok200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("missing Bearer prefix: %q", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, nil)
	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, "sk-whatever-1234567890")
	if ve != nil {
		t.Fatalf("expected nil on 200, got %+v", ve)
	}
}

func TestValidateWithProviderConfig_Ok404InAllowlist(t *testing.T) {
	// Mirrors MiniMax's real-world behaviour where /v1/models returns 404
	// on an Anthropic-compat gateway even with a good key.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, []int{200, 404})
	ve := validateWithProviderConfig(contextWithDeadline(t, 2*time.Second), p, "sk-whatever-1234567890")
	if ve != nil {
		t.Fatalf("expected nil when 404 is allowlisted, got %+v", ve)
	}
}

func TestValidateWithProviderConfig_TransientOnTimeout(t *testing.T) {
	// Server hangs past the validator's deadline; classify as transient,
	// not bad-key. Directly guards the "hang" regression the user hit.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newFakeProvider(t, srv.URL, nil)
	ve := validateWithProviderConfig(contextWithDeadline(t, 200*time.Millisecond), p, "sk-whatever-1234567890")
	if ve == nil {
		t.Fatal("expected timeout to produce ValidationError")
	}
	if ve.Kind != ValidationTransient {
		t.Errorf("Kind = %v, want ValidationTransient on timeout", ve.Kind)
	}
}

func TestValidateWithProviderConfig_TransientOnConnectionRefused(t *testing.T) {
	// Spin up then immediately close to get a guaranteed-unreachable URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	p := newFakeProvider(t, url, nil)
	ve := validateWithProviderConfig(contextWithDeadline(t, 1*time.Second), p, "sk-whatever-1234567890")
	if ve == nil || ve.Kind != ValidationTransient {
		t.Fatalf("expected Transient on connection refused, got %+v", ve)
	}
}

func TestValidateAPIKey_UnknownProvider(t *testing.T) {
	ve := validateAPIKey("nope-no-such-provider", "sk-whatever-1234567890")
	if ve == nil {
		t.Fatal("expected error for unknown provider")
	}
	if ve.Kind != ValidationTransient {
		t.Errorf("Kind = %v, want ValidationTransient for unknown provider", ve.Kind)
	}
}

func TestShouldSkipValidation_Toggles(t *testing.T) {
	for _, val := range []string{"1", "true", "yes", "YES", "True"} {
		t.Setenv("GOKIN_SKIP_VALIDATION", val)
		if !shouldSkipValidation() {
			t.Errorf("GOKIN_SKIP_VALIDATION=%q should skip", val)
		}
	}
	for _, val := range []string{"", "0", "false", "no"} {
		t.Setenv("GOKIN_SKIP_VALIDATION", val)
		if shouldSkipValidation() {
			t.Errorf("GOKIN_SKIP_VALIDATION=%q should NOT skip", val)
		}
	}
}

func TestValidationError_Unwrap(t *testing.T) {
	inner := errors.New("boom")
	ve := &ValidationError{Kind: ValidationTransient, Err: inner}
	if !errors.Is(ve, inner) {
		t.Error("errors.Is should find wrapped error")
	}
	if ve.Error() != "boom" {
		t.Errorf("Error() = %q, want %q", ve.Error(), "boom")
	}
}

func TestSaveProviderConfig_PreservesExistingFields(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	configPath := filepath.Join(tmp, "gokin", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Pre-seed config with fields the wizard must not clobber.
	existing := `api:
  glm_key: existing-glm-key-xxxxxxxxxxxxxxxxxx
  backend: glm
  active_provider: glm
model:
  provider: glm
  name: glm-5.1
  temperature: 0.42
tools:
  allowed_dirs:
    - /Users/alice/projects
mcp:
  servers:
    my-server:
      command: node
      args: ["server.js"]
`
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	savedPath, err := saveProviderConfig("kimi", "sk-kimi-newnewnewnewnewnewnewnew", "")
	if err != nil {
		t.Fatalf("saveProviderConfig: %v", err)
	}
	if savedPath != configPath {
		t.Errorf("savedPath = %q, want %q", savedPath, configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	api, _ := root["api"].(map[string]any)
	if api == nil {
		t.Fatal("api section missing after save")
	}
	if api["kimi_key"] != "sk-kimi-newnewnewnewnewnewnewnew" {
		t.Errorf("kimi_key not saved: got %v", api["kimi_key"])
	}
	if api["glm_key"] != "existing-glm-key-xxxxxxxxxxxxxxxxxx" {
		t.Errorf("glm_key was clobbered! this is the exact regression we're guarding: got %v", api["glm_key"])
	}
	if api["active_provider"] != "kimi" {
		t.Errorf("active_provider not updated: got %v", api["active_provider"])
	}
	if api["backend"] != "kimi" {
		t.Errorf("backend not updated: got %v", api["backend"])
	}

	model, _ := root["model"].(map[string]any)
	if model == nil {
		t.Fatal("model section missing after save")
	}
	if model["provider"] != "kimi" {
		t.Errorf("model.provider not updated: got %v", model["provider"])
	}
	if model["name"] != "kimi-for-coding" {
		t.Errorf("model.name not set to default: got %v", model["name"])
	}
	if model["temperature"] != 0.42 {
		t.Errorf("model.temperature was clobbered: got %v", model["temperature"])
	}

	tools, _ := root["tools"].(map[string]any)
	if tools == nil {
		t.Fatal("tools section was dropped")
	}
	if dirs, _ := tools["allowed_dirs"].([]any); len(dirs) != 1 || dirs[0] != "/Users/alice/projects" {
		t.Errorf("tools.allowed_dirs was clobbered: got %v", tools["allowed_dirs"])
	}

	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		t.Fatal("mcp section was dropped")
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil || servers["my-server"] == nil {
		t.Errorf("mcp.servers.my-server was clobbered: got %v", servers)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("permissions = %v, want 0600", mode)
	}
}

func TestSaveProviderConfig_FreshInstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	savedPath, err := saveProviderConfig("kimi", "sk-kimi-fresh-install-12345", "kimi-for-coding")
	if err != nil {
		t.Fatalf("saveProviderConfig: %v", err)
	}

	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	api, _ := root["api"].(map[string]any)
	if api["kimi_key"] != "sk-kimi-fresh-install-12345" {
		t.Errorf("kimi_key not saved on fresh install: got %v", api["kimi_key"])
	}
}

func TestSaveProviderConfig_UnknownProviderFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := saveProviderConfig("no-such-provider", "sk-whatever-1234567890", ""); err == nil {
		t.Error("expected error for unknown provider")
	}
}

// Regression: before this fix, setupOllamaLocal wrote config.yaml with a
// hand-built YAML literal and raw os.WriteFile, which silently clobbered
// every non-Ollama section the user already had configured (GLM/Kimi/
// DeepSeek keys, MCP servers, custom aliases). saveOllamaConfig must
// round-trip unrelated fields.
func TestSaveOllamaConfig_Local_PreservesExistingConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	configPath := filepath.Join(tmp, "gokin", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	existing := `api:
  glm_key: keep-this-glm-key-1234567890
  kimi_key: keep-this-kimi-key-1234567890
  deepseek_key: keep-this-deepseek-key-1234567890
  active_provider: kimi
model:
  name: kimi-for-coding
  provider: kimi
  temperature: 0.42
tools:
  allowed_dirs:
    - /Users/alice/projects
mcp:
  servers:
    my-server:
      command: node
      args: ["server.js"]
`
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	savedPath, err := saveOllamaConfig("", "llama3.2", "")
	if err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}
	if savedPath != configPath {
		t.Errorf("savedPath = %q, want %q", savedPath, configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	api, _ := root["api"].(map[string]any)
	if api == nil {
		t.Fatal("api section missing after save")
	}
	if api["glm_key"] != "keep-this-glm-key-1234567890" {
		t.Errorf("glm_key was clobbered by Ollama setup: got %v", api["glm_key"])
	}
	if api["kimi_key"] != "keep-this-kimi-key-1234567890" {
		t.Errorf("kimi_key was clobbered: got %v", api["kimi_key"])
	}
	if api["deepseek_key"] != "keep-this-deepseek-key-1234567890" {
		t.Errorf("deepseek_key was clobbered: got %v", api["deepseek_key"])
	}
	if api["active_provider"] != "ollama" {
		t.Errorf("active_provider not updated: got %v", api["active_provider"])
	}
	if api["backend"] != "ollama" {
		t.Errorf("backend not updated: got %v", api["backend"])
	}
	// Local mode (empty apiKey) must NOT set ollama_key.
	if _, hasKey := api["ollama_key"]; hasKey {
		t.Errorf("local ollama mode must not set ollama_key, got %v", api["ollama_key"])
	}
	// Empty serverURL means "use default localhost" — don't write the field.
	if _, hasURL := api["ollama_base_url"]; hasURL {
		t.Errorf("default server URL should leave ollama_base_url unset, got %v", api["ollama_base_url"])
	}

	model, _ := root["model"].(map[string]any)
	if model == nil {
		t.Fatal("model section missing")
	}
	if model["provider"] != "ollama" {
		t.Errorf("model.provider not updated: got %v", model["provider"])
	}
	if model["name"] != "llama3.2" {
		t.Errorf("model.name not set: got %v", model["name"])
	}

	tools, _ := root["tools"].(map[string]any)
	if tools == nil {
		t.Fatal("tools section was dropped")
	}
	if dirs, _ := tools["allowed_dirs"].([]any); len(dirs) != 1 || dirs[0] != "/Users/alice/projects" {
		t.Errorf("tools.allowed_dirs was clobbered: got %v", tools["allowed_dirs"])
	}

	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		t.Fatal("mcp section was dropped")
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil || servers["my-server"] == nil {
		t.Errorf("mcp.servers.my-server was clobbered: got %v", servers)
	}
}

// Cloud variant writes both ollama_key and the fixed ollama.com URL.
// Other sections must still survive.
func TestSaveOllamaConfig_Cloud_WritesKeyAndURL(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	configPath := filepath.Join(tmp, "gokin", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	existing := "api:\n  glm_key: keep-this-12345\n"
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := saveOllamaConfig("sk-ollama-cloud-1234567890", "llama3.2", "https://ollama.com"); err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	api, _ := root["api"].(map[string]any)
	if api["ollama_key"] != "sk-ollama-cloud-1234567890" {
		t.Errorf("ollama_key not saved: got %v", api["ollama_key"])
	}
	if api["ollama_base_url"] != "https://ollama.com" {
		t.Errorf("ollama_base_url not saved: got %v", api["ollama_base_url"])
	}
	if api["glm_key"] != "keep-this-12345" {
		t.Errorf("glm_key clobbered in cloud mode: got %v", api["glm_key"])
	}
}

// Regression (2nd-pass): Cloud → Local transition must clear stale cloud
// settings. Before the `delete(api, "ollama_*")` lines, a user who had
// configured Cloud (leaving ollama_key + ollama_base_url=https://ollama.com
// in config) and later re-ran the wizard picking Local with the default
// endpoint would inherit the old cloud URL silently — the CLI would keep
// hitting ollama.com.
func TestSaveOllamaConfig_LocalAfterCloud_ClearsCloudSettings(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	configPath := filepath.Join(tmp, "gokin", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Seed a prior Cloud-mode config.
	existing := `api:
  glm_key: keep-me-unrelated-12345
  ollama_key: stale-cloud-key-1234567890
  ollama_base_url: "https://ollama.com"
  active_provider: ollama
model:
  provider: ollama
  name: llama3.2
`
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Simulate user picking Local with default endpoint (empty serverURL).
	if _, err := saveOllamaConfig("", "llama3.2", ""); err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	api, _ := root["api"].(map[string]any)

	if _, has := api["ollama_key"]; has {
		t.Errorf("stale ollama_key survived switch to Local: got %v", api["ollama_key"])
	}
	if _, has := api["ollama_base_url"]; has {
		t.Errorf("stale ollama_base_url survived switch to Local: got %v", api["ollama_base_url"])
	}
	// Unrelated field must remain.
	if api["glm_key"] != "keep-me-unrelated-12345" {
		t.Errorf("unrelated glm_key was clobbered: got %v", api["glm_key"])
	}
}

// Inverse: Local with a custom endpoint → Cloud must overwrite the endpoint
// (not preserve the custom local URL). Also verifies Cloud correctly writes
// the fixed ollama.com URL.
func TestSaveOllamaConfig_CloudAfterCustomLocal_ReplacesEndpoint(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	configPath := filepath.Join(tmp, "gokin", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Seed a Local custom-endpoint config (common for LAN-hosted Ollama).
	existing := `api:
  ollama_base_url: "http://my-gpu-rig.local:11434"
  active_provider: ollama
model:
  provider: ollama
  name: codellama
`
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Simulate switching to Cloud.
	if _, err := saveOllamaConfig("sk-ollama-new-1234567890", "llama3.2", "https://ollama.com"); err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	api, _ := root["api"].(map[string]any)

	if api["ollama_base_url"] != "https://ollama.com" {
		t.Errorf("Cloud mode didn't override custom local URL: got %v", api["ollama_base_url"])
	}
	if api["ollama_key"] != "sk-ollama-new-1234567890" {
		t.Errorf("Cloud mode should set the key: got %v", api["ollama_key"])
	}
}

// Fresh install: no existing config file. saveOllamaConfig must create
// the directory and write a valid Ollama-only config.
func TestSaveOllamaConfig_FreshInstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	savedPath, err := saveOllamaConfig("", "codellama", "")
	if err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}

	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	api, _ := root["api"].(map[string]any)
	if api["active_provider"] != "ollama" {
		t.Errorf("active_provider = %v, want ollama", api["active_provider"])
	}
	model, _ := root["model"].(map[string]any)
	if model["name"] != "codellama" {
		t.Errorf("model.name = %v, want codellama", model["name"])
	}
}

func contextWithDeadline(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
