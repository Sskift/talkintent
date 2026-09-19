package store

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Sskift/talkintent/internal/protocol"
)

// Common errors returned by Store.
var (
	ErrNotFound       = errors.New("record not found")
	ErrAmbiguousMatch = errors.New("ambiguous target match")
	ErrInvalidInvite  = errors.New("invalid or expired invite code")
	ErrConflict       = errors.New("resource conflict")
)

// Store defines the persistence and in-memory index contract for the Hub.
type Store interface {
	Close() error
	Compact(ctx context.Context) error

	// Invite & Pairing
	CreateInvite(ctx context.Context, req *protocol.InviteCreateRequest) (*protocol.InviteCreateResponse, error)
	ConsumeInvite(ctx context.Context, req *protocol.PairRequest) (*protocol.PairResponse, error)

	// Members
	GetMember(ctx context.Context, id string) (*protocol.MemberInfo, error)
	GetMemberByToken(ctx context.Context, token string) (*protocol.MemberInfo, error)
	ResolveTargetMember(ctx context.Context, target string) (*protocol.MemberInfo, []protocol.CandidateMember, error)
	ListMembers(ctx context.Context) ([]protocol.MemberInfo, error)
	UpdateMember(ctx context.Context, id string, req *protocol.MemberUpdateRequest) (*protocol.MemberInfo, error)
	SetMemberOnline(ctx context.Context, id string, online bool, machineName string, workspaces []string) error

	// Feishu Bindings
	SaveFeishuBinding(ctx context.Context, memberID string, req *protocol.FeishuBindingRequest) error
	GetFeishuBinding(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error)
	DeleteFeishuBinding(ctx context.Context, memberID string) error

	// Queries & Audit
	CreateQuery(ctx context.Context, q *protocol.QueryDetailResponse) error
	GetQuery(ctx context.Context, id string) (*protocol.QueryDetailResponse, error)
	UpdateQueryStatus(ctx context.Context, id string, status string, answer string, tools []string, durationMS int64, tokens protocol.TokenUsage, errMsg string) error
	GetInboundAudit(ctx context.Context, memberID string, limit, offset int) (*protocol.AuditListResponse, error)
	GetOutboundAudit(ctx context.Context, memberID string, limit, offset int) (*protocol.AuditListResponse, error)
	GetQueuedQueriesForMember(ctx context.Context, memberID string) ([]*protocol.QueryDetailResponse, error)
}

// StoredEvent represents a single line in events.jsonl.
type StoredEvent struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// HashToken calculates SHA-256 hex digest of a token for secure storage at rest.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// HashTokenWithSalt computes an HMAC-SHA256 hex digest with the provided salt.
func HashTokenWithSalt(token string, salt []byte) string {
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(mac.Sum(nil))
}

