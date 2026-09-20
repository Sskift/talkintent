package feishu

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// ClientConfig holds dynamic connection intervals provided by Feishu server.
type ClientConfig struct {
	ReconnectCount    int `json:"ReconnectCount"`
	ReconnectInterval int `json:"ReconnectInterval"` // seconds
	ReconnectNonce    int `json:"ReconnectNonce"`    // seconds
	PingInterval      int `json:"PingInterval"`      // seconds
}

// EndpointResp matches Feishu /callback/ws/endpoint response structure.
type EndpointResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data *struct {
		URL          string        `json:"url"`
		ClientConfig *ClientConfig `json:"ClientConfig"`
	} `json:"data"`
}

// ClientError represents a fatal, non-retryable error from Feishu API.
type ClientError struct {
	Code int
	Msg  string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("feishu client error (code %d): %s", e.Code, e.Msg)
}

// ServerError represents a retryable error from Feishu server.
type ServerError struct {
	Code int
	Msg  string
}

func (e *ServerError) Error() string {
	return fmt.Sprintf("feishu server error (code %d): %s", e.Code, e.Msg)
}

// EventHandlerFunc receives decoded complete event JSON payloads from the long connection.
type EventHandlerFunc func(ctx context.Context, payload []byte) error

// WSClientConfig configures the Feishu long-connection WebSocket client.
type WSClientConfig struct {
	MemberID     string
	AppID        string
	AppSecret    string
	BaseURL      string
	EventHandler EventHandlerFunc
	Logger       *slog.Logger
	HTTPClient   *http.Client
}

// WSClient manages a Feishu WebSocket long connection for one bot binding.
type WSClient struct {
	cfg         WSClientConfig
	logger      *slog.Logger
	httpClient  *http.Client
	reassembler *FragmentReassembler

	stateMu      sync.RWMutex
	state        string // "connecting", "connected", "disconnected", "error", "stopped"
	lastError    string
	connectedAt  string
	reconnects   int
	clientConfig ClientConfig
	pingResetCh  chan time.Duration

	connMu     sync.Mutex
	activeConn *websocket.Conn
	serviceID  int32

	stopOnce   sync.Once
	stopCh     chan struct{}
	rootCtx    context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup
}

// NewWSClient creates a new Feishu WebSocket client.
func NewWSClient(cfg WSClientConfig) *WSClient {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.feishu.cn"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "feishu_ws", "app_id", cfg.AppID, "member_id", cfg.MemberID)

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())

	return &WSClient{
		cfg:         cfg,
		logger:      logger,
		httpClient:  httpClient,
		reassembler: NewFragmentReassembler(5 * time.Second),
		state:       "disconnected",
		pingResetCh: make(chan time.Duration, 1),
		clientConfig: ClientConfig{
			ReconnectCount:    -1,
			ReconnectInterval: 120,
			ReconnectNonce:    30,
			PingInterval:      120,
		},
		stopCh:     make(chan struct{}),
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
	}
}

// Start initiates the background long-connection loop.
func (c *WSClient) Start() {
	c.wg.Add(1)
	go c.runLoop()
}

// Stop cleanly shuts down the client and closes the active WebSocket connection.
func (c *WSClient) Stop() {
	c.stopOnce.Do(func() {
		c.stateMu.Lock()
		if c.state != "error" {
			c.state = "stopped"
			c.lastError = ""
			c.connectedAt = ""
		}
		c.stateMu.Unlock()
		c.rootCancel()
		close(c.stopCh)

		c.connMu.Lock()
		if c.activeConn != nil {
			_ = c.activeConn.Close(websocket.StatusNormalClosure, "client stopped")
		}
		c.connMu.Unlock()
	})
	c.wg.Wait()
}

// Status returns the current connection state and any recent error.
func (c *WSClient) Status() (string, string) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state, c.lastError
}

// LiveStatus returns current connection state, error, connected_at RFC3339 string, and reconnect count.
func (c *WSClient) LiveStatus() (state, lastErr, connectedAt string, reconnects int) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state, c.lastError, c.connectedAt, c.reconnects
}

func (c *WSClient) redact(s string) string {
	if c.cfg.AppSecret != "" && s != "" {
		return strings.ReplaceAll(s, c.cfg.AppSecret, "[REDACTED]")
	}
	return s
}

func (c *WSClient) setState(state, errMsg string) {
	errMsg = c.redact(errMsg)
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.state == "stopped" {
		return
	}
	c.state = state
	c.lastError = errMsg
	// connected_at describes the *current* session; the REST/UI contract omits it
	// whenever the client is not connected.
	if state != "connected" {
		c.connectedAt = ""
	}
}

