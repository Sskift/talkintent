package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
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
	cfg        *config.HubConfig
	store      store.Store
	webFS      fs.FS
	logger     *slog.Logger
	httpServer *http.Server
	listener   net.Listener

	mu           sync.RWMutex
	daemonConns  map[string]*daemonSession                      // memberID -> session
	queryWaiters map[string][]chan *protocol.QueryDetailResponse // queryID -> []chan

	// 1-hour idempotency cache: key -> queryID
	idempotencyMu    sync.Mutex
	idempotencyCache map[string]idempotencyRecord
}

type idempotencyRecord struct {
	queryID   string
	expiresAt time.Time
}

type daemonSession struct {
	sessionID      string
	conn           *websocket.Conn
	memberID       string
	maxConcurrency int
	wsLock         sync.Mutex
	closed         bool
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

	// 1. Ensure Admin Token is initialized and persisted
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

	h := &HubServer{
		cfg:              cfg,
		store:            st,
		webFS:            webFS,
		logger:           logger,
		daemonConns:      make(map[string]*daemonSession),
		queryWaiters:     make(map[string][]chan *protocol.QueryDetailResponse),
		idempotencyCache: make(map[string]idempotencyRecord),
	}

	mux := http.NewServeMux()
	h.registerRoutes(mux)

	h.httpServer = &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	return h, nil
}

// Addr returns the network address the server is listening on.
func (h *HubServer) Addr() string {
	if h.listener != nil {
		return h.listener.Addr().String()
	}
	return h.cfg.Addr
}

// Start opens the network port and serves requests until context cancellation.
func (h *HubServer) Start(ctx context.Context) error {
	addr := h.cfg.Addr
	if addr == "" {
		addr = ":8080"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}
	h.listener = ln
	h.logger.Info("TalkIntent Hub listening", "addr", ln.Addr().String(), "data_dir", h.cfg.DataDir)

	errCh := make(chan error, 1)
	go func() {
		if err := h.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		return h.Stop(context.Background())
	case err := <-errCh:
		return err
	}
}

// Stop gracefully shuts down the server.
func (h *HubServer) Stop(ctx context.Context) error {
	h.logger.Info("Shutting down Hub server")
	return h.httpServer.Shutdown(ctx)
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
	mux.HandleFunc("POST /api/v1/feishu/webhook/", h.handleFeishuWebhook)

	// Static Web UI
	if h.webFS != nil {
		fileServer := http.FileServer(http.FS(h.webFS))
		mux.Handle("/web/", http.StripPrefix("/web", fileServer))
		mux.HandleFunc("/web", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/", http.StatusPermanentRedirect)
		})
	}
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

// authenticateAdmin verifies that the request carries the admin token.
func (h *HubServer) authenticateAdmin(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.AdminToken)) == 1
}

// handleWebSocket handles incoming client daemon connections with takeover semantics.
func (h *HubServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}

	mem, err := h.store.GetMemberByToken(r.Context(), token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		h.logger.Warn("Failed to accept websocket", "err", err)
		return
	}
	defer conn.CloseNow()

	// Enforce 2 MB frame read limit
	conn.SetReadLimit(2 * 1024 * 1024)

	sessionUUID, _ := store.RandomHex(16)
	sess := &daemonSession{
		sessionID:      sessionUUID,
		conn:           conn,
		memberID:       mem.ID,
		maxConcurrency: config.DefaultMaxConcurrency,
	}

	// Connection Takeover: replace any existing session for this member
	h.mu.Lock()
	if prevSess, exists := h.daemonConns[mem.ID]; exists {
		h.logger.Info("Superseding previous daemon connection", "member_id", mem.ID, "prev_session", prevSess.sessionID, "new_session", sessionUUID)
		prevSess.closed = true
		_ = prevSess.conn.Close(websocket.StatusPolicyViolation, "superseded by new connection session")
	}
	h.daemonConns[mem.ID] = sess
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		curSess := h.daemonConns[mem.ID]
		if curSess == sess {
			delete(h.daemonConns, mem.ID)
		}
		h.mu.Unlock()

		// Revert unacknowledged in-flight dispatched queries back to queued state
		h.reconcileInFlightQueries(mem.ID)
		_ = h.store.SetMemberOnline(context.Background(), mem.ID, false, "", nil)
	}()

	_ = h.store.SetMemberOnline(r.Context(), mem.ID, true, r.Header.Get("X-Machine-Name"), nil)

	// Heartbeat timeout monitoring: 50s deadline (2.5x 20s interval)
	lastSeen := time.Now()
	var lastSeenMu sync.Mutex

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lastSeenMu.Lock()
				elapsed := time.Since(lastSeen)
				lastSeenMu.Unlock()
				if elapsed > 50*time.Second {
					h.logger.Warn("Daemon heartbeat timeout (50s exceeded), closing socket", "member_id", mem.ID)
					_ = conn.Close(websocket.StatusPolicyViolation, "heartbeat timeout")
					return
				}
			}
		}
	}()

	// Read loop
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		lastSeenMu.Lock()
		lastSeen = time.Now()
		lastSeenMu.Unlock()

		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		// Enforce protocol version check
		if env.Version != protocol.Version1 {
			h.logger.Warn("Rejecting frame with unsupported protocol version", "version", env.Version)
			continue
		}

		h.handleDaemonEnvelope(ctx, sess, env)
	}
}

