package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"google.golang.org/genai"

	"gokin/internal/logging"
	"gokin/internal/semantic"
)

const (
	impactGateMaxTargetsBase         = 200
	impactGateMaxImpactedFilesBase   = 240
	impactGateMaxImpactedModulesBase = 18
	impactGateMaxTargetsCap          = 1200
	impactGateMaxImpactedFilesCap    = 3200
	impactGateMaxImpactedModulesCap  = 96
)

var impactGatedTools = map[string]bool{
	"write":       true,
	"edit":        true,
	"delete":      true,
	"mkdir":       true,
	"copy":        true,
	"move":        true,
	"batch":       true,
	"refactor":    true,
	"atomicwrite": true,
}

type impactGateDecision struct {
	Allowed bool
	Reason  string
	Summary string
}

func (e *Executor) evaluateImpactGate(ctx context.Context, call *genai.FunctionCall) *impactGateDecision {
	if call == nil || !impactGatedTools[call.Name] {
		return nil
	}
	if call.Name == "refactor" {
		op, _ := GetString(call.Args, "operation")
		if strings.EqualFold(strings.TrimSpace(op), "find_refs") {
			return nil
		}
	}

	workDir := strings.TrimSpace(e.workDir)
	if workDir == "" {
		return &impactGateDecision{
			Allowed: false,
			Reason:  "impact gate blocked execution: executor workdir is not configured",
		}
	}

	targets, err := resolveImpactTargets(ctx, workDir, call)
	if err != nil {
		return &impactGateDecision{
			Allowed: false,
			Reason:  fmt.Sprintf("impact gate blocked execution: %s", err),
		}
	}
	if len(targets) == 0 {
		return &impactGateDecision{
			Allowed: false,
			Reason:  "impact gate blocked execution: no target files resolved for blast-radius analysis",
		}
	}

	graph, err := semantic.BuildDependencyGraph(workDir)
	if err != nil {
		return &impactGateDecision{
			Allowed: false,
			Reason:  fmt.Sprintf("impact gate blocked execution: dependency graph build failed: %v", err),
		}
	}

	nodes := graph.GetAllNodes()
	nodeSet := make(map[string]struct{}, len(nodes))
	fileNodeCount := 0
	for _, node := range nodes {
		nodeSet[node.ID] = struct{}{}
		if strings.HasPrefix(node.ID, "file:") {
			fileNodeCount++
		}
	}

	maxTargets, maxImpactedFiles, maxImpactedModules := impactGateLimits(fileNodeCount)
	if len(targets) > maxTargets {
		return &impactGateDecision{
			Allowed: false,
			Reason: fmt.Sprintf(
				"impact gate blocked execution: target set is too wide (%d files, max %d)",
				len(targets),
				maxTargets,
			),
		}
	}

	impacted := make(map[string]struct{})
	modules := make(map[string]struct{})

	for _, absPath := range targets {
		select {
		case <-ctx.Done():
			return &impactGateDecision{
				Allowed: false,
				Reason:  "impact gate blocked execution: analysis cancelled",
			}
		default:
		}

		rel, err := filepath.Rel(workDir, absPath)
		if err != nil {
			return &impactGateDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("impact gate blocked execution: failed to relativize %s", absPath),
			}
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		rel = strings.TrimPrefix(rel, "./")
		if rel == "." {
			continue
		}

		addImpactPath(rel, impacted, modules)
		nodeID := "file:" + rel
		if _, ok := nodeSet[nodeID]; !ok {
			// File not in dependency graph (e.g. new file, excluded directory).
			// Still count it in impacted set but skip dependency traversal.
			logging.Debug("impact gate: file not in dependency graph, skipping dependency analysis", "file", rel)
			continue
		}

		for _, dep := range graph.GetDependencies(nodeID) {
			if relDep, ok := fileNodeToRel(dep); ok {
				addImpactPath(relDep, impacted, modules)
			}
		}
		for _, dep := range graph.GetDependents(nodeID) {
			if relDep, ok := fileNodeToRel(dep); ok {
				addImpactPath(relDep, impacted, modules)
			}
		}
	}

	if len(impacted) > maxImpactedFiles {
		return &impactGateDecision{
			Allowed: false,
			Reason: fmt.Sprintf(
				"impact gate blocked execution: blast radius too large (%d impacted files, max %d)",
				len(impacted),
				maxImpactedFiles,
			),
			Summary: fmt.Sprintf(
				"impact analysis: targets=%d impacted=%d modules=%d repo_files=%d",
				len(targets),
				len(impacted),
				len(modules),
				fileNodeCount,
			),
		}
	}

	if len(modules) > maxImpactedModules {
		return &impactGateDecision{
			Allowed: false,
			Reason: fmt.Sprintf(
				"impact gate blocked execution: cross-module scope too broad (%d modules, max %d)",
				len(modules),
				maxImpactedModules,
			),
			Summary: fmt.Sprintf(
				"impact analysis: targets=%d impacted=%d modules=%d repo_files=%d",
				len(targets),
				len(impacted),
				len(modules),
				fileNodeCount,
			),
		}
	}

	decision := &impactGateDecision{
		Allowed: true,
		Summary: fmt.Sprintf(
			"impact gate passed: targets=%d impacted=%d modules=%d repo_files=%d",
			len(targets),
			len(impacted),
			len(modules),
			fileNodeCount,
		),
	}
	logging.Debug("impact gate passed", "tool", call.Name, "targets", len(targets), "impacted", len(impacted), "modules", len(modules))
	return decision
}

