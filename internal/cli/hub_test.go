package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/hub"
	"github.com/Sskift/talkintent/internal/protocol"
	"github.com/Sskift/talkintent/internal/store"
	"github.com/Sskift/talkintent/web"
)

// TestHubFlagHelpNamesEnvVars verifies that 'talkintent hub -h' documents the backing
// environment variable for every single supported hub configuration flag.
func TestHubFlagHelpNamesEnvVars(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"hub", "-h"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit code 0 for 'hub -h', got %d", code)
	}

	helpText := stderr.String()

	expectedEnvVars := []string{
		config.EnvTalkIntentHubAddr,
		config.EnvTalkIntentHubDataDir,
		config.EnvTalkIntentAdminToken,
		config.EnvTalkIntentPublicURL,
		config.EnvTalkIntentHeartbeat,
		config.EnvTalkIntentDefaultQueryTTL,
		config.EnvTalkIntentMaxQueryTTL,
		config.EnvTalkIntentMaxProbeTimeout,
		config.EnvTalkIntentRateLimitQPM,
		config.EnvTalkIntentRateLimitBurst,
	}

	for _, envVar := range expectedEnvVars {
		if !strings.Contains(helpText, "$"+envVar) {
			t.Errorf("expected hub help text to mention $%s, but was missing.\nHelp output:\n%s", envVar, helpText)
		}
	}
}

// TestBuildHubConfig_EnvOverrides verifies that all 10 hub environment variables
// override the base defaults in BuildHubConfig when no explicit CLI flags are given.
func TestBuildHubConfig_EnvOverrides(t *testing.T) {
	t.Setenv(config.EnvTalkIntentHubAddr, "127.0.0.1:8765")
	t.Setenv(config.EnvTalkIntentHubDataDir, "/tmp/talkintent-env-data")
	t.Setenv(config.EnvTalkIntentAdminToken, "ti_adm_env_specified_token")
	t.Setenv(config.EnvTalkIntentPublicURL, "https://public.env.test")
	t.Setenv(config.EnvTalkIntentHeartbeat, "35s")
	t.Setenv(config.EnvTalkIntentDefaultQueryTTL, "3600")
	t.Setenv(config.EnvTalkIntentMaxQueryTTL, "86400")
	t.Setenv(config.EnvTalkIntentMaxProbeTimeout, "75s")
	t.Setenv(config.EnvTalkIntentRateLimitQPM, "150")
	t.Setenv(config.EnvTalkIntentRateLimitBurst, "40")

	opts := HubOptions{
		Addr:                 config.DefaultHubAddr,
		DataDir:              "./data",
		HeartbeatIntervalSec: config.DefaultHeartbeatIntervalSec,
		DefaultQueryTTLSec:   config.DefaultQueryTTLSec,
		MaxQueryTTLSec:       config.MaxQueryTTLSec,
		MaxProbeTimeoutSec:   config.DefaultMaxProbeTimeoutSec,
		RateLimitQPM:         config.DefaultRateLimitQPM,
		RateLimitBurst:       config.DefaultRateLimitBurst,
		ExplicitFlags:        map[string]bool{}, // no flags explicitly passed on CLI
	}

	cfg, err := BuildHubConfig(opts)
	if err != nil {
		t.Fatalf("unexpected error from BuildHubConfig: %v", err)
	}

	if cfg.Addr != "127.0.0.1:8765" {
		t.Errorf("expected Addr '127.0.0.1:8765', got %q", cfg.Addr)
	}
	if cfg.DataDir != "/tmp/talkintent-env-data" {
		t.Errorf("expected DataDir '/tmp/talkintent-env-data', got %q", cfg.DataDir)
	}
	if cfg.AdminToken != "ti_adm_env_specified_token" {
		t.Errorf("expected AdminToken 'ti_adm_env_specified_token', got %q", cfg.AdminToken)
	}
	if cfg.PublicURL != "https://public.env.test" {
		t.Errorf("expected PublicURL 'https://public.env.test', got %q", cfg.PublicURL)
	}
	if cfg.HeartbeatIntervalSec != 35 {
		t.Errorf("expected HeartbeatIntervalSec 35, got %d", cfg.HeartbeatIntervalSec)
	}
	if cfg.DefaultQueryTTLSec != 3600 {
		t.Errorf("expected DefaultQueryTTLSec 3600, got %d", cfg.DefaultQueryTTLSec)
	}
	if cfg.MaxQueryTTLSec != 86400 {
		t.Errorf("expected MaxQueryTTLSec 86400, got %d", cfg.MaxQueryTTLSec)
	}
	if cfg.MaxProbeTimeoutSec != 75 {
		t.Errorf("expected MaxProbeTimeoutSec 75, got %d", cfg.MaxProbeTimeoutSec)
	}
	if cfg.RateLimits.QueriesPerMinute != 150 {
		t.Errorf("expected RateLimits.QueriesPerMinute 150, got %d", cfg.RateLimits.QueriesPerMinute)
	}
	if cfg.RateLimits.Burst != 40 {
		t.Errorf("expected RateLimits.Burst 40, got %d", cfg.RateLimits.Burst)
	}
}

