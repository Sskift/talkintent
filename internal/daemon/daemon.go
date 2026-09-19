package daemon

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/protocol"
)

// ClientVersion is the wire protocol version advertised in daemon_hello.
const ClientVersion = "1.0.0"

// Runner manages the client daemon lifecycle and connection loop.
type Runner interface {
	Start(ctx context.Context) error
	Stop() error
}

// ClientDaemon is the standard implementation of Runner.
type ClientDaemon struct {
	cfg        *config.ClientConfig
	agent      probe.Agent
	logger     *slog.Logger
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
	mu         sync.Mutex
	writeMu    sync.Mutex
	activeConn *websocket.Conn

	// Concurrency limiter & active probe tracking
	sem              chan struct{}
	activeProbeCount int32
	inFlightWg       sync.WaitGroup

	// Query cancellation registry
	activeQueriesMu sync.Mutex
	activeQueries   map[string]context.CancelFunc

	sessionID string

	// Status & PID file tracking
	statusFilePath string
	pidFilePath    string
	statusMu       sync.Mutex

	// Transport configuration knobs
	initialBackoff time.Duration
	maxBackoff     time.Duration
	pongTimeout    time.Duration
}

// NewClientDaemon constructs a new ClientDaemon runner.
func NewClientDaemon(cfg *config.ClientConfig, agent probe.Agent, logger *slog.Logger) (*ClientDaemon, error) {
	if cfg == nil {
		return nil, errors.New("client config is required")
	}
	if agent == nil {
		return nil, errors.New("probe agent is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	maxConc := cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = cfg.Daemon.MaxConcurrency
	}
	if maxConc <= 0 {
		maxConc = config.DefaultMaxConcurrency
	}

	homeDir := config.GetTalkIntentHome()
	return &ClientDaemon{
		cfg:            cfg,
		agent:          agent,
		logger:         logger,
		stopCh:         make(chan struct{}),
		sem:            make(chan struct{}, maxConc),
		activeQueries:  make(map[string]context.CancelFunc),
		statusFilePath: StatusFilePath(homeDir),
		pidFilePath:    PIDFilePath(homeDir),
		initialBackoff: 1 * time.Second,
		maxBackoff:     30 * time.Second,
		pongTimeout:    10 * time.Second,
	}, nil
}

// SetBackoffParams overrides backoff parameters (useful for fast-paced unit tests).
func (d *ClientDaemon) SetBackoffParams(initial, maxBackoff, pongTimeout time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initialBackoff = initial
	d.maxBackoff = maxBackoff
	d.pongTimeout = pongTimeout
}

// SetFilePaths overrides the paths for daemon-status.json and daemon.pid.
func (d *ClientDaemon) SetFilePaths(statusPath, pidPath string) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.statusFilePath = statusPath
	d.pidFilePath = pidPath
}

