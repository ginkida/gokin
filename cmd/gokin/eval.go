package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gokin/internal/evals"

	"github.com/spf13/cobra"
)

func newEvalCmd() *cobra.Command {
	evalCmd := &cobra.Command{
		Use:   "eval",
		Short: "Run coding-agent evals",
	}
	evalCmd.AddCommand(newEvalRunCmd())
	return evalCmd
}

func newEvalRunCmd() *cobra.Command {
	var opts evals.RunOptions
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run coding eval scenarios from a manifest",
		Long: `Run coding eval scenarios by copying each fixture into an isolated workspace,
running an agent command template there, then executing scenario verification commands.

The agent command receives environment variables like GOKIN_EVAL_PROMPT,
GOKIN_EVAL_SCENARIO_ID, GOKIN_EVAL_PROVIDER, GOKIN_EVAL_MODEL,
and GOKIN_EVAL_WORKSPACE. Template placeholders such as {{prompt}},
{{workspace}}, {{provider}}, {{model}}, and {{scenario_id}} are also supported.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Timeout = timeout
			results, err := evals.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}

			passed := 0
			for _, result := range results {
				if result.Status == "passed" || result.Status == "dry_run" {
					passed++
				}
				status := result.Status
				if result.Error != "" {
					status += ": " + result.Error
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", evalResultLabel(result), status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d/%d scenarios passed", passed, len(results))
			if opts.OutputPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " · results: %s", opts.OutputPath)
			}
			fmt.Fprintln(cmd.OutOrStdout())

			if passed != len(results) && !opts.DryRun {
				return fmt.Errorf("eval run failed: %d/%d scenarios passed", passed, len(results))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.ManifestPath, "manifest", "evals/coding/manifest.json", "eval manifest path")
	cmd.Flags().StringVar(&opts.FixturesRoot, "fixtures", "evals/coding/fixtures", "fixture root directory")
	cmd.Flags().StringVar(&opts.WorkRoot, "workdir", "", "workspace root for copied fixtures (default: temp dir)")
	cmd.Flags().StringVar(&opts.OutputPath, "output", ".gokin/evals/results.jsonl", "JSONL output path")
	cmd.Flags().StringVar(&opts.AgentCommand, "agent-command", "", "shell command template to run in each fixture workspace")
	cmd.Flags().StringArrayVar(&opts.ScenarioIDs, "scenario", nil, "scenario id to run; repeatable")
	cmd.Flags().StringArrayVar(&opts.Providers, "provider", nil, "provider to include in the matrix; repeatable")
	cmd.Flags().StringArrayVar(&opts.Models, "model", nil, "model to include in the matrix; repeatable")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "timeout per agent or verification command")
	cmd.Flags().BoolVar(&opts.KeepWorkspaces, "keep-workspaces", false, "keep temporary workspaces after the run")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "copy/list scenarios without running the agent command or verification")

	_ = cmd.RegisterFlagCompletionFunc("scenario", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		manifest, err := evals.LoadManifest(opts.ManifestPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var ids []string
		for _, scenario := range manifest.Scenarios {
			if strings.HasPrefix(scenario.ID, toComplete) {
				ids = append(ids, scenario.ID)
			}
		}
		return ids, cobra.ShellCompDirectiveNoFileComp
	})

	cmd.SetContext(context.Background())
	return cmd
}

func evalResultLabel(result evals.Result) string {
	var parts []string
	if result.Provider != "" {
		parts = append(parts, result.Provider)
	}
	if result.Model != "" {
		parts = append(parts, result.Model)
	}
	if len(parts) == 0 {
		return result.ScenarioID
	}
	return fmt.Sprintf("%s [%s]", result.ScenarioID, strings.Join(parts, "/"))
}
