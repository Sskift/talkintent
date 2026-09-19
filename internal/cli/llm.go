package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe/llm"
)

// LLMOptions holds flags and arguments for the llm subcommand.
type LLMOptions struct {
	ConfigPath         string
	Subcommand         string // "show", "set", "test"
	Provider           string
	BaseURL            string
	APIKey             string
	Model              string
	MaxTokens          int
	Temperature        float64
	CAFile             string
	TLSServerName      string
	InsecureSkipVerify bool
	RequestTimeoutSec  int
	MaxSteps           int
	JSONOutput         bool
}

// ExecuteLLM dispatches llm subcommands: show, set, test.
func ExecuteLLM(ctx context.Context, opts LLMOptions, stdout, stderr io.Writer) int {
	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err != nil {
		cfg = &config.ClientConfig{
			Workspaces:           []config.WorkspaceConfig{},
			MaxConcurrency:       config.DefaultMaxConcurrency,
			HeartbeatIntervalSec: config.DefaultHeartbeatIntervalSec,
		}
	}

	switch opts.Subcommand {
	case "set":
		return executeLLMSet(cfg, opts, stdout, stderr)
	case "test":
		return executeLLMTest(ctx, cfg, opts, stdout, stderr)
	case "show", "get", "":
		return executeLLMShow(cfg, opts, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown llm subcommand %q. Use 'show', 'set', or 'test'.\n", opts.Subcommand)
		return 1
	}
}

func executeLLMShow(cfg *config.ClientConfig, opts LLMOptions, stdout, stderr io.Writer) int {
	redacted := cfg.LLM.Redacted()

	if opts.JSONOutput {
		_ = PrintJSON(stdout, redacted)
		return 0
	}

	fmt.Fprintf(stdout, "Current Local LLM Configuration:\n")
	fmt.Fprintf(stdout, "  Provider:             %s\n", redacted.Provider)
	fmt.Fprintf(stdout, "  Base URL:             %s\n", redacted.BaseURL)
	fmt.Fprintf(stdout, "  Model:                %s\n", redacted.Model)
	fmt.Fprintf(stdout, "  API Key:              %s\n", redacted.APIKey)
	fmt.Fprintf(stdout, "  Max Tokens:           %d\n", redacted.Tokens())
	fmt.Fprintf(stdout, "  Temperature:          %.2f\n", redacted.Temperature)
	if redacted.CAFile != "" {
		fmt.Fprintf(stdout, "  CA File:              %s\n", redacted.CAFile)
	}
	if redacted.TLSServerName != "" {
		fmt.Fprintf(stdout, "  TLS Server Name:      %s\n", redacted.TLSServerName)
	}
	if redacted.InsecureSkipVerify {
		fmt.Fprintf(stdout, "  Insecure Skip Verify: true (WARNING: TLS verification disabled)\n")
	}
	fmt.Fprintf(stdout, "  Request Timeout:      %ds\n", int(redacted.Timeout().Seconds()))
	fmt.Fprintf(stdout, "  Max Steps:            %d\n", redacted.Steps())
	return 0
}

func executeLLMSet(cfg *config.ClientConfig, opts LLMOptions, stdout, stderr io.Writer) int {
	updated := false

	if opts.Provider != "" {
		cfg.LLM.Provider = strings.ToLower(strings.TrimSpace(opts.Provider))
		updated = true
	}
	if opts.BaseURL != "" {
		cfg.LLM.BaseURL = strings.TrimSpace(opts.BaseURL)
		updated = true
	}
	if opts.APIKey != "" {
		cfg.LLM.APIKey = strings.TrimSpace(opts.APIKey)
		updated = true
	}
	if opts.Model != "" {
		cfg.LLM.Model = strings.TrimSpace(opts.Model)
		updated = true
	}
	if opts.MaxTokens > 0 {
		cfg.LLM.MaxTokens = opts.MaxTokens
		updated = true
	}
	if opts.Temperature > 0 {
		cfg.LLM.Temperature = opts.Temperature
		updated = true
	}
	if opts.CAFile != "" {
		cfg.LLM.CAFile = strings.TrimSpace(opts.CAFile)
		updated = true
	}
	if opts.TLSServerName != "" {
		cfg.LLM.TLSServerName = strings.TrimSpace(opts.TLSServerName)
		updated = true
	}
	if opts.InsecureSkipVerify {
		cfg.LLM.InsecureSkipVerify = true
		updated = true
	}
	if opts.RequestTimeoutSec > 0 {
		cfg.LLM.RequestTimeoutSeconds = opts.RequestTimeoutSec
		updated = true
	}
	if opts.MaxSteps > 0 {
		cfg.LLM.MaxSteps = opts.MaxSteps
		updated = true
	}

	if !updated {
		fmt.Fprintf(stderr, "No LLM configuration options specified. Use flags like --provider, --base-url, --api-key, --model.\n")
		return 1
	}

	if err := config.SaveClientConfig(opts.ConfigPath, cfg); err != nil {
		fmt.Fprintf(stderr, "Failed to save configuration: %v\n", err)
		return 1
	}

	targetPath := opts.ConfigPath
	if targetPath == "" {
		targetPath = config.DefaultClientConfigPath()
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status":      "success",
			"config_path": targetPath,
			"llm":         cfg.LLM.Redacted(),
		})
		return 0
	}

	fmt.Fprintf(stdout, "LLM configuration updated successfully in %s (0600).\n", targetPath)
	return 0
}

func executeLLMTest(ctx context.Context, cfg *config.ClientConfig, opts LLMOptions, stdout, stderr io.Writer) int {
	if cfg.LLM.Provider == "" || cfg.LLM.Model == "" {
		fmt.Fprintf(stderr, "Error: LLM provider or model not configured. Run 'talkintent llm set' first.\n")
		return 1
	}

	provider, err := llm.NewProvider(cfg.LLM)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to initialize LLM provider: %v\n", err)
		return 1
	}

	req := &llm.ChatRequest{
		Model: cfg.LLM.Model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "Hello, respond with pong"},
		},
		Temperature: 0.0,
		MaxTokens:   16,
	}

	resp, err := provider.ChatWithTools(ctx, req)
	if err != nil {
		fmt.Fprintf(stderr, "LLM test call failed: %v\n", err)
		return 1
	}

	// Adhere strictly to F24/F39 & Rule 5: print ONLY status/model/token counts!
	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status":            "OK",
			"provider":          cfg.LLM.Provider,
			"model":             cfg.LLM.Model,
			"prompt_tokens":     resp.Usage.PromptTokens,
			"completion_tokens": resp.Usage.CompletionTokens,
			"total_tokens":      resp.Usage.TotalTokens,
		})
		return 0
	}

	fmt.Fprintf(stdout, "LLM test call successful!\n")
	fmt.Fprintf(stdout, "  Status:            OK\n")
	fmt.Fprintf(stdout, "  Provider:          %s\n", cfg.LLM.Provider)
	fmt.Fprintf(stdout, "  Model:             %s\n", cfg.LLM.Model)
	fmt.Fprintf(stdout, "  Prompt Tokens:     %d\n", resp.Usage.PromptTokens)
	fmt.Fprintf(stdout, "  Completion Tokens: %d\n", resp.Usage.CompletionTokens)
	fmt.Fprintf(stdout, "  Total Tokens:      %d\n", resp.Usage.TotalTokens)
	return 0
}
