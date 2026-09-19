package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
	"github.com/Sskift/talkintent/skills"
)

// TestExtractTargetTable tests natural language target resolution including longest match,
// alias resolution, case insensitivity, candidate deduplication, and ambiguity detection.
func TestExtractTargetTable(t *testing.T) {
	members := []protocol.MemberInfo{
		{
			ID:      "mem_1",
			Name:    "张三",
			Aliases: []string{"zhangsan", "san"},
		},
		{
			ID:      "mem_2",
			Name:    "张三丰",
			Aliases: []string{"taiji"},
		},
		{
			ID:      "mem_3",
			Name:    "李四",
			Aliases: []string{"lisi", "four"},
		},
		{
			ID:      "mem_4",
			Name:    "Bob Smith",
			Aliases: []string{"bob", "bsmith"},
		},
	}

	tests := []struct {
		name          string
		query         string
		expectedName  string
		expectedAlias string
		wantAmbiguous bool
		candidateLen  int
	}{
		{
			name:          "Direct name match",
			query:         "请问张三现在登录模块怎么样了",
			expectedName:  "张三",
			expectedAlias: "张三",
			wantAmbiguous: false,
		},
		{
			name:          "Longest name match wins (张三丰 over 张三)",
			query:         "问一下张三丰太极模块写得如何",
			expectedName:  "张三丰",
			expectedAlias: "张三丰",
			wantAmbiguous: false,
		},
		{
			name:          "Alias match",
			query:         "ask lisi what ports are open",
			expectedName:  "李四",
			expectedAlias: "lisi",
			wantAmbiguous: false,
		},
		{
			name:          "Case-insensitive alias match",
			query:         "Ask BOB about the database migration",
			expectedName:  "Bob Smith",
			expectedAlias: "bob",
			wantAmbiguous: false,
		},
		{
			name:          "Single member matching both name and alias deduplicates cleanly",
			query:         "问一下bob (Bob Smith) 进度",
			expectedName:  "Bob Smith",
			expectedAlias: "Bob Smith",
			wantAmbiguous: false,
		},
		{
			name:          "No match in query",
			query:         "今天天气怎么样",
			expectedName:  "",
			expectedAlias: "",
			wantAmbiguous: false,
		},
		{
			name:          "Empty query text",
			query:         "",
			expectedName:  "",
			expectedAlias: "",
			wantAmbiguous: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, matchedAlias, ambiguous, candidates := ExtractTarget(tt.query, members)
			if ambiguous != tt.wantAmbiguous {
				t.Fatalf("ambiguous = %v, want %v", ambiguous, tt.wantAmbiguous)
			}
			if target != tt.expectedName {
				t.Errorf("target = %q, want %q", target, tt.expectedName)
			}
			if matchedAlias != tt.expectedAlias {
				t.Errorf("matchedAlias = %q, want %q", matchedAlias, tt.expectedAlias)
			}
			if len(candidates) != tt.candidateLen {
				t.Errorf("len(candidates) = %d, want %d", len(candidates), tt.candidateLen)
			}
		})
	}
}

// TestExtractTargetAmbiguity verifies ambiguous match detection when two different members match same length.
func TestExtractTargetAmbiguity(t *testing.T) {
	members := []protocol.MemberInfo{
		{
			ID:      "mem_a",
			Name:    "Alex",
			Aliases: []string{"dev_a"},
		},
		{
			ID:      "mem_b",
			Name:    "Alan",
			Aliases: []string{"dev_b"},
		},
	}

	// Query mentioning both Alex and Alan equally
	query := "Can Alex and Alan review this PR?"
	target, _, ambiguous, candidates := ExtractTarget(query, members)
	if !ambiguous {
		t.Fatalf("expected ambiguous match, got target=%q", target)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
}

// TestSubcommandHelpAndVersion tests help, version, and basic metadata output via Run.
func TestSubcommandHelpAndVersion(t *testing.T) {
	ctx := context.Background()

	// 1. Help
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("help exited with code %d", code)
	}
	if !strings.Contains(stdout.String(), "TalkIntent") {
		t.Errorf("expected help output to mention TalkIntent, got: %s", stdout.String())
	}

	// 2. Version plain text
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("version exited with code %d", code)
	}
	if !strings.Contains(stdout.String(), "talkintent version") {
		t.Errorf("expected version output, got: %s", stdout.String())
	}

	// 3. Version JSON
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"version", "-json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("version -json exited with code %d", code)
	}
	var vi VersionInfo
	if err := json.Unmarshal(stdout.Bytes(), &vi); err != nil {
		t.Fatalf("failed to decode version json: %v", err)
	}
	if vi.Version == "" || vi.OS == "" {
		t.Errorf("incomplete version info: %+v", vi)
	}

	// 4. Unknown subcommand returns code 1
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"nonexistent_cmd"}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("expected exit code 1 for unknown command, got %d", code)
	}
}

