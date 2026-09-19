package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sskift/talkintent/test/mockllm"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := runContext(ctx, os.Args[1:]); err != nil {
		slog.Error("mockllm failed", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runContext(ctx, args)
}

func runContext(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mockllm", flag.ContinueOnError)

	addrFlag := fs.String("addr", getEnv("MOCKLLM_ADDR", ":8080"), "Address to listen on (e.g. :8080 or 127.0.0.1:8080)")
	modeFlag := fs.String("mode", getEnv("MOCKLLM_MODE", mockllm.ModeAuto), "Dialect mode: auto, openai, anthropic, or all")
	cannedFlag := fs.String("canned", getEnv("MOCKLLM_CANNED_RESPONSE", ""), "Canned answer override for non-tool or forced answers")
	refuseFlag := fs.String("refuse", getEnv("MOCKLLM_REFUSE_QUERY", ""), "Substring that triggers immediate refusal without tool use")
	refusalTextFlag := fs.String("refusal-text", getEnv("MOCKLLM_REFUSAL_TEXT", mockllm.DefaultRefusalText), "Refusal response text")
	thinkingFlag := fs.Bool("thinking", getEnvBool("MOCKLLM_EMIT_THINKING", false), "Emit an Anthropic thinking block before tool_use")
	latencyFlag := fs.Duration("latency", getEnvDuration("MOCKLLM_LATENCY", 0), "Simulated artificial latency (e.g. 50ms, 1s)")
	failOnceFlag := fs.Bool("fail-once", getEnvBool("MOCKLLM_FAIL_ONCE", false), "Inject an HTTP 500 internal server error once")
	toolsFlag := fs.String("tools", getEnv("MOCKLLM_TOOLS", "git_status,git_diff"), "Comma-separated ordered list of tools to invoke")
	authFlag := fs.Bool("require-auth", getEnvBool("MOCKLLM_REQUIRE_AUTH", false), "Require valid Authorization or x-api-key header")

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := mockllm.Config{
		CannedResponse:    *cannedFlag,
		RefuseSubstring:   *refuseFlag,
		RefusalText:       *refusalTextFlag,
		EmitThinkingBlock: *thinkingFlag,
		Latency:           *latencyFlag,
		FailOnce500:       *failOnceFlag,
		RequireAuth:       *authFlag,
		Mode:              strings.ToLower(strings.TrimSpace(*modeFlag)),
	}

	if *toolsFlag != "" {
		parts := strings.Split(*toolsFlag, ",")
		var cleanParts []string
		for _, p := range parts {
			t := strings.TrimSpace(p)
			if t != "" {
				cleanParts = append(cleanParts, t)
			}
		}
		if len(cleanParts) > 0 {
			cfg.ScriptedTools = cleanParts
		}
	}

	serverState := mockllm.NewServerState(cfg)
	handler := serverState.Handler()

	httpServer := &http.Server{
		Addr:         *addrFlag,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	serverErrCh := make(chan error, 1)
	go func() {
		slog.Info("starting mockllm server",
			"addr", *addrFlag,
			"mode", cfg.Mode,
			"thinking", cfg.EmitThinkingBlock,
			"fail_once", cfg.FailOnce500,
			"latency", cfg.Latency,
			"tools", cfg.ScriptedTools,
		)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrCh <- err
		}
	}()

	select {
	case err := <-serverErrCh:
		return fmt.Errorf("server listener failed: %w", err)
	case <-ctx.Done():
		slog.Info("stopping mockllm server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val := os.Getenv(key); val != "" {
		v := strings.ToLower(strings.TrimSpace(val))
		return v == "1" || v == "true" || v == "yes"
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultVal
}