// TestBuildHubConfig_Precedence verifies that explicitly passed CLI flags take precedence
// over environment variables, and environment variables take precedence over flag defaults.
func TestBuildHubConfig_Precedence(t *testing.T) {
	// Set environment variables for all settings
	t.Setenv(config.EnvTalkIntentHubAddr, ":9090")
	t.Setenv(config.EnvTalkIntentHubDataDir, "/env/dir")
	t.Setenv(config.EnvTalkIntentAdminToken, "ti_adm_env_token")
	t.Setenv(config.EnvTalkIntentPublicURL, "https://env.url")
	t.Setenv(config.EnvTalkIntentHeartbeat, "30s")
	t.Setenv(config.EnvTalkIntentDefaultQueryTTL, "1800")
	t.Setenv(config.EnvTalkIntentMaxQueryTTL, "7200")
	t.Setenv(config.EnvTalkIntentMaxProbeTimeout, "60")
	t.Setenv(config.EnvTalkIntentRateLimitQPM, "80")
	t.Setenv(config.EnvTalkIntentRateLimitBurst, "20")

	// Case 1: Explicit flags set for half the fields, unpassed for the other half
	opts := HubOptions{
		Addr:                 ":7070",                          // explicit
		DataDir:              "./data",                         // default (not explicit)
		AdminToken:           "ti_adm_flag_token",              // explicit
		PublicURL:            "",                               // default (not explicit)
		HeartbeatIntervalSec: 50,                               // explicit
		DefaultQueryTTLSec:   config.DefaultQueryTTLSec,        // default (not explicit)
		MaxQueryTTLSec:       14400,                            // explicit
		MaxProbeTimeoutSec:   config.DefaultMaxProbeTimeoutSec, // default (not explicit)
		RateLimitQPM:         200,                              // explicit
		RateLimitBurst:       config.DefaultRateLimitBurst,     // default (not explicit)
		ExplicitFlags: map[string]bool{
			"addr":           true,
			"admin-token":    true,
			"heartbeat":      true,
			"max-query-ttl":  true,
			"rate-limit-qpm": true,
		},
	}

	cfg, err := BuildHubConfig(opts)
	if err != nil {
		t.Fatalf("BuildHubConfig failed: %v", err)
	}

	// Explicit flags must win over env vars
	if cfg.Addr != ":7070" {
		t.Errorf("expected explicit flag Addr ':7070', got %q", cfg.Addr)
	}
	if cfg.AdminToken != "ti_adm_flag_token" {
		t.Errorf("expected explicit flag AdminToken 'ti_adm_flag_token', got %q", cfg.AdminToken)
	}
	if cfg.HeartbeatIntervalSec != 50 {
		t.Errorf("expected explicit flag Heartbeat 50, got %d", cfg.HeartbeatIntervalSec)
	}
	if cfg.MaxQueryTTLSec != 14400 {
		t.Errorf("expected explicit flag MaxQueryTTLSec 14400, got %d", cfg.MaxQueryTTLSec)
	}
	if cfg.RateLimits.QueriesPerMinute != 200 {
		t.Errorf("expected explicit flag RateLimitQPM 200, got %d", cfg.RateLimits.QueriesPerMinute)
	}

	// Unpassed flags must yield to env vars
	if cfg.DataDir != "/env/dir" {
		t.Errorf("expected env DataDir '/env/dir', got %q", cfg.DataDir)
	}
	if cfg.PublicURL != "https://env.url" {
		t.Errorf("expected env PublicURL 'https://env.url', got %q", cfg.PublicURL)
	}
	if cfg.DefaultQueryTTLSec != 1800 {
		t.Errorf("expected env DefaultQueryTTLSec 1800, got %d", cfg.DefaultQueryTTLSec)
	}
	if cfg.MaxProbeTimeoutSec != 60 {
		t.Errorf("expected env MaxProbeTimeoutSec 60, got %d", cfg.MaxProbeTimeoutSec)
	}
	if cfg.RateLimits.Burst != 20 {
		t.Errorf("expected env RateLimitBurst 20, got %d", cfg.RateLimits.Burst)
	}
}

