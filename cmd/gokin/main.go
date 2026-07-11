package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gokin/internal/app"
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
	version  = "0.100.81"
	cfgFile  string
	model    string
	provider string
	runSetup bool
	resume   bool
	headless bool
	prompt   string
	addDirs  []string
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
	rootCmd.PersistentFlags().BoolVar(&runSetup, "setup", false, "run the setup wizard")
	rootCmd.PersistentFlags().BoolVar(&resume, "resume", false, "resume the last session")
	rootCmd.PersistentFlags().BoolVar(&headless, "headless", false, "run one prompt without the interactive TUI")
	rootCmd.PersistentFlags().StringVar(&prompt, "prompt", "", "prompt to run in headless mode")
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

func runApp(cmd *cobra.Command, args []string) error {
	if headless && strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("--prompt is required when --headless is set")
	}

	// Run setup wizard if requested
	if runSetup {
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
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Set version from runtime
	cfg.Version = version

	if err := applyRuntimeOverrides(cfg, provider, model); err != nil {
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
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Headless runs cannot answer Builder.checkAllowedDirs' first-run prompt.
	// Allow the current workspace in memory so evals and scripts never block
	// waiting on stdin before the agent starts.
	if headless && len(cfg.Tools.AllowedDirs) == 0 {
		cfg.Tools.AllowedDirs = append(cfg.Tools.AllowedDirs, workDir)
	}

	// --add-dir grants (repeatable): resolve, refuse ungrantable locations, and
	// append in-memory only (never persisted — like the headless workDir append).
	// Works for interactive, headless, and eval launches; the builder propagates
	// AllowedDirs to every path-scoping tool and seeds the agent runner at boot.
	if err := applyAddDirFlags(cfg, addDirs); err != nil {
		return err
	}

	// Create the application
	application, err := app.New(cfg, workDir)
	if err != nil {
		return fmt.Errorf("failed to create application: %w", err)
	}

	if headless {
		// Honor --resume in headless too: load the prior session so a
		// sequence of `gokin --headless --resume` calls continues ONE
		// conversation (scriptable multi-turn sessions). Without this the
		// flag was a silent no-op here — the `if headless { return }` short-
		// circuited before the interactive resume block below. Warning goes to
		// stderr so stdout stays the model answer.
		if resume {
			if err := application.ResumeLastSession(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to resume session: %v\n", err)
			}
		}
		return application.RunHeadless(cmd.Context(), prompt)
	}

	// Attempt to resume session if requested
	if resume {
		if err := application.ResumeLastSession(); err != nil {
			// Don't fail completely, just warn
			fmt.Printf("Warning: failed to resume session: %v\nStarting new session instead.\n", err)
			// Small pause to let user read the warning
			// (not ideal but better than complex UI logic here)
		} else {
			fmt.Println("Resumed previous session.")
		}
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
