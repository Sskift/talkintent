package cli

import (
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
)

// WebOptions holds arguments for the web command.
type WebOptions struct {
	ConfigPath string
	Open       bool
	JSONOutput bool
}

// ExecuteWeb prints the Web Dashboard URL (strictly WITHOUT token per F11 and Rule 5)
// and optionally opens the system default browser.
func ExecuteWeb(opts WebOptions, stdout, stderr io.Writer) int {
	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	hubURL := "http://localhost:8080"
	if err == nil && cfg != nil && strings.TrimSpace(cfg.HubURL) != "" {
		hubURL = strings.TrimRight(cfg.HubURL, "/")
	}

	dashboardURL := hubURL + "/web"

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"url":    dashboardURL,
			"status": "ready",
		})
	} else {
		fmt.Fprintf(stdout, "TalkIntent Web Dashboard URL:\n\n  %s\n\n", dashboardURL)
		fmt.Fprintf(stdout, "(Note: Dashboard URL contains no credentials. Authenticate via Web UI prompt)\n")
	}

	if opts.Open {
		if err := openBrowser(dashboardURL); err != nil {
			fmt.Fprintf(stderr, "Failed to open browser automatically: %v\n", err)
			return 1
		}
	}

	return 0
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
