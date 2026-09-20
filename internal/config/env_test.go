package config

import (
	"strings"
	"testing"
)

func TestHubConfig_ApplyEnvOverrides_AllVars(t *testing.T) {
	t.Setenv(EnvTalkIntentHubAddr, "127.0.0.1:9099")
	t.Setenv(EnvTalkIntentHubDataDir, "/custom/data/dir")
	t.Setenv(EnvTalkIntentAdminToken, "ti_adm_test_token_12345")
	t.Setenv(EnvTalkIntentPublicURL, "https://hub.example.com")
	t.Setenv(EnvTalkIntentHeartbeat, "45s")
	t.Setenv(EnvTalkIntentDefaultQueryTTL, "2h")
	t.Setenv(EnvTalkIntentMaxQueryTTL, "48h")
	t.Setenv(EnvTalkIntentMaxProbeTimeout, "90")
	t.Setenv(EnvTalkIntentRateLimitQPM, "120")
	t.Setenv(EnvTalkIntentRateLimitBurst, "25")

	cfg := NewDefaultHubConfig()
	if err := cfg.ApplyEnvOverrides(); err != nil {
		t.Fatalf("unexpected error applying env overrides: %v", err)
	}

	if cfg.Addr != "127.0.0.1:9099" {
		t.Errorf("expected Addr '127.0.0.1:9099', got %q", cfg.Addr)
	}
	if cfg.DataDir != "/custom/data/dir" {
		t.Errorf("expected DataDir '/custom/data/dir', got %q", cfg.DataDir)
	}
	if cfg.AdminToken != "ti_adm_test_token_12345" {
		t.Errorf("expected AdminToken 'ti_adm_test_token_12345', got %q", cfg.AdminToken)
	}
	if cfg.PublicURL != "https://hub.example.com" {
		t.Errorf("expected PublicURL 'https://hub.example.com', got %q", cfg.PublicURL)
	}
	if cfg.HeartbeatIntervalSec != 45 {
		t.Errorf("expected HeartbeatIntervalSec 45, got %d", cfg.HeartbeatIntervalSec)
	}
	if cfg.DefaultQueryTTLSec != 7200 {
		t.Errorf("expected DefaultQueryTTLSec 7200 (2h), got %d", cfg.DefaultQueryTTLSec)
	}
	if cfg.MaxQueryTTLSec != 172800 {
		t.Errorf("expected MaxQueryTTLSec 172800 (48h), got %d", cfg.MaxQueryTTLSec)
	}
	if cfg.MaxProbeTimeoutSec != 90 {
		t.Errorf("expected MaxProbeTimeoutSec 90, got %d", cfg.MaxProbeTimeoutSec)
	}
	if cfg.RateLimits.QueriesPerMinute != 120 {
		t.Errorf("expected RateLimits.QueriesPerMinute 120, got %d", cfg.RateLimits.QueriesPerMinute)
	}
	if cfg.RateLimit.QueriesPerMinute != 120 {
		t.Errorf("expected RateLimit.QueriesPerMinute 120, got %d", cfg.RateLimit.QueriesPerMinute)
	}
	if cfg.RateLimits.Burst != 25 {
		t.Errorf("expected RateLimits.Burst 25, got %d", cfg.RateLimits.Burst)
	}
	if cfg.RateLimit.Burst != 25 {
		t.Errorf("expected RateLimit.Burst 25, got %d", cfg.RateLimit.Burst)
	}
}

func TestHubConfig_ApplyEnvOverrides_NilConfig(t *testing.T) {
	var cfg *HubConfig
	if err := cfg.ApplyEnvOverrides(); err != nil {
		t.Errorf("expected nil for nil receiver, got %v", err)
	}
}

