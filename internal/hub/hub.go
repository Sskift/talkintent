package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/feishu"
	"github.com/Sskift/talkintent/internal/protocol"
	"github.com/Sskift/talkintent/internal/store"
)

// Server defines the contract for the Hub daemon.
type Server interface {
	Addr() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// HubServer is the central coordination server for TalkIntent.
type HubServer struct {
	cfg           *config.HubConfig
	store         store.Store
	webFS         fs.FS
	logger        *slog.Logger
	httpServer    *http.Server
	listener      net.Listener
	feishuHandler *feishu.Handler
	rateLimiter   *rateLimiter
	stopCh        chan struct{}

	mu             sync.RWMutex
	daemonConns    map[string]*daemonSession                       // memberID -> primary/latest session
	memberSessions map[string]map[string]*daemonSession            // memberID -> sessionID -> session
	sessionsByID   map[string]*daemonSession                       // sessionID -> session
	queryWaiters   map[string][]chan *protocol.QueryDetailResponse // queryID -> []chan
	queryTimeouts  map[string]int                                  // queryID -> timeoutSeconds (for offline queue drain)

	// 1-hour idempotency cache: key -> queryID
	idempotencyMu    sync.Mutex
	idempotencyCache map[string]idempotencyRecord
}

type idempotencyRecord struct {
	queryID   string
	expiresAt time.Time
}

// envelopeWriteTimeout bounds a single frame write to a daemon; a peer that cannot
// drain a small JSON frame in this long is dead and the read loop's heartbeat deadline
// will reap it.
const envelopeWriteTimeout = 10 * time.Second

type daemonSession struct {
	sessionID      string
	conn           *websocket.Conn
	memberID       string
	machineName    string
	workspaces     []protocol.WorkspaceInfo
	maxConcurrency int
	wsLock         sync.Mutex
	closed         bool
	lastSeen       time.Time
}

// NewServer creates a new HubServer instance.
func NewServer(cfg *config.HubConfig, st store.Store, webFS fs.FS, logger *slog.Logger) (*HubServer, error) {
	if cfg == nil {
		return nil, errors.New("hub config is required")
	}
	if st == nil {
		return nil, errors.New("store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	// 1. Ensure Admin Token is initialized and persisted (F4, F25, F46)
	if cfg.AdminToken == "" {
		tokenPath := filepath.Join(cfg.DataDir, "admin.token")
		if data, err := os.ReadFile(tokenPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			cfg.AdminToken = strings.TrimSpace(string(data))
		} else {
			token, err := store.RandomHex(24)
			if err != nil {
				return nil, fmt.Errorf("failed to generate admin token: %w", err)
			}
			cfg.AdminToken = "ti_adm_" + token
			_ = os.MkdirAll(cfg.DataDir, 0700)
			if err := os.WriteFile(tokenPath, []byte(cfg.AdminToken+"\n"), 0600); err != nil {
				return nil, fmt.Errorf("failed to write admin token file: %w", err)
			}
			logger.Info("Generated and saved Hub admin token", "path", tokenPath)
		}
	}

	qpm := cfg.RateLimits.QueriesPerMinute
	burst := cfg.RateLimits.Burst
	if qpm <= 0 && cfg.RateLimit.QueriesPerMinute > 0 {
		qpm = cfg.RateLimit.QueriesPerMinute
	}
	if burst <= 0 && cfg.RateLimit.Burst > 0 {
		burst = cfg.RateLimit.Burst
	}

	h := &HubServer{
		cfg:              cfg,
		store:            st,
		webFS:            webFS,
		logger:           logger,
		daemonConns:      make(map[string]*daemonSession),
		memberSessions:   make(map[string]map[string]*daemonSession),
		sessionsByID:     make(map[string]*daemonSession),
		queryWaiters:     make(map[string][]chan *protocol.QueryDetailResponse),
		queryTimeouts:    make(map[string]int),
		idempotencyCache: make(map[string]idempotencyRecord),
		rateLimiter:      newRateLimiter(qpm, burst),
		stopCh:           make(chan struct{}),
	}

	// Initialize Feishu Webhook & Reply Handler (WP5 integration, F40)
	feishuCfg := feishu.HandlerConfig{
		BindingLookup: func(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
			return h.store.GetFeishuBinding(ctx, memberID)
		},
		Dispatch: func(ctx context.Context, targetMemberID string, asker feishu.AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*feishu.DispatchResult, error) {
			return h.dispatchFeishuQuery(ctx, targetMemberID, asker, queryText, feishuCtx)
		},
		Logger: logger,
	}
	h.feishuHandler = feishu.NewHandler(feishuCfg)

	mux := http.NewServeMux()
	h.registerRoutes(mux)

	h.httpServer = &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	return h, nil
}

// shutdownTimeout bounds the shutdown duration in Start. If in-flight handlers
// or connections do not drain within this window, the server forcefully closes all listeners
// and connections via httpServer.Close. 10s gives long-running requests ample time to wrap up
// cleanly while ensuring process exit (e.g. on SIGINT/SIGTERM) does not hang indefinitely.
const shutdownTimeout = 10 * time.Second

// Addr returns the network address the server is listening on. Callers poll this from
// another goroutine while Start is binding (e.g. tests using ":0"), so the listener
// field is read under the lock.
func (h *HubServer) Addr() string {
	h.mu.RLock()
	ln := h.listener
	h.mu.RUnlock()
	if ln != nil {
		return ln.Addr().String()
	}
	return h.cfg.Addr
}

// Handler returns the HTTP handler used by the server.
func (h *HubServer) Handler() http.Handler {
	return h.httpServer.Handler
}

// Store returns the underlying storage engine.
func (h *HubServer) Store() store.Store {
	return h.store
}

// AdminToken returns the administrative bearer token.
func (h *HubServer) AdminToken() string {
	return h.cfg.AdminToken
}

// Start opens the network port, launches background tasks, and serves requests until context cancellation.
func (h *HubServer) Start(ctx context.Context) error {
	addr := h.cfg.Addr
	if addr == "" {
		addr = ":8080"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	h.mu.Lock()
	h.listener = ln
	h.mu.Unlock()
	h.logger.Info("TalkIntent Hub listening", "addr", ln.Addr().String(), "data_dir", h.cfg.DataDir)

	// Background ticker for sweeping expired queries and notifying waiters (F19/F41)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.stopCh:
				return
			case <-ticker.C:
				count, err := h.store.SweepExpiredQueries(context.Background())
				if err == nil && count > 0 {
					h.logger.Debug("Swept expired queries", "count", count)
					h.mu.Lock()
					var waiterIDs []string
					for qid := range h.queryWaiters {
						waiterIDs = append(waiterIDs, qid)
					}
					h.mu.Unlock()

					for _, qid := range waiterIDs {
						if q, err := h.store.GetQuery(context.Background(), qid); err == nil && q.Status == protocol.QueryStatusExpired {
							h.notifyWaiters(qid)
							if q.FeishuContext != nil && q.FeishuContext.MessageID != "" && h.feishuHandler != nil {
								_ = h.feishuHandler.OnQueryComplete(context.Background(), q)
							}
						}
					}
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		if err := h.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := h.Stop(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = h.httpServer.Close()
			return err
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// Stop gracefully shuts down the server, honoring the caller-provided ctx deadline.
// If the context expires or shutdown errors, httpServer.Close is called to terminate
// any remaining connections.
func (h *HubServer) Stop(ctx context.Context) error {
	h.logger.Info("Shutting down Hub server")
	select {
	case <-h.stopCh:
	default:
		close(h.stopCh)
	}

	// Gracefully close all connected WebSocket sessions
	h.mu.Lock()
	for _, sess := range h.sessionsByID {
		sess.closed = true
		_ = sess.conn.Close(websocket.StatusNormalClosure, "hub shutting down")
	}
	h.sessionsByID = make(map[string]*daemonSession)
	h.memberSessions = make(map[string]map[string]*daemonSession)
	h.daemonConns = make(map[string]*daemonSession)
	h.mu.Unlock()

	if err := h.httpServer.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_ = h.httpServer.Close()
		return err
	}
	return nil
}

func (h *HubServer) registerRoutes(mux *http.ServeMux) {
	// WebSocket endpoint for client daemons
	mux.HandleFunc("/ws/daemon", h.handleWebSocket)

	// Authentication & Onboarding
	mux.HandleFunc("POST /api/v1/auth/pair", h.handleAuthPair)
	mux.HandleFunc("POST /api/v1/admin/invites", h.handleAdminInvites)

	// Member Directory & Profile
	mux.HandleFunc("GET /api/v1/members", h.handleMembersList)
	mux.HandleFunc("GET /api/v1/members/me", h.handleMemberMe)
	mux.HandleFunc("PUT /api/v1/members/me", h.handleMemberMeUpdate)
	mux.HandleFunc("GET /api/v1/members/", h.handleMemberGet)

	// Queries & Async Dispatch
	mux.HandleFunc("POST /api/v1/queries", h.handleQuerySubmit)
	mux.HandleFunc("GET /api/v1/queries/", h.handleQueryDetail)

	// Inbound & Outbound Audit Trails
	mux.HandleFunc("GET /api/v1/audit/inbound", h.handleAuditInbound)
	mux.HandleFunc("GET /api/v1/audit/outbound", h.handleAuditOutbound)

	// Feishu Integration & Webhooks
	mux.HandleFunc("GET /api/v1/feishu/binding", h.handleFeishuBindingGet)
	mux.HandleFunc("POST /api/v1/feishu/binding", h.handleFeishuBindingSave)
	mux.HandleFunc("DELETE /api/v1/feishu/binding", h.handleFeishuBindingDelete)
	mux.HandleFunc("POST /api/v1/feishu/webhook/", h.feishuHandler.ServeHTTP)

	// Static Web UI
	if h.webFS != nil {
		sub := h.webFS
		// If webFS embeds with "static/..." prefix (like embed.FS static/*), un-nest it
		if _, err := fs.Stat(sub, "static/index.html"); err == nil {
			if s, err := fs.Sub(sub, "static"); err == nil {
				sub = s
			}
		}
		fileServer := http.FileServer(http.FS(sub))
		mux.Handle("/web/", http.StripPrefix("/web", fileServer))
		mux.HandleFunc("GET /web", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/", http.StatusFound)
		})
		mux.Handle("GET /static/", http.StripPrefix("/static", fileServer))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/", http.StatusFound)
		})
		mux.Handle("GET /index.html", fileServer)
		mux.Handle("GET /style.css", fileServer)
		mux.Handle("GET /app.js", fileServer)
	}
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
		Error: protocol.ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func (h *HubServer) removeWaiter(queryID string, waitCh chan *protocol.QueryDetailResponse) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	var remaining []chan *protocol.QueryDetailResponse
	for _, ch := range h.queryWaiters[queryID] {
		if ch != waitCh {
			remaining = append(remaining, ch)
		}
	}
	if len(remaining) == 0 {
		delete(h.queryWaiters, queryID)
	} else {
		h.queryWaiters[queryID] = remaining
	}
	return len(remaining)
}

func (h *HubServer) cancelDispatchedQuery(queryID, targetMemberID, targetWorkspace, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q, err := h.store.GetQuery(ctx, queryID)
	if err != nil || q.Status != protocol.QueryStatusDispatched {
		return
	}

	targetID := targetMemberID
	if targetID == "" {
		targetID = q.TargetMemberID
	}
	ws := targetWorkspace
	if ws == "" {
		ws = q.TargetWorkspace
	}

	sess := h.selectSessionForQuery(targetID, ws)
	if sess == nil {
		return
	}

	cancelPayload := protocol.QueryCancelPayload{
		QueryID: queryID,
		Reason:  reason,
	}
	h.sendEnvelope(ctx, sess, protocol.TypeQueryCancel, cancelPayload)
	h.logger.Info("Dispatched query cancel frame to daemon", "query_id", queryID, "reason", reason, "member_id", targetID)
}

func (h *HubServer) getQueryTimeout(queryID string) int {
	h.mu.RLock()
	t, ok := h.queryTimeouts[queryID]
	h.mu.RUnlock()
	if !ok || t <= 0 {
		t = 60
	}
	if h.cfg.MaxProbeTimeoutSec > 0 && t > h.cfg.MaxProbeTimeoutSec {
		t = h.cfg.MaxProbeTimeoutSec
	}
	return t
}

func (h *HubServer) setQueryTimeout(queryID string, timeoutSec int) {
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if h.cfg.MaxProbeTimeoutSec > 0 && timeoutSec > h.cfg.MaxProbeTimeoutSec {
		timeoutSec = h.cfg.MaxProbeTimeoutSec
	}
	h.mu.Lock()
	h.queryTimeouts[queryID] = timeoutSec
	h.mu.Unlock()
}

// authenticateMember resolves a member from the Authorization header.
func (h *HubServer) authenticateMember(r *http.Request) (*protocol.MemberInfo, error) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return nil, errors.New("missing or malformed Authorization header")
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	return h.store.GetMemberByToken(r.Context(), token)
}

// authenticateAdmin verifies that the request carries the admin token (F4, F25).
func (h *HubServer) authenticateAdmin(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.AdminToken)) == 1
}

