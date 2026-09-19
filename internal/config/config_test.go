package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSaveAndLoadClientConfig_RoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")
	wsDir := filepath.Join(tempDir, "test-ws")
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		t.Fatalf("failed to create test workspace dir: %v", err)
	}

	caFile := filepath.Join(tempDir, "ca.pem")
	if err := os.WriteFile(caFile, []byte("-----BEGIN CERTIFICATE-----\nFAKE\n-----END CERTIFICATE-----"), 0644); err != nil {
		t.Fatalf("failed to write ca file: %v", err)
	}

	original := &ClientConfig{
		HubURL:      "http://127.0.0.1:8080",
		MemberID:    "mem_test_user",
		MemberName:  "Test Developer",
		Token:       "ti_mem_secret_token_1234567890",
		MachineName: "dev-laptop",
		Workspaces: []WorkspaceConfig{
			{
				ID:                "ws-app",
				Name:              "Main Application",
				RootPath:          wsDir,
				PrivacyPromptPath: filepath.Join(wsDir, "privacy.md"),
			},
		},
		LLM: LLMConfig{
			Provider:              "openai",
			BaseURL:               "https://api.openai.com/v1",
			APIKey:                "sk-test-super-secret-key-12345",
			Model:                 "gpt-4o",
			MaxTokens:             2048,
			Temperature:           0.3,
			CAFile:                caFile,
			TLSServerName:         "llm.internal",
			InsecureSkipVerify:    false,
			RequestTimeoutSeconds: 45,
			MaxSteps:              8,
		},
		Daemon: DaemonConfig{
			MaxConcurrency:      3,
			ProbeTimeoutSeconds: 90,
		},
		MaxConcurrency:          3,
		HeartbeatIntervalSec:    25,
		ProbeTimeoutSeconds:     90,
		GlobalPrivacyPromptPath: filepath.Join(tempDir, "global-privacy.md"),
	}

	if err := SaveClientConfig(configPath, original); err != nil {
		t.Fatalf("SaveClientConfig failed: %v", err)
	}

	// Verify file exists and has non-zero size
	fi, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatalf("config file is empty")
	}

	// Permission verification (0600 on non-windows)
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Errorf("expected file mode 0600, got %o", fi.Mode().Perm())
		}
	}

	// Load back
	loaded, err := LoadClientConfig(configPath)
	if err != nil {
		t.Fatalf("LoadClientConfig failed: %v", err)
	}

	if loaded.HubURL != original.HubURL {
		t.Errorf("HubURL mismatch: expected %q, got %q", original.HubURL, loaded.HubURL)
	}
	if loaded.MemberID != original.MemberID {
		t.Errorf("MemberID mismatch: expected %q, got %q", original.MemberID, loaded.MemberID)
	}
	if loaded.MemberName != original.MemberName {
		t.Errorf("MemberName mismatch: expected %q, got %q", original.MemberName, loaded.MemberName)
	}
	if loaded.Token != original.Token {
		t.Errorf("Token mismatch: expected %q, got %q", original.Token, loaded.Token)
	}
	if loaded.MachineName != original.MachineName {
		t.Errorf("MachineName mismatch: expected %q, got %q", original.MachineName, loaded.MachineName)
	}
	if len(loaded.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(loaded.Workspaces))
	}
	if loaded.Workspaces[0].ID != original.Workspaces[0].ID {
		t.Errorf("workspace ID mismatch: expected %q, got %q", original.Workspaces[0].ID, loaded.Workspaces[0].ID)
	}
	if loaded.LLM.Provider != original.LLM.Provider {
		t.Errorf("LLM Provider mismatch: expected %q, got %q", original.LLM.Provider, loaded.LLM.Provider)
	}
	if loaded.LLM.BaseURL != original.LLM.BaseURL {
		t.Errorf("LLM BaseURL mismatch: expected %q, got %q", original.LLM.BaseURL, loaded.LLM.BaseURL)
	}
	if loaded.LLM.APIKey != original.LLM.APIKey {
		t.Errorf("LLM APIKey mismatch: expected %q, got %q", original.LLM.APIKey, loaded.LLM.APIKey)
	}
	if loaded.LLM.Model != original.LLM.Model {
		t.Errorf("LLM Model mismatch: expected %q, got %q", original.LLM.Model, loaded.LLM.Model)
	}
	if loaded.LLM.MaxTokens != original.LLM.MaxTokens {
		t.Errorf("LLM MaxTokens mismatch: expected %d, got %d", original.LLM.MaxTokens, loaded.LLM.MaxTokens)
	}
	if loaded.LLM.CAFile != original.LLM.CAFile {
		t.Errorf("LLM CAFile mismatch: expected %q, got %q", original.LLM.CAFile, loaded.LLM.CAFile)
	}
	if loaded.LLM.TLSServerName != original.LLM.TLSServerName {
		t.Errorf("LLM TLSServerName mismatch: expected %q, got %q", original.LLM.TLSServerName, loaded.LLM.TLSServerName)
	}
	if loaded.LLM.RequestTimeoutSeconds != 45 {
		t.Errorf("LLM RequestTimeoutSeconds expected 45, got %d", loaded.LLM.RequestTimeoutSeconds)
	}
	if loaded.LLM.MaxSteps != 8 {
		t.Errorf("LLM MaxSteps expected 8, got %d", loaded.LLM.MaxSteps)
	}
	if loaded.MaxConcurrency != 3 {
		t.Errorf("MaxConcurrency expected 3, got %d", loaded.MaxConcurrency)
	}
	if loaded.Daemon.MaxConcurrency != 3 {
		t.Errorf("Daemon.MaxConcurrency expected 3, got %d", loaded.Daemon.MaxConcurrency)
	}
	if loaded.ProbeTimeoutSeconds != 90 {
		t.Errorf("ProbeTimeoutSeconds expected 90, got %d", loaded.ProbeTimeoutSeconds)
	}
	if loaded.Daemon.ProbeTimeoutSeconds != 90 {
		t.Errorf("Daemon.ProbeTimeoutSeconds expected 90, got %d", loaded.Daemon.ProbeTimeoutSeconds)
	}
	if loaded.GlobalPrivacyPromptPath != original.GlobalPrivacyPromptPath {
		t.Errorf("GlobalPrivacyPromptPath mismatch: expected %q, got %q", original.GlobalPrivacyPromptPath, loaded.GlobalPrivacyPromptPath)
	}

	// Repeated save to test atomic overwrite
	original.MemberName = "Updated Name"
	if err := SaveClientConfig(configPath, original); err != nil {
		t.Fatalf("second SaveClientConfig failed: %v", err)
	}
	loaded2, err := LoadClientConfig(configPath)
	if err != nil {
		t.Fatalf("second LoadClientConfig failed: %v", err)
	}
	if loaded2.MemberName != "Updated Name" {
		t.Errorf("expected updated MemberName %q, got %q", "Updated Name", loaded2.MemberName)
	}
}