// reconcileInFlightQueries reverts dispatched queries to queued status on daemon disconnect.
func (h *HubServer) reconcileInFlightQueries(memberID string) {
	queued, err := h.store.GetQueuedQueriesForMember(context.Background(), memberID)
	if err != nil {
		return
	}
	for _, q := range queued {
		if q.Status == protocol.QueryStatusDispatched {
			h.logger.Info("Reverting in-flight dispatched query to queued", "query_id", q.QueryID, "member_id", memberID)
			_ = h.store.UpdateQueryStatus(context.Background(), q.QueryID, protocol.QueryStatusQueued, "", nil, 0, protocol.TokenUsage{}, "")
		}
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
			var wsNames []string
			for _, ws := range hello.Workspaces {
				wsNames = append(wsNames, ws.Name)
			}
			_ = h.store.SetMemberOnline(ctx, sess.memberID, true, hello.MachineName, wsNames)

			ack := protocol.HubAckPayload{
				SessionID:            sess.sessionID,
				Authenticated:        true,
				HeartbeatIntervalSec: 20,
				PendingQueriesCount:  0,
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
			// Anti-hijacking check: verify query target matches this session's memberID
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

			// Normalize status taxonomy
			status := resp.Status
			if status == "success" || status == "" {
				status = protocol.QueryStatusCompleted
			}

			_ = h.store.UpdateQueryStatus(ctx, resp.QueryID, status, resp.Answer, resp.ToolsUsed, resp.DurationMS, resp.TokenUsage, resp.ErrorMessage)
			h.notifyWaiters(resp.QueryID)

			// Asynchronous Feishu reply if originated from Feishu bot webhook
			if existingQ.FeishuContext != nil && existingQ.FeishuContext.MessageID != "" {
				go h.dispatchFeishuReply(existingQ.TargetMemberID, existingQ.FeishuContext.MessageID, resp.Answer)
			}
		}
	}
}

