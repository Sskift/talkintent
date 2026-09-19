package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/Sskift/talkintent/internal/config"
)

// RunPair exchanges an invite code with the Hub and saves local config.
// Kept for backward compatibility with existing callers.
func RunPair(ctx context.Context, hubURL, code, machineName string) error {
	opts := PairOptions{
		HubURL:      hubURL,
		InviteCode:  code,
		MachineName: machineName,
	}
	codeInt := ExecutePair(ctx, opts, os.Stdout, os.Stderr)
	if codeInt != 0 {
		return fmt.Errorf("pairing failed with exit code %d", codeInt)
	}
	return nil
}

// RunAsk submits a query to a teammate's dev workspace.
// Kept for backward compatibility with existing callers.
func RunAsk(ctx context.Context, target, queryText string, wait bool) error {
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w. Have you run 'talkintent pair'?", err)
	}

	opts := RunAskOptions{
		Target: target,
		Query:  queryText,
		Wait:   wait,
	}

	codeInt := ExecuteAsk(ctx, cfg, opts, os.Stdout, os.Stderr)
	if codeInt != 0 {
		return fmt.Errorf("ask failed with exit code %d", codeInt)
	}
	return nil
}

// RunPrivacyTest performs a local dry-run probe execution without sending any network traffic.
// Kept for backward compatibility with existing callers.
func RunPrivacyTest(ctx context.Context, queryText string) error {
	opts := PrivacyOptions{
		Subcommand: "test",
		Query:      queryText,
	}
	codeInt := ExecutePrivacy(ctx, opts, os.Stdout, os.Stderr)
	if codeInt != 0 {
		return fmt.Errorf("privacy test failed with exit code %d", codeInt)
	}
	return nil
}
