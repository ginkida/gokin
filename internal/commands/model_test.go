package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
)

// fakeModelSetter is a minimal ModelSetter stub that just records the
// latest model name. Keeps these tests hermetic (no real client swap).
type fakeModelSetter struct {
	model string
}

func (f *fakeModelSetter) GetModel() string     { return f.model }
func (f *fakeModelSetter) SetModel(name string) { f.model = name }

type fakeAppForModel struct {
	*fakeAppForMCP
	setter     *fakeModelSetter
	applied    *config.Config
	applyCalls int
	clearCalls int
	clearErr   error
	events     []string
}

func (f *fakeAppForModel) GetModelSetter() ModelSetter { return f.setter }
func (f *fakeAppForModel) ApplyConfig(cfg *config.Config) error {
	f.events = append(f.events, "apply")
	f.applyCalls++
	f.applied = cfg.Clone()
	// Real ApplyConfig constructs and installs a replacement client after the
	// config commit. Mirror that lifecycle instead of relying on ModelCommand to
	// mutate the pre-commit setter captured at the start of Execute.
	f.setter = &fakeModelSetter{model: cfg.Model.Name}
	return nil
}
func (f *fakeAppForModel) ClearConversation() { f.clearCalls++ }
func (f *fakeAppForModel) ClearConversationChecked() error {
	f.events = append(f.events, "clear")
	f.clearCalls++
	return f.clearErr
}

func newModelApp(active string, currentModel string) *fakeAppForModel {
	return &fakeAppForModel{
		fakeAppForMCP: &fakeAppForMCP{
			cfg: &config.Config{
				API:   config.APIConfig{ActiveProvider: active},
				Model: config.ModelConfig{Provider: active, Name: currentModel},
			},
		},
		setter: &fakeModelSetter{model: currentModel},
	}
}

// TestModelCommand_KimiExampleIsCurrent guards against regression: an
// older iteration of the /model command advertised `k2.5` and
// `k2-thinking-turbo` — models retired in v0.69 when Kimi Coding Plan
// dropped to a single model (kimi-for-coding, K2.6). Users ran the
// examples, got "unknown model" errors, and bounced. The current hint
// must reference the live model.
func TestModelCommand_KimiExampleIsCurrent(t *testing.T) {
	app := newModelApp("kimi", "kimi-for-coding")
	cmd := &ModelCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// Must mention the current model.
	if !strings.Contains(out, "kimi-for-coding") {
		t.Errorf("expected current Kimi model in output:\n%s", out)
	}

	// Must NOT advertise retired models.
	for _, retired := range []string{"k2.5", "k2-thinking-turbo", "k2-turbo-preview"} {
		if strings.Contains(out, retired) {
			t.Errorf("retired Kimi model %q still advertised:\n%s", retired, out)
		}
	}

	// Sanity: the output must reflect what client.AvailableModels
	// actually registers for Kimi — otherwise we'd just lie to the user.
	available := client.GetModelsForProvider("kimi")
	if len(available) == 0 {
		t.Fatal("no Kimi models registered in client package")
	}
	for _, m := range available {
		if !strings.Contains(out, m.ID) {
			t.Errorf("registered Kimi model %q missing from /model output:\n%s", m.ID, out)
		}
	}
}

// TestModelCommand_KimiRetiredAliasRejected: if someone types the old
// alias, the command must say "unknown" rather than pretending to accept
// it. Regression guard on the ambiguous-match / exact-match flow.
func TestModelCommand_KimiRetiredAliasRejected(t *testing.T) {
	app := newModelApp("kimi", "kimi-for-coding")
	cmd := &ModelCommand{}
	out, _ := cmd.Execute(context.Background(), []string{"k2.5"}, app)
	if !strings.Contains(strings.ToLower(out), "unknown") {
		t.Errorf("retired alias should be rejected as unknown:\n%s", out)
	}
	// Active model must not have been mutated.
	if app.setter.model != "kimi-for-coding" {
		t.Errorf("model was switched despite unknown alias: got %q", app.setter.model)
	}
}

