package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// detectProjectContext scans the working directory for project markers, framework info,
// and documentation files. Returns a string describing the detected project context.
// This is called once at startup and cached in detectedProjectContext.
func (a *App) detectProjectContext() string {
	var parts []string

	// Detect project type from marker files
	projectType := ""
	goModPath := filepath.Join(a.workDir, "go.mod")
	packageJSONPath := filepath.Join(a.workDir, "package.json")
	pyprojectPath := filepath.Join(a.workDir, "pyproject.toml")
	setupPyPath := filepath.Join(a.workDir, "setup.py")
	cargoTomlPath := filepath.Join(a.workDir, "Cargo.toml")

	switch {
	case fileExists(goModPath):
		projectType = "Go project"
		if info := a.extractGoModInfo(goModPath); info != "" {
			parts = append(parts, info)
		}
	case fileExists(packageJSONPath):
		projectType = "Node.js project"
		if info := a.extractPackageJSONInfo(packageJSONPath); info != "" {
			parts = append(parts, info)
		}
	case fileExists(pyprojectPath):
		projectType = "Python project"
		if info := a.readFirstLines(pyprojectPath, 10); info != "" {
			parts = append(parts, "pyproject.toml excerpt:\n"+info)
		}
	case fileExists(setupPyPath):
		projectType = "Python project"
		if info := a.readFirstLines(setupPyPath, 10); info != "" {
			parts = append(parts, "setup.py excerpt:\n"+info)
		}
	case fileExists(cargoTomlPath):
		projectType = "Rust project"
		if info := a.readFirstLines(cargoTomlPath, 10); info != "" {
			parts = append(parts, "Cargo.toml excerpt:\n"+info)
		}
	}

	if projectType != "" {
		parts = append([]string{"Detected project type: " + projectType}, parts...)
	}

	if projectMap := a.buildProjectMap(); projectMap != "" {
		parts = append(parts, projectMap)
	}

	// Scan for documentation files and read first 500 chars of each
	docFiles := []string{"README.md", "ARCHITECTURE.md", "CONTRIBUTING.md"}
	for _, docFile := range docFiles {
		docPath := filepath.Join(a.workDir, docFile)
		if fileExists(docPath) {
			content := readFileHead(docPath, 500)
			if content != "" {
				parts = append(parts, fmt.Sprintf("%s (first 500 chars):\n%s", docFile, content))
			}
		}
	}

	return strings.Join(parts, "\n\n")
}

