package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/hub"
	"github.com/Sskift/talkintent/internal/store"
	"github.com/Sskift/talkintent/web"
)

// HubOptions contains flags and configuration for running the Hub server.
type HubOptions struct {
	Addr                 string
	DataDir              string
	AdminToken           string
	PublicURL            string
	HeartbeatIntervalSec int
	DefaultQueryTTLSec   int
	MaxQueryTTLSec       int
	MaxProbeTimeoutSec   int
	RateLimitQPM         int
	RateLimitBurst       int
	JSONOutput           bool

	// ExplicitFlags records which command-line flags were explicitly passed by the caller.
	// Used to ensure CLI flags take precedence over environment variables.
	ExplicitFlags map[string]bool
}

// BuildHubConfig constructs an effective HubConfig following the precedence rule:
// explicitly passed flag > env var > flag default.
func BuildHubConfig(opts HubOptions) (*config.HubConfig, error) {
	// 1. Initialize with standard defaults
	cfg := config.NewDefaultHubConfig()

	// 2. Apply environment variable overrides
	if err := cfg.ApplyEnvOverrides(); err != nil {
		return nil, fmt.Errorf("invalid environment configuration: %w", err)
	}

	// 3. Apply CLI flags with precedence: explicitly passed flag > env var > flag default
	if opts.ExplicitFlags != nil {
		if opts.ExplicitFlags["addr"] {
			cfg.Addr = opts.Addr
		}
		if opts.ExplicitFlags["data-dir"] {
			cfg.DataDir = opts.DataDir
		}
		if opts.ExplicitFlags["admin-token"] {
			cfg.AdminToken = opts.AdminToken
		}
		if opts.ExplicitFlags["public-url"] {
			cfg.PublicURL = opts.PublicURL
		}
		if opts.ExplicitFlags["heartbeat"] {
			cfg.HeartbeatIntervalSec = opts.HeartbeatIntervalSec
		}
		if opts.ExplicitFlags["default-query-ttl"] {
			cfg.DefaultQueryTTLSec = opts.DefaultQueryTTLSec
		}
		if opts.ExplicitFlags["max-query-ttl"] {
			cfg.MaxQueryTTLSec = opts.MaxQueryTTLSec
		}
		if opts.ExplicitFlags["max-probe-timeout"] {
			cfg.MaxProbeTimeoutSec = opts.MaxProbeTimeoutSec
		}
		if opts.ExplicitFlags["rate-limit-qpm"] {
			cfg.RateLimits.QueriesPerMinute = opts.RateLimitQPM
			cfg.RateLimit.QueriesPerMinute = opts.RateLimitQPM
		}
		if opts.ExplicitFlags["rate-limit-burst"] {
			cfg.RateLimits.Burst = opts.RateLimitBurst
			cfg.RateLimit.Burst = opts.RateLimitBurst
		}
	} else {
		// Programmatic invocation without explicit flags map:
		// Any non-empty / non-zero / non-default values take precedence.
		if opts.Addr != "" && opts.Addr != config.DefaultHubAddr {
			cfg.Addr = opts.Addr
		}
		if opts.DataDir != "" && opts.DataDir != "./data" {
			cfg.DataDir = opts.DataDir
		}
		if opts.AdminToken != "" {
			cfg.AdminToken = opts.AdminToken
		}
		if opts.PublicURL != "" {
			cfg.PublicURL = opts.PublicURL
		}
		if opts.HeartbeatIntervalSec > 0 && opts.HeartbeatIntervalSec != config.DefaultHeartbeatIntervalSec {
			cfg.HeartbeatIntervalSec = opts.HeartbeatIntervalSec
		}
		if opts.DefaultQueryTTLSec > 0 && opts.DefaultQueryTTLSec != config.DefaultQueryTTLSec {
			cfg.DefaultQueryTTLSec = opts.DefaultQueryTTLSec
		}
		if opts.MaxQueryTTLSec > 0 && opts.MaxQueryTTLSec != config.MaxQueryTTLSec {
			cfg.MaxQueryTTLSec = opts.MaxQueryTTLSec
		}
		if opts.MaxProbeTimeoutSec > 0 && opts.MaxProbeTimeoutSec != config.DefaultMaxProbeTimeoutSec {
			cfg.MaxProbeTimeoutSec = opts.MaxProbeTimeoutSec
		}
		if opts.RateLimitQPM > 0 && opts.RateLimitQPM != config.DefaultRateLimitQPM {
			cfg.RateLimits.QueriesPerMinute = opts.RateLimitQPM
			cfg.RateLimit.QueriesPerMinute = opts.RateLimitQPM
		}
		if opts.RateLimitBurst > 0 && opts.RateLimitBurst != config.DefaultRateLimitBurst {
			cfg.RateLimits.Burst = opts.RateLimitBurst
			cfg.RateLimit.Burst = opts.RateLimitBurst
		}
	}

	// 4. Validate resulting configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation error: %w", err)
	}

	return cfg, nil
}

