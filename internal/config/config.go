package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

	DefaultHubAddr             = ":8080"
	DefaultProbeTimeoutSeconds = 60
	DefaultQueryTTLSec         = 86400     // 24 hours
	MaxQueryTTLSec             = 7 * 86400 // 7 days
	DefaultMaxProbeTimeoutSec  = 120       // 2 minutes
	DefaultRateLimitQPM        = 60
	DefaultRateLimitBurst      = 10
	DefaultAdminTokenFile      = "admin.token"
	DefaultHubConfigFileJSON   = "hub.json"
	DefaultLLMMaxSteps         = 10
	DefaultLLMTimeoutSeconds   = 60
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
	Provider              string  `json:"provider"` // "openai" or "anthropic"
	BaseURL               string  `json:"base_url"`
	APIKey                string  `json:"api_key"`
	Model                 string  `json:"model"`
	MaxTokens             int     `json:"max_tokens,omitempty"`
	Temperature           float64 `json:"temperature,omitempty"`
	CAFile                string  `json:"ca_file,omitempty"`
	TLSServerName         string  `json:"tls_server_name,omitempty"`
	InsecureSkipVerify    bool    `json:"insecure_skip_verify,omitempty"`
	RequestTimeoutSeconds int     `json:"request_timeout_seconds,omitempty"`
	MaxSteps              int     `json:"max_steps,omitempty"`
}

// Timeout returns the configured request timeout or the default (60s).
func (l LLMConfig) Timeout() time.Duration {
	if l.RequestTimeoutSeconds > 0 {
		return time.Duration(l.RequestTimeoutSeconds) * time.Second
	}
	return time.Duration(DefaultLLMTimeoutSeconds) * time.Second
}

// Steps returns the configured maximum reasoning steps or default (10).
func (l LLMConfig) Steps() int {
	if l.MaxSteps > 0 {
		return l.MaxSteps
	}
	return DefaultLLMMaxSteps
}

// Tokens returns the configured max tokens or default (4096).
func (l LLMConfig) Tokens() int {
	if l.MaxTokens > 0 {
		return l.MaxTokens
	}
	return DefaultLLMMaxTokens
}