// RandomHex generates a cryptographically secure random hex string.
func RandomHex(bytesLen int) (string, error) {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// jsonlStore is the standard pure-Go append-only implementation of Store.
type jsonlStore struct {
	mu         sync.RWMutex
	dataDir    string
	eventsFile *os.File
	masterKey  []byte
	salt       []byte

	// In-memory indexes
	membersByID        map[string]*protocol.MemberInfo
	membersByTokenHash map[string]string // tokenHash -> memberID
	invitesByCodeHash  map[string]*storedInvite
	queriesByID        map[string]*protocol.QueryDetailResponse
	feishuBindings     map[string]*protocol.FeishuBindingRequest
	inboundAudit       map[string][]string // memberID -> []queryID
	outboundAudit      map[string][]string // memberID -> []queryID
}

type storedInvite struct {
	CodeHash   string   `json:"code_hash"`
	TargetName string   `json:"target_name"`
	Aliases    []string `json:"aliases"`
	ExpiresAt  int64    `json:"expires_at"`
	Consumed   bool     `json:"consumed"`
}

// NewJSONLStore initializes or opens an append-only store in the given data directory.
func NewJSONLStore(dataDir string) (Store, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create data dir %q: %w", dataDir, err)
	}

	// 1. Initialize or load master encryption key (AES-256)
	masterKeyPath := filepath.Join(dataDir, "master.key")
	masterKey, err := os.ReadFile(masterKeyPath)
	if err != nil || len(masterKey) != 32 {
		masterKey = make([]byte, 32)
		if _, err := rand.Read(masterKey); err != nil {
			return nil, fmt.Errorf("failed to generate master key: %w", err)
		}
		if err := os.WriteFile(masterKeyPath, masterKey, 0600); err != nil {
			return nil, fmt.Errorf("failed to save master key: %w", err)
		}
	}

	// 2. Initialize or load invite salt (HMAC-SHA256)
	saltPath := filepath.Join(dataDir, "salt")
	salt, err := os.ReadFile(saltPath)
	if err != nil || len(salt) < 16 {
		salt = make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, fmt.Errorf("failed to generate salt: %w", err)
		}
		if err := os.WriteFile(saltPath, salt, 0600); err != nil {
			return nil, fmt.Errorf("failed to save salt: %w", err)
		}
	}

	eventsPath := filepath.Join(dataDir, "events.jsonl")
	file, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open events file %q: %w", eventsPath, err)
	}

	s := &jsonlStore{
		dataDir:            dataDir,
		eventsFile:         file,
		masterKey:          masterKey,
		salt:               salt,
		membersByID:        make(map[string]*protocol.MemberInfo),
		membersByTokenHash: make(map[string]string),
		invitesByCodeHash:  make(map[string]*storedInvite),
		queriesByID:        make(map[string]*protocol.QueryDetailResponse),
		feishuBindings:     make(map[string]*protocol.FeishuBindingRequest),
		inboundAudit:       make(map[string][]string),
		outboundAudit:      make(map[string][]string),
	}

	if err := s.replayEvents(eventsPath); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to replay events: %w", err)
	}

	return s, nil
}

func (s *jsonlStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventsFile != nil {
		return s.eventsFile.Close()
	}
	return nil
}

func (s *jsonlStore) appendEventLocked(eventType string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	ev := StoredEvent{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Data:      b,
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	_, err = s.eventsFile.Write(line)
	return err
}

func (s *jsonlStore) replayEvents(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Allow lines up to 2MB for query detail records
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var ev StoredEvent
		if err := json.Unmarshal([]byte(text), &ev); err != nil {
			// Skip truncated or corrupted trailing line gracefully
			continue
		}
		s.applyEvent(ev)
	}
	return scanner.Err()
}

