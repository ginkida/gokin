package commands

import (
	"context"
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

func (f *fakeModelSetter) GetModel() string          { return f.model }
func (f *fakeModelSetter) SetModel(name string)      { f.model = name }

type fakeAppForModel struct {
	*fakeAppForMCP
	setter *fakeModelSetter
}

func (f *fakeAppForModel) GetModelSetter() ModelSetter { return f.setter }

func newModelApp(active string, currentModel string) *fakeAppForModel {
	return &fakeAppForModel{
		fakeAppForMCP: &fakeAppForMCP{
			cfg: &config.Config{
				API: config.APIConfig{ActiveProvider: active},
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
