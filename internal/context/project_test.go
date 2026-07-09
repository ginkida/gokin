package context

import (
	"os"
	"path/filepath"
	"testing"
)

// --- GetConfigDir ---

func TestGetConfigDir_XDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	dir, err := GetConfigDir()
	if err != nil {
		t.Fatalf("GetConfigDir: %v", err)
	}
	if dir != filepath.Join("/custom/xdg", "gokin") {
		t.Errorf("dir = %q, want /custom/xdg/gokin", dir)
	}
}

func TestGetConfigDir_FallbackHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir not available: %v", err)
	}
	dir, err := GetConfigDir()
	if err != nil {
		t.Fatalf("GetConfigDir: %v", err)
	}
	want := filepath.Join(home, ".config", "gokin")
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
}

// --- DetectProject ---

func TestDetectProject_Go(t *testing.T) {
	dir := t.TempDir()
	goMod := `module github.com/test/myproject

go 1.25
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	// main.go in a subdir to test findFiles
	sub := filepath.Join(dir, "cmd")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)
	if info.Type != ProjectTypeGo {
		t.Errorf("Type = %q, want %q", info.Type, ProjectTypeGo)
	}
	if info.Name != "github.com/test/myproject" {
		t.Errorf("Name = %q, want github.com/test/myproject", info.Name)
	}
	if info.BuildTool != "go" {
		t.Errorf("BuildTool = %q, want go", info.BuildTool)
	}
	if info.TestFramework != "go test" {
		t.Errorf("TestFramework = %q, want go test", info.TestFramework)
	}
	// main.go should be found (relative path)
	found := false
	for _, f := range info.MainFiles {
		if filepath.Base(f) == "main.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("main.go not in MainFiles: %v", info.MainFiles)
	}
}

func TestDetectProject_Node(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{
		"name": "myapp",
		"scripts": {
			"test": "jest",
			"build": "tsc"
		},
		"dependencies": {
			"react": "^18.0.0",
			"express": "^4.0.0",
			"lodash": "^4.0.0"
		}
	}
`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)
	if info.Type != ProjectTypeNode {
		t.Errorf("Type = %q, want %q", info.Type, ProjectTypeNode)
	}
	if info.Name != "myapp" {
		t.Errorf("Name = %q, want myapp", info.Name)
	}
	if info.PackageManager != "npm" {
		t.Errorf("PackageManager = %q, want npm", info.PackageManager)
	}
	if info.TestFramework != "npm test" {
		t.Errorf("TestFramework = %q, want npm test", info.TestFramework)
	}
	if info.BuildTool != "npm run build" {
		t.Errorf("BuildTool = %q, want npm run build", info.BuildTool)
	}
	// react and express are key deps; lodash is not
	hasReact, hasExpress, hasLodash := false, false, false
	for _, d := range info.Dependencies {
		switch d {
		case "react":
			hasReact = true
		case "express":
			hasExpress = true
		case "lodash":
			hasLodash = true
		}
	}
	if !hasReact || !hasExpress {
		t.Errorf("expected react and express in Dependencies: %v", info.Dependencies)
	}
	if hasLodash {
		t.Errorf("lodash should be filtered (not a key dep): %v", info.Dependencies)
	}
}

func TestDetectProject_Node_PackageManagers(t *testing.T) {
	cases := []struct {
		lockFile string
		want     string
	}{
		{"bun.lockb", "bun"},
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, c.lockFile), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			info := DetectProject(dir)
			if info.PackageManager != c.want {
				t.Errorf("PackageManager = %q, want %q", info.PackageManager, c.want)
			}
		})
	}
}

func TestDetectProject_Rust(t *testing.T) {
	dir := t.TempDir()
	cargo := `[package]
name = "myapp"
version = "0.1.0"

[dependencies]
`
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(cargo), 0o644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)
	if info.Type != ProjectTypeRust {
		t.Errorf("Type = %q, want %q", info.Type, ProjectTypeRust)
	}
	if info.Name != "myapp" {
		t.Errorf("Name = %q, want myapp", info.Name)
	}
	if info.BuildTool != "cargo" {
		t.Errorf("BuildTool = %q, want cargo", info.BuildTool)
	}
}

