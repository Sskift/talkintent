package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/protocol"
)

// mockAgent implements probe.Agent for testing daemon dispatch.
type mockAgent struct {
	mu          sync.Mutex
	runFunc     func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error)
	calledCount int32
	lastReq     *probe.RunRequest
}

func (m *mockAgent) Run(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
	atomic.AddInt32(&m.calledCount, 1)
	m.mu.Lock()
	m.lastReq = req
	fn := m.runFunc
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, req)
	}
	return &protocol.QueryResponsePayload{
		QueryID:    req.QueryID,
		Status:     protocol.QueryStatusCompleted,
		Answer:     "Mock synthesized answer for " + req.Query,
		ToolsUsed:  []string{"git_status", "read_file"},
		DurationMS: 120,
		TokenUsage: protocol.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
	}, nil
}

func setupTestDaemon(t *testing.T, hub *FakeHub, agent probe.Agent) (*ClientDaemon, string) {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv(config.EnvTalkIntentHome, tempDir)

	ws1 := filepath.Join(tempDir, "workspace1")
	ws2 := filepath.Join(tempDir, "workspace2")
	_ = os.MkdirAll(ws1, 0755)
	_ = os.MkdirAll(ws2, 0755)

	// Create a privacy prompt file in workspace1
	_ = os.WriteFile(filepath.Join(ws1, "privacy-prompt.md"), []byte("Confidential info"), 0644)

	cfg := &config.ClientConfig{
		HubURL:               hub.URL(),
		MemberID:             "mem_test_123",
		MemberName:           "Test User",
		Token:                "test_secret_token_456",
		MachineName:          "test-machine",
		MaxConcurrency:       2,
		HeartbeatIntervalSec: 20,
		Workspaces: []config.WorkspaceConfig{
			{
				ID:       "ws_1",
				Name:     "project-alpha",
				RootPath: ws1,
			},
			{
				ID:       "ws_2",
				Name:     "project-beta",
				RootPath: ws2,
			},
		},
	}

	d, err := NewClientDaemon(cfg, agent, nil)
	if err != nil {
		t.Fatalf("NewClientDaemon failed: %v", err)
	}

	statusPath := filepath.Join(tempDir, "daemon-status.json")
	pidPath := filepath.Join(tempDir, "daemon.pid")
	d.SetFilePaths(statusPath, pidPath)
	d.SetBackoffParams(50*time.Millisecond, 200*time.Millisecond, 2*time.Second)

	return d, tempDir
}

func TestDaemonConnectAndHello(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, tempDir := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	hello, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Failed to receive daemon_hello: %v", err)
	}

	// 1. Verify handshake headers
	headers := hub.Headers()
	auth := headers.Get("Authorization")
	if auth != "Bearer test_secret_token_456" {
		t.Errorf("Expected Bearer token in auth header, got %q", auth)
	}
	if headers.Get("X-Client-Version") != ClientVersion {
		t.Errorf("Expected X-Client-Version %q, got %q", ClientVersion, headers.Get("X-Client-Version"))
	}
	if headers.Get("X-Machine-Name") != "test-machine" {
		t.Errorf("Expected X-Machine-Name 'test-machine', got %q", headers.Get("X-Machine-Name"))
	}

	// 2. Verify daemon_hello payload
	if hello.MemberID != "mem_test_123" {
		t.Errorf("Expected member_id 'mem_test_123', got %q", hello.MemberID)
	}
	if hello.ClientVersion != ClientVersion {
		t.Errorf("Expected client_version %q, got %q", ClientVersion, hello.ClientVersion)
	}
	if hello.OS != runtime.GOOS || hello.Arch != runtime.GOARCH {
		t.Errorf("Expected OS %q Arch %q, got %q / %q", runtime.GOOS, runtime.GOARCH, hello.OS, hello.Arch)
	}
	if hello.MaxConcurrency != 2 {
		t.Errorf("Expected max_concurrency 2, got %d", hello.MaxConcurrency)
	}
	if len(hello.Workspaces) != 2 {
		t.Fatalf("Expected 2 workspaces, got %d", len(hello.Workspaces))
	}

	// Verify workspace 1 has_privacy_prompt computed true, workspace 2 false
	if !hello.Workspaces[0].HasPrivacyPrompt {
		t.Errorf("Expected workspace 1 has_privacy_prompt to be true")
	}
	if hello.Workspaces[1].HasPrivacyPrompt {
		t.Errorf("Expected workspace 2 has_privacy_prompt to be false")
	}

	// 3. Verify status file exists and connected is true
	statusPath := filepath.Join(tempDir, "daemon-status.json")
	var status *DaemonStatus
	for i := 0; i < 20; i++ {
		status, err = ReadDaemonStatus(statusPath)
		if err == nil && status.Connected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status == nil || !status.Connected {
		t.Fatalf("Expected daemon status connected=true, got %v (err: %v)", status, err)
	}
	if status.PID != os.Getpid() {
		t.Errorf("Expected status PID %d, got %d", os.Getpid(), status.PID)
	}
}

