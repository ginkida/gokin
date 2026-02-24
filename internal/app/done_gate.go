package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

const (
	doneGateOutputLimit         = 1600
	doneGateDefaultCheckTimeout = 3 * time.Minute
	doneGateMaxScanDepth        = 4
	doneGateMaxModulesPerStack  = 8
)

type doneGateCheck struct {
	Name string
	Run  func(context.Context) (tools.ToolResult, error)
}

type doneGateResult struct {
	Name    string
	Success bool
	Content string
	Error   string
}

type doneGatePolicy struct {
	Enabled         bool
	Mode            string
	FailClosed      bool
	CheckTimeout    time.Duration
	AutoFixAttempts int
}

type doneGateNodeProject struct {
	Dir     string
	Runner  string
	Scripts map[string]string
}

type doneGateProfile struct {
	GoModules         []string
	RustModules       []string
	NodeProjects      []doneGateNodeProject
	PythonRoots       []string
	PythonTestsLikely bool
}

func (a *App) enforceDoneGate(ctx context.Context, userMessage string) bool {
	policy := a.doneGatePolicy()
	if !policy.Enabled {
		return true
	}

	toolsUsed := a.snapshotResponseToolsUsed()
	if !shouldEnforceDoneGate(userMessage, toolsUsed) {
		return true
	}

	profile := detectDoneGateProfile(a.workDir)
	checks := a.buildDoneGateChecks(userMessage, toolsUsed, profile, policy)
	if len(checks) == 0 {
		if policy.FailClosed {
			return a.blockDoneGate("done-gate blocked finalization: no verification checks are available for this project/stack")
		}
		return true
	}

	var results []doneGateResult
	for attempt := 0; attempt <= policy.AutoFixAttempts; attempt++ {
		results = a.runDoneGateChecks(ctx, checks, policy.CheckTimeout)
		a.reportDoneGateResults(results, attempt)

		if doneGatePassed(results) {
			a.journalEvent("done_gate_passed", map[string]any{
				"attempt": attempt,
				"checks":  len(results),
			})
			return true
		}

		if attempt >= policy.AutoFixAttempts {
			break
		}

		if err := a.runDoneGateAutoFix(ctx, userMessage, results, attempt+1, policy.AutoFixAttempts); err != nil {
			logging.Warn("done-gate auto-fix failed", "attempt", attempt+1, "error", err)
			break
		}
	}

	return a.blockDoneGate("done-gate blocked finalization: required checks are still failing after auto-fix budget")
}

func (a *App) blockDoneGate(reason string) bool {
	a.journalEvent("done_gate_blocked", map[string]any{
		"reason": reason,
	})
	err := errors.New(reason)
	a.safeSendToProgram(ui.StreamTextMsg("\n" + err.Error() + "\n"))
	a.safeSendToProgram(ui.ErrorMsg(err))
	return false
}

func (a *App) snapshotResponseToolsUsed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := make([]string, len(a.responseToolsUsed))
	copy(snapshot, a.responseToolsUsed)
	return snapshot
}

func (a *App) doneGatePolicy() doneGatePolicy {
	p := doneGatePolicy{
		Enabled:         true,
		Mode:            "strict",
		FailClosed:      true,
		CheckTimeout:    doneGateDefaultCheckTimeout,
		AutoFixAttempts: 2,
	}
	if a.config == nil {
		return p
	}

	cfg := a.config.DoneGate
	p.Enabled = cfg.Enabled
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "normal", "strict":
		p.Mode = mode
	case "":
		// Keep strict default.
	default:
		logging.Warn("invalid done-gate mode; using strict", "mode", cfg.Mode)
	}
	p.FailClosed = cfg.FailClosed
	if cfg.CheckTimeout > 0 {
		p.CheckTimeout = cfg.CheckTimeout
	}
	if cfg.AutoFixAttempts >= 0 {
		p.AutoFixAttempts = cfg.AutoFixAttempts
	}
	return p
}

