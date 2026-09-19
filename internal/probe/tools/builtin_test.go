package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	expected := []string{
		"git_status",
		"git_diff",
		"git_log",
		"list_dir",
		"read_file",
		"grep_search",
		"recent_files",
		"listening_ports",
		"find_api_specs",
	}

	for _, name := range expected {
		tool, ok := r.Get(name)
		if !ok || tool == nil {
			t.Errorf("expected tool %q in registry", name)
			continue
		}
		if tool.Name() != name {
			t.Errorf("tool.Name() mismatch: got %q, want %q", tool.Name(), name)
		}
		if tool.Description() == "" {
			t.Errorf("tool %q has empty description", name)
		}
		params := tool.Parameters()
		if params == nil || params["type"] != "object" {
			t.Errorf("tool %q has invalid parameters schema", name)
		}
	}

	list := r.List()
	if len(list) != len(expected) {
		t.Errorf("expected %d tools in List(), got %d", len(expected), len(list))
	}
}

func TestReadFileTool(t *testing.T) {
	tmp := t.TempDir()
	fileA := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(fileA, []byte("Hello TalkIntent Probe!\nLine 2"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	tool := &ReadFileTool{}
	ctx := context.Background()

	// 1. Valid read
	out, err := tool.Execute(ctx, tmp, map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("expected successful read, got error: %v", err)
	}
	if !strings.Contains(out, "Hello TalkIntent Probe!") {
		t.Errorf("expected content in output, got: %s", out)
	}

	// 2. Denylisted file (.env)
	envFile := filepath.Join(tmp, ".env")
	_ = os.WriteFile(envFile, []byte("SECRET=12345"), 0600)
	_, err = tool.Execute(ctx, tmp, map[string]any{"path": ".env"})
	if !errors.Is(err, ErrDenylistedFile) {
		t.Errorf("expected ErrDenylistedFile for .env, got: %v", err)
	}

	// 3. Traversal attack
	_, err = tool.Execute(ctx, tmp, map[string]any{"path": "../escaping.txt"})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("expected ErrSandboxViolation for traversal path, got: %v", err)
	}

	// 4. Directory read attempt
	subDir := filepath.Join(tmp, "sub")
	_ = os.MkdirAll(subDir, 0755)
	_, err = tool.Execute(ctx, tmp, map[string]any{"path": "sub"})
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("expected error when reading directory, got: %v", err)
	}
}

func TestListDirTool(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, "README.md"), []byte("# Test"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, ".env"), []byte("SECRET=yes"), 0600) // Denylisted

	sub := filepath.Join(tmp, "sub")
	_ = os.MkdirAll(sub, 0755)
	_ = os.WriteFile(filepath.Join(sub, "child.go"), []byte("package sub"), 0644)

	tool := &ListDirTool{}
	ctx := context.Background()

	out, err := tool.Execute(ctx, tmp, map[string]any{"path": ".", "max_depth": 2})
	if err != nil {
		t.Fatalf("list_dir failed: %v", err)
	}

	if !strings.Contains(out, "main.go") {
		t.Errorf("expected main.go in output: %s", out)
	}
	if !strings.Contains(out, "README.md") {
		t.Errorf("expected README.md in output: %s", out)
	}
	if strings.Contains(out, ".env") {
		t.Errorf("expected .env to be omitted from list_dir: %s", out)
	}

	// Sandbox violation test
	_, err = tool.Execute(ctx, tmp, map[string]any{"path": "../escape"})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("expected sandbox violation, got %v", err)
	}
}

func TestGrepSearchTool(t *testing.T) {
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "service.go"), []byte("func StartServer() {\n  port := 8080\n}"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, "client.go"), []byte("func Connect() {\n  target := \"localhost\"\n}"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, ".env"), []byte("API_KEY=StartServer"), 0600) // Denylisted

	tool := &GrepSearchTool{}
	ctx := context.Background()

	out, err := tool.Execute(ctx, tmp, map[string]any{"pattern": "StartServer"})
	if err != nil {
		t.Fatalf("grep_search failed: %v", err)
	}

	if !strings.Contains(out, "service.go") || !strings.Contains(out, "func StartServer()") {
		t.Errorf("expected match in service.go, got: %s", out)
	}
	if strings.Contains(out, ".env") {
		t.Errorf("expected .env to be skipped in grep_search, got: %s", out)
	}
}

func TestRecentFilesTool(t *testing.T) {
	tmp := t.TempDir()
	f1 := filepath.Join(tmp, "older.txt")
	f2 := filepath.Join(tmp, "newer.txt")
	_ = os.WriteFile(f1, []byte("old"), 0644)
	time.Sleep(50 * time.Millisecond)
	_ = os.WriteFile(f2, []byte("new"), 0644)

	tool := &RecentFilesTool{}
	ctx := context.Background()

	out, err := tool.Execute(ctx, tmp, map[string]any{"limit": 5})
	if err != nil {
		t.Fatalf("recent_files failed: %v", err)
	}

	if !strings.Contains(out, "newer.txt") || !strings.Contains(out, "older.txt") {
		t.Errorf("expected both files in output: %s", out)
	}
}