func TestSaveAndLoadHubConfig_RoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "hub.json")
	dataDir := filepath.Join(tempDir, "hub-data")

	original := &HubConfig{
		Addr:                 "127.0.0.1:9090",
		DataDir:              dataDir,
		AdminToken:           "ti_adm_super_admin_secret_token_123",
		PublicURL:            "https://hub.company.internal",
		HeartbeatIntervalSec: 15,
		DefaultQueryTTLSec:   43200,  // 12 hours
		MaxQueryTTLSec:       259200, // 3 days
		MaxProbeTimeoutSec:   180,
		RateLimits: RateLimitConfig{
			QueriesPerMinute: 120,
			Burst:            20,
		},
	}

	if err := SaveHubConfig(configPath, original); err != nil {
		t.Fatalf("SaveHubConfig failed: %v", err)
	}

	fi, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("hub config missing: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Errorf("expected 0600, got %o", fi.Mode().Perm())
		}
	}

	loaded, err := LoadHubConfig(configPath)
	if err != nil {
		t.Fatalf("LoadHubConfig failed: %v", err)
	}

	if loaded.Addr != original.Addr {
		t.Errorf("Addr mismatch: expected %q, got %q", original.Addr, loaded.Addr)
	}
	if loaded.DataDir != original.DataDir {
		t.Errorf("DataDir mismatch: expected %q, got %q", original.DataDir, loaded.DataDir)
	}
	if loaded.AdminToken != original.AdminToken {
		t.Errorf("AdminToken mismatch: expected %q, got %q", original.AdminToken, loaded.AdminToken)
	}
	if loaded.PublicURL != original.PublicURL {
		t.Errorf("PublicURL mismatch: expected %q, got %q", original.PublicURL, loaded.PublicURL)
	}
	if loaded.HeartbeatIntervalSec != 15 {
		t.Errorf("HeartbeatIntervalSec expected 15, got %d", loaded.HeartbeatIntervalSec)
	}
	if loaded.DefaultQueryTTLSec != 43200 {
		t.Errorf("DefaultQueryTTLSec expected 43200, got %d", loaded.DefaultQueryTTLSec)
	}
	if loaded.MaxQueryTTLSec != 259200 {
		t.Errorf("MaxQueryTTLSec expected 259200, got %d", loaded.MaxQueryTTLSec)
	}
	if loaded.MaxProbeTimeoutSec != 180 {
		t.Errorf("MaxProbeTimeoutSec expected 180, got %d", loaded.MaxProbeTimeoutSec)
	}
	if loaded.RateLimits.QueriesPerMinute != 120 {
		t.Errorf("RateLimits.QueriesPerMinute expected 120, got %d", loaded.RateLimits.QueriesPerMinute)
	}
	if loaded.RateLimits.Burst != 20 {
		t.Errorf("RateLimits.Burst expected 20, got %d", loaded.RateLimits.Burst)
	}
	if loaded.RateLimit.QueriesPerMinute != 120 {
		t.Errorf("RateLimit alias QueriesPerMinute expected 120, got %d", loaded.RateLimit.QueriesPerMinute)
	}
}

