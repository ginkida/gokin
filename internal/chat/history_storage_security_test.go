package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newIsolatedHistoryManager(t *testing.T) *HistoryManager {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	m, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	return m
}

func TestValidateSessionIDPortableContract(t *testing.T) {
	invalid := []string{
		"", ".", "..", "-option", "a/b", `a\b`, "a:b", "a?b", "a*b",
		"a<b", "a>b", `a"b`, "a|b", "two words", "line\nbreak", "trailing.",
		"CON", "con.backup", "PRN", "NUL", "COM1", "lpt9.txt",
		strings.Repeat("я", MaxSessionIDRunes+1), strings.Repeat("界", maxSessionIDBytes/3+1),
		string([]byte{'b', 'a', 'd', 0xff}),
	}
	for _, id := range invalid {
		if err := ValidateSessionID(id); err == nil {
			t.Errorf("ValidateSessionID(%q) accepted an unsafe ID", id)
		}
	}
	valid := []string{"20260716-120000-a1b2c3", "release.v2", "checkpoint_два", ".local-save"}
	for _, id := range valid {
		if err := ValidateSessionID(id); err != nil {
			t.Errorf("ValidateSessionID(%q): %v", id, err)
		}
	}
}

func TestHistoryManagerRejectsNilAndEmbeddedIdentityMismatch(t *testing.T) {
	m := newIsolatedHistoryManager(t)
	if err := m.Save(nil); err == nil {
		t.Fatal("Save(nil) succeeded")
	}
	if err := m.SaveFull(nil); err == nil {
		t.Fatal("SaveFull(nil) succeeded")
	}

	legacyPath := filepath.Join(m.dataDir, "requested.json")
	data, err := json.Marshal(HistoryFile{SessionID: "different"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load("requested"); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("Load identity error = %v", err)
	}
}

func TestHistoryManagerRefusesSymlinkAndNonRegularSessionFiles(t *testing.T) {
	m := newIsolatedHistoryManager(t)
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(sessionsDir); err != nil {
		t.Fatal(err)
	}

	external := filepath.Join(t.TempDir(), "external.json")
	externalData, err := json.Marshal(SessionState{ID: "linked"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, externalData, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sessionsDir, "linked.json")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := m.LoadFull("linked"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("LoadFull symlink error = %v", err)
	}
	linkedSession := NewSession()
	linkedSession.SetID("linked")
	if err := m.SaveFull(linkedSession); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("SaveFull symlink error = %v", err)
	}
	if err := m.DeleteSession("linked"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("DeleteSession symlink error = %v", err)
	}
	if got, err := os.ReadFile(external); err != nil || !reflect.DeepEqual(got, externalData) {
		t.Fatalf("external symlink target changed: data=%q err=%v", got, err)
	}

	if err := os.Mkdir(filepath.Join(sessionsDir, "special.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadFull("special"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("LoadFull directory error = %v", err)
	}
	listed, err := m.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("ListSessions exposed non-regular entries: %+v", listed)
	}
}

func TestHistoryManagerRejectsSymlinkStorageDirectoryBeforeChmod(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	gokinDir := filepath.Join(xdg, "gokin")
	if err := os.MkdirAll(gokinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Chmod(external, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(gokinDir, "history")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := NewHistoryManager(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("NewHistoryManager symlink-dir error = %v", err)
	}
	info, err := os.Stat(external)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("symlink target mode changed to %#o", got)
	}
}

func TestHistoryManagerBoundsReadsAndWritesSymmetrically(t *testing.T) {
	m := newIsolatedHistoryManager(t)
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(sessionsDir); err != nil {
		t.Fatal(err)
	}

	oversizedPath := filepath.Join(sessionsDir, "oversized.json")
	f, err := os.OpenFile(oversizedPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxSessionFileBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadFull("oversized"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("LoadFull oversized error = %v", err)
	}

	original := NewSession()
	original.SetID("too-large")
	original.AddUserMessage("last known good state")
	if err := m.SaveFull(original); err != nil {
		t.Fatal(err)
	}
	tooLarge := NewSession()
	tooLarge.SetID("too-large")
	tooLarge.SetScratchpad(strings.Repeat("x", int(maxSessionFileBytes)+1))
	if err := m.SaveFull(tooLarge); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("SaveFull oversized error = %v", err)
	}
	loaded, err := m.LoadFull("too-large")
	if err != nil {
		t.Fatalf("oversized replacement damaged last good state: %v", err)
	}
	if len(loaded.History) != 1 || loaded.History[0].Parts[0].Text != "last known good state" {
		t.Fatalf("oversized replacement changed last good state: %+v", loaded.History)
	}
}

func TestHistoryManagerStructuralPreflightMatchesSaveAndLoad(t *testing.T) {
	m := newIsolatedHistoryManager(t)
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(sessionsDir); err != nil {
		t.Fatal(err)
	}

	// Compact hostile JSON can otherwise expand into a very large typed slice.
	items := strings.Repeat(`{},`, maxJSONContainerEntries) + `{}`
	raw := fmt.Sprintf(`{"id":"too-many","history":[%s]}`, items)
	if err := os.WriteFile(filepath.Join(sessionsDir, "too-many.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadFull("too-many"); err == nil || !strings.Contains(err.Error(), "array contains more") {
		t.Fatalf("LoadFull structural error = %v", err)
	}

	s := NewSession()
	s.SetID("too-many-checkpoints")
	s.Checkpoints = make(map[string]int, maxJSONContainerEntries+1)
	for i := 0; i <= maxJSONContainerEntries; i++ {
		s.Checkpoints[fmt.Sprintf("cp-%04d", i)] = 0
	}
	if err := m.SaveFull(s); err == nil || !strings.Contains(err.Error(), "object contains more") {
		t.Fatalf("SaveFull structural error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(sessionsDir, "too-many-checkpoints.json")); !os.IsNotExist(err) {
		t.Fatalf("structurally unsafe SaveFull published a file: %v", err)
	}
}

func TestHistoryManagerListsDeterministicallyAndSkipsCorruption(t *testing.T) {
	m := newIsolatedHistoryManager(t)
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(sessionsDir); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	states := []SessionState{
		{ID: "z-tie", LastActive: stamp},
		{ID: "older", LastActive: stamp.Add(-time.Hour)},
		{ID: "a-tie", LastActive: stamp},
		{ID: "newest", LastActive: stamp.Add(time.Hour)},
	}
	for _, state := range states {
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sessionsDir, state.ID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, "corrupt.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := m.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, info := range got {
		ids = append(ids, info.ID)
	}
	want := []string{"newest", "a-tie", "z-tie", "older"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ListSessions IDs = %v, want %v", ids, want)
	}

	for _, id := range []string{"z", "a", "m"} {
		data, err := json.Marshal(HistoryFile{SessionID: id})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(m.dataDir, id+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "m", "z"}; !reflect.DeepEqual(legacy, want) {
		t.Fatalf("List = %v, want %v", legacy, want)
	}
}