func TestFindAPISpecsTool(t *testing.T) {
	tmp := t.TempDir()
	protoDir := filepath.Join(tmp, "proto")
	_ = os.MkdirAll(protoDir, 0755)
	_ = os.WriteFile(filepath.Join(protoDir, "auth.proto"), []byte("syntax = \"proto3\";"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, "swagger.json"), []byte("{\"swagger\": \"2.0\"}"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, "schema.graphql"), []byte("type Query { me: User }"), 0644)
	_ = os.WriteFile(filepath.Join(tmp, "regular.txt"), []byte("not a spec"), 0644)

	tool := &FindAPISpecsTool{}
	ctx := context.Background()

	out, err := tool.Execute(ctx, tmp, map[string]any{})
	if err != nil {
		t.Fatalf("find_api_specs failed: %v", err)
	}

	if !strings.Contains(out, "auth.proto") {
		t.Errorf("expected auth.proto in find_api_specs output: %s", out)
	}
	if !strings.Contains(out, "swagger.json") {
		t.Errorf("expected swagger.json in find_api_specs output: %s", out)
	}
	if !strings.Contains(out, "schema.graphql") {
		t.Errorf("expected schema.graphql in find_api_specs output: %s", out)
	}
	if strings.Contains(out, "regular.txt") {
		t.Errorf("regular.txt should not be classified as spec: %s", out)
	}
}

func TestListeningPortsTool(t *testing.T) {
	tool := &ListeningPortsTool{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := tool.Execute(ctx, ".", map[string]any{})
	if err != nil {
		t.Fatalf("listening_ports failed: %v", err)
	}
	if out == "" {
		t.Errorf("expected non-empty output from listening_ports")
	}
}

func TestGitToolsInRepo(t *testing.T) {
	// Check if git is available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not found in PATH")
	}

	tmp := t.TempDir()

	// Initialize git repo
	runGit := func(args ...string) error {
		c := exec.Command("git", args...)
		c.Dir = tmp
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		return c.Run()
	}

	if err := runGit("init"); err != nil {
		t.Skipf("git init failed: %v", err)
	}
	// Initial commit
	testFile := filepath.Join(tmp, "types.go")
	_ = os.WriteFile(testFile, []byte("package types\ntype User struct{}\n"), 0644)
	_ = runGit("add", "types.go")
	_ = runGit("commit", "-m", "feat: initial commit")

	// Create branch feature/auth-v2
	_ = runGit("checkout", "-b", "feature/auth-v2")

	// Modify file
	_ = os.WriteFile(testFile, []byte("package types\ntype User struct{}\ntype Token struct{}\n"), 0644)

	ctx := context.Background()

	// 1. git_status
	statusTool := &GitStatusTool{}
	statusOut, err := statusTool.Execute(ctx, tmp, map[string]any{})
	if err != nil {
		t.Fatalf("git_status failed: %v", err)
	}
	if !strings.Contains(statusOut, "feature/auth-v2") {
		t.Errorf("expected branch feature/auth-v2 in git_status, got: %s", statusOut)
	}

	// 2. git_diff
	diffTool := &GitDiffTool{}
	diffOut, err := diffTool.Execute(ctx, tmp, map[string]any{})
	if err != nil {
		t.Fatalf("git_diff failed: %v", err)
	}
	if !strings.Contains(diffOut, "type Token struct{}") {
		t.Errorf("expected type Token struct{} in git_diff, got: %s", diffOut)
	}

	// 3. git_log
	logTool := &GitLogTool{}
	logOut, err := logTool.Execute(ctx, tmp, map[string]any{})
	if err != nil {
		t.Fatalf("git_log failed: %v", err)
	}
	if !strings.Contains(logOut, "feat: initial commit") {
		t.Errorf("expected commit message in git_log, got: %s", logOut)
	}
}

func TestGitToolsNonRepo(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()

	statusTool := &GitStatusTool{}
	out, err := statusTool.Execute(ctx, tmp, map[string]any{})
	if err != nil {
		t.Fatalf("expected no fatal error for non-git repo, got: %v", err)
	}
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("expected non-repo message, got: %s", out)
	}
}

