package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/app"
	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/permission"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func TestApplyRuntimeOverrides_ProviderSelectsRuntimeAndDefaultModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.Name = "stale-model"

	if err := applyRuntimeOverrides(cfg, "glm", ""); err != nil {
		t.Fatalf("applyRuntimeOverrides() error = %v", err)
	}

	if cfg.API.ActiveProvider != "glm" || cfg.API.Backend != "glm" || cfg.Model.Provider != "glm" {
		t.Fatalf("provider not applied: api=%q backend=%q model.provider=%q", cfg.API.ActiveProvider, cfg.API.Backend, cfg.Model.Provider)
	}
	if cfg.Model.Name != "glm-5.2" {
		t.Fatalf("model name = %q, want provider default glm-5.2", cfg.Model.Name)
	}
}

func TestLoadConfiguredConfigUsesExplicitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.yaml")
	if err := os.WriteFile(path, []byte("model:\n  name: glm-5.2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfiguredConfig(path)
	if err != nil {
		t.Fatalf("loadConfiguredConfig: %v", err)
	}
	if cfg.Model.Name != "glm-5.2" {
		t.Fatalf("model = %q, want explicit config value", cfg.Model.Name)
	}
	if _, err := loadConfiguredConfig(path + ".missing"); err == nil {
		t.Fatal("missing explicit config unexpectedly succeeded")
	}
}

func TestApplyRunConfigOverridesSurvivesReloadShape(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Session.Enabled = true
	cfg.Session.AutoLoad = true
	if err := applyRunConfigOverrides(
		cfg, "test-version", "glm", "glm-5.2", "https://example.test/api", true); err != nil {
		t.Fatal(err)
	}
	if cfg.Version != "test-version" ||
		cfg.Model.Name != "glm-5.2" ||
		cfg.Model.CustomBaseURL != "https://example.test/api" ||
		cfg.Session.Enabled ||
		cfg.Session.AutoLoad {
		t.Fatalf("runtime overrides not applied coherently: %+v", cfg)
	}
}

func TestApplyBareRunConfigMarksRuntimeWithoutMutatingPersistentFeatures(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Hooks.Enabled = true
	cfg.Hooks.Hooks = []config.HookConfig{{Name: "configured"}}
	cfg.Memory.Enabled = true
	cfg.Memory.AutoInject = true
	cfg.Memory.AllowGlobal = true
	cfg.SessionMemory.Enabled = true
	cfg.MCP.Enabled = true
	cfg.MCP.Servers = []config.MCPServerConfig{{Name: "configured"}}
	cfg.Web.GLMSearch = true
	cfg.Watcher.Enabled = true
	cfg.Update.AutoCheck = true
	cfg.Update.AutoDownload = true
	cfg.Session.Enabled = true
	cfg.Session.AutoLoad = true
	cfg.DoneGate.Enabled = true
	cfg.Completion.EvidenceFooter = true
	cfg.Tools.ProactiveContext.Enabled = true
	cfg.Tools.SmartValidation.Enabled = true
	cfg.Tools.SmartValidation.SelfReviewThreshold = 4
	cfg.Tools.DeltaCheck.Enabled = true
	cfg.Permission.Enabled = true
	cfg.Tools.Bash.Sandbox = true

	applyBareRunConfig(cfg, true)

	if !cfg.Bare {
		t.Fatal("bare runtime marker was not set")
	}
	if !cfg.Hooks.Enabled || len(cfg.Hooks.Hooks) != 1 ||
		!cfg.Memory.Enabled || !cfg.Memory.AutoInject || !cfg.Memory.AllowGlobal ||
		!cfg.SessionMemory.Enabled || !cfg.MCP.Enabled || len(cfg.MCP.Servers) != 1 ||
		!cfg.Web.GLMSearch || !cfg.Watcher.Enabled || !cfg.Update.AutoCheck ||
		!cfg.Update.AutoDownload || !cfg.Session.Enabled || !cfg.Session.AutoLoad ||
		!cfg.DoneGate.Enabled || !cfg.Completion.EvidenceFooter ||
		!cfg.Tools.ProactiveContext.Enabled || !cfg.Tools.SmartValidation.Enabled ||
		cfg.Tools.SmartValidation.SelfReviewThreshold != 4 ||
		!cfg.Tools.DeltaCheck.Enabled || !cfg.Permission.Enabled ||
		!cfg.Tools.Bash.Sandbox {
		t.Fatalf("--bare mutated persistent feature fields: %+v", cfg)
	}
}

func TestApplyBareRunConfigDisabledIsNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	before := cfg.Clone()
	applyBareRunConfig(cfg, false)
	if cfg.Bare || cfg.Hooks.Enabled != before.Hooks.Enabled ||
		cfg.Memory.Enabled != before.Memory.Enabled ||
		cfg.MCP.Enabled != before.MCP.Enabled {
		t.Fatal("disabled bare override mutated config")
	}
}

func TestConfigureBareEnvironmentScopesClaudeSimpleSignal(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SIMPLE", "previous")
	restore, err := configureBareEnvironment(true)
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("CLAUDE_CODE_SIMPLE"); got != "1" {
		t.Fatalf("CLAUDE_CODE_SIMPLE = %q, want 1", got)
	}
	restore()
	if got := os.Getenv("CLAUDE_CODE_SIMPLE"); got != "previous" {
		t.Fatalf("CLAUDE_CODE_SIMPLE after restore = %q, want previous", got)
	}
}

