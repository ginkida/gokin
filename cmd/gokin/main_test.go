package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"gokin/internal/app"
	"gokin/internal/chat"
	"gokin/internal/config"
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
		{name: "json needs headless", headless: false, raw: "json", wantErr: "requires --headless"},
		{name: "unknown", headless: true, raw: "yaml", wantErr: "want text or json"},
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

	lease, selected, err := prepareSessionForRun(fake, true, "saved-session", false)
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

	lease, selected, err := prepareSessionForRun(fake, true, "", true)
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
	if _, _, err := prepareSessionForRun(fakeBusy, true, "busy-session", false); !errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		t.Fatalf("busy error = %v", err)
	}
	if fakeBusy.exactCalls != 0 {
		t.Fatalf("busy session was restored %d times before acquiring lease", fakeBusy.exactCalls)
	}
	_ = held.Release()

	fakeFailed := newFakeResumableApplication("new-session")
	fakeFailed.exactErr = errors.New("corrupt snapshot")
	if lease, _, err := prepareSessionForRun(fakeFailed, true, "corrupt-session", false); err == nil || lease != nil {
		t.Fatalf("failed resume = lease %v error %v", lease, err)
	}
	// The lease acquired before the failing restore must not leak.
	lease, err := chat.AcquireSessionWriterLease("corrupt-session")
	if err != nil {
		t.Fatalf("failed resume leaked lease: %v", err)
	}
	_ = lease.Release()
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
	sawBusyDuringResume bool
}

func newFakeResumableApplication(id string) *fakeResumableApplication {
	session := chat.NewSession()
	session.SetID(id)
	return &fakeResumableApplication{session: session}
}

func (f *fakeResumableApplication) GetSession() *chat.Session { return f.session }

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
