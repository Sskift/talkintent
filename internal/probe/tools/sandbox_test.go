package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateSandboxPath_Traversal(t *testing.T) {
	ws := t.TempDir()

	// 1. Direct path inside workspace
	validFile := filepath.Join(ws, "main.go")
	_ = os.WriteFile(validFile, []byte("package main"), 0644)

	res, err := ValidateSandboxPath(ws, "main.go")
	if err != nil {
		t.Fatalf("expected valid file inside workspace to pass, got: %v", err)
	}
	if !strings.HasSuffix(res, "main.go") {
		t.Errorf("unexpected resolved path: %s", res)
	}

	// 2. Traversal escaping workspace root
	_, err = ValidateSandboxPath(ws, "../outside.txt")
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("expected ErrSandboxViolation, got: %v", err)
	}

	// 3. Nested traversal
	_, err = ValidateSandboxPath(ws, "sub/../../escaping.txt")
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("expected ErrSandboxViolation for nested traversal, got: %v", err)
	}
}

func TestValidateSandboxPath_Symlinks(t *testing.T) {
	ws := t.TempDir()
	outsideDir := t.TempDir()

	// Secret file outside workspace
	outsideSecret := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideSecret, []byte("super_secret_token"), 0600); err != nil {
		t.Fatalf("failed to create outside secret file: %v", err)
	}

	// Symlink inside workspace pointing to outside secret
	linkPath := filepath.Join(ws, "innocent_link.txt")
	err := os.Symlink(outsideSecret, linkPath)
	if err != nil {
		// On Windows, non-admin accounts might not have symlink permissions
		t.Skipf("skipping symlink test due to OS permission limitation: %v", err)
	}

	// Validating innocent_link.txt must resolve physical path and reject escape
	_, err = ValidateSandboxPath(ws, "innocent_link.txt")
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("expected ErrSandboxViolation for symlink pointing outside workspace, got: %v", err)
	}

	// Symlink inside pointing to internal file should succeed
	internalTarget := filepath.Join(ws, "real_code.go")
	_ = os.WriteFile(internalTarget, []byte("package code"), 0644)
	internalLink := filepath.Join(ws, "code_link.go")
	if err := os.Symlink(internalTarget, internalLink); err == nil {
		resolved, err := ValidateSandboxPath(ws, "code_link.go")
		if err != nil {
			t.Errorf("expected internal symlink to succeed, got: %v", err)
		}
		if !strings.HasSuffix(resolved, "real_code.go") && !strings.HasSuffix(resolved, "code_link.go") {
			t.Errorf("unexpected resolved path: %s", resolved)
		}
	}
}

func TestIsFileDenylistedComprehensive(t *testing.T) {
	denylisted := []string{
		".env",
		".env.local",
		".env.production",
		"config/.env.test",
		".git/config",
		"repo/.git/config",
		"id_rsa",
		"id_rsa.pub",
		"id_ed25519",
		"certs/server.pem",
		"keys/private.key",
		"root.crt",
		"bundle.pfx",
		"cert.p12",
		"client_secret.json",
		"user_credentials.yaml",
		"access_token.txt",
		"db_password.ini",
		".aws/credentials",
		".aws/config",
		"home/.ssh/id_rsa",
		"home/.gnupg/secring.gpg",
		"cloud/.config/gcloud/credentials.db",
	}

	for _, path := range denylisted {
		if !IsFileDenylisted(path) {
			t.Errorf("expected %q to be denylisted", path)
		}
	}

	allowed := []string{
		"main.go",
		"internal/probe/probe.go",
		"README.md",
		"package.json",
		"go.mod",
		"go.sum",
		"src/components/button.tsx",
		"docs/PROTOCOL.md",
		"api/swagger.yaml",
	}

	for _, path := range allowed {
		if IsFileDenylisted(path) {
			t.Errorf("expected %q to be allowed, but it was denylisted", path)
		}
	}
}

func TestTruncateOutput(t *testing.T) {
	msg := "short output"
	if TruncateOutput(msg, 100) != msg {
		t.Errorf("expected short output to remain unchanged")
	}

	longMsg := strings.Repeat("X", 200)
	truncated := TruncateOutput(longMsg, 100)
	if len(truncated) <= 100 {
		t.Errorf("expected truncated output to contain truncation marker")
	}
	if !strings.HasSuffix(truncated, "\n[truncated by TalkIntent]") {
		t.Errorf("expected [truncated by TalkIntent] suffix, got: %s", truncated)
	}
}

func TestNormalizeVolumeWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("skipping Windows volume normalization test on non-windows platform")
	}

	norm1 := normalizeVolume(`c:\Users\test`)
	if !strings.HasPrefix(norm1, `C:\`) {
		t.Errorf("expected capitalized drive letter, got: %s", norm1)
	}

	norm2 := normalizeVolume(`\\?\C:\Users\test`)
	if strings.HasPrefix(norm2, `\\?\`) {
		t.Errorf("expected \\\\?\\ prefix stripped, got: %s", norm2)
	}
	if !strings.HasPrefix(norm2, `C:\`) {
		t.Errorf("expected capitalized drive letter after stripping \\\\?\\, got: %s", norm2)
	}
}