func TestNormalizeOptionalDebugArgs(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "bare flag", in: []string{"--debug", "--print", "task"}, want: []string{"--debug", "--print", "task"}},
		{name: "separated filter", in: []string{"--debug", "api,mcp", "--print"}, want: []string{"--debug=api,mcp", "--print"}},
		{name: "negative filter", in: []string{"--debug", "!health,!file"}, want: []string{"--debug=!health,!file"}},
		{name: "equals unchanged", in: []string{"--debug=api", "task"}, want: []string{"--debug=api", "task"}},
		{name: "delimiter", in: []string{"--", "--debug", "api"}, want: []string{"--", "--debug", "api"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeOptionalDebugArgs(tc.in)
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("normalizeOptionalDebugArgs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveCLIDebug(t *testing.T) {
	t.Run("disabled ignores environment path", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_DEBUG_LOGS_DIR", "/should/not/activate")
		got, err := resolveCLIDebug(cliDebugFlags{})
		if err != nil || got.enabled {
			t.Fatalf("resolve disabled = %+v, %v", got, err)
		}
	})

	t.Run("debug file implies enabled and wins environment", func(t *testing.T) {
		t.Setenv("CLAUDE_CODE_DEBUG_LOGS_DIR", "/wrong/path")
		t.Setenv("CLAUDE_CODE_DEBUG_LOG_LEVEL", "warn")
		explicit := filepath.Join(t.TempDir(), "run.log")
		got, err := resolveCLIDebug(cliDebugFlags{
			debugFileSet: true,
			file:         explicit,
		})
		if err != nil {
			t.Fatal(err)
		}
		want, _ := filepath.Abs(explicit)
		if !got.enabled || got.path != want || got.filter != "*" || got.level != logging.LevelWarn {
			t.Fatalf("resolved debug = %+v", got)
		}
	})

	// The Claude-compatible variable names a DIRECTORY, which is how anyone who
	// already exports it for Claude Code has it set. Consuming it as a log FILE
	// path made `gokin --debug` fail the whole run for those users.
	t.Run("compatible environment directory receives a generated file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("CLAUDE_CODE_DEBUG_LOGS_DIR", dir)
		got, err := resolveCLIDebug(cliDebugFlags{debugSet: true, debug: "api,mcp"})
		if err != nil {
			t.Fatal(err)
		}
		wantDir, _ := filepath.Abs(dir)
		if filepath.Dir(got.path) != wantDir {
			t.Fatalf("resolved debug path %q is not inside %q", got.path, wantDir)
		}
		if filepath.Ext(got.path) != ".jsonl" {
			t.Fatalf("resolved debug path = %q, want a generated .jsonl file", got.path)
		}
		if got.filter != "api,mcp" || got.level != logging.LevelDebug {
			t.Fatalf("resolved debug = %+v", got)
		}
	})

	// GOKIN_DEBUG_LOG_FILE keeps FILE semantics — the two variables differ on
	// purpose.
	t.Run("gokin environment variable names the file itself", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "compat.log")
		t.Setenv("GOKIN_DEBUG_LOG_FILE", path)
		got, err := resolveCLIDebug(cliDebugFlags{debugSet: true, debug: "api,mcp"})
		if err != nil {
			t.Fatal(err)
		}
		want, _ := filepath.Abs(path)
		if got.path != want || got.filter != "api,mcp" || got.level != logging.LevelDebug {
			t.Fatalf("resolved debug = %+v", got)
		}
	})

	t.Run("invalid level fails closed", func(t *testing.T) {
		t.Setenv("GOKIN_DEBUG_LOG_LEVEL", "everything")
		_, err := resolveCLIDebug(cliDebugFlags{debugSet: true})
		if err == nil || !strings.Contains(err.Error(), "invalid debug log level") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestResolveInteractivePrompt(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "empty starts ordinary repl"},
		{name: "flag", flag: "inspect this", want: "inspect this"},
		{name: "positional", args: []string{"inspect", "this"}, want: "inspect this"},
		{name: "trim", args: []string{"  inspect  "}, want: "inspect"},
		{name: "ambiguous", flag: "one", args: []string{"two"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveInteractivePrompt(tc.flag, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveInteractivePrompt unexpectedly returned %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resolveInteractivePrompt = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestCobraArbitraryRootArgsPreservesSubcommandsAndPrompts(t *testing.T) {
	var rootArgs []string
	var childCalled bool
	root := &cobra.Command{
		Use:  "gokin",
		Args: cobra.ArbitraryArgs,
		Run: func(_ *cobra.Command, args []string) {
			rootArgs = append([]string(nil), args...)
		},
	}
	root.Flags().BoolP("print", "p", false, "")
	root.AddCommand(&cobra.Command{
		Use: "version",
		Run: func(*cobra.Command, []string) {
			childCalled = true
		},
	})

	root.SetArgs([]string{"-p", "inspect repository"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rootArgs, "") != "inspect repository" || childCalled {
		t.Fatalf("positional prompt dispatch: args=%q child=%v", rootArgs, childCalled)
	}

	rootArgs = nil
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !childCalled || len(rootArgs) != 0 {
		t.Fatalf("subcommand dispatch: args=%q child=%v", rootArgs, childCalled)
	}
}

func TestApplyRuntimeOverrides_ProviderAndModelUsesExplicitModel(t *testing.T) {
	cfg := config.DefaultConfig()

	if err := applyRuntimeOverrides(cfg, "deepseek", "deepseek-v4-pro"); err != nil {
		t.Fatalf("applyRuntimeOverrides() error = %v", err)
	}

	if cfg.API.ActiveProvider != "deepseek" || cfg.Model.Provider != "deepseek" {
		t.Fatalf("provider not applied: api=%q model.provider=%q", cfg.API.ActiveProvider, cfg.Model.Provider)
	}
	if cfg.Model.Name != "deepseek-v4-pro" {
		t.Fatalf("model name = %q, want explicit model", cfg.Model.Name)
	}
}

func TestApplyRuntimeOverrides_ModelOnlyDetectsProvider(t *testing.T) {
	cfg := config.DefaultConfig()

	if err := applyRuntimeOverrides(cfg, "", "MiniMax-M2.7"); err != nil {
		t.Fatalf("applyRuntimeOverrides() error = %v", err)
	}

	if cfg.API.ActiveProvider != "minimax" || cfg.API.Backend != "minimax" || cfg.Model.Provider != "minimax" {
		t.Fatalf("provider not detected from model: api=%q backend=%q model.provider=%q", cfg.API.ActiveProvider, cfg.API.Backend, cfg.Model.Provider)
	}
	if cfg.Model.Name != "MiniMax-M2.7" {
		t.Fatalf("model name = %q, want MiniMax-M2.7", cfg.Model.Name)
	}
}

func TestApplyRuntimeOverrides_UnknownProviderErrors(t *testing.T) {
	cfg := config.DefaultConfig()

	err := applyRuntimeOverrides(cfg, "nope", "")
	if err == nil {
		t.Fatal("applyRuntimeOverrides() error = nil, want unknown provider error")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error = %v, want unknown provider", err)
	}
}

func TestApplyRuntimeBaseURLOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	if err := applyRuntimeBaseURLOverride(cfg, "http://127.0.0.1:12345/api/"); err != nil {
		t.Fatalf("applyRuntimeBaseURLOverride: %v", err)
	}
	if cfg.Model.CustomBaseURL != "http://127.0.0.1:12345/api" {
		t.Fatalf("custom base URL = %q", cfg.Model.CustomBaseURL)
	}
	for _, raw := range []string{"relative/path", "ftp://example.test", "https://user:secret@example.test", "https://example.test?q=x"} {
		if err := applyRuntimeBaseURLOverride(cfg, raw); err == nil {
			t.Errorf("unsafe base URL %q was accepted", raw)
		}
	}
}

func TestEvalGateOptions_ParsesThresholds(t *testing.T) {
	opts, enabled, err := evalGateOptions("90%", "2%", true, []string{"verification_passed=100%"})
	if err != nil {
		t.Fatalf("evalGateOptions() error = %v", err)
	}
	if !enabled {
		t.Fatal("evalGateOptions() enabled = false, want true")
	}
	if opts.MinScoreRatio != 0.9 || opts.MaxRegression != 0.02 {
		t.Fatalf("ratios = %v/%v, want 0.9/0.02", opts.MinScoreRatio, opts.MaxRegression)
	}
	if !opts.RequireAllPassed {
		t.Fatal("RequireAllPassed = false, want true")
	}
	if opts.MetricMinRatios["verification_passed"] != 1 {
		t.Fatalf("metric threshold = %v, want 1", opts.MetricMinRatios["verification_passed"])
	}
}

func TestEvalGateOptions_RejectsInvalidMetricThreshold(t *testing.T) {
	_, _, err := evalGateOptions("", "", false, []string{"verification_passed"})
	if err == nil {
		t.Fatal("evalGateOptions() error = nil, want invalid metric threshold error")
	}
	if !strings.Contains(err.Error(), "--fail-metric") {
		t.Fatalf("error = %v, want --fail-metric context", err)
	}
}

func TestApplyAddDirFlags(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.AllowedDirs = nil

	work := t.TempDir()

	// A valid directory is appended in-memory.
	if err := applyAddDirFlags(cfg, []string{work}); err != nil {
		t.Fatalf("valid dir should be accepted: %v", err)
	}
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Fatalf("expected 1 allowed dir, got %v", cfg.Tools.AllowedDirs)
	}

	// Duplicate is deduped (AddAllowedDir).
	if err := applyAddDirFlags(cfg, []string{work}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Errorf("duplicate should be deduped, got %v", cfg.Tools.AllowedDirs)
	}

	// An ungrantable location is refused (and nothing is appended).
	before := len(cfg.Tools.AllowedDirs)
	if err := applyAddDirFlags(cfg, []string{"/etc"}); err == nil {
		t.Error("/etc must be refused")
	}
	if len(cfg.Tools.AllowedDirs) != before {
		t.Error("refused dir must not be appended")
	}

	// A non-existent path errors.
	if err := applyAddDirFlags(cfg, []string{work + "/does-not-exist"}); err == nil {
		t.Error("non-existent path should error")
	}

	// Empty entries are skipped without error.
	if err := applyAddDirFlags(cfg, []string{"", "   "}); err != nil {
		t.Errorf("empty entries should be skipped: %v", err)
	}
}

// TestRunApp_HeadlessSetupRefusesInsteadOfBlockingOrCrashing (round 7) pins
// the fix: `--setup` had no headless guard, unlike the auto-invoked wizard
// path 20 lines below (triggered by ErrMissingAuth), which has always
// refused to run interactively in headless mode. `gokin --headless --setup`
// either blocked forever waiting on stdin (a live TTY) or died with a
// confusing "EOF" (redirected/closed stdin, e.g. from a script/cron job)
// instead of headless mode's documented "never block, fail clearly"
// contract. The fix's early return happens BEFORE config.Load() or any
// other init runs, so this stays a fast, deterministic unit test — it must
// return an actionable error immediately, not attempt setup.
func TestRunApp_HeadlessSetupRefusesInsteadOfBlockingOrCrashing(t *testing.T) {
	origHeadless, origRunSetup, origPrompt := headless, runSetup, prompt
	t.Cleanup(func() { headless, runSetup, prompt = origHeadless, origRunSetup, origPrompt })

	headless = true
	runSetup = true
	prompt = "anything" // satisfy the earlier --prompt-required-in-headless check

	err := runApp(nil, nil)
	if err == nil {
		t.Fatal("runApp(--headless --setup) returned nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "--setup") || !strings.Contains(err.Error(), "headless") {
		t.Fatalf("error = %q, want it to mention both --setup and headless", err.Error())
	}
}

func TestResolveHeadlessOutputFormat(t *testing.T) {
	tests := []struct {
		name     string
		headless bool
		raw      string
		want     string
		wantErr  string
	}{
		{name: "default text", headless: false, raw: "", want: "text"},
		{name: "headless text", headless: true, raw: " TEXT ", want: "text"},
		{name: "headless json", headless: true, raw: "JSON", want: "json"},
		{name: "headless stream json", headless: true, raw: " STREAM-JSON ", want: "stream-json"},
		{name: "json needs headless", headless: false, raw: "json", wantErr: "requires --headless"},
		{name: "stream json needs headless", headless: false, raw: "stream-json", wantErr: "requires --headless"},
		{name: "unknown", headless: true, raw: "yaml", wantErr: "want text, json, or stream-json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHeadlessOutputFormat(tt.headless, tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveHeadlessOutputFormat: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("format = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveCLIJSONSchemaRequiresStructuredHeadlessOutput(t *testing.T) {
	valid := `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`
	tests := []struct {
		name     string
		headless bool
		format   app.HeadlessOutputFormat
		raw      string
		set      bool
		wantNil  bool
		wantErr  string
	}{
		{name: "unset", format: app.HeadlessOutputText, wantNil: true},
		{
			name:    "interactive rejected",
			raw:     valid,
			set:     true,
			wantErr: "requires --headless",
		},
		{
			name:     "text rejected",
			headless: true,
			format:   app.HeadlessOutputText,
			raw:      valid,
			set:      true,
			wantErr:  "--output-format",
		},
		{
			name:     "json accepted",
			headless: true,
			format:   app.HeadlessOutputJSON,
			raw:      valid,
			set:      true,
		},
		{
			name:     "stream json accepted",
			headless: true,
			format:   app.HeadlessOutputStreamJSON,
			raw:      valid,
			set:      true,
		},
		{
			name:     "malformed rejected",
			headless: true,
			format:   app.HeadlessOutputJSON,
			raw:      "{",
			set:      true,
			wantErr:  "parse --json-schema",
		},
		{
			name:     "empty explicit rejected",
			headless: true,
			format:   app.HeadlessOutputJSON,
			set:      true,
			wantErr:  "non-empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveCLIJSONSchema(
				test.headless, test.format, test.raw, test.set)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != test.wantNil {
				t.Fatalf("schema nil = %v, want %v", got == nil, test.wantNil)
			}
		})
	}
}

func TestResolveHeadlessInputFormat(t *testing.T) {
	tests := []struct {
		name     string
		headless bool
		raw      string
		output   app.HeadlessOutputFormat
		want     headlessInputMode
		wantErr  string
	}{
		{name: "default text", raw: "", output: app.HeadlessOutputText, want: headlessInputText},
		{name: "stream", headless: true, raw: " STREAM-JSON ", output: app.HeadlessOutputStreamJSON, want: headlessInputStreamJSON},
		{name: "stream needs headless", raw: "stream-json", output: app.HeadlessOutputStreamJSON, wantErr: "requires --headless"},
		{name: "stream needs streaming output", headless: true, raw: "stream-json", output: app.HeadlessOutputJSON, wantErr: "requires --output-format stream-json"},
		{name: "unknown", headless: true, raw: "json", output: app.HeadlessOutputStreamJSON, wantErr: "want text or stream-json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHeadlessInputFormat(tt.headless, tt.raw, tt.output)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("format = %q, err=%v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestParseHeadlessStreamInput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{name: "direct prompt", input: `{"type":"user","prompt":" inspect repo "}`, want: "inspect repo"},
		{name: "string content", input: `{"schema_version":1,"type":"user","message":{"role":"user","content":"fix tests"}}`, want: "fix tests"},
		{name: "text blocks", input: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`, want: "first\nsecond"},
		{name: "wrong type", input: `{"type":"assistant","prompt":"no"}`, wantErr: "type must be"},
		{name: "ambiguous", input: `{"type":"user","prompt":"one","message":{"role":"user","content":"two"}}`, wantErr: "either prompt or message"},
		{name: "wrong role", input: `{"type":"user","message":{"role":"assistant","content":"no"}}`, wantErr: "message.role"},
		{name: "unsupported block", input: `{"type":"user","message":{"role":"user","content":[{"type":"image","text":"no"}]}}`, wantErr: "must be \"text\""},
		{name: "future schema", input: `{"schema_version":2,"type":"user","prompt":"no"}`, wantErr: "unsupported schema_version"},
		{name: "malformed", input: `{`, wantErr: "decode JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHeadlessStreamInput([]byte(tt.input))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("prompt=%q err=%v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestRunHeadlessInputStreamExecutesSequentialTurns(t *testing.T) {
	var stdout bytes.Buffer
	runner := &recordingHeadlessInputRunner{}
	input := strings.Join([]string{
		`{"type":"user","prompt":"first turn"}`,
		"",
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second turn"}]}}`,
	}, "\n")
	err := runHeadlessInputStream(
		context.Background(),
		runner,
		strings.NewReader(input),
		app.HeadlessOptions{
			OutputFormat: app.HeadlessOutputStreamJSON,
			Stdout:       &stdout,
			Stderr:       io.Discard,
		},
		"session-1",
	)
	if err != nil {
		t.Fatalf("runHeadlessInputStream() error = %v", err)
	}
	if got := strings.Join(runner.prompts, "|"); got != "first turn|second turn" {
		t.Fatalf("prompts = %q", got)
	}
	lines := nonEmptyLines(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("terminal records = %d, want 2:\n%s", len(lines), stdout.String())
	}
	for i, line := range lines {
		var result app.HeadlessResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatalf("decode result %d: %v", i, err)
		}
		if result.Type != "result" || result.Result != runner.prompts[i] {
			t.Fatalf("result %d = %+v", i, result)
		}
	}
}

func TestRunHeadlessInputStreamReusesCompiledJSONSchema(t *testing.T) {
	schema, err := app.CompileStructuredOutputSchema(`{"type":"object"}`)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingHeadlessInputRunner{}
	err = runHeadlessInputStream(
		context.Background(),
		runner,
		strings.NewReader(
			"{\"type\":\"user\",\"prompt\":\"first\"}\n"+
				"{\"type\":\"user\",\"prompt\":\"second\"}\n"),
		app.HeadlessOptions{
			OutputFormat: app.HeadlessOutputStreamJSON,
			Stdout:       io.Discard,
			Stderr:       io.Discard,
			JSONSchema:   schema,
		},
		"session-schema",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.schemas) != 2 ||
		runner.schemas[0] != schema ||
		runner.schemas[1] != schema {
		t.Fatalf("forwarded schemas = %+v", runner.schemas)
	}
}

func TestRunHeadlessInputStreamMalformedRecordFailsWithoutLaterExecution(t *testing.T) {
	var stdout bytes.Buffer
	runner := &recordingHeadlessInputRunner{}
	input := "{\"type\":\"user\",\"prompt\":\"first\"}\n{\n{\"type\":\"user\",\"prompt\":\"never\"}\n"
	err := runHeadlessInputStream(
		context.Background(),
		runner,
		strings.NewReader(input),
		app.HeadlessOptions{
			OutputFormat: app.HeadlessOutputStreamJSON,
			Stdout:       &stdout,
			Stderr:       io.Discard,
		},
		"session-2",
	)
	if err == nil || !strings.Contains(err.Error(), "record 2") {
		t.Fatalf("stream error = %v", err)
	}
	if len(runner.prompts) != 1 || runner.prompts[0] != "first" {
		t.Fatalf("executed prompts = %+v", runner.prompts)
	}
	lines := nonEmptyLines(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("records = %d, want success + input error:\n%s", len(lines), stdout.String())
	}
	var failure app.HeadlessResult
	if err := json.Unmarshal([]byte(lines[1]), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error == nil || failure.Error.Kind != "input" {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestResolveHeadlessPromptSupportsAutomationInputs(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		args   []string
		stdin  string
		want   string
		errSub string
	}{
		{name: "flag", flag: "explain", want: "explain"},
		{name: "position", args: []string{"explain", "this"}, want: "explain this"},
		{name: "stdin only", stdin: "diagnose this log\n", want: "diagnose this log"},
		{name: "position plus piped context", args: []string{"diagnose"}, stdin: "panic: boom\n", want: "diagnose\n\npanic: boom"},
		{name: "flag plus piped context", flag: "review", stdin: "package main\n", want: "review\n\npackage main"},
		{name: "ambiguous query", flag: "one", args: []string{"two"}, errSub: "ambiguous"},
		{name: "empty", errSub: "prompt is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveHeadlessPrompt(tt.flag, tt.args, tt.stdin)
			if tt.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("error = %v, want substring %q", err, tt.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveHeadlessPrompt: %v", err)
			}
			if got != tt.want {
				t.Fatalf("prompt = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadHeadlessStdinUsesInjectedPipeAndBoundsInput(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("piped build output\n"))
	got, err := readHeadlessStdin(cmd)
	if err != nil || got != "piped build output\n" {
		t.Fatalf("readHeadlessStdin = %q, %v", got, err)
	}

	tooLarge := &cobra.Command{}
	tooLarge.SetIn(io.LimitReader(zeroReader{}, maxHeadlessStdinBytes+1))
	if _, err := readHeadlessStdin(tooLarge); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized stdin error = %v", err)
	}
}

func TestValidateHeadlessExecutionLimits(t *testing.T) {
	tests := []struct {
		name     string
		headless bool
		turns    int
		timeout  time.Duration
		budget   float64
		wantErr  string
	}{
		{name: "adaptive no deadline", headless: true},
		{name: "explicit limits", headless: true, turns: 12, timeout: 30 * time.Minute, budget: 1.25},
		{name: "negative turns", headless: true, turns: -1, wantErr: "--max-turns"},
		{name: "negative timeout", headless: true, timeout: -time.Second, wantErr: "--timeout"},
		{name: "negative budget", headless: true, budget: -0.01, wantErr: "--max-budget-usd"},
		{name: "turns need headless", turns: 3, wantErr: "requires --headless"},
		{name: "timeout needs headless", timeout: time.Minute, wantErr: "requires --headless"},
		{name: "budget needs headless", budget: 0.10, wantErr: "requires --headless"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateHeadlessExecutionLimits(tt.headless, tt.turns, tt.timeout, tt.budget)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestResolveCLIPermissionMode(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		dangerous bool
		want      cliPermissionMode
		wantErr   string
	}{
		{name: "implicit config", want: cliPermissionInherit},
		{name: "default", raw: "default", want: cliPermissionDefault},
		{name: "accept camel", raw: "acceptEdits", want: cliPermissionAcceptEdits},
		{name: "accept kebab", raw: "accept-edits", want: cliPermissionAcceptEdits},
		{name: "dont ask camel", raw: "dontAsk", want: cliPermissionDontAsk},
		{name: "dont ask kebab", raw: "dont-ask", want: cliPermissionDontAsk},
		{name: "bypass", raw: "bypassPermissions", want: cliPermissionBypass},
		{name: "plan", raw: "PLAN", want: cliPermissionPlan},
		{name: "dangerous alias", dangerous: true, want: cliPermissionBypass},
		{name: "matching alias", raw: "bypassPermissions", dangerous: true, want: cliPermissionBypass},
		{name: "conflict", raw: "plan", dangerous: true, wantErr: "conflicts"},
		{name: "dont ask conflict", raw: "dontAsk", dangerous: true, wantErr: "conflicts"},
		{name: "unknown", raw: "yolo", wantErr: "invalid --permission-mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCLIPermissionMode(tt.raw, tt.dangerous)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("resolve = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResolveCLISystemPromptPrecedenceAndConflicts(t *testing.T) {
	t.Run("replace plus append", func(t *testing.T) {
		got, err := resolveCLISystemPrompt(cliSystemPromptFlags{
			replacement:    "replace",
			replacementSet: true,
			append:         "append",
			appendSet:      true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.replacement == nil || *got.replacement != "replace" ||
			got.append != "append" {
			t.Fatalf("resolved prompt = %+v", got)
		}
	})

	t.Run("explicit empty replacement remains set", func(t *testing.T) {
		got, err := resolveCLISystemPrompt(cliSystemPromptFlags{
			replacementSet: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.replacement == nil || *got.replacement != "" {
			t.Fatalf("empty replacement lost: %+v", got)
		}
	})

	for _, test := range []struct {
		name  string
		flags cliSystemPromptFlags
		want  string
	}{
		{
			name: "replacement sources",
			flags: cliSystemPromptFlags{
				replacementSet: true,
				fileSet:        true,
			},
			want: "conflicts",
		},
		{
			name: "append sources",
			flags: cliSystemPromptFlags{
				appendSet:     true,
				appendFileSet: true,
			},
			want: "conflicts",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolveCLISystemPrompt(test.flags); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestResolveCLISystemPromptReadsBoundedUTF8Files(t *testing.T) {
	dir := t.TempDir()
	replacePath := filepath.Join(dir, "replace.txt")
	appendPath := filepath.Join(dir, "append.txt")
	if err := os.WriteFile(replacePath, []byte("замена\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appendPath, []byte("добавка"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveCLISystemPrompt(cliSystemPromptFlags{
		replacementFile: replacePath,
		fileSet:         true,
		appendFile:      appendPath,
		appendFileSet:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.replacement == nil || *got.replacement != "замена\n" ||
		got.append != "добавка" {
		t.Fatalf("file prompt = %+v", got)
	}

	invalidPath := filepath.Join(dir, "invalid.txt")
	if err := os.WriteFile(invalidPath, []byte{0xff, 0xfe}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCLISystemPrompt(cliSystemPromptFlags{
		replacementFile: invalidPath,
		fileSet:         true,
	}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}

	oversizedPath := filepath.Join(dir, "oversized.txt")
	if err := os.WriteFile(
		oversizedPath,
		[]byte(strings.Repeat("x", app.MaxRunSystemPromptBytes+1)),
		0600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCLISystemPrompt(cliSystemPromptFlags{
		appendFile:    oversizedPath,
		appendFileSet: true,
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized file error = %v", err)
	}
}

func TestResolveCLISystemPromptRejectsNULAndCombinedOverflow(t *testing.T) {
	if _, err := resolveCLISystemPrompt(cliSystemPromptFlags{
		append:    "bad\x00prompt",
		appendSet: true,
	}); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("NUL error = %v", err)
	}

	half := strings.Repeat("x", app.MaxRunSystemPromptBytes/2+1)
	if _, err := resolveCLISystemPrompt(cliSystemPromptFlags{
		replacement:    half,
		replacementSet: true,
		append:         half,
		appendSet:      true,
	}); err == nil || !strings.Contains(err.Error(), "combined") {
		t.Fatalf("combined overflow error = %v", err)
	}
}

func TestApplyCLIPermissionModeIsRuntimeScopedAndSafetyPreserving(t *testing.T) {
	t.Run("unspecified preserves config", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Permission.Rules["write"] = "deny"
		if err := applyCLIPermissionMode(cfg, cliPermissionInherit); err != nil {
			t.Fatal(err)
		}
		if cfg.Permission.Rules["write"] != "deny" {
			t.Fatalf("unspecified mode changed write policy: %+v", cfg.Permission.Rules)
		}
	})

	t.Run("default leaves plan mode", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Permission.Enabled = false
		cfg.Plan.Enabled = true
		if err := applyCLIPermissionMode(cfg, cliPermissionDefault); err != nil {
			t.Fatal(err)
		}
		if !cfg.Permission.Enabled || cfg.Plan.Enabled {
			t.Fatalf("default mode = permission %v plan %v",
				cfg.Permission.Enabled, cfg.Plan.Enabled)
		}
	})

	t.Run("accept edits allows file mutations only", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Permission.Enabled = false
		cfg.Plan.Enabled = true
		if err := applyCLIPermissionMode(cfg, cliPermissionAcceptEdits); err != nil {
			t.Fatal(err)
		}
		if !cfg.Permission.Enabled {
			t.Fatal("acceptEdits did not enable the permission policy")
		}
		if cfg.Plan.Enabled {
			t.Fatal("acceptEdits left mutually exclusive plan mode active")
		}
		for _, tool := range acceptEditsPermissionTools {
			if cfg.Permission.Rules[tool] != "allow" {
				t.Errorf("%s policy = %q, want allow", tool, cfg.Permission.Rules[tool])
			}
		}
		for _, tool := range []string{"bash", "git_commit", "ssh"} {
			policy := cfg.Permission.DefaultPolicy
			if configured, ok := cfg.Permission.Rules[tool]; ok {
				policy = configured
			}
			if policy != "ask" {
				t.Fatalf("acceptEdits widened %s authority to %q", tool, policy)
			}
		}
		manager := permission.NewManager(
			permission.NewRulesFromConfig(
				cfg.Permission.DefaultPolicy, cfg.Permission.Rules),
			cfg.Permission.Enabled,
		)
		editDecision, err := manager.Check(
			context.Background(), "edit", map[string]any{"file_path": "main.go"})
		if err != nil || editDecision == nil || !editDecision.Allowed {
			t.Fatalf("acceptEdits edit decision = %+v, %v", editDecision, err)
		}
		bashDecision, err := manager.Check(
			context.Background(), "bash", map[string]any{"command": "go test ./..."})
		if err == nil || bashDecision == nil || bashDecision.Allowed {
			t.Fatalf("acceptEdits bash decision = %+v, %v; want unavailable prompt",
				bashDecision, err)
		}
	})

	t.Run("bypass keeps sandbox enabled", func(t *testing.T) {
		cfg := config.DefaultConfig()
		if !cfg.Tools.Bash.Sandbox {
			t.Fatal("test requires default sandbox")
		}
		if err := applyCLIPermissionMode(cfg, cliPermissionBypass); err != nil {
			t.Fatal(err)
		}
		if cfg.Permission.Enabled {
			t.Fatal("bypass mode left permission prompts enabled")
		}
		if cfg.Plan.Enabled {
			t.Fatal("bypass mode left mutually exclusive plan mode active")
		}
		if !cfg.Tools.Bash.Sandbox {
			t.Fatal("bypass mode disabled the bash sandbox")
		}
		manager := permission.NewManager(nil, cfg.Permission.Enabled)
		decision, err := manager.Check(
			context.Background(), "bash", map[string]any{"command": "go test ./..."})
		if err != nil || decision == nil || !decision.Allowed {
			t.Fatalf("bypass runtime decision = %+v, %v", decision, err)
		}
	})

	t.Run("dontAsk denies prompts while preserving explicit allows", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Plan.Enabled = true
		if err := applyCLIPermissionMode(cfg, cliPermissionDontAsk); err != nil {
			t.Fatal(err)
		}
		if !cfg.Permission.Enabled || !cfg.Permission.DontAsk || cfg.Plan.Enabled {
			t.Fatalf("dontAsk config = enabled %v dontAsk %v plan %v",
				cfg.Permission.Enabled, cfg.Permission.DontAsk, cfg.Plan.Enabled)
		}
		manager := permission.NewManager(
			permission.NewRulesFromConfig(
				cfg.Permission.DefaultPolicy, cfg.Permission.Rules),
			cfg.Permission.Enabled,
		)
		manager.SetDontAsk(cfg.Permission.DontAsk)
		allowed, err := manager.Check(
			context.Background(), "read", map[string]any{"file_path": "main.go"})
		if err != nil || allowed == nil || !allowed.Allowed {
			t.Fatalf("dontAsk read = %+v, %v", allowed, err)
		}
		denied, err := manager.Check(
			context.Background(), "write", map[string]any{"file_path": "main.go"})
		if err != nil || denied == nil || denied.Allowed ||
			!strings.Contains(denied.Reason, "dontAsk") {
			t.Fatalf("dontAsk write = %+v, %v", denied, err)
		}
	})

	t.Run("plan retains approval boundary", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Plan.Enabled = false
		cfg.Plan.RequireApproval = false
		if err := applyCLIPermissionMode(cfg, cliPermissionPlan); err != nil {
			t.Fatal(err)
		}
		if !cfg.Plan.Enabled || !cfg.Plan.RequireApproval {
			t.Fatalf("plan mode = enabled %v approval %v",
				cfg.Plan.Enabled, cfg.Plan.RequireApproval)
		}
	})
}

func TestResolveCLIToolPermissionRules(t *testing.T) {
	allowed, err := resolveCLIAllowedToolRules([]string{
		"Read", "Read(/src/**)", "Bash(git status *)",
		"Write,Read", "WebFetch(domain:example.com)", "mcp__github__*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(allowed, ","); got !=
		"read,read(/src/**),bash(git status *),write,web_fetch(domain:example.com),mcp__github__*" {
		t.Fatalf("allowed rules = %q", got)
	}

	denied, err := resolveCLIDeniedToolRules([]string{
		"Edit", "Edit(/generated/**)", "Bash(git push *)", "Agent(Explore)", "mcp__*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(denied, ","); got !=
		"edit,edit(/generated/**),bash(git push *),task(Explore),mcp__*" {
		t.Fatalf("denied rules = %q", got)
	}
	if _, err := resolveCLIAllowedToolRules([]string{"Bash("}); err == nil ||
		!strings.Contains(err.Error(), "--allowedTools") {
		t.Fatalf("malformed allowed rule error = %v", err)
	}
	if _, err := resolveCLIDeniedToolRules([]string{"SSH(prod.example.com)"}); err == nil ||
		!strings.Contains(err.Error(), "--disallowedTools") {
		t.Fatalf("malformed denied rule error = %v", err)
	}
}

func TestCapabilityDeniesForCLIRulesHidesOnlyBareMatches(t *testing.T) {
	available := []string{
		"read", "grep", "write", "bash", "mcp__github__create_pr",
	}
	rules, err := resolveCLIDeniedToolRules([]string{
		"Read", "Write", "Bash(git push *)", "mcp__*",
	})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := capabilityDeniesForCLIRules(available, rules)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(denied, ","); got !=
		"grep,mcp__github__create_pr,read,write" {
		t.Fatalf("capability denies = %q", got)
	}
	if _, err := capabilityDeniesForCLIRules(available, []string{"reed"}); err == nil ||
		!strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown bare deny error = %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type recordingHeadlessInputRunner struct {
	prompts []string
	schemas []*app.StructuredOutputSchema
}

func (r *recordingHeadlessInputRunner) RunHeadlessWithOptions(
	_ context.Context,
	prompt string,
	opts app.HeadlessOptions,
) (app.HeadlessResult, error) {
	r.prompts = append(r.prompts, prompt)
	r.schemas = append(r.schemas, opts.JSONSchema)
	result := app.HeadlessResult{
		SchemaVersion: app.HeadlessSchemaVersion,
		Type:          "result",
		Result:        prompt,
		SessionID:     "session-1",
		Status:        "success",
	}
	if err := json.NewEncoder(opts.Stdout).Encode(result); err != nil {
		return result, err
	}
	return result, nil
}

func nonEmptyLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestValidateResumeSelection(t *testing.T) {
	if _, err := validateResumeSelection(true, "saved-work"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("continue+resume error = %v", err)
	}
	for _, id := range []string{"../escape", "-option", "two words", " trailing ", "CON"} {
		if _, err := validateResumeSelection(false, id); err == nil {
			t.Errorf("unsafe session ID %q was accepted", id)
		}
	}
	for _, id := range []string{"session-123", "saved.work", "задача_42"} {
		got, err := validateResumeSelection(false, id)
		if err != nil || got != id {
			t.Errorf("valid session ID %q => %q, %v", id, got, err)
		}
	}
	if got, err := validateResumeSelection(true, ""); err != nil || got != "" {
		t.Fatalf("continue-only selection = %q, %v", got, err)
	}
}

func TestValidateSessionPersistenceFlags(t *testing.T) {
	if err := validateSessionPersistenceFlags(true, false, "saved-work", false); err == nil ||
		!strings.Contains(err.Error(), "--resume") {
		t.Fatalf("resume conflict error = %v", err)
	}
	if err := validateSessionPersistenceFlags(true, true, "", false); err == nil ||
		!strings.Contains(err.Error(), "--continue") {
		t.Fatalf("continue conflict error = %v", err)
	}
	if err := validateSessionPersistenceFlags(true, false, "", true); err == nil ||
		!strings.Contains(err.Error(), "--fork-session") {
		t.Fatalf("fork conflict error = %v", err)
	}
	if err := validateSessionPersistenceFlags(true, false, "", false); err != nil {
		t.Fatalf("ephemeral fresh session error = %v", err)
	}
	if err := validateSessionPersistenceFlags(false, true, "", true); err != nil {
		t.Fatalf("persistent continue error = %v", err)
	}
}

func TestValidateNewSessionSelection(t *testing.T) {
	const valid = "67c220a6-5ba6-4d36-95bd-2df9a9f49d94"
	if got, err := validateNewSessionSelection(valid, false, "", false); err != nil || got != valid {
		t.Fatalf("valid UUID = %q, %v", got, err)
	}
	for _, id := range []string{
		"not-a-uuid",
		"67C220A6-5BA6-4D36-95BD-2DF9A9F49D94",
		" 67c220a6-5ba6-4d36-95bd-2df9a9f49d94",
	} {
		if _, err := validateNewSessionSelection(id, false, "", false); err == nil {
			t.Errorf("invalid new session ID %q was accepted", id)
		}
	}
	if _, err := validateNewSessionSelection(valid, false, "saved", false); err == nil {
		t.Fatal("--session-id with --resume was accepted")
	}
	if _, err := validateNewSessionSelection("", false, "", true); err == nil {
		t.Fatal("--fork-session without resume selection was accepted")
	}
	if _, err := validateNewSessionSelection("", true, "", true); err != nil {
		t.Fatalf("--fork-session with --continue: %v", err)
	}
}

func TestWriteHeadlessFailure_EmitsOneVersionedEnvelope(t *testing.T) {
	var out bytes.Buffer
	failure := errors.New("exact session is corrupt")
	if err := writeHeadlessFailure(&out, "saved-work", "resume_failed", failure); err != nil {
		t.Fatalf("writeHeadlessFailure: %v", err)
	}

	decoder := json.NewDecoder(&out)
	var got app.HeadlessResult
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("stdout contains more than one JSON value: %v", err)
	}
	if got.SchemaVersion != app.HeadlessSchemaVersion || got.Type != "result" || got.Status != "error" {
		t.Fatalf("envelope = %+v", got)
	}
	if got.SessionID != "saved-work" || got.Error == nil || got.Error.Kind != "resume_failed" || got.Error.Message != failure.Error() {
		t.Fatalf("typed failure = %+v", got)
	}
}

func TestPrepareSessionForRun_AcquiresLeaseBeforeExactRestore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake := newFakeResumableApplication("new-session")

	lease, selected, err := prepareSessionForRun(fake, true, "saved-session", false, "", false)
	if err != nil {
		t.Fatalf("prepareSessionForRun: %v", err)
	}
	if selected != "saved-session" || fake.exactCalls != 1 || !fake.sawBusyDuringResume {
		t.Fatalf("exact preparation selected=%q calls=%d leaseHeld=%v", selected, fake.exactCalls, fake.sawBusyDuringResume)
	}
	if fake.session.GetID() != "saved-session" {
		t.Fatalf("restored ID = %q", fake.session.GetID())
	}
	if _, err := chat.AcquireSessionWriterLease("saved-session"); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatalf("second writer error = %v, want busy", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	reacquired, err := chat.AcquireSessionWriterLease("saved-session")
	if err != nil {
		t.Fatalf("lease was not released: %v", err)
	}
	_ = reacquired.Release()
}

func TestPrepareSessionForRun_ContinueReloadsSelectedIDUnderLease(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake := newFakeResumableApplication("new-session")
	fake.lastID = "latest-session"

	lease, selected, err := prepareSessionForRun(fake, true, "", true, "", false)
	if err != nil {
		t.Fatalf("prepareSessionForRun: %v", err)
	}
	defer lease.Release()
	if selected != "latest-session" || fake.lastCalls != 1 || fake.exactCalls != 1 || !fake.sawBusyDuringResume {
		t.Fatalf("continue selected=%q last=%d exact=%d leaseHeld=%v",
			selected, fake.lastCalls, fake.exactCalls, fake.sawBusyDuringResume)
	}
}

func TestPrepareSessionForRun_BusyOrFailedResumeCannotProceed(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	held, err := chat.AcquireSessionWriterLease("busy-session")
	if err != nil {
		t.Fatal(err)
	}
	fakeBusy := newFakeResumableApplication("new-session")
	if _, _, err := prepareSessionForRun(fakeBusy, true, "busy-session", false, "", false); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatalf("busy error = %v", err)
	}
	if fakeBusy.exactCalls != 0 {
		t.Fatalf("busy session was restored %d times before acquiring lease", fakeBusy.exactCalls)
	}
	_ = held.Release()

	fakeFailed := newFakeResumableApplication("new-session")
	fakeFailed.exactErr = errors.New("corrupt snapshot")
	if lease, _, err := prepareSessionForRun(fakeFailed, true, "corrupt-session", false, "", false); err == nil || lease != nil {
		t.Fatalf("failed resume = lease %v error %v", lease, err)
	}
	// The lease acquired before the failing restore must not leak.
	lease, err := chat.AcquireSessionWriterLease("corrupt-session")
	if err != nil {
		t.Fatalf("failed resume leaked lease: %v", err)
	}
	_ = lease.Release()
}

func TestPrepareSessionForRun_ExplicitNewIDIsExclusiveAndCannotOverwrite(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	const id = "67c220a6-5ba6-4d36-95bd-2df9a9f49d94"
	fake := newFakeResumableApplication("generated")

	lease, selected, err := prepareSessionForRun(fake, true, "", false, id, false)
	if err != nil {
		t.Fatalf("prepare explicit ID: %v", err)
	}
	if selected != id || fake.session.GetID() != id {
		t.Fatalf("selected/session ID = %q/%q", selected, fake.session.GetID())
	}
	if _, err := chat.AcquireSessionWriterLease(id); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatalf("explicit ID lease is not held: %v", err)
	}
	_ = lease.Release()

	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	fake.session.SetWorkDir(t.TempDir())
	if err := history.SaveFull(fake.session); err != nil {
		t.Fatal(err)
	}
	other := newFakeResumableApplication("other")
	if lease, _, err := prepareSessionForRun(other, true, "", false, id, false); !errors.Is(err, errSessionIDInUse) || lease != nil {
		t.Fatalf("persisted ID reuse = lease %v error %v", lease, err)
	}
}

func TestPrepareSessionForRun_ForkMovesWriterToFreshIdentity(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fake := newFakeResumableApplication("generated")

	lease, selected, err := prepareSessionForRun(fake, true, "source-session", false, "", true)
	if err != nil {
		t.Fatalf("prepare fork: %v", err)
	}
	defer lease.Release()
	if selected == "source-session" {
		t.Fatalf("fork reused source identity")
	}
	if _, err := uuid.Parse(selected); err != nil {
		t.Fatalf("fork ID %q is not UUID: %v", selected, err)
	}
	if fake.forkCalls != 1 || fake.forkSourceID != "source-session" || fake.session.GetID() != selected {
		t.Fatalf("fork calls/source/current = %d/%q/%q", fake.forkCalls, fake.forkSourceID, fake.session.GetID())
	}
	sourceProbe, err := chat.AcquireSessionWriterLease("source-session")
	if err != nil {
		t.Fatalf("source lease was not released: %v", err)
	}
	_ = sourceProbe.Release()
	if _, err := chat.AcquireSessionWriterLease(selected); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatalf("fork destination lease is not held: %v", err)
	}
}

func TestSessionPreparationErrorKindIsMachineReadable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		resuming bool
		want     string
	}{
		{
			name: "corrupt",
			err: &chat.SessionLoadError{
				Kind:      chat.SessionLoadKindCorrupt,
				SessionID: "saved",
				Err:       errors.New("bad JSON"),
			},
			resuming: true,
			want:     "session_corrupt",
		},
		{name: "provider", err: app.ErrSessionProviderMismatch, resuming: true, want: "session_provider_mismatch"},
		{name: "busy", err: chat.ErrSessionWriterLeaseBusy, resuming: true, want: "session_busy"},
		{name: "new-session lease IO", err: errors.New("disk unavailable"), want: "session_lease"},
		{name: "generic resume", err: errors.New("empty session"), resuming: true, want: "resume_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionPreparationErrorKind(fmt.Errorf("outer: %w", tt.err), tt.resuming); got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
		})
	}
}

type fakeResumableApplication struct {
	session             *chat.Session
	lastID              string
	lastErr             error
	exactErr            error
	lastCalls           int
	exactCalls          int
	forkCalls           int
	forkSourceID        string
	sawBusyDuringResume bool
}

func newFakeResumableApplication(id string) *fakeResumableApplication {
	session := chat.NewSession()
	session.SetID(id)
	return &fakeResumableApplication{session: session}
}

func (f *fakeResumableApplication) GetSession() *chat.Session { return f.session }

func (f *fakeResumableApplication) SelectNewSessionID(id string) error {
	f.session.SetID(id)
	return nil
}

func (f *fakeResumableApplication) ResumeLastSession() error {
	f.lastCalls++
	if f.lastErr != nil {
		return f.lastErr
	}
	f.session.SetID(f.lastID)
	return nil
}

func (f *fakeResumableApplication) ResumeSession(id string) error {
	f.exactCalls++
	probe, err := chat.AcquireSessionWriterLease(id)
	if errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		f.sawBusyDuringResume = true
	} else if err == nil {
		_ = probe.Release()
	}
	if f.exactErr != nil {
		return f.exactErr
	}
	f.session.SetID(id)
	return nil
}

func (f *fakeResumableApplication) ForkLoadedSession(id string) error {
	f.forkCalls++
	f.forkSourceID = f.session.GetID()
	f.session.SetID(id)
	return nil
}
