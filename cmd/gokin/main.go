package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gokin/internal/app"
	"gokin/internal/chat"
	"gokin/internal/config"
	"gokin/internal/logging"
	"gokin/internal/security"
	"gokin/internal/setup"

	"github.com/spf13/cobra"
)

var (
	// version is the dev-time fallback. Release builds inject the real value
	// via `-X main.version=$(git describe --tags)` — see .github/workflows/release.yml.
	// Bump this when merging a sprint worth of changes so `go build` without
	// ldflags still shows something sensible in /version.
	version       = "0.100.104"
	cfgFile       string
	model         string
	provider      string
	baseURL       string
	runSetup      bool
	continueLast  bool
	resumeSession string
	headless      bool
	prompt        string
	outputFormat  string
	addDirs       []string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "gokin",
		Short: "AI-powered CLI assistant for code",
		Long: `Gokin is a CLI tool for AI-assisted coding. Supports Kimi
(default), GLM, MiniMax, DeepSeek, and Ollama. It provides an
interactive chat interface with tools for reading, writing, and
editing files, running commands, and orchestrating multi-agent
workflows — with zero proxies between you and the provider you
choose.`,
		RunE: runApp,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.config/gokin/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&model, "model", "", "model to use (default depends on provider)")
	rootCmd.PersistentFlags().StringVar(&provider, "provider", "", "provider to use for this run (glm, minimax, kimi, deepseek, ollama)")
	rootCmd.PersistentFlags().StringVar(&baseURL, "base-url", "", "custom provider API base URL for this run (in-memory only)")
	rootCmd.PersistentFlags().BoolVar(&runSetup, "setup", false, "run the setup wizard")
	rootCmd.PersistentFlags().BoolVarP(&continueLast, "continue", "c", false, "continue the most recent session in this workspace")
	rootCmd.PersistentFlags().StringVarP(&resumeSession, "resume", "r", "", "resume an exact session ID or saved name")
	rootCmd.PersistentFlags().BoolVar(&headless, "headless", false, "run one prompt without the interactive TUI")
	rootCmd.PersistentFlags().StringVar(&prompt, "prompt", "", "prompt to run in headless mode")
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output-format", "text", "headless output format: text or json")
	rootCmd.PersistentFlags().StringArrayVar(&addDirs, "add-dir", nil, "grant access to a directory outside the workspace (repeatable; in-memory for this run)")

	// Version command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("gokin version %s\n", version)
		},
	})

	// Update command
	rootCmd.AddCommand(newUpdateCmd())
	rootCmd.AddCommand(newEvalCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runApp(cmd *cobra.Command, args []string) (runErr error) {
	headlessFormat, err := resolveHeadlessOutputFormat(headless, outputFormat)
	if err != nil {
		return err
	}

	// Once JSON headless mode is recognized, every startup/runtime failure must
	// still produce exactly one result envelope. RunHeadlessWithOptions writes
	// its own terminal envelope; this defer covers failures before execution
	// begins (CLI validation, config/auth, app init, and exact resume).
	jsonEnvelopeWritten := false
	failureKind := "cli"
	failureSessionID := strings.TrimSpace(resumeSession)
	defer func() {
		if runErr == nil || !headless || headlessFormat != app.HeadlessOutputJSON || jsonEnvelopeWritten {
			return
		}
		if encodeErr := writeHeadlessFailure(os.Stdout, failureSessionID, failureKind, runErr); encodeErr != nil {
			runErr = errors.Join(runErr, encodeErr)
		}
	}()

	if headless && strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("--prompt is required when --headless is set")
	}
	resumeID, err := validateResumeSelection(continueLast, resumeSession)
	if err != nil {
		if continueLast && resumeSession != "" {
			failureKind = "resume_conflict"
		} else {
			failureKind = "session_invalid_id"
		}
		return err
	}

	// Run setup wizard if requested
	if runSetup {
		failureKind = "setup"
		if headless {
			// The auto-invoked path below (triggered by ErrMissingAuth) has
			// always refused to run the wizard in headless mode; the
			// explicit --setup flag lacked the same guard, so
			// `--headless --setup` either blocked forever on stdin (a live
			// TTY) or died with a confusing "EOF" (redirected/closed stdin)
			// instead of headless mode's documented "never block, fail
			// clearly" contract.
			return fmt.Errorf("--setup requires an interactive terminal; it cannot run with --headless")
		}
		if err := setup.RunSetupWizard(); err != nil {
			return err
		}
		// Continue to start the app after setup
	}

	// Load configuration
	failureKind = "configuration"
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Set version from runtime
	cfg.Version = version

	if err := applyRuntimeOverrides(cfg, provider, model); err != nil {
		return err
	}
	if err := applyRuntimeBaseURLOverride(cfg, baseURL); err != nil {
		return err
	}

	// Validate configuration - if no API key, run setup wizard automatically
	if err := cfg.Validate(); err != nil {
		if errors.Is(err, config.ErrMissingAuth) {
			if headless {
				return err
			}
			// No API key configured - run setup wizard
			if err := setup.RunSetupWizard(); err != nil {
				return err
			}
			// Reload config after setup
			cfg, err = config.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			// Re-validate
			if err := cfg.Validate(); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// Get working directory
	failureKind = "app_init"
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// --add-dir grants (repeatable): resolve, refuse ungrantable locations, and
	// append in-memory only (never persisted).
	// Works for interactive, headless, and eval launches; the builder propagates
	// AllowedDirs to every path-scoping tool and seeds the agent runner at boot.
	if err := applyAddDirFlags(cfg, addDirs); err != nil {
		return err
	}

	// Create the application
	application, err := app.NewWithOptions(cfg, workDir, app.BuildOptions{
		NonInteractive: headless,
	})
	if err != nil {
		return fmt.Errorf("failed to create application: %w", err)
	}

	failureKind = "resume_failed"
	sessionLease, selectedSessionID, err := prepareSessionForRun(application, cfg.Session.Enabled, resumeID, continueLast)
	if selectedSessionID != "" {
		failureSessionID = selectedSessionID
	}
	if err != nil {
		failureKind = sessionPreparationErrorKind(err, resumeID != "" || continueLast)
		return err
	}
	if sessionLease != nil {
		if err := application.AdoptSessionWriterLease(sessionLease); err != nil {
			_ = sessionLease.Release()
			failureKind = "session_lease"
			return fmt.Errorf("adopt session writer lease: %w", err)
		}
		defer func() {
			if releaseErr := application.ReleaseSessionWriterLease(); releaseErr != nil {
				// Descriptor-owned OS locks are released by process exit even if
				// the explicit unlock reports an error; keep the already-emitted
				// JSON status aligned with the process exit code.
				fmt.Fprintf(os.Stderr, "Warning: failed to release session writer lease: %v\n", releaseErr)
			}
		}()
	}

	if headless {
		failureKind = "execution"
		jsonEnvelopeWritten = headlessFormat == app.HeadlessOutputJSON
		_, err := application.RunHeadlessWithOptions(cmd.Context(), prompt, app.HeadlessOptions{
			OutputFormat: headlessFormat,
			Stdout:       os.Stdout,
			Stderr:       os.Stderr,
		})
		return err
	}

	// Interactive resume is also fail-closed (prepared above): an explicitly
	// requested context must never silently turn into a fresh conversation.
	switch {
	case resumeID != "":
		fmt.Printf("Resumed session %s.\n", resumeID)
	case continueLast:
		fmt.Println("Resumed previous session.")
	}

	// Check for updates on startup (non-blocking notification). Wrapped in
	// defer-recover so a panic inside the update path (network library bug,
	// nil-deref in update.NewUpdater, etc.) doesn't crash gokin before the
	// TUI even starts. v0.78.1 wrapped the equivalent app/ goroutines —
	// this is the cmd/ counterpart.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("update-check goroutine panicked", "panic", r)
			}
		}()
		CheckForUpdateOnStartup(cfg, application)
	}()

	fmt.Println("\nStarting Gokin...")
	return application.Run()
}

