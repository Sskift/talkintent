package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Sskift/talkintent/internal/config"
)

// StatusOptions holds arguments for the status subcommand.
type StatusOptions struct {
	ConfigPath string
	JSONOutput bool
}

// DaemonRuntimeInfo captures daemon process status.
type DaemonRuntimeInfo struct {
	Running bool   `json:"running"`
	PID     int    `json:"pid,omitempty"`
	Status  string `json:"status"`
}

// HubStatusInfo captures hub connection status.
type HubStatusInfo struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

// StatusReport provides a comprehensive overview of TalkIntent node health.
type StatusReport struct {
	Home       string            `json:"home"`
	ConfigPath string            `json:"config_path"`
	Paired     bool              `json:"paired"`
	MemberName string            `json:"member_name,omitempty"`
	MemberID   string            `json:"member_id,omitempty"`
	Machine    string            `json:"machine,omitempty"`
	Daemon     DaemonRuntimeInfo `json:"daemon"`
	Hub        HubStatusInfo     `json:"hub"`
	Workspaces []string          `json:"workspaces"`
	LLM        config.LLMConfig  `json:"llm"`
}

// ExecuteStatus inspects daemon status, hub reachability, and local configuration.
func ExecuteStatus(ctx context.Context, opts StatusOptions, stdout, stderr io.Writer) int {
	homeDir := config.GetTalkIntentHome()
	cfgPath := opts.ConfigPath
	if cfgPath == "" {
		cfgPath = config.DefaultClientConfigPath()
	}

	report := StatusReport{
		Home:       homeDir,
		ConfigPath: cfgPath,
		Workspaces: []string{},
	}

	// 1. Inspect daemon runtime process
	pidPath := filepath.Join(homeDir, "daemon.pid")
	if data, err := os.ReadFile(pidPath); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			report.Daemon.PID = pid
			if isProcessRunning(pid) {
				report.Daemon.Running = true
				report.Daemon.Status = "running"
			} else {
				report.Daemon.Running = false
				report.Daemon.Status = "stale_pid_file"
			}
		}
	}
	if !report.Daemon.Running && report.Daemon.Status == "" {
		report.Daemon.Status = "stopped"
	}

	// 2. Load configuration
	cfg, err := config.LoadClientConfig(cfgPath)
	if err != nil || cfg == nil {
		report.Paired = false
		if opts.JSONOutput {
			_ = PrintJSON(stdout, report)
			return 0
		}
		printStatusText(stdout, report)
		return 0
	}

	report.Paired = true
	report.MemberName = cfg.MemberName
	report.MemberID = cfg.MemberID
	report.Machine = cfg.MachineName
	report.LLM = cfg.LLM.Redacted()

	for _, ws := range cfg.Workspaces {
		report.Workspaces = append(report.Workspaces, fmt.Sprintf("%s (%s)", ws.Name, ws.RootPath))
	}

	// 3. Check Hub reachability
	if cfg.HubURL != "" {
		report.Hub.URL = cfg.HubURL
		hubURL := strings.TrimRight(cfg.HubURL, "/")
		checkURL := hubURL + "/api/v1/members/me"

		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(checkCtx, "GET", checkURL, nil)
		if err == nil {
			if cfg.Token != "" {
				req.Header.Set("Authorization", "Bearer "+cfg.Token)
			}
			start := time.Now()
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Do(req)
			if err == nil {
				report.Hub.Reachable = true
				report.Hub.LatencyMS = time.Since(start).Milliseconds()
				resp.Body.Close()
			} else {
				report.Hub.Reachable = false
				report.Hub.Error = err.Error()
			}
		}
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, report)
		return 0
	}

	printStatusText(stdout, report)
	return 0
}

func printStatusText(w io.Writer, rep StatusReport) {
	fmt.Fprintf(w, "=== TalkIntent Node Status ===\n")
	fmt.Fprintf(w, "  Home Directory: %s\n", rep.Home)
	fmt.Fprintf(w, "  Config Path:    %s\n", rep.ConfigPath)

	if !rep.Paired {
		fmt.Fprintf(w, "  Pairing:        NOT PAIRED (run 'talkintent pair' to join a Hub)\n")
	} else {
		fmt.Fprintf(w, "  Member:         %s (ID: %s)\n", rep.MemberName, rep.MemberID)
		fmt.Fprintf(w, "  Machine:        %s\n", rep.Machine)
		fmt.Fprintf(w, "  Hub URL:        %s\n", rep.Hub.URL)
		if rep.Hub.Reachable {
			fmt.Fprintf(w, "  Hub Status:     Online (%dms)\n", rep.Hub.LatencyMS)
		} else {
			fmt.Fprintf(w, "  Hub Status:     UNREACHABLE (%s)\n", rep.Hub.Error)
		}
	}

	if rep.Daemon.Running {
		fmt.Fprintf(w, "  Daemon:         RUNNING (PID: %d)\n", rep.Daemon.PID)
	} else {
		fmt.Fprintf(w, "  Daemon:         STOPPED (%s)\n", rep.Daemon.Status)
	}

	fmt.Fprintf(w, "  Workspaces (%d):\n", len(rep.Workspaces))
	if len(rep.Workspaces) == 0 {
		fmt.Fprintf(w, "    (No workspaces configured yet. Use 'talkintent workspace add')\n")
	} else {
		for _, ws := range rep.Workspaces {
			fmt.Fprintf(w, "    - %s\n", ws)
		}
	}

	fmt.Fprintf(w, "  LLM Provider:   %s (Model: %s)\n", rep.LLM.Provider, rep.LLM.Model)
}
