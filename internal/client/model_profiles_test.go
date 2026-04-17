package client

import (
	"testing"
)

func TestGetModelProfile_ExactMatch(t *testing.T) {
	tests := []struct {
		model      string
		wantFamily string
		wantCtx    int
		wantTools  bool
	}{
		{"llama3.2", "llama", 128000, true},
		{"claude-sonnet", "anthropic", 200000, true},
		{"gemini-3-flash", "gemini", 1000000, true},
		{"deepseek-chat", "deepseek", 64000, true},
		{"glm-5", "glm", 128000, true},
		{"kimi-k2.5", "kimi", 256000, true},
		{"minimax-m2.5", "minimax", 204800, true},
		{"phi3", "phi", 4096, false},
		{"llama2", "llama", 4096, false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			p := GetModelProfile(tt.model)
			if p.Family != tt.wantFamily {
				t.Errorf("Family = %q, want %q", p.Family, tt.wantFamily)
			}
			if p.ContextWindow != tt.wantCtx {
				t.Errorf("ContextWindow = %d, want %d", p.ContextWindow, tt.wantCtx)
			}
			if p.SupportsTools != tt.wantTools {
				t.Errorf("SupportsTools = %v, want %v", p.SupportsTools, tt.wantTools)
			}
		})
	}
}

func TestGetModelProfile_CaseInsensitive(t *testing.T) {
	p := GetModelProfile("LLAMA3.2")
	if p.Family != "llama" {
		t.Errorf("uppercase LLAMA3.2: Family = %q, want llama", p.Family)
	}

	p = GetModelProfile("Claude-Sonnet")
	if p.Family != "anthropic" {
		t.Errorf("mixed case Claude-Sonnet: Family = %q, want anthropic", p.Family)
	}

}

func TestGetModelProfile_TagStripping(t *testing.T) {
	// :latest, :7b etc. should be stripped
	p := GetModelProfile("llama3.2:latest")
	if p.Family != "llama" {
		t.Errorf("llama3.2:latest Family = %q, want llama", p.Family)
	}

	p = GetModelProfile("mistral:7b-instruct")
	if p.Family != "mistral" {
		t.Errorf("mistral:7b-instruct Family = %q, want mistral", p.Family)
	}
}

func TestGetModelProfile_SmallByTag(t *testing.T) {
	// llama3.1 is NOT small by default, but with :7b tag it should be
	p := GetModelProfile("llama3.1:7b")
	if !p.IsSmall {
		t.Error("llama3.1:7b should be IsSmall")
	}

	// llama3.1 without tag is NOT small
	p = GetModelProfile("llama3.1")
	if p.IsSmall {
		t.Error("llama3.1 should not be IsSmall")
	}

	// Already small + tag doesn't change
	p = GetModelProfile("llama3.2:3b")
	if !p.IsSmall {
		t.Error("llama3.2:3b should be IsSmall")
	}
}

func TestGetModelProfile_LongestPrefixMatch(t *testing.T) {
	// "qwen2.5-coder" should match more specific than "qwen2.5" or "qwen"
	p := GetModelProfile("qwen2.5-coder-32b")
	if p.Family != "qwen" {
		t.Errorf("Family = %q, want qwen", p.Family)
	}
	if !p.IsCoding {
		t.Error("qwen2.5-coder-32b should match qwen2.5-coder (IsCoding)")
	}
}

func TestGetModelProfile_Unknown(t *testing.T) {
	p := GetModelProfile("totally-unknown-model")
	if p.Family != "unknown" {
		t.Errorf("unknown model Family = %q, want unknown", p.Family)
	}
	if p.ContextWindow != 4096 {
		t.Errorf("unknown model ContextWindow = %d, want 4096", p.ContextWindow)
	}
	if p.SupportsTools {
		t.Error("unknown model should not support tools")
	}
	if !p.IsSmall {
		t.Error("unknown model should be IsSmall")
	}
}

func TestGetModelProfile_IsCoding(t *testing.T) {
	codingModels := []string{
		"qwen2.5-coder", "codellama", "starcoder2", "deepseek-coder",
		"claude-opus", "claude-sonnet", "deepseek-chat",
		"glm-5", "glm-4.7", "kimi-k2.5", "kimi-k2",
		"gemini-3.1-pro", "gemini-3-pro", "gemini-2.5-pro",
		"minimax-m2.7", "minimax-m2.5",
	}
	for _, m := range codingModels {
		p := GetModelProfile(m)
		if !p.IsCoding {
			t.Errorf("%q should be IsCoding", m)
		}
	}
}

func TestIsSmallByTag(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"llama3.2:7b", true},
		{"llama3.2:3b", true},
		{"llama3.2:1b", true},
		{"llama3.2:8b", true},
		{"llama3.2:70b", false},
		{"llama3.2:latest", false},
		{"model-7b-instruct", true},
		{"model-13b", false},
		{"plain-model", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := isSmallByTag(tt.model)
			if got != tt.want {
				t.Errorf("isSmallByTag(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestKnownModelProfilesCount(t *testing.T) {
	if len(knownModelProfiles) < 45 {
		t.Errorf("knownModelProfiles has %d entries, expected at least 45", len(knownModelProfiles))
	}
}