func TestDaemonHeartbeatPingPong(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	hub.SetAutoAck(true, 1) // 1 second heartbeat interval

	agent := &mockAgent{}
	d, tempDir := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	// Wait for connection
	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Failed to receive daemon_hello: %v", err)
	}

	// Wait for heartbeat ping
	ping, err := hub.WaitForPing(3 * time.Second)
	if err != nil {
		t.Fatalf("Failed to receive heartbeat_ping: %v", err)
	}
	if ping.ActiveProbeCount != 0 {
		t.Errorf("Expected ActiveProbeCount 0, got %d", ping.ActiveProbeCount)
	}

	// Verify status file updated with recent heartbeat
	statusPath := filepath.Join(tempDir, "daemon-status.json")
	time.Sleep(100 * time.Millisecond)
	status, err := ReadDaemonStatus(statusPath)
	if err != nil {
		t.Fatalf("Failed to read status: %v", err)
	}
	if time.Since(status.LastHeartbeat) > 5*time.Second {
		t.Errorf("Expected recent LastHeartbeat, got %v", status.LastHeartbeat)
	}
}

func TestDaemonHeartbeatPongTimeoutReconnect(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	// Hub acknowledges hello with 100ms interval, but disables autoPong to trigger timeout
	hub.SetAutoAck(true, 1)
	hub.SetAutoPong(false)

	agent := &mockAgent{}
	d, _ := setupTestDaemon(t, hub, agent)
	// Set very short pong timeout
	d.SetBackoffParams(50*time.Millisecond, 100*time.Millisecond, 150*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	// 1st connection hello
	_, err := hub.WaitForHello(2 * time.Second)
	if err != nil {
		t.Fatalf("First hello not received: %v", err)
	}

	// 1st ping arrives, but no pong is sent by hub
	_, err = hub.WaitForPing(2 * time.Second)
	if err != nil {
		t.Fatalf("First ping not received: %v", err)
	}

	// Daemon should detect pong timeout (150ms exceeded) and reconnect!
	// Now enable autoPong so second connection succeeds
	hub.SetAutoPong(true)

	hello2, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Daemon did not reconnect after pong timeout: %v", err)
	}
	if hello2.MemberID != "mem_test_123" {
		t.Errorf("Second hello has wrong member_id: %s", hello2.MemberID)
	}
}

