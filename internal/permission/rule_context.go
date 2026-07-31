package permission

import (
	"context"
	"path/filepath"
	"strings"
)

type permissionWorkDirContextKey struct{}

// ContextWithWorkDir binds permission path rules to the exact workspace of the
// executing foreground or delegated agent.
func ContextWithWorkDir(ctx context.Context, workDir string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ctx
	}
	if absolute, err := filepath.Abs(workDir); err == nil {
		workDir = filepath.Clean(absolute)
	}
	return context.WithValue(ctx, permissionWorkDirContextKey{}, workDir)
}

// WorkDirFromContext returns the permission-rule workspace, if supplied.
func WorkDirFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	workDir, _ := ctx.Value(permissionWorkDirContextKey{}).(string)
	return workDir
}