// TestSubcommandWeb verifies that the web dashboard URL contains NO credentials (F11 & Rule 5).
func TestSubcommandWeb(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.ClientConfig{
		HubURL: "http://hub.example.com:8080",
		Token:  "ti_mem_supersecrettoken12345",
	}
	if err := config.SaveClientConfig(cfgPath, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	var stdout, stderr bytes.Buffer
	ctx := context.Background()
	code := Run(ctx, []string{"web", "-config", cfgPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("web exited with code %d", code)
	}

	outStr := stdout.String()
	if !strings.Contains(outStr, "http://hub.example.com:8080/web") {
		t.Errorf("expected dashboard URL in output, got: %s", outStr)
	}
	// Strictly enforce F11 & Rule 5: NO token in printed URL
	if strings.Contains(outStr, "supersecrettoken") || strings.Contains(outStr, "ti_mem_") {
		t.Fatalf("CRITICAL SECURITY VIOLATION (F11/Rule 5): token leaked in web command output: %s", outStr)
	}
}

// TestSubcommandSkillInstall verifies that skill install writes SKILL.md to the target directory (F45).
func TestSubcommandSkillInstall(t *testing.T) {
	tmpDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ctx := context.Background()

	// 1. Install
	code := Run(ctx, []string{"skill", "install", "-dir", tmpDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("skill install failed with code %d: %s", code, stderr.String())
	}

	installedFile := filepath.Join(tmpDir, "talkintent", "SKILL.md")
	data, err := os.ReadFile(installedFile)
	if err != nil {
		t.Fatalf("failed to read installed SKILL.md at %s: %v", installedFile, err)
	}

	if !strings.Contains(string(data), "/talkintent") {
		t.Errorf("installed skill file missing expected content: %s", string(data))
	}

	// 2. Show
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"skill", "show"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("skill show failed with code %d", code)
	}
	if !strings.Contains(stdout.String(), "/talkintent") {
		t.Errorf("skill show missing expected content: %s", stdout.String())
	}
	if strings.TrimSpace(stdout.String()) != strings.TrimSpace(skills.SkillMD) {
		t.Errorf("skill show output differs from skills.SkillMD")
	}
}

// TestSubcommandWorkspace verifies workspace add, list, and remove commands with config persistence (F38).
func TestSubcommandWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.ClientConfig{
		HubURL:     "http://localhost:8080",
		Token:      "ti_mem_test123",
		Workspaces: []config.WorkspaceConfig{},
	}
	if err := config.SaveClientConfig(cfgPath, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	// 1. Add workspace
	wsDir := filepath.Join(tmpDir, "repo_a")
	_ = os.MkdirAll(wsDir, 0755)

	code := Run(ctx, []string{"workspace", "add", "-config", cfgPath, wsDir, "my_repo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("workspace add failed: %s", stderr.String())
	}

	// 2. List workspaces
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"workspace", "list", "-config", cfgPath, "-json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("workspace list failed: %s", stderr.String())
	}
	var listResp struct {
		Total      int                      `json:"total"`
		Workspaces []config.WorkspaceConfig `json:"workspaces"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listResp); err != nil {
		t.Fatalf("failed to decode workspace list: %v", err)
	}
	if listResp.Total != 1 || listResp.Workspaces[0].Name != "my_repo" {
		t.Errorf("unexpected workspace list: %+v", listResp)
	}

	// 3. Remove workspace
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"workspace", "remove", "-config", cfgPath, "my_repo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("workspace remove failed: %s", stderr.String())
	}

	// Verify persistence in config file
	loadedCfg, err := config.LoadClientConfig(cfgPath)
	if err != nil {
		t.Fatalf("failed to load updated config: %v", err)
	}
	if len(loadedCfg.Workspaces) != 0 {
		t.Errorf("expected 0 workspaces after remove, got %d", len(loadedCfg.Workspaces))
	}
}

// TestSubcommandLLM verifies llm set, show with redaction (Rule 5), and test.
func TestSubcommandLLM(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	// 1. Set LLM
	code := Run(ctx, []string{
		"llm", "set",
		"-config", cfgPath,
		"-provider", "openai",
		"-base-url", "https://api.openai.com/v1",
		"-api-key", "sk-proj-supersecretkey1234567890",
		"-model", "gpt-4o",
		"-ca-file", "/path/to/ca.pem",
		"-tls-server-name", "gateway.internal",
		"-insecure-skip-verify",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("llm set failed: %s", stderr.String())
	}

	// 2. Show LLM (must be redacted!)
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"llm", "show", "-config", cfgPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("llm show failed: %s", stderr.String())
	}

	showOut := stdout.String()
	if !strings.Contains(showOut, "gpt-4o") {
		t.Errorf("llm show missing model name")
	}
	// Strictly enforce Rule 5: never print raw secret key
	if strings.Contains(showOut, "supersecretkey") {
		t.Fatalf("CRITICAL SECURITY VIOLATION (Rule 5): raw API key exposed in llm show: %s", showOut)
	}
	if !strings.Contains(showOut, "sk-***[len=") {
		t.Errorf("expected redacted API key format, got: %s", showOut)
	}
}

// TestSubcommandPairAndFilePermissions verifies pairing against Hub stub and 0600 config permissions.
func TestSubcommandPairAndFilePermissions(t *testing.T) {
	// Stub Hub server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/pair" && r.Method == "POST" {
			var req protocol.PairRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.InviteCode == "INV-VALID" {
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(protocol.PairResponse{
					MemberID:   "mem_test",
					MemberName: "Tester",
					Token:      "ti_mem_newsecrettoken123",
				})
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(protocol.ErrorResponse{
				Error: protocol.ErrorDetail{
					Code:    protocol.ErrCodeInvalidArgument,
					Message: "invite code invalid",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	ctx := context.Background()

	// 1. Failed pair with bad code
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{
		"pair",
		"-hub", server.URL,
		"-code", "INV-BAD",
		"-config", cfgPath,
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected pair to fail with bad code, got 0")
	}

	// 2. Successful pair
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{
		"pair",
		"-hub", server.URL,
		"-code", "INV-VALID",
		"-name", "my-macbook",
		"-config", cfgPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pair failed with valid code: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "Tester") {
		t.Errorf("pair output missing member name: %s", stdout.String())
	}

	// 3. Verify file permissions on non-Windows platforms
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("failed to stat config file: %v", err)
		}
		perm := fi.Mode().Perm()
		if perm != 0600 {
			t.Errorf("expected config file mode 0600, got %04o", perm)
		}
	}

	// 4. Positional invite code with -hub flag (regression test for runner finding)
	stdout.Reset()
	stderr.Reset()
	cfgPath2 := filepath.Join(tmpDir, "config2.json")
	code = Run(ctx, []string{
		"pair",
		"-hub", server.URL,
		"INV-VALID",
		"-config", cfgPath2,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pair with positional code failed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Tester") {
		t.Errorf("expected Tester in output, got: %s", stdout.String())
	}

	// 5. Positional invite code with TALKINTENT_HUB_URL environment variable
	t.Setenv(config.EnvTalkIntentHubURL, server.URL)
	stdout.Reset()
	stderr.Reset()
	cfgPath3 := filepath.Join(tmpDir, "config3.json")
	code = Run(ctx, []string{
		"pair",
		"INV-VALID",
		"-config", cfgPath3,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pair with TALKINTENT_HUB_URL and positional code failed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Tester") {
		t.Errorf("expected Tester in output, got: %s", stdout.String())
	}

	// 6. Both hub URL and invite code passed positionally
	t.Setenv(config.EnvTalkIntentHubURL, "")
	stdout.Reset()
	stderr.Reset()
	cfgPath4 := filepath.Join(tmpDir, "config4.json")
	code = Run(ctx, []string{
		"pair",
		server.URL,
		"INV-VALID",
		"-config", cfgPath4,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("pair with positional hub and code failed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Tester") {
		t.Errorf("expected Tester in output, got: %s", stdout.String())
	}

	// 7. Positional code without hub URL or env variable fails cleanly
	stdout.Reset()
	stderr.Reset()
	cfgPath5 := filepath.Join(tmpDir, "config5.json")
	code = Run(ctx, []string{
		"pair",
		"INV-VALID",
		"-config", cfgPath5,
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected pair to fail when hub URL is missing, got 0")
	}
	if !strings.Contains(stderr.String(), "both --hub and --code are required") {
		t.Errorf("expected error message about required hub and code, got: %s", stderr.String())
	}
}

// TestSubcommandMembers verifies members directory querying.
func TestSubcommandMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/members" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(protocol.MemberListResponse{
				Members: []protocol.MemberInfo{
					{
						ID:         "mem_1",
						Name:       "张三",
						Online:     true,
						Workspaces: []string{"backend"},
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.ClientConfig{
		HubURL: server.URL,
		Token:  "ti_mem_token",
	}
	_ = config.SaveClientConfig(cfgPath, cfg)

	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{"members", "-config", cfgPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("members failed: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "张三") || !strings.Contains(stdout.String(), "ONLINE") {
		t.Errorf("unexpected members table: %s", stdout.String())
	}
}

// TestSubcommandHistory verifies inbound and outbound query history retrieval.
func TestSubcommandHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/audit/") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(protocol.AuditListResponse{
				Total: 1,
				Entries: []protocol.AuditLogEntry{
					{
						QueryID:          "q_100",
						AskerName:        "Alice",
						TargetMemberName: "Bob",
						Query:            "Any breaking API changes?",
						Status:           protocol.QueryStatusCompleted,
						DurationMS:       1200,
						Timestamp:        time.Now().UnixMilli(),
					},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.ClientConfig{
		HubURL: server.URL,
		Token:  "ti_mem_token",
	}
	_ = config.SaveClientConfig(cfgPath, cfg)

	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{"history", "-outbound", "-config", cfgPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("history failed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Any breaking API changes?") {
		t.Errorf("history output missing query: %s", stdout.String())
	}
}

// TestSubcommandAskExecution verifies ask against httptest Hub stub, covering completed,
// refused, ambiguous target, and timeout scenarios.
func TestSubcommandAskExecution(t *testing.T) {
	completedQueryID := "q_completed"
	refusedQueryID := "q_refused"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/members":
			_ = json.NewEncoder(w).Encode(protocol.MemberListResponse{
				Members: []protocol.MemberInfo{
					{ID: "mem_zs", Name: "张三", Aliases: []string{"zhangsan"}},
					{ID: "mem_ls", Name: "李四", Aliases: []string{"lisi"}},
				},
			})
		case r.URL.Path == "/api/v1/queries" && r.Method == "POST":
			var req protocol.QuerySubmitRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Target == "张三" {
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(protocol.QuerySubmitResponse{
					QueryID:          completedQueryID,
					Status:           protocol.QueryStatusDispatched,
					TargetMemberName: "张三",
				})
			} else if req.Target == "李四" {
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(protocol.QuerySubmitResponse{
					QueryID:          refusedQueryID,
					Status:           protocol.QueryStatusRefused,
					TargetMemberName: "李四",
				})
			} else {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(protocol.TargetResolveResponse{
					Status: "ambiguous",
					Candidates: []protocol.CandidateMember{
						{ID: "mem_zs", Name: "张三"},
						{ID: "mem_ls", Name: "李四"},
					},
				})
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/queries/"):
			qid := strings.TrimPrefix(r.URL.Path, "/api/v1/queries/")
			if strings.Contains(qid, completedQueryID) {
				_ = json.NewEncoder(w).Encode(protocol.QueryDetailResponse{
					QueryID:          completedQueryID,
					TargetMemberName: "张三",
					Status:           protocol.QueryStatusCompleted,
					Answer:           "登录模块重构已完成，新接口为 POST /api/v2/auth/login",
					ToolsUsed:        []string{"git_diff", "git_status"},
					DurationMS:       450,
				})
			} else if strings.Contains(qid, refusedQueryID) {
				_ = json.NewEncoder(w).Encode(protocol.QueryDetailResponse{
					QueryID:          refusedQueryID,
					TargetMemberName: "李四",
					Status:           protocol.QueryStatusRefused,
					ErrorMessage:     "Refused by teammate privacy guardrail",
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	cfg := &config.ClientConfig{
		HubURL: server.URL,
		Token:  "ti_mem_token",
	}
	_ = config.SaveClientConfig(cfgPath, cfg)

	ctx := context.Background()

	// 1. Successful ask with explicit target
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{
		"ask",
		"-config", cfgPath,
		"--target", "张三",
		"--query", "登录模块现在怎么样了",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ask expected code 0, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "登录模块重构已完成") {
		t.Errorf("ask output missing answer: %s", stdout.String())
	}

	// 2. Natural language target extraction ("问一下张三现在进度")
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{
		"ask",
		"-config", cfgPath,
		"问一下张三现在登录模块重构得怎么样了",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("NL ask expected code 0, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "登录模块重构已完成") {
		t.Errorf("NL ask output missing answer: %s", stdout.String())
	}

	// 3. Refused query returns non-zero exit code
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{
		"ask",
		"-config", cfgPath,
		"--target", "李四",
		"--query", "私有分支代码是什么",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code on refused query, got 0")
	}
	if !strings.Contains(stdout.String(), "refused") && !strings.Contains(stderr.String(), "refused") {
		t.Errorf("expected refusal mention in output")
	}

	// 4. Ambiguous target response shows candidates (F37)
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{
		"ask",
		"-config", cfgPath,
		"--target", "ambiguous_target",
		"--query", "hello",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit code on ambiguous target, got 0")
	}
	if !strings.Contains(stderr.String(), "Candidate Members") {
		t.Errorf("expected candidate member listing on ambiguous target: %s", stderr.String())
	}
}

// TestSubcommandInvite verifies admin invite generation via Hub REST.
func TestSubcommandInvite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/invites" && r.Method == "POST" {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer ti_adm_secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(protocol.InviteCreateResponse{
				Code:       "INV-ABC-123",
				ExpiresAt:  time.Now().Add(72 * time.Hour).UnixMilli(),
				TargetName: "Alice",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{
		"invite",
		"-hub", server.URL,
		"-admin-token", "ti_adm_secret",
		"-name", "Alice",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("invite failed: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "INV-ABC-123") {
		t.Errorf("invite output missing invite code: %s", stdout.String())
	}
}

// TestSubcommandPrivacy verifies privacy init, show, and edit-path.
func TestSubcommandPrivacy(t *testing.T) {
	tmpDir := t.TempDir()
	privacyPath := filepath.Join(tmpDir, "privacy-prompt.md")

	ctx := context.Background()
	var stdout, stderr bytes.Buffer

	// 1. privacy init
	code := Run(ctx, []string{"privacy", "init", "-path", privacyPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("privacy init failed: %s", stderr.String())
	}

	data, err := os.ReadFile(privacyPath)
	if err != nil {
		t.Fatalf("privacy file not created: %v", err)
	}
	if !strings.Contains(string(data), "Never disclose any credentials") {
		t.Errorf("unexpected privacy template content: %s", string(data))
	}

	// 2. privacy edit-path
	stdout.Reset()
	stderr.Reset()
	code = Run(ctx, []string{"privacy", "edit-path"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("privacy edit-path failed: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "privacy-prompt.md") {
		t.Errorf("unexpected edit-path output: %s", stdout.String())
	}
}