func impactGateLimits(fileNodeCount int) (maxTargets, maxImpactedFiles, maxImpactedModules int) {
	if fileNodeCount < 0 {
		fileNodeCount = 0
	}

	maxTargets = impactGateMaxTargetsBase
	maxImpactedFiles = impactGateMaxImpactedFilesBase
	maxImpactedModules = impactGateMaxImpactedModulesBase

	// Auto-scale gate thresholds for larger repositories to reduce false positives.
	if fileNodeCount > 0 {
		scaledTargets := int(float64(fileNodeCount)*0.15 + 0.5)
		scaledImpacted := int(float64(fileNodeCount)*0.35 + 0.5)
		scaledModules := int(float64(fileNodeCount)*0.03 + 0.5)

		if scaledTargets > maxTargets {
			maxTargets = scaledTargets
		}
		if scaledImpacted > maxImpactedFiles {
			maxImpactedFiles = scaledImpacted
		}
		if scaledModules > maxImpactedModules {
			maxImpactedModules = scaledModules
		}
	}

	maxTargets = clampInt(maxTargets, impactGateMaxTargetsBase, impactGateMaxTargetsCap)
	maxImpactedFiles = clampInt(maxImpactedFiles, impactGateMaxImpactedFilesBase, impactGateMaxImpactedFilesCap)
	maxImpactedModules = clampInt(maxImpactedModules, impactGateMaxImpactedModulesBase, impactGateMaxImpactedModulesCap)
	return maxTargets, maxImpactedFiles, maxImpactedModules
}