func TestDaemonDispatchQuerySuccess(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Send a query request
	req := protocol.QueryRequestPayload{
		QueryID:         "qry_test_001",
		AskerID:         "mem_asker_1",
		AskerName:       "Zhang San",
		AskerType:       "member",
		Query:           "How is the auth refactor going?",
		TargetWorkspace: "project-alpha",
		TimeoutSeconds:  10,
		CreatedAt:       time.Now().UnixMilli(),
	}
	if err := hub.SendQuery(req); err != nil {
		t.Fatalf("Failed to send query: %v", err)
	}

	resp, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Failed to receive query_response: %v", err)
	}

	if resp.QueryID != "qry_test_001" {
		t.Errorf("Expected query_id 'qry_test_001', got %q", resp.QueryID)
	}
	if resp.Status != protocol.QueryStatusCompleted {
		t.Errorf("Expected status 'completed', got %q", resp.Status)
	}
	if !strings.Contains(resp.Answer, "Mock synthesized answer") {
		t.Errorf("Unexpected answer: %q", resp.Answer)
	}
	if len(resp.ToolsUsed) != 2 || resp.ToolsUsed[0] != "git_status" {
		t.Errorf("Unexpected tools used: %v", resp.ToolsUsed)
	}
	if resp.DurationMS <= 0 {
		t.Errorf("Expected positive duration, got %d", resp.DurationMS)
	}
	if resp.TokenUsage.TotalTokens != 150 {
		t.Errorf("Expected 150 total tokens, got %d", resp.TokenUsage.TotalTokens)
	}
}

func TestDaemonWorkspaceResolution(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, tempDir := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// 1. Resolve by workspace ID
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_by_id",
		Query:           "Check ws2",
		TargetWorkspace: "ws_2",
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}
	resp, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response failed: %v", err)
	}
	if resp.Status != protocol.QueryStatusCompleted {
		t.Errorf("Expected completed, got %s (err: %s)", resp.Status, resp.ErrorMessage)
	}
	if agent.lastReq.Workspace.ID != "ws_2" {
		t.Errorf("Expected resolved workspace ws_2, got %s", agent.lastReq.Workspace.ID)
	}

	// 2. Resolve by RootPath
	ws1Path := filepath.Join(tempDir, "workspace1")
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_by_root",
		Query:           "Check ws1",
		TargetWorkspace: ws1Path,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}
	resp, err = hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response failed: %v", err)
	}
	if resp.Status != protocol.QueryStatusCompleted {
		t.Errorf("Expected completed, got %s", resp.Status)
	}
	if agent.lastReq.Workspace.ID != "ws_1" {
		t.Errorf("Expected resolved workspace ws_1, got %s", agent.lastReq.Workspace.ID)
	}

	// 3. Empty target workspace defaults to first workspace and passes all workspaces
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_empty_target",
		Query:           "Check all",
		TargetWorkspace: "",
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}
	resp, err = hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response failed: %v", err)
	}
	if resp.Status != protocol.QueryStatusCompleted {
		t.Errorf("Expected completed, got %s", resp.Status)
	}
	if agent.lastReq.Workspace.ID != "ws_1" {
		t.Errorf("Expected default workspace ws_1, got %s", agent.lastReq.Workspace.ID)
	}
	if len(agent.lastReq.Workspaces) != 2 {
		t.Errorf("Expected all 2 workspaces passed, got %d", len(agent.lastReq.Workspaces))
	}

	// 4. Unknown target workspace returns clear error
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_unknown",
		Query:           "Check unknown",
		TargetWorkspace: "non_existent_workspace",
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}
	resp, err = hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response failed: %v", err)
	}
	if resp.Status != protocol.QueryStatusError {
		t.Errorf("Expected status error for unknown workspace, got %s", resp.Status)
	}
	if !strings.Contains(resp.ErrorMessage, `target workspace "non_existent_workspace" not found on daemon`) {
		t.Errorf("Expected clear error message, got %q", resp.ErrorMessage)
	}
}

