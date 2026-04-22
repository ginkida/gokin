package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
)

// fakeAppForAuth is a narrow AppInterface stub focused on /login, /logout and
// /provider. It reuses fakeAppForMCP's embedded zero-value stubs.
type fakeAppForAuth struct {
	*fakeAppForMCP
	applied      *config.Config
	applyErr     error
	applyCalls   int
	applyLatency time.Duration
}

func (f *fakeAppForAuth) ApplyConfig(cfg *config.Config) error {
	f.applyCalls++
	if f.applyLatency > 0 {
		time.Sleep(f.applyLatency)
	}
	if f.applyErr != nil {
		return f.applyErr
	}
	// Shallow copy so caller-side mutation after Apply can't mask bugs.
	copyCfg := *cfg
	f.applied = &copyCfg
	return nil
}

func newAuthApp(cfg *config.Config) *fakeAppForAuth {
	return &fakeAppForAuth{
		fakeAppForMCP: &fakeAppForMCP{cfg: cfg},
	}
}

// — /login —

func TestLogin_NoArgs_ShowsStatus(t *testing.T) {
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	done := make(chan string, 1)

	// Run in a goroutine with a deadline so a regression that introduces a
	// blocking path will fail the test instead of hanging CI.
	go func() {
		out, err := cmd.Execute(context.Background(), nil, app)
		if err != nil {
			t.Errorf("unexpected err: %v", err)
		}
		done <- out
	}()

	select {
	case out := <-done:
		if !strings.Contains(out, "API Key Status") {
			t.Errorf("status output missing header: %q", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/login with no args hung (>2s)")
	}
}

func TestLogin_ProviderWithoutKey_ShowsUsage(t *testing.T) {
	// This is the exact path the user hit: typed "/login kimi" expecting
	// something to happen, got what should be instant help text.
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	done := make(chan string, 1)

	go func() {
		out, _ := cmd.Execute(context.Background(), []string{"kimi"}, app)
		done <- out
	}()

	select {
	case out := <-done:
		if !strings.Contains(strings.ToLower(out), "kimi") {
			t.Errorf("output should mention kimi: %q", out)
		}
		if !strings.Contains(out, "/login kimi") {
			t.Errorf("output should echo the usage format: %q", out)
		}
		if !strings.Contains(out, "kimi.com") {
			t.Errorf("output should include the setup URL: %q", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/login kimi (no key) hung (>2s) — the regression the user hit")
	}
	if app.applyCalls != 0 {
		t.Errorf("ApplyConfig called with no key: %d times", app.applyCalls)
	}
}

func TestLogin_UnknownProvider_ListsSupported(t *testing.T) {
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	out, err := cmd.Execute(context.Background(), []string{"no-such-provider-xyz"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Unknown provider") {
		t.Errorf("output should call out unknown provider: %q", out)
	}
	for _, p := range []string{"kimi", "glm", "minimax"} {
		if !strings.Contains(out, "/login "+p) {
			t.Errorf("output should list /login %s: %q", p, out)
		}
	}
}

func TestLogin_Ollama_NoKeyNeeded(t *testing.T) {
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	out, err := cmd.Execute(context.Background(), []string{"ollama"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "does not require") {
		t.Errorf("ollama path should say no key needed: %q", out)
	}
	if app.applyCalls != 0 {
		t.Errorf("ollama path should not mutate config")
	}
}

func TestLogin_ShortKey_Rejected(t *testing.T) {
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	out, err := cmd.Execute(context.Background(), []string{"kimi", "short"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "too short") {
		t.Errorf("short key should be rejected with clear message: %q", out)
	}
	if app.applyCalls != 0 {
		t.Errorf("short key must not be persisted")
	}
}

func TestLogin_ValidKey_SavesAndActivates(t *testing.T) {
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}

	key := "sk-kimi-" + strings.Repeat("x", 40)
	done := make(chan struct{}, 1)

	go func() {
		out, err := cmd.Execute(context.Background(), []string{"kimi", key}, app)
		if err != nil {
			t.Errorf("unexpected err: %v", err)
		}
		if !strings.Contains(out, "Kimi") {
			t.Errorf("output should mention Kimi: %q", out)
		}
		done <- struct{}{}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("/login kimi <key> hung (>2s) — ApplyConfig should be fast in tests")
	}

	if app.applyCalls != 1 {
		t.Errorf("ApplyConfig calls = %d, want 1", app.applyCalls)
	}
	if app.applied == nil {
		t.Fatal("applied config is nil")
	}
	if app.applied.API.KimiKey != key {
		t.Errorf("KimiKey = %q, want %q", app.applied.API.KimiKey, key)
	}
	if app.applied.API.ActiveProvider != "kimi" {
		t.Errorf("ActiveProvider = %q, want kimi", app.applied.API.ActiveProvider)
	}
	if app.applied.Model.Provider != "kimi" {
		t.Errorf("Model.Provider = %q, want kimi", app.applied.Model.Provider)
	}
	if app.applied.Model.Name != "kimi-for-coding" {
		t.Errorf("Model.Name = %q, want kimi-for-coding", app.applied.Model.Name)
	}
}

func TestLogin_KeyWithSurroundingWhitespace_Trimmed(t *testing.T) {
	// Users paste keys from browsers that include a trailing newline or a
	// wrapping quote. The command must accept those forgivingly.
	cases := []struct {
		name string
		raw  string
	}{
		{"trailing newline", "sk-kimi-" + strings.Repeat("x", 40) + "\n"},
		{"leading+trailing space", "   sk-kimi-" + strings.Repeat("x", 40) + "   "},
		{"double-quoted", `"sk-kimi-` + strings.Repeat("x", 40) + `"`},
		{"single-quoted", `'sk-kimi-` + strings.Repeat("x", 40) + `'`},
	}
	want := "sk-kimi-" + strings.Repeat("x", 40)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newAuthApp(&config.Config{})
			cmd := &LoginCommand{}
			_, err := cmd.Execute(context.Background(), []string{"kimi", tc.raw}, app)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if app.applied == nil {
				t.Fatal("applied config is nil")
			}
			if app.applied.API.KimiKey != want {
				t.Errorf("KimiKey = %q, want %q (raw input: %q)", app.applied.API.KimiKey, want, tc.raw)
			}
		})
	}
}

func TestLogin_ApplyConfigError_ReportedNotHung(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.applyErr = errorString("config disk full")
	cmd := &LoginCommand{}

	key := "sk-kimi-" + strings.Repeat("x", 40)
	done := make(chan string, 1)
	go func() {
		out, _ := cmd.Execute(context.Background(), []string{"kimi", key}, app)
		done <- out
	}()

	select {
	case out := <-done:
		if !strings.Contains(out, "Failed to save") {
			t.Errorf("output should report save failure: %q", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error path hung")
	}
}

// TestLogin_ContextCancellation_ReturnsPromptly guards against the specific
// class of bug the user reported: if something starts blocking synchronously,
// Ctrl+C / cancel must still free the command goroutine.
func TestLogin_ContextCancellation_ReturnsPromptly(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.applyLatency = 100 * time.Millisecond
	cmd := &LoginCommand{}

	ctx, cancel := context.WithCancel(context.Background())
	key := "sk-kimi-" + strings.Repeat("x", 40)

	done := make(chan struct{}, 1)
	go func() {
		_, _ = cmd.Execute(ctx, []string{"kimi", key}, app)
		done <- struct{}{}
	}()

	// Cancel immediately — command should wrap up without hitting the 1s wall.
	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("/login did not return within 1s after cancel")
	}
}

func TestLogin_KeyWithSpaces_Rejected(t *testing.T) {
	// strings.Fields() in the Parser would split the key into multiple args.
	// The command must detect this and warn, not silently save a fragment.
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	out, err := cmd.Execute(context.Background(), []string{"kimi", "sk-kimi", "trailing-garbage"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "spaces") && !strings.Contains(strings.ToLower(out), "parts") {
		t.Errorf("should warn about multi-part key: %q", out)
	}
	if app.applyCalls != 0 {
		t.Errorf("multi-part key must NOT be persisted (would save a fragment)")
	}
}

func TestLogin_URLAsKey_Rejected(t *testing.T) {
	// Easy mistake: paste the key URL instead of the key itself.
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	out, err := cmd.Execute(context.Background(), []string{"kimi", "https://kimi.com/settings/keys"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "url") {
		t.Errorf("should call out that user pasted a URL: %q", out)
	}
	if app.applyCalls != 0 {
		t.Errorf("URL must not be persisted as a key")
	}
}

func TestLogin_SuccessMessage_IncludesMaskedKey(t *testing.T) {
	// The full key must never be echoed back to the TUI — anyone reading
	// over the user's shoulder shouldn't see it.
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	key := "sk-kimi-" + strings.Repeat("SECRETSECRET", 4)
	out, err := cmd.Execute(context.Background(), []string{"kimi", key}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if strings.Contains(out, key) {
		t.Errorf("full key leaked into output: %q", out)
	}
	if !strings.Contains(out, "...") {
		t.Errorf("masked key (with ...) should be in output: %q", out)
	}
}

func TestLogin_NoKeyUsage_MentionsConfigPath(t *testing.T) {
	app := newAuthApp(&config.Config{})
	cmd := &LoginCommand{}
	out, _ := cmd.Execute(context.Background(), []string{"kimi"}, app)
	if !strings.Contains(out, "config.yaml") {
		t.Errorf("usage should mention config file path: %q", out)
	}
}

// — /provider —

func TestProvider_NoArgs_ShowsSummary(t *testing.T) {
	app := newAuthApp(&config.Config{
		API: config.APIConfig{KimiKey: "set-kimi"},
	})
	cmd := &ProviderCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "AI Providers") {
		t.Errorf("summary header missing: %q", out)
	}
}

func TestProvider_SwitchRequiresKey(t *testing.T) {
	// Kimi not configured — switching must NOT silently activate a keyless provider.
	app := newAuthApp(&config.Config{})
	cmd := &ProviderCommand{}
	out, err := cmd.Execute(context.Background(), []string{"kimi"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "not configured") {
		t.Errorf("should tell user kimi isn't configured: %q", out)
	}
	if app.applyCalls != 0 {
		t.Errorf("switch without key should not apply config")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