// ExecuteHub starts the central coordination Hub server.
// Respects Rule 5: never prints raw admin token, only SHA256 fingerprint and length!
func ExecuteHub(ctx context.Context, opts HubOptions, stdout, stderr io.Writer) int {
	cfg, err := BuildHubConfig(opts)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to configure Hub server: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		fmt.Fprintf(stderr, "Failed to create data directory %q: %v\n", cfg.DataDir, err)
		return 1
	}

	tokenPath := filepath.Join(cfg.DataDir, "admin.token")
	if cfg.AdminToken == "" {
		if data, err := os.ReadFile(tokenPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			cfg.AdminToken = strings.TrimSpace(string(data))
		} else {
			randPart, err := store.RandomHex(24)
			if err != nil {
				fmt.Fprintf(stderr, "Failed to generate random admin token: %v\n", err)
				return 1
			}
			cfg.AdminToken = "ti_adm_" + randPart
			if err := os.WriteFile(tokenPath, []byte(cfg.AdminToken+"\n"), 0600); err != nil {
				fmt.Fprintf(stderr, "Failed to write admin token file: %v\n", err)
				return 1
			}
		}
	}

	// Compute fingerprint (SHA256 hash first 16 chars)
	hasher := sha256.New()
	hasher.Write([]byte(cfg.AdminToken))
	fp := hex.EncodeToString(hasher.Sum(nil))[:16]

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status":            "starting",
			"addr":              cfg.Addr,
			"data_dir":          cfg.DataDir,
			"public_url":        cfg.PublicURL,
			"admin_token_fp":    fp,
			"admin_token_len":   len(cfg.AdminToken),
			"admin_token_store": tokenPath,
		})
	} else {
		fmt.Fprintf(stdout, "TalkIntent Hub Server starting...\n")
		fmt.Fprintf(stdout, "  Listen Address:        %s\n", cfg.Addr)
		fmt.Fprintf(stdout, "  Data Directory:        %s\n", cfg.DataDir)
		if cfg.PublicURL != "" {
			fmt.Fprintf(stdout, "  Public URL:            %s\n", cfg.PublicURL)
		} else {
			fmt.Fprintf(stdout, "  Public URL:            (none)\n")
		}
		fmt.Fprintf(stdout, "  Admin Token File:      %s (mode 0600)\n", tokenPath)
		fmt.Fprintf(stdout, "  Admin Token Fingerprint: sha256:%s... (length: %d chars)\n", fp, len(cfg.AdminToken))
		fmt.Fprintf(stdout, "  (Rule 5: full credentials are never logged or echoed to console)\n\n")
	}

	st, err := store.NewJSONLStore(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to initialize JSONL storage in %s: %v\n", cfg.DataDir, err)
		return 1
	}
	defer st.Close()

	logDest := stdout
	if opts.JSONOutput {
		logDest = stderr
	}
	logger := slog.New(slog.NewTextHandler(logDest, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	srv, err := hub.NewServer(cfg, st, web.FS(), logger)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to create Hub server: %v\n", err)
		return 1
	}

	if err := srv.Start(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(stderr, "Hub server error: %v\n", err)
		return 1
	}

	return 0
}