func TestFilterDiffOutput_Variations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		redacted bool
		contains string
	}{
		{
			name: "standard diff with .env",
			input: `diff --git a/.env b/.env
index 0000000..1234567 100644
--- /dev/null
+++ b/.env
@@ -0,0 +1,2 @@
+API_KEY=secret_value_123`,
			redacted: true,
			contains: "diff --git a/.env b/.env",
		},
		{
			name: "diff with spaces and quotes",
			input: `diff --git "a/config files/.env" "b/config files/.env"
index 1111111..2222222 100644
--- "a/config files/.env"
+++ "b/config files/.env"
@@ -1,1 +1,2 @@
+PASSWORD=supersecret`,
			redacted: true,
			contains: `diff --git "a/config files/.env" "b/config files/.env"`,
		},
		{
			name: "diff with no-prefix flag",
			input: `diff --git .env .env
index 1111111..2222222 100644
--- .env
+++ .env
@@ -1,1 +1,2 @@
+TOKEN=ghp_secret`,
			redacted: true,
			contains: "diff --git .env .env",
		},
		{
			name: "rename to sensitive file",
			input: `diff --git a/innocent.txt b/secret.txt
similarity index 100%
rename from innocent.txt
rename to credentials.json`,
			redacted: true,
			contains: "diff --git a/innocent.txt b/secret.txt",
		},
		{
			name: "copy from sensitive file",
			input: `diff --git a/id_rsa b/id_rsa_backup
similarity index 100%
copy from id_rsa
copy to id_rsa_backup`,
			redacted: true,
			contains: "diff --git a/id_rsa b/id_rsa_backup",
		},
		{
			name: "submodule with sensitive name",
			input: `Submodule secret_module 0000000...1111111:
> Commit message in secret submodule`,
			redacted: true,
			contains: "Submodule secret_module 0000000...1111111:",
		},
		{
			name: "safe file diff remains unredacted",
			input: `diff --git a/main.go b/main.go
index 1234567..89abcde 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+func Hello() string { return "world" }`,
			redacted: false,
			contains: "func Hello() string",
		},
		{
			name: "mixed diff redacts only sensitive file",
			input: `diff --git a/main.go b/main.go
index 1234567..89abcde 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+func Safe() {}
diff --git a/.env b/.env
index 0000000..1234567 100644
--- /dev/null
+++ b/.env
@@ -0,0 +1,1 @@
+SECRET=123`,
			redacted: true,
			contains: "func Safe() {}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := filterDiffOutput(tc.input)
			if tc.redacted {
				if !strings.Contains(out, "[diff contents redacted: sensitive file in privacy denylist]") {
					t.Errorf("expected redacted notice, got: %s", out)
				}
				if strings.Contains(out, "secret_value_123") || strings.Contains(out, "supersecret") || strings.Contains(out, "ghp_secret") || strings.Contains(out, "SECRET=123") {
					t.Errorf("sensitive contents leaked in output: %s", out)
				}
			} else {
				if strings.Contains(out, "[diff contents redacted") {
					t.Errorf("expected no redaction for safe diff, got: %s", out)
				}
			}
			if !strings.Contains(out, tc.contains) {
				t.Errorf("expected output to contain %q, got: %s", tc.contains, out)
			}
		})
	}
}

func TestReadProcNetTCP_MasksPrivateIPs(t *testing.T) {
	tmpDir := t.TempDir()
	procFile := filepath.Join(tmpDir, "fake_tcp")

	// /proc/net/tcp format:
	// sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
	// 0100007F:1F90 is 127.0.0.1:8080 (loopback)
	// 00000000:0BB8 is 0.0.0.0:3000 (unspecified)
	// C0A80164:1F90 is 192.168.1.100:8080 (private IPv4 -> should be masked to 0.0.0.0)
	// 0500000A:1388 is 10.0.0.5:5000 (private IPv4 -> should be masked to 0.0.0.0)
	fakeData := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000000000000000 100 0 0 10 0
   2: C0A80164:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1 0000000000000000 100 0 0 10 0
   3: 0500000A:1388 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12348 1 0000000000000000 100 0 0 10 0
   4: 0100007F:0016 00000000:0000 01 00000000:00000000 00:00000000 00000000  1000        0 12349 1 0000000000000000 100 0 0 10 0
`
	if err := os.WriteFile(procFile, []byte(fakeData), 0644); err != nil {
		t.Fatalf("failed to write fake proc file: %v", err)
	}

	entries := readProcNetTCP(procFile)
	if len(entries) == 0 {
		t.Fatalf("expected entries from fake proc file, got none")
	}

	joined := strings.Join(entries, "\n")
	// Loopback and unspecified should be preserved
	if !strings.Contains(joined, "127.0.0.1:8080") {
		t.Errorf("expected 127.0.0.1:8080 to be preserved, got: %v", entries)
	}
	if !strings.Contains(joined, "0.0.0.0:3000") {
		t.Errorf("expected 0.0.0.0:3000 to be preserved, got: %v", entries)
	}
	// Private IPs (192.168.1.100, 10.0.0.5) must NOT appear
	if strings.Contains(joined, "192.168") {
		t.Errorf("private IP 192.168 leaked in output: %v", entries)
	}
	if strings.Contains(joined, "10.0.0.5") {
		t.Errorf("private IP 10.0.0.5 leaked in output: %v", entries)
	}
	// Private port 5000 should be preserved with 0.0.0.0
	if !strings.Contains(joined, "0.0.0.0:5000") {
		t.Errorf("expected 0.0.0.0:5000, got: %v", entries)
	}
}

func TestSanitizeListeningAddress(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"0.0.0.0:3000", "0.0.0.0:3000"},
		{"192.168.1.50:9000", "0.0.0.0:9000"},
		{"10.10.10.10:80", "0.0.0.0:80"},
		{"172.20.0.2:443", "0.0.0.0:443"},
		{"[::1]:8080", "[::1]:8080"},
		{"[::]:3000", "[::]:3000"},
	}

	for _, tc := range cases {
		got := sanitizeListeningAddress(tc.input)
		if got != tc.expected {
			t.Errorf("sanitizeListeningAddress(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}
