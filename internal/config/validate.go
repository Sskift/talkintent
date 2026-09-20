package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ValidationError represents an actionable configuration error with concrete remediation advice.
type ValidationError struct {
	Field       string
	Message     string
	Remediation string
}

func (e ValidationError) Error() string {
	if e.Remediation != "" {
		return fmt.Sprintf("invalid config for %s: %s (remediation: %s)", e.Field, e.Message, e.Remediation)
	}
	return fmt.Sprintf("invalid config for %s: %s", e.Field, e.Message)
}

// ValidationErrors is a collection of actionable validation issues.
type ValidationErrors []ValidationError

func (ve ValidationErrors) Error() string {
	if len(ve) == 0 {
		return ""
	}
	if len(ve) == 1 {
		return ve[0].Error()
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d configuration validation error(s):\n", len(ve)))
	for i, e := range ve {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, e.Error()))
	}
	return strings.TrimSpace(b.String())
}

// Validate performs actionable checks on ClientConfig.
func (c *ClientConfig) Validate() error {
	if c == nil {
		return &ValidationError{
			Field:       "config",
			Message:     "client configuration is nil",
			Remediation: "initialize with NewDefaultClientConfig() or run 'talkintent pair'",
		}
	}

	var errs ValidationErrors

	// HubURL
	if c.HubURL == "" {
		errs = append(errs, ValidationError{
			Field:       "hub_url",
			Message:     "cannot be empty",
			Remediation: "specify Hub URL via 'talkintent pair --hub <url>' or TALKINTENT_HUB_URL",
		})
	} else {
		u, err := url.Parse(c.HubURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ws" && u.Scheme != "wss") {
			errs = append(errs, ValidationError{
				Field:       "hub_url",
				Message:     fmt.Sprintf("malformed Hub URL %q", c.HubURL),
				Remediation: "use valid HTTP/HTTPS or WS/WSS URL, e.g. http://127.0.0.1:8080",
			})
		}
	}

	// HubCAFile
	if c.HubCAFile != "" {
		if _, err := os.Stat(c.HubCAFile); err != nil {
			errs = append(errs, ValidationError{
				Field:       "hub_ca_file",
				Message:     fmt.Sprintf("Hub CA certificate file %q not found", c.HubCAFile),
				Remediation: "provide an existing PEM certificate file path or unset",
			})
		}
	}

	// Member ID & Token
	if c.MemberID == "" {
		errs = append(errs, ValidationError{
			Field:       "member_id",
			Message:     "cannot be empty",
			Remediation: "run 'talkintent pair' to pair with the Hub or set TALKINTENT_MEMBER_ID",
		})
	}
	if c.Token == "" {
		errs = append(errs, ValidationError{
			Field:       "token",
			Message:     "member token cannot be empty",
			Remediation: "run 'talkintent pair' or set TALKINTENT_TOKEN",
		})
	}

	// Concurrency & timeouts
	if c.MaxConcurrency < 0 {
		errs = append(errs, ValidationError{
			Field:       "max_concurrency",
			Message:     "must be non-negative",
			Remediation: "set a positive integer (e.g. 2) or leave 0 for default",
		})
	}
	if c.HeartbeatIntervalSec < 0 {
		errs = append(errs, ValidationError{
			Field:       "heartbeat_interval_sec",
			Message:     "must be non-negative",
			Remediation: "set a positive interval in seconds (default: 20)",
		})
	}
	if c.ProbeTimeoutSeconds < 0 {
		errs = append(errs, ValidationError{
			Field:       "probe_timeout_seconds",
			Message:     "must be non-negative",
			Remediation: "set a positive timeout in seconds (default: 60)",
		})
	}

	// Workspaces
	for i, ws := range c.Workspaces {
		if ws.ID == "" && ws.Name == "" {
			errs = append(errs, ValidationError{
				Field:       fmt.Sprintf("workspaces[%d]", i),
				Message:     "workspace must have an id or name",
				Remediation: "specify a non-empty name or id for each workspace",
			})
		}
		if ws.RootPath == "" {
			errs = append(errs, ValidationError{
				Field:       fmt.Sprintf("workspaces[%d].root_path", i),
				Message:     "root_path cannot be empty",
				Remediation: "provide the absolute path to the workspace root directory",
			})
		}
	}

	// LLM validation
	if err := c.LLM.Validate(); err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			errs = append(errs, ve...)
		} else if single, ok := err.(*ValidationError); ok {
			errs = append(errs, *single)
		} else {
			errs = append(errs, ValidationError{
				Field:       "llm",
				Message:     err.Error(),
				Remediation: "run 'talkintent llm' to configure local LLM parameters",
			})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Validate checks LLMConfig settings.
func (l LLMConfig) Validate() error {
	var errs ValidationErrors

	if l.Provider != "" && l.Provider != "openai" && l.Provider != "anthropic" {
		errs = append(errs, ValidationError{
			Field:       "llm.provider",
			Message:     fmt.Sprintf("unsupported provider %q", l.Provider),
			Remediation: "provider must be 'openai' or 'anthropic'",
		})
	}

	if l.BaseURL != "" {
		u, err := url.Parse(l.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, ValidationError{
				Field:       "llm.base_url",
				Message:     fmt.Sprintf("invalid base_url %q", l.BaseURL),
				Remediation: "specify a valid HTTP/HTTPS base URL, e.g. https://api.openai.com/v1",
			})
		}
	}

	if l.MaxTokens < 0 {
		errs = append(errs, ValidationError{
			Field:       "llm.max_tokens",
			Message:     "max_tokens must be non-negative",
			Remediation: "set a positive value or leave unset for default (4096)",
		})
	}

	if l.Temperature < 0.0 || l.Temperature > 2.0 {
		errs = append(errs, ValidationError{
			Field:       "llm.temperature",
			Message:     fmt.Sprintf("temperature %.2f out of bounds", l.Temperature),
			Remediation: "set temperature between 0.0 and 2.0 (e.g. 0.2)",
		})
	}

	if l.CAFile != "" {
		if _, err := os.Stat(l.CAFile); err != nil {
			errs = append(errs, ValidationError{
				Field:       "llm.ca_file",
				Message:     fmt.Sprintf("CA certificate file %q not found", l.CAFile),
				Remediation: "provide an existing PEM certificate file path or unset",
			})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Validate performs actionable checks on HubConfig.
func (c *HubConfig) Validate() error {
	if c == nil {
		return &ValidationError{
			Field:       "config",
			Message:     "hub configuration is nil",
			Remediation: "initialize with NewDefaultHubConfig()",
		}
	}

	var errs ValidationErrors

	if c.Addr == "" {
		errs = append(errs, ValidationError{
			Field:       "addr",
			Message:     "listen address cannot be empty",
			Remediation: "specify address like ':8080' or set TALKINTENT_HUB_ADDR",
		})
	}

	if c.DataDir == "" {
		errs = append(errs, ValidationError{
			Field:       "data_dir",
			Message:     "data directory cannot be empty",
			Remediation: "specify data directory like '~/.talkintent/hub' or set TALKINTENT_DATA_DIR",
		})
	}

	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, ValidationError{
				Field:       "public_url",
				Message:     fmt.Sprintf("malformed public URL %q", c.PublicURL),
				Remediation: "specify valid HTTP or HTTPS public URL",
			})
		}
	}

	if c.HeartbeatIntervalSec < 0 {
		errs = append(errs, ValidationError{
			Field:       "heartbeat_interval_sec",
			Message:     "must be non-negative",
			Remediation: "set positive interval in seconds (default: 20)",
		})
	}

	if c.DefaultQueryTTLSec < 0 {
		errs = append(errs, ValidationError{
			Field:       "default_query_ttl_sec",
			Message:     "must be non-negative",
			Remediation: "set positive seconds (default: 86400 / 24h)",
		})
	}

	if c.MaxQueryTTLSec < 0 {
		errs = append(errs, ValidationError{
			Field:       "max_query_ttl_sec",
			Message:     "must be non-negative",
			Remediation: "set positive seconds (default: 604800 / 7d)",
		})
	}

	if c.DefaultQueryTTLSec > 0 && c.MaxQueryTTLSec > 0 && c.DefaultQueryTTLSec > c.MaxQueryTTLSec {
		errs = append(errs, ValidationError{
			Field:       "default_query_ttl_sec",
			Message:     fmt.Sprintf("default query TTL (%ds) exceeds max query TTL (%ds)", c.DefaultQueryTTLSec, c.MaxQueryTTLSec),
			Remediation: "increase max_query_ttl_sec or decrease default_query_ttl_sec",
		})
	}

	if c.MaxProbeTimeoutSec < 0 {
		errs = append(errs, ValidationError{
			Field:       "max_probe_timeout_sec",
			Message:     "must be non-negative",
			Remediation: "set positive seconds (default: 120)",
		})
	}

	if c.RateLimits.QueriesPerMinute < 0 {
		errs = append(errs, ValidationError{
			Field:       "rate_limits.queries_per_minute",
			Message:     "must be non-negative",
			Remediation: "set positive rate limit (default: 60)",
		})
	}

	if c.RateLimits.Burst < 0 {
		errs = append(errs, ValidationError{
			Field:       "rate_limits.burst",
			Message:     "must be non-negative",
			Remediation: "set positive burst limit (default: 10)",
		})
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}
