package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"

	"gokin/internal/logging"
)

const (
	deltaCheckDefaultTimeout   = 90 * time.Second
	deltaCheckDefaultMaxModule = 8
	deltaCheckOutputMaxChars   = 1200
)

var deltaCheckToolSet = map[string]bool{
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

type deltaCheckResult struct {
	Ran      bool
	Passed   bool
	Hash     string
	Summary  string
	Details  string
	Executed int
	Failed   int
	Skipped  int
}

type deltaCheckCommand struct {
	Key     string
	Dir     string
	Display string
	Command string
	Args    []string
}

type deltaModuleTarget struct {
	Kind   string
	Dir    string
	Script string
	Runner string
}

func shouldRunDeltaCheckCall(call *genai.FunctionCall) bool {
	if call == nil || !deltaCheckToolSet[call.Name] {
		return false
	}
	switch call.Name {
	case "batch":
		return !GetBoolDefault(call.Args, "dry_run", false)
	case "refactor":
		op, _ := GetString(call.Args, "operation")
		return !strings.EqualFold(strings.TrimSpace(op), "find_refs")
	default:
		return true
	}
}

func (e *Executor) ensureDeltaBaseline(ctx context.Context) {
	e.deltaCheckMu.Lock()
	enabled := e.deltaCheckEnabled
	workDir := strings.TrimSpace(e.workDir)
	captured := e.deltaBaselineCaptured
	e.deltaCheckMu.Unlock()

	if !enabled || workDir == "" || captured {
		return
	}

	paths, _, ok := deltaGitStatusPaths(ctx, workDir)

	e.deltaCheckMu.Lock()
	defer e.deltaCheckMu.Unlock()
	if e.deltaBaselineCaptured {
		return
	}
	e.deltaBaselineCaptured = true
	if e.deltaBaselinePaths == nil {
		e.deltaBaselinePaths = make(map[string]struct{})
	} else {
		for key := range e.deltaBaselinePaths {
			delete(e.deltaBaselinePaths, key)
		}
	}
	if ok {
		for path := range paths {
			e.deltaBaselinePaths[path] = struct{}{}
		}
	}
}

func (e *Executor) enforceDeltaCheckBarrier(ctx context.Context, call *genai.FunctionCall) (string, bool) {
	if !shouldRunDeltaCheckCall(call) {
		return "", false
	}

	e.ensureDeltaBaseline(ctx)

	e.deltaCheckMu.Lock()
	enabled := e.deltaCheckEnabled
	warnOnly := e.deltaCheckWarnOnly
	blocked := e.deltaCheckBlocked
	blockReason := e.deltaCheckBlockReason
	e.deltaCheckMu.Unlock()

	if !enabled || warnOnly || !blocked {
		return "", false
	}

	result := e.runDeltaCheck(ctx)
	if result.Passed || !result.Ran {
		return "", false
	}

	reason := strings.TrimSpace(blockReason)
	if reason == "" {
		reason = strings.TrimSpace(result.Summary)
	}
	if reason == "" {
		reason = "delta-check guard: previous changes still fail verification"
	} else if !strings.HasPrefix(strings.ToLower(reason), "delta-check guard:") {
		reason = "delta-check guard: " + reason
	}
	return reason, true
}

func (e *Executor) runDeltaCheckAfterMutation(ctx context.Context, call *genai.FunctionCall) *deltaCheckResult {
	if !shouldRunDeltaCheckCall(call) {
		return nil
	}

	e.ensureDeltaBaseline(ctx)
	e.addPendingDeltaTargets(ctx, call)
	result := e.runDeltaCheck(ctx)
	if !result.Ran {
		return nil
	}
	return &result
}

func (e *Executor) addPendingDeltaTargets(ctx context.Context, call *genai.FunctionCall) {
	if call == nil {
		return
	}
	workDir := strings.TrimSpace(e.workDir)
	if workDir == "" {
		return
	}

	pending := make(map[string]struct{})

	if targets, err := resolveImpactTargets(ctx, workDir, call); err == nil {
		for _, abs := range targets {
			if rel, ok := deltaAbsToRel(workDir, abs); ok {
				pending[rel] = struct{}{}
			}
		}
	} else {
		for _, raw := range extractFilePaths(call) {
			abs, pathErr := resolveImpactPath(workDir, raw)
			if pathErr != nil {
				continue
			}
			if rel, ok := deltaAbsToRel(workDir, abs); ok {
				pending[rel] = struct{}{}
			}
		}
	}

	if len(pending) == 0 {
		return
	}

	e.deltaCheckMu.Lock()
	if e.deltaPendingPaths == nil {
		e.deltaPendingPaths = make(map[string]struct{})
	}
	for rel := range pending {
		e.deltaPendingPaths[rel] = struct{}{}
	}
	e.deltaCheckMu.Unlock()
}

func (e *Executor) runDeltaCheck(ctx context.Context) deltaCheckResult {
	e.deltaCheckMu.Lock()
	enabled := e.deltaCheckEnabled
	warnOnly := e.deltaCheckWarnOnly
	workDir := strings.TrimSpace(e.workDir)
	timeout := e.deltaCheckTimeout
	maxModules := e.deltaCheckMaxModules
	lastHash := e.deltaCheckLastHash
	var cached deltaCheckResult
	hasCached := e.deltaCheckLastResult != nil
	if hasCached {
		cached = *e.deltaCheckLastResult
	}
	baselineCaptured := e.deltaBaselineCaptured
	baseline := copyStringSet(e.deltaBaselinePaths)
	pending := copyStringSet(e.deltaPendingPaths)
	e.deltaCheckMu.Unlock()

	if !enabled || workDir == "" {
		return deltaCheckResult{Ran: false, Passed: true}
	}
	if timeout <= 0 {
		timeout = deltaCheckDefaultTimeout
	}
	if maxModules <= 0 {
		maxModules = deltaCheckDefaultMaxModule
	}

	changed, hashSeed := collectDeltaChangedPaths(ctx, workDir, baselineCaptured, baseline, pending)
	commands := buildDeltaCheckCommands(workDir, changed, maxModules)

	if len(commands) == 0 {
		result := deltaCheckResult{
			Ran:     false,
			Passed:  true,
			Summary: "delta-check skipped: no supported modules in current change set",
		}
		e.commitDeltaCheckResult(result, warnOnly)
		return result
	}

	hash := hashDeltaCheckState(changed, commands, hashSeed)
	if hasCached && hash == lastHash {
		return cached
	}

	result := deltaCheckResult{
		Ran:    true,
		Passed: true,
		Hash:   hash,
	}

	failures := make([]string, 0)
	for _, check := range commands {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(runCtx, check.Command, check.Args...)
		cmd.Dir = check.Dir
		output, err := cmd.CombinedOutput()
		cancel()

		outputText := strings.TrimSpace(string(output))
		if err != nil {
			if isDeltaCheckCommandMissing(err) {
				result.Skipped++
				continue
			}
			result.Failed++
			result.Passed = false
			failures = append(failures, fmt.Sprintf(
				"%s in %s failed: %s\n%s",
				check.Display,
				check.Dir,
				trimDeltaOutput(err.Error(), 280),
				trimDeltaOutput(outputText, deltaCheckOutputMaxChars),
			))
			continue
		}
		result.Executed++
	}

	total := len(commands)
	if result.Passed {
		result.Summary = fmt.Sprintf(
			"delta-check passed: %d/%d checks succeeded%s",
			result.Executed,
			total,
			deltaSkipSuffix(result.Skipped),
		)
	} else {
		result.Summary = fmt.Sprintf(
			"delta-check failed: %d/%d checks failed%s",
			result.Failed,
			total,
			deltaSkipSuffix(result.Skipped),
		)
		result.Details = strings.Join(failures, "\n\n")
	}

	e.commitDeltaCheckResult(result, warnOnly)
	return result
}

func (e *Executor) commitDeltaCheckResult(result deltaCheckResult, warnOnly bool) {
	e.deltaCheckMu.Lock()
	defer e.deltaCheckMu.Unlock()

	copyResult := result
	e.deltaCheckLastHash = result.Hash
	e.deltaCheckLastResult = &copyResult

	if result.Passed || !result.Ran || warnOnly {
		e.deltaCheckBlocked = false
		e.deltaCheckBlockReason = ""
		// Successful/skip verification drains pending set.
		e.deltaPendingPaths = make(map[string]struct{})
		return
	}

	e.deltaCheckBlocked = true
	e.deltaCheckBlockReason = strings.TrimSpace(result.Summary)
	if detail := strings.TrimSpace(result.Details); detail != "" {
		e.deltaCheckBlockReason += "\n" + trimDeltaOutput(detail, 900)
	}
}

func collectDeltaChangedPaths(
	ctx context.Context,
	workDir string,
	baselineCaptured bool,
	baseline map[string]struct{},
	pending map[string]struct{},
) ([]string, string) {
	changedSet := make(map[string]struct{})
	for rel := range pending {
		if normalized := normalizeDeltaRelPath(rel); normalized != "" {
			changedSet[normalized] = struct{}{}
		}
	}

	hashSeed := ""
	if statusPaths, statusText, ok := deltaGitStatusPaths(ctx, workDir); ok {
		if baselineCaptured {
			for rel := range statusPaths {
				if _, exists := baseline[rel]; exists {
					continue
				}
				changedSet[rel] = struct{}{}
			}
		} else {
			for rel := range statusPaths {
				changedSet[rel] = struct{}{}
			}
		}
		hashSeed = statusText
	}

	changed := make([]string, 0, len(changedSet))
	for rel := range changedSet {
		changed = append(changed, rel)
	}
	sort.Strings(changed)

	if hashSeed == "" {
		hashSeed = deltaLocalSignature(workDir, changed)
	}
	return changed, hashSeed
}

func deltaGitStatusPaths(ctx context.Context, workDir string) (map[string]struct{}, string, bool) {
	paths := make(map[string]struct{})
	if strings.TrimSpace(workDir) == "" {
		return paths, "", false
	}

	if out, err := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--is-inside-work-tree").CombinedOutput(); err != nil ||
		!strings.EqualFold(strings.TrimSpace(string(out)), "true") {
		return paths, "", false
	}

	out, err := exec.CommandContext(ctx, "git", "-C", workDir, "status", "--porcelain", "--untracked-files=all").CombinedOutput()
	if err != nil {
		return paths, "", false
	}

	text := strings.TrimSpace(string(out))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 3 {
			line = strings.TrimSpace(line[3:])
		}
		if strings.Contains(line, " -> ") {
			parts := strings.Split(line, " -> ")
			line = parts[len(parts)-1]
		}
		line = strings.Trim(line, `"`)
		if rel := normalizeDeltaRelPath(line); rel != "" {
			paths[rel] = struct{}{}
		}
	}

	return paths, text, true
}