func TestEnvOverrides_Precedence(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.json")

	base := &ClientConfig{
		HubURL:               "http://file-hub:8080",
		MemberID:             "mem_file",
		MemberName:           "File User",
		Token:                "ti_mem_file_token",
		MachineName:          "file-box",
		MaxConcurrency:       2,
		HeartbeatIntervalSec: 20,
		ProbeTimeoutSeconds:  60,
		LLM: LLMConfig{
			Provider:              "openai",
			BaseURL:               "https://file-llm.com",
			APIKey:                "sk-file-key",
			Model:                 "file-model",
			CAFile:                "/file/ca.pem",
			TLSServerName:         "file.sni",
			InsecureSkipVerify:    false,
			RequestTimeoutSeconds: 30,
			MaxSteps:              5,
			MaxTokens:             1000,
		},
	}

	if err := SaveClientConfig(configPath, base); err != nil {
		t.Fatalf("SaveClientConfig failed: %v", err)
	}

	// Set overrides
	t.Setenv(EnvTalkIntentHubURL, "http://env-hub:9999")
	t.Setenv(EnvTalkIntentToken, "ti_mem_env_token_override")
	t.Setenv(EnvTalkIntentMemberID, "mem_env_override")
	t.Setenv(EnvTalkIntentMemberName, "Env User")
	t.Setenv(EnvTalkIntentMachineName, "env-machine")
	t.Setenv(EnvTalkIntentMaxConcurrency, "5")
	t.Setenv(EnvTalkIntentHeartbeat, "35")
	t.Setenv(EnvTalkIntentProbeTimeout, "120")
	t.Setenv(EnvTalkIntentPrivacyPrompt, "/env/privacy.md")
	t.Setenv(EnvTalkIntentLLMProvider, "anthropic")
	t.Setenv(EnvTalkIntentLLMBaseURL, "https://api.anthropic.com")
	t.Setenv(EnvTalkIntentLLMAPIKey, "sk-ant-env-secret-key")
	t.Setenv(EnvTalkIntentLLMModel, "claude-3-5-sonnet")
	t.Setenv(EnvTalkIntentLLMCAFile, "/env/ca.pem")
	t.Setenv(EnvTalkIntentLLMTLSServer, "env.sni")
	t.Setenv(EnvTalkIntentLLMInsecure, "true")
	t.Setenv(EnvTalkIntentLLMTimeout, "55")
	t.Setenv(EnvTalkIntentLLMMaxSteps, "12")
	t.Setenv(EnvTalkIntentLLMMaxTokens, "8192")

	loaded, err := LoadClientConfig(configPath)
	if err != nil {
		t.Fatalf("LoadClientConfig failed: %v", err)
	}

	// Verify all environment overrides took precedence over file values
	if loaded.HubURL != "http://env-hub:9999" {
		t.Errorf("expected HubURL override, got %q", loaded.HubURL)
	}
	if loaded.Token != "ti_mem_env_token_override" {
		t.Errorf("expected Token override, got %q", loaded.Token)
	}
	if loaded.MemberID != "mem_env_override" {
		t.Errorf("expected MemberID override, got %q", loaded.MemberID)
	}
	if loaded.MemberName != "Env User" {
		t.Errorf("expected MemberName override, got %q", loaded.MemberName)
	}
	if loaded.MachineName != "env-machine" {
		t.Errorf("expected MachineName override, got %q", loaded.MachineName)
	}
	if loaded.MaxConcurrency != 5 {
		t.Errorf("expected MaxConcurrency override 5, got %d", loaded.MaxConcurrency)
	}
	if loaded.Daemon.MaxConcurrency != 5 {
		t.Errorf("expected Daemon.MaxConcurrency override 5, got %d", loaded.Daemon.MaxConcurrency)
	}
	if loaded.HeartbeatIntervalSec != 35 {
		t.Errorf("expected HeartbeatIntervalSec override 35, got %d", loaded.HeartbeatIntervalSec)
	}
	if loaded.ProbeTimeoutSeconds != 120 {
		t.Errorf("expected ProbeTimeoutSeconds override 120, got %d", loaded.ProbeTimeoutSeconds)
	}
	if loaded.GlobalPrivacyPromptPath != "/env/privacy.md" {
		t.Errorf("expected GlobalPrivacyPromptPath override, got %q", loaded.GlobalPrivacyPromptPath)
	}
	if loaded.LLM.Provider != "anthropic" {
		t.Errorf("expected LLM Provider override anthropic, got %q", loaded.LLM.Provider)
	}
	if loaded.LLM.BaseURL != "https://api.anthropic.com" {
		t.Errorf("expected LLM BaseURL override, got %q", loaded.LLM.BaseURL)
	}
	if loaded.LLM.APIKey != "sk-ant-env-secret-key" {
		t.Errorf("expected LLM APIKey override, got %q", loaded.LLM.APIKey)
	}
	if loaded.LLM.Model != "claude-3-5-sonnet" {
		t.Errorf("expected LLM Model override, got %q", loaded.LLM.Model)
	}
	if loaded.LLM.CAFile != "/env/ca.pem" {
		t.Errorf("expected LLM CAFile override, got %q", loaded.LLM.CAFile)
	}
	if loaded.LLM.TLSServerName != "env.sni" {
		t.Errorf("expected LLM TLSServerName override, got %q", loaded.LLM.TLSServerName)
	}
	if !loaded.LLM.InsecureSkipVerify {
		t.Errorf("expected InsecureSkipVerify override true, got false")
	}
	if loaded.LLM.RequestTimeoutSeconds != 55 {
		t.Errorf("expected RequestTimeoutSeconds override 55, got %d", loaded.LLM.RequestTimeoutSeconds)
	}
	if loaded.LLM.MaxSteps != 12 {
		t.Errorf("expected MaxSteps override 12, got %d", loaded.LLM.MaxSteps)
	}
	if loaded.LLM.MaxTokens != 8192 {
		t.Errorf("expected MaxTokens override 8192, got %d", loaded.LLM.MaxTokens)
	}
}