// Start begins the persistent connection loop, reconnecting with exponential backoff and jitter.
func (d *ClientDaemon) Start(ctx context.Context) error {
	d.wg.Add(1)
	defer d.wg.Done()

	d.logger.Info("Starting TalkIntent client daemon", "member_id", d.cfg.MemberID, "hub_url", d.cfg.HubURL)

	d.initFiles()
	defer d.cleanupFiles()

	initialBackoff := d.initialBackoff
	if initialBackoff <= 0 {
		initialBackoff = 1 * time.Second
	}
	maxBackoff := d.maxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}
	backoff := initialBackoff

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		default:
		}

		connectedAt := time.Now()
		err := d.connectAndServe(ctx)

		select {
		case <-d.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		d.writeStatusFile(false)

		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Warn("WebSocket connection terminated, reconnecting", "err", err, "retry_in", backoff)
		}

		// If the connection was active for > 15s, reset backoff to baseline
		if time.Since(connectedAt) > 15*time.Second {
			backoff = initialBackoff
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		default:
		}

		// Calculate exponential backoff with pseudo-random jitter (+/- 20%)
		jitter := computeJitter(backoff)
		sleepDuration := backoff + jitter
		if sleepDuration < 10*time.Millisecond {
			sleepDuration = 10 * time.Millisecond
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		case <-time.After(sleepDuration):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// Stop cleanly shuts down the daemon, draining in-flight probes before closing.
func (d *ClientDaemon) Stop() error {
	d.stopOnce.Do(func() {
		close(d.stopCh)
	})

	// 1. Drain in-flight probes with a 15-second grace window
	drainDone := make(chan struct{})
	go func() {
		d.inFlightWg.Wait()
		close(drainDone)
	}()

	select {
	case <-drainDone:
		d.logger.Info("All in-flight probes drained successfully")
	case <-time.After(15 * time.Second):
		d.logger.Warn("Timed out waiting for in-flight probes to drain during shutdown")
	}

	// 2. Close active WebSocket connection
	d.mu.Lock()
	if d.activeConn != nil {
		_ = d.activeConn.Close(websocket.StatusNormalClosure, "daemon stopping")
	}
	d.mu.Unlock()

	d.wg.Wait()
	d.cleanupFiles()
	return nil
}

func (d *ClientDaemon) connectAndServe(ctx context.Context) error {
	hubURL := d.cfg.HubURL
	if !strings.Contains(hubURL, "://") {
		hubURL = "http://" + hubURL
	}
	u, err := url.Parse(hubURL)
	if err != nil {
		return fmt.Errorf("invalid hub url %q: %w", d.cfg.HubURL, err)
	}

	wsScheme := "ws"
	if u.Scheme == "https" || u.Scheme == "wss" {
		wsScheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/ws/daemon", wsScheme, u.Host)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.cfg.Token)
	headers.Set("X-Client-Version", ClientVersion)
	headers.Set("X-Machine-Name", d.cfg.MachineName)

	opts := &websocket.DialOptions{
		HTTPHeader: headers,
	}

	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.CloseNow()

	// Enforce 2 MB frame read limit to prevent read limit exceeded disconnections (F23)
	conn.SetReadLimit(2 * 1024 * 1024)

	d.mu.Lock()
	d.activeConn = conn
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		if d.activeConn == conn {
			d.activeConn = nil
		}
		d.mu.Unlock()
	}()

	// 1. Send daemon_hello with computed privacy prompt metadata
	workspaces := make([]protocol.WorkspaceInfo, 0, len(d.cfg.Workspaces))
	for _, ws := range d.cfg.Workspaces {
		workspaces = append(workspaces, protocol.WorkspaceInfo{
			ID:               ws.ID,
			Name:             ws.Name,
			RootPath:         ws.RootPath,
			HasPrivacyPrompt: computeHasPrivacyPrompt(ws),
		})
	}

	maxConc := d.cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = d.cfg.Daemon.MaxConcurrency
	}
	if maxConc <= 0 {
		maxConc = config.DefaultMaxConcurrency
	}

	hello := protocol.DaemonHelloPayload{
		SessionID:      d.sessionID,
		MemberID:       d.cfg.MemberID,
		ClientVersion:  ClientVersion,
		MachineName:    d.cfg.MachineName,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		MaxConcurrency: maxConc,
		Workspaces:     workspaces,
	}
	if err := d.writeEnvelope(ctx, conn, protocol.TypeDaemonHello, hello); err != nil {
		return fmt.Errorf("send hello failed: %w", err)
	}

	d.logger.Info("Connected to Hub successfully", "ws_url", wsURL)
	d.writeStatusFile(true)

	// Heartbeat setup and dynamic interval adjustment
	intervalSec := d.cfg.HeartbeatIntervalSec
	if intervalSec <= 0 {
		intervalSec = config.DefaultHeartbeatIntervalSec
	}
	interval := time.Duration(intervalSec) * time.Second

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Watchdog ticker to evaluate pong deadlines with fine granularity
	watchdog := time.NewTicker(500 * time.Millisecond)
	defer watchdog.Stop()

	pongTimeout := d.pongTimeout
	if pongTimeout <= 0 {
		pongTimeout = 10 * time.Second
	}

	var (
		hbMu         sync.Mutex
		lastPong     = time.Now()
		awaitingPong bool
		pingSentAt   time.Time
	)

	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()

	msgCh := make(chan protocol.Envelope, 32)
	errCh := make(chan error, 1)

	go func() {
		for {
			_, data, err := conn.Read(connCtx)
			if err != nil {
				errCh <- err
				return
			}
			var env protocol.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			// Enforce envelope version check (F32)
			if env.Version != protocol.Version1 {
				d.logger.Warn("Ignoring message with unsupported version", "version", env.Version)
				continue
			}
			select {
			case msgCh <- env:
			case <-connCtx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		case err := <-errCh:
			return fmt.Errorf("read failed: %w", err)

		case <-watchdog.C:
			hbMu.Lock()
			if awaitingPong && time.Since(pingSentAt) > pongTimeout {
				hbMu.Unlock()
				return fmt.Errorf("heartbeat pong timeout (%v exceeded)", pongTimeout)
			}
			if time.Since(lastPong) > time.Duration(float64(interval)*2.5) {
				hbMu.Unlock()
				return fmt.Errorf("heartbeat pong missed threshold (2.5x interval exceeded)")
			}
			hbMu.Unlock()

		case <-ticker.C:
			activeProbes := int(atomic.LoadInt32(&d.activeProbeCount))
			ping := protocol.HeartbeatPingPayload{ActiveProbeCount: activeProbes}
			if err := d.writeEnvelope(connCtx, conn, protocol.TypeHeartbeatPing, ping); err != nil {
				return fmt.Errorf("ping failed: %w", err)
			}
			hbMu.Lock()
			awaitingPong = true
			pingSentAt = time.Now()
			hbMu.Unlock()

		case env := <-msgCh:
			switch env.Type {
			case protocol.TypeHeartbeatPong:
				hbMu.Lock()
				lastPong = time.Now()
				awaitingPong = false
				hbMu.Unlock()
				d.writeStatusFile(true)

			case protocol.TypeHubAck:
				var ack protocol.HubAckPayload
				if err := json.Unmarshal(env.Payload, &ack); err == nil {
					d.sessionID = ack.SessionID
					if ack.HeartbeatIntervalSec > 0 {
						newInterval := time.Duration(ack.HeartbeatIntervalSec) * time.Second
						if newInterval != interval {
							interval = newInterval
							ticker.Reset(interval)
						}
					}
				}
				d.logger.Debug("Received hub_ack", "id", env.ID, "session_id", d.sessionID)
				d.writeStatusFile(true)

			case protocol.TypeQueryCancel:
				var cancelPayload protocol.QueryCancelPayload
				if err := json.Unmarshal(env.Payload, &cancelPayload); err == nil {
					d.activeQueriesMu.Lock()
					cancelFn, ok := d.activeQueries[cancelPayload.QueryID]
					if ok {
						d.logger.Info("Cancelling in-flight query", "query_id", cancelPayload.QueryID, "reason", cancelPayload.Reason)
						cancelFn()
					}
					d.activeQueriesMu.Unlock()
				}

			case protocol.TypeQueryRequest:
				var req protocol.QueryRequestPayload
				if err := json.Unmarshal(env.Payload, &req); err != nil {
					d.logger.Warn("Failed to unmarshal query_request payload", "err", err)
					continue
				}
				go d.handleQueryRequest(connCtx, conn, req)
			}
		}
	}
}

func (d *ClientDaemon) writeEnvelope(ctx context.Context, conn *websocket.Conn, msgType string, payload any) error {
	if conn == nil {
		return errors.New("no active connection")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}
	env := protocol.Envelope{
		Version:   protocol.Version1,
		Type:      msgType,
		ID:        fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		Timestamp: time.Now().UnixMilli(),
		Payload:   raw,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}

	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (d *ClientDaemon) handleQueryRequest(connCtx context.Context, conn *websocket.Conn, req protocol.QueryRequestPayload) {
	d.inFlightWg.Add(1)
	defer d.inFlightWg.Done()

	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = d.cfg.ProbeTimeoutSeconds
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	queryTimeout := time.Duration(timeoutSec) * time.Second

	queryCtx, cancel := context.WithTimeout(connCtx, queryTimeout)
	defer cancel()

	d.activeQueriesMu.Lock()
	d.activeQueries[req.QueryID] = cancel
	d.activeQueriesMu.Unlock()

	defer func() {
		d.activeQueriesMu.Lock()
		delete(d.activeQueries, req.QueryID)
		d.activeQueriesMu.Unlock()
	}()

	// Queue excess frames: wait for concurrency semaphore slot or cancellation (F22, F43)
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-queryCtx.Done():
		if errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
			res := &protocol.QueryResponsePayload{
				QueryID:      req.QueryID,
				Status:       protocol.QueryStatusTimeout,
				ErrorMessage: "query timed out waiting in daemon queue",
			}
			_ = d.sendQueryResponse(conn, res)
		}
		return
	case <-d.stopCh:
		return
	}

	atomic.AddInt32(&d.activeProbeCount, 1)
	d.writeStatusFile(true)
	defer func() {
		atomic.AddInt32(&d.activeProbeCount, -1)
		d.writeStatusFile(true)
	}()

	d.dispatchQuery(queryCtx, conn, req)
}