func buildDeltaCheckCommands(workDir string, changed []string, maxModules int) []deltaCheckCommand {
	if len(changed) == 0 {
		return nil
	}
	if maxModules <= 0 {
		maxModules = deltaCheckDefaultMaxModule
	}

	targets := make(map[string]deltaModuleTarget)
	for _, rel := range changed {
		target, ok := detectDeltaModuleTarget(workDir, rel)
		if !ok {
			continue
		}
		key := target.Kind + "|" + target.Dir
		if target.Kind == "node" {
			key += "|" + target.Runner + "|" + target.Script
		}
		if _, exists := targets[key]; !exists {
			targets[key] = target
		}
	}

	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > maxModules {
		keys = keys[:maxModules]
	}

	commands := make([]deltaCheckCommand, 0, len(keys))
	for _, key := range keys {
		target := targets[key]
		switch target.Kind {
		case "go":
			commands = append(commands, deltaCheckCommand{
				Key:     key,
				Dir:     target.Dir,
				Display: "go build ./...",
				Command: "go",
				Args:    []string{"build", "./..."},
			})
		case "rust":
			commands = append(commands, deltaCheckCommand{
				Key:     key,
				Dir:     target.Dir,
				Display: "cargo check --all-targets",
				Command: "cargo",
				Args:    []string{"check", "--all-targets"},
			})
		case "python":
			commands = append(commands, deltaCheckCommand{
				Key:     key,
				Dir:     target.Dir,
				Display: "python3 -m compileall -q .",
				Command: "python3",
				Args:    []string{"-m", "compileall", "-q", "."},
			})
		case "java_maven":
			commands = append(commands, deltaCheckCommand{
				Key:     key,
				Dir:     target.Dir,
				Display: "mvn -q -DskipTests verify",
				Command: "mvn",
				Args:    []string{"-q", "-DskipTests", "verify"},
			})
		case "java_gradle":
			gradleCmd := "gradle"
			if fileExists(filepath.Join(target.Dir, "gradlew")) {
				gradleCmd = "./gradlew"
			}
			commands = append(commands, deltaCheckCommand{
				Key:     key,
				Dir:     target.Dir,
				Display: gradleCmd + " -q build -x test",
				Command: gradleCmd,
				Args:    []string{"-q", "build", "-x", "test"},
			})
		case "node":
			command, args := nodeDeltaRunnerCommand(target.Runner, target.Script)
			commands = append(commands, deltaCheckCommand{
				Key:     key,
				Dir:     target.Dir,
				Display: command + " " + strings.Join(args, " "),
				Command: command,
				Args:    args,
			})
		}
	}

	return commands
}