func TestHubConfig_ApplyEnvOverrides_DurationFormats(t *testing.T) {
	tests := []struct {
		name     string
		envVar   string
		value    string
		expected int
	}{
		{"Heartbeat plain integer", EnvTalkIntentHeartbeat, "30", 30},
		{"Heartbeat seconds duration", EnvTalkIntentHeartbeat, "30s", 30},
		{"Heartbeat minutes duration", EnvTalkIntentHeartbeat, "2m", 120},
		{"DefaultQueryTTL hours duration", EnvTalkIntentDefaultQueryTTL, "1h", 3600},
		{"MaxProbeTimeout seconds", EnvTalkIntentMaxProbeTimeout, "180s", 180},
		{"MaxQueryTTL days as hours", EnvTalkIntentMaxQueryTTL, "72h", 259200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)
			cfg := NewDefaultHubConfig()
			if err := cfg.ApplyEnvOverrides(); err != nil {
				t.Fatalf("unexpected error for %s=%s: %v", tt.envVar, tt.value, err)
			}
			var got int
			switch tt.envVar {
			case EnvTalkIntentHeartbeat:
				got = cfg.HeartbeatIntervalSec
			case EnvTalkIntentDefaultQueryTTL:
				got = cfg.DefaultQueryTTLSec
			case EnvTalkIntentMaxProbeTimeout:
				got = cfg.MaxProbeTimeoutSec
			case EnvTalkIntentMaxQueryTTL:
				got = cfg.MaxQueryTTLSec
			}
			if got != tt.expected {
				t.Errorf("for %s=%s, expected %d, got %d", tt.envVar, tt.value, tt.expected, got)
			}
		})
	}
}

func TestHubConfig_ApplyEnvOverrides_InvalidValues(t *testing.T) {
	tests := []struct {
		name        string
		envVar      string
		value       string
		errContains string
	}{
		{"Heartbeat non-numeric", EnvTalkIntentHeartbeat, "invalid_num", "invalid TALKINTENT_HEARTBEAT_INTERVAL value"},
		{"Heartbeat zero", EnvTalkIntentHeartbeat, "0", "must be positive"},
		{"Heartbeat negative", EnvTalkIntentHeartbeat, "-10", "must be positive"},
		{"DefaultQueryTTL invalid duration", EnvTalkIntentDefaultQueryTTL, "abc", "invalid TALKINTENT_DEFAULT_QUERY_TTL value"},
		{"DefaultQueryTTL negative", EnvTalkIntentDefaultQueryTTL, "-60s", "must be positive"},
		{"MaxQueryTTL invalid", EnvTalkIntentMaxQueryTTL, "not_time", "invalid TALKINTENT_MAX_QUERY_TTL value"},
		{"MaxProbeTimeout non-numeric", EnvTalkIntentMaxProbeTimeout, "bad_timeout", "invalid TALKINTENT_MAX_PROBE_TIMEOUT value"},
		{"MaxProbeTimeout negative", EnvTalkIntentMaxProbeTimeout, "-5", "must be positive"},
		{"RateLimitQPM non-numeric", EnvTalkIntentRateLimitQPM, "bad_qpm", "must be a positive integer"},
		{"RateLimitQPM zero", EnvTalkIntentRateLimitQPM, "0", "must be positive"},
		{"RateLimitQPM negative", EnvTalkIntentRateLimitQPM, "-50", "must be positive"},
		{"RateLimitBurst non-numeric", EnvTalkIntentRateLimitBurst, "bad_burst", "must be a positive integer"},
		{"RateLimitBurst zero", EnvTalkIntentRateLimitBurst, "0", "must be positive"},
		{"RateLimitBurst negative", EnvTalkIntentRateLimitBurst, "-2", "must be positive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envVar, tt.value)
			cfg := NewDefaultHubConfig()
			err := cfg.ApplyEnvOverrides()
			if err == nil {
				t.Fatalf("expected error for %s=%q, but got nil", tt.envVar, tt.value)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("expected error containing %q, got %q", tt.errContains, err.Error())
			}
		})
	}
}

func TestClientConfig_ApplyEnvOverrides_HubCAFile(t *testing.T) {
	t.Setenv(EnvTalkIntentHubCAFile, "/custom/certs/hub-ca.pem")
	cfg := NewDefaultClientConfig()
	cfg.ApplyEnvOverrides()

	if cfg.HubCAFile != "/custom/certs/hub-ca.pem" {
		t.Errorf("expected HubCAFile '/custom/certs/hub-ca.pem', got %q", cfg.HubCAFile)
	}
}