func TestDaemonConcurrencyLimitHonoured(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	var runningProbes int32
	var maxObservedProbes int32
	unblockCh := make(chan struct{})
	probesReady := make(chan struct{})
	var readyOnce sync.Once

	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			cur := atomic.AddInt32(&runningProbes, 1)
			defer atomic.AddInt32(&runningProbes, -1)

			for {
				old := atomic.LoadInt32(&maxObservedProbes)
				if cur <= old || atomic.CompareAndSwapInt32(&maxObservedProbes, old, cur) {
					break
				}
			}
			if cur == 2 {
				readyOnce.Do(func() { close(probesReady) })
			}

			select {
			case <-unblockCh:
				return &protocol.QueryResponsePayload{
					QueryID: req.QueryID,
					Status:  protocol.QueryStatusCompleted,
					Answer:  "Done: " + req.Query,
				}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Dispatch 3 queries concurrently while max_concurrency is 2
	for i := 1; i <= 3; i++ {
		_ = hub.SendQuery(protocol.QueryRequestPayload{
			QueryID:         fmt.Sprintf("qry_conc_%d", i),
			Query:           fmt.Sprintf("Concurrent task %d", i),
			TargetWorkspace: "ws_1",
			TimeoutSeconds:  10,
		})
	}

	// Wait until 2 concurrent probes saturate the daemon semaphore (F22, F43)
	select {
	case <-probesReady:
	case <-time.After(3 * time.Second):
		t.Fatalf("Timed out waiting for 2 concurrent probes to start")
	}

	// Brief pause to ensure the third query does not illegally enter the semaphore
	time.Sleep(50 * time.Millisecond)

	// Verify maximum observed concurrent probes is exactly 2 (F22, F43)
	observed := atomic.LoadInt32(&maxObservedProbes)
	if observed > 2 {
		t.Fatalf("Concurrency limit exceeded: observed %d concurrent probes, limit is 2", observed)
	}
	if observed != 2 {
		t.Errorf("Expected 2 active probes, got %d", observed)
	}

	// Now unblock queries and verify all 3 eventually complete
	close(unblockCh)

	responses := make(map[string]bool)
	for i := 0; i < 3; i++ {
		resp, err := hub.WaitForResponse(3 * time.Second)
		if err != nil {
			t.Fatalf("Failed to receive response %d: %v", i+1, err)
		}
		responses[resp.QueryID] = true
	}

	if len(responses) != 3 {
		t.Errorf("Expected 3 distinct query responses, got %d", len(responses))
	}
}

func TestDaemonQueryCancel(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	probeStarted := make(chan struct{})
	var startOnce sync.Once
	probeCanceled := make(chan struct{})
	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			startOnce.Do(func() { close(probeStarted) })
			select {
			case <-ctx.Done():
				close(probeCanceled)
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return &protocol.QueryResponsePayload{
					QueryID: req.QueryID,
					Status:  protocol.QueryStatusCompleted,
				}, nil
			}
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Dispatch query
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_cancel_me",
		Query:           "Long running inspection",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  30,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	// Wait for probe execution to begin before sending cancel (avoids scheduler jitter race)
	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("Timed out waiting for probe execution to start")
	}

	// Send cancel frame from Hub (F27, F48)
	err = hub.SendCancel("qry_cancel_me", "user_aborted")
	if err != nil {
		t.Fatalf("SendCancel failed: %v", err)
	}

	select {
	case <-probeCanceled:
		// Succeeded: probe received context cancellation promptly
	case <-time.After(2 * time.Second):
		t.Fatalf("In-flight probe context was not cancelled upon receiving query_cancel")
	}
}

func TestDaemonBackoffReconnectAfterClose(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, _ := setupTestDaemon(t, hub, agent)
	d.SetBackoffParams(50*time.Millisecond, 150*time.Millisecond, 2*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	// 1st connection
	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("First hello not received: %v", err)
	}

	// Force close active connection from server side
	hub.CloseActiveConn(websocket.StatusPolicyViolation, "takeover")

	// Daemon must reconnect with backoff and send another hello
	hello2, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Daemon failed to reconnect after connection close: %v", err)
	}
	if hello2.MemberID != "mem_test_123" {
		t.Errorf("Expected member_id mem_test_123, got %s", hello2.MemberID)
	}
}

