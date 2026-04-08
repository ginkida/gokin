package tools

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
)

// DockerComposeValidator checks docker-compose files for common issues.
type DockerComposeValidator struct{}

func (v *DockerComposeValidator) Name() string { return "docker_compose" }

func (v *DockerComposeValidator) Matches(filePath string) bool {
	base := strings.ToLower(filepath.Base(filePath))
	return base == "docker-compose.yml" || base == "docker-compose.yaml" ||
		base == "compose.yml" || base == "compose.yaml"
}

var (
	composeImageLatest = regexp.MustCompile(`(?i)^\s+image:\s*\S+:latest\s*$`)
	composeImageNoTag  = regexp.MustCompile(`(?i)^\s+image:\s*([a-z][a-z0-9._/-]+)\s*$`)
	composeImageTagged = regexp.MustCompile(`(?i)^\s+image:\s*\S+:\S+`)
	composePrivPort    = regexp.MustCompile(`(?i)^\s+(?:-\s+)?["']?(\d+):(\d+)`)
)

func (v *DockerComposeValidator) Validate(_ context.Context, filePath string, content []byte, _ string) []ValidationWarning {
	var warnings []ValidationWarning
	baseName := filepath.Base(filePath)
	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// image: something:latest — unpinned
		if composeImageLatest.MatchString(line) {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      i + 1,
				Message:   "image uses :latest tag — pin to a specific version for reproducible deployments",
			})
		}

		// image: something (no tag)
		if strings.Contains(trimmed, "image:") &&
			composeImageNoTag.MatchString(line) &&
			!composeImageTagged.MatchString(line) &&
			!strings.Contains(trimmed, "${") &&
			!strings.Contains(trimmed, "scratch") {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "warning",
				File:      baseName,
				Line:      i + 1,
				Message:   "image without version tag — defaults to :latest, pin to a specific version",
			})
		}

		// Privileged ports (host port < 1024) — may need root
		if matches := composePrivPort.FindStringSubmatch(line); len(matches) >= 2 {
			port := matches[1]
			if len(port) <= 3 && port != "0" {
				// Port under 1024 (rough check: 1-3 digits and not 0)
				portNum := 0
				for _, ch := range port {
					portNum = portNum*10 + int(ch-'0')
				}
				if portNum > 0 && portNum < 1024 {
					warnings = append(warnings, ValidationWarning{
						Validator: v.Name(),
						Severity:  "info",
						File:      baseName,
						Line:      i + 1,
						Message:   "binding to privileged port " + port + " — may require root or elevated permissions",
					})
				}
			}
		}

		// container_name: hardcoded — prevents scaling
		if strings.HasPrefix(trimmed, "container_name:") {
			warnings = append(warnings, ValidationWarning{
				Validator: v.Name(),
				Severity:  "info",
				File:      baseName,
				Line:      i + 1,
				Message:   "hardcoded container_name prevents running multiple instances (docker compose up --scale)",
			})
		}
	}

	return warnings
}