func resolveImpactTargets(ctx context.Context, workDir string, call *genai.FunctionCall) ([]string, error) {
	addResolved := func(out map[string]struct{}, raw string) error {
		path, err := resolveImpactPath(workDir, raw)
		if err != nil {
			return err
		}
		return expandImpactPath(ctx, path, out)
	}

	targets := make(map[string]struct{})
	args := call.Args

	switch call.Name {
	case "write", "edit", "atomicwrite":
		filePath, _ := GetString(args, "file_path")
		if filePath == "" {
			return nil, fmt.Errorf("missing file_path argument")
		}
		if err := addResolved(targets, filePath); err != nil {
			return nil, err
		}

	case "delete":
		path, _ := GetString(args, "path")
		if path == "" {
			return nil, fmt.Errorf("missing path argument")
		}
		if err := addResolved(targets, path); err != nil {
			return nil, err
		}

	case "mkdir":
		path, _ := GetString(args, "path")
		if path == "" {
			return nil, fmt.Errorf("missing path argument")
		}
		if err := addResolved(targets, path); err != nil {
			return nil, err
		}

	case "copy", "move":
		source, _ := GetString(args, "source")
		dest, _ := GetString(args, "destination")
		if source == "" || dest == "" {
			return nil, fmt.Errorf("missing source/destination argument")
		}
		if err := addResolved(targets, source); err != nil {
			return nil, err
		}
		// Destination can be new, include it as target file context.
		destPath, err := resolveImpactPath(workDir, dest)
		if err != nil {
			return nil, err
		}
		targets[destPath] = struct{}{}

	case "batch":
		for _, raw := range stringListFromAny(args["files"]) {
			if err := addResolved(targets, raw); err != nil {
				return nil, err
			}
		}
		pattern, _ := GetString(args, "pattern")
		if strings.TrimSpace(pattern) != "" {
			expanded, err := expandImpactPattern(workDir, pattern)
			if err != nil {
				return nil, err
			}
			for _, path := range expanded {
				if err := expandImpactPath(ctx, path, targets); err != nil {
					return nil, err
				}
			}
		}

	case "refactor":
		if filePath, _ := GetString(args, "file_path"); strings.TrimSpace(filePath) != "" {
			if err := addResolved(targets, filePath); err != nil {
				return nil, err
			}
		}
		if pattern, _ := GetString(args, "pattern"); strings.TrimSpace(pattern) != "" {
			expanded, err := expandImpactPattern(workDir, pattern)
			if err != nil {
				return nil, err
			}
			for _, path := range expanded {
				if err := expandImpactPath(ctx, path, targets); err != nil {
					return nil, err
				}
			}
		}
	}

	out := make([]string, 0, len(targets))
	for path := range targets {
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func resolveImpactPath(workDir, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty path in tool arguments")
	}
	if strings.ContainsAny(raw, "*?[]{}") {
		return "", fmt.Errorf("ambiguous wildcard path %q; provide concrete file paths", raw)
	}

	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path %q: %w", raw, err)
	}

	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	rel, err := filepath.Rel(workAbs, absPath)
	if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "", fmt.Errorf("path %q is outside workspace", raw)
	}
	return filepath.Clean(absPath), nil
}

func expandImpactPath(ctx context.Context, absPath string, out map[string]struct{}) error {
	info, err := os.Stat(absPath)
	if err != nil {
		// New file paths are still valid impact targets.
		if os.IsNotExist(err) {
			out[absPath] = struct{}{}
			return nil
		}
		return fmt.Errorf("failed to inspect target %q: %w", absPath, err)
	}

	if !info.IsDir() {
		out[absPath] = struct{}{}
		return nil
	}

	return filepath.Walk(absPath, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if fi.IsDir() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out[filepath.Clean(path)] = struct{}{}
		return nil
	})
}

func expandImpactPattern(workDir, pattern string) ([]string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern for impact analysis")
	}

	absolutePattern := pattern
	if !filepath.IsAbs(absolutePattern) {
		absolutePattern = filepath.Join(workDir, absolutePattern)
	}
	matches, err := doublestar.FilepathGlob(absolutePattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("pattern %q matched no files", pattern)
	}

	out := make([]string, 0, len(matches))
	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil || info.IsDir() {
			continue
		}
		absMatch, err := filepath.Abs(m)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(workAbs, absMatch)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("pattern %q resolved outside workspace: %s", pattern, m)
		}
		out = append(out, filepath.Clean(absMatch))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pattern %q matched no files", pattern)
	}
	return out, nil
}

func addImpactPath(rel string, impacted map[string]struct{}, modules map[string]struct{}) {
	rel = strings.TrimSpace(strings.TrimPrefix(filepath.ToSlash(rel), "./"))
	if rel == "" || rel == "." {
		return
	}
	impacted[rel] = struct{}{}
	root := "__root__"
	if idx := strings.IndexByte(rel, '/'); idx > 0 {
		root = rel[:idx]
	}
	modules[root] = struct{}{}
}

func fileNodeToRel(nodeID string) (string, bool) {
	if !strings.HasPrefix(nodeID, "file:") {
		return "", false
	}
	rel := strings.TrimSpace(strings.TrimPrefix(nodeID, "file:"))
	if rel == "" {
		return "", false
	}
	return rel, true
}


func stringListFromAny(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