func TestDaemonResponseTruncationAndRedaction(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	// Mock agent that returns massive output > 16KB and sensitive token
	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			if req.Query == "overflow" {
				bigAnswer := strings.Repeat("A", 20*1024)
				return &protocol.QueryResponsePayload{
					QueryID: req.QueryID,
					Status:  protocol.QueryStatusSuccess,
					Answer:  bigAnswer,
				}, nil
			}
			// Error leaking secret
			return nil, errors.New("failed dialing 192.168.1.100 with key sk-proj-12345678901234567890")
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// 1. Test answer truncation to 16KB (F10)
	_ = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_overflow",
		Query:           "overflow",
		TargetWorkspace: "ws_1",
	})
	resp1, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response 1 failed: %v", err)
	}
	if !strings.HasSuffix(resp1.Answer, "[truncated by TalkIntent daemon]") {
		t.Errorf("Expected answer to end with truncation notice, got length %d", len(resp1.Answer))
	}
	if len(resp1.Answer) > 16*1024+50 {
		t.Errorf("Answer exceeded 16KB cap: %d bytes", len(resp1.Answer))
	}
	if resp1.Status != protocol.QueryStatusCompleted {
		t.Errorf("Expected status normalized to completed, got %s", resp1.Status)
	}

	// 2. Test error message redaction (F15)
	_ = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_leak",
		Query:           "leak",
		TargetWorkspace: "ws_1",
	})
	resp2, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response 2 failed: %v", err)
	}
	if resp2.Status != protocol.QueryStatusError {
		t.Errorf("Expected error status, got %s", resp2.Status)
	}
	if strings.Contains(resp2.ErrorMessage, "192.168.1.100") || strings.Contains(resp2.ErrorMessage, "sk-proj") {
		t.Errorf("Sensitive credentials leaked in error message: %q", resp2.ErrorMessage)
	}
	if !strings.Contains(resp2.ErrorMessage, "[REDACTED]") {
		t.Errorf("Expected [REDACTED] in error message: %q", resp2.ErrorMessage)
	}
}

func TestDaemonVersionCheck(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Send an envelope with unsupported version v2 (F32)
	qPayload, _ := json.Marshal(protocol.QueryRequestPayload{
		QueryID: "qry_v2",
		Query:   "hello",
	})
	envV2 := protocol.Envelope{
		Version:   "v2",
		Type:      protocol.TypeQueryRequest,
		ID:        "msg_v2",
		Timestamp: time.Now().UnixMilli(),
		Payload:   qPayload,
	}
	_ = hub.SendRawEnvelope(envV2)

	// Send a valid v1 query frame immediately following the v2 query
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_v1_sync",
		Query:           "sync probe",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  10,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	// Wait for the v1 response to arrive; its arrival proves that the preceding v2 frame was processed
	resp, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Failed to receive v1 response: %v", err)
	}
	if resp.QueryID != "qry_v1_sync" {
		t.Fatalf("Expected response for qry_v1_sync, got %s", resp.QueryID)
	}

	// Daemon should only have executed the v1 probe, proving that the preceding v2 frame was discarded
	if called := atomic.LoadInt32(&agent.calledCount); called != 1 {
		t.Errorf("Expected calledCount == 1 (v1 only), got %d", called)
	}
}

func TestDaemonStatusAndPIDFiles(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, tempDir := setupTestDaemon(t, hub, agent)

	statusPath := filepath.Join(tempDir, "daemon-status.json")
	pidPath := filepath.Join(tempDir, "daemon.pid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Verify PID file
	pid, err := ReadDaemonPID(pidPath)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("Expected PID %d, got %d", os.Getpid(), pid)
	}

	// Verify status file
	status, err := ReadDaemonStatus(statusPath)
	if err != nil {
		t.Fatalf("Failed to read status file: %v", err)
	}
	if !status.Connected {
		t.Errorf("Expected connected=true")
	}
	if status.Version != ClientVersion {
		t.Errorf("Expected version %s, got %s", ClientVersion, status.Version)
	}

	// Clean shutdown
	err = d.Stop()
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Verify PID file removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("Expected PID file to be removed on stop, err: %v", err)
	}

	// Verify status marked disconnected
	statusAfter, err := ReadDaemonStatus(statusPath)
	if err != nil {
		t.Fatalf("Failed to read status after stop: %v", err)
	}
	if statusAfter.Connected {
		t.Errorf("Expected connected=false after stop")
	}
}