func TestHubEnvOverrides(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "hub.json")

	base := &HubConfig{
		Addr:       ":8080",
		DataDir:    filepath.Join(tempDir, "data"),
		AdminToken: "ti_adm_file",
	}
	if err := SaveHubConfig(configPath, base); err != nil {
		t.Fatalf("SaveHubConfig failed: %v", err)
	}

	t.Setenv(EnvTalkIntentHubAddr, ":9090")
	t.Setenv(EnvTalkIntentHubDataDir, "/env/hub-data")
	t.Setenv(EnvTalkIntentAdminToken, "ti_adm_env_token")
	t.Setenv(EnvTalkIntentPublicURL, "https://env.talkintent.org")
	t.Setenv(EnvTalkIntentDefaultQueryTTL, "1000")
	t.Setenv(EnvTalkIntentMaxQueryTTL, "5000")
	t.Setenv(EnvTalkIntentMaxProbeTimeout, "300")
	t.Setenv(EnvTalkIntentRateLimitQPM, "99")
	t.Setenv(EnvTalkIntentRateLimitBurst, "33")

	loaded, err := LoadHubConfig(configPath)
	if err != nil {
		t.Fatalf("LoadHubConfig failed: %v", err)
	}

	if loaded.Addr != ":9090" {
		t.Errorf("expected Addr :9090, got %s", loaded.Addr)
	}
	if loaded.DataDir != "/env/hub-data" {
		t.Errorf("expected DataDir /env/hub-data, got %s", loaded.DataDir)
	}
	if loaded.AdminToken != "ti_adm_env_token" {
		t.Errorf("expected AdminToken ti_adm_env_token, got %s", loaded.AdminToken)
	}
	if loaded.PublicURL != "https://env.talkintent.org" {
		t.Errorf("expected PublicURL https://env.talkintent.org, got %s", loaded.PublicURL)
	}
	if loaded.DefaultQueryTTLSec != 1000 {
		t.Errorf("expected DefaultQueryTTLSec 1000, got %d", loaded.DefaultQueryTTLSec)
	}
	if loaded.MaxQueryTTLSec != 5000 {
		t.Errorf("expected MaxQueryTTLSec 5000, got %d", loaded.MaxQueryTTLSec)
	}
	if loaded.MaxProbeTimeoutSec != 300 {
		t.Errorf("expected MaxProbeTimeoutSec 300, got %d", loaded.MaxProbeTimeoutSec)
	}
	if loaded.RateLimits.QueriesPerMinute != 99 {
		t.Errorf("expected RateLimits.QueriesPerMinute 99, got %d", loaded.RateLimits.QueriesPerMinute)
	}
	if loaded.RateLimits.Burst != 33 {
		t.Errorf("expected RateLimits.Burst 33, got %d", loaded.RateLimits.Burst)
	}
}

