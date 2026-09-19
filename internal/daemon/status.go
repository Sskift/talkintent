package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sskift/talkintent/internal/config"
)

var statusFileMu sync.RWMutex

// Default filenames for daemon status and process ID tracking.
const (
	StatusFilename = "daemon-status.json"
	PIDFilename    = "daemon.pid"
)

// DaemonStatus represents the on-disk state of a running TalkIntent daemon.
type DaemonStatus struct {
	PID           int       `json:"pid"`
	HubURL        string    `json:"hub_url"`
	Connected     bool      `json:"connected"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	ActiveProbes  int       `json:"active_probes"`
	Version       string    `json:"version"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// StatusFilePath returns the canonical path to daemon-status.json.
// If homeDir is empty, config.GetTalkIntentHome() is used.
func StatusFilePath(homeDir string) string {
	if homeDir == "" {
		homeDir = config.GetTalkIntentHome()
	}
	return filepath.Join(homeDir, StatusFilename)
}

// PIDFilePath returns the canonical path to daemon.pid.
// If homeDir is empty, config.GetTalkIntentHome() is used.
func PIDFilePath(homeDir string) string {
	if homeDir == "" {
		homeDir = config.GetTalkIntentHome()
	}
	return filepath.Join(homeDir, PIDFilename)
}

// ReadDaemonStatus reads and parses the daemon status file.
func ReadDaemonStatus(path string) (*DaemonStatus, error) {
	statusFileMu.RLock()
	defer statusFileMu.RUnlock()

	if path == "" {
		path = StatusFilePath("")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var status DaemonStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("malformed daemon status file: %w", err)
	}
	return &status, nil
}

// ReadDaemonPID reads the integer process ID stored in the PID file.
func ReadDaemonPID(path string) (int, error) {
	statusFileMu.RLock()
	defer statusFileMu.RUnlock()

	if path == "" {
		path = PIDFilePath("")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid pid in file %q: %w", path, err)
	}
	return pid, nil
}

// StopDaemonByPID terminates a daemon process identified by its PID.
func StopDaemonByPID(pid int) error {
	if pid <= 0 {
		return errors.New("invalid PID")
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process %d: %w", pid, err)
	}
	if runtime.GOOS == "windows" {
		return proc.Kill()
	}
	return proc.Signal(os.Interrupt)
}

// writeAtomicJSON writes a JSON representation of v to targetPath atomically.
// It writes to a temporary file in the same directory and renames it.
func writeAtomicJSON(targetPath string, v any) error {
	statusFileMu.Lock()
	defer statusFileMu.Unlock()

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}

	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal json: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, raw, 0600); err != nil {
		return fmt.Errorf("failed to write temp file %q: %w", tmpFile, err)
	}

	var renameErr error
	for attempt := 0; attempt < 10; attempt++ {
		renameErr = os.Rename(tmpFile, targetPath)
		if renameErr == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = os.Remove(tmpFile)
	return fmt.Errorf("failed to rename %q to %q: %w", tmpFile, targetPath, renameErr)
}

// writePIDFile writes pid to targetPath atomically.
func writePIDFile(targetPath string, pid int) error {
	statusFileMu.Lock()
	defer statusFileMu.Unlock()

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}

	content := fmt.Sprintf("%d\n", pid)
	tmpFile := fmt.Sprintf("%s.tmp.%d", targetPath, time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to write temp file %q: %w", tmpFile, err)
	}

	var renameErr error
	for attempt := 0; attempt < 10; attempt++ {
		renameErr = os.Rename(tmpFile, targetPath)
		if renameErr == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = os.Remove(tmpFile)
	return fmt.Errorf("failed to rename %q to %q: %w", tmpFile, targetPath, renameErr)
}