// TestBuildHubConfig_InvalidEnvProducesClearError verifies that invalid env variables
// produce clear, actionable errors when building the hub configuration.
func TestBuildHubConfig_InvalidEnvProducesClearError(t *testing.T) {
	t.Setenv(config.EnvTalkIntentHeartbeat, "not_valid_interval")

	opts := HubOptions{
		Addr:    ":8080",
		DataDir: "./data",
	}

	_, err := BuildHubConfig(opts)
	if err == nil {
		t.Fatal("expected error for invalid TALKINTENT_HEARTBEAT_INTERVAL, got nil")
	}

	if !strings.Contains(err.Error(), "invalid TALKINTENT_HEARTBEAT_INTERVAL value") {
		t.Errorf("expected clear error message mentioning TALKINTENT_HEARTBEAT_INTERVAL, got %v", err)
	}
}

// TestAdminTokenHierarchy verifies the four-tier resolution hierarchy:
// flag > env > <data-dir>/admin.token > auto-generate.
func TestAdminTokenHierarchy(t *testing.T) {
	// Case 1: Flag beats env, file, and auto-gen
	t.Run("FlagWins", func(t *testing.T) {
		tempDir := t.TempDir()
		tokenFile := filepath.Join(tempDir, "admin.token")
		_ = os.WriteFile(tokenFile, []byte("ti_adm_file_token\n"), 0600)

		t.Setenv(config.EnvTalkIntentAdminToken, "ti_adm_env_token")

		opts := HubOptions{
			Addr:          ":8080",
			DataDir:       tempDir,
			AdminToken:    "ti_adm_flag_token",
			ExplicitFlags: map[string]bool{"admin-token": true},
		}

		cfg, err := BuildHubConfig(opts)
		if err != nil {
			t.Fatalf("BuildHubConfig failed: %v", err)
		}
		if cfg.AdminToken != "ti_adm_flag_token" {
			t.Errorf("expected flag token, got %q", cfg.AdminToken)
		}
	})

	// Case 2: Env beats file and auto-gen
	t.Run("EnvWinsOverFile", func(t *testing.T) {
		tempDir := t.TempDir()
		tokenFile := filepath.Join(tempDir, "admin.token")
		_ = os.WriteFile(tokenFile, []byte("ti_adm_file_token\n"), 0600)

		t.Setenv(config.EnvTalkIntentAdminToken, "ti_adm_env_token")

		opts := HubOptions{
			Addr:          ":8080",
			DataDir:       tempDir,
			ExplicitFlags: map[string]bool{},
		}

		cfg, err := BuildHubConfig(opts)
		if err != nil {
			t.Fatalf("BuildHubConfig failed: %v", err)
		}
		if cfg.AdminToken != "ti_adm_env_token" {
			t.Errorf("expected env token, got %q", cfg.AdminToken)
		}
	})

	// Case 3: File fallback when flag and env are empty
	t.Run("FileFallback", func(t *testing.T) {
		tempDir := t.TempDir()
		tokenFile := filepath.Join(tempDir, "admin.token")
		_ = os.WriteFile(tokenFile, []byte("ti_adm_file_token\n"), 0600)

		t.Setenv(config.EnvTalkIntentAdminToken, "")

		// ExecuteHub binds the listener even with a cancelled ctx; loopback + port 0 keeps
		// the test off 0.0.0.0 (which triggers the Windows Defender network prompt on every
		// freshly built test binary) and avoids fixed-port collisions.
		opts := HubOptions{
			Addr:          "127.0.0.1:0",
			DataDir:       tempDir,
			ExplicitFlags: map[string]bool{"data-dir": true, "addr": true},
		}

		cfg, err := BuildHubConfig(opts)
		if err != nil {
			t.Fatalf("BuildHubConfig failed: %v", err)
		}

		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // immediately cancel to test startup logic without blocking

		_ = ExecuteHub(ctx, opts, &stdout, &stderr)

		// Read token file to verify it wasn't overwritten
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			t.Fatalf("failed to read token file: %v", err)
		}
		if strings.TrimSpace(string(data)) != "ti_adm_file_token" {
			t.Errorf("expected existing file token preserved, got %q", string(data))
		}
		_ = cfg
	})

	// Case 4: Auto-generate when flag, env, and file are all empty
	t.Run("AutoGenerate", func(t *testing.T) {
		tempDir := t.TempDir()
		tokenFile := filepath.Join(tempDir, "admin.token")

		t.Setenv(config.EnvTalkIntentAdminToken, "")

		opts := HubOptions{
			Addr:          "127.0.0.1:0",
			DataDir:       tempDir,
			ExplicitFlags: map[string]bool{"data-dir": true, "addr": true},
		}

		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_ = ExecuteHub(ctx, opts, &stdout, &stderr)

		data, err := os.ReadFile(tokenFile)
		if err != nil {
			t.Fatalf("expected admin.token to be auto-generated, error: %v", err)
		}
		token := strings.TrimSpace(string(data))
		if !strings.HasPrefix(token, "ti_adm_") {
			t.Errorf("expected generated token to start with 'ti_adm_', got %q", token)
		}
		if len(token) != len("ti_adm_")+48 {
			t.Errorf("expected generated token length %d, got %d", len("ti_adm_")+48, len(token))
		}
	})
}

