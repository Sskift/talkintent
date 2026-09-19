package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/protocol"
)

// RunPair exchanges an invite code with the Hub and saves local config.
func RunPair(ctx context.Context, hubURL, code, machineName string) error {
	if hubURL == "" || code == "" {
		return fmt.Errorf("both --hub and --code are required")
	}

	if machineName == "" {
		host, _ := os.Hostname()
		machineName = host
	}

	reqBody := protocol.PairRequest{
		InviteCode:    code,
		MachineName:   machineName,
		ClientVersion: "1.0.0",
	}
	b, _ := json.Marshal(reqBody)

	url := strings.TrimRight(hubURL, "/") + "/api/v1/auth/pair"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("pairing request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp protocol.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("pairing rejected (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
	}

	var pairResp protocol.PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&pairResp); err != nil {
		return err
	}

	cfg := &config.ClientConfig{
		HubURL:               hubURL,
		MemberID:             pairResp.MemberID,
		MemberName:           pairResp.MemberName,
		Token:                pairResp.Token,
		MachineName:          machineName,
		Workspaces:           []config.WorkspaceConfig{},
		MaxConcurrency:       config.DefaultMaxConcurrency,
		HeartbeatIntervalSec: config.DefaultHeartbeatIntervalSec,
	}

	if err := config.SaveClientConfig("", cfg); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Successfully paired with TalkIntent Hub!\n")
	fmt.Printf("  Member ID:   %s\n", pairResp.MemberID)
	fmt.Printf("  Member Name: %s\n", pairResp.MemberName)
	fmt.Printf("  Config saved: %s\n", config.DefaultClientConfigPath())
	return nil
}

// RunAsk submits a query to a teammate's dev workspace.
func RunAsk(ctx context.Context, target, queryText string, wait bool) error {
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w. Have you run 'talkintent pair'?", err)
	}

	reqBody := protocol.QuerySubmitRequest{
		Target:          target,
		Query:           queryText,
		TimeoutSeconds:  60,
		TTLSeconds:      86400,
	}
	b, _ := json.Marshal(reqBody)

	url := strings.TrimRight(cfg.HubURL, "/") + "/api/v1/queries"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("submit query failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		var errResp protocol.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("query rejected (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
	}

	var submitResp protocol.QuerySubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
		return err
	}

	fmt.Printf("Query submitted [ID: %s] -> %s (status: %s)\n", submitResp.QueryID, submitResp.TargetMemberName, submitResp.Status)

	if !wait {
		return nil
	}

	fmt.Printf("Waiting for on-site probe response...\n")
	detailURL := fmt.Sprintf("%s/api/v1/queries/%s?wait=30s", strings.TrimRight(cfg.HubURL, "/"), submitResp.QueryID)
	getReq, err := http.NewRequestWithContext(ctx, "GET", detailURL, nil)
	if err != nil {
		return err
	}
	getReq.Header.Set("Authorization", "Bearer "+cfg.Token)

	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return err
	}
	defer getResp.Body.Close()

	var detail protocol.QueryDetailResponse
	if err := json.NewDecoder(getResp.Body).Decode(&detail); err != nil {
		return err
	}

	fmt.Printf("\n=== Response from %s ===\n", detail.TargetMemberName)
	if detail.Status == protocol.QueryStatusCompleted {
		fmt.Println(detail.Answer)
		fmt.Printf("\n[Tools used: %s | Elapsed: %dms]\n", strings.Join(detail.ToolsUsed, ", "), detail.DurationMS)
	} else {
		fmt.Printf("Status: %s (Message: %s)\n", detail.Status, detail.ErrorMessage)
	}

	return nil
}

// RunPrivacyTest performs a local dry-run probe execution without sending any network traffic.
func RunPrivacyTest(ctx context.Context, queryText string) error {
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		return fmt.Errorf("failed to load local config: %w", err)
	}

	var ws config.WorkspaceConfig
	if len(cfg.Workspaces) > 0 {
		ws = cfg.Workspaces[0]
	} else {
		cwd, _ := os.Getwd()
		ws = config.WorkspaceConfig{
			Name:     "current-dir",
			RootPath: cwd,
		}
	}

	fmt.Printf("Running local privacy guardrail dry-run for query: %q\n", queryText)
	fmt.Printf("Target workspace: %s (%s)\n", ws.Name, ws.RootPath)

	rules := probe.LoadPrivacyPrompts("", ws.PrivacyPromptPath, ws.RootPath)
	fmt.Printf("\nLoaded Privacy Rules:\n---\n%s\n---\n", rules)

	agent := probe.NewDefaultAgent(nil)
	req := &probe.RunRequest{
		QueryID:   "test_privacy_dryrun",
		Query:     queryText,
		Workspace: ws,
		Timeout:   30 * time.Second,
	}

	res, err := agent.Run(ctx, req)
	if err != nil {
		return fmt.Errorf("local probe test failed: %w", err)
	}

	fmt.Printf("\nDry-Run Outcome:\n")
	fmt.Printf("  Status: %s\n", res.Status)
	fmt.Printf("  Answer: %s\n", res.Answer)
	fmt.Printf("  Tools:  %v\n", res.ToolsUsed)
	return nil
}
