package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func newGrantTestApp(t *testing.T) (*App, string, string) {
	t.Helper()
	workDir := testkit.ResolvedTempDir(t)
	external := testkit.ResolvedTempDir(t)
	a := &App{
		workDir:  workDir,
		config:   config.DefaultConfig(),
		registry: tools.DefaultRegistry(workDir),
	}
	return a, workDir, external
}

func TestApp_GrantAllowedDir_SessionGrantPropagatesAndResets(t *testing.T) {
	a, _, external := newGrantTestApp(t)
	extFile := filepath.Join(external, "f.txt")
	if err := os.WriteFile(extFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	if a.isDirAccessAllowed(extFile) {
		t.Fatal("external path must be denied before any grant")
	}

	resolved, err := a.GrantAllowedDir(external, false)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if resolved == "" {
		t.Fatal("grant should return the resolved dir")
	}
	if !a.isDirAccessAllowed(extFile) {
		t.Error("external path must be allowed after grant")
	}
	if got := a.ListAllowedDirs(); len(got) != 1 {
		t.Errorf("ListAllowedDirs = %v, want one session grant", got)
	}

	// The grant must NOT have been persisted to config (session-only).
	if len(a.config.Tools.AllowedDirs) != 0 {
		t.Errorf("session grant must not touch config, got %v", a.config.Tools.AllowedDirs)
	}

	// /clear-style reset revokes it.
	a.resetGrantedDirs()
	if a.isDirAccessAllowed(extFile) {
		t.Error("external path must be denied again after reset")
	}
}

func TestApp_GrantAllowedDir_RefusesUngrantable(t *testing.T) {
	a, _, _ := newGrantTestApp(t)
	for _, bad := range []string{"/etc", "/"} {
		if _, err := a.GrantAllowedDir(bad, false); err == nil {
			t.Errorf("GrantAllowedDir(%q) should be refused", bad)
		}
	}
	if _, err := a.GrantAllowedDir(filepath.Join(testkit.ResolvedTempDir(t), "does-not-exist"), false); err == nil {
		t.Error("granting a non-existent path should error")
	}
}

func TestApp_RevokeGrantedDir(t *testing.T) {
	a, _, external := newGrantTestApp(t)
	if _, err := a.GrantAllowedDir(external, false); err != nil {
		t.Fatal(err)
	}
	removed, err := a.RevokeGrantedDir(external)
	if err != nil || !removed {
		t.Fatalf("revoke should remove the grant: removed=%v err=%v", removed, err)
	}
	if a.isDirAccessAllowed(filepath.Join(external, "f.txt")) {
		t.Error("path must be denied after revoke")
	}
	// Revoking again is a no-op (not an error).
	removed, err = a.RevokeGrantedDir(external)
	if err != nil || removed {
		t.Errorf("second revoke should report not-removed, no error: removed=%v err=%v", removed, err)
	}
}

func TestApp_DirGrantPrompt_HeadlessRequiresExplicitGrant(t *testing.T) {
	a, _, external := newGrantTestApp(t)
	// a.program == nil: there is nobody who can approve new filesystem scope.
	allowed, err := a.dirGrantPrompt(context.Background(), "read", filepath.Join(external, "new.txt"))
	if err == nil || !strings.Contains(err.Error(), "--add-dir") {
		t.Fatalf("headless denial should explain explicit grant: %v", err)
	}
	if allowed {
		t.Fatal("headless must not create ambient filesystem authority")
	}
	if a.isDirAccessAllowed(filepath.Join(external, "new.txt")) {
		t.Error("denied directory must not appear in the session grants")
	}
}

func TestApp_DirectoryAccessContext(t *testing.T) {
	a, _, external := newGrantTestApp(t)

	// Before any grant: the capability hint is ALWAYS present (the model must
	// learn it can request /add-dir before it ever hits a denial), but no
	// granted-dirs list.
	ctx := a.directoryAccessContext()
	if !strings.Contains(ctx, "/add-dir") {
		t.Errorf("capability hint must always mention /add-dir: %q", ctx)
	}
	if strings.Contains(ctx, external) {
		t.Errorf("no grant yet, but context lists %s", external)
	}

	// After granting: the dir appears in the context so the model knows it is
	// available.
	if _, err := a.GrantAllowedDir(external, false); err != nil {
		t.Fatal(err)
	}
	ctx = a.directoryAccessContext()
	if !strings.Contains(ctx, external) {
		t.Errorf("granted dir should appear in directory-access context: %q", ctx)
	}
	if !strings.Contains(ctx, "/add-dir") {
		t.Errorf("capability hint should still be present after a grant: %q", ctx)
	}

	// The block must be part of the full turn context too.
	if !strings.Contains(a.turnContextContent(), "Directory access") {
		t.Error("turnContextContent must include the directory-access block")
	}
}

func TestApp_DirGrantPrompt_RefusesUngrantableWithoutPrompt(t *testing.T) {
	a, _, _ := newGrantTestApp(t)
	// /etc is non-grantable: headless must NOT auto-grant it.
	allowed, err := a.dirGrantPrompt(context.Background(), "read", "/etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Error("an ungrantable location must never be granted, even headless")
	}
}