// UnmarshalJSON supports both request_timeout and request_timeout_seconds fields.
func (l *LLMConfig) UnmarshalJSON(data []byte) error {
	type Alias LLMConfig
	aux := struct {
		*Alias
		RequestTimeout *int `json:"request_timeout,omitempty"`
	}{
		Alias: (*Alias)(l),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.RequestTimeout != nil && l.RequestTimeoutSeconds == 0 {
		l.RequestTimeoutSeconds = *aux.RequestTimeout
	}
	return nil
}

// DaemonConfig encapsulates execution parameters for the background client daemon.
type DaemonConfig struct {
	MaxConcurrency      int `json:"max_concurrency,omitempty"`
	ProbeTimeoutSeconds int `json:"probe_timeout_seconds,omitempty"`
}

// ClientConfig represents the complete configuration stored on a member's machine.
type ClientConfig struct {
	HubURL                  string            `json:"hub_url"`
	MemberID                string            `json:"member_id"`
	MemberName              string            `json:"member_name"`
	Token                   string            `json:"token"`
	MachineName             string            `json:"machine_name"`
	Workspaces              []WorkspaceConfig `json:"workspaces"`
	LLM                     LLMConfig         `json:"llm"`
	Daemon                  DaemonConfig      `json:"daemon,omitempty"`
	MaxConcurrency          int               `json:"max_concurrency,omitempty"`
	HeartbeatIntervalSec    int               `json:"heartbeat_interval_sec,omitempty"`
	ProbeTimeoutSeconds     int               `json:"probe_timeout_seconds,omitempty"`
	GlobalPrivacyPromptPath string            `json:"global_privacy_prompt_path,omitempty"`
}

// UnmarshalJSON synchronizes nested DaemonConfig and top-level daemon fields.
func (c *ClientConfig) UnmarshalJSON(data []byte) error {
	type Alias ClientConfig
	aux := struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if c.Daemon.MaxConcurrency > 0 && c.MaxConcurrency == 0 {
		c.MaxConcurrency = c.Daemon.MaxConcurrency
	} else if c.MaxConcurrency > 0 && c.Daemon.MaxConcurrency == 0 {
		c.Daemon.MaxConcurrency = c.MaxConcurrency
	}

	if c.Daemon.ProbeTimeoutSeconds > 0 && c.ProbeTimeoutSeconds == 0 {
		c.ProbeTimeoutSeconds = c.Daemon.ProbeTimeoutSeconds
	} else if c.ProbeTimeoutSeconds > 0 && c.Daemon.ProbeTimeoutSeconds == 0 {
		c.Daemon.ProbeTimeoutSeconds = c.ProbeTimeoutSeconds
	}

	return nil
}

// RateLimitConfig configures request limits on the central Hub.
type RateLimitConfig struct {
	QueriesPerMinute int `json:"queries_per_minute,omitempty"`
	Burst            int `json:"burst,omitempty"`
}

// HubConfig represents configuration options for starting a central Hub server.
type HubConfig struct {
	Addr                 string          `json:"addr"`
	DataDir              string          `json:"data_dir"`
	AdminToken           string          `json:"admin_token"`
	PublicURL            string          `json:"public_url,omitempty"`
	HeartbeatIntervalSec int             `json:"heartbeat_interval_sec,omitempty"`
	DefaultQueryTTLSec   int             `json:"default_query_ttl_sec,omitempty"`
	MaxQueryTTLSec       int             `json:"max_query_ttl_sec,omitempty"`
	MaxProbeTimeoutSec   int             `json:"max_probe_timeout_sec,omitempty"`
	RateLimits           RateLimitConfig `json:"rate_limits,omitempty"`
	RateLimit            RateLimitConfig `json:"rate_limit,omitempty"`
}

// UnmarshalJSON synchronizes RateLimits and RateLimit fields.
func (c *HubConfig) UnmarshalJSON(data []byte) error {
	type Alias HubConfig
	aux := struct {
		*Alias
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if c.RateLimit.QueriesPerMinute > 0 && c.RateLimits.QueriesPerMinute == 0 {
		c.RateLimits = c.RateLimit
	} else if c.RateLimits.QueriesPerMinute > 0 && c.RateLimit.QueriesPerMinute == 0 {
		c.RateLimit = c.RateLimits
	}
	return nil
}

// NewDefaultClientConfig returns a ClientConfig populated with standard defaults.
func NewDefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		Workspaces: []WorkspaceConfig{},
		LLM: LLMConfig{
			Provider:              "openai",
			MaxTokens:             DefaultLLMMaxTokens,
			Temperature:           DefaultLLMTemperature,
			MaxSteps:              DefaultLLMMaxSteps,
			RequestTimeoutSeconds: DefaultLLMTimeoutSeconds,
		},
		Daemon: DaemonConfig{
			MaxConcurrency:      DefaultMaxConcurrency,
			ProbeTimeoutSeconds: DefaultProbeTimeoutSeconds,
		},
		MaxConcurrency:       DefaultMaxConcurrency,
		HeartbeatIntervalSec: DefaultHeartbeatIntervalSec,
		ProbeTimeoutSeconds:  DefaultProbeTimeoutSeconds,
	}
}

// NewDefaultHubConfig returns a HubConfig populated with standard defaults.
func NewDefaultHubConfig() *HubConfig {
	dataDir := filepath.Join(GetTalkIntentHome(), "hub")
	rateLimit := RateLimitConfig{
		QueriesPerMinute: DefaultRateLimitQPM,
		Burst:            DefaultRateLimitBurst,
	}
	return &HubConfig{
		Addr:                 DefaultHubAddr,
		DataDir:              dataDir,
		HeartbeatIntervalSec: DefaultHeartbeatIntervalSec,
		DefaultQueryTTLSec:   DefaultQueryTTLSec,
		MaxQueryTTLSec:       MaxQueryTTLSec,
		MaxProbeTimeoutSec:   DefaultMaxProbeTimeoutSec,
		RateLimits:           rateLimit,
		RateLimit:            rateLimit,
	}
}

