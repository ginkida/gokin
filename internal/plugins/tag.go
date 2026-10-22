package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/cobra"
	"gokin/internal/fileutil"
)

type TagOptions struct {
	DryRun  bool
	Force   bool
	Push    bool
	Remote  string
	Message string
}

// TagResult retains the local result even if a subsequent push fails.
type TagResult struct {
	Command         string     `json:"command"`
	Outcome         string     `json:"outcome"`
	Message         string     `json:"message"`
	Name            string     `json:"name,omitempty"`
	Version         string     `json:"version,omitempty"`
	VersionFile     string     `json:"versionFile,omitempty"`
	MarketplaceFile string     `json:"marketplaceFile,omitempty"`
	Tag             string     `json:"tag,omitempty"`
	Commit          string     `json:"commit,omitempty"`
	Changed         bool       `json:"changed,omitempty"`
	DryRun          bool       `json:"dryRun,omitempty"`
	Pushed          bool       `json:"pushed,omitempty"`
	Commands        [][]string `json:"commands,omitempty"`
	Warnings        []string   `json:"warnings,omitempty"`
}

// releaseEntry walks only within the plugin's repository. An unrelated parent
// checkout must not supply the version for a nested repository's release.
func releaseEntry(root, repo, name string) (string, string, error) {
	for dir := root; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".claude-plugin", "marketplace.json")
		data, err := fileutil.ReadResolvedRegularFile(path, 4<<20)
		if err == nil {
			catalog, err := ParseCatalog(data)
			if err != nil {
				return "", "", fmt.Errorf("%s: %w", path, err)
			}
			for _, entry := range catalog.Plugins {
				if entry.Name == name {
					return path, entry.Version, nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		if samePath(dir, repo) || filepath.Dir(dir) == dir {
			return "", "", nil
		}
	}
}

func (m *Manager) Tag(ctx context.Context, path string, opts TagOptions) (result TagResult, err error) {
	result = TagResult{Command: "tag", Outcome: "failed", DryRun: opts.DryRun}
	defer func() {
		if err != nil {
			result.Message = err.Error()
		}
	}()
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.opts.WorkDir, path)
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return result, err
	}
	git := func(args ...string) ([]byte, error) {
		return runCommand(ctx, root, "git", gitArgs(args...)...)
	}
	output, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return result, fmt.Errorf("plugin tag requires a Git working tree: %w", err)
	}
	repo, err := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
	if err != nil {
		return result, err
	}
	manifestPath := filepath.Join(root, ".claude-plugin", "plugin.json")
	manifest, err := readObject(manifestPath)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(manifest["name"], &result.Name) != nil || !ValidName(result.Name) {
		return result, fmt.Errorf("plugin manifest requires a valid name")
	}
	if raw, found := manifest["version"]; found && json.Unmarshal(raw, &result.Version) != nil {
		return result, fmt.Errorf("plugin version must be a semantic version string")
	}
	result.VersionFile = manifestPath
	marketPath, marketVersion, err := releaseEntry(root, repo, result.Name)
	if err != nil {
		return result, err
	}
	result.MarketplaceFile = marketPath
	if result.Version == "" {
		result.Version, result.VersionFile = marketVersion, marketPath
	} else if marketVersion != "" && result.Version != marketVersion {
		return result, fmt.Errorf("plugin version %s differs from marketplace version %s in %s", result.Version, marketVersion, marketPath)
	}
	if _, err := semver.StrictNewVersion(result.Version); err != nil {
		return result, fmt.Errorf("plugin tag requires an exact semantic version in plugin.json or its marketplace entry")
	}
	report, err := m.validatePlugin(root)
	if err != nil {
		return result, fmt.Errorf("plugin validation failed: %w", err)
	}
	result.Warnings = report.Warnings
	result.Tag = result.Name + "--v" + result.Version
	if _, err := git("check-ref-format", "refs/tags/"+result.Tag); err != nil {
		return result, fmt.Errorf("invalid release tag: %w", err)
	}
	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}
	if strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\r\n \t:") {
		return result, fmt.Errorf("invalid Git remote name")
	}
	if _, err := git("check-ref-format", "refs/remotes/"+remote+"/release"); err != nil {
		return result, fmt.Errorf("invalid Git remote name")
	}
	if opts.Push {
		if _, err := git("remote", "get-url", "--", remote); err != nil {
			return result, fmt.Errorf("release remote %q is unavailable: %w", remote, err)
		}
	}
	output, err = git("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return result, fmt.Errorf("plugin tag requires a committed release: %w", err)
	}
	result.Commit = strings.TrimSpace(string(output))
	if !hexSHA.MatchString(result.Commit) {
		return result, fmt.Errorf("git returned an invalid release commit")
	}
	if !opts.Force {
		// Include the catalog that supplied/validated the release version, even
		// when it is outside the plugin subtree. Literal pathspecs handle names
		// containing Git wildcard characters without broadening the check.
		args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none", "--"}
		for _, target := range []string{root, marketPath} {
			if target == "" {
				continue
			}
			rel, err := filepath.Rel(repo, target)
			if err != nil {
				return result, err
			}
			if rel == "." {
				args = append(args, ":/")
			} else {
				args = append(args, ":(top,literal)"+filepath.ToSlash(rel))
			}
		}
		output, err = git(args...)
		if err != nil {
			return result, err
		}
		if len(output) > 0 {
			return result, fmt.Errorf("plugin or matching marketplace working tree is dirty; commit the release or use --force")
		}
		// Ignored manifests are absent from status, but cannot define a release
		// that consumers will obtain from the tagged commit.
		args = []string{"ls-files", "--error-unmatch", "--"}
		for _, target := range []string{manifestPath, marketPath} {
			if target == "" {
				continue
			}
			rel, err := filepath.Rel(repo, target)
			if err != nil {
				return result, err
			}
			args = append(args, ":(top,literal)"+filepath.ToSlash(rel))
		}
		if _, err := git(args...); err != nil {
			return result, fmt.Errorf("release manifests must be tracked in Git: %w", err)
		}
		output, err = git("tag", "--list", "--", result.Tag)
		if err != nil {
			return result, err
		}
		if len(output) > 0 {
			return result, fmt.Errorf("tag %s already exists; use --force to replace the local tag", result.Tag)
		}
	}
	message := opts.Message
	if message == "" {
		message = result.Name + " " + result.Version
	}
	message = strings.ReplaceAll(message, "%s", result.Version)
	if strings.ContainsRune(message, '\x00') {
		return result, fmt.Errorf("tag annotation cannot contain NUL")
	}
	tagArgs := []string{"tag", "--annotate", "--no-sign", "--cleanup=verbatim", "--message", message}
	if opts.Force {
		tagArgs = append(tagArgs, "--force")
	}
	tagArgs = append(tagArgs, "--", result.Tag, result.Commit)
	pushArgs := []string{"push", "--", remote, "refs/tags/" + result.Tag + ":refs/tags/" + result.Tag}
	result.Commands = [][]string{append([]string{"git", "-C", root}, tagArgs...), append([]string{"git", "-C", root}, pushArgs...)}
	if opts.DryRun {
		result.Outcome, result.Message = "ok", "Dry run; no tag created."
		return result, nil
	}
	if _, err := git(tagArgs...); err != nil {
		return result, fmt.Errorf("cannot create tag %s: %w", result.Tag, err)
	}
	result.Changed = true
	if opts.Push {
		if _, err := git(pushArgs...); err != nil {
			return result, fmt.Errorf("created tag %s locally; push to %s failed: %w", result.Tag, remote, err)
		}
		result.Pushed = true
	}
	result.Outcome, result.Message = "ok", "Created tag "+result.Tag
	return result, nil
}