func TestNoWorkspacesConfigured(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	cfg := &config.ClientConfig{
		HubURL:      hub.URL(),
		MemberID:    "mem_empty_ws",
		Token:       "tok_empty",
		MachineName: "test-pc",
		Workspaces:  []config.WorkspaceConfig{}, // Empty workspaces
	}

	d, err := NewClientDaemon(cfg, agent, nil)
	if err != nil {
		t.Fatalf("NewClientDaemon failed: %v", err)
	}
	tempDir := t.TempDir()
	d.SetFilePaths(filepath.Join(tempDir, "status.json"), filepath.Join(tempDir, "pid"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err = hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID: "qry_no_ws",
		Query:   "Test query",
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	resp, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Response failed: %v", err)
	}
	if resp.Status != protocol.QueryStatusError {
		t.Errorf("Expected error status, got %s", resp.Status)
	}
	if !strings.Contains(resp.ErrorMessage, "no workspaces configured on daemon") {
		t.Errorf("Expected 'no workspaces configured on daemon', got %q", resp.ErrorMessage)
	}
}

func TestDaemonQueryTimeoutTransmission(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			// Simulate a probe that blocks until its context deadline expires
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = d.Start(ctx)
	}()
	defer d.Stop()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Send a query with a 1-second timeout
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_timeout_test",
		Query:           "Slow query",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  1,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	// Hub should successfully receive the timeout query_response envelope (Finding 1 fix)
	resp, err := hub.WaitForResponse(5 * time.Second)
	if err != nil {
		t.Fatalf("Hub failed to receive timeout response from daemon: %v", err)
	}

	if resp.QueryID != "qry_timeout_test" {
		t.Errorf("Expected QueryID 'qry_timeout_test', got %q", resp.QueryID)
	}
	if resp.Status != protocol.QueryStatusTimeout {
		t.Errorf("Expected status %q, got %q", protocol.QueryStatusTimeout, resp.Status)
	}
}

func TestDaemonGracefulDrainOnStop(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})

	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			close(probeStarted)
			select {
			case <-probeRelease:
				return &protocol.QueryResponsePayload{
					QueryID: req.QueryID,
					Status:  protocol.QueryStatusCompleted,
					Answer:  "Gracefully finished",
				}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)
	d.SetDrainGrace(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErrCh := make(chan error, 1)
	go func() {
		startErrCh <- d.Start(ctx)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Dispatch query
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_drain_stop",
		Query:           "Drain test query",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  10,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	// Wait for probe to start running
	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("Timed out waiting for probe to start")
	}

	// Call Stop in a goroutine while probe is in flight
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- d.Stop()
	}()

	// Ensure Stop does not immediately close connection or kill probe
	select {
	case err := <-stopDone:
		t.Fatalf("Stop() returned prematurely while probe in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Release probe
	close(probeRelease)

	// Hub must receive the query_response
	resp, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Hub failed to receive query_response during drain: %v", err)
	}
	if resp.QueryID != "qry_drain_stop" || resp.Answer != "Gracefully finished" {
		t.Errorf("Unexpected response: %+v", resp)
	}

	// Hub connection should close with StatusNormalClosure
	code, err := hub.WaitForClose(3 * time.Second)
	if err != nil {
		t.Fatalf("Connection not closed after drain: %v", err)
	}
	if code != websocket.StatusNormalClosure {
		t.Errorf("Expected StatusNormalClosure (1000), got %v", code)
	}

	// Stop() should return nil promptly
	select {
	case err := <-stopDone:
		if err != nil {
			t.Errorf("Stop() failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Timed out waiting for Stop() to return")
	}

	select {
	case <-startErrCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("Start() did not exit")
	}
}

func TestDaemonDrainGraceTimeoutOnStop(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	probeStarted := make(chan struct{})
	probeExited := make(chan struct{})
	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			close(probeStarted)
			<-ctx.Done() // Never finishes on its own until connCtx is cancelled
			close(probeExited)
			return nil, ctx.Err()
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)
	d.SetDrainGrace(150 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErrCh := make(chan error, 1)
	go func() {
		startErrCh <- d.Start(ctx)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_hang",
		Query:           "Hanging query",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  30,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("Probe did not start")
	}

	start := time.Now()
	err = d.Stop()
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Stop() returned error: %v", err)
	}
	if duration > 2*time.Second {
		t.Errorf("Stop() took %v, expected < 2s with 150ms drain grace", duration)
	}
	if duration < 100*time.Millisecond {
		t.Errorf("Stop() returned too quickly (%v), expected >= 100ms", duration)
	}

	// Wait for Start and probe to cleanly finish unwinding before tempDir cleanup
	select {
	case <-probeExited:
	case <-time.After(2 * time.Second):
	}
	select {
	case <-startErrCh:
	case <-time.After(2 * time.Second):
	}
}