func detectDeltaModuleTarget(workDir, relPath string) (deltaModuleTarget, bool) {
	relPath = normalizeDeltaRelPath(relPath)
	if relPath == "" {
		return deltaModuleTarget{}, false
	}

	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return deltaModuleTarget{}, false
	}

	absPath := filepath.Join(workAbs, filepath.FromSlash(relPath))
	startDir := absPath
	if info, statErr := os.Stat(absPath); statErr != nil || !info.IsDir() {
		startDir = filepath.Dir(absPath)
	}

	for dir := startDir; ; dir = filepath.Dir(dir) {
		if relToRoot, relErr := filepath.Rel(workAbs, dir); relErr != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			break
		}

		switch {
		case fileExists(filepath.Join(dir, "go.mod")):
			return deltaModuleTarget{Kind: "go", Dir: dir}, true
		case fileExists(filepath.Join(dir, "Cargo.toml")):
			return deltaModuleTarget{Kind: "rust", Dir: dir}, true
		case fileExists(filepath.Join(dir, "pom.xml")):
			return deltaModuleTarget{Kind: "java_maven", Dir: dir}, true
		case fileExists(filepath.Join(dir, "build.gradle")), fileExists(filepath.Join(dir, "build.gradle.kts")), fileExists(filepath.Join(dir, "gradlew")):
			return deltaModuleTarget{Kind: "java_gradle", Dir: dir}, true
		case fileExists(filepath.Join(dir, "pyproject.toml")), fileExists(filepath.Join(dir, "requirements.txt")), fileExists(filepath.Join(dir, "setup.py")):
			return deltaModuleTarget{Kind: "python", Dir: dir}, true
		case fileExists(filepath.Join(dir, "package.json")):
			runner := detectNodeRunner(dir)
			script := selectNodeDeltaScript(filepath.Join(dir, "package.json"))
			if script == "" {
				return deltaModuleTarget{}, false
			}
			return deltaModuleTarget{Kind: "node", Dir: dir, Runner: runner, Script: script}, true
		}

		if dir == workAbs {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}

	return deltaModuleTarget{}, false
}