func resolveHeadlessOutputFormat(headless bool, raw string) (app.HeadlessOutputFormat, error) {
	format := app.HeadlessOutputFormat(strings.ToLower(strings.TrimSpace(raw)))
	if format == "" {
		format = app.HeadlessOutputText
	}
	if format != app.HeadlessOutputText && format != app.HeadlessOutputJSON {
		return "", fmt.Errorf("invalid --output-format %q (want text or json)", raw)
	}
	if !headless && format != app.HeadlessOutputText {
		return "", fmt.Errorf("--output-format %s requires --headless", format)
	}
	return format, nil
}

func validateResumeSelection(continueLast bool, rawID string) (string, error) {
	if continueLast && rawID != "" {
		return "", fmt.Errorf("--continue and --resume are mutually exclusive")
	}
	if rawID == "" {
		return "", nil
	}
	if strings.TrimSpace(rawID) != rawID {
		return "", fmt.Errorf("invalid --resume session ID: leading or trailing whitespace is not allowed")
	}
	if err := chat.ValidateSessionID(rawID); err != nil {
		return "", fmt.Errorf("invalid --resume session ID: %w", err)
	}
	return rawID, nil
}

type resumableApplication interface {
	GetSession() *chat.Session
	ResumeLastSession() error
	ResumeSession(sessionID string) error
}