// selectSessionForQuery picks the best daemon session for a member (matching targetWorkspace if specified).
func (h *HubServer) selectSessionForQuery(memberID string, targetWorkspace string) *daemonSession {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sessions := h.memberSessions[memberID]
	if len(sessions) == 0 {
		return nil
	}

	if targetWorkspace != "" {
		for _, sess := range sessions {
			for _, ws := range sess.workspaces {
				if ws.Name == targetWorkspace || ws.ID == targetWorkspace || ws.RootPath == targetWorkspace {
					return sess
				}
			}
		}
	}

	var best *daemonSession
	for _, sess := range sessions {
		if best == nil || sess.lastSeen.After(best.lastSeen) {
			best = sess
		}
	}
	return best
}

// handleWebSocket handles incoming client daemon connections with multi-device support and takeover semantics (F16, F17, F18, F23).
func (h *HubServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "missing bearer token")
		return
	}

	mem, err := h.store.GetMemberByToken(r.Context(), token)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "invalid token")
		return
	}

	// Validate Origin header if present (F16)
	originPatterns := []string{"localhost:*", "127.0.0.1:*", r.Host}
	if h.cfg.PublicURL != "" {
		if u, err := url.Parse(h.cfg.PublicURL); err == nil && u.Host != "" {
			originPatterns = append(originPatterns, u.Host)
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: originPatterns,
	})
	if err != nil {
		h.logger.Warn("Failed to accept websocket", "err", err)
		return
	}
	defer conn.CloseNow()

	// Enforce 2 MB frame read limit (F23, DESIGN §6)
	conn.SetReadLimit(2 * 1024 * 1024)

	sessionUUID, _ := store.RandomHex(16)
	sessionID := "sess_" + sessionUUID

	machineName := r.Header.Get("X-Machine-Name")
	if machineName == "" {
		machineName = "default"
	}

	sess := &daemonSession{
		sessionID:      sessionID,
		conn:           conn,
		memberID:       mem.ID,
		machineName:    machineName,
		maxConcurrency: config.DefaultMaxConcurrency,
		lastSeen:       time.Now(),
	}

	// Connection Takeover & Multi-device tracking (F17)
	h.mu.Lock()
	if _, ok := h.memberSessions[mem.ID]; !ok {
		h.memberSessions[mem.ID] = make(map[string]*daemonSession)
	}

	// Check if this same machine had an active session; if so, gracefully supersede it
	for prevID, prevSess := range h.memberSessions[mem.ID] {
		if prevSess.machineName == machineName {
			h.logger.Info("Superseding previous connection from same machine",
				"member_id", mem.ID, "machine", machineName, "prev_session", prevID, "new_session", sessionID)
			prevSess.closed = true
			delete(h.memberSessions[mem.ID], prevID)
			delete(h.sessionsByID, prevID)
			_ = prevSess.conn.Close(websocket.StatusPolicyViolation, "superseded by new connection session")
		}
	}

	h.memberSessions[mem.ID][sessionID] = sess
	h.sessionsByID[sessionID] = sess
	h.daemonConns[mem.ID] = sess
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		sess.closed = true
		if userSessions, ok := h.memberSessions[mem.ID]; ok {
			delete(userSessions, sessionID)
			if len(userSessions) == 0 {
				delete(h.memberSessions, mem.ID)
				delete(h.daemonConns, mem.ID)
			} else {
				for _, rem := range userSessions {
					h.daemonConns[mem.ID] = rem
					break
				}
			}
		}
		delete(h.sessionsByID, sessionID)
		remainingActive := len(h.memberSessions[mem.ID])
		h.mu.Unlock()

		// If no more active sessions remain for this member, reconcile in-flight queries and mark offline
		if remainingActive == 0 {
			h.reconcileInFlightQueries(mem.ID)
			_ = h.store.SetMemberOnline(context.Background(), mem.ID, false, "", nil)
		}
	}()

	_ = h.store.SetMemberOnline(r.Context(), mem.ID, true, machineName, nil)

	heartbeatInterval := h.cfg.HeartbeatIntervalSec
	if heartbeatInterval <= 0 {
		heartbeatInterval = 20
	}
	heartbeatTimeout := time.Duration(float64(heartbeatInterval)*2.5) * time.Second // 50s deadline (F18)

	sessCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Frame read loop with heartbeat read deadline (F18)
	for {
		readCtx, cancelRead := context.WithTimeout(sessCtx, heartbeatTimeout)
		_, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				h.logger.Warn("Daemon heartbeat timeout (50s exceeded), terminating connection",
					"member_id", mem.ID, "session_id", sess.sessionID)
				_ = conn.Close(websocket.StatusPolicyViolation, "heartbeat timeout")
			}
			break
		}
		sess.lastSeen = time.Now()

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		// Enforce protocol version check (F32)
		if env.Version != protocol.Version1 {
			h.logger.Warn("Rejecting frame with unsupported protocol version", "version", env.Version, "session_id", sess.sessionID)
			_ = conn.Close(websocket.StatusProtocolError, "unsupported protocol version")
			break
		}

		h.handleDaemonEnvelope(sessCtx, sess, env)
	}
}

