package evals

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const outputPreviewLimit = 6000

// RunOptions configures a coding eval run.
type RunOptions struct {
	ManifestPath   string
	FixturesRoot   string
	WorkRoot       string
	OutputPath     string
	AgentCommand   string
	ScenarioIDs    []string
	Providers      []string
	Models         []string
	Timeout        time.Duration
	KeepWorkspaces bool
	DryRun         bool
}

// Result is one scenario outcome, suitable for JSONL output.
type Result struct {
	ScenarioID     string            `json:"scenario_id"`
	Category       string            `json:"category"`
	Difficulty     string            `json:"difficulty"`
	Provider       string            `json:"provider,omitempty"`
	Model          string            `json:"model,omitempty"`
	Status         string            `json:"status"`
	Workspace      string            `json:"workspace,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	DurationMillis int64             `json:"duration_ms"`
	Agent          CommandResult     `json:"agent"`
	Verification   []CommandResult   `json:"verification"`
	ChangedFiles   []string          `json:"changed_files,omitempty"`
	Journal        *JournalSummary   `json:"journal,omitempty"`
	Metrics        map[string]bool   `json:"metrics"`
	Score          ScoreSummary      `json:"score"`
	Error          string            `json:"error,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// ScoreSummary is a compact aggregate over the boolean eval metrics.
type ScoreSummary struct {
	Passed int     `json:"passed"`
	Total  int     `json:"total"`
	Ratio  float64 `json:"ratio"`
}

// JournalSummary captures eval-relevant evidence from .gokin/execution_journal.jsonl.
type JournalSummary struct {
	Path                 string   `json:"path,omitempty"`
	ToolCalls            int      `json:"tool_calls"`
	Tools                []string `json:"tools,omitempty"`
	FilesRead            []string `json:"files_read,omitempty"`
	FilesEdited          []string `json:"files_edited,omitempty"`
	VerificationCommands []string `json:"verification_commands,omitempty"`
	FalseFileClaims      []string `json:"false_file_claims,omitempty"`
	ParseErrors          []string `json:"parse_errors,omitempty"`
}

// CommandResult captures one external command execution.
type CommandResult struct {
	Command        string `json:"command"`
	Success        bool   `json:"success"`
	ExitCode       int    `json:"exit_code"`
	DurationMillis int64  `json:"duration_ms"`
	OutputPreview  string `json:"output_preview,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Run executes selected coding eval scenarios.
func Run(ctx context.Context, opts RunOptions) ([]Result, error) {
	if opts.ManifestPath == "" {
		opts.ManifestPath = filepath.Join("evals", "coding", "manifest.json")
	}
	if opts.FixturesRoot == "" {
		opts.FixturesRoot = filepath.Join("evals", "coding", "fixtures")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Minute
	}

	manifest, err := LoadManifest(opts.ManifestPath)
	if err != nil {
		return nil, err
	}
	scenarios, err := selectScenarios(manifest.Scenarios, opts.ScenarioIDs)
	if err != nil {
		return nil, err
	}

	workRoot := opts.WorkRoot
	tempRoot := ""
	if workRoot == "" {
		tempRoot, err = os.MkdirTemp("", "gokin-evals-*")
		if err != nil {
			return nil, fmt.Errorf("create temp work root: %w", err)
		}
		workRoot = tempRoot
	}
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create work root: %w", err)
	}
	if tempRoot != "" && !opts.KeepWorkspaces {
		defer os.RemoveAll(tempRoot)
	}

	var out *os.File
	if opts.OutputPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.OutputPath), 0o755); err != nil {
			return nil, fmt.Errorf("create output dir: %w", err)
		}
		out, err = os.OpenFile(opts.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open output: %w", err)
		}
		defer out.Close()
	}

	matrix := buildProviderModelMatrix(opts.Providers, opts.Models)
	results := make([]Result, 0, len(scenarios)*len(matrix))
	for _, scenario := range scenarios {
		for _, variant := range matrix {
			result := runScenario(ctx, manifest, scenario, opts, workRoot, variant)
			results = append(results, result)
			if out != nil {
				if err := writeJSONL(out, result); err != nil {
					return results, err
				}
			}
		}
	}
	return results, nil
}

type matrixEntry struct {
	Provider string
	Model    string
}

func runScenario(ctx context.Context, manifest *Manifest, scenario Scenario, opts RunOptions, workRoot string, variant matrixEntry) (result Result) {
	start := time.Now()
	result = Result{
		ScenarioID: scenario.ID,
		Category:   scenario.Category,
		Difficulty: scenario.Difficulty,
		Provider:   variant.Provider,
		Model:      variant.Model,
		Status:     "running",
		StartedAt:  start,
		Metrics:    make(map[string]bool),
		Metadata: map[string]string{
			"manifest": manifest.Name,
			"fixture":  scenario.Fixture,
		},
	}
	if variant.Provider != "" {
		result.Metadata["provider"] = variant.Provider
	}
	if variant.Model != "" {
		result.Metadata["model"] = variant.Model
	}
	defer func() {
		result.FinishedAt = time.Now()
		result.DurationMillis = result.FinishedAt.Sub(start).Milliseconds()
	}()

	workspace := filepath.Join(workRoot, scenario.ID)
	if label := matrixLabel(variant); label != "" {
		workspace = filepath.Join(workRoot, scenario.ID, sanitizePathPart(label))
	}
	result.Workspace = workspace
	fixturePath := filepath.Join(opts.FixturesRoot, filepath.FromSlash(scenario.Fixture))
	if !dirExists(fixturePath) {
		result.Status = "fixture_missing"
		result.Error = fmt.Sprintf("fixture not found: %s", fixturePath)
		return result
	}

	if err := resetWorkspace(fixturePath, workspace); err != nil {
		result.Status = "setup_failed"
		result.Error = err.Error()
		return result
	}

	before, err := snapshotFiles(workspace)
	if err != nil {
		result.Status = "setup_failed"
		result.Error = err.Error()
		return result
	}

	if opts.DryRun {
		result.Status = "dry_run"
		result.Metrics["task_completed"] = true
		result.Score = summarizeScore(result.Metrics)
		return result
	}
	if strings.TrimSpace(opts.AgentCommand) == "" {
		result.Status = "agent_command_missing"
		result.Error = "agent command is required unless dry_run is true"
		return result
	}

	agentCommand := expandCommandTemplate(opts.AgentCommand, manifest, scenario, workspace, variant)
	result.Agent = runShellCommand(ctx, workspace, agentCommand, opts.Timeout, evalEnv(manifest, scenario, workspace, variant))

	afterAgent, _ := snapshotFiles(workspace)
	result.ChangedFiles = diffSnapshots(before, afterAgent)

	for _, command := range scenario.VerificationCommands {
		verification := runShellCommand(ctx, workspace, command, opts.Timeout, evalEnv(manifest, scenario, workspace, variant))
		result.Verification = append(result.Verification, verification)
	}

	afterVerify, _ := snapshotFiles(workspace)
	result.ChangedFiles = diffSnapshots(before, afterVerify)
	result.Journal = summarizeExecutionJournal(workspace, result.Agent.OutputPreview, result.ChangedFiles)
	result.Metrics = scoreScenario(scenario, result)
	result.Score = summarizeScore(result.Metrics)

	if result.Agent.Success && allCommandsSuccessful(result.Verification) {
		result.Status = "passed"
	} else {
		result.Status = "failed"
	}
	return result
}

func selectScenarios(all []Scenario, ids []string) ([]Scenario, error) {
	if len(ids) == 0 {
		return all, nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id != "" {
			want[id] = true
		}
	}
	var selected []Scenario
	for _, scenario := range all {
		if want[scenario.ID] {
			selected = append(selected, scenario)
			delete(want, scenario.ID)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown scenario id(s): %s", strings.Join(missing, ", "))
	}
	return selected, nil
}

func buildProviderModelMatrix(providers, models []string) []matrixEntry {
	providers = compactNonEmptyUnique(providers)
	models = compactNonEmptyUnique(models)
	if len(providers) == 0 && len(models) == 0 {
		return []matrixEntry{{}}
	}
	if len(providers) == 0 {
		entries := make([]matrixEntry, 0, len(models))
		for _, model := range models {
			entries = append(entries, matrixEntry{Model: model})
		}
		return entries
	}
	if len(models) == 0 {
		entries := make([]matrixEntry, 0, len(providers))
		for _, provider := range providers {
			entries = append(entries, matrixEntry{Provider: provider})
		}
		return entries
	}

	entries := make([]matrixEntry, 0, len(providers)*len(models))
	for _, provider := range providers {
		for _, model := range models {
			entries = append(entries, matrixEntry{Provider: provider, Model: model})
		}
	}
	return entries
}

func compactNonEmptyUnique(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func matrixLabel(entry matrixEntry) string {
	switch {
	case entry.Provider != "" && entry.Model != "":
		return entry.Provider + "-" + entry.Model
	case entry.Provider != "":
		return entry.Provider
	case entry.Model != "":
		return entry.Model
	default:
		return ""
	}
}

func sanitizePathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "default"
	}
	return out
}

func resetWorkspace(src, dst string) error {
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("reset workspace: %w", err)
	}
	return copyDir(src, dst)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() && shouldSkipCopyDir(d.Name()) {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func shouldSkipCopyDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".gokin", ".pytest_cache", "__pycache__":
		return true
	default:
		return false
	}
}

func runShellCommand(ctx context.Context, dir, command string, timeout time.Duration, env []string) CommandResult {
	start := time.Now()
	result := CommandResult{Command: command, ExitCode: -1}

	cmdCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	result.DurationMillis = time.Since(start).Milliseconds()
	result.OutputPreview = trimPreview(string(output), outputPreviewLimit)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		if cmdCtx.Err() == context.DeadlineExceeded {
			result.Error = fmt.Sprintf("command timed out after %v", timeout)
		} else {
			result.Error = err.Error()
		}
		return result
	}
	result.Success = true
	result.ExitCode = 0
	return result
}

func evalEnv(manifest *Manifest, scenario Scenario, workspace string, variant matrixEntry) []string {
	return []string{
		"GOKIN_EVAL_MANIFEST=" + manifest.Name,
		"GOKIN_EVAL_SCENARIO_ID=" + scenario.ID,
		"GOKIN_EVAL_CATEGORY=" + scenario.Category,
		"GOKIN_EVAL_DIFFICULTY=" + scenario.Difficulty,
		"GOKIN_EVAL_FIXTURE=" + scenario.Fixture,
		"GOKIN_EVAL_WORKSPACE=" + workspace,
		"GOKIN_EVAL_PROVIDER=" + variant.Provider,
		"GOKIN_EVAL_MODEL=" + variant.Model,
		"GOKIN_EVAL_PROMPT=" + scenario.Prompt,
	}
}

func expandCommandTemplate(command string, manifest *Manifest, scenario Scenario, workspace string, variant matrixEntry) string {
	replacements := map[string]string{
		"{{manifest}}":    shellQuote(manifest.Name),
		"{{scenario_id}}": shellQuote(scenario.ID),
		"{{category}}":    shellQuote(scenario.Category),
		"{{difficulty}}":  shellQuote(scenario.Difficulty),
		"{{fixture}}":     shellQuote(scenario.Fixture),
		"{{workspace}}":   shellQuote(workspace),
		"{{provider}}":    shellQuote(variant.Provider),
		"{{model}}":       shellQuote(variant.Model),
		"{{prompt}}":      shellQuote(scenario.Prompt),
	}
	out := command
	for key, value := range replacements {
		out = strings.ReplaceAll(out, key, value)
	}
	return out
}

func snapshotFiles(root string) (map[string]string, error) {
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && shouldSkipCopyDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = hash
		return nil
	})
	return out, err
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func diffSnapshots(before, after map[string]string) []string {
	seen := make(map[string]bool)
	for path, hash := range after {
		if before[path] != hash {
			seen[path] = true
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			seen[path] = true
		}
	}
	changed := make([]string, 0, len(seen))
	for path := range seen {
		changed = append(changed, path)
	}
	sort.Strings(changed)
	return changed
}

type journalEvent struct {
	Event   string         `json:"event"`
	Details map[string]any `json:"details"`
}

func summarizeExecutionJournal(workspace, output string, changed []string) *JournalSummary {
	falseClaims := falseFileClaims(output, changed)
	journalPath := filepath.Join(workspace, ".gokin", "execution_journal.jsonl")
	f, err := os.Open(journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			if len(falseClaims) == 0 {
				return nil
			}
			return &JournalSummary{FalseFileClaims: falseClaims}
		}
		return &JournalSummary{
			Path:            ".gokin/execution_journal.jsonl",
			FalseFileClaims: falseClaims,
			ParseErrors:     []string{err.Error()},
		}
	}
	defer f.Close()

	summary := &JournalSummary{
		Path:            ".gokin/execution_journal.jsonl",
		FalseFileClaims: falseClaims,
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event journalEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			summary.ParseErrors = append(summary.ParseErrors, fmt.Sprintf("line %d: %v", lineNo, err))
			continue
		}
		recordJournalEvent(summary, workspace, event)
	}
	if err := scanner.Err(); err != nil {
		summary.ParseErrors = append(summary.ParseErrors, err.Error())
	}

	sort.Strings(summary.Tools)
	sort.Strings(summary.FilesRead)
	sort.Strings(summary.FilesEdited)
	sort.Strings(summary.VerificationCommands)
	sort.Strings(summary.FalseFileClaims)
	return summary
}

func recordJournalEvent(summary *JournalSummary, workspace string, event journalEvent) {
	switch strings.ToLower(strings.TrimSpace(event.Event)) {
	case "tool_start":
		recordJournalToolStart(summary, workspace, event.Details)
	case "plan_step_verification_passed":
		if text := detailString(event.Details, "summary", "command"); text != "" {
			appendUniqueString(&summary.VerificationCommands, text)
		} else {
			appendUniqueString(&summary.VerificationCommands, "plan_step_verification_passed")
		}
	}
}

func recordJournalToolStart(summary *JournalSummary, workspace string, details map[string]any) {
	tool := detailString(details, "tool", "name", "tool_name")
	if tool == "" {
		return
	}
	summary.ToolCalls++
	appendUniqueString(&summary.Tools, tool)

	args, _ := details["args"].(map[string]any)
	switch strings.ToLower(tool) {
	case "read":
		appendArgPaths(&summary.FilesRead, workspace, args, "file_path", "path")
	case "write", "edit", "delete", "mkdir", "refactor":
		appendArgPaths(&summary.FilesEdited, workspace, args, "file_path", "path")
	case "move", "copy":
		appendArgPaths(&summary.FilesEdited, workspace, args, "source", "destination", "new_path")
	case "batch":
		appendBatchEditedPaths(&summary.FilesEdited, workspace, args)
	case "bash":
		command := detailString(args, "command")
		if commandLooksLikeVerification(command) {
			appendUniqueString(&summary.VerificationCommands, command)
		}
	case "run_tests", "verify_code":
		appendUniqueString(&summary.VerificationCommands, tool)
	}
}

func appendBatchEditedPaths(paths *[]string, workspace string, args map[string]any) {
	if args == nil {
		return
	}
	tools, ok := args["tools"].([]any)
	if !ok {
		tools, _ = args["calls"].([]any)
	}
	for _, item := range tools {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.ToLower(detailString(call, "tool", "name", "tool_name"))
		callArgs, _ := call["args"].(map[string]any)
		switch name {
		case "write", "edit", "delete", "mkdir", "refactor":
			appendArgPaths(paths, workspace, callArgs, "file_path", "path")
		case "move", "copy":
			appendArgPaths(paths, workspace, callArgs, "source", "destination", "new_path")
		}
	}
}

func appendArgPaths(paths *[]string, workspace string, args map[string]any, keys ...string) {
	if args == nil {
		return
	}
	for _, key := range keys {
		appendPathValue(paths, workspace, args[key])
	}
}

func appendPathValue(paths *[]string, workspace string, value any) {
	switch v := value.(type) {
	case string:
		path := normalizeJournalPath(workspace, v)
		if path != "" {
			appendUniqueString(paths, path)
		}
	case []any:
		for _, item := range v {
			appendPathValue(paths, workspace, item)
		}
	case []string:
		for _, item := range v {
			appendPathValue(paths, workspace, item)
		}
	}
}

func normalizeJournalPath(workspace, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(workspace, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
		return filepath.ToSlash(filepath.Clean(path))
	}
	path = strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "./")
	if path == "." {
		return ""
	}
	return path
}

func detailString(details map[string]any, keys ...string) string {
	if details == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := details[key]; ok {
			if s, ok := value.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func appendUniqueString(items *[]string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	for _, existing := range *items {
		if existing == value {
			return
		}
	}
	*items = append(*items, value)
}

func commandLooksLikeVerification(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	patterns := []string{
		"go test", "go vet", "go build", "pytest", "cargo test", "cargo check",
		"npm test", "npm run test", "npm run lint", "npm run typecheck",
		"pnpm test", "pnpm lint", "pnpm typecheck",
		"yarn test", "yarn lint", "yarn typecheck",
		"bun test", "dotnet test", "mvn test", "gradle test",
		"swift test", "zig test", "make test", "make check",
		"ruff", "mypy", "tsc", "eslint", "golangci-lint",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return strings.Contains(lower, " verify") || strings.Contains(lower, " lint") ||
		strings.Contains(lower, " typecheck") || strings.Contains(lower, " compile")
}

func scoreScenario(scenario Scenario, result Result) map[string]bool {
	agentOutput := result.Agent.OutputPreview
	verificationPassed := allCommandsSuccessful(result.Verification)
	toolCallsReasonable := observedToolCallsWithinLimit(agentOutput, scenario.MaxToolCalls)
	if result.Journal != nil && result.Journal.ToolCalls > 0 && scenario.MaxToolCalls > 0 {
		toolCallsReasonable = result.Journal.ToolCalls <= scenario.MaxToolCalls
	}
	journalPresent := result.Journal != nil && result.Journal.Path != ""
	metrics := map[string]bool{
		"task_completed":                     result.Agent.Success,
		"verification_passed":                verificationPassed,
		"touched_files_scoped":               touchedFilesScoped(result.ChangedFiles),
		"no_false_file_claims":               noFalseFileClaims(agentOutput, result.ChangedFiles),
		"tool_calls_reasonable":              toolCallsReasonable,
		"final_answer_mentions_verification": mentionsVerification(agentOutput, scenario.VerificationCommands),
		"journal_present":                    journalPresent,
		"files_read_recorded":                result.Journal != nil && len(result.Journal.FilesRead) > 0,
		"files_edited_recorded":              len(result.ChangedFiles) == 0 || (result.Journal != nil && len(result.Journal.FilesEdited) > 0),
		"verification_recorded":              result.Journal != nil && len(result.Journal.VerificationCommands) > 0,
	}
	return metrics
}

func summarizeScore(metrics map[string]bool) ScoreSummary {
	if len(metrics) == 0 {
		return ScoreSummary{}
	}
	passed := 0
	for _, ok := range metrics {
		if ok {
			passed++
		}
	}
	return ScoreSummary{
		Passed: passed,
		Total:  len(metrics),
		Ratio:  float64(passed) / float64(len(metrics)),
	}
}

func allCommandsSuccessful(commands []CommandResult) bool {
	if len(commands) == 0 {
		return false
	}
	for _, command := range commands {
		if !command.Success {
			return false
		}
	}
	return true
}

func touchedFilesScoped(paths []string) bool {
	for _, path := range paths {
		lower := strings.ToLower(filepath.ToSlash(path))
		if strings.HasPrefix(lower, "vendor/") ||
			strings.Contains(lower, "/vendor/") ||
			strings.HasPrefix(lower, "node_modules/") ||
			strings.Contains(lower, "/node_modules/") ||
			strings.HasPrefix(lower, ".git/") {
			return false
		}
	}
	return true
}

var pathTokenRE = regexp.MustCompile(`\b[A-Za-z0-9_\-./]+\.(?:go|py|ts|tsx|js|jsx|rs|java|kt|rb|swift|c|cc|cpp|h|hpp|yaml|yml|toml|json|md|mod|sum)\b`)

func noFalseFileClaims(output string, changed []string) bool {
	return len(falseFileClaims(output, changed)) == 0
}

func falseFileClaims(output string, changed []string) []string {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	var claims []string
	allowed := make(map[string]bool, len(changed)*2)
	for _, path := range changed {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		allowed[path] = true
		allowed[filepath.Base(path)] = true
	}
	for _, token := range pathTokenRE.FindAllString(output, -1) {
		token = strings.Trim(token, ".,;:()[]{}\"'`")
		if token == "" {
			continue
		}
		if allowed[token] || allowed[filepath.Base(token)] {
			continue
		}
		// Commands like go test ./... and package names can include .; don't fail on them.
		if strings.HasPrefix(token, "./") || token == "go.mod" || token == "go.sum" || token == "package.json" {
			continue
		}
		appendUniqueString(&claims, token)
	}
	return claims
}

func observedToolCallsWithinLimit(output string, max int) bool {
	if max <= 0 {
		return true
	}
	count := strings.Count(strings.ToLower(output), "tool call")
	if count == 0 {
		return true
	}
	return count <= max
}

func mentionsVerification(output string, commands []string) bool {
	lower := strings.ToLower(output)
	if strings.TrimSpace(lower) == "" {
		return false
	}
	if strings.Contains(lower, "verified") || strings.Contains(lower, "verification") ||
		strings.Contains(lower, "test") || strings.Contains(lower, "build") {
		return true
	}
	for _, command := range commands {
		command = strings.ToLower(strings.TrimSpace(command))
		if command != "" && strings.Contains(lower, command) {
			return true
		}
	}
	return false
}

func writeJSONL(w io.Writer, result Result) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func trimPreview(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return s[:limit] + fmt.Sprintf("\n...(%d chars truncated)", len(s)-limit)
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ReadResults reads JSONL result files written by Run.
func ReadResults(path string) ([]Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var results []Result
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var result Result
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, scanner.Err()
}
