package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
)

// VerifyCodeTool automatically checks code correctness.
type VerifyCodeTool struct {
	workDir string
}

// NewVerifyCodeTool creates a new VerifyCodeTool instance.
func NewVerifyCodeTool(workDir string) *VerifyCodeTool {
	return &VerifyCodeTool{
		workDir: workDir,
	}
}

func (t *VerifyCodeTool) Name() string {
	return "verify_code"
}

func (t *VerifyCodeTool) Description() string {
	return `Automatically verifies code correctness in the project.
Detects project type (Go, Node.js, Python, Rust) and runs relevant checks like build or lint.
Use this after making changes to ensure no regressions or syntax errors were introduced.`
}

func (t *VerifyCodeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {
					Type:        genai.TypeString,
					Description: "The directory to verify (defaults to project root)",
				},
			},
		},
	}
}

func (t *VerifyCodeTool) Validate(args map[string]any) error {
	return nil
}

func (t *VerifyCodeTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	path, _ := GetString(args, "path")
	if path == "" {
		path = t.workDir
	}

	const verifyTimeout = 3 * time.Minute
	// Cap raw compiler/linter output so a failing build over a large module
	// can't flood the model's context. The failure path puts output in the
	// Error field, which (unlike Content) escapes ToMap/ResponseCompressor
	// caps — so bound it at the source, like the sibling delta_check.
	const verifyOutputMaxChars = 12000
	verifyCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	// 1. Detect project type and the best target directory for verification.
	projectType, targetDir := t.detectProjectTarget(path)
	if projectType == "" {
		return NewErrorResult("Could not detect project type for verification. Supported: Go, Node.js, Python, Rust."), nil
	}
	if targetDir == "" {
		targetDir = path
	}

	// 2. Run verification command
	var cmd *exec.Cmd
	var checkName string

	switch projectType {
	case "go":
		checkName = "go build ./..."
		cmd = exec.CommandContext(verifyCtx, "go", "build", "./...")
	case "rust":
		checkName = "cargo check --all-targets"
		cmd = exec.CommandContext(verifyCtx, "cargo", "check", "--all-targets")
	case "node":
		checkName, cmd = t.nodeVerificationCommand(verifyCtx, targetDir)
		if cmd == nil {
			return NewSuccessResult("Verification skipped: Node project has no build/lint/typecheck/check script in package.json"), nil
		}
	case "python":
		checkName = "python3 -m compileall -q ."
		cmd = exec.CommandContext(verifyCtx, "python3", "-m", "compileall", "-q", ".")
	}

	if cmd == nil {
		return NewErrorResult(fmt.Sprintf("No verification command found for project type: %s", projectType)), nil
	}

	cmd.Dir = targetDir
	output, err := cmd.CombinedOutput()

	if err != nil {
		// Honest classification: the 3-min budget elapsing (or a parent
		// Esc-cancel) returns empty/truncated output — reporting that as a
		// compile-style "Verification failed" would send the model chasing a
		// phantom build error. Match evals/runner.go's direct ctx.Err() check.
		if verifyCtx.Err() == context.DeadlineExceeded {
			return NewErrorResult(fmt.Sprintf("Verification timed out after %v (%s) — code may still be fine; the check ran out of its time budget", verifyTimeout, checkName)), nil
		}
		if verifyCtx.Err() == context.Canceled {
			return NewErrorResult(fmt.Sprintf("Verification cancelled before completion (%s)", checkName)), nil
		}
		// Head+tail trim: node/python toolchains print the fatal error LAST —
		// a head-only trim would keep 12K of progress noise and cut the reason.
		out := trimOutputKeepEnds(string(output), verifyOutputMaxChars)
		return ToolResult{
			Success: false,
			Error:   fmt.Sprintf("Verification failed (%s):\n%s", checkName, out),
			Content: out,
		}, nil
	}

	location := "."
	if rel, relErr := filepath.Rel(path, targetDir); relErr == nil && rel != "" {
		location = rel
	}
	if location != "." {
		checkName = fmt.Sprintf("%s (dir: %s)", checkName, location)
	}

	return NewSuccessResult(fmt.Sprintf("Verification successful (%s):\n%s", checkName, trimDeltaOutput(string(output), verifyOutputMaxChars))), nil
}

func (t *VerifyCodeTool) detectProjectTarget(path string) (string, string) {
	// Prefer direct markers in the requested path.
	if t.fileExists(filepath.Join(path, "go.mod")) {
		return "go", path
	}
	if t.fileExists(filepath.Join(path, "Cargo.toml")) {
		return "rust", path
	}
	if t.fileExists(filepath.Join(path, "package.json")) {
		return "node", path
	}
	if t.fileExists(filepath.Join(path, "requirements.txt")) || t.fileExists(filepath.Join(path, "pyproject.toml")) || t.fileExists(filepath.Join(path, "setup.py")) {
		return "python", path
	}

	// For monorepo roots, discover nearest module/package markers.
	candidates := t.discoverProjectCandidates(path)
	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].depth == candidates[j].depth {
				if candidates[i].projectType == candidates[j].projectType {
					return candidates[i].dir < candidates[j].dir
				}
				return candidates[i].projectType < candidates[j].projectType
			}
			return candidates[i].depth < candidates[j].depth
		})
		best := candidates[0]
		return best.projectType, best.dir
	}

	// Final fallback: infer from extensions in the current directory only.
	files, _ := os.ReadDir(path)
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".go") {
			return "go", path
		}
		if strings.HasSuffix(f.Name(), ".rs") {
			return "rust", path
		}
		if strings.HasSuffix(f.Name(), ".js") || strings.HasSuffix(f.Name(), ".ts") {
			return "node", path
		}
		if strings.HasSuffix(f.Name(), ".py") {
			return "python", path
		}
	}

	return "", ""
}