func selectNodeDeltaScript(packageJSONPath string) string {
	data, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return ""
	}
	var payload struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	if len(payload.Scripts) == 0 {
		return ""
	}

	lowerScripts := make(map[string]string, len(payload.Scripts))
	for key, body := range payload.Scripts {
		k := strings.ToLower(strings.TrimSpace(key))
		body = strings.TrimSpace(body)
		if k == "" || body == "" {
			continue
		}
		lowerScripts[k] = body
	}

	order := []string{"lint", "lint:ci", "typecheck", "check-types", "tsc", "check", "build", "build:ci"}
	for _, script := range order {
		body, ok := lowerScripts[script]
		if !ok {
			continue
		}
		if isLikelyNoopScript(body) {
			continue
		}
		return script
	}
	return ""
}

func detectNodeRunner(path string) string {
	for dir := path; ; dir = filepath.Dir(dir) {
		switch {
		case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
			return "pnpm"
		case fileExists(filepath.Join(dir, "yarn.lock")):
			return "yarn"
		case fileExists(filepath.Join(dir, "bun.lockb")), fileExists(filepath.Join(dir, "bun.lock")):
			return "bun"
		case fileExists(filepath.Join(dir, "package-lock.json")), fileExists(filepath.Join(dir, "npm-shrinkwrap.json")):
			return "npm"
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "npm"
}

func nodeDeltaRunnerCommand(runner, script string) (string, []string) {
	switch runner {
	case "pnpm":
		return "pnpm", []string{"-s", "run", script}
	case "yarn":
		return "yarn", []string{"-s", "run", script}
	case "bun":
		return "bun", []string{"run", script}
	default:
		return "npm", []string{"run", "-s", script}
	}
}

func normalizeDeltaRelPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "/")
	if path == "." || path == "" || path == ".." || strings.HasPrefix(path, "../") {
		return ""
	}
	return path
}

