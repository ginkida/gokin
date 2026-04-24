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
	clearCalls   int
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

// ClearConversation override tracks whether the session was cleared —
// used by provider-switch regression tests to ensure history
// incompatibility between providers is handled automatically.
func (f *fakeAppForAuth) ClearConversation() { f.clearCalls++ }

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

// Regression: switching providers via /login must clear session
// history. Mid-session swaps between Kimi and DeepSeek otherwise
// produced "content[].thinking must be passed back" 400s because
// the two providers disagree on how thinking signatures are stored
// in assistant turns.
func TestLogin_ClearsHistoryOnProviderSwitch(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "kimi"
	cfg.API.KimiKey = "sk-kimi-existing-key-ignore-me"
	app := newAuthApp(cfg)
	cmd := &LoginCommand{}

	out, err := cmd.Execute(context.Background(),
		[]string{"deepseek", "sk-deepseek-test-key-ignore-me-12345"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 1 {
		t.Errorf("ClearConversation calls = %d, want 1", app.clearCalls)
	}
	if !strings.Contains(out, "Session cleared") {
		t.Errorf("user-facing message should surface the clear, got: %q", out)
	}
}

// Counter-case: logging into the SAME provider (e.g. re-entering
// a fresh key) must NOT clear the session — users would lose work
// for no reason.
func TestLogin_DoesNotClearHistoryWhenProviderUnchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "deepseek"
	app := newAuthApp(cfg)
	cmd := &LoginCommand{}

	_, err := cmd.Execute(context.Background(),
		[]string{"deepseek", "sk-deepseek-new-key-ignore-me-12345"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 0 {
		t.Errorf("ClearConversation should NOT fire on same-provider re-login, got %d calls", app.clearCalls)
	}
}

// Edge: when ActiveProvider is empty (first login), we shouldn't
// clear anything — there's no history to invalidate and the message
// would confuse a first-time user.
func TestLogin_DoesNotClearOnFirstSetup(t *testing.T) {
	cfg := &config.Config{} // ActiveProvider == ""
	app := newAuthApp(cfg)
	cmd := &LoginCommand{}

	out, err := cmd.Execute(context.Background(),
		[]string{"deepseek", "sk-deepseek-test-key-ignore-me-12345"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 0 {
		t.Errorf("first setup should not clear, got %d calls", app.clearCalls)
	}
	if strings.Contains(out, "Session cleared") {
		t.Errorf("first setup should not surface a clear-note: %q", out)
	}
}

// /provider switch must also clear.
func TestProvider_ClearsHistoryOnSwitch(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "kimi"
	cfg.API.KimiKey = "sk-kimi-existing"
	cfg.API.DeepSeekKey = "sk-deepseek-ready-to-use"
	app := newAuthApp(cfg)
	cmd := &ProviderCommand{}

	out, err := cmd.Execute(context.Background(), []string{"deepseek"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 1 {
		t.Errorf("ClearConversation calls = %d, want 1", app.clearCalls)
	}
	if !strings.Contains(out, "session cleared") {
		t.Errorf("output should surface clear: %q", out)
	}
}

// Regression: /logout of the currently-active provider, when another
// provider is still configured, auto-switches to that provider. Before
// this fix the auto-switch silently kept session history built for the
// just-removed provider, which then 400'd on the next request to the
// new provider with the same thinking-signature mismatch that /login
// and /provider already handle.
func TestLogout_ActiveProviderAutoSwitch_ClearsHistory(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "kimi"
	cfg.API.KimiKey = "sk-kimi-existing-ignore-me"
	cfg.API.DeepSeekKey = "sk-deepseek-ready-ignore-me"
	app := newAuthApp(cfg)
	cmd := &LogoutCommand{}

	out, err := cmd.Execute(context.Background(), []string{"kimi"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 1 {
		t.Errorf("ClearConversation calls = %d, want 1 (autoswitch must clear cross-provider history)", app.clearCalls)
	}
	if !strings.Contains(out, "Auto-switched") {
		t.Errorf("output should mention the autoswitch: %q", out)
	}
	if !strings.Contains(out, "session cleared") {
		t.Errorf("output should surface the session clear so user understands why: %q", out)
	}
	if app.applied == nil || app.applied.API.ActiveProvider != "deepseek" {
		t.Errorf("autoswitch target = %q, want deepseek", app.applied.API.ActiveProvider)
	}
}

// Counter-case: logging out a NON-active provider just removes the key,
// no autoswitch, no session clear (history is still valid for the
// active provider).
func TestLogout_NonActiveProvider_DoesNotClearHistory(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "kimi"
	cfg.API.KimiKey = "sk-kimi-stays"
	cfg.API.DeepSeekKey = "sk-deepseek-goes-away"
	app := newAuthApp(cfg)
	cmd := &LogoutCommand{}

	_, err := cmd.Execute(context.Background(), []string{"deepseek"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 0 {
		t.Errorf("ClearConversation should NOT fire when logging out a non-active provider, got %d calls", app.clearCalls)
	}
	if app.applied == nil || app.applied.API.DeepSeekKey != "" {
		t.Errorf("DeepSeek key should be cleared")
	}
	if app.applied.API.ActiveProvider != "kimi" {
		t.Errorf("ActiveProvider should stay kimi, got %q", app.applied.API.ActiveProvider)
	}
}

// /provider X where X is already the active provider: previously returned
// a dead-end "Already using X" that confused users trying to recover from
// a stuck session ("I did /provider kimi and nothing happened"). The new
// message keeps the same no-op behavior but surfaces the escape hatches
// (/clear to reset, /provider to inspect, /login to switch).
func TestProvider_AlreadyOnProvider_SurfacesResetHint(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "kimi"
	cfg.API.KimiKey = "sk-kimi-existing"
	cfg.Model.Name = "kimi-for-coding"
	app := newAuthApp(cfg)
	cmd := &ProviderCommand{}

	out, err := cmd.Execute(context.Background(), []string{"kimi"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// Must NOT apply config or clear conversation (user is staying put).
	if app.applyCalls != 0 {
		t.Errorf("same-provider case must not re-apply config: got %d calls", app.applyCalls)
	}
	if app.clearCalls != 0 {
		t.Errorf("same-provider case must not auto-clear: got %d calls", app.clearCalls)
	}

	// Message must teach the user about /clear — the escape hatch most
	// relevant to the "why didn't /provider do anything?" scenario.
	if !strings.Contains(out, "Already using kimi") {
		t.Errorf("output should still confirm no-op: %q", out)
	}
	if !strings.Contains(out, "/clear") {
		t.Errorf("output should suggest /clear as a recovery path: %q", out)
	}
	if !strings.Contains(out, "kimi-for-coding") {
		t.Errorf("output should show the current model so user knows what they're on: %q", out)
	}
}

// Edge: logging out the only configured provider leaves no fallback to
// switch to. The user gets an onboarding hint but no autoswitch and no
// spurious history clear (the next /login will clear if provider differs).
func TestLogout_OnlyProvider_NoAutoswitchNoClear(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.ActiveProvider = "kimi"
	cfg.API.KimiKey = "sk-kimi-only"
	app := newAuthApp(cfg)
	cmd := &LogoutCommand{}

	out, err := cmd.Execute(context.Background(), []string{"kimi"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.clearCalls != 0 {
		t.Errorf("ClearConversation should NOT fire when no autoswitch happens, got %d", app.clearCalls)
	}
	if strings.Contains(out, "Auto-switched") {
		t.Errorf("should NOT auto-switch when no other providers exist: %q", out)
	}
	if !strings.Contains(strings.ToLower(out), "no api keys") {
		t.Errorf("should surface the no-keys onboarding hint: %q", out)
	}
}