func tagCommand(newManager func() (*Manager, error), asJSON *bool) *cobra.Command {
	var opts TagOptions
	command := &cobra.Command{Use: "tag [path]", Short: "Validate and tag a plugin release", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := newManager()
		if err != nil {
			return err
		}
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		value, err := manager.Tag(cmd.Context(), path, opts)
		if *asJSON {
			if writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(value); writeErr != nil {
				return writeErr
			}
		} else {
			if value.Tag != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Plugin: %s\nVersion: %s (%s)\nTag: %s\n", value.Name, value.Version, value.VersionFile, value.Tag)
			}
			if value.MarketplaceFile != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Marketplace:", value.MarketplaceFile)
			}
			fmt.Fprintln(cmd.OutOrStdout(), value.Message)
			for _, warning := range value.Warnings {
				fmt.Fprintln(cmd.OutOrStdout(), "Warning:", warning)
			}
			if value.Pushed {
				fmt.Fprintln(cmd.OutOrStdout(), "Pushed to", opts.Remote)
			} else if value.DryRun || value.Changed {
				for i, args := range value.Commands {
					if i == 0 && !value.DryRun {
						continue
					}
					quoted := make([]string, len(args))
					for j, arg := range args {
						quoted[j] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
					}
					fmt.Fprintln(cmd.OutOrStdout(), strings.Join(quoted, " "))
				}
			}
		}
		return err
	}}
	command.Flags().BoolVar(&opts.DryRun, "dry-run", false, "preview release checks and commands without creating a tag")
	command.Flags().BoolVarP(&opts.Force, "force", "f", false, "allow a dirty working tree and replace an existing local tag")
	command.Flags().BoolVar(&opts.Push, "push", false, "push the created tag to the selected remote")
	command.Flags().StringVar(&opts.Remote, "remote", "origin", "remote to push the release tag to")
	command.Flags().StringVarP(&opts.Message, "message", "m", "", "tag annotation; %s is replaced with the version")
	return command
}
