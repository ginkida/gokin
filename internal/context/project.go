package context

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// GetConfigDir returns the configuration directory for the application.
// It follows XDG Base Directory Specification on Linux/macOS.
func GetConfigDir() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "gokin"), nil
}

// ProjectType represents the detected project type.
type ProjectType string

const (
	ProjectTypeGo      ProjectType = "go"
	ProjectTypeNode    ProjectType = "node"
	ProjectTypeRust    ProjectType = "rust"
	ProjectTypePython  ProjectType = "python"
	ProjectTypeJava    ProjectType = "java"
	ProjectTypeRuby    ProjectType = "ruby"
	ProjectTypePHP     ProjectType = "php"
	ProjectTypeUnknown ProjectType = "unknown"
)

// ProjectInfo contains detected project information.
type ProjectInfo struct {
	Type           ProjectType
	Name           string
	RootDir        string
	Dependencies   []string
	PackageManager string
	MainFiles      []string
	TestFramework  string
	BuildTool      string

	// Docker context (overlay — any project type can have Docker)
	HasDocker        bool     // Dockerfile exists
	HasDockerCompose bool     // docker-compose.yml/yaml exists
	ComposeFile      string   // path to compose file ("docker-compose.yml" or "compose.yml")
	DockerServices   []string // service names from compose file
	DockerBaseImage  string   // base image from Dockerfile (first FROM)
}

// projectMarkers maps file patterns to project types.
var projectMarkers = map[ProjectType][]string{
	ProjectTypeGo:     {"go.mod", "go.sum"},
	ProjectTypeNode:   {"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"},
	ProjectTypeRust:   {"Cargo.toml", "Cargo.lock"},
	ProjectTypePython: {"pyproject.toml", "requirements.txt", "setup.py", "Pipfile"},
	ProjectTypeJava:   {"pom.xml", "build.gradle", "build.gradle.kts"},
	ProjectTypeRuby:   {"Gemfile", "Rakefile"},
	ProjectTypePHP:    {"composer.json", "composer.lock"},
}

// DetectProject detects project type and information from the working directory.
func DetectProject(workDir string) *ProjectInfo {
	info := &ProjectInfo{
		Type:    ProjectTypeUnknown,
		RootDir: workDir,
	}

	// Check each project type
	for projectType, markers := range projectMarkers {
		for _, marker := range markers {
			markerPath := filepath.Join(workDir, marker)
			if _, err := os.Stat(markerPath); err == nil {
				info.Type = projectType
				info.extractProjectDetails(workDir, marker)
				break
			}
		}
		if info.Type != ProjectTypeUnknown {
			break
		}
	}

	// Detect Docker overlay (any project type can have Docker)
	info.detectDocker(workDir)

	return info
}

// extractProjectDetails extracts additional project details based on type.
func (p *ProjectInfo) extractProjectDetails(workDir, markerFile string) {
	switch p.Type {
	case ProjectTypeGo:
		p.extractGoDetails(workDir)
	case ProjectTypeNode:
		p.extractNodeDetails(workDir)
	case ProjectTypeRust:
		p.extractRustDetails(workDir)
	case ProjectTypePython:
		p.extractPythonDetails(workDir)
	}
}

// extractGoDetails extracts Go project details.
func (p *ProjectInfo) extractGoDetails(workDir string) {
	p.BuildTool = "go"
	p.TestFramework = "go test"

	// Read go.mod for module name
	goModPath := filepath.Join(workDir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			p.Name = strings.TrimPrefix(line, "module ")
			break
		}
	}

	// Find main files
	p.MainFiles = findFiles(workDir, "main.go")
}

// extractNodeDetails extracts Node.js project details.
func (p *ProjectInfo) extractNodeDetails(workDir string) {
	// Detect package manager
	if _, err := os.Stat(filepath.Join(workDir, "bun.lockb")); err == nil {
		p.PackageManager = "bun"
	} else if _, err := os.Stat(filepath.Join(workDir, "pnpm-lock.yaml")); err == nil {
		p.PackageManager = "pnpm"
	} else if _, err := os.Stat(filepath.Join(workDir, "yarn.lock")); err == nil {
		p.PackageManager = "yarn"
	} else {
		p.PackageManager = "npm"
	}

	// Read package.json
	pkgPath := filepath.Join(workDir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return
	}

	var pkg struct {
		Name         string            `json:"name"`
		Scripts      map[string]string `json:"scripts"`
		Dependencies map[string]string `json:"dependencies"`
	}

	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}

	p.Name = pkg.Name

	// Detect test framework
	if _, ok := pkg.Scripts["test"]; ok {
		p.TestFramework = p.PackageManager + " test"
	}

	// Detect build tool from scripts
	if _, ok := pkg.Scripts["build"]; ok {
		p.BuildTool = p.PackageManager + " run build"
	}

	// Extract key dependencies
	for dep := range pkg.Dependencies {
		if isKeyDependency(dep) {
			p.Dependencies = append(p.Dependencies, dep)
		}
	}
}