func TestTalkIntentHome_Honored(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv(EnvTalkIntentHome, tempHome)

	home := GetTalkIntentHome()
	if home != filepath.Clean(tempHome) {
		t.Errorf("expected home %q, got %q", tempHome, home)
	}

	clientPath := DefaultClientConfigPath()
	expectedClient := filepath.Join(tempHome, "config.json")
	if clientPath != expectedClient {
		t.Errorf("expected client config path %q, got %q", expectedClient, clientPath)
	}

	hubPath := DefaultHubConfigPath()
	expectedHub := filepath.Join(tempHome, "hub.json")
	if hubPath != expectedHub {
		t.Errorf("expected hub config path %q, got %q", expectedHub, hubPath)
	}

	promptPath := DefaultPrivacyPromptPath()
	expectedPrompt := filepath.Join(tempHome, "privacy-prompt.md")
	if promptPath != expectedPrompt {
		t.Errorf("expected privacy prompt path %q, got %q", expectedPrompt, promptPath)
	}
}

func TestValidation_ClientConfig_ActionableErrors(t *testing.T) {
	// 1. nil config
	var nilCfg *ClientConfig
	if err := nilCfg.Validate(); err == nil {
		t.Errorf("expected error on nil ClientConfig")
	}

	// 2. Empty mandatory fields
	emptyCfg := &ClientConfig{}
	err := emptyCfg.Validate()
	if err == nil {
		t.Fatalf("expected validation errors on empty config")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "hub_url") || !strings.Contains(errStr, "remediation") {
		t.Errorf("expected actionable error mentioning hub_url and remediation, got: %s", errStr)
	}
	if !strings.Contains(errStr, "member_id") {
		t.Errorf("expected error mentioning member_id, got: %s", errStr)
	}
	if !strings.Contains(errStr, "token") {
		t.Errorf("expected error mentioning token, got: %s", errStr)
	}

	// 3. Malformed hub_url
	badURLCfg := &ClientConfig{
		HubURL:   "ftp://invalid-hub",
		MemberID: "mem_1",
		Token:    "ti_mem_1",
	}
	err = badURLCfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "malformed Hub URL") {
		t.Errorf("expected malformed Hub URL error, got: %v", err)
	}

	// 4. Invalid LLM provider
	badLLMCfg := &ClientConfig{
		HubURL:   "http://localhost:8080",
		MemberID: "mem_1",
		Token:    "ti_mem_1",
		LLM: LLMConfig{
			Provider: "google-gemini",
		},
	}
	err = badLLMCfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected unsupported provider error, got: %v", err)
	}

	// 5. Invalid temperature
	badTempCfg := &ClientConfig{
		HubURL:   "http://localhost:8080",
		MemberID: "mem_1",
		Token:    "ti_mem_1",
		LLM: LLMConfig{
			Provider:    "openai",
			Temperature: 2.5,
		},
	}
	err = badTempCfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "temperature") {
		t.Errorf("expected temperature out of bounds error, got: %v", err)
	}

	// 6. CA file not found
	badCACfg := &ClientConfig{
		HubURL:   "http://localhost:8080",
		MemberID: "mem_1",
		Token:    "ti_mem_1",
		LLM: LLMConfig{
			Provider: "openai",
			CAFile:   filepath.Join(t.TempDir(), "nonexistent.pem"),
		},
	}
	err = badCACfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected CA file not found error, got: %v", err)
	}

	// 7. Workspace with empty root path
	badWSCfg := &ClientConfig{
		HubURL:   "http://localhost:8080",
		MemberID: "mem_1",
		Token:    "ti_mem_1",
		Workspaces: []WorkspaceConfig{
			{Name: "app", RootPath: ""},
		},
	}
	err = badWSCfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "root_path") {
		t.Errorf("expected root_path error, got: %v", err)
	}

	// 8. Valid config passes
	validCfg := &ClientConfig{
		HubURL:         "http://localhost:8080",
		MemberID:       "mem_valid",
		Token:          "ti_mem_valid_token",
		MaxConcurrency: 2,
		LLM: LLMConfig{
			Provider:    "openai",
			BaseURL:     "https://api.openai.com/v1",
			APIKey:      "sk-test",
			Model:       "gpt-4o",
			Temperature: 0.2,
		},
	}
	if err := validCfg.Validate(); err != nil {
		t.Errorf("expected valid config to pass validation, got: %v", err)
	}
}