// TestHubStartupOutput_EffectiveSettingsAndRedaction verifies that Hub startup output
// in both text and -json formats reports effective addr, data-dir, and public-url,
// while strictly obeying Rule 5 (never printing raw admin tokens).
func TestHubStartupOutput_EffectiveSettingsAndRedaction(t *testing.T) {
	tempDir := t.TempDir()
	customAddr := "127.0.0.1:8999"
	customURL := "https://hub.corp.example.com"
	secretToken := "ti_adm_super_secret_never_leak_this_token"

	t.Setenv(config.EnvTalkIntentHubAddr, customAddr)
	t.Setenv(config.EnvTalkIntentHubDataDir, tempDir)
	t.Setenv(config.EnvTalkIntentAdminToken, secretToken)
	t.Setenv(config.EnvTalkIntentPublicURL, customURL)

	// Test Text output
	t.Run("TextOutput", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		opts := HubOptions{
			Addr:          config.DefaultHubAddr,
			DataDir:       tempDir,
			ExplicitFlags: map[string]bool{"data-dir": true},
			JSONOutput:    false,
		}

		_ = ExecuteHub(ctx, opts, &stdout, &stderr)
		out := stdout.String()

		if !strings.Contains(out, customAddr) {
			t.Errorf("text output missing effective address %q:\n%s", customAddr, out)
		}
		if !strings.Contains(out, tempDir) {
			t.Errorf("text output missing effective data dir %q:\n%s", tempDir, out)
		}
		if !strings.Contains(out, customURL) {
			t.Errorf("text output missing effective public URL %q:\n%s", customURL, out)
		}
		// Strict check: raw secret token MUST NEVER appear in output
		if strings.Contains(out, secretToken) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: raw admin token printed in text output!")
		}
		// Fingerprint must appear
		if !strings.Contains(out, "Admin Token Fingerprint: sha256:") {
			t.Errorf("text output missing admin token fingerprint label:\n%s", out)
		}
	})

	// Test JSON output
	t.Run("JSONOutput", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		opts := HubOptions{
			Addr:          config.DefaultHubAddr,
			DataDir:       tempDir,
			ExplicitFlags: map[string]bool{"data-dir": true},
			JSONOutput:    true,
		}

		_ = ExecuteHub(ctx, opts, &stdout, &stderr)
		out := stdout.String()

		var res map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
			t.Fatalf("failed to parse startup JSON output: %v\nOutput: %s", err, out)
		}

		if res["status"] != "starting" {
			t.Errorf("expected status 'starting', got %v", res["status"])
		}
		if res["addr"] != customAddr {
			t.Errorf("expected addr %q, got %v", customAddr, res["addr"])
		}
		if res["data_dir"] != tempDir {
			t.Errorf("expected data_dir %q, got %v", tempDir, res["data_dir"])
		}
		if res["public_url"] != customURL {
			t.Errorf("expected public_url %q, got %v", customURL, res["public_url"])
		}
		if fp, ok := res["admin_token_fp"].(string); !ok || len(fp) != 16 {
			t.Errorf("expected 16-char admin_token_fp, got %v", res["admin_token_fp"])
		}
		if tokenLen, ok := res["admin_token_len"].(float64); !ok || int(tokenLen) != len(secretToken) {
			t.Errorf("expected admin_token_len %d, got %v", len(secretToken), res["admin_token_len"])
		}
		// Strict check: raw secret token MUST NEVER appear anywhere in JSON output
		if strings.Contains(out, secretToken) {
			t.Fatalf("CRITICAL SECURITY VIOLATION: raw admin token found in JSON output!")
		}
	})
}

