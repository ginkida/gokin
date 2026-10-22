package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"gokin/internal/pluginpolicy"
)

// NewCommand exposes the same lifecycle operations to the terminal and the
// interactive /plugin command. It constructs no model client.
func NewCommand(options func() (Options, error)) *cobra.Command {
	var scope string
	var asJSON, available, keepData bool
	var configValues, accept []string
	var installValuesStdin bool
	root := &cobra.Command{Use: "plugin", Aliases: []string{"plugins"}, Short: "Manage Claude-compatible plugins and marketplaces", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "print structured results")
	root.PersistentFlags().StringVarP(&scope, "scope", "s", "", "user, project or local (install/uninstall default: user; other operations auto-detect)")
	root.PersistentFlags().StringArrayVar(&accept, "accept-command", nil, "accept exactly this SHA-256 of a displayed install/helper command (repeatable)")
	newManager := func() (*Manager, error) {
		opts, err := options()
		if err != nil {
			return nil, err
		}
		return NewManager(opts)
	}
	root.AddCommand(tagCommand(newManager, &asJSON))
	result := func(cmd *cobra.Command, operation string, run func(*Manager) (Result, error)) error {
		manager, err := newManager()
		if err != nil {
			return err
		}
		value, err := run(manager)
		if err != nil {
			value = Result{Command: operation, Outcome: "failed", Message: err.Error()}
			var approval *CommandApprovalError
			if errors.As(err, &approval) {
				shown := approval.ShownCommand
				value.ShownCommand = &shown
			}
		}
		if asJSON {
			if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(value); writeErr != nil {
				return writeErr
			}
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), value.Message)
			if value.Command == "marketplace-add" || value.Command == "marketplace-update" {
				for _, id := range value.Pruned {
					fmt.Fprintln(cmd.OutOrStdout(), "Removed from marketplace:", id)
				}
			}
			for _, warning := range value.Warnings {
				fmt.Fprintln(cmd.OutOrStdout(), "Warning:", warning)
			}
		}
		return err
	}
	for _, action := range []string{"install", "update"} {
		action := action
		command := &cobra.Command{Use: action + " <name[@marketplace]>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			values := map[string]string{}
			if installValuesStdin {
				var err error
				values, err = readOptionValues(cmd.InOrStdin())
				if err != nil {
					return err
				}
			}
			var argumentConfig []string
			for _, raw := range configValues {
				key, value, ok := strings.Cut(raw, "=")
				if !ok || key == "" {
					return fmt.Errorf("--config requires key=value")
				}
				values[key] = value
				argumentConfig = append(argumentConfig, key)
			}
			return result(cmd, action, func(manager *Manager) (Result, error) {
				return manager.Install(cmd.Context(), args[0], InstallOptions{Scope: scope, Config: values, ArgumentConfig: argumentConfig, AcceptCommands: accept, Update: action == "update"})
			})
		}}
		command.Flags().StringArrayVar(&configValues, "config", nil, "set a declared plugin option as key=value (repeatable)")
		command.Flags().BoolVar(&installValuesStdin, "values-stdin", false, "read declared option values, including secrets, from stdin as JSON")
		if action == "install" {
			command.Aliases = []string{"i"}
		}
		root.AddCommand(command)
	}
	for _, action := range []string{"enable", "disable"} {
		action := action
		root.AddCommand(&cobra.Command{Use: action + " <name[@marketplace]>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return result(cmd, action, func(manager *Manager) (Result, error) {
				return manager.SetEnabled(cmd.Context(), args[0], scope, action == "enable")
			})
		}})
	}
	remove := &cobra.Command{Use: "uninstall <name[@marketplace]>", Aliases: []string{"remove", "rm"}, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return result(cmd, "uninstall", func(manager *Manager) (Result, error) {
			return manager.Uninstall(cmd.Context(), args[0], RemoveOptions{Scope: scope, KeepData: keepData})
		})
	}}
	remove.Flags().BoolVar(&keepData, "keep-data", false, "preserve persistent plugin data")
	root.AddCommand(remove)
	var pruneDryRun, pruneYes bool
	prune := &cobra.Command{Use: "prune", Aliases: []string{"autoremove"}, Short: "Remove unused auto-installed dependencies", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return result(cmd, "prune", func(manager *Manager) (Result, error) {
			value, err := manager.Prune(cmd.Context(), PruneOptions{Scope: scope, DryRun: pruneDryRun || !pruneYes, KeepData: keepData})
			if value.DryRun && len(value.Pruned) > 0 {
				value.Message += " Dry run; pass --yes to remove."
			}
			return value, err
		})
	}}
	prune.Flags().BoolVar(&pruneDryRun, "dry-run", false, "preview unused dependencies without changes")
	prune.Flags().BoolVarP(&pruneYes, "yes", "y", false, "remove the listed unused dependencies")
	prune.Flags().BoolVar(&keepData, "keep-data", false, "preserve persistent plugin data")
	root.AddCommand(prune)
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := newManager()
		if err != nil {
			return err
		}
		rows, err := manager.List()
		if err != nil {
			return err
		}
		if available {
			choices, err := manager.Available()
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"installed": rows, "available": choices})
			}
			for _, entry := range choices {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", entry.PluginID, entry.Description)
			}
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
		}
		if len(rows) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No plugins installed.")
		}
		for _, row := range rows {
			state := "disabled"
			if row.Enabled {
				state = "enabled"
			}
			if row.Status == "renamed" {
				state = "Renamed to " + row.RenamedTo
			}
			if row.Status == "removed-from-marketplace" {
				state = "Flagged: Removed from marketplace"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s [%s] %s (%s)\n", row.ID, row.Scope, state, row.Version)
			for _, issue := range row.Errors {
				fmt.Fprintln(cmd.OutOrStdout(), "  Error:", issue)
			}
			for _, warning := range row.Warnings {
				fmt.Fprintln(cmd.OutOrStdout(), "  Warning:", warning)
			}
		}
		return nil
	}}
	list.Flags().BoolVar(&available, "available", false, "also list marketplace plugins")
	root.AddCommand(list)
	root.AddCommand(&cobra.Command{Use: "validate <path>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := newManager()
		if err != nil {
			return err
		}
		report, err := manager.Validate(args[0])
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Validation passed:", report.Name)
		for _, warning := range report.Warnings {
			fmt.Fprintln(cmd.OutOrStdout(), "Warning:", warning)
		}
		return nil
	}})
	var valuesStdin bool
	configure := &cobra.Command{Use: "configure <name@marketplace>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !valuesStdin {
			return fmt.Errorf("provide --values-stdin with a JSON object of declared option values")
		}
		values, err := readOptionValues(cmd.InOrStdin())
		if err != nil {
			return err
		}
		return result(cmd, "configure", func(manager *Manager) (Result, error) { return manager.Configure(cmd.Context(), args[0], values) })
	}}
	configure.Flags().BoolVar(&valuesStdin, "values-stdin", false, "read option values from stdin as JSON")
	root.AddCommand(configure)
	market := &cobra.Command{Use: "marketplace", Short: "Manage plugin catalogs"}
	market.AddCommand(&cobra.Command{Use: "auto-update <name> <on|off>", Short: "Set automatic catalog and plugin updates", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if args[1] != "on" && args[1] != "off" {
			return fmt.Errorf("auto-update requires on or off")
		}
		return result(cmd, "marketplace-auto-update", func(manager *Manager) (Result, error) {
			return manager.SetMarketplaceAutoUpdate(cmd.Context(), args[0], args[1] == "on")
		})
	}})
	root.AddCommand(&cobra.Command{Use: "sync", Short: "Reconcile declared plugins and automatic updates", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := newManager()
		if err != nil {
			return err
		}
		report, err := manager.SyncStartup(cmd.Context())
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
		if asJSON {
			if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(report); writeErr != nil {
				return writeErr
			}
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Plugins: %d marketplaces registered, %d installed, %d updated, %d removed.\n", len(report.Registered), len(report.Installed), len(report.Updated), len(report.Removed))
			for _, issue := range append(append(report.Skipped, report.Errors...), report.Renamed...) {
				fmt.Fprintln(cmd.OutOrStdout(), issue)
			}
		}
		if err != nil {
			return err
		}
		if len(report.Errors) > 0 {
			return fmt.Errorf("%d plugin synchronization operations failed", len(report.Errors))
		}
		return nil
	}})
	market.AddCommand(&cobra.Command{Use: "add <source>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return result(cmd, "marketplace-add", func(manager *Manager) (Result, error) { return manager.AddMarketplace(cmd.Context(), args[0], accept) })
	}})
	market.AddCommand(&cobra.Command{Use: "update <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return result(cmd, "marketplace-update", func(manager *Manager) (Result, error) {
			return manager.UpdateMarketplace(cmd.Context(), args[0], accept)
		})
	}})
	market.AddCommand(&cobra.Command{Use: "remove <name>", Aliases: []string{"rm"}, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return result(cmd, "marketplace-remove", func(manager *Manager) (Result, error) { return manager.RemoveMarketplace(cmd.Context(), args[0]) })
	}})
	market.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := newManager()
		if err != nil {
			return err
		}
		entries, err := manager.MarketplaceList()
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
		}
		names := make([]string, 0, len(entries))
		for name := range entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", name, entries[name].Source.Kind)
		}
		if len(names) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No marketplaces registered.")
		}
		return nil
	}})
	root.AddCommand(market)
	descriptions := map[string]string{"install": "Install and validate a marketplace plugin", "update": "Update an installed plugin", "enable": "Enable a plugin in one scope", "disable": "Disable a plugin in one scope", "uninstall": "Uninstall a plugin from one scope", "list": "List installed plugins and their status", "configure": "Save validated plugin option values", "validate": "Validate plugin or marketplace content without execution"}
	for _, child := range root.Commands() {
		if child.Short == "" {
			child.Short = descriptions[child.Name()]
		}
	}
	for _, child := range market.Commands() {
		if child.Short == "" {
			child.Short = map[string]string{"add": "Register a marketplace source", "update": "Refresh a registered marketplace", "remove": "Remove an empty marketplace", "list": "List registered marketplaces"}[child.Name()]
		}
	}
	root.SetIn(os.Stdin)
	var wrap func(*cobra.Command)
	wrap = func(command *cobra.Command) {
		if run := command.RunE; run != nil {
			command.RunE = func(cmd *cobra.Command, args []string) error {
				for _, hash := range accept {
					if len(hash) != 64 || !hexSHA.MatchString(hash) {
						return fmt.Errorf("--accept-command requires 64 hex digits")
					}
				}
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				cmd.SetContext(ctx)
				return run(cmd, args)
			}
		}
		for _, child := range command.Commands() {
			wrap(child)
		}
	}
	wrap(root)
	return root
}

func readOptionValues(reader io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 64<<10+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("plugin options exceed input limit")
	}
	var values map[string]string
	if pluginpolicy.ValidateJSON(data) != nil || json.Unmarshal(data, &values) != nil || values == nil {
		return nil, fmt.Errorf("plugin values must be a JSON object of strings")
	}
	return values, nil
}