func TestValidation_HubConfig_ActionableErrors(t *testing.T) {
	// 1. nil config
	var nilCfg *HubConfig
	if err := nilCfg.Validate(); err == nil {
		t.Errorf("expected error on nil HubConfig")
	}

	// 2. empty addr & data_dir
	emptyCfg := &HubConfig{}
	err := emptyCfg.Validate()
	if err == nil {
		t.Fatalf("expected validation errors on empty HubConfig")
	}
	if !strings.Contains(err.Error(), "addr") || !strings.Contains(err.Error(), "data_dir") {
		t.Errorf("expected addr and data_dir errors, got: %s", err.Error())
	}

	// 3. DefaultQueryTTL > MaxQueryTTL
	ttlCfg := &HubConfig{
		Addr:               ":8080",
		DataDir:            t.TempDir(),
		DefaultQueryTTLSec: 10000,
		MaxQueryTTLSec:     5000,
	}
	err = ttlCfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds max query TTL") {
		t.Errorf("expected default TTL > max TTL error, got: %v", err)
	}

	// 4. Valid HubConfig passes
	validCfg := NewDefaultHubConfig()
	validCfg.DataDir = t.TempDir()
	if err := validCfg.Validate(); err != nil {
		t.Errorf("expected default hub config to pass, got: %v", err)
	}
}

func TestEnsureAdminToken_F46(t *testing.T) {
	tempDir := t.TempDir()

	// Scenario 1: TALKINTENT_ADMIN_TOKEN is set
	t.Setenv(EnvTalkIntentAdminToken, "ti_adm_env_explicit_123")
	tok, err := EnsureAdminToken(tempDir)
	if err != nil {
		t.Fatalf("EnsureAdminToken failed: %v", err)
	}
	if tok != "ti_adm_env_explicit_123" {
		t.Errorf("expected env token, got %q", tok)
	}

	// Scenario 2: Env unset, file already exists
	t.Setenv(EnvTalkIntentAdminToken, "")
	tokenFile := filepath.Join(tempDir, "admin.token")
	if err := os.WriteFile(tokenFile, []byte("ti_adm_persisted_file_456\n"), 0600); err != nil {
		t.Fatalf("failed to write admin.token: %v", err)
	}
	tok2, err := EnsureAdminToken(tempDir)
	if err != nil {
		t.Fatalf("EnsureAdminToken failed: %v", err)
	}
	if tok2 != "ti_adm_persisted_file_456" {
		t.Errorf("expected file token, got %q", tok2)
	}

	// Scenario 3: Neither exists -> generate and persist
	freshDir := filepath.Join(tempDir, "fresh-hub")
	tok3, err := EnsureAdminToken(freshDir)
	if err != nil {
		t.Fatalf("EnsureAdminToken fresh failed: %v", err)
	}
	if !strings.HasPrefix(tok3, "ti_adm_") {
		t.Errorf("expected ti_adm_ prefix, got %q", tok3)
	}
	if len(tok3) != len("ti_adm_")+48 {
		t.Errorf("expected token length %d, got %d (%q)", len("ti_adm_")+48, len(tok3), tok3)
	}

	// Check file was written with 0600
	savedPath := filepath.Join(freshDir, "admin.token")
	fi, err := os.Stat(savedPath)
	if err != nil {
		t.Fatalf("admin.token missing: %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0600 {
			t.Errorf("expected 0600 permissions, got %o", fi.Mode().Perm())
		}
	}

	// Subsequent call returns identical token
	tok4, err := EnsureAdminToken(freshDir)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if tok4 != tok3 {
		t.Errorf("expected idempotent token %q, got %q", tok3, tok4)
	}

	// HubConfig.EnsureAdminToken helper
	hubCfg := &HubConfig{DataDir: freshDir}
	tok5, err := hubCfg.EnsureAdminToken()
	if err != nil {
		t.Fatalf("hubCfg.EnsureAdminToken failed: %v", err)
	}
	if tok5 != tok3 || hubCfg.AdminToken != tok3 {
		t.Errorf("hubCfg.EnsureAdminToken mismatch: got %q", tok5)
	}
}