// GetTalkIntentHome returns the base directory for TalkIntent state.
// Evaluates $TALKINTENT_HOME first, falling back to ~/.talkintent.
func GetTalkIntentHome() string {
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHome)); v != "" {
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

// DefaultHubConfigPath returns the canonical path to hub.json.
func DefaultHubConfigPath() string {
	return filepath.Join(GetTalkIntentHome(), DefaultHubConfigFileJSON)
}

// EffectivePrivacyPromptPath returns the configured global privacy prompt path,
// or defaults to ~/.talkintent/privacy-prompt.md.
func (c *ClientConfig) EffectivePrivacyPromptPath() string {
	if c.GlobalPrivacyPromptPath != "" {
		return c.GlobalPrivacyPromptPath
	}
	return DefaultPrivacyPromptPath()
}

// LoadClientConfig reads and parses ClientConfig from the specified file path.
// If path is empty, DefaultClientConfigPath() is used.
// Applies defaults and environment variable overrides before returning.
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

	// Apply defaults
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = DefaultMaxConcurrency
	}
	cfg.Daemon.MaxConcurrency = cfg.MaxConcurrency

	if cfg.HeartbeatIntervalSec <= 0 {
		cfg.HeartbeatIntervalSec = DefaultHeartbeatIntervalSec
	}

	if cfg.ProbeTimeoutSeconds <= 0 {
		cfg.ProbeTimeoutSeconds = DefaultProbeTimeoutSeconds
	}
	cfg.Daemon.ProbeTimeoutSeconds = cfg.ProbeTimeoutSeconds

	if cfg.LLM.MaxTokens <= 0 {
		cfg.LLM.MaxTokens = DefaultLLMMaxTokens
	}
	if cfg.LLM.MaxSteps <= 0 {
		cfg.LLM.MaxSteps = DefaultLLMMaxSteps
	}
	if cfg.LLM.RequestTimeoutSeconds <= 0 {
		cfg.LLM.RequestTimeoutSeconds = DefaultLLMTimeoutSeconds
	}
	if cfg.Workspaces == nil {
		cfg.Workspaces = []WorkspaceConfig{}
	}

	// Apply environment variable overrides
	cfg.ApplyEnvOverrides()

	return &cfg, nil
}

// SaveClientConfig serializes and writes ClientConfig to disk with 0600 permissions
// using an atomic rename to prevent partial writes.
func SaveClientConfig(path string, cfg *ClientConfig) error {
	if cfg == nil {
		return errors.New("cannot save nil ClientConfig")
	}
	if path == "" {
		path = DefaultClientConfigPath()
	}

	// Synchronize daemon settings prior to writing
	if cfg.Daemon.MaxConcurrency > 0 && cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = cfg.Daemon.MaxConcurrency
	} else if cfg.MaxConcurrency > 0 {
		cfg.Daemon.MaxConcurrency = cfg.MaxConcurrency
	}
	if cfg.Daemon.ProbeTimeoutSeconds > 0 && cfg.ProbeTimeoutSeconds == 0 {
		cfg.ProbeTimeoutSeconds = cfg.Daemon.ProbeTimeoutSeconds
	} else if cfg.ProbeTimeoutSeconds > 0 {
		cfg.Daemon.ProbeTimeoutSeconds = cfg.ProbeTimeoutSeconds
	}

	if cfg.Workspaces == nil {
		cfg.Workspaces = []WorkspaceConfig{}
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal client config: %w", err)
	}

	return atomicWriteFile(path, append(data, '\n'), 0600)
}

// LoadHubConfig reads and parses HubConfig from disk.
// If path is empty, DefaultHubConfigPath() is used.
// Applies defaults and environment variable overrides before returning.
func LoadHubConfig(path string) (*HubConfig, error) {
	if path == "" {
		path = DefaultHubConfigPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read hub config file %q: %w", path, err)
	}

	var cfg HubConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse hub config JSON from %q: %w", path, err)
	}

	if cfg.Addr == "" {
		cfg.Addr = DefaultHubAddr
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(GetTalkIntentHome(), "hub")
	}
	if cfg.HeartbeatIntervalSec <= 0 {
		cfg.HeartbeatIntervalSec = DefaultHeartbeatIntervalSec
	}
	if cfg.DefaultQueryTTLSec <= 0 {
		cfg.DefaultQueryTTLSec = DefaultQueryTTLSec
	}
	if cfg.MaxQueryTTLSec <= 0 {
		cfg.MaxQueryTTLSec = MaxQueryTTLSec
	}
	if cfg.MaxProbeTimeoutSec <= 0 {
		cfg.MaxProbeTimeoutSec = DefaultMaxProbeTimeoutSec
	}
	if cfg.RateLimits.QueriesPerMinute <= 0 {
		cfg.RateLimits.QueriesPerMinute = DefaultRateLimitQPM
	}
	if cfg.RateLimits.Burst <= 0 {
		cfg.RateLimits.Burst = DefaultRateLimitBurst
	}
	cfg.RateLimit = cfg.RateLimits

	cfg.ApplyEnvOverrides()

	return &cfg, nil
}