func TestDetectProject_Python(t *testing.T) {
	dir := t.TempDir()
	pyproject := `[project]
name = "myapp"
version = "0.1.0"
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0o644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)
	if info.Type != ProjectTypePython {
		t.Errorf("Type = %q, want %q", info.Type, ProjectTypePython)
	}
	if info.Name != "myapp" {
		t.Errorf("Name = %q, want myapp", info.Name)
	}
	if info.PackageManager != "pip" {
		t.Errorf("PackageManager = %q, want pip", info.PackageManager)
	}
	if info.TestFramework != "pytest" {
		t.Errorf("TestFramework = %q, want pytest", info.TestFramework)
	}
}

func TestDetectProject_Python_PackageManagers(t *testing.T) {
	cases := []struct {
		lockFile string
		want     string
	}{
		{"poetry.lock", "poetry"},
		{"Pipfile", "pipenv"},
		{"uv.lock", "uv"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			dir := t.TempDir()
			// pyproject.toml is the marker that triggers Python detection
			if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[project]`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, c.lockFile), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			info := DetectProject(dir)
			if info.PackageManager != c.want {
				t.Errorf("PackageManager = %q, want %q", info.PackageManager, c.want)
			}
		})
	}
}

func TestDetectProject_Unknown(t *testing.T) {
	dir := t.TempDir()
	info := DetectProject(dir)
	if info.Type != ProjectTypeUnknown {
		t.Errorf("Type = %q, want %q", info.Type, ProjectTypeUnknown)
	}
}

// --- Docker detection ---

func TestDetectProject_Docker(t *testing.T) {
	dir := t.TempDir()
	dockerfile := `FROM golang:1.25 AS builder
COPY . .
RUN go build -o /app .

FROM alpine:latest
COPY --from=builder /app /app
`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)
	if !info.HasDocker {
		t.Errorf("HasDocker = false, want true")
	}
	if info.DockerBaseImage != "golang:1.25" {
		t.Errorf("DockerBaseImage = %q, want golang:1.25", info.DockerBaseImage)
	}
}