func TestModelCommand_ModelMatchingIsCaseInsensitive(t *testing.T) {
	app := newModelApp("minimax", "MiniMax-M2.5")
	cmd := &ModelCommand{}
	out, err := cmd.Execute(context.Background(), []string{"m2.7"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "MiniMax-M2.7") {
		t.Fatalf("expected switch output for MiniMax-M2.7, got:\n%s", out)
	}
	if app.setter.model != "MiniMax-M2.7" {
		t.Fatalf("model = %q, want MiniMax-M2.7", app.setter.model)
	}
	if app.clearCalls != 0 {
		t.Fatalf("same-provider model switch cleared conversation")
	}
}

func TestModelCommand_CrossProviderDeepSeekModelSwitch(t *testing.T) {
	app := newModelApp("glm", "glm-5.1")
	app.fakeAppForMCP.cfg.API.DeepSeekKey = "sk-deepseek-test-key-for-unit-test-12345"

	cmd := &ModelCommand{}
	out, err := cmd.Execute(context.Background(), []string{"deepseek-v4-pro"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "deepseek-v4-pro") || !strings.Contains(out, "session cleared") {
		t.Fatalf("expected DeepSeek switch output with session cleared, got:\n%s", out)
	}
	if app.setter.model != "deepseek-v4-pro" {
		t.Fatalf("setter model = %q, want deepseek-v4-pro", app.setter.model)
	}
	if app.applied == nil {
		t.Fatal("ApplyConfig was not called")
	}
	if app.applied.API.ActiveProvider != "deepseek" {
		t.Fatalf("ActiveProvider = %q, want deepseek", app.applied.API.ActiveProvider)
	}
	if app.applied.Model.Provider != "deepseek" {
		t.Fatalf("Model.Provider = %q, want deepseek", app.applied.Model.Provider)
	}
	if app.clearCalls != 1 {
		t.Fatalf("ClearConversation calls = %d, want 1", app.clearCalls)
	}
	if got := strings.Join(app.events, ","); got != "clear,apply" {
		t.Fatalf("cross-provider switch order = %q, want durable clear before apply", got)
	}
}

func TestModelCommand_CrossProviderClearFailurePreventsApply(t *testing.T) {
	app := newModelApp("glm", "glm-5.1")
	app.fakeAppForMCP.cfg.API.DeepSeekKey = "sk-deepseek-test-key-for-unit-test-12345"
	app.clearErr = errors.New("session disk full")

	out, err := (&ModelCommand{}).Execute(context.Background(), []string{"deepseek-v4-pro"}, app)
	if err == nil || !strings.Contains(err.Error(), "was not switched") {
		t.Fatalf("clear failure outcome = %q, %v; want fail-closed error", out, err)
	}
	if app.applyCalls != 0 {
		t.Fatalf("ApplyConfig calls = %d after failed clear, want 0", app.applyCalls)
	}
}

// The audit fix (v0.100.90): ClearConversationChecked returning the
// "cleared, durability unconfirmed" case must NOT be treated the same as a
// genuine clear failure — the conversation DID clear (every in-memory reset
// ran), only the on-disk durability confirmation is uncertain. Before the
// fix, clearConversationChecked forwarded this non-nil error verbatim and
// ModelCommand aborted the whole provider switch with a FALSE "was not
// switched" message while the old conversation was already gone — a split
// state where history vanished but the model/provider never changed.
func TestModelCommand_CrossProviderClearUncertainDurabilityStillSwitches(t *testing.T) {
	app := newModelApp("glm", "glm-5.1")
	app.fakeAppForMCP.cfg.API.DeepSeekKey = "sk-deepseek-test-key-for-unit-test-12345"
	app.clearErr = fmt.Errorf("%w: rename could not be fsync-confirmed", ErrConversationClearedUncertainDurability)

	out, err := (&ModelCommand{}).Execute(context.Background(), []string{"deepseek-v4-pro"}, app)
	if err != nil {
		t.Fatalf("uncertain-durability clear must not block the switch, got err = %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "deepseek-v4-pro") || !strings.Contains(out, "session cleared") {
		t.Fatalf("expected the switch to proceed as if cleared cleanly, got:\n%s", out)
	}
	if app.applyCalls != 1 {
		t.Fatalf("ApplyConfig calls = %d, want 1 — the switch must still happen", app.applyCalls)
	}
}

func TestModelCommand_CrossProviderRequiresCredentials(t *testing.T) {
	app := newModelApp("glm", "glm-5.1")

	cmd := &ModelCommand{}
	out, err := cmd.Execute(context.Background(), []string{"deepseek-v4-pro"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "/login deepseek") {
		t.Fatalf("expected login guidance for unconfigured DeepSeek, got:\n%s", out)
	}
	if app.applyCalls != 0 {
		t.Fatalf("ApplyConfig calls = %d, want 0", app.applyCalls)
	}
	if app.setter.model != "glm-5.1" {
		t.Fatalf("model changed despite missing credentials: %q", app.setter.model)
	}
}

// TestMatchModelInProvider covers the exact/substring/ambiguous matcher that
// both same-provider and cross-provider switching are built on.
func TestMatchModelInProvider(t *testing.T) {
	models := []client.ModelInfo{
		{ID: "acme-pro", Name: "Acme Pro"},
		{ID: "acme-pro-max", Name: "Acme Pro Max"},
		{ID: "acme-lite", Name: "Acme Lite"},
	}

	// Exact ID match wins even though it is a substring of acme-pro-max.
	if got, amb := matchModelInProvider("acme-pro", models); got != "acme-pro" || amb {
		t.Fatalf("exact match: got (%q,%v), want (acme-pro,false)", got, amb)
	}
	// Substring matching two models is ambiguous.
	if got, amb := matchModelInProvider("pro", models); !amb || got != "" {
		t.Fatalf("substring 'pro': got (%q,%v), want ('',true)", got, amb)
	}
	// Substring matching exactly one model resolves.
	if got, amb := matchModelInProvider("lite", models); got != "acme-lite" || amb {
		t.Fatalf("single substring: got (%q,%v), want (acme-lite,false)", got, amb)
	}
	// No match / empty query resolve to no result, no ambiguity.
	if got, amb := matchModelInProvider("zzz", models); got != "" || amb {
		t.Fatalf("no match: got (%q,%v)", got, amb)
	}
	if got, amb := matchModelInProvider("   ", models); got != "" || amb {
		t.Fatalf("empty query: got (%q,%v)", got, amb)
	}
}

// TestMatchModelAcrossProviders_NoMatch pins the cross-provider fallback's
// no-match path: an unknown query returns empty with no ambiguity, so the
// command falls through to "Unknown model" rather than switching.
func TestMatchModelAcrossProviders_NoMatch(t *testing.T) {
	p, m, amb := matchModelAcrossProviders("totally-unknown-model-xyz", "glm")
	if p != "" || m != "" || amb {
		t.Fatalf("got (%q,%q,%v), want empty with no ambiguity", p, m, amb)
	}
}

// TestModelCommand_AllProviderHintsReferRealModels walks every provider
// the command has a custom hint for, pulls the active-model registry
// for that provider, and asserts at least one registered model name
// appears in the rendered hint. Catches drift where a provider adds
// models but the hint still points at retired IDs.
func TestModelCommand_AllProviderHintsReferRealModels(t *testing.T) {
	providers := []string{"kimi", "glm", "minimax"}
	for _, p := range providers {
		t.Run(p, func(t *testing.T) {
			app := newModelApp(p, "")
			// GetModelsForProvider must return something; if the provider
			// has no current model, the setter returns "" and the code
			// falls through to "No models available". This assertion
			// guards that we maintain at least one model per cloud
			// provider.
			regs := client.GetModelsForProvider(p)
			if len(regs) == 0 {
				t.Fatalf("provider %s has no registered models — /model hint would be useless", p)
			}
			// Seed the fake setter with the first registered model so the
			// command doesn't error early.
			app.setter.model = regs[0].ID
			cmd := &ModelCommand{}
			out, err := cmd.Execute(context.Background(), nil, app)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			// The hint must include the short name of at least one
			// registered model, otherwise it's leading users astray.
			found := false
			for _, m := range regs {
				if strings.Contains(out, m.ID) || strings.Contains(out, extractShortName(m.ID)) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("provider %s hint advertises no registered model:\n%s", p, out)
			}
		})
	}
}

// TestModelCommand_ClearsStalePresetOnExplicitSelect pins that an explicit
// /model choice clears any stale model.preset. Otherwise MigrateConfig/
// NormalizeConfig (run on every ApplyConfig via NewClient) re-apply the preset
// and silently revert the selection to the preset's model.
func TestModelCommand_ClearsStalePresetOnExplicitSelect(t *testing.T) {
	app := newModelApp("glm", "glm-5")
	app.fakeAppForMCP.cfg.Model.Preset = "coding" // a stale preset would override the choice

	cmd := &ModelCommand{}
	if _, err := cmd.Execute(context.Background(), []string{"glm-5.1"}, app); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.applied == nil {
		t.Fatal("ApplyConfig was not called")
	}
	if app.applied.Model.Preset != "" {
		t.Fatalf("Preset = %q, want cleared (else MigrateConfig reverts the model)", app.applied.Model.Preset)
	}
	if app.applied.Model.Name != "glm-5.1" {
		t.Fatalf("Name = %q, want glm-5.1", app.applied.Model.Name)
	}
}