func (a *App) buildDoneGateChecks(userMessage string, toolsUsed []string, profile doneGateProfile, policy doneGatePolicy) []doneGateCheck {
	var checks []doneGateCheck
	seen := make(map[string]bool)

	if verifyTool, ok := a.registry.Get("verify_code"); ok {
		checks = append(checks, doneGateCheck{
			Name: "verify_code",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return verifyTool.Execute(ctx, map[string]any{"path": a.workDir})
			},
		})
		seen["verify_code"] = true
	}

	// Stack-specific checks.
	if bashTool, ok := a.registry.Get("bash"); ok {
		for _, moduleDir := range profile.GoModules {
			name := "go_vet@" + relPathOrDot(a.workDir, moduleDir)
			command := "go vet ./..."
			checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
				bashTool,
				name,
				moduleDir,
				command,
			))
		}
		for _, moduleDir := range profile.RustModules {
			name := "cargo_check@" + relPathOrDot(a.workDir, moduleDir)
			command := "cargo check --all-targets"
			checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
				bashTool,
				name,
				moduleDir,
				command,
			))
		}
		for _, project := range profile.NodeProjects {
			if scriptName, _, ok := pickNodeScript(project.Scripts, "lint"); ok {
				name := "node_lint@" + relPathOrDot(a.workDir, project.Dir)
				command := nodeRunScriptCommand(project.Runner, scriptName)
				checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
					bashTool,
					name,
					project.Dir,
					command,
				))
			}
			if scriptName, _, ok := pickNodeScript(project.Scripts, "typecheck"); ok {
				name := "node_typecheck@" + relPathOrDot(a.workDir, project.Dir)
				command := nodeRunScriptCommand(project.Runner, scriptName)
				checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
					bashTool,
					name,
					project.Dir,
					command,
				))
			}
			if scriptName, scriptBody, ok := pickNodeScript(project.Scripts, "test"); ok && !isNoopNodeTestScript(scriptBody) {
				name := "node_test@" + relPathOrDot(a.workDir, project.Dir)
				command := nodeRunScriptCommand(project.Runner, scriptName)
				checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
					bashTool,
					name,
					project.Dir,
					command,
				))
			}

			if policy.Mode == "strict" {
				if scriptName, _, ok := pickNodeScript(project.Scripts, "build"); ok {
					name := "node_build@" + relPathOrDot(a.workDir, project.Dir)
					command := nodeRunScriptCommand(project.Runner, scriptName)
					checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
						bashTool,
						name,
						project.Dir,
						command,
					))
				}
			}
		}

		for _, pythonRoot := range profile.PythonRoots {
			name := "python_compile@" + relPathOrDot(a.workDir, pythonRoot)
			command := "python3 -m compileall -q ."
			checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheckWithDir(
				bashTool,
				name,
				pythonRoot,
				command,
			))
		}

		// Generic repository health checks (stack-agnostic).
		checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheck(
			bashTool,
			"git_diff_check",
			"if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then git diff --check; else true; fi",
		))
		if policy.Mode == "strict" {
			checks = appendUniqueDoneGateCheck(checks, seen, a.newBashDoneGateCheck(
				bashTool,
				"git_unmerged_paths",
				"if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then if [ -n \"$(git ls-files -u)\" ]; then echo 'unmerged paths detected'; git ls-files -u; exit 1; fi; fi",
			))
		}
	}

	testArgs, canRunToolTests := doneGateRunTestsArgs(profile)
	if canRunToolTests && shouldRunDoneGateToolTests(userMessage, toolsUsed, profile) {
		if testsTool, ok := a.registry.Get("run_tests"); ok {
			checks = append(checks, doneGateCheck{
				Name: "run_tests",
				Run: func(ctx context.Context) (tools.ToolResult, error) {
					return testsTool.Execute(ctx, copyDoneGateToolArgs(testArgs))
				},
			})
		}
	}

	return checks
}

func appendUniqueDoneGateCheck(checks []doneGateCheck, seen map[string]bool, check doneGateCheck) []doneGateCheck {
	if check.Name == "" {
		return checks
	}
	if seen[check.Name] {
		return checks
	}
	seen[check.Name] = true
	return append(checks, check)
}

func copyDoneGateToolArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

func (a *App) newBashDoneGateCheck(bashTool tools.Tool, name, command string) doneGateCheck {
	return doneGateCheck{
		Name: name,
		Run: func(ctx context.Context) (tools.ToolResult, error) {
			return bashTool.Execute(ctx, map[string]any{
				"command":     command,
				"description": "done-gate check: " + name,
			})
		},
	}
}

func (a *App) newBashDoneGateCheckWithDir(bashTool tools.Tool, name, dir, command string) doneGateCheck {
	if strings.TrimSpace(dir) == "" || dir == "." {
		return a.newBashDoneGateCheck(bashTool, name, command)
	}

	wrapped := "cd " + shellQuote(dir) + " && " + command
	return a.newBashDoneGateCheck(bashTool, name, wrapped)
}