// reconcileInFlightQueries reverts dispatched queries to queued status on daemon disconnect (F21).
func (h *HubServer) reconcileInFlightQueries(memberID string) {
	requeued, err := h.store.RequeueInFlightQueries(context.Background(), memberID)
	if err != nil {
		h.logger.Warn("Failed to requeue in-flight queries", "member_id", memberID, "err", err)
		return
	}
	if len(requeued) > 0 {
		h.logger.Info("Reverted in-flight dispatched queries to queued", "member_id", memberID, "requeued_count", len(requeued))
	}
}

func (h *HubServer) handleDaemonEnvelope(ctx context.Context, sess *daemonSession, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeDaemonHello:
		var hello protocol.DaemonHelloPayload
		if err := json.Unmarshal(env.Payload, &hello); err == nil {
			if hello.MaxConcurrency > 0 {
				sess.maxConcurrency = hello.MaxConcurrency
			}
			if hello.MachineName != "" {
				sess.machineName = hello.MachineName
			}
			sess.workspaces = hello.Workspaces

			var wsNames []string
			for _, ws := range hello.Workspaces {
				wsNames = append(wsNames, ws.Name)
			}
			_ = h.store.SetMemberOnline(ctx, sess.memberID, true, sess.machineName, wsNames)

			// Calculate pending queries count for hub_ack (MUST-HAVE)
			queued, _ := h.store.GetQueuedQueriesForMember(ctx, sess.memberID)
			pendingCount := len(queued)

			heartbeatInterval := h.cfg.HeartbeatIntervalSec
			if heartbeatInterval <= 0 {
				heartbeatInterval = 20
			}

			ack := protocol.HubAckPayload{
				SessionID:            sess.sessionID,
				Authenticated:        true,
				HeartbeatIntervalSec: heartbeatInterval,
				PendingQueriesCount:  pendingCount,
			}
			h.sendEnvelope(ctx, sess, protocol.TypeHubAck, ack)
			h.drainOfflineQueue(ctx, sess)
		}

	case protocol.TypeHeartbeatPing:
		pong := protocol.HeartbeatPongPayload{ServerTime: time.Now().UnixMilli()}
		h.sendEnvelope(ctx, sess, protocol.TypeHeartbeatPong, pong)

	case protocol.TypeQueryResponse:
		var resp protocol.QueryResponsePayload
		if err := json.Unmarshal(env.Payload, &resp); err == nil {
			// Anti-hijacking check: verify query target matches this session's memberID (F5)
			existingQ, err := h.store.GetQuery(ctx, resp.QueryID)
			if err != nil {
				h.logger.Warn("Ignoring response for unknown query", "query_id", resp.QueryID)
				return
			}
			if existingQ.TargetMemberID != sess.memberID {
				h.logger.Warn("Spoofed query response rejected: session member does not match query target",
					"query_id", resp.QueryID, "expected_member", existingQ.TargetMemberID, "actual_member", sess.memberID)
				return
			}

			// Normalize status taxonomy: success -> completed (F36)
			status := resp.Status
			if status == "success" || status == "" {
				status = protocol.QueryStatusCompleted
			}

			_ = h.store.UpdateQueryStatus(ctx, resp.QueryID, status, resp.Answer, resp.ToolsUsed, resp.DurationMS, resp.TokenUsage, resp.ErrorMessage)
			h.notifyWaiters(resp.QueryID)

			// Asynchronous Feishu reply if originated from Feishu bot webhook (F40)
			if existingQ.FeishuContext != nil && existingQ.FeishuContext.MessageID != "" && h.feishuHandler != nil {
				updatedQ, err := h.store.GetQuery(ctx, resp.QueryID)
				if err == nil {
					go func(q *protocol.QueryDetailResponse) {
						replyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
						defer cancel()
						_ = h.feishuHandler.OnQueryComplete(replyCtx, q)
					}(updatedQ)
				}
			}
		}
	}
}