func (s *jsonlStore) encryptCredentials(plain []byte) (string, error) {
	block, err := aes.NewCipher(s.masterKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *jsonlStore) decryptCredentials(encoded string) ([]byte, error) {
	cipherData, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(s.masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(cipherData) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ciphertext := cipherData[:nonceSize], cipherData[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

func (s *jsonlStore) applyEvent(ev StoredEvent) {
	switch ev.Type {
	case "invite_created":
		var inv storedInvite
		if err := json.Unmarshal(ev.Data, &inv); err == nil {
			s.invitesByCodeHash[inv.CodeHash] = &inv
		}
	case "invite_consumed":
		var payload struct {
			CodeHash string `json:"code_hash"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			if inv, ok := s.invitesByCodeHash[payload.CodeHash]; ok {
				inv.Consumed = true
			}
		}
	case "member_created":
		var m protocol.MemberInfo
		if err := json.Unmarshal(ev.Data, &m); err == nil {
			s.membersByID[m.ID] = &m
		}
	case "member_token_indexed":
		var payload struct {
			TokenHash string `json:"token_hash"`
			MemberID  string `json:"member_id"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			s.membersByTokenHash[payload.TokenHash] = payload.MemberID
		}
	case "member_updated":
		var payload struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Aliases []string `json:"aliases"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			if m, ok := s.membersByID[payload.ID]; ok {
				if payload.Name != "" {
					m.Name = payload.Name
				}
				m.Aliases = payload.Aliases
			}
		}
	case "member_presence":
		var payload struct {
			ID          string   `json:"id"`
			Online      bool     `json:"online"`
			MachineName string   `json:"machine_name"`
			Workspaces  []string `json:"workspaces"`
			LastSeenAt  int64    `json:"last_seen_at"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			if m, ok := s.membersByID[payload.ID]; ok {
				m.Online = payload.Online
				m.MachineName = payload.MachineName
				m.Workspaces = payload.Workspaces
				m.LastSeenAt = payload.LastSeenAt
			}
		}
	case "feishu_binding_saved":
		var payload struct {
			MemberID   string                        `json:"member_id"`
			Binding    protocol.FeishuBindingRequest `json:"binding,omitempty"`
			Ciphertext string                        `json:"ciphertext,omitempty"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			var binding protocol.FeishuBindingRequest
			if payload.Ciphertext != "" {
				if plain, err := s.decryptCredentials(payload.Ciphertext); err == nil {
					_ = json.Unmarshal(plain, &binding)
				}
			} else {
				binding = payload.Binding
			}
			s.feishuBindings[payload.MemberID] = &binding
			if m, ok := s.membersByID[payload.MemberID]; ok {
				m.HasFeishuBot = true
			}
		}
	case "feishu_binding_deleted":
		var payload struct {
			MemberID string `json:"member_id"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			delete(s.feishuBindings, payload.MemberID)
			if m, ok := s.membersByID[payload.MemberID]; ok {
				m.HasFeishuBot = false
			}
		}
	case "query_created":
		var q protocol.QueryDetailResponse
		if err := json.Unmarshal(ev.Data, &q); err == nil {
			s.queriesByID[q.QueryID] = &q
			s.inboundAudit[q.TargetMemberID] = append(s.inboundAudit[q.TargetMemberID], q.QueryID)
			s.outboundAudit[q.AskerID] = append(s.outboundAudit[q.AskerID], q.QueryID)
		}
	case "query_updated":
		var payload struct {
			ID          string              `json:"id"`
			Status      string              `json:"status"`
			Answer      string              `json:"answer"`
			ToolsUsed   []string            `json:"tools_used"`
			DurationMS  int64               `json:"duration_ms"`
			TokenUsage  protocol.TokenUsage `json:"token_usage"`
			CompletedAt int64               `json:"completed_at"`
			ErrorMsg    string              `json:"error_msg"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err == nil {
			if q, ok := s.queriesByID[payload.ID]; ok {
				q.Status = payload.Status
				q.Answer = payload.Answer
				q.ToolsUsed = payload.ToolsUsed
				q.DurationMS = payload.DurationMS
				q.TokenUsage = payload.TokenUsage
				q.CompletedAt = payload.CompletedAt
				q.ErrorMessage = payload.ErrorMsg
			}
		}
	}
}

// Compact creates an atomic snapshot of current state and replaces events.jsonl.
func (s *jsonlStore) Compact(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tmpPath := filepath.Join(s.dataDir, "events.jsonl.tmp")
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open compact temp file: %w", err)
	}

	writeEvent := func(eventType string, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		ev := StoredEvent{
			Type:      eventType,
			Timestamp: time.Now().UnixMilli(),
			Data:      b,
		}
		line, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		_, err = f.Write(line)
		return err
	}

	// 1. Members
	for _, m := range s.membersByID {
		if err := writeEvent("member_created", m); err != nil {
			f.Close()
			return err
		}
	}

	// 2. Token mappings
	for th, mid := range s.membersByTokenHash {
		payload := struct {
			TokenHash string `json:"token_hash"`
			MemberID  string `json:"member_id"`
		}{
			TokenHash: th,
			MemberID:  mid,
		}
		if err := writeEvent("member_token_indexed", payload); err != nil {
			f.Close()
			return err
		}
	}

	// 3. Active unconsumed invites
	now := time.Now().UnixMilli()
	for _, inv := range s.invitesByCodeHash {
		if !inv.Consumed && inv.ExpiresAt > now {
			if err := writeEvent("invite_created", inv); err != nil {
				f.Close()
				return err
			}
		}
	}

	// 4. Feishu bindings (encrypted)
	for mid, binding := range s.feishuBindings {
		raw, err := json.Marshal(binding)
		if err == nil {
			cipherText, err := s.encryptCredentials(raw)
			if err == nil {
				payload := struct {
					MemberID   string `json:"member_id"`
					Ciphertext string `json:"ciphertext"`
				}{
					MemberID:   mid,
					Ciphertext: cipherText,
				}
				if err := writeEvent("feishu_binding_saved", payload); err != nil {
					f.Close()
					return err
				}
			}
		}
	}

	// 5. Queries
	for _, q := range s.queriesByID {
		if err := writeEvent("query_created", q); err != nil {
			f.Close()
			return err
		}
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// Close existing events file
	if s.eventsFile != nil {
		_ = s.eventsFile.Close()
	}

	// Atomically replace
	eventsPath := filepath.Join(s.dataDir, "events.jsonl")
	if err := os.Rename(tmpPath, eventsPath); err != nil {
		return fmt.Errorf("failed to replace events.jsonl: %w", err)
	}

	reopened, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("failed to reopen events file: %w", err)
	}
	s.eventsFile = reopened
	return nil
}

// CreateInvite registers a new invite code with salted HMAC hashing.
func (s *jsonlStore) CreateInvite(ctx context.Context, req *protocol.InviteCreateRequest) (*protocol.InviteCreateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	codeRaw, err := RandomHex(8)
	if err != nil {
		return nil, err
	}
	code := fmt.Sprintf("INV-%s-%s", strings.ToUpper(codeRaw[:8]), strings.ToUpper(codeRaw[8:]))
	codeHash := HashTokenWithSalt(code, s.salt)

	hours := req.ExpiresInHours
	if hours <= 0 {
		hours = 48
	}
	expiresAt := time.Now().Add(time.Duration(hours) * time.Hour).UnixMilli()

	inv := &storedInvite{
		CodeHash:   codeHash,
		TargetName: req.TargetName,
		Aliases:    req.Aliases,
		ExpiresAt:  expiresAt,
		Consumed:   false,
	}

	if err := s.appendEventLocked("invite_created", inv); err != nil {
		return nil, err
	}
	s.invitesByCodeHash[codeHash] = inv

	return &protocol.InviteCreateResponse{
		Code:       code,
		ExpiresAt:  expiresAt,
		TargetName: req.TargetName,
	}, nil
}

// ConsumeInvite pairs an invite code with a client machine and returns a permanent token.
func (s *jsonlStore) ConsumeInvite(ctx context.Context, req *protocol.PairRequest) (*protocol.PairResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	code := strings.TrimSpace(req.InviteCode)
	codeHash := HashTokenWithSalt(code, s.salt)
	inv, ok := s.invitesByCodeHash[codeHash]
	if !ok {
		// Fallback check against unsalted hash for backward compatibility
		legacyHash := HashToken(code)
		inv, ok = s.invitesByCodeHash[legacyHash]
		if !ok {
			return nil, ErrInvalidInvite
		}
		codeHash = legacyHash
	}

	if inv.Consumed || time.Now().UnixMilli() > inv.ExpiresAt {
		return nil, ErrInvalidInvite
	}

	memberIDRaw, err := RandomHex(6)
	if err != nil {
		return nil, err
	}
	memberID := "mem_" + memberIDRaw

	tokenRaw, err := RandomHex(16)
	if err != nil {
		return nil, err
	}
	token := "ti_mem_" + tokenRaw
	tokenHash := HashToken(token)

	// Record consumption
	consumePayload := struct {
		CodeHash string `json:"code_hash"`
	}{CodeHash: codeHash}
	if err := s.appendEventLocked("invite_consumed", consumePayload); err != nil {
		return nil, err
	}
	inv.Consumed = true

	// Record new member
	m := &protocol.MemberInfo{
		ID:          memberID,
		Name:        inv.TargetName,
		Aliases:     inv.Aliases,
		Online:      false,
		LastSeenAt:  time.Now().UnixMilli(),
		MachineName: req.MachineName,
	}
	if err := s.appendEventLocked("member_created", m); err != nil {
		return nil, err
	}
	s.membersByID[memberID] = m

	// Record token mapping
	tokenPayload := struct {
		TokenHash string `json:"token_hash"`
		MemberID  string `json:"member_id"`
	}{
		TokenHash: tokenHash,
		MemberID:  memberID,
	}
	if err := s.appendEventLocked("member_token_indexed", tokenPayload); err != nil {
		return nil, err
	}
	s.membersByTokenHash[tokenHash] = memberID

	return &protocol.PairResponse{
		MemberID:   memberID,
		MemberName: inv.TargetName,
		Token:      token,
	}, nil
}

// GetMember retrieves member by ID.
func (s *jsonlStore) GetMember(ctx context.Context, id string) (*protocol.MemberInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.membersByID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

// GetMemberByToken resolves a member from a Bearer token hash.
func (s *jsonlStore) GetMemberByToken(ctx context.Context, token string) (*protocol.MemberInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	th := HashToken(token)
	mid, ok := s.membersByTokenHash[th]
	if !ok {
		return nil, ErrNotFound
	}
	m, ok := s.membersByID[mid]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *m
	return &cp, nil
}

// ResolveTargetMember looks up a member by exact name, alias, ID, or natural language query extraction.
func (s *jsonlStore) ResolveTargetMember(ctx context.Context, target string) (*protocol.MemberInfo, []protocol.CandidateMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return nil, nil, ErrNotFound
	}

	// 1. Exact match by Name, ID, or Alias
	var candidates []protocol.CandidateMember
	var exactMatch *protocol.MemberInfo

	for _, m := range s.membersByID {
		if strings.EqualFold(m.Name, trimmed) || m.ID == trimmed {
			exactMatch = m
			break
		}
		for _, alias := range m.Aliases {
			if strings.EqualFold(alias, trimmed) {
				candidates = append(candidates, protocol.CandidateMember{
					ID:           m.ID,
					Name:         m.Name,
					MatchedAlias: alias,
				})
			}
		}
	}

	if exactMatch != nil {
		cp := *exactMatch
		return &cp, nil, nil
	}

	if len(candidates) == 1 {
		m := s.membersByID[candidates[0].ID]
		cp := *m
		return &cp, nil, nil
	}

	if len(candidates) > 1 {
		return nil, candidates, ErrAmbiguousMatch
	}

	// 2. Substring match: target is a substring of member Name
	for _, m := range s.membersByID {
		if strings.Contains(strings.ToLower(m.Name), strings.ToLower(trimmed)) {
			candidates = append(candidates, protocol.CandidateMember{
				ID:   m.ID,
				Name: m.Name,
			})
		}
	}

	if len(candidates) == 1 {
		m := s.membersByID[candidates[0].ID]
		cp := *m
		return &cp, nil, nil
	}
	if len(candidates) > 1 {
		return nil, candidates, ErrAmbiguousMatch
	}

	// 3. Reverse substring match: target is a natural language question containing member Name or Alias
	// e.g. target = "问一下张三现在登录模块重构得怎么样了"
	var reverseCandidates []protocol.CandidateMember
	for _, m := range s.membersByID {
		matched := false
		if strings.Contains(trimmed, m.Name) {
			reverseCandidates = append(reverseCandidates, protocol.CandidateMember{
				ID:   m.ID,
				Name: m.Name,
			})
			matched = true
		}
		if !matched {
			for _, alias := range m.Aliases {
				if len(alias) >= 2 && strings.Contains(strings.ToLower(trimmed), strings.ToLower(alias)) {
					reverseCandidates = append(reverseCandidates, protocol.CandidateMember{
						ID:           m.ID,
						Name:         m.Name,
						MatchedAlias: alias,
					})
					break
				}
			}
		}
	}

	if len(reverseCandidates) == 1 {
		m := s.membersByID[reverseCandidates[0].ID]
		cp := *m
		return &cp, nil, nil
	}
	if len(reverseCandidates) > 1 {
		return nil, reverseCandidates, ErrAmbiguousMatch
	}

	return nil, nil, ErrNotFound
}

// ListMembers returns all registered members.
func (s *jsonlStore) ListMembers(ctx context.Context) ([]protocol.MemberInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]protocol.MemberInfo, 0, len(s.membersByID))
	for _, m := range s.membersByID {
		res = append(res, *m)
	}
	return res, nil
}

// UpdateMember modifies a member's profile.
func (s *jsonlStore) UpdateMember(ctx context.Context, id string, req *protocol.MemberUpdateRequest) (*protocol.MemberInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.membersByID[id]
	if !ok {
		return nil, ErrNotFound
	}

	payload := struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Aliases []string `json:"aliases"`
	}{
		ID:      id,
		Name:    req.Name,
		Aliases: req.Aliases,
	}

	if err := s.appendEventLocked("member_updated", payload); err != nil {
		return nil, err
	}

	if req.Name != "" {
		m.Name = req.Name
	}
	m.Aliases = req.Aliases
	cp := *m
	return &cp, nil
}

// SetMemberOnline updates a member's presence, machine name, and workspaces.
func (s *jsonlStore) SetMemberOnline(ctx context.Context, id string, online bool, machineName string, workspaces []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.membersByID[id]
	if !ok {
		return ErrNotFound
	}

	now := time.Now().UnixMilli()
	payload := struct {
		ID          string   `json:"id"`
		Online      bool     `json:"online"`
		MachineName string   `json:"machine_name"`
		Workspaces  []string `json:"workspaces"`
		LastSeenAt  int64    `json:"last_seen_at"`
	}{
		ID:          id,
		Online:      online,
		MachineName: machineName,
		Workspaces:  workspaces,
		LastSeenAt:  now,
	}

	if err := s.appendEventLocked("member_presence", payload); err != nil {
		return err
	}

	m.Online = online
	if machineName != "" {
		m.MachineName = machineName
	}
	if workspaces != nil {
		m.Workspaces = workspaces
	}
	m.LastSeenAt = now
	return nil
}

// SaveFeishuBinding saves a member's Feishu bot credentials, encrypting sensitive fields at rest using AES-GCM.
func (s *jsonlStore) SaveFeishuBinding(ctx context.Context, memberID string, req *protocol.FeishuBindingRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	cipherText, err := s.encryptCredentials(raw)
	if err != nil {
		return fmt.Errorf("failed to encrypt feishu credentials: %w", err)
	}

	payload := struct {
		MemberID   string `json:"member_id"`
		Ciphertext string `json:"ciphertext"`
	}{
		MemberID:   memberID,
		Ciphertext: cipherText,
	}

	if err := s.appendEventLocked("feishu_binding_saved", payload); err != nil {
		return err
	}

	s.feishuBindings[memberID] = req
	if m, ok := s.membersByID[memberID]; ok {
		m.HasFeishuBot = true
	}
	return nil
}

// GetFeishuBinding retrieves decrypted Feishu bot credentials for a member.
func (s *jsonlStore) GetFeishuBinding(ctx context.Context, memberID string) (*protocol.FeishuBindingRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.feishuBindings[memberID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *b
	return &cp, nil
}

// DeleteFeishuBinding clears a member's Feishu bot credentials.
func (s *jsonlStore) DeleteFeishuBinding(ctx context.Context, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload := struct {
		MemberID string `json:"member_id"`
	}{
		MemberID: memberID,
	}

	if err := s.appendEventLocked("feishu_binding_deleted", payload); err != nil {
		return err
	}

	delete(s.feishuBindings, memberID)
	if m, ok := s.membersByID[memberID]; ok {
		m.HasFeishuBot = false
	}
	return nil
}

// CreateQuery persists a new query record and indexes it.
func (s *jsonlStore) CreateQuery(ctx context.Context, q *protocol.QueryDetailResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.appendEventLocked("query_created", q); err != nil {
		return err
	}

	cp := *q
	s.queriesByID[q.QueryID] = &cp
	s.inboundAudit[q.TargetMemberID] = append(s.inboundAudit[q.TargetMemberID], q.QueryID)
	s.outboundAudit[q.AskerID] = append(s.outboundAudit[q.AskerID], q.QueryID)
	return nil
}

// GetQuery retrieves a query detail by ID.
func (s *jsonlStore) GetQuery(ctx context.Context, id string) (*protocol.QueryDetailResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.queriesByID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *q
	return &cp, nil
}

// UpdateQueryStatus updates the status, answer, and execution metrics of a query.
func (s *jsonlStore) UpdateQueryStatus(ctx context.Context, id string, status string, answer string, tools []string, durationMS int64, tokens protocol.TokenUsage, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	q, ok := s.queriesByID[id]
	if !ok {
		return ErrNotFound
	}

	now := time.Now().UnixMilli()
	payload := struct {
		ID          string              `json:"id"`
		Status      string              `json:"status"`
		Answer      string              `json:"answer"`
		ToolsUsed   []string            `json:"tools_used"`
		DurationMS  int64               `json:"duration_ms"`
		TokenUsage  protocol.TokenUsage `json:"token_usage"`
		CompletedAt int64               `json:"completed_at"`
		ErrorMsg    string              `json:"error_msg"`
	}{
		ID:          id,
		Status:      status,
		Answer:      answer,
		ToolsUsed:   tools,
		DurationMS:  durationMS,
		TokenUsage:  tokens,
		CompletedAt: now,
		ErrorMsg:    errMsg,
	}

	if err := s.appendEventLocked("query_updated", payload); err != nil {
		return err
	}

	q.Status = status
	q.Answer = answer
	q.ToolsUsed = tools
	q.DurationMS = durationMS
	q.TokenUsage = tokens
	q.CompletedAt = now
	q.ErrorMessage = errMsg
	return nil
}

// GetInboundAudit returns queries targeting memberID with pagination.
func (s *jsonlStore) GetInboundAudit(ctx context.Context, memberID string, limit, offset int) (*protocol.AuditListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	qids := s.inboundAudit[memberID]
	total := len(qids)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}

	selected := qids[offset:end]
	entries := make([]protocol.AuditLogEntry, 0, len(selected))
	for _, id := range selected {
		if q, ok := s.queriesByID[id]; ok {
			entries = append(entries, protocol.AuditLogEntry{
				QueryID:          q.QueryID,
				Timestamp:        q.CreatedAt,
				AskerID:          q.AskerID,
				AskerName:        q.AskerName,
				TargetMemberID:   q.TargetMemberID,
				TargetMemberName: q.TargetMemberName,
				Query:            q.Query,
				Status:           q.Status,
				Answer:           q.Answer,
				ToolsUsed:        q.ToolsUsed,
				DurationMS:       q.DurationMS,
			})
		}
	}

	return &protocol.AuditListResponse{Total: total, Entries: entries}, nil
}

// GetOutboundAudit returns queries created by memberID with pagination.
func (s *jsonlStore) GetOutboundAudit(ctx context.Context, memberID string, limit, offset int) (*protocol.AuditListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	qids := s.outboundAudit[memberID]
	total := len(qids)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}

	selected := qids[offset:end]
	entries := make([]protocol.AuditLogEntry, 0, len(selected))
	for _, id := range selected {
		if q, ok := s.queriesByID[id]; ok {
			entries = append(entries, protocol.AuditLogEntry{
				QueryID:          q.QueryID,
				Timestamp:        q.CreatedAt,
				AskerID:          q.AskerID,
				AskerName:        q.AskerName,
				TargetMemberID:   q.TargetMemberID,
				TargetMemberName: q.TargetMemberName,
				Query:            q.Query,
				Status:           q.Status,
				Answer:           q.Answer,
				ToolsUsed:        q.ToolsUsed,
				DurationMS:       q.DurationMS,
			})
		}
	}

	return &protocol.AuditListResponse{Total: total, Entries: entries}, nil
}

// GetQueuedQueriesForMember returns pending or queued queries waiting for member's daemon,
// automatically evaluating TTL and expiring stale queries.
func (s *jsonlStore) GetQueuedQueriesForMember(ctx context.Context, memberID string) ([]*protocol.QueryDetailResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	var res []*protocol.QueryDetailResponse

	for _, qid := range s.inboundAudit[memberID] {
		if q, ok := s.queriesByID[qid]; ok {
			if q.Status == protocol.QueryStatusQueued || q.Status == protocol.QueryStatusDispatched {
				// Evaluate TTL expiration
				if q.TTLExpiresAt > 0 && now > q.TTLExpiresAt {
					q.Status = protocol.QueryStatusExpired
					q.ErrorMessage = "query expired in offline queue"
					_ = s.appendEventLocked("query_updated", struct {
						ID          string              `json:"id"`
						Status      string              `json:"status"`
						Answer      string              `json:"answer"`
						ToolsUsed   []string            `json:"tools_used"`
						DurationMS  int64               `json:"duration_ms"`
						TokenUsage  protocol.TokenUsage `json:"token_usage"`
						CompletedAt int64               `json:"completed_at"`
						ErrorMsg    string              `json:"error_msg"`
					}{
						ID:          q.QueryID,
						Status:      protocol.QueryStatusExpired,
						CompletedAt: now,
						ErrorMsg:    q.ErrorMessage,
					})
					continue
				}

				cp := *q
				res = append(res, &cp)
			}
		}
	}
	return res, nil
}