func (d *ClientDaemon) sendQueryResponse(conn *websocket.Conn, res *protocol.QueryResponsePayload) error {
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.writeEnvelope(writeCtx, conn, protocol.TypeQueryResponse, res)
}

func (d *ClientDaemon) dispatchQuery(ctx context.Context, conn *websocket.Conn, req protocol.QueryRequestPayload) {
	d.logger.Info("Processing query", "query_id", req.QueryID, "asker", req.AskerName, "target_workspace", req.TargetWorkspace)

	if len(d.cfg.Workspaces) == 0 {
		res := &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: "no workspaces configured on daemon",
		}
		_ = d.sendQueryResponse(conn, res)
		return
	}

	// Resolve target workspace by ID, Name, or RootPath (F8, F33)
	selectedWs, matched := d.resolveWorkspace(req.TargetWorkspace)
	if !matched {
		res := &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: fmt.Sprintf("target workspace %q not found on daemon", req.TargetWorkspace),
		}
		_ = d.sendQueryResponse(conn, res)
		return
	}

	if selectedWs.RootPath == "" {
		res := &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: "selected workspace has empty root path",
		}
		_ = d.sendQueryResponse(conn, res)
		return
	}

	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = d.cfg.ProbeTimeoutSeconds
	}
	if timeoutSec <= 0 {
		timeoutSec = 60
	}

	runReq := &probe.RunRequest{
		QueryID:                 req.QueryID,
		Query:                   req.Query,
		Workspace:               selectedWs,
		Workspaces:              d.cfg.Workspaces,
		GlobalPrivacyPromptPath: d.cfg.GlobalPrivacyPromptPath,
		LLMConfig:               d.cfg.LLM,
		Timeout:                 time.Duration(timeoutSec) * time.Second,
	}

	res, err := d.agent.Run(ctx, runReq)
	redactor := probe.NewDefaultRedactor()

	if err != nil {
		status := protocol.QueryStatusError
		if errors.Is(err, context.DeadlineExceeded) || (ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
			status = protocol.QueryStatusTimeout
		}
		redactedErr := redactor.Redact(err.Error())
		res = &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       status,
			ErrorMessage: redactedErr,
			DurationMS:   0,
		}
	} else {
		// Truncate response answer to 16 KB and redact errors & secrets (F10, F15)
		if res.Answer != "" {
			res.Answer = probe.TruncateAnswer(redactor.Redact(res.Answer))
		}
		if res.ErrorMessage != "" {
			res.ErrorMessage = redactor.Redact(res.ErrorMessage)
		}
		if res.Status == "" || res.Status == protocol.QueryStatusSuccess {
			res.Status = protocol.QueryStatusCompleted
		}
	}

	_ = d.sendQueryResponse(conn, res)
}

