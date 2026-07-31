package client

import (
	"testing"

	"gokin/internal/config"
)

func TestNewOllamaClient_CustomBaseURLOverridesPersistentURL(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.OllamaBaseURL = "http://persistent.example:11434"
	cfg.Model.CustomBaseURL = "http://127.0.0.1:18765"

	got, err := newOllamaClient(cfg, "mock-coder")
	if err != nil {
		t.Fatalf("newOllamaClient() error = %v", err)
	}
	ollama, ok := got.(*OllamaClient)
	if !ok {
		t.Fatalf("newOllamaClient() type = %T, want *OllamaClient", got)
	}
	if ollama.config.BaseURL != cfg.Model.CustomBaseURL {
		t.Fatalf("BaseURL = %q, want runtime override %q", ollama.config.BaseURL, cfg.Model.CustomBaseURL)
	}
}

func TestNewOllamaClient_PersistentBaseURLFallback(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.OllamaBaseURL = "http://persistent.example:11434"
	cfg.Model.CustomBaseURL = ""

	got, err := newOllamaClient(cfg, "mock-coder")
	if err != nil {
		t.Fatalf("newOllamaClient() error = %v", err)
	}
	ollama := got.(*OllamaClient)
	if ollama.config.BaseURL != cfg.API.OllamaBaseURL {
		t.Fatalf("BaseURL = %q, want persistent URL %q", ollama.config.BaseURL, cfg.API.OllamaBaseURL)
	}
}
