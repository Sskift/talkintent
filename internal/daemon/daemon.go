package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/protocol"
)

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
	wg         sync.WaitGroup
	mu         sync.Mutex
	activeConn *websocket.Conn

	// Concurrency limiter & active probe tracking
	sem              chan struct{}
	activeProbeCount int32

	// Query cancellation registry
	activeQueriesMu sync.Mutex
	activeQueries   map[string]context.CancelFunc

	sessionID string
}

// NewClientDaemon constructs a new ClientDaemon runner.
func NewClientDaemon(cfg *config.ClientConfig, agent probe.Agent, logger *slog.Logger) (*ClientDaemon, error) {
	if cfg == nil {
		return nil, errors.New("client config is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	maxConc := cfg.MaxConcurrency
	if maxConc <= 0 {
		maxConc = config.DefaultMaxConcurrency
	}

	return &ClientDaemon{
		cfg:           cfg,
		agent:         agent,
		logger:        logger,
		stopCh:        make(chan struct{}),
		sem:           make(chan struct{}, maxConc),
		activeQueries: make(map[string]context.CancelFunc),
	}, nil
}

// Start begins the persistent connection loop, reconnecting with exponential backoff.
func (d *ClientDaemon) Start(ctx context.Context) error {
	d.logger.Info("Starting TalkIntent client daemon", "member_id", d.cfg.MemberID, "hub_url", d.cfg.HubURL)

	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		default:
		}

		err := d.connectAndServe(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Warn("WebSocket connection terminated, reconnecting", "err", err, "retry_in", backoff)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.stopCh:
			return nil
		case <-time.After(backoff):
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// Stop cleanly shuts down the daemon.
func (d *ClientDaemon) Stop() error {
	close(d.stopCh)
	d.mu.Lock()
	if d.activeConn != nil {
		_ = d.activeConn.Close(websocket.StatusNormalClosure, "daemon stopping")
	}
	d.mu.Unlock()
	d.wg.Wait()
	return nil
}

func (d *ClientDaemon) connectAndServe(ctx context.Context) error {
	u, err := url.Parse(d.cfg.HubURL)
	if err != nil {
		return fmt.Errorf("invalid hub url %q: %w", d.cfg.HubURL, err)
	}

	wsScheme := "ws"
	if u.Scheme == "https" {
		wsScheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/ws/daemon", wsScheme, u.Host)

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.cfg.Token)
	headers.Set("X-Client-Version", "1.0.0")
	headers.Set("X-Machine-Name", d.cfg.MachineName)

	opts := &websocket.DialOptions{
		HTTPHeader: headers,
	}

	conn, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.CloseNow()

	// Enforce 2 MB frame read limit to prevent read limit exceeded disconnections
	conn.SetReadLimit(2 * 1024 * 1024)

	d.mu.Lock()
	d.activeConn = conn
	d.mu.Unlock()

	// 1. Send daemon_hello
	workspaces := make([]protocol.WorkspaceInfo, 0, len(d.cfg.Workspaces))
	for _, ws := range d.cfg.Workspaces {
		workspaces = append(workspaces, protocol.WorkspaceInfo{
			ID:               ws.ID,
			Name:             ws.Name,
			RootPath:         ws.RootPath,
			HasPrivacyPrompt: ws.PrivacyPromptPath != "",
		})
	}

	hello := protocol.DaemonHelloPayload{
		MemberID:       d.cfg.MemberID,
		ClientVersion:  "1.0.0",
		MachineName:    d.cfg.MachineName,
		MaxConcurrency: d.cfg.MaxConcurrency,
		Workspaces:     workspaces,
	}
	if err := d.writeEnvelope(ctx, conn, protocol.TypeDaemonHello, hello); err != nil {
		return fmt.Errorf("send hello failed: %w", err)
	}

	d.logger.Info("Connected to Hub successfully", "ws_url", wsURL)

	// Heartbeat ticker and deadline verification
	interval := time.Duration(d.cfg.HeartbeatIntervalSec) * time.Second
	if interval <= 0 {
		interval = 20 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastPong := time.Now()
	var awaitingPong bool
	var pingSentAt time.Time

	// Read frames in a goroutine so we can enforce read timeouts/pong deadlines
	msgCh := make(chan protocol.Envelope, 10)
	errCh := make(chan error, 1)

	go func() {
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				errCh <- err
				return
			}
			var env protocol.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			// Reject major protocol version mismatch
			if env.Version != protocol.Version1 {
				d.logger.Warn("Ignoring message with unsupported version", "version", env.Version)
				continue
			}
			msgCh <- env
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
		case <-ticker.C:
			// If awaiting pong from previous ping and 10s elapsed, fail fast
			if awaitingPong && time.Since(pingSentAt) > 10*time.Second {
				return errors.New("heartbeat pong timeout (10s exceeded)")
			}
			if time.Since(lastPong) > time.Duration(float64(interval)*2.5) {
				return errors.New("heartbeat pong missed threshold (2.5x interval exceeded)")
			}

			activeProbes := int(atomic.LoadInt32(&d.activeProbeCount))
			ping := protocol.HeartbeatPingPayload{ActiveProbeCount: activeProbes}
			if err := d.writeEnvelope(ctx, conn, protocol.TypeHeartbeatPing, ping); err != nil {
				return fmt.Errorf("ping failed: %w", err)
			}
			awaitingPong = true
			pingSentAt = time.Now()

		case env := <-msgCh:
			switch env.Type {
			case protocol.TypeHeartbeatPong:
				lastPong = time.Now()
				awaitingPong = false
			case protocol.TypeHubAck:
				var ack protocol.HubAckPayload
				if err := json.Unmarshal(env.Payload, &ack); err == nil {
					d.sessionID = ack.SessionID
				}
				d.logger.Debug("Received hub_ack", "id", env.ID, "session_id", d.sessionID)
			case protocol.TypeQueryCancel:
				var cancelPayload protocol.QueryCancelPayload
				if err := json.Unmarshal(env.Payload, &cancelPayload); err == nil {
					d.activeQueriesMu.Lock()
					if cancelFn, ok := d.activeQueries[cancelPayload.QueryID]; ok {
						d.logger.Info("Cancelling in-flight query", "query_id", cancelPayload.QueryID, "reason", cancelPayload.Reason)
						cancelFn()
					}
					d.activeQueriesMu.Unlock()
				}
			case protocol.TypeQueryRequest:
				var req protocol.QueryRequestPayload
				if err := json.Unmarshal(env.Payload, &req); err != nil {
					continue
				}
				go d.handleQueryRequest(ctx, conn, req)
			}
		}
	}
}

func (d *ClientDaemon) writeEnvelope(ctx context.Context, conn *websocket.Conn, msgType string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
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
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (d *ClientDaemon) handleQueryRequest(ctx context.Context, conn *websocket.Conn, req protocol.QueryRequestPayload) {
	// Concurrency throttling
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		// Saturated capacity
		res := &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: "DAEMON_CONCURRENCY_EXCEEDED",
		}
		_ = d.writeEnvelope(ctx, conn, protocol.TypeQueryResponse, res)
		return
	}

	atomic.AddInt32(&d.activeProbeCount, 1)
	defer atomic.AddInt32(&d.activeProbeCount, -1)

	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	d.activeQueriesMu.Lock()
	d.activeQueries[req.QueryID] = cancel
	d.activeQueriesMu.Unlock()

	defer func() {
		d.activeQueriesMu.Lock()
		delete(d.activeQueries, req.QueryID)
		d.activeQueriesMu.Unlock()
	}()

	d.dispatchQuery(queryCtx, conn, req)
}

func (d *ClientDaemon) dispatchQuery(ctx context.Context, conn *websocket.Conn, req protocol.QueryRequestPayload) {
	d.logger.Info("Processing query", "query_id", req.QueryID, "asker", req.AskerName, "target_workspace", req.TargetWorkspace)

	if len(d.cfg.Workspaces) == 0 {
		res := &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: "no workspaces configured on daemon",
		}
		_ = d.writeEnvelope(ctx, conn, protocol.TypeQueryResponse, res)
		return
	}

	// Resolve target workspace matching req.TargetWorkspace
	var selectedWs config.WorkspaceConfig
	var matched bool

	if req.TargetWorkspace != "" {
		for _, ws := range d.cfg.Workspaces {
			if ws.Name == req.TargetWorkspace || ws.ID == req.TargetWorkspace {
				selectedWs = ws
				matched = true
				break
			}
		}
		if !matched {
			res := &protocol.QueryResponsePayload{
				QueryID:      req.QueryID,
				Status:       protocol.QueryStatusError,
				ErrorMessage: fmt.Sprintf("target workspace %q not found on daemon", req.TargetWorkspace),
			}
			_ = d.writeEnvelope(ctx, conn, protocol.TypeQueryResponse, res)
			return
		}
	} else {
		selectedWs = d.cfg.Workspaces[0]
	}

	// Verify workspace root is not empty
	if selectedWs.RootPath == "" {
		res := &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: "selected workspace has empty root path",
		}
		_ = d.writeEnvelope(ctx, conn, protocol.TypeQueryResponse, res)
		return
	}

	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 60
	}

	runReq := &probe.RunRequest{
		QueryID:    req.QueryID,
		Query:      req.Query,
		Workspace:  selectedWs,
		Workspaces: d.cfg.Workspaces,
		LLMConfig:  d.cfg.LLM,
		Timeout:    time.Duration(timeoutSec) * time.Second,
	}

	res, err := d.agent.Run(ctx, runReq)
	if err != nil {
		res = &protocol.QueryResponsePayload{
			QueryID:      req.QueryID,
			Status:       protocol.QueryStatusError,
			ErrorMessage: err.Error(),
			DurationMS:   0,
		}
	} else {
		// Truncate response answer to 16 KB and normalize status
		res.Answer = probe.TruncateAnswer(res.Answer)
		if res.Status == "" || res.Status == protocol.QueryStatusSuccess {
			res.Status = protocol.QueryStatusCompleted
		}
	}

	_ = d.writeEnvelope(ctx, conn, protocol.TypeQueryResponse, res)
}
