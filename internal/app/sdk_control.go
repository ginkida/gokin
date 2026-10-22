package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/commands"
	"gokin/internal/pluginpolicy"
	"gokin/internal/security"
	"gokin/internal/tools"
)

type sdkRegisteredRepository struct {
	directory                     string
	instructions, skills, plugins bool
}

func (a *App) ensureSDKRootBaseline() {
	a.sdkRootBaselineOnce.Do(func() {
		a.mu.Lock()
		directories := []string{a.workDir}
		if a.config != nil {
			directories = append(directories, a.config.Tools.AllowedDirs...)
		}
		a.mu.Unlock()
		for _, directory := range directories {
			if resolved, err := resolveGrantTarget(directory); err == nil {
				a.sdkRootBaseline = append(a.sdkRootBaseline, resolved)
			}
		}
	})
}

// HandleSDKControl is shared by the CLI connection and embedded SDK hosts.
// Controls operate on the live conversation; request correlation and interrupt
// ownership belong to the connection, not to an individual model request.
func (a *App) HandleSDKControl(ctx context.Context, data json.RawMessage) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("control request exceeds 16 MiB")
	}
	if err := pluginpolicy.ValidateJSON(data); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("control request must be an object")
	}
	var subtype string
	if err := json.Unmarshal(fields["subtype"], &subtype); err != nil {
		return nil, fmt.Errorf("control request requires subtype")
	}
	switch subtype {
	case "initialize":
		for _, field := range []string{"hooks", "sdkMcpServers", "sdkMcpServerConfigs", "sdkMcpServerManifests", "agents"} {
			var value any
			if len(fields[field]) > 0 {
				_ = json.Unmarshal(fields[field], &value)
			}
			empty := value == nil
			if values, ok := value.(map[string]any); ok {
				empty = len(values) == 0
			}
			if values, ok := value.([]any); ok {
				empty = len(values) == 0
			}
			if !empty {
				return nil, fmt.Errorf("initialize.%s is not supported yet", field)
			}
		}
		result := a.sdkInitialization()
		if _, present := fields["hooks"]; present {
			result["hooks_applied"] = true
		}
		if _, present := fields["plugins"]; present {
			result["plugins_applied"] = false
		}
		return result, nil
	case "get_binary_version":
		a.mu.Lock()
		runtimeVersion := a.config.Version
		a.mu.Unlock()
		return map[string]any{"version": runtimeVersion}, nil
	case "register_repo_root":
		var request struct {
			Subtype      string `json:"subtype"`
			Directory    string `json:"directory"`
			Instructions bool   `json:"reload_claude_md"`
			Plugins      bool   `json:"reload_plugins"`
			Skills       bool   `json:"reload_skills"`
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			return nil, err
		}
		return a.registerSDKRepository(ctx, request.Directory, request.Instructions, request.Skills, request.Plugins)
	case "reload_skills":
		if skill := a.sdkSkillTool(); skill != nil {
			skill.ReloadCatalog()
		}
		return map[string]any{"skills": a.sdkSkillCommands()}, nil
	default:
		return nil, fmt.Errorf("unsupported control request subtype: %s", subtype)
	}
}

func (a *App) sdkSkillTool() *tools.SkillTool {
	if a.registry != nil {
		if registered, ok := a.registry.Get("skill"); ok {
			skill, _ := registered.(*tools.SkillTool)
			return skill
		}
	}
	return nil
}

func (a *App) sdkSkillCommands() []map[string]any {
	result := []map[string]any{}
	if skill := a.sdkSkillTool(); skill != nil {
		for _, entry := range skill.UserSkillSnapshot() {
			result = append(result, map[string]any{"name": entry.Name, "description": entry.Description, "argumentHint": entry.ArgumentHint})
		}
	}
	return result
}

func (a *App) sdkInitialization() map[string]any {
	cmds := a.sdkSkillCommands()
	if a.commandHandler != nil {
		for _, command := range a.commandHandler.ListCommands() {
			hint := ""
			if metadata, ok := command.(interface {
				GetMetadata() commands.CommandMetadata
			}); ok {
				hint = metadata.GetMetadata().ArgHint
			}
			cmds = append(cmds, map[string]any{"name": command.Name(), "description": command.Description(), "argumentHint": hint})
		}
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i]["name"].(string) < cmds[j]["name"].(string) })
	models := []map[string]any{}
	agents := []map[string]any{}
	if a.agentTypeRegistry != nil {
		for _, definition := range a.agentTypeRegistry.SnapshotAgentTypes() {
			agents = append(agents, map[string]any{"name": definition.Name, "description": definition.Description, "model": definition.Model})
		}
	}
	if main := a.GetMainClient(); main != nil {
		models = append(models, map[string]any{"value": main.GetModel(), "displayName": main.GetModel(), "description": "Active model"})
	}
	return map[string]any{"commands": cmds, "agents": agents, "models": models, "account": map[string]any{}, "output_style": "default", "available_output_styles": []string{"default"}}
}

