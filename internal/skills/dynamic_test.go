package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDynamicCommandsParseOnlyOriginalInjectionSyntax(t *testing.T) {
	body := "State: !`git status`\nKEY=!`literal`\nEscaped: \\!`literal`\n  ```!\nprintf 'one'\nprintf 'two'\n  ```\n~~~!\ngit diff\n~~~\n"
	commands, err := ParseDynamicCommands(body)
	if err != nil || len(commands) != 3 {
		t.Fatalf("commands=%+v err=%v", commands, err)
	}
	for i, want := range []string{"git status", "printf 'one'\nprintf 'two'", "git diff"} {
		if commands[i].Command != want || commands[i].End <= commands[i].Start {
			t.Fatalf("command %d = %+v", i, commands[i])
		}
	}
}

func TestDynamicCommandsValidateWholeTemplate(t *testing.T) {
	for _, body := range []string{
		"!`good` followed by !`unterminated",
		"!`line\nbreak`", "!` `", "```!\ncommand", "~~~!\n\n~~~",
		"```!bash\ncommand\n```",
		strings.Repeat("!`echo` ", MaxDynamicCommands+1),
	} {
		if commands, err := ParseDynamicCommands(body); err == nil {
			t.Fatalf("malformed template accepted: %q (%+v)", body, commands)
		}
	}
}

func TestCatalogDoesNotExecuteDynamicCommandsDuringDiscovery(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	writeSkill(t, root, "dynamic", "---\ndescription: Dynamic context\n---\nState: !`touch "+marker+"`")
	catalog := NewCatalog([]Root{{Path: root, Source: "claude-project"}})
	catalog.Reload()
	if _, ok := catalog.Get("dynamic"); !ok {
		t.Fatalf("skill not loaded: %v", catalog.Warnings())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("discovery executed command: %v", err)
	}
}
