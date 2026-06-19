package commands

import (
	"context"
	"strings"
	"testing"
)

// fakeAppForAddDir records grant/revoke calls. Embeds fakeAppForMCP for the rest
// of the AppInterface surface.
type fakeAppForAddDir struct {
	fakeAppForMCP
	grantedPath   string
	grantPersist  bool
	revokedPath   string
	revokeReturns bool
	listReturns   []string
	grantErr      error
}

func (f *fakeAppForAddDir) GrantAllowedDir(path string, persist bool) (string, error) {
	f.grantedPath = path
	f.grantPersist = persist
	if f.grantErr != nil {
		return "", f.grantErr
	}
	return "/resolved/" + path, nil
}
func (f *fakeAppForAddDir) RevokeGrantedDir(path string) (bool, error) {
	f.revokedPath = path
	return f.revokeReturns, nil
}
func (f *fakeAppForAddDir) ListAllowedDirs() []string { return f.listReturns }
func (f *fakeAppForAddDir) GetWorkDir() string        { return "/work" }

func TestAddDirCommand_GrantSession(t *testing.T) {
	app := &fakeAppForAddDir{}
	out, err := (&AddDirCommand{}).Execute(context.Background(), []string{"/some/dir"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if app.grantedPath != "/some/dir" || app.grantPersist {
		t.Errorf("expected session grant of /some/dir, got path=%q persist=%v", app.grantedPath, app.grantPersist)
	}
	if !strings.Contains(out, "/resolved//some/dir") {
		t.Errorf("output should confirm the resolved dir: %s", out)
	}
}

func TestAddDirCommand_Persist(t *testing.T) {
	app := &fakeAppForAddDir{}
	if _, err := (&AddDirCommand{}).Execute(context.Background(), []string{"--persist", "/some/dir"}, app); err != nil {
		t.Fatal(err)
	}
	if !app.grantPersist {
		t.Error("--persist should set persist=true")
	}
	if app.grantedPath != "/some/dir" {
		t.Errorf("persist flag must be stripped from the path, got %q", app.grantedPath)
	}
}

func TestAddDirCommand_NoArgsLists(t *testing.T) {
	app := &fakeAppForAddDir{listReturns: []string{"/a (session)", "/b (persisted)"}}
	out, err := (&AddDirCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatal(err)
	}
	if app.grantedPath != "" {
		t.Error("no-args invocation must not grant anything")
	}
	if !strings.Contains(out, "/a (session)") || !strings.Contains(out, "/work") {
		t.Errorf("list output should show dirs and workspace: %s", out)
	}
}

func TestRemoveDirCommand(t *testing.T) {
	app := &fakeAppForAddDir{revokeReturns: true}
	out, err := (&RemoveDirCommand{}).Execute(context.Background(), []string{"/some/dir"}, app)
	if err != nil {
		t.Fatal(err)
	}
	if app.revokedPath != "/some/dir" {
		t.Errorf("revoke path = %q", app.revokedPath)
	}
	if !strings.Contains(out, "Revoked") {
		t.Errorf("output should confirm revoke: %s", out)
	}

	// Revoking a path that was never granted reports not-found (no error).
	app2 := &fakeAppForAddDir{revokeReturns: false, listReturns: nil}
	out2, err := (&RemoveDirCommand{}).Execute(context.Background(), []string{"/unknown"}, app2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "No session grant") {
		t.Errorf("output should report not-found: %s", out2)
	}
}

func TestRemoveDirCommand_RequiresPath(t *testing.T) {
	if _, err := (&RemoveDirCommand{}).Execute(context.Background(), nil, &fakeAppForAddDir{}); err == nil {
		t.Error("remove-dir with no path should error")
	}
}