type verifyProjectCandidate struct {
	projectType string
	dir         string
	depth       int
}

func (t *VerifyCodeTool) discoverProjectCandidates(root string) []verifyProjectCandidate {
	var candidates []verifyProjectCandidate
	seen := make(map[string]bool)

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if shouldSkipVerifyDir(d.Name()) {
				return filepath.SkipDir
			}
			if pathDepth(root, path) > 4 {
				return filepath.SkipDir
			}
			return nil
		}

		dir := filepath.Dir(path)
		if pathDepth(root, dir) > 4 {
			return nil
		}

		var projectType string
		switch d.Name() {
		case "go.mod":
			projectType = "go"
		case "Cargo.toml":
			projectType = "rust"
		case "package.json":
			projectType = "node"
		case "pyproject.toml", "requirements.txt", "setup.py":
			projectType = "python"
		default:
			return nil
		}

		key := projectType + "|" + dir
		if seen[key] {
			return nil
		}
		seen[key] = true

		candidates = append(candidates, verifyProjectCandidate{
			projectType: projectType,
			dir:         dir,
			depth:       pathDepth(root, dir),
		})
		return nil
	})

	return candidates
}

func shouldSkipVerifyDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".next", ".turbo",
		".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache":
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

func (t *VerifyCodeTool) nodeVerificationCommand(ctx context.Context, path string) (string, *exec.Cmd) {
	packageJSON := filepath.Join(path, "package.json")
	if !t.fileExists(packageJSON) {
		return "", nil
	}

	data, err := os.ReadFile(packageJSON)
	if err != nil {
		return "", nil
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", nil
	}

	lowerScripts := make(map[string]string, len(pkg.Scripts))
	for name, script := range pkg.Scripts {
		name = strings.ToLower(strings.TrimSpace(name))
		script = strings.TrimSpace(script)
		if name == "" || script == "" {
			continue
		}
		lowerScripts[name] = script
	}

	runner := t.detectNodeRunner(path)
	scriptOrder := []string{
		"build", "build:ci", "typecheck", "check-types", "types", "tsc", "lint", "lint:ci", "check",
	}
	for _, scriptName := range scriptOrder {
		if _, ok := lowerScripts[scriptName]; !ok {
			continue
		}
		return nodeRunnerCommand(ctx, runner, scriptName)
	}

	return "", nil
}

func (t *VerifyCodeTool) detectNodeRunner(path string) string {
	packageJSONPath := filepath.Join(path, "package.json")
	data, err := os.ReadFile(packageJSONPath)
	if err == nil {
		var pkg struct {
			PackageManager string `json:"packageManager"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			pm := strings.ToLower(strings.TrimSpace(pkg.PackageManager))
			switch {
			case strings.HasPrefix(pm, "pnpm@"):
				return "pnpm"
			case strings.HasPrefix(pm, "yarn@"):
				return "yarn"
			case strings.HasPrefix(pm, "bun@"):
				return "bun"
			case strings.HasPrefix(pm, "npm@"):
				return "npm"
			}
		}
	}

	for dir := path; ; dir = filepath.Dir(dir) {
		if t.fileExists(filepath.Join(dir, "pnpm-lock.yaml")) {
			return "pnpm"
		}
		if t.fileExists(filepath.Join(dir, "yarn.lock")) {
			return "yarn"
		}
		if t.fileExists(filepath.Join(dir, "bun.lockb")) || t.fileExists(filepath.Join(dir, "bun.lock")) {
			return "bun"
		}
		if t.fileExists(filepath.Join(dir, "package-lock.json")) || t.fileExists(filepath.Join(dir, "npm-shrinkwrap.json")) {
			return "npm"
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "npm"
}

func nodeRunnerCommand(ctx context.Context, runner, scriptName string) (string, *exec.Cmd) {
	switch runner {
	case "pnpm":
		return "pnpm -s run " + scriptName, exec.CommandContext(ctx, "pnpm", "-s", "run", scriptName)
	case "yarn":
		return "yarn -s run " + scriptName, exec.CommandContext(ctx, "yarn", "-s", "run", scriptName)
	case "bun":
		return "bun run " + scriptName, exec.CommandContext(ctx, "bun", "run", scriptName)
	default:
		return "npm run -s " + scriptName, exec.CommandContext(ctx, "npm", "run", "-s", scriptName)
	}
}

func (t *VerifyCodeTool) fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
