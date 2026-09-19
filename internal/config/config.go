package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Default constants for configuration.
const (
	EnvTalkIntentHome = "TALKINTENT_HOME"
	DefaultDirName    = ".talkintent"
	ConfigFileJSON    = "config.json"
	PrivacyPromptMD   = "privacy-prompt.md"

	DefaultMaxConcurrency       = 2
	DefaultHeartbeatIntervalSec = 20
	DefaultLLMMaxTokens         = 4096
	DefaultLLMTemperature       = 0.2
)

// WorkspaceConfig holds directory and privacy settings for a monitored project.
type WorkspaceConfig struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	RootPath          string `json:"root_path"`
	PrivacyPromptPath string `json:"privacy_prompt_path,omitempty"`
}

// LLMConfig specifies local model credentials and parameters.
type LLMConfig struct {
	Provider           string  `json:"provider"` // "openai" or "anthropic"
	BaseURL            string  `json:"base_url"`
	APIKey             string  `json:"api_key"`
	Model              string  `json:"model"`
	MaxTokens          int     `json:"max_tokens,omitempty"`
	Temperature        float64 `json:"temperature,omitempty"`
	CAFile             string  `json:"ca_file,omitempty"`
	TLSServerName      string  `json:"tls_server_name,omitempty"`
	InsecureSkipVerify bool    `json:"insecure_skip_verify,omitempty"`
}

// ClientConfig represents the complete configuration stored on a member's machine.
type ClientConfig struct {
	HubURL               string            `json:"hub_url"`
	MemberID             string            `json:"member_id"`
	MemberName           string            `json:"member_name"`
	Token                string            `json:"token"`
	MachineName          string            `json:"machine_name"`
	Workspaces           []WorkspaceConfig `json:"workspaces"`
	LLM                  LLMConfig         `json:"llm"`
	MaxConcurrency       int               `json:"max_concurrency"`
	HeartbeatIntervalSec int               `json:"heartbeat_interval_sec"`
}

// HubConfig represents configuration options for starting a central Hub server.
type HubConfig struct {
	Addr       string `json:"addr"`
	DataDir    string `json:"data_dir"`
	AdminToken string `json:"admin_token"`
	PublicURL  string `json:"public_url"`
}

// GetTalkIntentHome returns the base directory for TalkIntent state.
// Evaluates $TALKINTENT_HOME first, falling back to ~/.talkintent.
func GetTalkIntentHome() string {
	if v := os.Getenv(EnvTalkIntentHome); v != "" {
		return filepath.Clean(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Clean("." + string(filepath.Separator) + DefaultDirName)
	}
	return filepath.Join(home, DefaultDirName)
}

// DefaultClientConfigPath returns the canonical path to client config.json.
func DefaultClientConfigPath() string {
	return filepath.Join(GetTalkIntentHome(), ConfigFileJSON)
}

// DefaultPrivacyPromptPath returns the canonical path to global privacy-prompt.md.
func DefaultPrivacyPromptPath() string {
	return filepath.Join(GetTalkIntentHome(), PrivacyPromptMD)
}

// LoadClientConfig reads and parses ClientConfig from the specified file path.
func LoadClientConfig(path string) (*ClientConfig, error) {
	if path == "" {
		path = DefaultClientConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var cfg ClientConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON from %q: %w", path, err)
	}

	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = DefaultMaxConcurrency
	}
	if cfg.HeartbeatIntervalSec <= 0 {
		cfg.HeartbeatIntervalSec = DefaultHeartbeatIntervalSec
	}

	return &cfg, nil
}

// SaveClientConfig serializes and writes ClientConfig to disk with 0600 permissions.
func SaveClientConfig(path string, cfg *ClientConfig) error {
	if cfg == nil {
		return errors.New("cannot save nil ClientConfig")
	}
	if path == "" {
		path = DefaultClientConfigPath()
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write with 0600 permissions (user read/write only)
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		return fmt.Errorf("failed to write config to %q: %w", path, err)
	}

	return nil
}

// NormalizeWorkspacePath cleans and checks if workspace directory exists.
func NormalizeWorkspacePath(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace directory does not exist %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory %q", abs)
	}
	return abs, nil
}
