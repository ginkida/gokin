package app

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gokin/internal/hooks"
	"gokin/internal/lsp"
	"gokin/internal/mcp"
)

var errPluginReloadCache = errors.New("plugin reload would invalidate the prompt cache")
var errPluginReloadBusy = errors.New("plugin reload waits for the foreground turn")

// Called inside foreground admission and MCP mutation barriers. Preview uses
// a separate authority, so declining the cache cost does not adopt new roots,
// start servers, or revoke working wrappers from the active session.
func (a *App) pluginReloadCacheChanges(authority *hooks.Manager, manager *mcp.Manager, workDir string, allowDefault bool) ([]string, error) {
	a.mu.Lock()
	session, used := a.session, a.totalInputTokens > 0
	a.mu.Unlock()
	if !used && (session == nil || len(session.GetHistory()) == 0) {
		return nil, nil
	}
	preview, err := authority.PreviewClaudePluginInstalls()
	if err != nil {
		return nil, err
	}
	configs, err := mcp.LoadClaudePluginServers(preview, workDir, allowDefault)
	if err != nil {
		return nil, err
	}
	previous, next := map[string]bool{}, map[string]bool{}
	for _, name := range manager.ClaudePluginServerNames() {
		previous[name] = true
	}
	for _, cfg := range configs {
		next[cfg.Name] = true
	}
	var changes []string
	for name := range previous {
		if !next[name] {
			changes = append(changes, "MCP "+name)
		}
	}
	for name := range next {
		if !previous[name] {
			changes = append(changes, "MCP "+name)
		}
	}
	languages, err := lsp.LoadPluginConfigs(preview, workDir)
	if err != nil {
		return nil, err
	}
	currentLSP := false
	if a.registry != nil {
		_, currentLSP = a.registry.Get("lsp")
	}
	if currentLSP != (len(languages) > 0) {
		changes = append(changes, "LSP tool")
	}
	sort.Strings(changes)
	return changes, nil
}

func pluginReloadCacheMessage(changes []string) string {
	return fmt.Sprintf("This reload changes tools (%s) — your next message will re-read the conversation instead of using the prompt cache. Run /reload-plugins --force to apply.", strings.Join(changes, ", "))
}
