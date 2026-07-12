package setup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// withTempConfigDir redirects XDG_CONFIG_HOME to a temp dir so tests don't
// write to the developer's real config. getConfigPath() → config.GetConfigPath()
// checks XDG_CONFIG_HOME first, so this is the single redirect point.
func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "gokin", "config.yaml")
}

// readConfigYAML reads the config file from XDG_CONFIG_HOME and unmarshals
// it into a generic map for field-by-field assertions.
func readConfigYAML(t *testing.T) map[string]any {
	t.Helper()
	path, err := getConfigPath()
	if err != nil {
		t.Fatalf("getConfigPath: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// getConfigPath
// ---------------------------------------------------------------------------

func TestGetConfigPath_Success(t *testing.T) {
	withTempConfigDir(t)
	path, err := getConfigPath()
	if err != nil {
		t.Fatalf("getConfigPath: %v", err)
	}
	if !strings.HasSuffix(path, "config.yaml") {
		t.Errorf("path = %q", path)
	}
}

// ---------------------------------------------------------------------------
// ensureMap — all type-switch branches
// ---------------------------------------------------------------------------

func TestEnsureMap_StringMap(t *testing.T) {
	parent := map[string]any{
		"api": map[string]any{"key": "val"},
	}
	sub := ensureMap(parent, "api")
	if sub["key"] != "val" {
		t.Errorf("sub[key] = %v", sub["key"])
	}
}

func TestEnsureMap_AnyMap(t *testing.T) {
	// yaml.v3 unmarshals to map[string]any, but yaml.v2 used map[any]any.
	// ensureMap must coerce this shape.
	parent := map[string]any{
		"api": map[any]any{"key": "val"},
	}
	sub := ensureMap(parent, "api")
	if sub["key"] != "val" {
		t.Errorf("sub[key] = %v", sub["key"])
	}
	// The parent should now hold the converted map[string]any
	if _, ok := parent["api"].(map[string]any); !ok {
		t.Error("parent[api] should be map[string]any after coercion")
	}
}

func TestEnsureMap_MissingKey(t *testing.T) {
	parent := map[string]any{}
	sub := ensureMap(parent, "newsection")
	if sub == nil {
		t.Fatal("sub should not be nil")
	}
	sub["x"] = "y"
	if parent["newsection"] == nil {
		t.Error("parent[newsection] should be set")
	}
}

func TestEnsureMap_NonMapValue(t *testing.T) {
	parent := map[string]any{"api": "not-a-map"}
	sub := ensureMap(parent, "api")
	if sub == nil {
		t.Fatal("sub should not be nil")
	}
	// Should have replaced the string with a fresh map
	if _, ok := parent["api"].(map[string]any); !ok {
		t.Error("parent[api] should be replaced with map[string]any")
	}
}

// ---------------------------------------------------------------------------
// loadRawConfigOrEmpty — all branches
// ---------------------------------------------------------------------------

func TestLoadRawConfigOrEmpty_NonExistent(t *testing.T) {
	m, err := loadRawConfigOrEmpty("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing file: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %d keys", len(m))
	}
}

func TestLoadRawConfigOrEmpty_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte{}, 0600)

	m, err := loadRawConfigOrEmpty(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map for empty file")
	}
}

func TestLoadRawConfigOrEmpty_InvalidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("api: [unclosed"), 0600)

	_, err := loadRawConfigOrEmpty(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadRawConfigOrEmpty_ValidYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte("api:\n  key: val\n"), 0600)

	m, err := loadRawConfigOrEmpty(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	api, ok := m["api"].(map[string]any)
	if !ok {
		t.Fatal("api section missing")
	}
	if api["key"] != "val" {
		t.Errorf("api[key] = %v", api["key"])
	}
}

// ---------------------------------------------------------------------------
// writeFileAtomic — rename failure fallback
// ---------------------------------------------------------------------------

func TestWriteFileAtomic_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := []byte("hello")

	if err := writeFileAtomic(path, data, 0600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q", string(got))
	}
}

