package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadClientConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	original := &ClientConfig{
		HubURL:      "http://localhost:8080",
		MemberID:    "mem_test",
		MemberName:  "Test User",
		Token:       "ti_mem_12345",
		MachineName: "test-box",
		Workspaces: []WorkspaceConfig{
			{ID: "ws1", Name: "app", RootPath: tempDir},
		},
		LLM: LLMConfig{
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			APIKey:   "sk-test",
			Model:    "gpt-4o",
		},
		MaxConcurrency:       2,
		HeartbeatIntervalSec: 20,
	}

	if err := SaveClientConfig(configPath, original); err != nil {
		t.Fatalf("SaveClientConfig failed: %v", err)
	}

	// Verify file exists
	fi, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatalf("config file is empty")
	}

	loaded, err := LoadClientConfig(configPath)
	if err != nil {
		t.Fatalf("LoadClientConfig failed: %v", err)
	}

	if loaded.MemberID != original.MemberID {
		t.Errorf("expected member ID %s, got %s", original.MemberID, loaded.MemberID)
	}
	if loaded.LLM.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", loaded.LLM.Model)
	}
	if len(loaded.Workspaces) != 1 {
		t.Errorf("expected 1 workspace, got %d", len(loaded.Workspaces))
	}
}
