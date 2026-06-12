package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func writeHooksFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFile_DefaultsEnabledAndParses(t *testing.T) {
	path := writeHooksFile(t, `
hooks:
  - name: fmt-after-edit
    type: post_tool
    tool_name: edit
    command: gofmt -w ${FILE_PATH}
  - name: off-hook
    type: pre_tool
    command: echo no
    enabled: false
`)
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("hooks = %d, want 2", len(loaded))
	}
	if !loaded[0].Enabled {
		t.Fatal("hooks in a standalone file must default to enabled — zero-value false would make every project hook a silent no-op")
	}
	if loaded[1].Enabled {
		t.Fatal("explicit enabled:false must be honored")
	}
	if loaded[0].Type != PostTool || loaded[0].ToolName != "edit" {
		t.Fatalf("hook 0 parsed wrong: %+v", loaded[0])
	}
}

func TestLoadFile_MissingFileIsFine(t *testing.T) {
	loaded, err := LoadFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || loaded != nil {
		t.Fatalf("missing file: loaded=%v err=%v, want nil/nil", loaded, err)
	}
}

func TestLoadFile_RejectsUnknownTypeAndMissingCommand(t *testing.T) {
	if _, err := LoadFile(writeHooksFile(t, "hooks:\n  - name: x\n    type: pre_tool_typo\n    command: echo hi\n")); err == nil {
		t.Fatal("unknown type must fail loudly")
	}
	if _, err := LoadFile(writeHooksFile(t, "hooks:\n  - name: x\n    type: pre_tool\n")); err == nil {
		t.Fatal("missing command must fail loudly")
	}
	if _, err := LoadFile(writeHooksFile(t, "hooks: [not yaml")); err == nil {
		t.Fatal("broken yaml must fail loudly")
	}
}

func TestLoadFile_StopTypeAccepted(t *testing.T) {
	loaded, err := LoadFile(writeHooksFile(t, "hooks:\n  - name: gate\n    type: stop\n    command: ./check.sh\n    fail_on_error: true\n"))
	if err != nil || len(loaded) != 1 || loaded[0].Type != Stop || !loaded[0].FailOnError {
		t.Fatalf("stop hook: %+v err=%v", loaded, err)
	}
}