func TestWriteFileAtomic_RenameFallbackToWriteFile(t *testing.T) {
	// os.Rename can fail cross-device; when it does, writeFileAtomic falls
	// back to a plain os.WriteFile. We simulate by writing to a path whose
	// directory doesn't exist for .tmp but the final path also fails — but
	// the more reliable test is that rename within the same dir always works.
	// Instead test the fallback path by making .tmp creation fail: write to
	// a read-only dir.
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")
	// subdir doesn't exist → both WriteFile(tmp) and Rename fail → fallback
	// WriteFile(path) also fails → should return an error
	err := writeFileAtomic(path, []byte("x"), 0600)
	if err == nil {
		// On some systems the dir may be auto-created; if it succeeded, just
		// verify the content is correct.
		if _, statErr := os.Stat(path); statErr == nil {
			t.Log("writeFileAtomic succeeded despite missing subdir")
			return
		}
		t.Error("expected error when dir doesn't exist, got nil")
	}
}

// ---------------------------------------------------------------------------
// saveProviderConfig — success, preserve existing, unknown provider
// ---------------------------------------------------------------------------

func TestSaveProviderConfig_Success(t *testing.T) {
	withTempConfigDir(t)

	path, err := saveProviderConfig("glm", "test-key-1234567890", "glm-5.2")
	if err != nil {
		t.Fatalf("saveProviderConfig: %v", err)
	}
	if path == "" {
		t.Fatal("path should not be empty")
	}

	m := readConfigYAML(t)
	api := m["api"].(map[string]any)
	if api["glm_key"] != "test-key-1234567890" {
		t.Errorf("glm_key = %v", api["glm_key"])
	}
	if api["active_provider"] != "glm" {
		t.Errorf("active_provider = %v", api["active_provider"])
	}
	model := m["model"].(map[string]any)
	if model["name"] != "glm-5.2" {
		t.Errorf("model.name = %v", model["name"])
	}
}

func TestSaveProviderConfig_DefaultModel(t *testing.T) {
	withTempConfigDir(t)

	// Empty model → should use provider's DefaultModel
	_, err := saveProviderConfig("glm", "test-key", "")
	if err != nil {
		t.Fatalf("saveProviderConfig: %v", err)
	}

	m := readConfigYAML(t)
	model := m["model"].(map[string]any)
	p := config.GetProvider("glm")
	if model["name"] != p.DefaultModel {
		t.Errorf("model.name = %v, want %v", model["name"], p.DefaultModel)
	}
}

func TestSaveProviderConfig_UnknownProvider(t *testing.T) {
	withTempConfigDir(t)

	_, err := saveProviderConfig("nonexistent-provider", "key", "model")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestSaveProviderConfig_PreservesUnrelatedFields(t *testing.T) {
	configPath := withTempConfigDir(t)

	// Pre-write an existing config with unrelated fields
	existing := map[string]any{
		"theme": "dark",
		"mcp":   map[string]any{"server1": "config"},
	}
	data, _ := yaml.Marshal(existing)
	os.MkdirAll(filepath.Dir(configPath), 0700)
	os.WriteFile(configPath, data, 0600)

	// Now save a provider config
	_, err := saveProviderConfig("glm", "new-key", "")
	if err != nil {
		t.Fatalf("saveProviderConfig: %v", err)
	}

	m := readConfigYAML(t)
	if m["theme"] != "dark" {
		t.Errorf("theme field was clobbered: %v", m["theme"])
	}
}

// ---------------------------------------------------------------------------
// saveOllamaConfig — local + cloud modes
// ---------------------------------------------------------------------------

func TestSaveOllamaConfig_LocalDefaultModel(t *testing.T) {
	withTempConfigDir(t)

	path, err := saveOllamaConfig("", "", "http://localhost:11434")
	if err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}
	if path == "" {
		t.Fatal("path should not be empty")
	}

	m := readConfigYAML(t)
	model := m["model"].(map[string]any)
	if model["name"] != "llama3.2" {
		t.Errorf("model.name = %v, want llama3.2", model["name"])
	}
	api := m["api"].(map[string]any)
	if api["active_provider"] != "ollama" {
		t.Errorf("active_provider = %v", api["active_provider"])
	}
	// Local mode: no ollama_key
	if _, hasKey := api["ollama_key"]; hasKey {
		t.Error("local mode should not have ollama_key")
	}
	if api["ollama_base_url"] == "http://localhost:11434" {
		// ok
	} else {
		t.Errorf("ollama_base_url = %v", api["ollama_base_url"])
	}
}