func (a *App) runDoneGateChecks(ctx context.Context, checks []doneGateCheck, checkTimeout time.Duration) []doneGateResult {
	results := make([]doneGateResult, 0, len(checks))
	for _, check := range checks {
		checkCtx := ctx
		cancel := func() {}
		if checkTimeout > 0 {
			checkCtx, cancel = context.WithTimeout(ctx, checkTimeout)
		}
		result, err := check.Run(checkCtx)
		timedOut := errors.Is(checkCtx.Err(), context.DeadlineExceeded)
		cancel()
		if err != nil {
			errMsg := strings.TrimSpace(err.Error())
			if timedOut {
				errMsg = fmt.Sprintf("check timed out after %v", checkTimeout)
			}
			results = append(results, doneGateResult{
				Name:    check.Name,
				Success: false,
				Error:   errMsg,
			})
			continue
		}
		content := strings.TrimSpace(result.Content)
		errContent := strings.TrimSpace(result.Error)
		if timedOut {
			results = append(results, doneGateResult{
				Name:    check.Name,
				Success: false,
				Content: content,
				Error:   fmt.Sprintf("check timed out after %v", checkTimeout),
			})
			continue
		}
		results = append(results, doneGateResult{
			Name:    check.Name,
			Success: result.Success,
			Content: content,
			Error:   errContent,
		})
	}
	return results
}

func (a *App) reportDoneGateResults(results []doneGateResult, attempt int) {
	var sb strings.Builder
	if attempt == 0 {
		sb.WriteString("\nDone-gate checks:\n")
	} else {
		sb.WriteString(fmt.Sprintf("\nDone-gate recheck after auto-fix #%d:\n", attempt))
	}

	for _, r := range results {
		status := "PASS"
		if !r.Success {
			status = "FAIL"
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", r.Name, status))
		if !r.Success {
			if detail := compactDoneGateFailureDetail(r); detail != "" {
				sb.WriteString("  -> ")
				sb.WriteString(detail)
				sb.WriteString("\n")
			}
		}
	}

	a.safeSendToProgram(ui.StreamTextMsg(sb.String()))
}

func (a *App) runDoneGateAutoFix(ctx context.Context, userMessage string, results []doneGateResult, attempt, max int) error {
	var failed []doneGateResult
	for _, r := range results {
		if !r.Success {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		return nil
	}

	fixPrompt := buildDoneGateFixPrompt(userMessage, failed, attempt, max)
	history := a.session.GetHistory()

	newHistory, _, err := a.executor.Execute(ctx, history, fixPrompt)
	if err != nil {
		return err
	}

	a.session.SetHistory(newHistory)
	a.applyToolOutputHygiene()
	if a.sessionManager != nil {
		_ = a.sessionManager.SaveAfterMessage()
	}
	return nil
}

func doneGatePassed(results []doneGateResult) bool {
	for _, r := range results {
		if !r.Success {
			return false
		}
	}
	return true
}

func shouldEnforceDoneGate(userMessage string, toolsUsed []string) bool {
	if len(toolsUsed) == 0 {
		return false
	}

	sideEffecting := map[string]bool{
		"write": true, "edit": true, "move": true, "copy": true, "delete": true,
		"mkdir": true, "refactor": true, "batch": true,
	}

	for _, name := range toolsUsed {
		if sideEffecting[name] {
			return true
		}
		if name == "bash" && looksLikeCodingTask(userMessage) {
			return true
		}
	}
	return false
}

func shouldRunDoneGateToolTests(userMessage string, toolsUsed []string, profile doneGateProfile) bool {
	lower := strings.ToLower(userMessage)
	if strings.Contains(lower, "без тест") || strings.Contains(lower, "no tests") {
		return false
	}

	_, canRunToolTests := doneGateRunTestsArgs(profile)

	for _, name := range toolsUsed {
		if name == "run_tests" && canRunToolTests {
			return true
		}
	}

	if len(profile.GoModules) > 0 || len(profile.RustModules) > 0 {
		return true
	}
	if len(profile.PythonRoots) > 0 && profile.PythonTestsLikely {
		return true
	}

	triggers := []string{
		"run tests", "go test", "pytest", "npm test", "cargo test",
		"запусти тест", "прогони тест", "прогон тест", "падение тест",
	}
	for _, t := range triggers {
		if strings.Contains(lower, t) && canRunToolTests {
			return true
		}
	}
	return false
}

func detectDoneGateProfile(workDir string) doneGateProfile {
	markers := discoverDoneGateMarkers(workDir)

	profile := doneGateProfile{
		GoModules:   pruneNestedDirs(limitAndSortDirs(markers.GoModules, doneGateMaxModulesPerStack)),
		RustModules: pruneNestedDirs(limitAndSortDirs(markers.RustModules, doneGateMaxModulesPerStack)),
		PythonRoots: pruneNestedDirs(limitAndSortDirs(markers.PythonRoots, doneGateMaxModulesPerStack)),
	}
	nodeDirs := limitAndSortDirs(markers.NodeProjects, doneGateMaxModulesPerStack)
	profile.NodeProjects = loadNodeProjects(workDir, nodeDirs)
	profile.PythonTestsLikely = detectPythonTestsLikely(profile.PythonRoots)
	return profile
}

type doneGateMarkers struct {
	GoModules    map[string]struct{}
	RustModules  map[string]struct{}
	NodeProjects map[string]struct{}
	PythonRoots  map[string]struct{}
}

func discoverDoneGateMarkers(workDir string) doneGateMarkers {
	markers := doneGateMarkers{
		GoModules:    make(map[string]struct{}),
		RustModules:  make(map[string]struct{}),
		NodeProjects: make(map[string]struct{}),
		PythonRoots:  make(map[string]struct{}),
	}

	_ = filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			if path == workDir {
				return nil
			}
			if shouldSkipDoneGateDir(d.Name()) {
				return filepath.SkipDir
			}
			if pathDepth(workDir, path) > doneGateMaxScanDepth {
				return filepath.SkipDir
			}
			return nil
		}

		if pathDepth(workDir, path) > doneGateMaxScanDepth+1 {
			return nil
		}

		dir := filepath.Dir(path)
		switch d.Name() {
		case "go.mod":
			markers.GoModules[dir] = struct{}{}
		case "Cargo.toml":
			markers.RustModules[dir] = struct{}{}
		case "package.json":
			markers.NodeProjects[dir] = struct{}{}
		case "pyproject.toml", "requirements.txt", "setup.py":
			markers.PythonRoots[dir] = struct{}{}
		}

		return nil
	})

	return markers
}

func shouldSkipDoneGateDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".next", ".turbo",
		".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache", ".idea", ".vscode":
		return true
	default:
		return false
	}
}

func pathDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func limitAndSortDirs(set map[string]struct{}, limit int) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for dir := range set {
		out = append(out, filepath.Clean(dir))
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func pruneNestedDirs(dirs []string) []string {
	if len(dirs) <= 1 {
		return dirs
	}

	ordered := make([]string, len(dirs))
	copy(ordered, dirs)
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) == len(ordered[j]) {
			return ordered[i] < ordered[j]
		}
		return len(ordered[i]) < len(ordered[j])
	})

	var out []string
	for _, dir := range ordered {
		skip := false
		for _, existing := range out {
			if dir == existing || strings.HasPrefix(dir, existing+string(filepath.Separator)) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out
}

func loadNodeProjects(workDir string, dirs []string) []doneGateNodeProject {
	if len(dirs) == 0 {
		return nil
	}
	projects := make([]doneGateNodeProject, 0, len(dirs))
	for _, dir := range dirs {
		scripts := readNodeScripts(filepath.Join(dir, "package.json"))
		projects = append(projects, doneGateNodeProject{
			Dir:     dir,
			Runner:  detectNodeRunner(workDir, dir),
			Scripts: scripts,
		})
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Dir < projects[j].Dir
	})
	return projects
}

func readNodeScripts(packageJSONPath string) map[string]string {
	scripts := make(map[string]string)
	data, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return scripts
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return scripts
	}

	for name, script := range pkg.Scripts {
		name = strings.TrimSpace(strings.ToLower(name))
		script = strings.TrimSpace(script)
		if name == "" || script == "" {
			continue
		}
		scripts[name] = script
	}
	return scripts
}