func (d *ClientDaemon) resolveWorkspace(target string) (config.WorkspaceConfig, bool) {
	if len(d.cfg.Workspaces) == 0 {
		return config.WorkspaceConfig{}, false
	}
	if target == "" {
		return d.cfg.Workspaces[0], true
	}
	cleanTarget := filepath.Clean(target)
	for _, ws := range d.cfg.Workspaces {
		if ws.ID == target {
			return ws, true
		}
		if strings.EqualFold(ws.Name, target) {
			return ws, true
		}
		if ws.RootPath != "" && (ws.RootPath == target || filepath.Clean(ws.RootPath) == cleanTarget) {
			return ws, true
		}
	}
	return config.WorkspaceConfig{}, false
}

func computeHasPrivacyPrompt(ws config.WorkspaceConfig) bool {
	if ws.PrivacyPromptPath != "" {
		if info, err := os.Stat(ws.PrivacyPromptPath); err == nil && !info.IsDir() {
			return true
		}
	}
	if ws.RootPath != "" {
		c1 := filepath.Join(ws.RootPath, ".talkintent", "privacy-prompt.md")
		if info, err := os.Stat(c1); err == nil && !info.IsDir() {
			return true
		}
		c2 := filepath.Join(ws.RootPath, "privacy-prompt.md")
		if info, err := os.Stat(c2); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func computeJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := binary.LittleEndian.Uint64(b[:]) % 1000 // 0 to 999
	// Jitter ratio: -0.20 to +0.20
	ratio := (float64(n)/1000.0)*0.4 - 0.2
	return time.Duration(float64(base) * ratio)
}

func (d *ClientDaemon) initFiles() {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()

	pid := os.Getpid()
	if d.pidFilePath != "" {
		_ = writePIDFile(d.pidFilePath, pid)
	}
	if d.statusFilePath != "" {
		status := DaemonStatus{
			PID:          pid,
			HubURL:       d.cfg.HubURL,
			Connected:    false,
			ActiveProbes: 0,
			Version:      ClientVersion,
			UpdatedAt:    time.Now().UTC(),
		}
		_ = writeAtomicJSON(d.statusFilePath, status)
	}
}

func (d *ClientDaemon) writeStatusFile(connected bool) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()

	if d.statusFilePath == "" {
		return
	}

	status := DaemonStatus{
		PID:           os.Getpid(),
		HubURL:        d.cfg.HubURL,
		Connected:     connected,
		LastHeartbeat: time.Now().UTC(),
		ActiveProbes:  int(atomic.LoadInt32(&d.activeProbeCount)),
		Version:       ClientVersion,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := writeAtomicJSON(d.statusFilePath, status); err != nil {
		d.logger.Warn("Failed to write status file", "err", err, "path", d.statusFilePath)
	}
}

func (d *ClientDaemon) cleanupFiles() {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()

	if d.statusFilePath != "" {
		status := DaemonStatus{
			PID:          os.Getpid(),
			HubURL:       d.cfg.HubURL,
			Connected:    false,
			ActiveProbes: 0,
			Version:      ClientVersion,
			UpdatedAt:    time.Now().UTC(),
		}
		_ = writeAtomicJSON(d.statusFilePath, status)
	}
	if d.pidFilePath != "" {
		_ = os.Remove(d.pidFilePath)
	}
}