func (h *HubServer) sendEnvelope(ctx context.Context, sess *daemonSession, msgType string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
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
		return
	}
	// The caller's ctx is frequently somebody else's lifetime — for query_request it is
	// the asker's HTTP request. coder/websocket closes the whole connection if the write
	// ctx is cancelled before the write is fully acknowledged, and on Windows the payload
	// reaches the peer before the writing goroutine wakes from the IOCP completion, so an
	// asker hanging up at that instant would kill the target daemon's socket. Detach the
	// cancellation and keep only a bounded timeout.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), envelopeWriteTimeout)
	defer cancel()

	sess.wsLock.Lock()
	defer sess.wsLock.Unlock()
	if sess.closed {
		return
	}
	_ = sess.conn.Write(writeCtx, websocket.MessageText, b)
}

func (h *HubServer) drainOfflineQueue(ctx context.Context, sess *daemonSession) {
	// 1. Sweep expired queries first (F19, F41)
	_, _ = h.store.SweepExpiredQueries(ctx)

	queued, err := h.store.GetQueuedQueriesForMember(ctx, sess.memberID)
	if err != nil || len(queued) == 0 {
		return
	}

	limit := sess.maxConcurrency
	if limit <= 0 {
		limit = config.DefaultMaxConcurrency
	}

	count := 0
	now := time.Now().UnixMilli()
	for _, q := range queued {
		if count >= limit {
			break
		}
		// Skip expired items
		if q.TTLExpiresAt > 0 && now > q.TTLExpiresAt {
			continue
		}
		req := protocol.QueryRequestPayload{
			QueryID:         q.QueryID,
			AskerID:         q.AskerID,
			AskerName:       q.AskerName,
			AskerType:       q.Origin,
			Query:           q.Query,
			TargetWorkspace: q.TargetWorkspace,
			TimeoutSeconds:  h.getQueryTimeout(q.QueryID),
			CreatedAt:       q.CreatedAt,
		}
		if req.AskerType == "" {
			req.AskerType = "member"
		}
		h.sendEnvelope(ctx, sess, protocol.TypeQueryRequest, req)
		_ = h.store.UpdateQueryStatus(ctx, q.QueryID, protocol.QueryStatusDispatched, "", nil, 0, protocol.TokenUsage{}, "")
		count++
	}
}