func TestDaemonGracefulDrainOnContextCancel(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})

	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			close(probeStarted)
			select {
			case <-probeRelease:
				return &protocol.QueryResponsePayload{
					QueryID: req.QueryID,
					Status:  protocol.QueryStatusCompleted,
					Answer:  "Context cancel drain finished",
				}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)
	d.SetDrainGrace(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())

	startErrCh := make(chan error, 1)
	go func() {
		startErrCh <- d.Start(ctx)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_ctx_cancel",
		Query:           "Cancel test query",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  10,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("Timed out waiting for probe to start")
	}

	// Cancel the context passed to Start (simulating SIGINT)
	cancel()

	// Ensure probe is not aborted immediately
	select {
	case <-startErrCh:
		t.Fatalf("Start() returned prematurely before probe finished")
	case <-time.After(50 * time.Millisecond):
	}

	// Release probe
	close(probeRelease)

	// Hub should receive query_response
	resp, err := hub.WaitForResponse(3 * time.Second)
	if err != nil {
		t.Fatalf("Hub failed to receive query_response: %v", err)
	}
	if resp.QueryID != "qry_ctx_cancel" || resp.Answer != "Context cancel drain finished" {
		t.Errorf("Unexpected response: %+v", resp)
	}

	// Connection closed normally
	code, err := hub.WaitForClose(3 * time.Second)
	if err != nil {
		t.Fatalf("Connection not closed: %v", err)
	}
	if code != websocket.StatusNormalClosure {
		t.Errorf("Expected StatusNormalClosure, got %v", code)
	}

	// Start() exits cleanly with context.Canceled
	select {
	case err := <-startErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Start() did not exit")
	}
}

