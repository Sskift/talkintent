package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
)

// HistoryOptions contains flags for the history command.
type HistoryOptions struct {
	ConfigPath string
	Inbound    bool
	Outbound   bool
	Limit      int
	Offset     int
	JSONOutput bool
}

// ExecuteHistory retrieves and renders inbound or outbound audit history.
func ExecuteHistory(ctx context.Context, opts HistoryOptions, stdout, stderr io.Writer) int {
	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: failed to load client config: %v. Run 'talkintent pair' first.\n", err)
		return 1
	}

	if strings.TrimSpace(cfg.HubURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		fmt.Fprintf(stderr, "Error: client node is not paired. Run 'talkintent pair' first.\n")
		return 1
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := opts.Offset
	if offset < 0 {
		offset = 0
	}

	endpoint := "outbound"
	title := "Outbound Queries"
	if opts.Inbound {
		endpoint = "inbound"
		title = "Inbound Queries (who queried my workspace)"
	}

	url := fmt.Sprintf("%s/api/v1/audit/%s?limit=%d&offset=%d",
		strings.TrimRight(cfg.HubURL, "/"), endpoint, limit, offset)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		fmt.Fprintf(stderr, "Error creating audit request: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	client, err := config.NewHubHTTPClient(cfg.HubCAFile, 15*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to create http client: %v\n", err)
		return 1
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to retrieve audit history: %v\n", err)
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
		fmt.Fprintf(stderr, "Failed to retrieve audit history: %s\n", msg)
		return 1
	}

	var auditResp protocol.AuditListResponse
	if err := json.NewDecoder(resp.Body).Decode(&auditResp); err != nil {
		fmt.Fprintf(stderr, "Failed to decode audit response: %v\n", err)
		return 1
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, auditResp)
		return 0
	}

	PrintAuditTable(stdout, auditResp.Entries, title)
	return 0
}