func (c *WSClient) getConfig() ClientConfig {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.clientConfig
}

func (c *WSClient) updateConfig(cfg *ClientConfig) {
	if cfg == nil {
		return
	}
	c.stateMu.Lock()
	oldPing := c.clientConfig.PingInterval
	if cfg.ReconnectCount != 0 {
		c.clientConfig.ReconnectCount = cfg.ReconnectCount
	}
	if cfg.ReconnectInterval > 0 {
		c.clientConfig.ReconnectInterval = cfg.ReconnectInterval
	}
	if cfg.ReconnectNonce > 0 {
		c.clientConfig.ReconnectNonce = cfg.ReconnectNonce
	}
	if cfg.PingInterval > 0 {
		c.clientConfig.PingInterval = cfg.PingInterval
	}
	newPing := c.clientConfig.PingInterval
	c.stateMu.Unlock()

	if newPing > 0 && newPing != oldPing {
		select {
		case c.pingResetCh <- time.Duration(newPing) * time.Second:
		default:
			// Drain old if any and replace
			select {
			case <-c.pingResetCh:
			default:
			}
			c.pingResetCh <- time.Duration(newPing) * time.Second
		}
	}
}

// FetchEndpoint discovers the WebSocket URL and connection settings from Feishu.
func (c *WSClient) FetchEndpoint(ctx context.Context) (string, *ClientConfig, error) {
	requestURL := c.cfg.BaseURL + "/callback/ws/endpoint"
	bodyData := map[string]string{
		"AppID":     c.cfg.AppID,
		"AppSecret": c.cfg.AppSecret,
	}
	bs, err := json.Marshal(bodyData)
	if err != nil {
		return "", nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewBuffer(bs))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("locale", "zh")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}

	if resp.StatusCode != http.StatusOK {
		var errResp EndpointResp
		_ = json.Unmarshal(respBytes, &errResp)
		msg := errResp.Msg
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return "", nil, &ServerError{Code: resp.StatusCode, Msg: msg}
	}

	var endpointResp EndpointResp
	if err := json.Unmarshal(respBytes, &endpointResp); err != nil {
		return "", nil, fmt.Errorf("failed to parse endpoint response: %w", err)
	}

	switch endpointResp.Code {
	case 0:
		// OK
	case 1:
		return "", nil, &ServerError{Code: 1, Msg: "system busy"}
	case 1000040343:
		return "", nil, &ServerError{Code: 1000040343, Msg: endpointResp.Msg}
	default:
		return "", nil, &ClientError{Code: endpointResp.Code, Msg: endpointResp.Msg}
	}

	if endpointResp.Data == nil || endpointResp.Data.URL == "" {
		return "", nil, &ServerError{Code: 500, Msg: "empty endpoint url received"}
	}

	return endpointResp.Data.URL, endpointResp.Data.ClientConfig, nil
}