func TestRedacted_Views(t *testing.T) {
	rawToken := "ti_mem_1234567890abcdefghijklmnopqrstuvwxyz"
	rawKey := "sk-openai-super-secret-key-1234567890"
	rawAdmin := "ti_adm_abcdef1234567890abcdef1234567890"

	clientCfg := &ClientConfig{
		HubURL:   "http://hub.internal:8080",
		MemberID: "mem_alice",
		Token:    rawToken,
		LLM: LLMConfig{
			Provider: "openai",
			APIKey:   rawKey,
			Model:    "gpt-4o",
		},
		Workspaces: []WorkspaceConfig{
			{ID: "ws1", Name: "app", RootPath: "/home/alice/app"},
		},
	}

	redactedClient := clientCfg.Redacted()

	// Verify original is untouched
	if clientCfg.Token != rawToken {
		t.Errorf("original client token was mutated")
	}
	if clientCfg.LLM.APIKey != rawKey {
		t.Errorf("original LLM API key was mutated")
	}

	// Verify redacted copy has no raw secrets
	if strings.Contains(redactedClient.Token, "1234567890") {
		t.Errorf("redacted token leaked secret: %s", redactedClient.Token)
	}
	if strings.Contains(redactedClient.LLM.APIKey, "super-secret") {
		t.Errorf("redacted API key leaked secret: %s", redactedClient.LLM.APIKey)
	}

	// Check lengths or fingerprints only
	if !strings.Contains(redactedClient.Token, "[len=43]") {
		t.Errorf("expected length in redacted token, got: %s", redactedClient.Token)
	}
	if !strings.Contains(redactedClient.LLM.APIKey, "[len=37]") {
		t.Errorf("expected length in redacted key, got: %s", redactedClient.LLM.APIKey)
	}

	// String() representation must not leak credentials
	clientStr := clientCfg.String()
	if strings.Contains(clientStr, rawToken) {
		t.Errorf("clientCfg.String() leaked raw token")
	}
	if strings.Contains(clientStr, rawKey) {
		t.Errorf("clientCfg.String() leaked raw API key")
	}

	// HubConfig redaction
	hubCfg := &HubConfig{
		Addr:       ":8080",
		AdminToken: rawAdmin,
	}
	redactedHub := hubCfg.Redacted()
	if hubCfg.AdminToken != rawAdmin {
		t.Errorf("original admin token mutated")
	}
	if strings.Contains(redactedHub.AdminToken, "abcdef") {
		t.Errorf("redacted admin token leaked secret: %s", redactedHub.AdminToken)
	}
	hubStr := hubCfg.String()
	if strings.Contains(hubStr, rawAdmin) {
		t.Errorf("hubCfg.String() leaked raw admin token")
	}

	// Test RedactSecret helper directly
	if RedactSecret("") != "" {
		t.Errorf("expected empty string for empty secret")
	}
	short := RedactSecret("secret")
	if strings.Contains(short, "secret") {
		t.Errorf("RedactSecret leaked short secret: %s", short)
	}
}