// SaveHubConfig serializes and atomically writes HubConfig to disk with 0600 permissions.
func SaveHubConfig(path string, cfg *HubConfig) error {
	if cfg == nil {
		return errors.New("cannot save nil HubConfig")
	}
	if path == "" {
		path = DefaultHubConfigPath()
	}

	if cfg.RateLimit.QueriesPerMinute > 0 && cfg.RateLimits.QueriesPerMinute == 0 {
		cfg.RateLimits = cfg.RateLimit
	} else if cfg.RateLimits.QueriesPerMinute > 0 {
		cfg.RateLimit = cfg.RateLimits
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal hub config: %w", err)
	}

	return atomicWriteFile(path, append(data, '\n'), 0600)
}

// EnsureAdminToken ensures an admin token is available for the Hub server (F46).
// Order of resolution:
// 1. TALKINTENT_ADMIN_TOKEN environment variable.
// 2. Existing $DATA_DIR/admin.token file content.
// 3. Secure random generation (48 hex chars with "ti_adm_" prefix) persisted to $DATA_DIR/admin.token (0600).
func EnsureAdminToken(dataDir string) (string, error) {
	if envToken := strings.TrimSpace(os.Getenv(EnvTalkIntentAdminToken)); envToken != "" {
		return envToken, nil
	}

	if dataDir == "" {
		dataDir = filepath.Join(GetTalkIntentHome(), "hub")
	}

	tokenPath := filepath.Join(dataDir, DefaultAdminTokenFile)
	if data, err := os.ReadFile(tokenPath); err == nil {
		tok := strings.TrimSpace(string(data))
		if tok != "" {
			return tok, nil
		}
	}

	// Generate 24 random bytes -> 48 hex chars
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate random token: %w", err)
	}
	token := "ti_adm_" + hex.EncodeToString(buf)

	if err := atomicWriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
		return "", fmt.Errorf("failed to save admin token to %q: %w", tokenPath, err)
	}

	return token, nil
}

// EnsureAdminToken initializes AdminToken on HubConfig if not already set.
func (c *HubConfig) EnsureAdminToken() (string, error) {
	if c.AdminToken != "" {
		return c.AdminToken, nil
	}
	token, err := EnsureAdminToken(c.DataDir)
	if err != nil {
		return "", err
	}
	c.AdminToken = token
	return token, nil
}

// atomicWriteFile writes data to a temporary file in the target directory and renames it.
// Enforces perm (e.g. 0600) on POSIX filesystems.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary config file in %q: %w", dir, err)
	}
	tmpPath := tmpFile.Name()

	cleanedUp := false
	defer func() {
		if !cleanedUp {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if runtime.GOOS != "windows" {
		if err := tmpFile.Chmod(perm); err != nil {
			return fmt.Errorf("failed to chmod temporary file %q: %w", tmpPath, err)
		}
	}

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write data to %q: %w", tmpPath, err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary file %q: %w", tmpPath, err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file %q: %w", tmpPath, err)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, perm); err != nil {
			return fmt.Errorf("failed to set permissions on %q: %w", tmpPath, err)
		}
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// On Windows, if target exists, attempt removal and second rename attempt
		if removeErr := os.Remove(path); removeErr == nil {
			if renameErr := os.Rename(tmpPath, path); renameErr == nil {
				cleanedUp = true
				if runtime.GOOS != "windows" {
					_ = os.Chmod(path, perm)
				}
				return nil
			}
		}
		return fmt.Errorf("failed to atomically rename %q to %q: %w", tmpPath, path, err)
	}

	cleanedUp = true
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, perm)
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