func (h *HubServer) notifyWaiters(queryID string) {
	h.mu.Lock()
	waiters := h.queryWaiters[queryID]
	delete(h.queryWaiters, queryID)
	delete(h.queryTimeouts, queryID)
	h.mu.Unlock()

	if len(waiters) == 0 {
		return
	}
	q, err := h.store.GetQuery(context.Background(), queryID)
	if err != nil {
		return
	}
	for _, ch := range waiters {
		select {
		case ch <- q:
		default:
		}
	}
}

// dispatchFeishuQuery creates and routes an asynchronous query originating from Feishu (F40).
func (h *HubServer) dispatchFeishuQuery(ctx context.Context, targetMemberID string, asker feishu.AskerInfo, queryText string, feishuCtx protocol.FeishuContext) (*feishu.DispatchResult, error) {
	targetMem, err := h.store.GetMember(ctx, targetMemberID)
	if err != nil {
		return nil, fmt.Errorf("target member not found: %w", err)
	}

	queryUUID, _ := store.RandomHex(8)
	queryID := "q_" + queryUUID

	ttlSec := h.cfg.DefaultQueryTTLSec
	if ttlSec <= 0 {
		ttlSec = 86400 // 24 hours default
	}
	ttlExpiresAt := time.Now().Add(time.Duration(ttlSec) * time.Second).UnixMilli()

	q := &protocol.QueryDetailResponse{
		QueryID:          queryID,
		Status:           protocol.QueryStatusQueued,
		AskerID:          asker.OpenID,
		AskerName:        asker.DisplayName(),
		TargetMemberID:   targetMem.ID,
		TargetMemberName: targetMem.Name,
		Query:            queryText,
		TTLExpiresAt:     ttlExpiresAt,
		CreatedAt:        time.Now().UnixMilli(),
		Origin:           "feishu",
		FeishuContext:    &feishuCtx,
	}

	if err := h.store.CreateQuery(ctx, q); err != nil {
		return nil, fmt.Errorf("failed to create query: %w", err)
	}
	h.setQueryTimeout(queryID, 60)

	sess := h.selectSessionForQuery(targetMem.ID, "")
	if sess != nil {
		queryReq := protocol.QueryRequestPayload{
			QueryID:        queryID,
			AskerID:        q.AskerID,
			AskerName:      q.AskerName,
			AskerType:      "feishu",
			Query:          queryText,
			TimeoutSeconds: 60,
			CreatedAt:      q.CreatedAt,
		}
		h.sendEnvelope(ctx, sess, protocol.TypeQueryRequest, queryReq)
		_ = h.store.UpdateQueryStatus(ctx, queryID, protocol.QueryStatusDispatched, "", nil, 0, protocol.TokenUsage{}, "")
		q.Status = protocol.QueryStatusDispatched
	}

	queuePos, _ := h.store.GetQueuePosition(ctx, queryID)
	return &feishu.DispatchResult{
		QueryID:          queryID,
		Status:           q.Status,
		TargetMemberName: targetMem.Name,
		QueuePosition:    queuePos,
	}, nil
}