func TestWorkspaceHelpers(t *testing.T) {
	tempDir := t.TempDir()
	ws1Dir := filepath.Join(tempDir, "repo-alpha")
	ws2Dir := filepath.Join(tempDir, "repo-beta")
	if err := os.MkdirAll(ws1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ws2Dir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &ClientConfig{}

	// Add first workspace
	err := cfg.AddWorkspace(WorkspaceConfig{
		RootPath: ws1Dir,
	})
	if err != nil {
		t.Fatalf("AddWorkspace failed: %v", err)
	}
	if len(cfg.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(cfg.Workspaces))
	}
	if cfg.Workspaces[0].Name != "repo-alpha" {
		t.Errorf("expected auto-derived name 'repo-alpha', got %q", cfg.Workspaces[0].Name)
	}
	if cfg.Workspaces[0].ID != "repo-alpha" {
		t.Errorf("expected auto-derived id 'repo-alpha', got %q", cfg.Workspaces[0].ID)
	}

	// Add second workspace with explicit ID
	err = cfg.AddWorkspace(WorkspaceConfig{
		ID:       "beta-custom-id",
		Name:     "Beta Repo",
		RootPath: ws2Dir,
	})
	if err != nil {
		t.Fatalf("AddWorkspace 2 failed: %v", err)
	}
	if len(cfg.Workspaces) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(cfg.Workspaces))
	}

	// Updating workspace 1 in-place by root path
	err = cfg.AddWorkspace(WorkspaceConfig{
		ID:                "repo-alpha",
		Name:              "Renamed Alpha",
		RootPath:          ws1Dir,
		PrivacyPromptPath: filepath.Join(ws1Dir, "custom-prompt.md"),
	})
	if err != nil {
		t.Fatalf("update workspace failed: %v", err)
	}
	if len(cfg.Workspaces) != 2 {
		t.Errorf("expected in-place update, but length is %d", len(cfg.Workspaces))
	}
	if cfg.Workspaces[0].Name != "Renamed Alpha" {
		t.Errorf("expected updated name, got %q", cfg.Workspaces[0].Name)
	}

	// FindWorkspace
	found, ok := cfg.FindWorkspace("beta-custom-id")
	if !ok || found.Name != "Beta Repo" {
		t.Errorf("FindWorkspace by ID failed")
	}
	found2, ok2 := cfg.FindWorkspace(ws1Dir)
	if !ok2 || found2.ID != "repo-alpha" {
		t.Errorf("FindWorkspace by path failed")
	}
	_, ok3 := cfg.FindWorkspace("non-existent")
	if ok3 {
		t.Errorf("FindWorkspace found non-existent workspace")
	}

	// RemoveWorkspace
	removed := cfg.RemoveWorkspace("beta-custom-id")
	if !removed || len(cfg.Workspaces) != 1 {
		t.Errorf("RemoveWorkspace failed")
	}
	removed2 := cfg.RemoveWorkspace(ws1Dir)
	if !removed2 || len(cfg.Workspaces) != 0 {
		t.Errorf("RemoveWorkspace by path failed")
	}
}

func TestNormalizeWorkspacePath(t *testing.T) {
	tempDir := t.TempDir()
	subDir := filepath.Join(tempDir, "valid-dir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	norm, err := NormalizeWorkspacePath(subDir)
	if err != nil {
		t.Fatalf("NormalizeWorkspacePath failed: %v", err)
	}
	if norm != filepath.Clean(subDir) {
		t.Errorf("expected %q, got %q", filepath.Clean(subDir), norm)
	}

	// Non-existent directory
	_, err = NormalizeWorkspacePath(filepath.Join(tempDir, "missing"))
	if err == nil {
		t.Errorf("expected error for missing directory")
	}

	// File instead of directory
	filePath := filepath.Join(tempDir, "regular-file.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = NormalizeWorkspacePath(filePath)
	if err == nil {
		t.Errorf("expected error for regular file")
	}
}

func TestLLMConfigDefaults(t *testing.T) {
	l := LLMConfig{}
	if l.Timeout() != 60*time.Second {
		t.Errorf("expected default timeout 60s, got %v", l.Timeout())
	}
	if l.Steps() != 10 {
		t.Errorf("expected default steps 10, got %d", l.Steps())
	}
	if l.Tokens() != 4096 {
		t.Errorf("expected default tokens 4096, got %d", l.Tokens())
	}

	l2 := LLMConfig{
		RequestTimeoutSeconds: 15,
		MaxSteps:              3,
		MaxTokens:             512,
	}
	if l2.Timeout() != 15*time.Second {
		t.Errorf("expected 15s, got %v", l2.Timeout())
	}
	if l2.Steps() != 3 {
		t.Errorf("expected 3, got %d", l2.Steps())
	}
	if l2.Tokens() != 512 {
		t.Errorf("expected 512, got %d", l2.Tokens())
	}
}
