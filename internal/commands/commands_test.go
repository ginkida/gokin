package commands

import "testing"

func TestHandlerParse(t *testing.T) {
	h := NewHandler()

	tests := []struct {
		input     string
		wantName  string
		wantIsCmd bool
		wantArgs  []string
	}{
		// Known commands
		{"/help", "help", true, nil},
		{"/clear", "clear", true, nil},
		{"/doctor", "doctor", true, nil},
		{"/help some-topic", "help", true, []string{"some-topic"}},
		{"/model gemini-flash", "model", true, []string{"gemini-flash"}},

		// Unknown commands (paths, typos)
		{"/home/user/file.go", "", false, nil},
		{"/nonexistent", "", false, nil},
		{"hello", "", false, nil},
		{"", "", false, nil},
		{"  /help  ", "help", true, nil}, // trimmed

		// Not commands
		{"just a message", "", false, nil},
		{"/ no command", "", false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			name, args, isCmd := h.Parse(tt.input)
			if isCmd != tt.wantIsCmd {
				t.Errorf("Parse(%q) isCmd = %v, want %v", tt.input, isCmd, tt.wantIsCmd)
			}
			if name != tt.wantName {
				t.Errorf("Parse(%q) name = %q, want %q", tt.input, name, tt.wantName)
			}
			if tt.wantArgs != nil && len(args) != len(tt.wantArgs) {
				t.Errorf("Parse(%q) args = %v, want %v", tt.input, args, tt.wantArgs)
			}
		})
	}
}

func TestHandlerGetCommand(t *testing.T) {
	h := NewHandler()

	// Known command
	cmd, ok := h.GetCommand("help")
	if !ok || cmd == nil {
		t.Error("GetCommand(help) should return a command")
	}
	if cmd.Name() != "help" {
		t.Errorf("GetCommand(help).Name() = %q", cmd.Name())
	}

	// Unknown command
	_, ok = h.GetCommand("nonexistent")
	if ok {
		t.Error("GetCommand(nonexistent) should return false")
	}
}

func TestHandlerListCommands(t *testing.T) {
	h := NewHandler()
	cmds := h.ListCommands()

	if len(cmds) < 30 {
		t.Errorf("ListCommands() returned %d commands, expected at least 30", len(cmds))
	}

	// Verify essential commands exist. This list is the safety net
	// against accidental Register() drops during refactors — every
	// name here should round-trip through NewHandler. When you add a
	// new slash command that ships in a release, add it here too.
	essential := []string{
		// core session
		"help", "clear", "doctor", "config", "model", "plan", "stats",
		// auth + setup
		"login", "logout", "provider", "status",
		// git inspect / action set (Sprint 9 → v0.77.x)
		"commit", "pr", "diff", "log", "branches", "grep",
		// update / release feedback loop (v0.74.x → v0.76.x)
		"update", "restart", "whats-new", "changelog",
	}
	cmdNames := make(map[string]bool)
	for _, c := range cmds {
		cmdNames[c.Name()] = true
	}
	for _, name := range essential {
		if !cmdNames[name] {
			t.Errorf("essential command %q not found in ListCommands()", name)
		}
	}
}

// TestHelpCategoriesAreRegistered guards against the silent-drop bug
// where a command name appears in the /help category list but has no
// matching Register() call — that combo renders an empty category
// without warning. Cross-references the hard-coded category list in
// HelpCommand.Execute against the real handler.
func TestHelpCategoriesAreRegistered(t *testing.T) {
	h := NewHandler()
	registered := make(map[string]bool)
	for _, c := range h.ListCommands() {
		registered[c.Name()] = true
	}

	// Mirror of the categories defined in builtin.go HelpCommand.Execute.
	// Keep in sync — this is the whole point of the test.
	helpCategories := map[string][]string{
		"Getting Started": {"help", "quickstart"},
		"Session":         {"model", "clear", "compact", "save", "resume", "sessions", "stats", "instructions"},
		"Auth & Setup":    {"login", "logout", "provider", "status", "doctor", "config", "update", "restart", "whats-new", "changelog"},
		"Git":             {"init", "commit", "pr", "diff", "log", "branches", "grep"},
		"Planning":        {"plan", "resume-plan", "health", "policy", "ledger", "plan-proof", "journal", "recovery", "observability", "memory-governance", "tree-stats"},
		"Tools": {"browse", "open", "copy", "paste", "clear-todos", "ql", "permissions", "sandbox", "theme",
			"register-agent-type", "list-agent-types", "unregister-agent-type"},
	}

	for cat, names := range helpCategories {
		for _, name := range names {
			if !registered[name] {
				t.Errorf("/help category %q lists %q but it is not registered in NewHandler", cat, name)
			}
		}
	}
}

func TestHandlerRegisterPanicAfterFreeze(t *testing.T) {
	h := NewHandler() // frozen after construction

	defer func() {
		if r := recover(); r == nil {
			t.Error("Register after freeze should panic")
		}
	}()

	h.Register(&ClearCommand{})
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"help", "hlep", 2},
		{"doctor", "doctr", 1},
		{"clear", "clean", 1},
		{"config", "confg", 1},
	}

	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestFindClosestCommand(t *testing.T) {
	h := NewHandler()

	// Close typos should be found
	if closest := h.findClosestCommand("hlep"); closest != "help" {
		t.Errorf("findClosestCommand(hlep) = %q, want %q", closest, "help")
	}

	// Too far away should return empty
	if closest := h.findClosestCommand("zzzzzzz"); closest != "" {
		t.Errorf("findClosestCommand(zzzzzzz) = %q, want empty", closest)
	}
}

func TestCommandMetadata(t *testing.T) {
	h := NewHandler()

	// DoctorCommand should have metadata
	cmd, ok := h.GetCommand("doctor")
	if !ok {
		t.Fatal("doctor command not found")
	}

	meta := h.getCommandMetadata(cmd)
	if meta.Category == "" {
		t.Error("doctor should have a category")
	}
}

func TestPaletteContext(t *testing.T) {
	h := NewHandler()

	ctx := PaletteContext{
		IsGitRepo: false,
		HasAPIKey: true,
		Platform:  "darwin",
	}

	// Git-requiring commands should be disabled when not in git repo
	state := h.GetCommandState("commit", ctx)
	if state.Enabled {
		t.Error("commit should be disabled when not in git repo")
	}

	// Non-git commands should be enabled
	state = h.GetCommandState("help", ctx)
	if !state.Enabled {
		t.Error("help should always be enabled")
	}

	// Unknown command
	state = h.GetCommandState("nonexistent", ctx)
	if state.Enabled {
		t.Error("nonexistent should be disabled")
	}
}