func deltaAbsToRel(workDir, absPath string) (string, bool) {
	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", false
	}
	workAbs, err := filepath.Abs(workDir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(workAbs, absPath)
	if err != nil {
		return "", false
	}
	rel = normalizeDeltaRelPath(rel)
	if rel == "" {
		return "", false
	}
	return rel, true
}

func deltaLocalSignature(workDir string, relPaths []string) string {
	if len(relPaths) == 0 {
		return "local:empty"
	}
	signatures := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			signatures = append(signatures, rel+":missing")
			continue
		}
		signatures = append(signatures, fmt.Sprintf("%s:%d:%d", rel, info.Size(), info.ModTime().UnixNano()))
	}
	sort.Strings(signatures)
	return strings.Join(signatures, "|")
}

func hashDeltaCheckState(changed []string, commands []deltaCheckCommand, hashSeed string) string {
	payload := make([]string, 0, len(changed)+len(commands)+1)
	payload = append(payload, hashSeed)
	payload = append(payload, changed...)
	for _, cmd := range commands {
		payload = append(payload, cmd.Key+"|"+cmd.Dir+"|"+cmd.Display)
	}
	sum := sha256.Sum256([]byte(strings.Join(payload, "\n")))
	return hex.EncodeToString(sum[:])
}

func trimDeltaOutput(content string, maxChars int) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if maxChars <= 0 || len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + "\n... (truncated)"
}

func deltaSkipSuffix(skipped int) string {
	if skipped <= 0 {
		return ""
	}
	return fmt.Sprintf(", %d skipped", skipped)
}

func isLikelyNoopScript(script string) bool {
	script = strings.ToLower(strings.TrimSpace(script))
	if script == "" {
		return true
	}
	switch script {
	case "true", ":", "exit 0":
		return true
	}
	if strings.Contains(script, "no test specified") {
		return true
	}
	return false
}

func isDeltaCheckCommandMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory")
}

func copyStringSet(src map[string]struct{}) map[string]struct{} {
	if len(src) == 0 {
		return map[string]struct{}{}
	}
	dst := make(map[string]struct{}, len(src))
	for key := range src {
		dst[key] = struct{}{}
	}
	return dst
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func logDeltaCheckFailure(result deltaCheckResult) {
	if result.Passed {
		return
	}
	logging.Warn("delta-check failed",
		"summary", result.Summary,
		"failed", result.Failed,
		"executed", result.Executed,
		"skipped", result.Skipped,
	)
}