func prepareSessionForRun(application resumableApplication, persistenceEnabled bool, exactID string, continueLast bool) (*chat.SessionWriterLease, string, error) {
	if application == nil || application.GetSession() == nil {
		return nil, exactID, fmt.Errorf("session runtime is not initialized")
	}
	if (exactID != "" || continueLast) && !persistenceEnabled {
		return nil, exactID, fmt.Errorf("cannot resume: session persistence is disabled")
	}

	selectedID := exactID
	if continueLast {
		// LoadLast selects the newest usable snapshot. No model/tool call can
		// occur here. After acquiring its exact ID below we reload that snapshot
		// under the lease, closing the selection/acquisition TOCTOU window.
		if err := application.ResumeLastSession(); err != nil {
			return nil, "", fmt.Errorf("continue last session: %w", err)
		}
		selectedID = application.GetSession().GetID()
	}
	if selectedID == "" {
		selectedID = application.GetSession().GetID()
	}

	if !persistenceEnabled {
		return nil, selectedID, nil
	}
	lease, err := chat.AcquireSessionWriterLease(selectedID)
	if err != nil {
		return nil, selectedID, fmt.Errorf("acquire writer lease for session %q: %w", selectedID, err)
	}

	if exactID != "" || continueLast {
		if err := application.ResumeSession(selectedID); err != nil {
			_ = lease.Release()
			return nil, selectedID, fmt.Errorf("resume session %q: %w", selectedID, err)
		}
	}
	return lease, selectedID, nil
}

func sessionPreparationErrorKind(err error, resuming bool) string {
	if loadKind, ok := chat.SessionLoadErrorKindOf(err); ok {
		return "session_" + string(loadKind)
	}
	if errors.Is(err, app.ErrSessionProviderMismatch) {
		return "session_provider_mismatch"
	}
	if errors.Is(err, chat.ErrSessionWriterLeaseBusy) {
		return "session_busy"
	}
	if !resuming {
		return "session_lease"
	}
	return "resume_failed"
}

func writeHeadlessFailure(w io.Writer, sessionID, kind string, failure error) error {
	if w == nil {
		return fmt.Errorf("write headless JSON failure: output is nil")
	}
	if failure == nil {
		return fmt.Errorf("write headless JSON failure: failure is nil")
	}
	if strings.TrimSpace(kind) == "" {
		kind = "startup"
	}
	result := app.HeadlessResult{
		SchemaVersion: app.HeadlessSchemaVersion,
		Type:          "result",
		Result:        "",
		SessionID:     sessionID,
		Status:        "error",
		Error: &app.HeadlessError{
			Kind:    kind,
			Message: failure.Error(),
		},
		Usage: app.HeadlessUsage{},
		Cost:  app.HeadlessCost{},
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		return fmt.Errorf("write headless JSON failure: %w", err)
	}
	return nil
}

// applyAddDirFlags resolves each --add-dir value, refuses ungrantable locations
// (filesystem root, system dirs, .git, secret dirs), and appends it to
// cfg.Tools.AllowedDirs IN MEMORY ONLY (never saved). The builder propagates
// AllowedDirs to every path-scoping tool and seeds the agent runner at boot, so
// these grants are live before the first prompt — interactive, headless, or eval.
func applyAddDirFlags(cfg *config.Config, dirs []string) error {
	for _, raw := range dirs {
		d := strings.TrimSpace(raw)
		if d == "" {
			continue
		}
		if d == "~" || strings.HasPrefix(d, "~/") {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				if d == "~" {
					d = home
				} else {
					d = filepath.Join(home, d[2:])
				}
			}
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			return fmt.Errorf("--add-dir %q: %w", raw, err)
		}
		info, statErr := os.Stat(abs)
		if statErr != nil {
			return fmt.Errorf("--add-dir %q: %v", raw, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("--add-dir %q: not a directory", raw)
		}
		if resolved, rErr := filepath.EvalSymlinks(abs); rErr == nil {
			abs = resolved
		}
		if err := security.IsGrantableDir(abs); err != nil {
			return fmt.Errorf("--add-dir %q: %v", raw, err)
		}
		cfg.AddAllowedDir(abs) // dedups; in-memory only (no Save)
	}
	return nil
}

func applyRuntimeOverrides(cfg *config.Config, providerOverride, modelOverride string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	providerOverride = strings.ToLower(strings.TrimSpace(providerOverride))
	modelOverride = strings.TrimSpace(modelOverride)

	if providerOverride != "" {
		p := config.GetProvider(providerOverride)
		if p == nil {
			return fmt.Errorf("unknown provider %q (supported: %s)", providerOverride, strings.Join(config.ProviderNames(), ", "))
		}
		cfg.API.ActiveProvider = providerOverride
		cfg.API.Backend = providerOverride
		cfg.Model.Provider = providerOverride
		if modelOverride == "" && p.DefaultModel != "" {
			cfg.Model.Name = p.DefaultModel
		}
	}

	if modelOverride != "" {
		cfg.Model.Name = modelOverride
		if providerOverride == "" {
			if detected := config.DetectKnownProviderFromModel(modelOverride); detected != "" {
				cfg.Model.Provider = detected
				cfg.API.ActiveProvider = detected
				cfg.API.Backend = detected
			}
		}
	}

	return nil
}

func applyRuntimeBaseURLOverride(cfg *config.Config, raw string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid --base-url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid --base-url %q: must be an absolute http/https URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid --base-url %q: credentials, query, and fragment are not allowed", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	cfg.Model.CustomBaseURL = u.String()
	return nil
}
