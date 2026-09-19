package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RedactSecret returns a safe, redacted representation of a secret credential,
// exposing only its known standard prefix and byte length or fingerprint.
// Adheres strictly to: never print/log credential values — lengths or fingerprints only.
func RedactSecret(secret string) string {
	if secret == "" {
		return ""
	}
	length := len(secret)

	var prefix string
	if strings.HasPrefix(secret, "ti_mem_") {
		prefix = "ti_mem_"
	} else if strings.HasPrefix(secret, "ti_adm_") {
		prefix = "ti_adm_"
	} else if strings.HasPrefix(secret, "sk-") {
		prefix = "sk-"
	} else if strings.HasPrefix(secret, "ghp_") {
		prefix = "ghp_"
	}

	return fmt.Sprintf("%s***[len=%d]", prefix, length)
}

// Redacted returns a deep copy of LLMConfig with APIKey redacted.
func (l LLMConfig) Redacted() LLMConfig {
	cp := l
	cp.APIKey = RedactSecret(l.APIKey)
	return cp
}

// String returns formatted JSON of the redacted LLMConfig.
func (l LLMConfig) String() string {
	data, err := json.MarshalIndent(l.Redacted(), "", "  ")
	if err != nil {
		return fmt.Sprintf("LLMConfig{Provider:%q, Model:%q, APIKey:%q}", l.Provider, l.Model, RedactSecret(l.APIKey))
	}
	return string(data)
}

// Redacted returns a deep copy of ClientConfig with all sensitive secrets
// (member token, LLM API key) safely redacted for display or logging.
func (c *ClientConfig) Redacted() *ClientConfig {
	if c == nil {
		return nil
	}
	cp := *c
	cp.Token = RedactSecret(c.Token)
	cp.LLM = c.LLM.Redacted()

	if c.Workspaces != nil {
		cp.Workspaces = make([]WorkspaceConfig, len(c.Workspaces))
		copy(cp.Workspaces, c.Workspaces)
	}

	return &cp
}

// String returns formatted JSON of the redacted ClientConfig.
// Safely prevents credentials from being leaked if logged via fmt or slog.
func (c *ClientConfig) String() string {
	if c == nil {
		return "<nil>"
	}
	data, err := json.MarshalIndent(c.Redacted(), "", "  ")
	if err != nil {
		return fmt.Sprintf("ClientConfig{MemberID:%q, Token:%q}", c.MemberID, RedactSecret(c.Token))
	}
	return string(data)
}

// Redacted returns a deep copy of HubConfig with AdminToken safely redacted.
func (c *HubConfig) Redacted() *HubConfig {
	if c == nil {
		return nil
	}
	cp := *c
	cp.AdminToken = RedactSecret(c.AdminToken)
	return &cp
}

// String returns formatted JSON of the redacted HubConfig.
func (c *HubConfig) String() string {
	if c == nil {
		return "<nil>"
	}
	data, err := json.MarshalIndent(c.Redacted(), "", "  ")
	if err != nil {
		return fmt.Sprintf("HubConfig{Addr:%q, AdminToken:%q}", c.Addr, RedactSecret(c.AdminToken))
	}
	return string(data)
}
