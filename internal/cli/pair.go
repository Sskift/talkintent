package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
)

// PairOptions contains flags for the pair subcommand.
type PairOptions struct {
	HubURL      string
	InviteCode  string
	MachineName string
	ConfigPath  string
	JSONOutput  bool
}

// ExecutePair handles the pairing exchange with Hub and writes 0600 config.
func ExecutePair(ctx context.Context, opts PairOptions, stdout, stderr io.Writer) int {
	hubURL := strings.TrimSpace(opts.HubURL)
	if hubURL == "" {
		hubURL = strings.TrimSpace(os.Getenv(config.EnvTalkIntentHubURL))
	}
	inviteCode := strings.TrimSpace(opts.InviteCode)

	if hubURL == "" || inviteCode == "" {
		fmt.Fprintf(stderr, "Error: both --hub and --code are required (e.g. talkintent pair --hub http://hub:8080 --code INV-XXXX)\n")
		return 1
	}

	machineName := opts.MachineName
	if machineName == "" {
		host, _ := os.Hostname()
		machineName = host
	}

	reqBody := protocol.PairRequest{
		InviteCode:    inviteCode,
		MachineName:   machineName,
		ClientVersion: "1.0.0",
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(stderr, "Error encoding pair request: %v\n", err)
		return 1
	}

	url := strings.TrimRight(hubURL, "/") + "/api/v1/auth/pair"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(stderr, "Error creating pair request: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "Pairing request failed: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp protocol.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		msg := errResp.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		fmt.Fprintf(stderr, "Pairing rejected: %s\n", msg)
		return 1
	}

	var pairResp protocol.PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&pairResp); err != nil {
		fmt.Fprintf(stderr, "Failed to decode pairing response: %v\n", err)
		return 1
	}

	// Load existing configuration if present to preserve LLM settings and workspaces
	configPath := opts.ConfigPath
	cfg, err := config.LoadClientConfig(configPath)
	if err != nil || cfg == nil {
		cfg = &config.ClientConfig{
			Workspaces:           []config.WorkspaceConfig{},
			MaxConcurrency:       config.DefaultMaxConcurrency,
			HeartbeatIntervalSec: config.DefaultHeartbeatIntervalSec,
		}
	}

	cfg.HubURL = hubURL
	cfg.MemberID = pairResp.MemberID
	cfg.MemberName = pairResp.MemberName
	cfg.Token = pairResp.Token
	cfg.MachineName = machineName

	if err := config.SaveClientConfig(configPath, cfg); err != nil {
		fmt.Fprintf(stderr, "Failed to save configuration: %v\n", err)
		return 1
	}

	savedPath := configPath
	if savedPath == "" {
		savedPath = config.DefaultClientConfigPath()
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status":      "success",
			"member_id":   pairResp.MemberID,
			"member_name": pairResp.MemberName,
			"hub_url":     hubURL,
			"machine":     machineName,
			"config_path": savedPath,
		})
		return 0
	}

	fmt.Fprintf(stdout, "Successfully paired with TalkIntent Hub!\n")
	fmt.Fprintf(stdout, "  Member ID:   %s\n", pairResp.MemberID)
	fmt.Fprintf(stdout, "  Member Name: %s\n", pairResp.MemberName)
	fmt.Fprintf(stdout, "  Hub URL:     %s\n", hubURL)
	fmt.Fprintf(stdout, "  Machine:     %s\n", machineName)
	fmt.Fprintf(stdout, "  Config saved: %s (0600)\n", savedPath)
	return 0
}