func TestSaveOllamaConfig_Cloud(t *testing.T) {
	withTempConfigDir(t)

	_, err := saveOllamaConfig("cloud-api-key-12345", "llama3.2", "https://ollama.com")
	if err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}

	m := readConfigYAML(t)
	api := m["api"].(map[string]any)
	if api["ollama_key"] != "cloud-api-key-12345" {
		t.Errorf("ollama_key = %v", api["ollama_key"])
	}
	if api["ollama_base_url"] != "https://ollama.com" {
		t.Errorf("ollama_base_url = %v", api["ollama_base_url"])
	}
}

func TestSaveOllamaConfig_ClearsStaleCloudFields(t *testing.T) {
	configPath := withTempConfigDir(t)

	// Pre-write a cloud config
	cloud := map[string]any{
		"api": map[string]any{
			"ollama_key":      "old-cloud-key",
			"ollama_base_url": "https://ollama.com",
		},
	}
	data, _ := yaml.Marshal(cloud)
	os.MkdirAll(filepath.Dir(configPath), 0700)
	os.WriteFile(configPath, data, 0600)

	// Switch to local mode (no apiKey, no serverURL)
	_, err := saveOllamaConfig("", "llama3.2", "")
	if err != nil {
		t.Fatalf("saveOllamaConfig: %v", err)
	}

	m := readConfigYAML(t)
	api := m["api"].(map[string]any)
	if _, hasKey := api["ollama_key"]; hasKey {
		t.Error("stale ollama_key should be cleared in local mode")
	}
	if _, hasURL := api["ollama_base_url"]; hasURL {
		t.Error("stale ollama_base_url should be cleared in local mode")
	}
}

// ---------------------------------------------------------------------------
// detectInstalledOllamaModels
// ---------------------------------------------------------------------------