func TestDetectProject_DockerCompose(t *testing.T) {
	dir := t.TempDir()
	compose := `version: "3"
services:
  web:
    image: nginx
    ports:
      - "80:80"
  db:
    image: postgres
    environment:
      POSTGRES_PASSWORD: secret
volumes:
  data:
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)
	if !info.HasDockerCompose {
		t.Errorf("HasDockerCompose = false, want true")
	}
	if info.ComposeFile != "docker-compose.yml" {
		t.Errorf("ComposeFile = %q, want docker-compose.yml", info.ComposeFile)
	}
	// Should detect web and db services
	hasWeb, hasDB := false, false
	for _, s := range info.DockerServices {
		if s == "web" {
			hasWeb = true
		}
		if s == "db" {
			hasDB = true
		}
	}
	if !hasWeb || !hasDB {
		t.Errorf("expected web and db in services: %v", info.DockerServices)
	}
}

func TestDetectProject_ComposeYamlVariant(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  api:\n    image: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info := DetectProject(dir)
	if !info.HasDockerCompose {
		t.Errorf("HasDockerCompose = false for compose.yaml")
	}
	if info.ComposeFile != "compose.yaml" {
		t.Errorf("ComposeFile = %q, want compose.yaml", info.ComposeFile)
	}
}

// --- extractDockerBaseImage ---

func TestExtractDockerBaseImage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("# comment\nFROM node:18-alpine\nRUN npm install\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if img := extractDockerBaseImage(p); img != "node:18-alpine" {
		t.Errorf("extractDockerBaseImage = %q, want node:18-alpine", img)
	}
}

// TestExtractDockerBaseImage_PlatformFlag pins the v0.100.73 #14 fix: the common
// multi-arch form `FROM --platform=$BUILDPLATFORM golang:1.25 AS build` must
// yield the image, not the flag token.
func TestExtractDockerBaseImage_PlatformFlag(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("FROM --platform=$BUILDPLATFORM golang:1.25 AS build\nRUN go build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if img := extractDockerBaseImage(p); img != "golang:1.25" {
		t.Errorf("extractDockerBaseImage = %q, want golang:1.25 (flag token skipped)", img)
	}
}

func TestExtractDockerBaseImage_NoFrom(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("# no from here\nRUN echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if img := extractDockerBaseImage(p); img != "" {
		t.Errorf("extractDockerBaseImage = %q, want empty", img)
	}
}

func TestExtractDockerBaseImage_NonExistent(t *testing.T) {
	if img := extractDockerBaseImage("/nonexistent/Dockerfile"); img != "" {
		t.Errorf("extractDockerBaseImage on missing file = %q, want empty", img)
	}
}

// --- extractComposeServices ---

func TestExtractComposeServices_SkipsMergeKeysAndVars(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "compose.yml")
	compose := `services:
  web:
    image: nginx
  db:
    image: postgres
`
	if err := os.WriteFile(p, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	services := extractComposeServices(p)
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2: %v", len(services), services)
	}
}

func TestExtractComposeServices_NonExistent(t *testing.T) {
	if s := extractComposeServices("/nonexistent/compose.yml"); s != nil {
		t.Errorf("extractComposeServices on missing file = %v, want nil", s)
	}
}

// --- countIndent ---

func TestCountIndent(t *testing.T) {
	cases := []struct {
		line string
		want int
	}{
		{"no indent", 0},
		{"  two spaces", 2},
		{"    four spaces", 4},
		{"\ttab", 2},
		{"\t\ttwo tabs", 4},
		{"  \tmixed", 4},
		{"", 0},
	}
	for _, c := range cases {
		if got := countIndent(c.line); got != c.want {
			t.Errorf("countIndent(%q) = %d, want %d", c.line, got, c.want)
		}
	}
}

// --- findFiles ---

func TestFindFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "target.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Unrelated file
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := findFiles(dir, "target.txt")
	if len(files) != 3 {
		t.Errorf("found %d files, want 3: %v", len(files), files)
	}
}

func TestFindFiles_SkipsVendorDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor", "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := findFiles(dir, "main.go")
	if len(files) != 1 {
		t.Errorf("found %d files, want 1 (vendor skipped): %v", len(files), files)
	}
}

func TestFindFiles_None(t *testing.T) {
	dir := t.TempDir()
	files := findFiles(dir, "nonexistent.go")
	if len(files) != 0 {
		t.Errorf("found %d files, want 0: %v", len(files), files)
	}
}

// --- isKeyDependency ---

func TestIsKeyDependency(t *testing.T) {
	keyDeps := []string{"react", "vue", "angular", "svelte", "next", "nuxt", "express", "fastify", "nestjs", "typescript", "vite", "webpack"}
	for _, d := range keyDeps {
		if !isKeyDependency(d) {
			t.Errorf("isKeyDependency(%q) = false, want true", d)
		}
	}
	nonKey := []string{"lodash", "axios", "moment", "", "react-dom"}
	for _, d := range nonKey {
		if isKeyDependency(d) {
			t.Errorf("isKeyDependency(%q) = true, want false", d)
		}
	}
}

// --- ProjectType.String ---

func TestProjectTypeString(t *testing.T) {
	cases := []struct {
		t    ProjectType
		want string
	}{
		{ProjectTypeGo, "Go"},
		{ProjectTypeNode, "Node.js"},
		{ProjectTypeRust, "Rust"},
		{ProjectTypePython, "Python"},
		{ProjectTypeJava, "Java"},
		{ProjectTypeRuby, "Ruby"},
		{ProjectTypePHP, "PHP"},
		{ProjectTypeUnknown, "Unknown"},
	}
	for _, c := range cases {
		if got := c.t.String(); got != c.want {
			t.Errorf("%q.String() = %q, want %q", c.t, got, c.want)
		}
	}
}