// TestHubServer_ObservableEnvEndToEnd proves end-to-end that setting hub environment variables
// changes the configuration actually passed to hub.NewServer and modifies Hub server behavior:
// 1. Addr, DataDir, PublicURL, and AdminToken are honored.
// 2. Admin authorization succeeds with the env token.
// 3. Invite generation uses the env PublicURL.
// 4. Rate limiting responds with 429 when rate-limit env thresholds are exceeded.
func TestHubServer_ObservableEnvEndToEnd(t *testing.T) {
	tempDir := t.TempDir()
	envToken := "ti_adm_e2e_observable_token"
	envPublicURL := "https://hub.customdomain.org:9443"

	t.Setenv(config.EnvTalkIntentHubAddr, "127.0.0.1:0") // let OS assign free port
	t.Setenv(config.EnvTalkIntentHubDataDir, tempDir)
	t.Setenv(config.EnvTalkIntentAdminToken, envToken)
	t.Setenv(config.EnvTalkIntentPublicURL, envPublicURL)
	t.Setenv(config.EnvTalkIntentHeartbeat, "15s")
	t.Setenv(config.EnvTalkIntentDefaultQueryTTL, "1800")
	t.Setenv(config.EnvTalkIntentMaxQueryTTL, "7200")
	t.Setenv(config.EnvTalkIntentMaxProbeTimeout, "30")
	t.Setenv(config.EnvTalkIntentRateLimitQPM, "1")   // 1 request per min
	t.Setenv(config.EnvTalkIntentRateLimitBurst, "1") // burst of 1

	opts := HubOptions{
		Addr:          config.DefaultHubAddr,
		DataDir:       "./data",
		ExplicitFlags: map[string]bool{},
	}

	cfg, err := BuildHubConfig(opts)
	if err != nil {
		t.Fatalf("BuildHubConfig failed: %v", err)
	}

	// Verify all config fields reflect env
	if cfg.DataDir != tempDir {
		t.Fatalf("expected cfg.DataDir %q, got %q", tempDir, cfg.DataDir)
	}
	if cfg.AdminToken != envToken {
		t.Fatalf("expected cfg.AdminToken %q, got %q", envToken, cfg.AdminToken)
	}
	if cfg.PublicURL != envPublicURL {
		t.Fatalf("expected cfg.PublicURL %q, got %q", envPublicURL, cfg.PublicURL)
	}
	if cfg.HeartbeatIntervalSec != 15 {
		t.Fatalf("expected cfg.HeartbeatIntervalSec 15, got %d", cfg.HeartbeatIntervalSec)
	}
	if cfg.MaxProbeTimeoutSec != 30 {
		t.Fatalf("expected cfg.MaxProbeTimeoutSec 30, got %d", cfg.MaxProbeTimeoutSec)
	}
	if cfg.RateLimits.QueriesPerMinute != 1 {
		t.Fatalf("expected cfg.RateLimits.QueriesPerMinute 1, got %d", cfg.RateLimits.QueriesPerMinute)
	}
	if cfg.RateLimits.Burst != 1 {
		t.Fatalf("expected cfg.RateLimits.Burst 1, got %d", cfg.RateLimits.Burst)
	}

	// Initialize store in the env data directory
	st, err := store.NewJSONLStore(cfg.DataDir)
	if err != nil {
		t.Fatalf("failed to create JSONL store: %v", err)
	}
	defer st.Close()

	srv, err := hub.NewServer(cfg, st, web.FS(), nil)
	if err != nil {
		t.Fatalf("hub.NewServer failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = srv.Start(ctx)
	}()

	// Wait for server to bind and listen
	var boundAddr string
	for i := 0; i < 50; i++ {
		boundAddr = srv.Addr()
		if boundAddr != "" && boundAddr != "127.0.0.1:0" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if boundAddr == "" {
		t.Fatal("timed out waiting for Hub server to listen")
	}

	baseURL := "http://" + boundAddr

	// 1. Verify Admin Token Authentication works with the env token
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/admin/invites", strings.NewReader(`{"name":"Alice"}`))
	if err != nil {
		t.Fatalf("failed to build invite request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+envToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("invite request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 200/201 from invite creation with env admin token, got %d", resp.StatusCode)
	}

	var inviteResp protocol.InviteCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&inviteResp); err != nil {
		t.Fatalf("failed to decode invite response: %v", err)
	}
	if inviteResp.Code == "" {
		t.Fatalf("expected non-empty invite code, got empty")
	}

	// 2. Verify PublicURL from env was returned upon pairing
	pairBody, _ := json.Marshal(protocol.PairRequest{
		InviteCode:    inviteResp.Code,
		MachineName:   "test-worker-node",
		ClientVersion: "1.0.0",
	})
	pairReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/pair", bytes.NewReader(pairBody))
	if err != nil {
		t.Fatalf("failed to build pair request: %v", err)
	}
	pairReq.Header.Set("Content-Type", "application/json")

	pairHTTPResp, err := client.Do(pairReq)
	if err != nil {
		t.Fatalf("pair request failed: %v", err)
	}
	defer pairHTTPResp.Body.Close()

	if pairHTTPResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from pairing, got %d", pairHTTPResp.StatusCode)
	}

	var pairResp protocol.PairResponse
	if err := json.NewDecoder(pairHTTPResp.Body).Decode(&pairResp); err != nil {
		t.Fatalf("failed to decode pair response: %v", err)
	}

	if pairResp.HubURL != envPublicURL {
		t.Errorf("expected pair response HubURL to equal env PublicURL %q, got %q", envPublicURL, pairResp.HubURL)
	}

	// 3. Verify Rate Limiting behavior matches env settings (QPM=1, Burst=1)
	// Query submit endpoint enforces rate limiting per member/IP
	submitQuery := func() int {
		queryReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/queries", strings.NewReader(`{"target":"Alice","query":"hello"}`))
		if err != nil {
			t.Fatalf("failed to build query request: %v", err)
		}
		queryReq.Header.Set("Authorization", "Bearer "+pairResp.Token)
		queryReq.Header.Set("Content-Type", "application/json")

		r, err := client.Do(queryReq)
		if err != nil {
			t.Fatalf("query request failed: %v", err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}

	// First query: within burst=1, should be processed
	status1 := submitQuery()
	if status1 == http.StatusTooManyRequests {
		t.Fatalf("first query was unexpectedly rate limited")
	}

	// Second query immediately after: burst=1 exceeded, must receive HTTP 429 Too Many Requests
	status2 := submitQuery()
	if status2 != http.StatusTooManyRequests {
		t.Errorf("expected HTTP 429 Too Many Requests due to TALKINTENT_RATE_LIMIT_BURST=1, got %d", status2)
	}

	// 4. Verify data directory from env is populated with hub storage files
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("failed to read env data directory: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf("expected files written into env data directory %s, but found none", tempDir)
	}
}