func (h *HubServer) publicURL(r *http.Request) string {
	if h.cfg.PublicURL != "" {
		return strings.TrimRight(h.cfg.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}

// REST Handlers

func (h *HubServer) handleAuthPair(w http.ResponseWriter, r *http.Request) {
	var req protocol.PairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, "invalid json")
		return
	}
	resp, err := h.store.ConsumeInvite(r.Context(), &req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, err.Error())
		return
	}
	resp.HubURL = h.publicURL(r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateAdmin(r) {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}

	var req protocol.InviteCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, "invalid json")
		return
	}
	resp, err := h.store.CreateInvite(r.Context(), &req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleMembersList(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateAdmin(r) {
		if _, err := h.authenticateMember(r); err != nil {
			writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
			return
		}
	}
	list, err := h.store.ListMembers(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.MemberListResponse{Members: list})
}

func (h *HubServer) handleMemberMe(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mem)
}

func (h *HubServer) handleMemberMeUpdate(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req protocol.MemberUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, "invalid json")
		return
	}
	updated, err := h.store.UpdateMember(r.Context(), mem.ID, &req)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func (h *HubServer) handleMemberGet(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateAdmin(r) {
		if _, err := h.authenticateMember(r); err != nil {
			writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
			return
		}
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/members/")
	id = strings.TrimSpace(id)
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, "missing member id")
		return
	}
	mem, err := h.store.GetMember(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, protocol.ErrCodeNotFound, "member not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mem)
}

