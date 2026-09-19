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

// HubOptions contains flags for running the Hub server.
type HubOptions struct {
	Addr       string
	DataDir    string
	AdminToken string
	PublicURL  string
	JSONOutput bool
}

// ExecuteHub starts the central coordination Hub server.
// Respects Rule 5: never prints raw admin token, only SHA256 fingerprint and length!
func ExecuteHub(ctx context.Context, opts HubOptions, stdout, stderr io.Writer) int {
	addr := opts.Addr
	if addr == "" {
		addr = ":8080"
	}
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = "./data"
	}

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		fmt.Fprintf(stderr, "Failed to create data directory %q: %v\n", dataDir, err)
		return 1
	}

	adminToken := strings.TrimSpace(opts.AdminToken)
	if adminToken == "" {
		adminToken = strings.TrimSpace(os.Getenv("TALKINTENT_ADMIN_TOKEN"))
	}

	tokenPath := filepath.Join(dataDir, "admin.token")
	if adminToken == "" {
		if data, err := os.ReadFile(tokenPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			adminToken = strings.TrimSpace(string(data))
		} else {
			randPart, err := store.RandomHex(24)
			if err != nil {
				fmt.Fprintf(stderr, "Failed to generate random admin token: %v\n", err)
				return 1
			}
			adminToken = "ti_adm_" + randPart
			if err := os.WriteFile(tokenPath, []byte(adminToken+"\n"), 0600); err != nil {
				fmt.Fprintf(stderr, "Failed to write admin token file: %v\n", err)
				return 1
			}
		}
	}

	// Compute fingerprint (SHA256 hash first 16 chars)
	hasher := sha256.New()
	hasher.Write([]byte(adminToken))
	fp := hex.EncodeToString(hasher.Sum(nil))[:16]

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status":            "starting",
			"addr":              addr,
			"data_dir":          dataDir,
			"admin_token_fp":    fp,
			"admin_token_len":   len(adminToken),
			"admin_token_store": tokenPath,
		})
	} else {
		fmt.Fprintf(stdout, "TalkIntent Hub Server starting...\n")
		fmt.Fprintf(stdout, "  Listen Address:        %s\n", addr)
		fmt.Fprintf(stdout, "  Data Directory:        %s\n", dataDir)
		fmt.Fprintf(stdout, "  Admin Token File:      %s (mode 0600)\n", tokenPath)
		fmt.Fprintf(stdout, "  Admin Token Fingerprint: sha256:%s... (length: %d chars)\n", fp, len(adminToken))
		fmt.Fprintf(stdout, "  (Rule 5: full credentials are never logged or echoed to console)\n\n")
	}

	st, err := store.NewJSONLStore(dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to initialize JSONL storage in %s: %v\n", dataDir, err)
		return 1
	}
	defer st.Close()

	cfg := &config.HubConfig{
		Addr:       addr,
		DataDir:    dataDir,
		AdminToken: adminToken,
		PublicURL:  opts.PublicURL,
	}

	logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{
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