func (c *WSClient) runLoop() {
	defer c.wg.Done()

	reconnectAttempt := 0
	reconnectAttemptCount := 0

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		c.setState("connecting", "")

		connectCtx, connectCancel := context.WithTimeout(c.rootCtx, 30*time.Second)
		wsURL, conf, err := c.FetchEndpoint(connectCtx)
		connectCancel()

		if err != nil {
			select {
			case <-c.stopCh:
				return
			default:
			}

			var clientErr *ClientError
			if errors.As(err, &clientErr) {
				c.logger.Error("Fatal Feishu client error, terminating long connection", "code", clientErr.Code, "msg", c.redact(clientErr.Msg))
				c.setState("error", clientErr.Error())
				return
			}

			if currentConf := c.getConfig(); currentConf.ReconnectCount >= 0 && reconnectAttempt >= currentConf.ReconnectCount {
				c.logger.Error("Reconnect count exceeded", "attempts", reconnectAttempt, "max", currentConf.ReconnectCount)
				c.setState("error", "reconnect limit reached")
				return
			}

			c.logger.Warn("Failed to fetch Feishu endpoint, will retry", "err", c.redact(err.Error()))
			c.setState("disconnected", err.Error())
			if !c.sleepBackoff(reconnectAttempt) {
				return
			}
			reconnectAttempt++
			continue
		}

		if conf != nil {
			c.updateConfig(conf)
		}

		// Extract service_id if provided in URL query parameters
		if parsed, err := url.Parse(wsURL); err == nil {
			if sidStr := parsed.Query().Get("service_id"); sidStr != "" {
				if sid, err := strconv.ParseInt(sidStr, 10, 32); err == nil {
					c.connMu.Lock()
					c.serviceID = int32(sid)
					c.connMu.Unlock()
				}
			}
		}

		// Dial WebSocket
		dialCtx, dialCancel := context.WithTimeout(c.rootCtx, 20*time.Second)
		conn, _, err := websocket.Dial(dialCtx, wsURL, nil)
		dialCancel()

		if err != nil {
			select {
			case <-c.stopCh:
				return
			default:
			}

			if currentConf := c.getConfig(); currentConf.ReconnectCount >= 0 && reconnectAttempt >= currentConf.ReconnectCount {
				c.logger.Error("Reconnect count exceeded", "attempts", reconnectAttempt, "max", currentConf.ReconnectCount)
				c.setState("error", "reconnect limit reached")
				return
			}

			c.logger.Warn("Failed to dial Feishu WebSocket, will retry", "err", c.redact(err.Error()))
			c.setState("disconnected", err.Error())
			if !c.sleepBackoff(reconnectAttempt) {
				return
			}
			reconnectAttempt++
			continue
		}

		// Connection established! Reset reconnect attempts
		reconnectAttempt = 0
		conn.SetReadLimit(10 * 1024 * 1024)

		c.connMu.Lock()
		c.activeConn = conn
		c.connMu.Unlock()

		c.stateMu.Lock()
		// Stop() may have raced the dial; never resurrect a stopped client.
		if c.state != "stopped" {
			c.state = "connected"
			c.lastError = ""
			c.connectedAt = time.Now().Format(time.RFC3339)
			if reconnectAttemptCount > 0 {
				c.reconnects++
			}
			reconnectAttemptCount++
		}
		c.stateMu.Unlock()
		c.logger.Info("Feishu long-connection established")

		// Serve active connection
		connErr := c.serveConnection(conn)

		c.connMu.Lock()
		c.activeConn = nil
		c.connMu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "connection cycle ended")

		select {
		case <-c.stopCh:
			return
		default:
		}

		if connErr != nil {
			c.logger.Warn("Feishu WebSocket connection lost", "err", connErr)
			c.setState("disconnected", connErr.Error())
		} else {
			c.setState("disconnected", "")
		}

		if currentConf := c.getConfig(); currentConf.ReconnectCount >= 0 && reconnectAttempt >= currentConf.ReconnectCount {
			c.logger.Error("Reconnect count exceeded", "attempts", reconnectAttempt, "max", currentConf.ReconnectCount)
			c.setState("error", "reconnect limit reached")
			return
		}

		if !c.sleepBackoff(reconnectAttempt) {
			return
		}
		reconnectAttempt++
	}
}

func (c *WSClient) serveConnection(conn *websocket.Conn) error {
	cfg := c.getConfig()
	pingInterval := time.Duration(cfg.PingInterval) * time.Second
	if pingInterval <= 0 {
		pingInterval = 120 * time.Second
	}

	connCtx, connCancel := context.WithCancel(c.rootCtx)
	defer connCancel()

	// Ping loop
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		// Send initial ping immediately
		c.sendPing(conn)

		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-c.stopCh:
				return
			case <-connCtx.Done():
				return
			case newInterval := <-c.pingResetCh:
				if newInterval > 0 {
					ticker.Reset(newInterval)
				}
			case <-ticker.C:
				c.sendPing(conn)
			}
		}
	}()

	// Read loop
	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			select {
			case <-c.stopCh:
				return
			case <-connCtx.Done():
				return
			default:
			}

			// Read timeout: 2 * dynamic pingInterval + 5s
			cfg := c.getConfig()
			interval := time.Duration(cfg.PingInterval) * time.Second
			if interval <= 0 {
				interval = 120 * time.Second
			}
			timeout := 2*interval + 5*time.Second
			readCtx, readCancel := context.WithTimeout(connCtx, timeout)
			msgType, data, err := conn.Read(readCtx)
			readCancel()

			if err != nil {
				readErrCh <- err
				return
			}

			if msgType != websocket.MessageBinary {
				continue
			}

			c.handleIncomingFrame(conn, data)
		}
	}()

	select {
	case <-c.stopCh:
		connCancel()
		<-readErrCh
		<-pingDone
		return nil
	case err := <-readErrCh:
		connCancel()
		<-pingDone
		return err
	}
}