// extractRustDetails extracts Rust project details.
func (p *ProjectInfo) extractRustDetails(workDir string) {
	p.BuildTool = "cargo"
	p.TestFramework = "cargo test"

	// Basic Cargo.toml parsing
	cargoPath := filepath.Join(workDir, "Cargo.toml")
	data, err := os.ReadFile(cargoPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	inPackage := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "[package]" {
			inPackage = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inPackage = false
		}
		if inPackage && strings.HasPrefix(line, "name") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				p.Name = strings.Trim(strings.TrimSpace(parts[1]), "\"")
			}
		}
	}
}

// extractPythonDetails extracts Python project details.
func (p *ProjectInfo) extractPythonDetails(workDir string) {
	// Detect package manager
	if _, err := os.Stat(filepath.Join(workDir, "poetry.lock")); err == nil {
		p.PackageManager = "poetry"
	} else if _, err := os.Stat(filepath.Join(workDir, "Pipfile")); err == nil {
		p.PackageManager = "pipenv"
	} else if _, err := os.Stat(filepath.Join(workDir, "uv.lock")); err == nil {
		p.PackageManager = "uv"
	} else {
		p.PackageManager = "pip"
	}

	p.TestFramework = "pytest"
	p.BuildTool = p.PackageManager

	// Try to get project name from pyproject.toml
	pyprojectPath := filepath.Join(workDir, "pyproject.toml")
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				p.Name = strings.Trim(strings.TrimSpace(parts[1]), "\"")
			}
			break
		}
	}
}

// detectDocker checks for Docker and docker-compose files in the project.
func (p *ProjectInfo) detectDocker(workDir string) {
	// Check for Dockerfile
	if _, err := os.Stat(filepath.Join(workDir, "Dockerfile")); err == nil {
		p.HasDocker = true
		p.DockerBaseImage = extractDockerBaseImage(filepath.Join(workDir, "Dockerfile"))
	}

	// Check for docker-compose files (multiple naming conventions)
	composeFiles := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}
	for _, cf := range composeFiles {
		cfPath := filepath.Join(workDir, cf)
		if _, err := os.Stat(cfPath); err == nil {
			p.HasDockerCompose = true
			p.ComposeFile = cf
			p.DockerServices = extractComposeServices(cfPath)
			break
		}
	}
}

// extractDockerBaseImage reads the first FROM instruction from a Dockerfile.
func extractDockerBaseImage(dockerfilePath string) string {
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}

// extractComposeServices parses service names from a docker-compose YAML file.
// Uses simple line parsing to avoid a YAML dependency.
//
// Strategy: find the `services:` top-level key, then collect lines at exactly
// the next indent level (the service names). Deeper-indented lines (service
// properties like `image:`, `volumes:`, `ports:`) are skipped.
func extractComposeServices(composePath string) []string {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil
	}

	var services []string
	inServices := false
	serviceIndent := -1 // indent level of service names (detected from first service)
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip empty lines and comments
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect top-level "services:" key (no leading whitespace)
		if !inServices {
			if trimmed == "services:" && countIndent(line) == 0 {
				inServices = true
			}
			continue
		}

		indent := countIndent(line)

		// Another top-level key (indent 0) ends the services block
		if indent == 0 {
			break
		}

		// First non-empty indented line after "services:" sets the service indent level
		if serviceIndent < 0 {
			serviceIndent = indent
		}

		// Only lines at exactly the service indent level are service names.
		// Deeper lines are properties of a service (image, ports, volumes, etc.)
		if indent != serviceIndent {
			continue
		}

		// Service name: ends with ":" (optionally followed by space/anchor),
		// extract just the name part
		if idx := strings.Index(trimmed, ":"); idx > 0 {
			name := trimmed[:idx]
			// Skip YAML merge keys and variable references
			if name == "<<" || strings.HasPrefix(name, "$") {
				continue
			}
			services = append(services, name)
		}
	}

	return services
}

// countIndent returns the number of leading spaces (tabs count as 2).
func countIndent(line string) int {
	indent := 0
	for _, ch := range line {
		if ch == ' ' {
			indent++
		} else if ch == '\t' {
			indent += 2
		} else {
			break
		}
	}
	return indent
}

// findFiles finds files matching a pattern in the directory.
func findFiles(dir, pattern string) []string {
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Skip common directories
			base := filepath.Base(path)
			if base == "node_modules" || base == "vendor" || base == ".git" || base == "target" {
				return filepath.SkipDir
			}
		}
		if !info.IsDir() && filepath.Base(path) == pattern {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, rel)
		}
		return nil
	})
	return files
}

// isKeyDependency checks if a dependency is a key framework.
func isKeyDependency(dep string) bool {
	keyDeps := map[string]bool{
		"react": true, "vue": true, "angular": true, "svelte": true,
		"next": true, "nuxt": true, "express": true, "fastify": true,
		"nestjs": true, "typescript": true, "vite": true, "webpack": true,
	}
	return keyDeps[dep]
}

// String returns a human-readable string for the project type.
func (t ProjectType) String() string {
	switch t {
	case ProjectTypeGo:
		return "Go"
	case ProjectTypeNode:
		return "Node.js"
	case ProjectTypeRust:
		return "Rust"
	case ProjectTypePython:
		return "Python"
	case ProjectTypeJava:
		return "Java"
	case ProjectTypeRuby:
		return "Ruby"
	case ProjectTypePHP:
		return "PHP"
	default:
		return "Unknown"
	}
}
