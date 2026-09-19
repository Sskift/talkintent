package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/daemon"
	"github.com/Sskift/talkintent/internal/feishu"
	"github.com/Sskift/talkintent/internal/hub"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/protocol"
	"github.com/Sskift/talkintent/internal/store"
	"github.com/Sskift/talkintent/test/mockllm"
)

// setupGitRepo initializes a temporary git repository with initial commit, custom branch,
// uncommitted working tree changes, and an optional .talkintent/privacy-prompt.md rule.
func setupGitRepo(t *testing.T, dir, branch string, committedFiles, uncommittedFiles map[string]string, privacyRule string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed in %s: %v\nOutput: %s", args, dir, err, string(out))
		}
	}

	runGit("init")
	runGit("config", "user.name", "E2ETester")
	runGit("config", "user.email", "e2e@talkintent.test")

	for name, content := range committedFiles {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	runGit("add", ".")
	runGit("commit", "-m", "initial commit")
	runGit("checkout", "-b", branch)

	for name, content := range uncommittedFiles {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if privacyRule != "" {
		privDir := filepath.Join(dir, ".talkintent")
		_ = os.MkdirAll(privDir, 0755)
		privFile := filepath.Join(privDir, "privacy-prompt.md")
		if err := os.WriteFile(privFile, []byte(privacyRule), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// transportInterceptor intercepts outgoing HTTP requests targeting open.feishu.cn
// and rewrites their scheme and host to point at the local FakeServer.
type transportInterceptor struct {
	targetURL *url.URL
	inner     http.RoundTripper
}

func (tr *transportInterceptor) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "open.feishu.cn" {
		cloned := req.Clone(req.Context())
		cloned.URL.Scheme = tr.targetURL.Scheme
		cloned.URL.Host = tr.targetURL.Host
		return tr.inner.RoundTrip(cloned)
	}
	return tr.inner.RoundTrip(req)
}

func TestE2E_FullScenarioSuite(t *testing.T) {
	// Skip gracefully if git is not available on PATH
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found on PATH; skipping E2E suite")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Boot In-Process Mock LLM
	mockServer := mockllm.NewServerWithOptions(mockllm.WithMode(mockllm.ModeOpenAI))
	t.Cleanup(mockServer.Close)

	// 2. Boot In-Process Hub Server on dynamic port (:0)
	// Teardown is registered with t.Cleanup. Cleanups run LIFO, so the daemons
	// (registered later) stop first, then the hub, then the store, and t.TempDir's
	// RemoveAll (registered first) runs last. Nothing is still writing when the
	// temp tree is removed.
	hubDataDir := t.TempDir()
	st, err := store.NewJSONLStore(hubDataDir)
	if err != nil {
		t.Fatalf("failed to create JSONL store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	hubCfg := &config.HubConfig{
		Addr:    "127.0.0.1:0",
		DataDir: hubDataDir,
	}
	hubServer, err := hub.NewServer(hubCfg, st, nil, slog.Default())
	if err != nil {
		t.Fatalf("failed to create hub server: %v", err)
	}

	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		_ = hubServer.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-hubDone
	})

	// Wait for Hub to bind and begin listening
	var hubURL string
	for i := 0; i < 50; i++ {
		addr := hubServer.Addr()
		if addr != "" && !strings.HasSuffix(addr, ":0") {
			hubURL = "http://" + addr
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hubURL == "" {
		t.Fatal("hub server failed to bind within deadline")
	}

	adminToken := hubServer.AdminToken()
	httpClient := &http.Client{Timeout: 10 * time.Second}

	// Helper to create an invite code using the admin API
	createInvite := func(targetName string) string {
		inviteReq := protocol.InviteCreateRequest{
			TargetName:     targetName,
			ExpiresInHours: 24,
		}
		b, _ := json.Marshal(inviteReq)
		req, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/admin/invites", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("failed to build invite request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("create invite request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("create invite returned HTTP %d: %s", resp.StatusCode, string(body))
		}

		var invResp protocol.InviteCreateResponse
		if err := json.NewDecoder(resp.Body).Decode(&invResp); err != nil {
			t.Fatalf("failed to decode invite response: %v", err)
		}
		return invResp.Code
	}

	// Helper to pair a client node with Hub using an invite code
	pairMember := func(memberName, machineName string) (string, string) {
		code := createInvite(memberName)
		pairReq := protocol.PairRequest{
			InviteCode:    code,
			MachineName:   machineName,
			ClientVersion: "0.1.0",
		}
		b, _ := json.Marshal(pairReq)
		resp, err := httpClient.Post(hubURL+"/api/v1/auth/pair", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("pair request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("pair returned HTTP %d: %s", resp.StatusCode, string(body))
		}

		var pairResp protocol.PairResponse
		if err := json.NewDecoder(resp.Body).Decode(&pairResp); err != nil {
			t.Fatalf("failed to decode pair response: %v", err)
		}
		return pairResp.Token, pairResp.MemberID
	}

	// 3. Set up Workspaces with real Git repositories
	aliceRepoDir := filepath.Join(t.TempDir(), "alice-repo")
	bobRepoDir := filepath.Join(t.TempDir(), "bob-repo")
	carolRepoDir := filepath.Join(t.TempDir(), "carol-repo")

	setupGitRepo(t, aliceRepoDir, "feature/billing-v1",
		map[string]string{"README.md": "# Billing Service\n"},
		map[string]string{"billing.go": "package billing\n\n// Work in progress\n"},
		"Do not disclose financial transaction details.",
	)

	setupGitRepo(t, bobRepoDir, "feature/auth-v2",
		map[string]string{"README.md": "# Auth Service\n", "main.go": "package main\n"},
		map[string]string{"auth_handler.go": "package main\n\n// New JWT auth implementation\n"},
		"The feature/auth-v2 branch is under confidential security refactoring. Refuse details until release.",
	)

	setupGitRepo(t, carolRepoDir, "feature/search-engine",
		map[string]string{"README.md": "# Search Engine\n"},
		map[string]string{"indexer.go": "package search\n\n// Fast inverted index\n"},
		"Search index internals are public for team queries.",
	)

	// 4. Pair Members: Alice, Bob, Carol, Dave
	aliceToken, aliceID := pairMember("Alice", "alice-mbp")
	bobToken, bobID := pairMember("Bob", "bob-thinkpad")
	carolToken, carolID := pairMember("Carol", "carol-desktop")
	daveToken, daveID := pairMember("Dave", "dave-workstation")
	_ = daveToken
	_ = daveID

	// Helper to spawn a ClientDaemon. The daemon is stopped via t.Cleanup on the
	// given t (so Carol, started inside a subtest, is torn down with that subtest):
	// Start's exit path writes a final status file, so the test must outlive it.
	startDaemon := func(t *testing.T, name, memberToken, memberID, repoDir string, cancelCtx context.Context) *daemon.ClientDaemon {
		clientCfg := &config.ClientConfig{
			HubURL:   hubURL,
			Token:    memberToken,
			MemberID: memberID,
			Workspaces: []config.WorkspaceConfig{
				{
					ID:       name + "-ws",
					Name:     name + "-workspace",
					RootPath: repoDir,
				},
			},
			LLM: config.LLMConfig{
				Provider: "openai",
				BaseURL:  mockServer.URL + "/v1",
				Model:    "gpt-4o",
				APIKey:   "test-key-mock",
			},
			ProbeTimeoutSeconds:  30,
			HeartbeatIntervalSec: 1,
		}

		agent := probe.NewDefaultAgent(nil)

		d, err := daemon.NewClientDaemon(clientCfg, agent, slog.Default())
		if err != nil {
			t.Fatalf("failed to create client daemon for %s: %v", name, err)
		}
		d.SetBackoffParams(20*time.Millisecond, 100*time.Millisecond, 1*time.Second)
		d.SetDrainGrace(2 * time.Second)
		daemonDir := t.TempDir()
		d.SetFilePaths(filepath.Join(daemonDir, "status.json"), filepath.Join(daemonDir, "daemon.pid"))

		go func() {
			_ = d.Start(cancelCtx)
		}()
		t.Cleanup(func() { _ = d.Stop() })
		return d
	}

	// Start Daemons for Alice and Bob immediately
	aliceCtx, cancelAlice := context.WithCancel(ctx)
	defer cancelAlice()
	_ = startDaemon(t, "alice", aliceToken, aliceID, aliceRepoDir, aliceCtx)

	bobCtx, cancelBob := context.WithCancel(ctx)
	defer cancelBob()
	_ = startDaemon(t, "bob", bobToken, bobID, bobRepoDir, bobCtx)

	// Poll Hub until Alice and Bob both report online: true (addresses finding: avoid fixed sleep)
	pollStart := time.Now()
	for {
		membersReq, err := http.NewRequestWithContext(ctx, "GET", hubURL+"/api/v1/members", nil)
		if err != nil {
			t.Fatal(err)
		}
		membersReq.Header.Set("Authorization", "Bearer "+aliceToken)

		resp, err := httpClient.Do(membersReq)
		if err == nil && resp.StatusCode == http.StatusOK {
			var memList protocol.MemberListResponse
			if err := json.NewDecoder(resp.Body).Decode(&memList); err == nil {
				aliceOnline, bobOnline := false, false
				for _, m := range memList.Members {
					if m.Name == "Alice" && m.Online {
						aliceOnline = true
					}
					if m.Name == "Bob" && m.Online {
						bobOnline = true
					}
				}
				resp.Body.Close()
				if aliceOnline && bobOnline {
					break
				}
			} else {
				resp.Body.Close()
			}
		} else if resp != nil {
			resp.Body.Close()
		}

		if time.Since(pollStart) > 5*time.Second {
			t.Fatal("timed out waiting for Alice and Bob daemons to report online: true")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// =========================================================================
	// Scenario (a): Alice asks Bob with wait=true -> completed, git tools used,
	//                answer mentions branch/changed file synthesized by Mock LLM.
	// =========================================================================
	t.Run("ScenarioA_AliceAsksBob_CompletedWithGitTools", func(t *testing.T) {
		mockServer.Reset()
		queryReq := protocol.QuerySubmitRequest{
			Target:         "Bob",
			Query:          "What are you working on right now?",
			TimeoutSeconds: 15,
			Wait:           true,
		}
		b, _ := json.Marshal(queryReq)
		req, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/queries?wait=true", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("query request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected HTTP 200 for wait=true, got %d: %s", resp.StatusCode, string(body))
		}

		var detail protocol.QueryDetailResponse
		if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if detail.Status != protocol.QueryStatusCompleted {
			t.Fatalf("expected status 'completed', got %q (error: %s)", detail.Status, detail.ErrorMessage)
		}

		// Verify git inspection tools were used
		hasGitTool := false
		for _, tool := range detail.ToolsUsed {
			if tool == "git_status" || tool == "git_diff" {
				hasGitTool = true
				break
			}
		}
		if !hasGitTool {
			t.Fatalf("expected tools_used to include git inspection tools, got %v", detail.ToolsUsed)
		}

		// Verify answer mentions branch and modified file synthesized by mock LLM
		if !strings.Contains(detail.Answer, "feature/auth-v2") {
			t.Fatalf("expected answer to mention branch 'feature/auth-v2', got: %s", detail.Answer)
		}
		if !strings.Contains(detail.Answer, "auth_handler.go") {
			t.Fatalf("expected answer to mention modified file 'auth_handler.go', got: %s", detail.Answer)
		}
	})

	// =========================================================================
	// Scenario (b): Privacy refusal (mock refuse-when-contains knob / auth-v2 rule)
	//                -> status refused, answer is canned text, contains no diff.
	// =========================================================================
	t.Run("ScenarioB_PrivacyRefusal_AuthV2", func(t *testing.T) {
		mockServer.Reset()
		mockServer.SetRefusal("auth-v2", "Refusal: confidential branch details cannot be disclosed per privacy guardrails.")

		queryReq := protocol.QuerySubmitRequest{
			Target:         "Bob",
			Query:          "Give me the full git diff for the feature/auth-v2 changes.",
			TimeoutSeconds: 15,
			Wait:           true,
		}
		b, _ := json.Marshal(queryReq)
		req, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/queries?wait=true", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("query request failed: %v", err)
		}
		defer resp.Body.Close()

		var detail protocol.QueryDetailResponse
		if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if detail.Status != protocol.QueryStatusRefused {
			t.Fatalf("expected status 'refused', got %q (answer: %s)", detail.Status, detail.Answer)
		}

		// Answer must be canned refusal text and contain no diff content
		if !strings.Contains(detail.Answer, "Refusal:") {
			t.Fatalf("expected canned refusal text, got: %s", detail.Answer)
		}
		if strings.Contains(detail.Answer, "diff --git") || strings.Contains(detail.Answer, "@@") {
			t.Fatalf("refused answer must not contain git diff output, got: %s", detail.Answer)
		}
	})

	// =========================================================================
	// Scenario (c): Offline queue: query to Carol whose daemon is started
	//                AFTER the query -> queued then transitions to completed.
	// =========================================================================
	t.Run("ScenarioC_OfflineQueue_CarolLateStart", func(t *testing.T) {
		mockServer.Reset()

		// Carol's daemon has NOT started yet. Alice submits query with wait=false.
		queryReq := protocol.QuerySubmitRequest{
			Target:         "Carol",
			Query:          "What search indexing features are you building?",
			TimeoutSeconds: 20,
			Wait:           false,
		}
		b, _ := json.Marshal(queryReq)
		req, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/queries", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("submit request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected HTTP 202/200, got %d: %s", resp.StatusCode, string(body))
		}

		var submitResp protocol.QuerySubmitResponse
		if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
			t.Fatalf("failed to decode submit response: %v", err)
		}

		if submitResp.Status != protocol.QueryStatusQueued {
			t.Fatalf("expected query to be 'queued' for offline Carol, got %q", submitResp.Status)
		}

		// Now launch Carol's daemon!
		carolCtx, cancelCarol := context.WithCancel(ctx)
		defer cancelCarol()
		_ = startDaemon(t, "carol", carolToken, carolID, carolRepoDir, carolCtx)

		// Long-poll query status until Carol's daemon drains the queue and completes it
		var finalDetail protocol.QueryDetailResponse
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			pollReq, err := http.NewRequestWithContext(ctx, "GET",
				fmt.Sprintf("%s/api/v1/queries/%s?wait=3s", hubURL, submitResp.QueryID), nil)
			if err != nil {
				t.Fatal(err)
			}
			pollReq.Header.Set("Authorization", "Bearer "+aliceToken)

			pollResp, err := httpClient.Do(pollReq)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			if pollResp.StatusCode == http.StatusOK {
				var d protocol.QueryDetailResponse
				if json.NewDecoder(pollResp.Body).Decode(&d) == nil && d.Status == protocol.QueryStatusCompleted {
					finalDetail = d
					pollResp.Body.Close()
					break
				}
			}
			pollResp.Body.Close()
			time.Sleep(100 * time.Millisecond)
		}

		if finalDetail.Status != protocol.QueryStatusCompleted {
			t.Fatalf("expected query %s to transition to 'completed' after Carol connected, got %q",
				submitResp.QueryID, finalDetail.Status)
		}
	})

	// =========================================================================
	// Scenario (d): TTL expiry: query with 1-2s TTL -> expired.
	// =========================================================================
	t.Run("ScenarioD_TTLExpiry_DaveNeverConnects", func(t *testing.T) {
		// Dave is paired, but Dave's daemon is never started.
		queryReq := protocol.QuerySubmitRequest{
			Target:         "Dave",
			Query:          "Quick question for Dave with short TTL",
			TimeoutSeconds: 5,
			TTLSeconds:     1, // 1-second TTL
			Wait:           false,
		}
		b, _ := json.Marshal(queryReq)
		req, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/queries", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+aliceToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("submit request failed: %v", err)
		}
		defer resp.Body.Close()

		var submitResp protocol.QuerySubmitResponse
		if err := json.NewDecoder(resp.Body).Decode(&submitResp); err != nil {
			t.Fatalf("failed to decode submit response: %v", err)
		}

		if submitResp.Status != protocol.QueryStatusQueued {
			t.Fatalf("expected status 'queued', got %q", submitResp.Status)
		}

		// Wait for TTL (1 second) to expire
		time.Sleep(1500 * time.Millisecond)

		// Fetch query detail: store evaluates TTL inline
		detailReq, err := http.NewRequestWithContext(ctx, "GET",
			fmt.Sprintf("%s/api/v1/queries/%s", hubURL, submitResp.QueryID), nil)
		if err != nil {
			t.Fatal(err)
		}
		detailReq.Header.Set("Authorization", "Bearer "+aliceToken)

		detailResp, err := httpClient.Do(detailReq)
		if err != nil {
			t.Fatalf("get query detail failed: %v", err)
		}
		defer detailResp.Body.Close()

		var detail protocol.QueryDetailResponse
		if err := json.NewDecoder(detailResp.Body).Decode(&detail); err != nil {
			t.Fatalf("failed to decode query detail: %v", err)
		}

		if detail.Status != protocol.QueryStatusExpired {
			t.Fatalf("expected query status to be 'expired' after TTL elapsed, got %q", detail.Status)
		}
	})

	// =========================================================================
	// Scenario (e): Audit: Bob's inbound shows Alice's query; Alice's outbound
	//                shows it; query detail forbidden (403) for unrelated member.
	// =========================================================================
	t.Run("ScenarioE_AuditAndForbiddenAccessControl", func(t *testing.T) {
		// First, submit a fresh query from Alice to Bob
		mockServer.Reset()
		queryReq := protocol.QuerySubmitRequest{
			Target:         "Bob",
			Query:          "Audit scenario test question from Alice to Bob",
			TimeoutSeconds: 15,
			Wait:           true,
		}
		b, _ := json.Marshal(queryReq)
		subReq, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/queries?wait=true", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		subReq.Header.Set("Authorization", "Bearer "+aliceToken)
		subReq.Header.Set("Content-Type", "application/json")

		subResp, err := httpClient.Do(subReq)
		if err != nil {
			t.Fatalf("query submit failed: %v", err)
		}
		defer subResp.Body.Close()

		var subDetail protocol.QueryDetailResponse
		if err := json.NewDecoder(subResp.Body).Decode(&subDetail); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		aliceToBobQID := subDetail.QueryID

		// 1. Bob's Inbound Audit: must contain aliceToBobQID
		inboundReq, err := http.NewRequestWithContext(ctx, "GET", hubURL+"/api/v1/audit/inbound", nil)
		if err != nil {
			t.Fatal(err)
		}
		inboundReq.Header.Set("Authorization", "Bearer "+bobToken)

		inboundResp, err := httpClient.Do(inboundReq)
		if err != nil {
			t.Fatalf("inbound audit request failed: %v", err)
		}
		defer inboundResp.Body.Close()

		var inAudit protocol.AuditListResponse
		if err := json.NewDecoder(inboundResp.Body).Decode(&inAudit); err != nil {
			t.Fatalf("failed to decode inbound audit: %v", err)
		}

		foundInbound := false
		for _, q := range inAudit.Entries {
			if q.QueryID == aliceToBobQID {
				foundInbound = true
				if q.AskerName != "Alice" && q.AskerID != aliceID {
					t.Errorf("expected asker Alice, got %s (%s)", q.AskerName, q.AskerID)
				}
				break
			}
		}
		if !foundInbound {
			t.Fatalf("expected Bob's inbound audit to contain query %s, got %d records",
				aliceToBobQID, len(inAudit.Entries))
		}

		// 2. Alice's Outbound Audit: must contain aliceToBobQID
		outboundReq, err := http.NewRequestWithContext(ctx, "GET", hubURL+"/api/v1/audit/outbound", nil)
		if err != nil {
			t.Fatal(err)
		}
		outboundReq.Header.Set("Authorization", "Bearer "+aliceToken)

		outboundResp, err := httpClient.Do(outboundReq)
		if err != nil {
			t.Fatalf("outbound audit request failed: %v", err)
		}
		defer outboundResp.Body.Close()

		var outAudit protocol.AuditListResponse
		if err := json.NewDecoder(outboundResp.Body).Decode(&outAudit); err != nil {
			t.Fatalf("failed to decode outbound audit: %v", err)
		}

		foundOutbound := false
		for _, q := range outAudit.Entries {
			if q.QueryID == aliceToBobQID {
				foundOutbound = true
				if q.TargetMemberName != "Bob" && q.TargetMemberID != bobID {
					t.Errorf("expected target Bob, got %s (%s)", q.TargetMemberName, q.TargetMemberID)
				}
				break
			}
		}
		if !foundOutbound {
			t.Fatalf("expected Alice's outbound audit to contain query %s, got %d records",
				aliceToBobQID, len(outAudit.Entries))
		}

		// 3. Unrelated member Carol attempts to inspect Alice's query to Bob -> HTTP 403 Forbidden
		forbiddenReq, err := http.NewRequestWithContext(ctx, "GET",
			fmt.Sprintf("%s/api/v1/queries/%s", hubURL, aliceToBobQID), nil)
		if err != nil {
			t.Fatal(err)
		}
		forbiddenReq.Header.Set("Authorization", "Bearer "+carolToken)

		forbiddenResp, err := httpClient.Do(forbiddenReq)
		if err != nil {
			t.Fatalf("detail request failed: %v", err)
		}
		defer forbiddenResp.Body.Close()

		if forbiddenResp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(forbiddenResp.Body)
			t.Fatalf("expected HTTP 403 Forbidden for unrelated member Carol, got %d: %s",
				forbiddenResp.StatusCode, string(body))
		}
	})

	// =========================================================================
	// Scenario (f): Feishu integration: fake Feishu server posts signed+encrypted
	//                im.message.receive_v1 to Bob's webhook -> daemon answers ->
	//                fake server records the reply text.
	// =========================================================================
	t.Run("ScenarioF_FeishuWebhook_SignedEncryptedReply", func(t *testing.T) {
		mockServer.Reset()

		// Set up Fake Feishu Open Platform Server
		fakeFeishu := feishu.NewFakeServer()
		defer fakeFeishu.Close()

		fakeURL, err := url.Parse(fakeFeishu.URL())
		if err != nil {
			t.Fatalf("failed to parse fake feishu url: %v", err)
		}

		// Intercept outgoing HTTP calls to open.feishu.cn on default transport
		origTransport := http.DefaultTransport
		http.DefaultTransport = &transportInterceptor{
			targetURL: fakeURL,
			inner:     origTransport,
		}
		defer func() {
			http.DefaultTransport = origTransport
		}()

		// Save Feishu bot binding for Bob
		encryptKey := "12345678901234567890123456789012"
		bindingReq := protocol.FeishuBindingRequest{
			AppID:             "cli_mock_bob_app",
			AppSecret:         "sec_mock_bob_secret",
			VerificationToken: "ver_mock_bob_token",
			EncryptKey:        encryptKey,
		}
		b, _ := json.Marshal(bindingReq)
		bindHTTPReq, err := http.NewRequestWithContext(ctx, "POST", hubURL+"/api/v1/feishu/binding", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		bindHTTPReq.Header.Set("Authorization", "Bearer "+bobToken)
		bindHTTPReq.Header.Set("Content-Type", "application/json")

		bindResp, err := httpClient.Do(bindHTTPReq)
		if err != nil {
			t.Fatalf("save feishu binding failed: %v", err)
		}
		defer bindResp.Body.Close()

		if bindResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(bindResp.Body)
			t.Fatalf("save binding returned HTTP %d: %s", bindResp.StatusCode, string(body))
		}

		// Build encrypted Feishu im.message.receive_v1 event payload
		eventBody, err := fakeFeishu.BuildEncryptedMessageReceiveEvent(
			"evt_feishu_test_001",
			"om_feishu_msg_1001",
			"oc_test_chat_001",
			"ou_feishu_user_001",
			"What are you working on right now?",
			encryptKey,
		)
		if err != nil {
			t.Fatalf("failed to build encrypted feishu event: %v", err)
		}

		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		nonce := "mock_test_nonce_888"
		sig := fakeFeishu.SignEvent(timestamp, nonce, encryptKey, eventBody)

		// Post webhook to Bob's endpoint: /api/v1/feishu/webhook/{bob_member_id}
		webhookURL := fmt.Sprintf("%s/api/v1/feishu/webhook/%s", hubURL, bobID)
		webhookReq, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(eventBody))
		if err != nil {
			t.Fatal(err)
		}
		webhookReq.Header.Set("Content-Type", "application/json; charset=utf-8")
		webhookReq.Header.Set("X-Lark-Request-Timestamp", timestamp)
		webhookReq.Header.Set("X-Lark-Request-Nonce", nonce)
		webhookReq.Header.Set("X-Lark-Signature", sig)

		webhookResp, err := httpClient.Do(webhookReq)
		if err != nil {
			t.Fatalf("feishu webhook post failed: %v", err)
		}
		defer webhookResp.Body.Close()

		if webhookResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(webhookResp.Body)
			t.Fatalf("feishu webhook returned HTTP %d: %s", webhookResp.StatusCode, string(body))
		}

		// Wait for Bob's daemon to answer and Hub to send the reply back to FakeServer
		deadline := time.Now().Add(10 * time.Second)
		var replies []feishu.RecordedReply
		for time.Now().Before(deadline) {
			replies = fakeFeishu.Replies()
			if len(replies) > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if len(replies) == 0 {
			t.Fatal("expected FakeServer to receive Feishu reply message from Hub, got none")
		}

		reply := replies[0]
		if reply.MessageID != "om_feishu_msg_1001" {
			t.Errorf("expected reply message_id 'om_feishu_msg_1001', got %q", reply.MessageID)
		}
		if !strings.Contains(reply.Text, "feature/auth-v2") {
			t.Errorf("expected Feishu reply text to mention Bob's workspace branch, got: %s", reply.Text)
		}
	})
}
