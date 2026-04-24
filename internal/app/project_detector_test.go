package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectContextIncludesProjectMap(t *testing.T) {
	dir := t.TempDir()
	writeProjectDetectorFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.23\n")
	writeProjectDetectorFile(t, filepath.Join(dir, "package.json"), `{
  "name": "example-app",
  "scripts": {
    "test": "go test ./...",
    "build": "go build ./cmd/api"
  },
  "dependencies": {"react": "latest"}
}`)
	writeProjectDetectorFile(t, filepath.Join(dir, "Dockerfile"), "FROM golang:1.23\n")
	writeProjectDetectorFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n")
	writeProjectDetectorFile(t, filepath.Join(dir, "internal", "service", "handler_test.go"), "package service\n")

	app := &App{workDir: dir}
	ctx := app.detectProjectContext()

	for _, needle := range []string{
		"Project map:",
		"Entrypoints: cmd/api/main.go",
		"Tests: internal/service/handler_test.go",
		"Configs:",
		"Dockerfile",
		"go.mod",
		"package.json",
		"Scripts:",
		"build=go build ./cmd/api",
		"test=go test ./...",
	} {
		if !strings.Contains(ctx, needle) {
			t.Fatalf("detectProjectContext() missing %q:\n%s", needle, ctx)
		}
	}
}

func TestBuildProjectMapSkipsHeavyDirs(t *testing.T) {
	dir := t.TempDir()
	writeProjectDetectorFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n")
	writeProjectDetectorFile(t, filepath.Join(dir, "vendor", "dep", "main.go"), "package main\n")
	writeProjectDetectorFile(t, filepath.Join(dir, "cmd", "api", "main.go"), "package main\n")

	app := &App{workDir: dir}
	projectMap := app.buildProjectMap()
	if strings.Contains(projectMap, "vendor/dep/main.go") {
		t.Fatalf("project map should skip vendor files:\n%s", projectMap)
	}
	if !strings.Contains(projectMap, "cmd/api/main.go") {
		t.Fatalf("project map missing real entrypoint:\n%s", projectMap)
	}
}

func writeProjectDetectorFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}