func (h *HubServer) handleQuerySubmit(w http.ResponseWriter, r *http.Request) {
	asker, err := h.authenticateMember(r)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:    protocol.ErrCodeUnauthorized,
				Message: "unauthorized",
			},
		})
		return
	}

	// Per-asker rate limiting -> 429 (FOCUS / MUST-HAVES)
	if !h.rateLimiter.Allow(asker.ID) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:    protocol.ErrCodeRateLimited,
				Message: "Query submission rate limit exceeded.",
			},
		})
		return
	}

	var req protocol.QuerySubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:    protocol.ErrCodeInvalidArgument,
				Message: "invalid json payload",
			},
		})
		return
	}

	// F30 Idempotency verification
	if req.IdempotencyKey != "" {
		h.idempotencyMu.Lock()
		rec, exists := h.idempotencyCache[req.IdempotencyKey]
		h.idempotencyMu.Unlock()
		if exists && time.Now().Before(rec.expiresAt) {
			existingQuery, err := h.store.GetQuery(r.Context(), rec.queryID)
			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(existingQuery)
				return
			}
		}
		if existingQuery, err := h.store.GetQueryByIdempotencyKey(r.Context(), req.IdempotencyKey); err == nil && existingQuery != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(existingQuery)
			return
		}
	}

	// Target member resolution
	targetMem, candidates, err := h.store.ResolveTargetMember(r.Context(), req.Target)
	if err != nil {
		if errors.Is(err, store.ErrAmbiguousMatch) {
			// AMBIGUOUS_TARGET 400 with candidates (PROTOCOL §1.3, §3.3.1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
				Error: protocol.ErrorDetail{
					Code:    protocol.ErrCodeAmbiguousTarget,
					Message: fmt.Sprintf("Target '%s' matches multiple members. Please specify exact name.", req.Target),
					Details: map[string]any{
						"candidates": candidates,
					},
				},
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:    protocol.ErrCodeNotFound,
				Message: "target member not found",
			},
		})
		return
	}

	queryUUID, _ := store.RandomHex(8)
	queryID := "q_" + queryUUID

	ttlSec := req.TTLSeconds
	if ttlSec <= 0 {
		ttlSec = h.cfg.DefaultQueryTTLSec
		if ttlSec <= 0 {
			ttlSec = 86400 // 24 hours default
		}
	}
	if h.cfg.MaxQueryTTLSec > 0 && ttlSec > h.cfg.MaxQueryTTLSec {
		ttlSec = h.cfg.MaxQueryTTLSec
	}
	ttlExpiresAt := time.Now().Add(time.Duration(ttlSec) * time.Second).UnixMilli()

	timeoutSec := req.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	if h.cfg.MaxProbeTimeoutSec > 0 && timeoutSec > h.cfg.MaxProbeTimeoutSec {
		timeoutSec = h.cfg.MaxProbeTimeoutSec
	}

	q := &protocol.QueryDetailResponse{
		QueryID:          queryID,
		Status:           protocol.QueryStatusQueued,
		AskerID:          asker.ID,
		AskerName:        asker.Name,
		TargetMemberID:   targetMem.ID,
		TargetMemberName: targetMem.Name,
		TargetWorkspace:  req.TargetWorkspace,
		Query:            req.Query,
		TTLExpiresAt:     ttlExpiresAt,
		CreatedAt:        time.Now().UnixMilli(),
		Origin:           "cli",
	}

	if err := h.store.CreateQuery(r.Context(), q); err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	h.setQueryTimeout(queryID, timeoutSec)

	// Cache idempotency key
	if req.IdempotencyKey != "" {
		h.idempotencyMu.Lock()
		h.idempotencyCache[req.IdempotencyKey] = idempotencyRecord{
			queryID:   queryID,
			expiresAt: time.Now().Add(1 * time.Hour),
		}
		h.idempotencyMu.Unlock()
		_ = h.store.SaveIdempotencyKey(r.Context(), req.IdempotencyKey, queryID, ttlExpiresAt)
	}

	// Check if target member daemon session is online
	sess := h.selectSessionForQuery(targetMem.ID, req.TargetWorkspace)

	shouldWait := req.Wait || r.URL.Query().Get("wait") != ""
	var waitCh chan *protocol.QueryDetailResponse
	if shouldWait {
		waitCh = make(chan *protocol.QueryDetailResponse, 1)
		h.mu.Lock()
		h.queryWaiters[queryID] = append(h.queryWaiters[queryID], waitCh)
		h.mu.Unlock()
	}

	if sess != nil {
		queryReq := protocol.QueryRequestPayload{
			QueryID:         queryID,
			AskerID:         asker.ID,
			AskerName:       asker.Name,
			AskerType:       "member",
			Query:           req.Query,
			TargetWorkspace: req.TargetWorkspace,
			TimeoutSeconds:  timeoutSec,
			CreatedAt:       q.CreatedAt,
		}
		// Once the frame is on the wire the daemon owns the query; the status record must
		// say so even if the asker hangs up right now, or cancelDispatchedQuery (which only
		// acts on dispatched queries) would never tell the daemon to stop.
		dispatchCtx := context.WithoutCancel(r.Context())
		h.sendEnvelope(dispatchCtx, sess, protocol.TypeQueryRequest, queryReq)
		_ = h.store.UpdateQueryStatus(dispatchCtx, queryID, protocol.QueryStatusDispatched, "", nil, 0, protocol.TokenUsage{}, "")
		q.Status = protocol.QueryStatusDispatched
	} else {
		q.QueuePosition, _ = h.store.GetQueuePosition(r.Context(), queryID)
	}

	if shouldWait && waitCh != nil {
		waitTimeout := time.Duration(timeoutSec) * time.Second
		if waitTimeout <= 0 {
			waitTimeout = 60 * time.Second
		}
		if waitTimeout > 60*time.Second {
			waitTimeout = 60 * time.Second
		}
		select {
		case finished := <-waitCh:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(finished)
			return
		case <-time.After(waitTimeout):
			// Timeout reached while waiting
			h.removeWaiter(queryID, waitCh)
		case <-h.stopCh:
			// Server is shutting down; drain in-flight waiter with current status immediately
			h.removeWaiter(queryID, waitCh)
		case <-r.Context().Done():
			remaining := h.removeWaiter(queryID, waitCh)
			if remaining == 0 {
				h.cancelDispatchedQuery(queryID, targetMem.ID, req.TargetWorkspace, "asker_cancelled")
			}
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(q)
}

func (h *HubServer) handleQueryDetail(w http.ResponseWriter, r *http.Request) {
	queryID := strings.TrimPrefix(r.URL.Path, "/api/v1/queries/")
	queryID = strings.TrimSpace(queryID)
	if queryID == "" {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, "missing query id")
		return
	}

	q, err := h.store.GetQuery(r.Context(), queryID)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
			Error: protocol.ErrorDetail{
				Code:    protocol.ErrCodeNotFound,
				Message: "query not found",
			},
		})
		return
	}

	// Authorization verification: only asker, target member, or admin can inspect query detail (F9)
	isAdmin := h.authenticateAdmin(r)
	if !isAdmin {
		caller, err := h.authenticateMember(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
				Error: protocol.ErrorDetail{
					Code:    protocol.ErrCodeUnauthorized,
					Message: "unauthorized",
				},
			})
			return
		}
		if caller.ID != q.AskerID && caller.ID != q.TargetMemberID {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
				Error: protocol.ErrorDetail{
					Code:    protocol.ErrCodeForbidden,
					Message: "forbidden: caller is neither asker nor target",
				},
			})
			return
		}
	}

	// Long-polling (?wait=duration)
	waitParam := r.URL.Query().Get("wait")
	if waitParam != "" {
		var waitDur time.Duration
		if d, err := time.ParseDuration(waitParam); err == nil {
			waitDur = d
		} else if sec, err := strconv.Atoi(waitParam); err == nil {
			waitDur = time.Duration(sec) * time.Second
		} else {
			waitDur = 30 * time.Second
		}
		if waitDur > 60*time.Second {
			waitDur = 60 * time.Second
		}
		if waitDur <= 0 {
			waitDur = 30 * time.Second
		}

		if q.Status == protocol.QueryStatusQueued || q.Status == protocol.QueryStatusDispatched || q.Status == protocol.QueryStatusPending {
			waitCh := make(chan *protocol.QueryDetailResponse, 1)
			h.mu.Lock()
			h.queryWaiters[queryID] = append(h.queryWaiters[queryID], waitCh)
			h.mu.Unlock()

			select {
			case finished := <-waitCh:
				if finished != nil {
					q = finished
				} else if latest, err := h.store.GetQuery(r.Context(), queryID); err == nil {
					q = latest
				}
			case <-time.After(waitDur):
				h.removeWaiter(queryID, waitCh)
				if latest, err := h.store.GetQuery(r.Context(), queryID); err == nil {
					q = latest
				}
			case <-h.stopCh:
				// Server is shutting down; drain in-flight waiter with latest status immediately
				h.removeWaiter(queryID, waitCh)
				if latest, err := h.store.GetQuery(r.Context(), queryID); err == nil {
					q = latest
				}
			case <-r.Context().Done():
				h.removeWaiter(queryID, waitCh)
				return
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(q)
}

func (h *HubServer) handleAuditInbound(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	resp, err := h.store.GetInboundAudit(r.Context(), mem.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleAuditOutbound(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 50
	}
	resp, err := h.store.GetOutboundAudit(r.Context(), mem.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleFeishuBindingGet(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	binding, err := h.store.GetFeishuBinding(r.Context(), mem.ID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, protocol.ErrCodeNotFound, "binding not found")
		return
	}
	webhookURL := fmt.Sprintf("%s/api/v1/feishu/webhook/%s", h.publicURL(r), mem.ID)
	resp := protocol.FeishuBindingResponse{
		Bound:      binding != nil && binding.AppID != "",
		AppID:      binding.AppID,
		WebhookURL: webhookURL,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleFeishuBindingSave(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	var req protocol.FeishuBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, protocol.ErrCodeInvalidArgument, "invalid json")
		return
	}
	if err := h.store.SaveFeishuBinding(r.Context(), mem.ID, &req); err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	webhookURL := fmt.Sprintf("%s/api/v1/feishu/webhook/%s", h.publicURL(r), mem.ID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"webhook_url": webhookURL,
	})
}

func (h *HubServer) handleFeishuBindingDelete(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "unauthorized")
		return
	}
	if err := h.store.DeleteFeishuBinding(r.Context(), mem.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, protocol.ErrCodeInternalError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
