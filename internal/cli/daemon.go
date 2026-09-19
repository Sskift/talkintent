package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/daemon"
	"github.com/Sskift/talkintent/internal/probe"
)

// DaemonPIDFilePath returns the path to daemon.pid.
func DaemonPIDFilePath() string {
	return filepath.Join(config.GetTalkIntentHome(), "daemon.pid")
}

// DaemonLogFilePath returns the path to daemon.log.
func DaemonLogFilePath() string {
	return filepath.Join(config.GetTalkIntentHome(), "daemon.log")
}

// ReadDaemonPID reads the PID from daemon.pid if present.
func ReadDaemonPID() (int, error) {
	pidPath := DaemonPIDFilePath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, err
	}
	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("invalid PID in %s: %q", pidPath, pidStr)
	}
	return pid, nil
}

// WriteDaemonPID writes the PID to daemon.pid.
func WriteDaemonPID(pid int) error {
	pidPath := DaemonPIDFilePath()
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(pidPath, []byte(strconv.Itoa(pid)+"\n"), 0600)
}

// RemoveDaemonPID removes daemon.pid.
func RemoveDaemonPID() {
	_ = os.Remove(DaemonPIDFilePath())
}

// DaemonOptions encapsulates flags for the daemon command.
type DaemonOptions struct {
	ConfigPath string
	Workspace  string
	Detach     bool
	Action     string // "", "stop", "status"
	JSONOutput bool
}

// ExecuteDaemon executes daemon subcommands: foreground run, detached spawn, stop, status.
func ExecuteDaemon(ctx context.Context, opts DaemonOptions, stdout, stderr io.Writer) int {
	switch opts.Action {
	case "stop":
		return executeDaemonStop(stdout, stderr)
	case "status":
		return executeDaemonStatus(stdout, stderr)
	default:
		if opts.Detach {
			return executeDaemonDetach(opts, stdout, stderr)
		}
		return executeDaemonForeground(ctx, opts, stdout, stderr)
	}
}

func executeDaemonStop(stdout, stderr io.Writer) int {
	pid, err := ReadDaemonPID()
	if err != nil {
		fmt.Fprintf(stdout, "TalkIntent daemon is not running (no PID file found).\n")
		return 0
	}

	if !isProcessRunning(pid) {
		RemoveDaemonPID()
		fmt.Fprintf(stdout, "TalkIntent daemon is not running (stale PID file removed).\n")
		return 0
	}

	if err := terminateProcess(pid); err != nil {
		fmt.Fprintf(stderr, "Failed to terminate daemon process (PID %d): %v\n", pid, err)
		return 1
	}

	RemoveDaemonPID()
	fmt.Fprintf(stdout, "TalkIntent daemon (PID %d) stopped successfully.\n", pid)
	return 0
}

func executeDaemonStatus(stdout, stderr io.Writer) int {
	pid, err := ReadDaemonPID()
	if err != nil {
		fmt.Fprintf(stdout, "Daemon: NOT RUNNING\n")
		return 0
	}

	if isProcessRunning(pid) {
		fmt.Fprintf(stdout, "Daemon: RUNNING (PID %d)\n", pid)
		fmt.Fprintf(stdout, "PID file: %s\n", DaemonPIDFilePath())
		fmt.Fprintf(stdout, "Log file: %s\n", DaemonLogFilePath())
		return 0
	}

	RemoveDaemonPID()
	fmt.Fprintf(stdout, "Daemon: NOT RUNNING (stale PID %d cleaned up)\n", pid)
	return 0
}

func executeDaemonDetach(opts DaemonOptions, stdout, stderr io.Writer) int {
	// Check if already running
	if pid, err := ReadDaemonPID(); err == nil && isProcessRunning(pid) {
		fmt.Fprintf(stdout, "TalkIntent daemon is already running (PID %d).\n", pid)
		return 0
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "Failed to locate executable: %v\n", err)
		return 1
	}

	// Filter out --detach and -d
	var args []string
	args = append(args, "daemon")
	if opts.ConfigPath != "" {
		args = append(args, "--config", opts.ConfigPath)
	}
	if opts.Workspace != "" {
		args = append(args, "--workspace", opts.Workspace)
	}

	logPath := DaemonLogFilePath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		fmt.Fprintf(stderr, "Failed to create log directory: %v\n", err)
		return 1
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to open daemon log file: %v\n", err)
		return 1
	}
	defer logFile.Close()

	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetachedProcessAttr(cmd)

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "Failed to start background daemon: %v\n", err)
		return 1
	}

	pid := cmd.Process.Pid
	if err := WriteDaemonPID(pid); err != nil {
		fmt.Fprintf(stderr, "Warning: failed to write PID file: %v\n", err)
	}

	fmt.Fprintf(stdout, "TalkIntent daemon started in background (PID: %d).\n", pid)
	fmt.Fprintf(stdout, "  Log file: %s\n", logPath)
	fmt.Fprintf(stdout, "  Run 'talkintent daemon stop' to stop.\n")
	return 0
}

func executeDaemonForeground(ctx context.Context, opts DaemonOptions, stdout, stderr io.Writer) int {
	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to load client config: %v. Run 'talkintent pair' first.\n", err)
		return 1
	}

	// Persist workspace if specified on command line
	if opts.Workspace != "" {
		norm, err := config.NormalizeWorkspacePath(opts.Workspace)
		if err != nil {
			fmt.Fprintf(stderr, "Invalid workspace path %q: %v\n", opts.Workspace, err)
			return 1
		}
		_ = cfg.AddWorkspace(config.WorkspaceConfig{
			Name:     filepath.Base(norm),
			RootPath: norm,
		})
		if err := config.SaveClientConfig(opts.ConfigPath, cfg); err != nil {
			fmt.Fprintf(stderr, "Warning: failed to persist workspace to config: %v\n", err)
		}
	}

	pid := os.Getpid()
	if err := WriteDaemonPID(pid); err != nil {
		fmt.Fprintf(stderr, "Warning: failed to write PID file: %v\n", err)
	}
	defer RemoveDaemonPID()

	agent := probe.NewDefaultAgent(nil)
	logger := slog.New(slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	d, err := daemon.NewClientDaemon(cfg, agent, logger)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to initialize daemon: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Starting TalkIntent Daemon (PID %d)...\n", pid)
	fmt.Fprintf(stdout, "  Member:     %s (%s)\n", cfg.MemberName, cfg.MemberID)
	fmt.Fprintf(stdout, "  Hub URL:    %s\n", cfg.HubURL)
	fmt.Fprintf(stdout, "  Workspaces: %d monitored\n", len(cfg.Workspaces))
	for _, ws := range cfg.Workspaces {
		fmt.Fprintf(stdout, "    - %s -> %s\n", ws.Name, ws.RootPath)
	}
	fmt.Fprintf(stdout, "Press Ctrl+C to stop.\n")

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Start(ctx)
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintf(stdout, "\nShutting down TalkIntent daemon...\n")
		_ = d.Stop()
		return 0
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "Daemon exited with error: %v\n", err)
			return 1
		}
		return 0
	}
}
