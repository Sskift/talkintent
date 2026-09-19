package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
)

// MembersOptions contains flags for the members command.
type MembersOptions struct {
	ConfigPath string
	JSONOutput bool
}

// ExecuteMembers fetches the member directory from the Hub and displays it.
func ExecuteMembers(ctx context.Context, opts MembersOptions, stdout, stderr io.Writer) int {
	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: failed to load client config: %v. Run 'talkintent pair' first.\n", err)
		return 1
	}

	if strings.TrimSpace(cfg.HubURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		fmt.Fprintf(stderr, "Error: client node is not paired. Run 'talkintent pair' first.\n")
		return 1
	}

	members, err := FetchMembers(ctx, cfg.HubURL, cfg.Token)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching member directory: %v\n", err)
		return 1
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"hub_url": cfg.HubURL,
			"total":   len(members),
			"members": members,
		})
		return 0
	}

	fmt.Fprintf(stdout, "Connected Hub: %s\n\n", cfg.HubURL)
	PrintMembersTable(stdout, members)
	return 0
}
