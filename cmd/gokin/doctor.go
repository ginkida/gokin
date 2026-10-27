package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gokin/internal/commands"
	"gokin/internal/repl"
	"gokin/internal/security"

	"github.com/spf13/cobra"
)

var doctorANSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

var doctorREPLDetector = repl.Detect

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose configuration and local environment without starting a model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			workDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("doctor: resolve working directory: %w", err)
			}
			cfg, err := loadConfiguredConfig(cfgFile)
			if err != nil {
				configPath := strings.TrimSpace(cfgFile)
				if configPath == "" {
					configPath = "(default config)"
				} else if absolute, absErr := filepath.Abs(configPath); absErr == nil {
					configPath = absolute
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"Gokin %s diagnostics\nConfig: %s\nConfiguration error: %v\n",
					version, configPath, err)
				return fmt.Errorf("doctor found an unreadable configuration")
			}
			if err := applyRunConfigOverrides(cfg, version, provider, model, baseURL, false); err != nil {
				return fmt.Errorf("doctor: apply runtime overrides: %w", err)
			}
			resolvedDeniedRules, err := resolveCLIDeniedToolRules(append(
				append([]string(nil), deniedTools...),
				deniedToolsCompat...,
			))
			if err != nil {
				return err
			}
			runtimeDenies := optionalRuntimeDeniesForCLIRules(resolvedDeniedRules)
			replCapabilityDisabled := bare || !cliStartupAllowsOptionalRuntime(
				toolCeiling, runtimeDenies, "repl_exec")
			configPath := strings.TrimSpace(cfgFile)
			if configPath != "" {
				if absolute, absErr := filepath.Abs(configPath); absErr == nil {
					configPath = absolute
				}
			}
			executablePath, _ := os.Executable()
			bashConfig := security.DefaultSandboxConfig()
			bashConfig.Enabled = cfg.Tools.Bash.Sandbox
			bashConfig.RequireSandbox = cfg.Tools.Bash.RequireSandbox
			bashConfig.AllowNetwork = cfg.Tools.Bash.AllowNetwork
			bashConfig.SensitivePaths = []string{configPath}
			bashStatus := security.NewSandboxPolicy(workDir, bashConfig).Status(cmd.Context())
			var hybridAvailability *repl.Availability
			if strings.ToLower(strings.TrimSpace(cfg.Engine.Mode)) != "tools" && !replCapabilityDisabled {
				availability := doctorREPLDetector(cmd.Context(), workDir)
				hybridAvailability = &availability
			}
			report := commands.RenderDoctor(commands.DoctorOptions{
				Version:             version,
				Config:              cfg,
				WorkDir:             workDir,
				ConfigPath:          configPath,
				ExecutablePath:      executablePath,
				CLI:                 true,
				RuntimeREPLDisabled: replCapabilityDisabled,
				HybridAvailability:  hybridAvailability,
				BashSandbox:         &bashStatus,
			})
			_, err = fmt.Fprint(cmd.OutOrStdout(), doctorANSIPattern.ReplaceAllString(report, ""))
			return err
		},
	}
}

func cliStartupAllowsOptionalRuntime(allowed, denied []string, name string) bool {
	contains := func(values []string) bool {
		for _, value := range values {
			if strings.TrimSpace(value) == name {
				return true
			}
		}
		return false
	}
	return (allowed == nil || contains(allowed)) && !contains(denied)
}
