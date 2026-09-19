package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
)

// InviteOptions holds arguments for the invite subcommand.
type InviteOptions struct {
	ConfigPath     string
	HubURL         string
	AdminToken     string
	TargetName     string
	Aliases        []string
	ExpiresInHours int
	JSONOutput     bool
}

// ExecuteInvite calls the Hub REST endpoint to generate a new onboarding invite code.
func ExecuteInvite(ctx context.Context, opts InviteOptions, stdout, stderr io.Writer) int {
	hubURL := strings.TrimRight(opts.HubURL, "/")
	if hubURL == "" {
		if cfg, err := config.LoadClientConfig(opts.ConfigPath); err == nil && cfg != nil && cfg.HubURL != "" {
			hubURL = strings.TrimRight(cfg.HubURL, "/")
		} else if env := os.Getenv("TALKINTENT_HUB_URL"); env != "" {
			hubURL = strings.TrimRight(env, "/")
		} else {
			hubURL = "http://localhost:8080"
		}
	}

	adminToken := opts.AdminToken
	if adminToken == "" {
		adminToken = os.Getenv("TALKINTENT_ADMIN_TOKEN")
	}
	if adminToken == "" {
		// Attempt to read from default Hub data dir admin.token
		dataDir := os.Getenv("TALKINTENT_DATA_DIR")
		if dataDir == "" {
			dataDir = "./data"
		}
		tokenPath := filepath.Join(dataDir, "admin.token")
		if data, err := os.ReadFile(tokenPath); err == nil {
			adminToken = strings.TrimSpace(string(data))
		}
	}

	if adminToken == "" {
		fmt.Fprintf(stderr, "Error: admin token required to create invites. Use --admin-token or set $TALKINTENT_ADMIN_TOKEN.\n")
		return 1
	}

	name := strings.TrimSpace(opts.TargetName)
	if name == "" {
		fmt.Fprintf(stderr, "Error: --name is required to specify target member name.\n")
		return 1
	}

	expires := opts.ExpiresInHours
	if expires <= 0 {
		expires = 72
	}

	reqBody := protocol.InviteCreateRequest{
		TargetName:     name,
		Aliases:        opts.Aliases,
		ExpiresInHours: expires,
	}

	b, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to encode invite request: %v\n", err)
		return 1
	}

	url := hubURL + "/api/v1/admin/invites"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		fmt.Fprintf(stderr, "Failed to create HTTP request: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to connect to Hub at %s: %v\n", hubURL, err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var errResp protocol.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		msg := errResp.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		fmt.Fprintf(stderr, "Failed to create invite: %s\n", msg)
		return 1
	}

	var inviteResp protocol.InviteCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&inviteResp); err != nil {
		fmt.Fprintf(stderr, "Failed to decode invite response: %v\n", err)
		return 1
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, inviteResp)
		return 0
	}

	expiresTime := time.UnixMilli(inviteResp.ExpiresAt).Format("2006-01-02 15:04:05")
	fmt.Fprintf(stdout, "Created Invite Code for member: %s\n", inviteResp.TargetName)
	fmt.Fprintf(stdout, "  Invite Code: %s\n", inviteResp.Code)
	fmt.Fprintf(stdout, "  Expires At:  %s\n\n", expiresTime)
	fmt.Fprintf(stdout, "The member can join using:\n")
	fmt.Fprintf(stdout, "  talkintent pair --hub %s --code %s\n", hubURL, inviteResp.Code)
	return 0
}