func (c *WSClient) sendPing(conn *websocket.Conn) {
	c.connMu.Lock()
	serviceID := c.serviceID
	c.connMu.Unlock()

	ping := NewPingFrame(serviceID)
	data, err := ping.Marshal()
	if err != nil {
		c.logger.Warn("Failed to marshal ping frame", "err", err)
		return
	}

	// Detached context for writing
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer writeCancel()

	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.activeConn != conn {
		return
	}
	if err := conn.Write(writeCtx, websocket.MessageBinary, data); err != nil {
		c.logger.Warn("Failed to write ping frame", "err", err)
	}
}

func (c *WSClient) handleIncomingFrame(conn *websocket.Conn, raw []byte) {
	var f Frame
	if err := f.Unmarshal(raw); err != nil {
		c.logger.Warn("Failed to unmarshal incoming frame", "err", err)
		return
	}

	switch f.Method {
	case 0: // Control
		if f.HeaderValue("type") == "pong" {
			if len(f.Payload) > 0 {
				var conf ClientConfig
				if err := json.Unmarshal(f.Payload, &conf); err == nil {
					c.updateConfig(&conf)
				}
			}
		}
	case 1: // Data
		if f.HeaderValue("type") != "event" {
			return
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.processDataFrame(conn, f)
		}()
	}
}

func (c *WSClient) processDataFrame(conn *websocket.Conn, req Frame) {
	startedAt := time.Now()

	sum := 1
	if s := req.HeaderValue("sum"); s != "" {
		if val, err := strconv.Atoi(s); err == nil {
			sum = val
		}
	}
	seq := 0
	if s := req.HeaderValue("seq"); s != "" {
		if val, err := strconv.Atoi(s); err == nil {
			seq = val
		}
	}
	msgID := req.HeaderValue("message_id")

	payload := req.Payload
	if sum > 1 {
		payload = c.reassembler.AddFragment(msgID, sum, seq, req.Payload)
		if payload == nil {
			// Incomplete fragments, waiting for others
			return
		}
	}

	var handlerErr error
	if c.cfg.EventHandler != nil && len(payload) > 0 {
		ctx, cancel := context.WithTimeout(c.rootCtx, 30*time.Second)
		handlerErr = c.cfg.EventHandler(ctx, payload)
		cancel()
	}

	// Build and echo response frame
	elapsedMs := time.Since(startedAt).Milliseconds()
	respFrame := Frame{
		SeqID:           req.SeqID,
		LogID:           req.LogID,
		Service:         req.Service,
		Method:          req.Method,
		PayloadEncoding: req.PayloadEncoding,
		PayloadType:     req.PayloadType,
		LogIDNew:        req.LogIDNew,
	}

	// Clone original headers and append biz_rt
	for _, h := range req.Headers {
		respFrame.Headers = append(respFrame.Headers, h)
	}
	respFrame.SetHeader("biz_rt", strconv.FormatInt(elapsedMs, 10))

	code := 200
	if handlerErr != nil {
		code = 500
		c.logger.Error("Event handler failed", "err", handlerErr, "message_id", msgID)
	}

	respPayload, _ := json.Marshal(map[string]any{
		"code":    code,
		"headers": nil,
		"data":    nil,
	})
	respFrame.Payload = respPayload

	wire, err := respFrame.Marshal()
	if err != nil {
		c.logger.Error("Failed to marshal response frame", "err", err)
		return
	}

	// Dedicated detached context for writing to shared connection
	writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer writeCancel()

	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.activeConn != conn {
		return
	}
	if err := conn.Write(writeCtx, websocket.MessageBinary, wire); err != nil {
		c.logger.Warn("Failed to write response frame", "err", err, "message_id", msgID)
	}
}

func (c *WSClient) sleepBackoff(attempt int) bool {
	cfg := c.getConfig()

	var sleepDuration time.Duration
	if attempt == 0 {
		// First reconnect: random jitter in [0, ReconnectNonce)
		nonceSec := cfg.ReconnectNonce
		if nonceSec <= 0 {
			nonceSec = 30
		}
		nBig, _ := rand.Int(rand.Reader, big.NewInt(int64(nonceSec*1000)))
		sleepDuration = time.Duration(nBig.Int64()) * time.Millisecond
	} else {
		// Subsequent: ReconnectInterval (default 120s)
		intervalSec := cfg.ReconnectInterval
		if intervalSec <= 0 {
			intervalSec = 120
		}
		sleepDuration = time.Duration(intervalSec) * time.Second
	}

	if sleepDuration < 100*time.Millisecond {
		sleepDuration = 100 * time.Millisecond
	}

	select {
	case <-c.stopCh:
		return false
	case <-time.After(sleepDuration):
		return true
	}
}