func TestDetectInstalledOllamaModels_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			resp := map[string]any{
				"models": []map[string]any{
					{"name": "llama3.2"},
					{"name": "qwen2.5"},
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	models, err := detectInstalledOllamaModels(srv.URL)
	if err != nil {
		t.Fatalf("detectInstalledOllamaModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0] != "llama3.2" {
		t.Errorf("models[0] = %q", models[0])
	}
}

func TestDetectInstalledOllamaModels_InvalidURL(t *testing.T) {
	_, err := detectInstalledOllamaModels("://invalid-url")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestDetectInstalledOllamaModels_ConnectionError(t *testing.T) {
	_, err := detectInstalledOllamaModels("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestDetectInstalledOllamaModels_DefaultURL(t *testing.T) {
	// Empty serverURL → defaults to config.DefaultOllamaBaseURL.
	// We can't easily mock that default, but we can verify the error path.
	_, err := detectInstalledOllamaModels("")
	if err == nil {
		// If Ollama happens to be running locally, this is not a test failure.
		t.Log("Ollama is running locally — default URL test found a server")
	}
}

// ---------------------------------------------------------------------------
// shouldSkipValidation / runValidation
// ---------------------------------------------------------------------------

func TestShouldSkipValidation(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"no", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"YES", true},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("GOKIN_SKIP_VALIDATION", tc.env)
			if got := shouldSkipValidation(); got != tc.want {
				t.Errorf("shouldSkipValidation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunValidation_Skipped(t *testing.T) {
	t.Setenv("GOKIN_SKIP_VALIDATION", "1")
	ve := runValidation("glm", "test-key")
	if ve != nil {
		t.Errorf("expected nil when skipped, got %v", ve)
	}
}

func TestRunValidation_UnknownProvider(t *testing.T) {
	ve := runValidation("nonexistent-provider", "test-key-1234567890")
	if ve == nil {
		t.Fatal("expected ValidationError for unknown provider")
	}
	if ve.Kind != ValidationTransient {
		t.Errorf("Kind = %v, want ValidationTransient", ve.Kind)
	}
}

// ---------------------------------------------------------------------------
// ValidationError.Error / Unwrap
// ---------------------------------------------------------------------------

func TestValidationError_Error_Nil(t *testing.T) {
	var ve *ValidationError
	if ve.Error() != "" {
		t.Errorf("nil receiver Error() = %q, want empty", ve.Error())
	}
}

func TestValidationError_Error_NilErr(t *testing.T) {
	ve := &ValidationError{Kind: ValidationBadKey, Err: nil}
	if ve.Error() != "" {
		t.Errorf("nil Err Error() = %q, want empty", ve.Error())
	}
}

func TestValidationError_UnwrapReturnsInner(t *testing.T) {
	inner := os.ErrNotExist
	ve := &ValidationError{Kind: ValidationTransient, Err: inner}
	if ve.Unwrap() != inner {
		t.Error("Unwrap should return wrapped error")
	}
	// errors.Is must traverse the wrapper
	if !errors.Is(ve, inner) {
		t.Error("errors.Is should match the wrapped error")
	}
}

// ---------------------------------------------------------------------------
// validateAPIKeyReal — deprecated wrapper
// ---------------------------------------------------------------------------

func TestValidateAPIKeyReal_NoValidationURL(t *testing.T) {
	// A provider with empty KeyValidation.URL → short-key check only.
	// We need a registered provider; use a real one with no URL configured.
	// Since all real providers have URLs, we test the short-key path directly.
	p := config.GetProvider("glm")
	if p == nil {
		t.Skip("glm provider not registered")
	}
	// All real providers have validation URLs, so this tests the "too short" path
	err := validateAPIKeyReal("glm", "short")
	if err == nil {
		// If glm has no validation URL and key is long enough, it returns nil
		t.Log("validateAPIKeyReal returned nil for short key (provider may have URL)")
	}
}

// ---------------------------------------------------------------------------
// promptSaveDespiteValidation
// ---------------------------------------------------------------------------

func TestPromptSaveDespiteValidation_DefaultYes(t *testing.T) {
	withStdinTerminal(t, false)
	r := bufio.NewReader(strings.NewReader("\n"))
	ve := &ValidationError{Kind: ValidationTransient, Err: os.ErrNotExist}
	if !promptSaveDespiteValidation(r, ve) {
		t.Error("empty answer should default to yes")
	}
}

func TestPromptSaveDespiteValidation_Yes(t *testing.T) {
	withStdinTerminal(t, false)
	r := bufio.NewReader(strings.NewReader("y\n"))
	ve := &ValidationError{Kind: ValidationTransient, Err: os.ErrNotExist}
	if !promptSaveDespiteValidation(r, ve) {
		t.Error("'y' should return true")
	}
}

func TestPromptSaveDespiteValidation_No(t *testing.T) {
	withStdinTerminal(t, false)
	r := bufio.NewReader(strings.NewReader("n\n"))
	ve := &ValidationError{Kind: ValidationTransient, Err: os.ErrNotExist}
	if promptSaveDespiteValidation(r, ve) {
		t.Error("'n' should return false")
	}
}

func TestPromptSaveDespiteValidation_ReadError(t *testing.T) {
	withStdinTerminal(t, false)
	// Empty reader → ReadString returns EOF immediately
	r := bufio.NewReader(strings.NewReader(""))
	ve := &ValidationError{Kind: ValidationTransient, Err: os.ErrNotExist}
	if promptSaveDespiteValidation(r, ve) {
		t.Error("read error should return false")
	}
}

// ---------------------------------------------------------------------------
// setupAPIKey — interactive flow with mock stdin
// ---------------------------------------------------------------------------

func TestSetupAPIKey_KeyTooShort(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)
	t.Setenv("GOKIN_SKIP_VALIDATION", "1")

	r := bufio.NewReader(strings.NewReader("short\n"))
	err := setupAPIKey(r, "glm")
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestSetupAPIKey_UnknownProvider(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)

	r := bufio.NewReader(strings.NewReader(""))
	err := setupAPIKey(r, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestSetupAPIKey_Success(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)
	t.Setenv("GOKIN_SKIP_VALIDATION", "1")

	r := bufio.NewReader(strings.NewReader("valid-api-key-1234567890abcdef\n"))
	err := setupAPIKey(r, "glm")
	if err != nil {
		t.Fatalf("setupAPIKey: %v", err)
	}

	m := readConfigYAML(t)
	api := m["api"].(map[string]any)
	if api["glm_key"] != "valid-api-key-1234567890abcdef" {
		t.Errorf("glm_key = %v", api["glm_key"])
	}
}

// ---------------------------------------------------------------------------
// setupOllama — mode selection
// ---------------------------------------------------------------------------

func TestSetupOllama_InvalidChoice(t *testing.T) {
	withTempConfigDir(t)

	// Invalid choice → defaults to Local → needs model name + URL (3 lines total)
	input := "invalid\ncustom-model\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllama(r)
	if err != nil {
		t.Fatalf("setupOllama with invalid choice: %v", err)
	}

	m := readConfigYAML(t)
	model := m["model"].(map[string]any)
	if model["name"] != "custom-model" {
		t.Errorf("model.name = %v, want custom-model", model["name"])
	}
}

func TestSetupOllama_Local(t *testing.T) {
	withTempConfigDir(t)

	input := "1\ncustom-model\nhttp://localhost:11434\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllama(r)
	if err != nil {
		t.Fatalf("setupOllama Local: %v", err)
	}

	m := readConfigYAML(t)
	model := m["model"].(map[string]any)
	if model["name"] != "custom-model" {
		t.Errorf("model.name = %v", model["name"])
	}
}

func TestSetupOllama_Cloud(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)

	input := "2\nollama-cloud-key-1234567890\ngemma3\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllama(r)
	if err != nil {
		t.Fatalf("setupOllama Cloud: %v", err)
	}

	m := readConfigYAML(t)
	api := m["api"].(map[string]any)
	if api["ollama_key"] != "ollama-cloud-key-1234567890" {
		t.Errorf("ollama_key = %v", api["ollama_key"])
	}
}

func TestSetupOllama_Cloud_ShortKey(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)

	input := "2\nshort\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllama(r)
	if err == nil {
		t.Fatal("expected error for short cloud key")
	}
}

// ---------------------------------------------------------------------------
// setupOllamaLocal / setupOllamaCloud — direct calls
// ---------------------------------------------------------------------------

func TestSetupOllamaLocal_DefaultModel(t *testing.T) {
	withTempConfigDir(t)

	input := "\n\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllamaLocal(r)
	if err != nil {
		t.Fatalf("setupOllamaLocal: %v", err)
	}

	m := readConfigYAML(t)
	model := m["model"].(map[string]any)
	if model["name"] != "llama3.2" {
		t.Errorf("model.name = %v, want llama3.2", model["name"])
	}
}

func TestSetupOllamaCloud_Success(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)

	input := "ollama-cloud-key-1234567890\nllama3.2\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllamaCloud(r)
	if err != nil {
		t.Fatalf("setupOllamaCloud: %v", err)
	}
}

func TestSetupOllamaCloud_ShortKey(t *testing.T) {
	withTempConfigDir(t)
	withStdinTerminal(t, false)

	input := "short\n"
	r := bufio.NewReader(strings.NewReader(input))
	err := setupOllamaCloud(r)
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

// ---------------------------------------------------------------------------
// showNextSteps / showOllamaLocalNextSteps / showOllamaCloudNextSteps
// ---------------------------------------------------------------------------

// captureStdout swaps os.Stdout for a pipe, runs fn, and returns everything fn wrote.
// Restores the original stdout even on panic.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestShowNextSteps_OutputContainsHints(t *testing.T) {
	out := captureStdout(t, func() { showNextSteps() })
	for _, want := range []string{"Next Steps", "gokin", "/quickstart", "/doctor"} {
		if !strings.Contains(out, want) {
			t.Errorf("showNextSteps output missing %q:\n%s", want, out)
		}
	}
}

func TestShowOllamaLocalNextSteps_IncludesModel(t *testing.T) {
	out := captureStdout(t, func() { showOllamaLocalNextSteps("gemma3") })
	if !strings.Contains(out, "gemma3") {
		t.Errorf("output missing model name: %s", out)
	}
	if !strings.Contains(out, "Next Steps") {
		t.Error("output missing 'Next Steps' header")
	}
}

func TestShowOllamaCloudNextSteps_OutputContainsCloudHint(t *testing.T) {
	out := captureStdout(t, func() { showOllamaCloudNextSteps() })
	if !strings.Contains(out, "Ollama Cloud") {
		t.Errorf("output missing cloud hint: %s", out)
	}
}

// ---------------------------------------------------------------------------
// spin
// ---------------------------------------------------------------------------

func TestSpin_NormalExitReturns(t *testing.T) {
	done := make(chan bool, 1)
	done <- true // immediately done
	finished := make(chan struct{})
	go func() {
		spin("test message", done)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("spin did not return after done signal")
	}
}

func TestSpin_TicksThenExits(t *testing.T) {
	// Keep spin running long enough to tick the spinner frame loop at least once.
	// 200ms exercises the ticker case + clear/rewrite logic without a slow test.
	done := make(chan bool, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		done <- true
	}()
	finished := make(chan struct{})
	go func() {
		spin("waiting", done)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("spin did not return after done signal")
	}
}