func (h *HubServer) dispatchFeishuReply(memberID, messageID, answer string) {
	binding, err := h.store.GetFeishuBinding(context.Background(), memberID)
	if err != nil || binding == nil || binding.AppSecret == "" {
		return
	}
	client := feishu.NewClient(binding.AppID, binding.AppSecret, "")
	if err := client.ReplyMessage(context.Background(), messageID, answer); err != nil {
		h.logger.Warn("Failed to send Feishu reply", "message_id", messageID, "err", err)
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
	sess.wsLock.Lock()
	defer sess.wsLock.Unlock()
	if sess.closed {
		return
	}
	_ = sess.conn.Write(ctx, websocket.MessageText, b)
}

func (h *HubServer) drainOfflineQueue(ctx context.Context, sess *daemonSession) {
	queued, err := h.store.GetQueuedQueriesForMember(ctx, sess.memberID)
	if err != nil || len(queued) == 0 {
		return
	}

	limit := sess.maxConcurrency
	if limit <= 0 {
		limit = config.DefaultMaxConcurrency
	}

	count := 0
	for _, q := range queued {
		if count >= limit {
			break
		}
		req := protocol.QueryRequestPayload{
			QueryID:         q.QueryID,
			AskerID:         q.AskerID,
			AskerName:       q.AskerName,
			Query:           q.Query,
			TargetWorkspace: q.TargetWorkspace,
			TimeoutSeconds:  60,
			CreatedAt:       q.CreatedAt,
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

// REST Handlers

func (h *HubServer) handleAuthPair(w http.ResponseWriter, r *http.Request) {
	var req protocol.PairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	resp, err := h.store.ConsumeInvite(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hubURL := h.cfg.PublicURL
	if hubURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		hubURL = fmt.Sprintf("%s://%s", scheme, r.Host)
	}
	resp.HubURL = hubURL
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	if !h.authenticateAdmin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req protocol.InviteCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	resp, err := h.store.CreateInvite(r.Context(), &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleMembersList(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListMembers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.MemberListResponse{Members: list})
}

func (h *HubServer) handleMemberMe(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mem)
}

func (h *HubServer) handleMemberMeUpdate(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req protocol.MemberUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	updated, err := h.store.UpdateMember(r.Context(), mem.ID, &req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(updated)
}

func (h *HubServer) handleMemberGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/members/")
	if id == "" {
		http.Error(w, "missing member id", http.StatusBadRequest)
		return
	}
	mem, err := h.store.GetMember(r.Context(), id)
	if err != nil {
		http.Error(w, "member not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(mem)
}

func (h *HubServer) handleQuerySubmit(w http.ResponseWriter, r *http.Request) {
	asker, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req protocol.QuerySubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// 1-hour Idempotency verification
	if req.IdempotencyKey != "" {
		h.idempotencyMu.Lock()
		rec, exists := h.idempotencyCache[req.IdempotencyKey]
		if exists && time.Now().Before(rec.expiresAt) {
			h.idempotencyMu.Unlock()
			existingQuery, err := h.store.GetQuery(r.Context(), rec.queryID)
			if err == nil {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(existingQuery)
				return
			}
		}
		h.idempotencyMu.Unlock()
	}

	// Target member resolution
	targetMem, candidates, err := h.store.ResolveTargetMember(r.Context(), req.Target)
	if err != nil {
		if errors.Is(err, store.ErrAmbiguousMatch) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMultipleChoices)
			_ = json.NewEncoder(w).Encode(protocol.TargetResolveResponse{
				Status:     "ambiguous",
				Candidates: candidates,
			})
			return
		}
		http.Error(w, "target member not found", http.StatusNotFound)
		return
	}

	queryUUID, _ := store.RandomHex(8)
	queryID := "q_" + queryUUID

	ttlSec := req.TTLSeconds
	if ttlSec <= 0 {
		ttlSec = 3600 // 1 hour default
	}
	ttlExpiresAt := time.Now().Add(time.Duration(ttlSec) * time.Second).UnixMilli()

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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Cache idempotency key
	if req.IdempotencyKey != "" {
		h.idempotencyMu.Lock()
		h.idempotencyCache[req.IdempotencyKey] = idempotencyRecord{
			queryID:   queryID,
			expiresAt: time.Now().Add(1 * time.Hour),
		}
		h.idempotencyMu.Unlock()
	}

	// Check if target member daemon is online
	h.mu.RLock()
	sess, online := h.daemonConns[targetMem.ID]
	h.mu.RUnlock()

	var waitCh chan *protocol.QueryDetailResponse
	if req.Wait {
		waitCh = make(chan *protocol.QueryDetailResponse, 1)
		h.mu.Lock()
		h.queryWaiters[queryID] = append(h.queryWaiters[queryID], waitCh)
		h.mu.Unlock()
	}

	if online {
		queryReq := protocol.QueryRequestPayload{
			QueryID:         queryID,
			AskerID:         asker.ID,
			AskerName:       asker.Name,
			Query:           req.Query,
			TargetWorkspace: req.TargetWorkspace,
			TimeoutSeconds:  req.TimeoutSeconds,
			CreatedAt:       q.CreatedAt,
		}
		h.sendEnvelope(r.Context(), sess, protocol.TypeQueryRequest, queryReq)
		_ = h.store.UpdateQueryStatus(r.Context(), queryID, protocol.QueryStatusDispatched, "", nil, 0, protocol.TokenUsage{}, "")
		q.Status = protocol.QueryStatusDispatched
	}

	if req.Wait && waitCh != nil {
		waitTimeout := time.Duration(req.TimeoutSeconds) * time.Second
		if waitTimeout <= 0 {
			waitTimeout = 60 * time.Second
		}
		select {
		case finished := <-waitCh:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(finished)
			return
		case <-time.After(waitTimeout):
			// Timeout reached while waiting
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(q)
}

func (h *HubServer) handleQueryDetail(w http.ResponseWriter, r *http.Request) {
	queryID := strings.TrimPrefix(r.URL.Path, "/api/v1/queries/")
	if queryID == "" {
		http.Error(w, "missing query id", http.StatusBadRequest)
		return
	}

	q, err := h.store.GetQuery(r.Context(), queryID)
	if err != nil {
		http.Error(w, "query not found", http.StatusNotFound)
		return
	}

	// Authorization verification: only asker, target member, or admin can inspect query detail
	isAdmin := h.authenticateAdmin(r)
	if !isAdmin {
		caller, err := h.authenticateMember(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if caller.ID != q.AskerID && caller.ID != q.TargetMemberID {
			http.Error(w, "forbidden: caller is neither asker nor target", http.StatusForbidden)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(q)
}

func (h *HubServer) handleAuditInbound(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	resp, err := h.store.GetInboundAudit(r.Context(), mem.ID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleAuditOutbound(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 20
	}
	resp, err := h.store.GetOutboundAudit(r.Context(), mem.ID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *HubServer) handleFeishuBindingGet(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	binding, err := h.store.GetFeishuBinding(r.Context(), mem.ID)
	if err != nil {
		http.Error(w, "binding not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(binding)
}

func (h *HubServer) handleFeishuBindingSave(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req protocol.FeishuBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := h.store.SaveFeishuBinding(r.Context(), mem.ID, &req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *HubServer) handleFeishuBindingDelete(w http.ResponseWriter, r *http.Request) {
	mem, err := h.authenticateMember(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.store.DeleteFeishuBinding(r.Context(), mem.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *HubServer) handleFeishuWebhook(w http.ResponseWriter, r *http.Request) {
	memberID := strings.TrimPrefix(r.URL.Path, "/api/v1/feishu/webhook/")
	if memberID == "" {
		http.Error(w, "missing member id in webhook path", http.StatusBadRequest)
		return
	}

	binding, err := h.store.GetFeishuBinding(r.Context(), memberID)
	if err != nil {
		http.Error(w, "feishu binding not found for member", http.StatusNotFound)
		return
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	// 1. Signature and Timestamp verification
	timestamp := r.Header.Get("X-Lark-Request-Timestamp")
	nonce := r.Header.Get("X-Lark-Request-Nonce")
	signature := r.Header.Get("X-Lark-Signature")

	if binding.EncryptKey != "" && signature != "" {
		if !feishu.VerifyTimestampFreshness(timestamp, 300) {
			http.Error(w, "expired timestamp", http.StatusUnauthorized)
			return
		}
		if !feishu.VerifySignature(timestamp, nonce, binding.EncryptKey, rawBody, signature) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	var parsed map[string]any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return
	}

	// 2. URL Verification Challenge
	if challenge, ok := parsed["challenge"].(string); ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": challenge})
		return
	}

	// 3. Encrypted event payload handling
	if encryptData, ok := parsed["encrypt"].(string); ok && binding.EncryptKey != "" {
		plainBytes, err := feishu.DecryptPayload(encryptData, binding.EncryptKey)
		if err != nil {
			http.Error(w, "payload decryption failed", http.StatusBadRequest)
			return
		}
		_ = json.Unmarshal(plainBytes, &parsed)
	}

	w.WriteHeader(http.StatusOK)
}