func TestDaemonDrainingServicesPongsAndCancelsAndRejectsNewQueries(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	probe1Started := make(chan struct{})
	probe1Canceled := make(chan struct{})
	probe2Started := make(chan struct{})

	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			if req.QueryID == "qry_cancel_drain" {
				close(probe1Started)
				<-ctx.Done()
				close(probe1Canceled)
				return nil, ctx.Err()
			}
			if req.QueryID == "qry_rejected" {
				close(probe2Started)
				return &protocol.QueryResponsePayload{
					QueryID: req.QueryID,
					Status:  protocol.QueryStatusCompleted,
				}, nil
			}
			return &protocol.QueryResponsePayload{
				QueryID: req.QueryID,
				Status:  protocol.QueryStatusCompleted,
			}, nil
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)
	d.SetDrainGrace(3 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		_ = d.Start(ctx)
		close(startDone)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	// Start query 1
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_cancel_drain",
		Query:           "To be cancelled during drain",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  30,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	select {
	case <-probe1Started:
	case <-time.After(3 * time.Second):
		t.Fatalf("Probe 1 did not start")
	}

	// Trigger shutdown
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- d.Stop()
	}()

	// Wait until daemon entered draining state
	drainDeadline := time.Now().Add(3 * time.Second)
	for !d.isDraining() && time.Now().Before(drainDeadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !d.isDraining() {
		t.Fatalf("Daemon failed to enter draining state")
	}

	// Send a heartbeat pong from hub to ensure pong handling works without panicking
	_ = hub.SendEnvelope(protocol.TypeHeartbeatPong, protocol.HeartbeatPongPayload{ServerTime: time.Now().UnixMilli()})

	// Send query 2 which arrives AFTER shutdown began - must be rejected/ignored
	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_rejected",
		Query:           "Should not be executed",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  5,
	})
	if err != nil {
		t.Fatalf("SendQuery 2 failed: %v", err)
	}

	// Verify query 2 is NOT started
	select {
	case <-probe2Started:
		t.Fatalf("Query arriving during drain was erroneously executed")
	case <-time.After(100 * time.Millisecond):
	}

	// Cancel query 1 during drain: cancel frame must be processed!
	err = hub.SendCancel("qry_cancel_drain", "cancel during drain")
	if err != nil {
		t.Fatalf("SendCancel failed: %v", err)
	}

	// Probe 1 must be cancelled promptly
	select {
	case <-probe1Canceled:
	case <-time.After(2 * time.Second):
		t.Fatalf("Probe 1 was not cancelled by query_cancel during drain")
	}

	// Wait for Stop() to complete cleanly
	select {
	case err := <-stopDone:
		if err != nil {
			t.Errorf("Stop() failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Stop() timed out")
	}
	<-startDone
}

func TestDaemonConnectionLossCancelsInFlightProbe(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	probeStarted := make(chan struct{})
	probeCanceled := make(chan struct{})

	agent := &mockAgent{
		runFunc: func(ctx context.Context, req *probe.RunRequest) (*protocol.QueryResponsePayload, error) {
			close(probeStarted)
			<-ctx.Done()
			close(probeCanceled)
			return nil, ctx.Err()
		},
	}

	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		_ = d.Start(ctx)
		close(startDone)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	err = hub.SendQuery(protocol.QueryRequestPayload{
		QueryID:         "qry_conn_loss",
		Query:           "Should abort on conn drop",
		TargetWorkspace: "ws_1",
		TimeoutSeconds:  30,
	})
	if err != nil {
		t.Fatalf("SendQuery failed: %v", err)
	}

	select {
	case <-probeStarted:
	case <-time.After(3 * time.Second):
		t.Fatalf("Probe did not start")
	}

	// Drop connection abruptly
	hub.CloseActiveConn(websocket.StatusAbnormalClosure, "network failure")

	select {
	case <-probeCanceled:
		// Success: probe aborted on connection loss
	case <-time.After(2 * time.Second):
		t.Fatalf("Probe context was not cancelled upon connection loss")
	}

	cancel()
	_ = d.Stop()
	<-startDone
}

func TestDaemonWriteEnvelopeBounded(t *testing.T) {
	hub := NewFakeHub(t)
	defer hub.Close()

	agent := &mockAgent{}
	d, _ := setupTestDaemon(t, hub, agent)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan struct{})
	go func() {
		_ = d.Start(ctx)
		close(startDone)
	}()

	_, err := hub.WaitForHello(3 * time.Second)
	if err != nil {
		t.Fatalf("Hello not received: %v", err)
	}

	d.mu.Lock()
	conn := d.activeConn
	d.mu.Unlock()

	if conn == nil {
		t.Fatalf("activeConn is nil")
	}

	// Verify writeEnvelope with a cancelled parent context still succeeds due to context.WithoutCancel
	canceledCtx, cancelImmediate := context.WithCancel(context.Background())
	cancelImmediate()

	ping := protocol.HeartbeatPingPayload{ActiveProbeCount: 0}
	err = d.writeEnvelope(canceledCtx, conn, protocol.TypeHeartbeatPing, ping)
	if err != nil {
		t.Fatalf("writeEnvelope failed with cancelled caller context: %v", err)
	}

	_ = d.Stop()
	<-startDone
}