func detectNodeRunner(workDir, startDir string) string {
	for dir := startDir; ; dir = filepath.Dir(dir) {
		if fileExists(filepath.Join(dir, "pnpm-lock.yaml")) {
			return "pnpm"
		}
		if fileExists(filepath.Join(dir, "yarn.lock")) {
			return "yarn"
		}
		if fileExists(filepath.Join(dir, "bun.lockb")) || fileExists(filepath.Join(dir, "bun.lock")) {
			return "bun"
		}
		if fileExists(filepath.Join(dir, "package-lock.json")) || fileExists(filepath.Join(dir, "npm-shrinkwrap.json")) {
			return "npm"
		}
		if dir == workDir {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "npm"
}

func pickNodeScript(scripts map[string]string, category string) (string, string, bool) {
	if len(scripts) == 0 {
		return "", "", false
	}

	aliases := nodeScriptAliases(category)
	for _, alias := range aliases {
		if script, ok := scripts[alias]; ok {
			script = strings.TrimSpace(script)
			if script == "" {
				continue
			}
			return alias, script, true
		}
	}
	return "", "", false
}

func nodeScriptAliases(category string) []string {
	switch category {
	case "lint":
		return []string{"lint", "lint:ci", "check:lint", "eslint", "check-lint"}
	case "typecheck":
		return []string{"typecheck", "check-types", "types", "tsc", "check:type", "type-check"}
	case "test":
		return []string{"test", "test:ci", "ci:test", "unit", "test:unit"}
	case "build":
		return []string{"build", "build:ci", "check", "compile"}
	default:
		return []string{category}
	}
}

func nodeRunScriptCommand(runner, script string) string {
	switch strings.ToLower(strings.TrimSpace(runner)) {
	case "pnpm":
		return "pnpm -s run " + shellQuote(script)
	case "yarn":
		return "yarn -s run " + shellQuote(script)
	case "bun":
		return "bun run " + shellQuote(script)
	default:
		return "npm run -s " + shellQuote(script)
	}
}

func isNoopNodeTestScript(script string) bool {
	s := strings.ToLower(strings.TrimSpace(script))
	if s == "" {
		return true
	}
	noop := []string{
		"no test specified",
		"echo \"error: no test specified\"",
		"echo 'error: no test specified'",
		"echo \"no tests\"",
		"echo 'no tests'",
		"echo no tests",
		"exit 1",
	}
	for _, marker := range noop {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func detectPythonTestsLikely(pythonRoots []string) bool {
	for _, root := range pythonRoots {
		if fileExists(filepath.Join(root, "pytest.ini")) ||
			fileExists(filepath.Join(root, "conftest.py")) ||
			fileExists(filepath.Join(root, "tox.ini")) ||
			fileExists(filepath.Join(root, "noxfile.py")) ||
			dirExists(filepath.Join(root, "tests")) ||
			dirExists(filepath.Join(root, "test")) ||
			strings.Contains(readFileHead(filepath.Join(root, "pyproject.toml"), 6000), "[tool.pytest") {
			return true
		}
	}
	return false
}

func doneGateRunTestsArgs(profile doneGateProfile) (map[string]any, bool) {
	if len(profile.GoModules) > 0 {
		return map[string]any{
			"path":      profile.GoModules[0],
			"framework": "go",
		}, true
	}
	if len(profile.RustModules) > 0 {
		return map[string]any{
			"path":      profile.RustModules[0],
			"framework": "cargo",
		}, true
	}
	if len(profile.PythonRoots) > 0 && profile.PythonTestsLikely {
		return map[string]any{
			"path":      profile.PythonRoots[0],
			"framework": "pytest",
		}, true
	}
	return nil, false
}

func relPathOrDot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return "."
	}
	return rel
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func looksLikeCodingTask(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	keywords := []string{
		"implement", "fix", "refactor", "update", "change", "bug", "build", "lint", "compile",
		"доработ", "исправ", "рефактор", "обнов", "помен", "ошиб", "сборк", "линт", "код",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func buildDoneGateFixPrompt(userMessage string, failed []doneGateResult, attempt, max int) string {
	var sb strings.Builder
	sb.WriteString("The hard done-gate failed. Autonomously fix the code until checks pass.\n")
	sb.WriteString(fmt.Sprintf("Auto-fix attempt %d/%d.\n\n", attempt, max))
	sb.WriteString("Original user task:\n")
	sb.WriteString(userMessage)
	sb.WriteString("\n\nFailed checks:\n")
	for _, r := range failed {
		sb.WriteString(fmt.Sprintf("- %s failed.\n", r.Name))
		if r.Error != "" {
			sb.WriteString("  Error: ")
			sb.WriteString(truncateDoneGateText(r.Error))
			sb.WriteString("\n")
		}
		if r.Content != "" {
			sb.WriteString("  Output: ")
			sb.WriteString(truncateDoneGateText(r.Content))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nRules:\n")
	sb.WriteString("- Apply minimal deterministic fixes.\n")
	sb.WriteString("- Do not ask the user for input.\n")
	sb.WriteString("- After fixing, stop with a short summary of changes.\n")
	return sb.String()
}

func truncateDoneGateText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= doneGateOutputLimit {
		return s
	}
	return s[:doneGateOutputLimit] + "..."
}

func compactDoneGateFailureDetail(r doneGateResult) string {
	raw := strings.TrimSpace(r.Error)
	if raw == "" {
		raw = strings.TrimSpace(r.Content)
	}
	if raw == "" {
		return ""
	}

	raw = strings.Join(strings.Fields(raw), " ")
	if raw == "" {
		return ""
	}
	if len(raw) > 280 {
		return raw[:280] + "..."
	}
	return raw
}