func (a *App) buildProjectMap() string {
	if a == nil || strings.TrimSpace(a.workDir) == "" {
		return ""
	}

	entrypoints := a.findProjectMapFiles(8, isProjectMapEntrypoint)
	tests := a.findProjectMapFiles(8, isProjectMapTestFile)
	configs := a.detectProjectMapConfigs()
	scripts := a.detectProjectMapScripts()

	var lines []string
	if len(entrypoints) > 0 {
		lines = append(lines, "Entrypoints: "+strings.Join(entrypoints, ", "))
	}
	if len(tests) > 0 {
		lines = append(lines, "Tests: "+strings.Join(tests, ", "))
	}
	if len(configs) > 0 {
		lines = append(lines, "Configs: "+strings.Join(configs, ", "))
	}
	if len(scripts) > 0 {
		lines = append(lines, "Scripts: "+strings.Join(scripts, ", "))
	}
	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Project map:\n")
	for _, line := range lines {
		sb.WriteString("- ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (a *App) findProjectMapFiles(limit int, match func(string) bool) []string {
	if limit <= 0 {
		return nil
	}
	var matches []string
	_ = filepath.WalkDir(a.workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == a.workDir {
				return nil
			}
			if shouldSkipDoneGateDir(d.Name()) || pathDepth(a.workDir, path) > 4 {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(a.workDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." || strings.HasPrefix(rel, "..") {
			return nil
		}
		if match(rel) {
			matches = append(matches, rel)
		}
		return nil
	})
	sort.Strings(matches)
	if len(matches) > limit {
		matches = append(matches[:limit], fmt.Sprintf("(+%d more)", len(matches)-limit))
	}
	return matches
}

func isProjectMapEntrypoint(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	base := strings.ToLower(filepath.Base(rel))
	switch base {
	case "main.go", "main.ts", "main.tsx", "main.js", "main.jsx",
		"index.ts", "index.tsx", "index.js", "index.jsx",
		"server.ts", "server.js", "app.ts", "app.js":
		return true
	}
	return strings.HasPrefix(rel, "cmd/") && base == "main.go"
}

func isProjectMapTestFile(rel string) bool {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	base := strings.ToLower(filepath.Base(rel))
	if strings.HasSuffix(base, "_test.go") ||
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") {
		return true
	}
	return strings.Contains(rel, "/tests/") || strings.Contains(rel, "/test/")
}

func (a *App) detectProjectMapConfigs() []string {
	candidates := []string{
		"go.mod", "go.work", "package.json", "pnpm-workspace.yaml", "tsconfig.json",
		"pyproject.toml", "requirements.txt", "Cargo.toml", "Makefile", "justfile",
		"Dockerfile", "docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml",
		".golangci.yml", ".golangci.yaml",
	}
	var found []string
	for _, name := range candidates {
		if fileExists(filepath.Join(a.workDir, name)) {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

func (a *App) detectProjectMapScripts() []string {
	packageJSONPath := filepath.Join(a.workDir, "package.json")
	data, err := os.ReadFile(packageJSONPath)
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	names := make([]string, 0, len(pkg.Scripts))
	for name, command := range pkg.Scripts {
		name = strings.TrimSpace(name)
		command = strings.TrimSpace(command)
		if name == "" || command == "" {
			continue
		}
		names = append(names, name+"="+truncateProjectMapValue(command, 60))
	}
	sort.Strings(names)
	if len(names) > 8 {
		names = append(names[:8], fmt.Sprintf("(+%d more)", len(names)-8))
	}
	return names
}

func truncateProjectMapValue(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

// extractGoModInfo reads go.mod and extracts module name and key dependencies.
func (a *App) extractGoModInfo(goModPath string) string {
	f, err := os.Open(goModPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var moduleName string
	var deps []string
	knownFrameworks := map[string]string{
		"github.com/labstack/echo":           "Echo",
		"github.com/gin-gonic/gin":           "Gin",
		"github.com/gofiber/fiber":           "Fiber",
		"github.com/gorilla/mux":             "Gorilla Mux",
		"github.com/go-chi/chi":              "Chi",
		"google.golang.org/grpc":             "gRPC",
		"github.com/spf13/cobra":             "Cobra CLI",
		"github.com/spf13/viper":             "Viper",
		"gorm.io/gorm":                       "GORM",
		"github.com/jmoiron/sqlx":            "sqlx",
		"github.com/charmbracelet/bubbletea": "Bubble Tea TUI",
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			moduleName = strings.TrimPrefix(line, "module ")
		}
		// Check for known framework dependencies in require blocks
		for prefix, name := range knownFrameworks {
			if strings.Contains(line, prefix) {
				deps = append(deps, name)
			}
		}
	}

	var info []string
	if moduleName != "" {
		info = append(info, "Module: "+moduleName)
	}
	if len(deps) > 0 {
		info = append(info, "Key frameworks: "+strings.Join(deps, ", "))
	}
	return strings.Join(info, "\n")
}

// extractPackageJSONInfo reads package.json and extracts name and key dependencies.
func (a *App) extractPackageJSONInfo(packageJSONPath string) string {
	content := readFileHead(packageJSONPath, 2000)
	if content == "" {
		return ""
	}
	// Simple extraction without full JSON parsing to avoid importing encoding/json
	var info []string

	// Extract "name"
	if idx := strings.Index(content, `"name"`); idx >= 0 {
		rest := content[idx:]
		if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
			rest = rest[colonIdx+1:]
			rest = strings.TrimSpace(rest)
			if len(rest) > 0 && rest[0] == '"' {
				endQuote := strings.Index(rest[1:], `"`)
				if endQuote >= 0 {
					info = append(info, "Package: "+rest[1:endQuote+1])
				}
			}
		}
	}

	// Check for key frameworks in dependencies
	knownDeps := []string{"react", "vue", "angular", "svelte", "next", "express", "nestjs", "fastify", "nuxt"}
	var found []string
	for _, dep := range knownDeps {
		if strings.Contains(content, `"`+dep+`"`) {
			found = append(found, dep)
		}
	}
	if len(found) > 0 {
		info = append(info, "Key frameworks: "+strings.Join(found, ", "))
	}

	return strings.Join(info, "\n")
}

// readFirstLines reads the first N lines of a file and returns them as a string.
func (a *App) readFirstLines(filePath string, n int) string {
	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for i := 0; i < n && scanner.Scan(); i++ {
		lines = append(lines, scanner.Text())
	}
	return strings.Join(lines, "\n")
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readFileHead reads the first maxChars characters from a file.
func readFileHead(path string, maxChars int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := string(data)
	if len(content) > maxChars {
		content = content[:maxChars]
	}
	return content
}
