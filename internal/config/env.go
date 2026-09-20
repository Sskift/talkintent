package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable constants for TalkIntent client and hub overrides.
const (
	EnvTalkIntentHubURL         = "TALKINTENT_HUB_URL"
	EnvTalkIntentHubCAFile      = "TALKINTENT_HUB_CA_FILE"
	EnvTalkIntentToken          = "TALKINTENT_TOKEN"
	EnvTalkIntentMemberToken    = "TALKINTENT_MEMBER_TOKEN" // Alias for TALKINTENT_TOKEN
	EnvTalkIntentMemberID       = "TALKINTENT_MEMBER_ID"
	EnvTalkIntentMemberName     = "TALKINTENT_MEMBER_NAME"
	EnvTalkIntentMachineName    = "TALKINTENT_MACHINE_NAME"
	EnvTalkIntentMaxConcurrency = "TALKINTENT_MAX_CONCURRENCY"
	EnvTalkIntentHeartbeat      = "TALKINTENT_HEARTBEAT_INTERVAL"
	EnvTalkIntentProbeTimeout   = "TALKINTENT_PROBE_TIMEOUT"
	EnvTalkIntentPrivacyPrompt  = "TALKINTENT_PRIVACY_PROMPT_PATH"

	// LLM environment variables
	EnvTalkIntentLLMProvider  = "TALKINTENT_LLM_PROVIDER"
	EnvTalkIntentLLMBaseURL   = "TALKINTENT_LLM_BASE_URL"
	EnvTalkIntentLLMAPIKey    = "TALKINTENT_LLM_API_KEY"
	EnvTalkIntentLLMModel     = "TALKINTENT_LLM_MODEL"
	EnvTalkIntentLLMCAFile    = "TALKINTENT_LLM_CA_FILE"
	EnvTalkIntentLLMTLSServer = "TALKINTENT_LLM_TLS_SERVER_NAME"
	EnvTalkIntentLLMInsecure  = "TALKINTENT_LLM_INSECURE_SKIP_VERIFY"
	EnvTalkIntentLLMTimeout   = "TALKINTENT_LLM_TIMEOUT"
	EnvTalkIntentLLMMaxSteps  = "TALKINTENT_LLM_MAX_STEPS"
	EnvTalkIntentLLMMaxTokens = "TALKINTENT_LLM_MAX_TOKENS"

	// Hub environment variables
	EnvTalkIntentHubAddr         = "TALKINTENT_HUB_ADDR"
	EnvTalkIntentHubDataDir      = "TALKINTENT_DATA_DIR"
	EnvTalkIntentAdminToken      = "TALKINTENT_ADMIN_TOKEN"
	EnvTalkIntentPublicURL       = "TALKINTENT_PUBLIC_URL"
	EnvTalkIntentDefaultQueryTTL = "TALKINTENT_DEFAULT_QUERY_TTL"
	EnvTalkIntentMaxQueryTTL     = "TALKINTENT_MAX_QUERY_TTL"
	EnvTalkIntentMaxProbeTimeout = "TALKINTENT_MAX_PROBE_TIMEOUT"
	EnvTalkIntentRateLimitQPM    = "TALKINTENT_RATE_LIMIT_QPM"
	EnvTalkIntentRateLimitBurst  = "TALKINTENT_RATE_LIMIT_BURST"
)

// ApplyEnvOverrides overrides ClientConfig fields from environment variables.
func (c *ClientConfig) ApplyEnvOverrides() {
	if c == nil {
		return
	}

	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHubURL)); v != "" {
		c.HubURL = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHubCAFile)); v != "" {
		c.HubCAFile = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentToken)); v != "" {
		c.Token = v
	} else if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMemberToken)); v != "" {
		c.Token = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMemberID)); v != "" {
		c.MemberID = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMemberName)); v != "" {
		c.MemberName = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMachineName)); v != "" {
		c.MachineName = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentPrivacyPrompt)); v != "" {
		c.GlobalPrivacyPromptPath = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMaxConcurrency)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxConcurrency = n
			c.Daemon.MaxConcurrency = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHeartbeat)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.HeartbeatIntervalSec = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentProbeTimeout)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ProbeTimeoutSeconds = n
			c.Daemon.ProbeTimeoutSeconds = n
		}
	}

	// LLM overrides
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMProvider)); v != "" {
		c.LLM.Provider = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMBaseURL)); v != "" {
		c.LLM.BaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMAPIKey)); v != "" {
		c.LLM.APIKey = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMModel)); v != "" {
		c.LLM.Model = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMCAFile)); v != "" {
		c.LLM.CAFile = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMTLSServer)); v != "" {
		c.LLM.TLSServerName = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMInsecure)); v != "" {
		c.LLM.InsecureSkipVerify = parseBool(v)
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMTimeout)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LLM.RequestTimeoutSeconds = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMMaxSteps)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LLM.MaxSteps = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentLLMMaxTokens)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.LLM.MaxTokens = n
		}
	}
}

// ApplyEnvOverrides overrides HubConfig fields from environment variables.
func (c *HubConfig) ApplyEnvOverrides() error {
	if c == nil {
		return nil
	}

	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHubAddr)); v != "" {
		c.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHubDataDir)); v != "" {
		c.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentAdminToken)); v != "" {
		c.AdminToken = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentPublicURL)); v != "" {
		c.PublicURL = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentHeartbeat)); v != "" {
		n, err := parseDurationOrPositiveSeconds(v, EnvTalkIntentHeartbeat)
		if err != nil {
			return err
		}
		c.HeartbeatIntervalSec = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentDefaultQueryTTL)); v != "" {
		n, err := parseDurationOrPositiveSeconds(v, EnvTalkIntentDefaultQueryTTL)
		if err != nil {
			return err
		}
		c.DefaultQueryTTLSec = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMaxQueryTTL)); v != "" {
		n, err := parseDurationOrPositiveSeconds(v, EnvTalkIntentMaxQueryTTL)
		if err != nil {
			return err
		}
		c.MaxQueryTTLSec = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentMaxProbeTimeout)); v != "" {
		n, err := parseDurationOrPositiveSeconds(v, EnvTalkIntentMaxProbeTimeout)
		if err != nil {
			return err
		}
		c.MaxProbeTimeoutSec = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentRateLimitQPM)); v != "" {
		n, err := parsePositiveInt(v, EnvTalkIntentRateLimitQPM)
		if err != nil {
			return err
		}
		c.RateLimits.QueriesPerMinute = n
		c.RateLimit.QueriesPerMinute = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvTalkIntentRateLimitBurst)); v != "" {
		n, err := parsePositiveInt(v, EnvTalkIntentRateLimitBurst)
		if err != nil {
			return err
		}
		c.RateLimits.Burst = n
		c.RateLimit.Burst = n
	}
	return nil
}

func parseDurationOrPositiveSeconds(s, envVar string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("invalid %s value %q: must be positive", envVar, s)
		}
		return n, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		sec := int(d.Seconds())
		if sec <= 0 {
			return 0, fmt.Errorf("invalid %s value %q: must be positive", envVar, s)
		}
		return sec, nil
	}
	return 0, fmt.Errorf("invalid %s value %q: must be a positive integer or duration (e.g. 60 or 60s)", envVar, s)
}

func parsePositiveInt(s, envVar string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid %s value %q: must be a positive integer", envVar, s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("invalid %s value %q: must be positive", envVar, s)
	}
	return n, nil
}

// parseBool interprets common truthy string values ("true", "1", "yes", "on").
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
