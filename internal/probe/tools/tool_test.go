package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSandboxPath(t *testing.T) {
	tempDir := t.TempDir()
	subDir := filepath.Join(tempDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	// Valid subpath
	validPath, err := ValidateSandboxPath(tempDir, "sub")
	if err != nil {
		t.Fatalf("expected valid path, got err: %v", err)
	}
	canonicalSub, _ := filepath.EvalSymlinks(subDir)
	if validPath != canonicalSub {
		t.Errorf("expected %s, got %s", canonicalSub, validPath)
	}

	// Invalid path escaping root
	_, err = ValidateSandboxPath(tempDir, "../escaping")
	if err == nil {
		t.Errorf("expected sandbox violation, got nil error")
	}

	// Symlink pointing outside workspace root
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("secret"), 0600)
	symlinkPath := filepath.Join(subDir, "link_outside")
	if err := os.Symlink(outsideFile, symlinkPath); err == nil {
		_, err = ValidateSandboxPath(tempDir, filepath.Join("sub", "link_outside"))
		if err == nil {
			t.Errorf("expected sandbox violation for symlink pointing outside root, got nil error")
		}
	}
}

func TestIsFileDenylisted(t *testing.T) {
	deniedCases := []string{
		".env",
		".env.production",
		"id_rsa",
		"id_ed25519",
		"server.key",
		"client.pem",
		"api_secret.txt",
		"user_credentials.json",
		"github_token.txt",
		".git/config",
		"repo/.git/config",
		"sub/.git/HEAD",
		".ssh/config",
		"~/.ssh/id_rsa",
		".aws/credentials",
		".gnupg/secring.gpg",
		".config/gcloud/credentials.db",
	}

	for _, c := range deniedCases {
		if !IsFileDenylisted(c) {
			t.Errorf("expected file %q to be denylisted", c)
		}
	}

	allowedCases := []string{
		"main.go",
		"README.md",
		"index.html",
		"package.json",
		"Makefile",
		"types.go",
	}

	for _, c := range allowedCases {
		if IsFileDenylisted(c) {
			t.Errorf("expected file %q to be allowed", c)
		}
	}
}