func (a *App) registerSDKRepository(ctx context.Context, directory string, instructions, skillReload, pluginReload bool) (map[string]any, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("directory is required")
	}
	a.ensureSDKRootBaseline()
	if a.session == nil || a.hooksManager == nil {
		return nil, fmt.Errorf("repository registration requires an active session")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(a.workDir, directory)
	}
	resolved, err := resolveGrantTarget(directory)
	if err != nil {
		return nil, err
	}
	if err := security.IsGrantableDir(resolved); err != nil {
		return nil, err
	}
	if err := a.lockSDKSessionLease(ctx); err != nil {
		return nil, err
	}
	defer a.sessionLeaseMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	owner := a.ensureSessionStartHooksLocked()
	if !a.fileHookOwnerCurrentLocked(owner) {
		return nil, fmt.Errorf("repository registration belongs to a replaced conversation")
	}
	if lineage, ok := conversationLineageFromContext(ctx); ok && (lineage.sessionID != owner.id || lineage.epoch != a.recoveryEpoch.Load()) {
		return nil, fmt.Errorf("repository registration belongs to a stale turn")
	}
	allowed := false
	for _, parent := range a.sdkRootBaseline {
		if sameShellDirectory(parent, resolved) {
			return nil, fmt.Errorf("already a registered working directory: %s", resolved)
		}
		within, err := security.IsPathWithinAny(resolved, []string{parent})
		allowed = allowed || err == nil && within
	}
	if !allowed {
		return nil, fmt.Errorf("directory must be a strict subdirectory of cwd or a launch-time --add-dir root")
	}
	for _, registered := range owner.sdkRepositories {
		if sameShellDirectory(registered.directory, resolved) {
			return nil, fmt.Errorf("already a registered working directory: %s", resolved)
		}
	}
	if len(owner.sdkRepositories) >= 128 {
		return nil, fmt.Errorf("at most 128 repositories may be registered")
	}
	a.mu.Lock()
	bare := a.config.Bare
	a.mu.Unlock()
	if bare && (instructions || skillReload || pluginReload) {
		return nil, fmt.Errorf("repository discovery is unavailable in bare mode")
	}
	if instructions && a.promptBuilder == nil {
		return nil, fmt.Errorf("project instruction loader is unavailable")
	}
	if skillReload && a.sdkSkillTool() == nil {
		return nil, fmt.Errorf("skill loader is unavailable")
	}
	// Build the discovery lists before publishing the new root. Each component
	// keeps its ordinary containment, source trust and managed-policy checks.
	repositories := append(append([]sdkRegisteredRepository(nil), owner.sdkRepositories...), sdkRegisteredRepository{resolved, instructions, skillReload, pluginReload})
	var instructionRoots, skillRoots, pluginRoots []string
	for _, registered := range repositories {
		if registered.instructions {
			instructionRoots = append(instructionRoots, registered.directory)
		}
		if registered.skills {
			skillRoots = append(skillRoots, registered.directory)
		}
		if registered.plugins {
			pluginRoots = append(pluginRoots, registered.directory)
		}
	}
	if pluginReload {
		preview, err := a.hooksManager.PreviewClaudePluginInstalls()
		if err != nil {
			return nil, err
		}
		preview.ConfigureClaudeRepositoryDirs(pluginRoots)
		if err := preview.RefreshClaudeSettings(); err != nil {
			return nil, err
		}
	}
	a.directoryGrantMu.Lock()
	_, grantErr := a.grantAllowedDirLocked(resolved, false)
	a.directoryGrantMu.Unlock()
	if grantErr != nil {
		return nil, grantErr
	}
	owner.sdkRepositories = repositories
	warnings := []string{}
	if instructions || skillReload || pluginReload {
		a.sdkRootsApplied = true
	}
	if instructions && a.promptBuilder != nil {
		if err := a.promptBuilder.SetAdditionalInstructionRoots(instructionRoots); err != nil {
			warnings = append(warnings, err.Error())
		}
		a.refreshSystemInstruction()
	}
	if skillReload {
		if skill := a.sdkSkillTool(); skill != nil {
			warnings = append(warnings, skill.SetAdditionalProjectRoots(skillRoots)...)
		}
	}
	if pluginReload {
		a.hooksManager.ConfigureClaudeRepositoryDirs(pluginRoots)
		if _, err := a.reloadClaudePluginsLocked(ctx, true, false); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	a.startDirectoryAddedLocked(owner, resolved, "register_repo_root")
	return map[string]any{"directory": resolved, "warnings": warnings}, nil
}

func (a *App) lockSDKSessionLease(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.sessionLeaseMu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) resetSDKRepositoryComponentsLocked() {
	if !a.sdkRootsApplied {
		return
	}
	a.sdkRootsApplied = false
	if a.hooksManager != nil {
		a.hooksManager.ConfigureClaudeRepositoryDirs(nil)
	}
	if skill := a.sdkSkillTool(); skill != nil {
		skill.SetAdditionalProjectRoots(nil)
	}
	if a.promptBuilder != nil {
		_ = a.promptBuilder.SetAdditionalInstructionRoots(nil)
		a.refreshSystemInstruction()
	}
}
